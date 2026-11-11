package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/anastasia/job-bot/internal/model"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func vac(id, company string) model.Vacancy {
	return model.Vacancy{Source: "hh", ExternalID: id, URL: "https://hh.ru/vacancy/" + id,
		Title: "Senior PHP", Company: company, PublishedAt: time.Now()}
}

func TestInsertDedup(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	ok, err := s.Insert(ctx, vac("1", "Acme"), model.StatusNew, "", "")
	if err != nil || !ok {
		t.Fatalf("first insert: ok=%v err=%v", ok, err)
	}
	ok, err = s.Insert(ctx, vac("1", "Acme"), model.StatusNew, "", "")
	if err != nil || ok {
		t.Fatalf("duplicate must be ignored: ok=%v err=%v", ok, err)
	}
}

func TestUnpark(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	s.Insert(ctx, vac("1", "Ozon Tech"), model.StatusParked, "", "")
	s.Insert(ctx, vac("2", "Avito"), model.StatusParked, "", "")

	n, err := s.Unpark(ctx, "ozon")
	if err != nil || n != 1 {
		t.Fatalf("unpark ozon: n=%d err=%v", n, err)
	}
	c, _ := s.Counts(ctx)
	if c[model.StatusNew] != 1 || c[model.StatusParked] != 1 {
		t.Fatalf("unexpected counts: %v", c)
	}
}

func TestPaused(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	if p, _ := s.Paused(ctx); p {
		t.Fatal("must not be paused by default")
	}
	s.SetPaused(ctx, true)
	if p, _ := s.Paused(ctx); !p {
		t.Fatal("must be paused")
	}
	s.SetPaused(ctx, false)
	if p, _ := s.Paused(ctx); p {
		t.Fatal("must be resumed")
	}
}

func TestListByStatus(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	s.Insert(ctx, vac("1", "Acme"), model.StatusNew, "ЗП не указана", "")
	rows, err := s.ListByStatus(ctx, model.StatusNew, 10)
	if err != nil || len(rows) != 1 || rows[0].Notes != "ЗП не указана" || rows[0].ID == 0 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if err := s.SetStatusByID(ctx, rows[0].ID, model.StatusSkipped); err != nil {
		t.Fatal(err)
	}
	if rows, _ = s.ListByStatus(ctx, model.StatusNew, 10); len(rows) != 0 {
		t.Fatalf("must be empty: %+v", rows)
	}
}

func TestCrossPostDedupe(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := model.Vacancy{Source: "tg:rabotaphp", ExternalID: "1", URL: "u1", Title: "Senior PHP Developer", Company: "t.me/rabotaphp",
		Description: "Senior PHP Developer. Symfony, Kafka! Удалённо. #remote", PublishedAt: time.Now()}
	b := a // тот же текст в другом канале, другая пунктуация и регистр
	b.Source, b.ExternalID, b.URL = "tg:php_rabota", "99", "u99"
	b.Description = "senior php developer symfony kafka удаленно remote"
	b.Description = "Senior PHP Developer. SYMFONY, Kafka?? Удалённо — #remote"

	if ok, err := s.Insert(ctx, a, model.StatusNew, "", ""); err != nil || !ok {
		t.Fatalf("first: ok=%v err=%v", ok, err)
	}
	if ok, err := s.Insert(ctx, b, model.StatusNew, "", ""); err != nil || ok {
		t.Fatalf("cross-post must be skipped: ok=%v err=%v", ok, err)
	}
	// вакансии из ATS с одинаковым текстом (разные локации) не склеиваем
	g1 := model.Vacancy{Source: "greenhouse:x", ExternalID: "1", URL: "g1", Title: "Eng", Company: "x", Description: "same", PublishedAt: time.Now()}
	g2 := g1
	g2.ExternalID, g2.URL = "2", "g2"
	if ok, _ := s.Insert(ctx, g1, model.StatusNew, "", ""); !ok {
		t.Fatal("g1")
	}
	if ok, _ := s.Insert(ctx, g2, model.StatusNew, "", ""); !ok {
		t.Fatal("ATS postings must not be deduped by content")
	}
}

func TestUnparkFindsCompanyInPostTextAndIgnoresPartialWords(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	// пост из Telegram: компания не выделена («t.me/канал»), Ozon назван в тексте
	tg := model.Vacancy{Source: "tg:rabotaphp", ExternalID: "1", URL: "u1", Title: "Senior PHP Developer",
		Company: "t.me/rabotaphp", Description: "Команда Ozon Tech ищет разработчика. PHP, Symfony", PublishedAt: time.Now()}
	devkit := model.Vacancy{Source: "habr", ExternalID: "2", URL: "u2", Title: "PHP Developer",
		Company: "DEVKIT Studio", Description: "PHP", PublishedAt: time.Now()}
	for _, v := range []model.Vacancy{tg, devkit} {
		if _, err := s.Insert(ctx, v, model.StatusParked, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Unpark(ctx, "ozon"); err != nil || n != 1 {
		t.Fatalf("Ozon is named in the post text: n=%d err=%v", n, err)
	}
	if n, err := s.Unpark(ctx, "VK"); err != nil || n != 0 {
		t.Fatalf("«VK» inside «DEVKIT» must not match: n=%d err=%v", n, err)
	}
	c, _ := s.Counts(ctx)
	if c[model.StatusNew] != 1 || c[model.StatusParked] != 1 {
		t.Fatalf("counts=%v", c)
	}
}
