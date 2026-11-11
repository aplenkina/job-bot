package filter

import (
	"strings"
	"testing"
	"time"

	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/model"
)

func newFilter(t *testing.T) *Filter {
	t.Helper()
	f, err := New(config.Profile{
		SalaryMinUSD:     3000,
		AllowUnknownPay:  true,
		Remote:           true,
		HybridCities:     []string{"Минск", "Dubai"},
		StopWords:        []string{"junior"},
		TitleRoles:       []string{"engineer", "developer", "разработчик"},
		ExcludeLocations: []string{"united states"},
		ParkedCompanies:  []string{"Ozon"},
		MaxAgeHours:      72,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.now = func() time.Time { return time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC) }
	return f
}

func base() model.Vacancy {
	return model.Vacancy{
		Title: "Senior PHP Developer", Company: "Acme", Description: "We use PHP and Symfony.",
		Location: "Remote", Remote: true, PublishedAt: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC),
	}
}

func TestCheck(t *testing.T) {
	f := newFilter(t)
	tests := []struct {
		name   string
		mod    func(*model.Vacancy)
		want   model.Status
		reason string
	}{
		{"подходит", func(v *model.Vacancy) {}, model.StatusNew, ""},
		{"junior в названии", func(v *model.Vacancy) { v.Title = "Junior PHP Developer" }, model.StatusFiltered, "stop_word:junior"},
		{"не та роль", func(v *model.Vacancy) { v.Title = "Sales Manager PHP" }, model.StatusFiltered, "role"},
		{"нет технологии", func(v *model.Vacancy) { v.Description = "Java and Kotlin"; v.Title = "Java Developer" }, model.StatusFiltered, "tech"},
		{"Go как язык", func(v *model.Vacancy) { v.Title = "Backend Engineer"; v.Description = "Stack: Go, Kafka" }, model.StatusNew, ""},
		{"Go-To-Market не язык", func(v *model.Vacancy) { v.Title = "Systems Engineer - Go-To-Market"; v.Description = "sales ops" }, model.StatusFiltered, "tech"},
		{"устарела", func(v *model.Vacancy) { v.PublishedAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }, model.StatusFiltered, "stale"},
		{"только США", func(v *model.Vacancy) { v.Location = "Remote - United States" }, model.StatusFiltered, "location:united states"},
		{"офис в другом городе", func(v *model.Vacancy) { v.Remote = false; v.Location = "Berlin" }, model.StatusFiltered, "location"},
		{"гибрид в Минске", func(v *model.Vacancy) { v.Remote = false; v.Location = "Минск" }, model.StatusNew, ""},
		{"зарплата ниже порога", func(v *model.Vacancy) { v.SalaryTo = 2000; v.Currency = "USD" }, model.StatusFiltered, "salary"},
		{"зарплата в рублях проходит", func(v *model.Vacancy) { v.SalaryTo = 400000; v.Currency = "RUR" }, model.StatusNew, ""},
		{"отложенная компания", func(v *model.Vacancy) { v.Company = "Ozon Tech" }, model.StatusParked, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := base()
			tt.mod(&v)
			got := f.Check(v)
			if got.Status != tt.want || got.Reason != tt.reason {
				t.Fatalf("got %+v, want status=%s reason=%q", got, tt.want, tt.reason)
			}
		})
	}
}

func TestUnknownSalaryDisallowed(t *testing.T) {
	f := newFilter(t)
	f.p.AllowUnknownPay = false
	if got := f.Check(base()); got.Reason != "salary" {
		t.Fatalf("got %+v", got)
	}
}

func TestParkedInTelegramPost(t *testing.T) {
	f := newFilter(t)
	f.p.ParkedCompanies = []string{"Ozon", "VK"}
	mk := func(desc string) model.Vacancy {
		v := base()
		v.Source, v.Company, v.Description = "tg:rabotaphp", "t.me/rabotaphp", desc
		return v
	}
	if got := f.Check(mk("PHP, Symfony. Команда Ozon ищет разработчика")); got.Status != model.StatusParked {
		t.Fatalf("Ozon in post must be parked: %+v", got)
	}
	if got := f.Check(mk("PHP. Работа в VK, удалённо")); got.Status != model.StatusParked {
		t.Fatalf("VK in post must be parked: %+v", got)
	}
	if got := f.Check(mk("PHP. Avkus Group, удалённо")); got.Status != model.StatusNew {
		t.Fatalf("VK inside a word must not park: %+v", got)
	}
}

func TestTrustedMailVacancies(t *testing.T) {
	f := newFilter(t)
	f.p.ParkedCompanies = []string{"Ozon"}
	mail := func(mod func(*model.Vacancy)) model.Vacancy {
		v := model.Vacancy{Source: "mail:hh.ru", Title: "Руководитель отдела", Company: "Acme", Description: "Acme\nМосква",
			Location: "", PublishedAt: time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)}
		if mod != nil {
			mod(&v)
		}
		return v
	}
	// обычная вакансия с таким названием и текстом отсеялась бы по роли и стеку, но письмо уже отобрано сайтом
	got := f.Check(mail(nil))
	if got.Status != model.StatusNew || !strings.Contains(strings.Join(got.Notes, ";"), "проверьте формат работы") {
		t.Fatalf("trusted mail vacancy must pass role/tech/location checks: %+v", got)
	}
	// остальные проверки действуют
	if got := f.Check(mail(func(v *model.Vacancy) { v.Title = "Junior PHP разработчик" })); got.Reason != "stop_word:junior" {
		t.Errorf("stop words still apply: %+v", got)
	}
	if got := f.Check(mail(func(v *model.Vacancy) { v.PublishedAt = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) })); got.Reason != "stale" {
		t.Errorf("stale still applies: %+v", got)
	}
	if got := f.Check(mail(func(v *model.Vacancy) { v.SalaryTo, v.Currency = 1000, "USD" })); got.Reason != "salary" {
		t.Errorf("salary still applies: %+v", got)
	}
	if got := f.Check(mail(func(v *model.Vacancy) { v.Description = "Ozon Tech\nМосква" })); got.Status != model.StatusParked {
		t.Errorf("parked company in an email must be parked: %+v", got)
	}
	if got := f.Check(mail(func(v *model.Vacancy) { v.Description = "Avkusozon group" })); got.Status != model.StatusNew {
		t.Errorf("company name inside a word must not park: %+v", got)
	}
}

func TestParkedAreKeptEvenWhenOld(t *testing.T) {
	f := newFilter(t)
	old := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) // старше max_age_hours
	v := base()
	v.PublishedAt, v.Company = old, "Ozon Tech"
	if got := f.Check(v); got.Status != model.StatusParked {
		t.Fatalf("an old vacancy of a parked company must be stored as parked, got %+v", got)
	}
	// остальные компании по-прежнему отсекаются по возрасту
	v.Company = "Acme"
	if got := f.Check(v); got.Reason != "stale" {
		t.Fatalf("old vacancy of a normal company must stay stale, got %+v", got)
	}
	// отложенная компания, но не подходит по роли — всё равно отсев
	v.Company, v.Title = "Ozon Tech", "Sales Manager"
	if got := f.Check(v); got.Status != model.StatusFiltered {
		t.Fatalf("parked vacancies still go through the other filters, got %+v", got)
	}
}
