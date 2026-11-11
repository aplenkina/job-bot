package bot

import (
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/anastasia/job-bot/internal/store"
)

// Карточка вакансии в HTML-разметке Telegram: заголовок — ссылка на вакансию,
// пункты — спокойные точки, важное выделено жирным. Единственный цветной значок —
// кружок релевантности перед заголовком: 🟢 хорошо подходит, 🟡 подходит частично, 🟠 слабее.

var reNoteSplit = regexp.MustCompile(`[.;]\s+`)

const bullet = "•"

// FormatVacancy собирает текст карточки. Весь пользовательский текст экранируется.
func FormatVacancy(r store.Row) string {
	var lines []string
	item := func(format string, a ...any) { lines = append(lines, bullet+" "+fmt.Sprintf(format, a...)) }

	title := html.EscapeString(r.Title)
	head := "<b>" + title + "</b>"
	if r.URL != "" {
		head = fmt.Sprintf(`<b><a href="%s">%s</a></b>`, html.EscapeString(r.URL), title)
	}
	if dot := relevanceDot(r.Score); dot != "" {
		head = dot + " " + head
	}
	lines = append(lines, head)

	var who []string
	if strings.HasPrefix(r.Company, "t.me/") {
		who = append(who, "@"+html.EscapeString(strings.TrimPrefix(r.Company, "t.me/")))
	} else if r.Company != "" {
		who = append(who, html.EscapeString(r.Company))
	}
	if loc := truncate(r.Location, 120); loc != "" {
		who = append(who, html.EscapeString(loc))
	}
	if len(who) > 0 {
		lines = append(lines, strings.Join(who, " · "))
	}
	lines = append(lines, "")

	if r.Remote {
		item("<b>Удалёнка</b>")
	} else {
		item("<b>Офис / гибрид</b>")
	}

	usd, otherNotes := splitNotes(r.Notes)
	if sal := formatSalary(r); sal != "" {
		if usd != "" {
			sal += " (" + usd + ")"
		}
		item("ЗП: <b>%s</b>", html.EscapeString(sal))
	} else if usd != "" {
		item("ЗП: <b>%s</b>", html.EscapeString(usd))
	} else {
		item("ЗП не указана")
	}

	if r.Score > 0 {
		item("Оценка: <b>%d/100</b>", r.Score)
	}
	for _, part := range reNoteSplit.Split(strings.TrimSpace(r.Note), -1) {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case strings.HasPrefix(part, "Совпало:"):
			item("%s", html.EscapeString(part))
		default:
			item("<i>%s</i>", html.EscapeString(part))
		}
	}
	for _, n := range otherNotes {
		item("<i>%s</i>", html.EscapeString(n))
	}

	var foot []string
	if !r.PublishedAt.IsZero() && r.PublishedAt.Unix() > 0 {
		foot = append(foot, r.PublishedAt.Local().Format("02.01 15:04"))
	}
	foot = append(foot, html.EscapeString(r.Source))
	lines = append(lines, "", "<i>"+strings.Join(foot, " · ")+"</i>")

	return strings.Join(lines, "\n")
}

// FormatLetter оформляет черновик письма блоком: в Telegram по нажатию на такой блок текст копируется.
func FormatLetter(letter string) string {
	return "<b>Черновик письма</b> — нажмите на текст, чтобы скопировать\n\n<pre>" +
		html.EscapeString(truncate(letter, 3500)) + "</pre>"
}

// splitNotes отделяет пометку о пересчёте зарплаты («до ~$4000/мес») от остальных пометок фильтров.
// Пометку «ЗП не указана» не возвращает: она показывается отдельной строкой по полям вакансии.
func splitNotes(notes string) (usd string, other []string) {
	for _, n := range strings.Split(notes, "; ") {
		n = strings.TrimSpace(n)
		switch {
		case n == "" || n == "ЗП не указана":
		case strings.HasPrefix(n, "до ~$"):
			usd = n
		default:
			other = append(other, n)
		}
	}
	return usd, other
}

func formatSalary(r store.Row) string {
	switch {
	case r.SalaryFrom > 0 && r.SalaryTo > 0:
		return fmt.Sprintf("%s–%s %s/мес", spaced(r.SalaryFrom), spaced(r.SalaryTo), r.Currency)
	case r.SalaryFrom > 0:
		return fmt.Sprintf("от %s %s/мес", spaced(r.SalaryFrom), r.Currency)
	case r.SalaryTo > 0:
		return fmt.Sprintf("до %s %s/мес", spaced(r.SalaryTo), r.Currency)
	}
	return ""
}

// spaced: 280000 → «280 000» (неразрывный пробел, чтобы число не рвалось на строках).
func spaced(n int) string {
	s := fmt.Sprint(n)
	var out []string
	for len(s) > 3 {
		out = append([]string{s[len(s)-3:]}, out...)
		s = s[:len(s)-3]
	}
	out = append([]string{s}, out...)
	return strings.Join(out, "\u00a0")
}

// Пороги релевантности (оценка 0–100): совпадают с тем, как оценка считается в match.RuleAssessor.
const (
	scoreGreen  = 70
	scoreYellow = 50
)

// relevanceDot возвращает кружок по оценке; без оценки значка нет.
func relevanceDot(score int) string {
	switch {
	case score <= 0:
		return ""
	case score >= scoreGreen:
		return "🟢"
	case score >= scoreYellow:
		return "🟡"
	default:
		return "🟠"
	}
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
