package source

import (
	"regexp"
	"strconv"
	"strings"
)

// Разбор текста вакансии из Telegram-поста: зарплата, формат работы, название, компания.

const (
	numPat = `\d{1,3}(?:[ \x{00A0}\x{202F}]\d{3})+|\d+(?:[.,]\d+)?`
	sfxPat = `(?:[kкKК]|тыс\.?)?`
	curPat = `\$|usd|€|eur|₽|руб\.?|rub|rur|₸|kzt|тенге|byn`
	sepPat = `\s*(?:-|–|—|до|to)\s*`
)

var (
	// 280 000 – 300 000 ₽, 3000-5000 USD, 300к руб
	reSalaryAfter = regexp.MustCompile(`(?i)(` + numPat + `)(` + sfxPat + `)(?:` + sepPat + `(` + numPat + `)(` + sfxPat + `))?\s*(` + curPat + `)`)
	// $3000-5000, ₽180 000
	reSalaryBefore = regexp.MustCompile(`(?i)(` + curPat + `)\s*(` + numPat + `)(` + sfxPat + `)(?:` + sepPat + `(` + numPat + `)(` + sfxPat + `))?`)
	reYear         = regexp.MustCompile(`(?i)в год|per year|annual|/year|/yr|годов`)
	reUpTo         = regexp.MustCompile(`(?i)(?:до|up to|max)\s*$`)
)

func currencyCode(s string) string {
	switch strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), ".")) {
	case "$", "usd":
		return "USD"
	case "€", "eur":
		return "EUR"
	case "₽", "руб", "rub", "rur":
		return "RUR"
	case "₸", "kzt", "тенге":
		return "KZT"
	case "byn":
		return "BYN"
	}
	return ""
}

func parseAmount(num, sfx string) float64 {
	num = strings.NewReplacer(" ", "", " ", "", " ", "").Replace(num)
	num = strings.Replace(num, ",", ".", 1)
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	if sfx != "" {
		v *= 1000
	}
	return v
}

// parseSalary ищет первую правдоподобную зарплатную вилку. Результат — в месяц.
// Если указано только «до N», заполняется to; если «от N» или просто N — from.
func parseSalary(text string) (from, to int, currency string) {
	type cand struct {
		a, b     float64
		cur      string
		start    int
		end      int
		aK, bK   bool
		hasRange bool
	}
	var cands []cand
	for _, m := range reSalaryAfter.FindAllStringSubmatchIndex(text, -1) {
		g := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return text[m[2*i]:m[2*i+1]]
		}
		c := cand{a: parseAmount(g(1), g(2)), aK: g(2) != "", cur: currencyCode(g(5)), start: m[0], end: m[1]}
		if g(3) != "" {
			c.b, c.bK, c.hasRange = parseAmount(g(3), g(4)), g(4) != "", true
		}
		cands = append(cands, c)
	}
	for _, m := range reSalaryBefore.FindAllStringSubmatchIndex(text, -1) {
		g := func(i int) string {
			if m[2*i] < 0 {
				return ""
			}
			return text[m[2*i]:m[2*i+1]]
		}
		c := cand{a: parseAmount(g(2), g(3)), aK: g(3) != "", cur: currencyCode(g(1)), start: m[0], end: m[1]}
		if g(4) != "" {
			c.b, c.bK, c.hasRange = parseAmount(g(4), g(5)), g(5) != "", true
		}
		cands = append(cands, c)
	}
	for _, c := range cands {
		if c.cur == "" {
			continue
		}
		// «3-5k$»: суффикс k относится к обоим числам
		if c.hasRange && c.bK && !c.aK && c.a < 1000 {
			c.a *= 1000
		}
		if c.a < 100 && (!c.hasRange || c.b < 100) { // слишком мало для месячной зарплаты: «5 лет $»
			continue
		}
		tail := text[c.end:min(len(text), c.end+30)]
		perYear := reYear.MatchString(tail)
		conv := func(v float64) int {
			if perYear {
				v /= 12
			}
			return int(v)
		}
		if c.hasRange {
			lo, hi := c.a, c.b
			if lo > hi {
				lo, hi = hi, lo
			}
			return conv(lo), conv(hi), c.cur
		}
		if reUpTo.MatchString(text[max(0, c.start-8):c.start]) {
			return 0, conv(c.a), c.cur
		}
		return conv(c.a), 0, c.cur
	}
	return 0, 0, ""
}

var (
	reHashLine  = regexp.MustCompile(`^(#\S+\s*)+$`)
	reTitleLike = regexp.MustCompile(`(?i)разработчик|developer|engineer|программист|backend|бэкенд|бекенд|golang|\bgo\b|php|architect|архитектор|tech lead|тимлид|team lead`)
	reCompany   = regexp.MustCompile(`(?i)^(?:🏢\s*)?(?:компания|company)\s*[:\-—]\s*(.+)$`)
	reFormat    = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:формат|локаци|город|location|офис|удал[её]н|remote|гибрид|hybrid|office|страна)`)
	reRemote    = regexp.MustCompile(`(?i)#remote|#удал[её]нка|удал[её]нн?о|удал[её]нка|удал[её]нная|fully remote|remote\b|можно удал`)
	reNotOffice = regexp.MustCompile(`(?i)офис/гибрид|только офис|#office\b|onsite`)
	reDigest    = regexp.MustCompile(`(?i)за неделю|подборка|дайджест|вакансии дня|вакансии по php за|топ-?\d+`)
)

// parsePost превращает текст поста в поля вакансии. ok=false — это не вакансия (реклама, подборка).
func parsePost(text, channel string) (title, company, location string, remote bool, ok bool) {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !reHashLine.MatchString(l) {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return "", "", "", false, false
	}
	titleIdx := 0
	for i, l := range lines[:min(len(lines), 4)] {
		if reTitleLike.MatchString(l) && len([]rune(l)) <= 120 {
			titleIdx = i
			break
		}
	}
	title = strings.Trim(lines[titleIdx], "🔹•▪️ ")
	if reDigest.MatchString(title) {
		return "", "", "", false, false
	}

	company = "t.me/" + channel
	for _, l := range lines {
		if m := reCompany.FindStringSubmatch(l); m != nil {
			company = strings.TrimSpace(m[1])
			break
		}
	}
	if company == "t.me/"+channel && titleIdx > 0 && len([]rune(lines[titleIdx-1])) <= 60 {
		company = lines[titleIdx-1] // формат: «Компания\nДолжность»
	}

	var loc []string
	for _, l := range lines {
		if reFormat.MatchString(l) && len([]rune(l)) <= 160 {
			loc = append(loc, l)
		}
	}
	location = strings.Join(loc, "; ")
	if len([]rune(location)) > 200 {
		location = string([]rune(location)[:200])
	}
	remote = reRemote.MatchString(text) && !reNotOffice.MatchString(text)
	return title, company, location, remote, true
}
