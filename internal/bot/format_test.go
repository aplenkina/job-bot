package bot

import (
	"strings"
	"testing"
	"time"

	"github.com/anastasia/job-bot/internal/model"
	"github.com/anastasia/job-bot/internal/store"
)

func row() store.Row {
	return store.Row{
		Score: 82, Note: "Совпало: symfony, kafka. Go — ваш опыт около года",
		Notes: "до ~$4000/мес",
		Vacancy: model.Vacancy{
			Source: "habr", URL: "https://career.habr.com/vacancies/1", Title: "Senior PHP Developer",
			Company: "Acme", Location: "Москва", Remote: true,
			SalaryFrom: 280000, SalaryTo: 320000, Currency: "RUR",
			PublishedAt: time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC),
		},
	}
}

func TestFormatVacancyCard(t *testing.T) {
	got := FormatVacancy(row())
	for _, want := range []string{
		`<b><a href="https://career.habr.com/vacancies/1">Senior PHP Developer</a></b>`,
		"Acme · Москва", "• <b>Удалёнка</b>",
		"• ЗП: <b>280\u00a0000–320\u00a0000 RUR/мес (до ~$4000/мес)</b>",
		"• Оценка: <b>82/100</b>", "• Совпало: symfony, kafka", "• <i>Go — ваш опыт около года</i>", "<i>10.11 ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
}

func TestOnlyRelevanceDots(t *testing.T) {
	for _, r := range []store.Row{row(), func() store.Row { r := row(); r.Remote, r.Company = false, "t.me/x"; return r }()} {
		out := FormatVacancy(r) + FormatLetter("текст")
		for _, loud := range []string{"🔴", "⚪", "✅", "⚠️", "💰", "🏢", "📍", "📡", "🎯", "✉️", "ℹ️", "🕒", "📢"} {
			if strings.Contains(out, loud) {
				t.Errorf("unexpected emoji %q in:\n%s", loud, out)
			}
		}
	}
}

func TestRelevanceDot(t *testing.T) {
	cases := []struct {
		score int
		dot   string
	}{{95, "🟢"}, {70, "🟢"}, {69, "🟡"}, {50, "🟡"}, {49, "🟠"}, {36, "🟠"}, {1, "🟠"}, {0, ""}}
	for _, c := range cases {
		r := row()
		r.Score = c.score
		first := strings.SplitN(FormatVacancy(r), "\n", 2)[0]
		if c.dot == "" {
			if strings.ContainsAny(first, "🟢🟡🟠") || !strings.HasPrefix(first, "<b>") {
				t.Errorf("score %d: no dot expected, got %q", c.score, first)
			}
			continue
		}
		if !strings.HasPrefix(first, c.dot+" <b><a href=") {
			t.Errorf("score %d: want %s before the title, got %q", c.score, c.dot, first)
		}
	}
	// дублей значка в остальной карточке нет: он один, перед заголовком
	if n := strings.Count(FormatVacancy(row()), "🟢"); n != 1 {
		t.Errorf("dot must appear once, got %d", n)
	}
}

func TestFormatVacancyEscapesHTML(t *testing.T) {
	r := row()
	r.Title = `PHP <script>alert(1)</script> & "co"`
	r.Company = "A&B <b>"
	r.URL = `https://x.test/?a=1&b="2"`
	got := FormatVacancy(r)
	for _, bad := range []string{"<script>", "<b>A&B", `b="2"`} {
		if strings.Contains(got, bad) {
			t.Errorf("unescaped %q in:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "A&amp;B") {
		t.Errorf("escaping missing:\n%s", got)
	}
}

func TestFormatVacancyVariants(t *testing.T) {
	r := row()
	r.Remote, r.SalaryFrom, r.SalaryTo, r.Currency, r.Notes = false, 0, 0, "", "ЗП не указана; офис/гибрид: Москва"
	r.Company, r.Source, r.Score = "t.me/rabotaphp", "tg:rabotaphp", 40
	got := FormatVacancy(r)
	for _, want := range []string{"• <b>Офис / гибрид</b>", "• ЗП не указана", "@rabotaphp", "• Оценка: <b>40/100</b>", "• <i>офис/гибрид: Москва</i>"} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "ЗП не указана") != 1 {
		t.Errorf("unexpected duplicates:\n%s", got)
	}
	// только нижняя граница и только верхняя
	r.SalaryFrom, r.Currency = 3000, "USD"
	if !strings.Contains(FormatVacancy(r), "от 3\u00a0000 USD/мес") {
		t.Error("from-only salary")
	}
	r.SalaryFrom, r.SalaryTo = 0, 5000
	if !strings.Contains(FormatVacancy(r), "до 5\u00a0000 USD/мес") {
		t.Error("to-only salary")
	}
}

func TestFormatLetter(t *testing.T) {
	got := FormatLetter("Здравствуйте!\n\nТекст <письма> & всё")
	if !strings.Contains(got, "<pre>Здравствуйте!") || !strings.HasSuffix(got, "</pre>") || !strings.Contains(got, "&lt;письма&gt; &amp;") {
		t.Errorf("unexpected letter block:\n%s", got)
	}
	if len([]rune(FormatLetter(strings.Repeat("я", 10000)))) > 4096 {
		t.Error("letter message must fit Telegram's 4096-character limit")
	}
}
