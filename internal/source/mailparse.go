package source

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"

	"github.com/anastasia/job-bot/internal/model"
)

// Разбор писем с рассылками вакансий. Письма у каждого сайта свои, поэтому логика общая:
// находим ссылки на вакансии по шаблону отправителя, а название, компанию, зарплату и формат
// берём из текста вокруг ссылки. Эвристики придётся уточнять по реальным письмам.

// MailSender описывает отправителя рассылки.
type MailSender struct {
	Domain string         // «hh.ru»: ищем письма, где From содержит эту строку
	Link   *regexp.Regexp // ссылка на вакансию; первая группа — идентификатор вакансии
	URL    string         // шаблон канонической ссылки, %s — идентификатор; пусто — как в письме
}

// DefaultMailSenders — сайты, для которых шаблон ссылки известен из публичных адресов вакансий.
func DefaultMailSenders() []MailSender {
	return []MailSender{
		{Domain: "hh.ru", Link: regexp.MustCompile(`https?://(?:[a-z0-9-]+\.)?hh\.(?:ru|kz|by|uz)/vacancy/(\d+)`), URL: "https://hh.ru/vacancy/%s"},
		{Domain: "geekjob.ru", Link: regexp.MustCompile(`https?://geekjob\.ru/vacancy/([a-z0-9]+)`), URL: "https://geekjob.ru/vacancy/%s"},
	}
}

var (
	genericLinkText = regexp.MustCompile(`(?i)^(откликнуться|посмотреть|подробнее|перейти|открыть|смотреть|читать|view|apply|details|more|подробности|ещё|еще)\b`)
	reSalaryLine    = regexp.MustCompile(`(?i)(?:\d[\d \x{00A0}\x{202F}.,]*\s*(?:₽|руб|rub|rur|\$|usd|€|eur|₸|kzt|byn))|(?:от|до)\s+\d`)
	reSpaces        = regexp.MustCompile(`[ \t\r\f\v\x{00A0}]+`)
)

// vacancyLink — найденная в письме ссылка на вакансию.
type vacancyLink struct {
	id      string
	title   string
	node    *html.Node
	textLen int
}

// parseMailHTML достаёт вакансии из HTML-письма. sender задаёт шаблон ссылки.
func parseMailHTML(body string, sender MailSender) []model.Vacancy {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}
	// 1) все ссылки на вакансии, сгруппированные по идентификатору
	best := map[string]*vacancyLink{}
	var order []string
	walk(doc, func(n *html.Node) bool {
		if n.Type != html.ElementNode || n.Data != "a" {
			return true
		}
		m := sender.Link.FindStringSubmatch(attr(n, "href"))
		if m == nil || len(m) < 2 {
			return true
		}
		id := m[1]
		text := strings.TrimSpace(reSpaces.ReplaceAllString(nodeText(n), " "))
		cur, ok := best[id]
		if !ok {
			cur = &vacancyLink{id: id, node: n}
			best[id] = cur
			order = append(order, id)
		}
		// названием считаем самый длинный осмысленный текст ссылки (кнопки «Откликнуться» не подходят)
		if text != "" && !genericLinkText.MatchString(text) && len([]rune(text)) > cur.textLen {
			cur.title, cur.textLen, cur.node = text, len([]rune(text)), n
		}
		return true
	})

	// 2) для каждой вакансии — текст блока вокруг ссылки
	var out []model.Vacancy
	for _, id := range order {
		l := best[id]
		if l.title == "" {
			continue // у ссылки нет названия — не вакансия (логотип, футер)
		}
		block := blockLines(l.node, id, sender)
		v := model.Vacancy{
			Source:     "mail:" + sender.Domain,
			ExternalID: id,
			URL:        canonicalURL(sender, id),
			Title:      l.title,
			Company:    guessCompany(block, l.title),
			Language:   detectLang(strings.Join(block, " ")),
		}
		v.Description = strings.Join(block, "\n")
		v.SalaryFrom, v.SalaryTo, v.Currency = parseSalary(v.Description)
		v.Remote = reRemote.MatchString(v.Description) && !reNotOffice.MatchString(v.Description)
		v.Location = guessLocation(block, v.Company, l.title)
		out = append(out, v)
	}
	return out
}

func canonicalURL(s MailSender, id string) string {
	if s.URL == "" {
		return id
	}
	return strings.Replace(s.URL, "%s", id, 1)
}

// blockLines поднимается от ссылки вверх, пока блок содержит только эту вакансию и не слишком велик,
// и возвращает непустые строки текста блока.
func blockLines(a *html.Node, id string, s MailSender) []string {
	best := a
	for p := a.Parent; p != nil; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		if p.Data == "body" || p.Data == "html" {
			break
		}
		if countOtherVacancies(p, id, s) > 0 || len([]rune(blockText(p))) > 700 {
			break
		}
		best = p
	}
	var lines []string
	for _, l := range strings.Split(blockText(best), "\n") {
		l = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
		if l != "" && !genericLinkText.MatchString(l) {
			lines = append(lines, l)
		}
	}
	return lines
}

func countOtherVacancies(n *html.Node, id string, s MailSender) int {
	cnt := 0
	walk(n, func(c *html.Node) bool {
		if c.Type == html.ElementNode && c.Data == "a" {
			if m := s.Link.FindStringSubmatch(attr(c, "href")); m != nil && len(m) > 1 && m[1] != id {
				cnt++
			}
		}
		return true
	})
	return cnt
}

// guessCompany: строка после названия, которая не похожа на зарплату и не слишком длинная.
func guessCompany(lines []string, title string) string {
	seenTitle := false
	for _, l := range lines {
		if !seenTitle {
			if strings.Contains(l, title) || strings.Contains(title, l) {
				seenTitle = true
			}
			continue
		}
		if len([]rune(l)) <= 80 && !reSalaryLine.MatchString(l) {
			return l
		}
	}
	return ""
}

// guessLocation: строка с городом или форматом работы; пусто, если не нашлось.
func guessLocation(lines []string, company, title string) string {
	for _, l := range lines {
		if l == company || strings.Contains(l, title) || reSalaryLine.MatchString(l) {
			continue
		}
		if reFormat.MatchString(l) || len([]rune(l)) <= 40 {
			return truncateRunes(l, 120)
		}
	}
	return ""
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ---------- простой разбор текстового письма ----------

var reURL = regexp.MustCompile(`https?://[^\s<>"')\]]+`)

// parseMailText: для писем без HTML — ссылки на вакансии из текста, название берём из предыдущей строки.
func parseMailText(body string, sender MailSender) []model.Vacancy {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	seen := map[string]bool{}
	var out []model.Vacancy
	for i, l := range lines {
		m := sender.Link.FindStringSubmatch(l)
		if m == nil || len(m) < 2 || seen[m[1]] {
			continue
		}
		title := ""
		for j := i - 1; j >= 0 && j >= i-3; j-- { // ближайшая непустая строка выше ссылки
			if t := strings.TrimSpace(lines[j]); t != "" && !reURL.MatchString(t) {
				title = t
				break
			}
		}
		if title == "" {
			continue
		}
		seen[m[1]] = true
		out = append(out, model.Vacancy{
			Source: "mail:" + sender.Domain, ExternalID: m[1], URL: canonicalURL(sender, m[1]),
			Title: title, Company: "", Language: detectLang(title),
		})
	}
	return out
}

// blockText — текст узла, где блочные элементы и ячейки таблиц разделены переводами строк
// (в письмах вёрстка таблицами, и обычное склеивание текста слило бы название с компанией).
func blockText(n *html.Node) string {
	var sb strings.Builder
	var rec func(*html.Node)
	rec = func(c *html.Node) {
		switch c.Type {
		case html.TextNode:
			sb.WriteString(c.Data)
			return
		case html.ElementNode:
			switch c.Data {
			case "script", "style", "head":
				return
			case "br":
				sb.WriteString("\n")
			case "p", "div", "tr", "td", "th", "li", "ul", "ol", "table", "h1", "h2", "h3", "h4", "h5", "h6", "section":
				sb.WriteString("\n")
				defer sb.WriteString("\n")
			}
		}
		for ch := c.FirstChild; ch != nil; ch = ch.NextSibling {
			rec(ch)
		}
	}
	rec(n)
	return sb.String()
}
