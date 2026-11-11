package source

import (
	"testing"

	"github.com/anastasia/job-bot/internal/config"
)

func TestParseSalary(t *testing.T) {
	tests := []struct {
		in       string
		from, to int
		cur      string
	}{
		{"ЗП: от 280 000 до 300 000 ₽", 280000, 300000, "RUR"},
		{"от 245 000 до 260 000 ₽", 245000, 260000, "RUR"},
		{"$3000-5000", 3000, 5000, "USD"},
		{"3-5k$", 3000, 5000, "USD"},
		{"300к руб на руки", 300000, 0, "RUR"},
		{"до 5000$", 0, 5000, "USD"},
		{"3500 USD", 3500, 0, "USD"},
		{"Middle · Hybrid · от  ₽180 000", 180000, 0, "RUR"},
		{"60000 EUR в год", 5000, 0, "EUR"},
		{"Опыт 5 лет, стек PHP", 0, 0, ""},
		{"Полная занятость, график 5/2", 0, 0, ""},
	}
	for _, tt := range tests {
		f, to, cur := parseSalary(tt.in)
		if f != tt.from || to != tt.to || cur != tt.cur {
			t.Errorf("%q: got %d,%d,%q want %d,%d,%q", tt.in, f, to, cur, tt.from, tt.to, tt.cur)
		}
	}
}

func TestParsePost(t *testing.T) {
	t.Run("rabotaphp: гибрид в Москве", func(t *testing.T) {
		title, _, loc, remote, ok := parsePost("Senior Fullstack-разработчик\n\nЗП: от 280 000 до 300 000 ₽\nФормат: офис/гибрид (Москва)\nПолная занятость\n\n#office #hybrid #fulltime", "rabotaphp")
		if !ok || title != "Senior Fullstack-разработчик" || remote || loc != "Формат: офис/гибрид (Москва)" {
			t.Fatalf("title=%q loc=%q remote=%v ok=%v", title, loc, remote, ok)
		}
	})
	t.Run("GolangJobsit: компания над названием", func(t *testing.T) {
		title, company, _, remote, ok := parsePost("#senior #300k #удаленка\n\nData Light\nGo-разработчик (Middle/Senior)\nот 280 000 до 320 000 ₽\nФормат работы: удалённо", "GolangJobsit")
		if !ok || title != "Go-разработчик (Middle/Senior)" || company != "Data Light" || !remote {
			t.Fatalf("title=%q company=%q remote=%v ok=%v", title, company, remote, ok)
		}
	})
	t.Run("Компания: явно", func(t *testing.T) {
		_, company, _, remote, _ := parsePost("Программист Bitrix24\n\nКомпания: ЕАЕ-Консалт\nФормат: удалённо\n#remote", "rabotaphp")
		if company != "ЕАЕ-Консалт" || !remote {
			t.Fatalf("company=%q remote=%v", company, remote)
		}
	})
	t.Run("аллокации — не локация", func(t *testing.T) {
		_, _, loc, _, _ := parsePost("Go-разработчик\nФормат работы: удалённо\nРабота с аллокациями памяти", "x")
		if loc != "Формат работы: удалённо" {
			t.Fatalf("loc=%q", loc)
		}
	})
	t.Run("подборка — не вакансия", func(t *testing.T) {
		if _, _, _, _, ok := parsePost("✔️ Вакансии по PHP за неделю:\n1) PHP Developer", "php_rabota"); ok {
			t.Fatal("digest must be skipped")
		}
	})
}

const samplePage = `<html><body>
<div class="tgme_widget_message_wrap"><div class="tgme_widget_message text_not_supported_wrap js-widget_message" data-post="rabotaphp/724">
 <div class="tgme_widget_message_text js-message_text" dir="auto">Senior PHP-разработчик<br/><br/>ЗП: $3500-4500<br/>Формат: удалённо<br/>#remote</div>
 <div class="tgme_widget_message_footer"><a class="tgme_widget_message_date" href="https://t.me/rabotaphp/724"><time datetime="2026-11-10T10:00:00+00:00" class="time">10:00</time></a></div>
</div></div>
<div class="tgme_widget_message_wrap"><div class="tgme_widget_message" data-post="rabotaphp/725">
 <div class="tgme_widget_message_text">Реклама без вакансий</div>
 <time datetime="2026-11-10T11:00:00+00:00"></time>
</div></div>
<div class="tgme_widget_message_wrap"><div class="tgme_widget_message" data-post="rabotaphp/726"><div class="tgme_widget_message_photo"></div></div></div>
</body></html>`

func TestParseChannelPage(t *testing.T) {
	vs, posts, err := parseChannelPage(samplePage, "rabotaphp")
	if err != nil {
		t.Fatal(err)
	}
	if posts != 3 {
		t.Fatalf("posts=%d, want 3 (включая пост без текста)", posts)
	}
	// пост 725 разбирается как «вакансия» с заголовком-рекламой — отсеет фильтр по роли и стеку дальше;
	// пост без текста (726) пропускается.
	if len(vs) != 2 {
		t.Fatalf("got %d vacancies: %+v", len(vs), vs)
	}
	v := vs[0]
	if v.ExternalID != "724" || v.URL != "https://t.me/rabotaphp/724" || v.Title != "Senior PHP-разработчик" ||
		!v.Remote || v.SalaryFrom != 3500 || v.SalaryTo != 4500 || v.Currency != "USD" || v.PublishedAt.IsZero() {
		t.Fatalf("unexpected: %+v", v)
	}
}

func TestParseChannelPageWithoutPosts(t *testing.T) {
	// так выглядит ответ для несуществующего или переименованного канала: страница есть, постов нет
	vs, posts, err := parseChannelPage("<html><body><div class=\"tgme_page\">Telegram: Contact @nochannel</div></body></html>", "nochannel")
	if err != nil || posts != 0 || len(vs) != 0 {
		t.Fatalf("vs=%v posts=%d err=%v", vs, posts, err)
	}
}

func TestBuildSplitsTelegramChannels(t *testing.T) {
	srcs := Build(config.Sources{Telegram: []string{"rabotaphp", "@php_rabota"}})
	if len(srcs) != 2 || srcs[0].Name() != "telegram:rabotaphp" || srcs[1].Name() != "telegram:php_rabota" {
		var names []string
		for _, s := range srcs {
			names = append(names, s.Name())
		}
		t.Fatalf("each channel must be its own source: %v", names)
	}
}
