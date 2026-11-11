// Package filter решает, подходит ли вакансия под профиль, по жёстким правилам.
package filter

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/model"
)

// Verdict — результат проверки.
type Verdict struct {
	Status model.Status // new (подходит), parked или filtered
	Reason string       // почему отсеяна (для filtered)
	Notes  []string     // пометки для карточки: «ЗП не указана», «гибрид: Минск»
}

var defaultRates = map[string]float64{
	"USD": 1, "EUR": 1.10, "GBP": 1.30, "RUR": 0.0125, "RUB": 0.0125, "KZT": 0.002, "BYN": 0.31,
}

var reGoToMarket = regexp.MustCompile(`(?i)go[- ]to[- ]market`)

type Filter struct {
	p     config.Profile
	roles []*regexp.Regexp
	tech  []*regexp.Regexp
	rates map[string]float64
	now   func() time.Time
}

// New компилирует регулярки профиля.
func New(p config.Profile) (*Filter, error) {
	roles, err := compile(p.TitleRoles, true)
	if err != nil {
		return nil, fmt.Errorf("title_roles: %w", err)
	}
	techPatterns := p.TechAny
	if len(techPatterns) == 0 {
		techPatterns = []string{`(?i)\bphp\b`, `(?i)\bgolang\b`, `\bGo\b`}
	}
	tech, err := compile(techPatterns, false)
	if err != nil {
		return nil, fmt.Errorf("tech_any: %w", err)
	}
	rates := map[string]float64{}
	for k, v := range defaultRates {
		rates[k] = v
	}
	for k, v := range p.USDRates {
		rates[strings.ToUpper(k)] = v
	}
	return &Filter{p: p, roles: roles, tech: tech, rates: rates, now: time.Now}, nil
}

// compile: для ролей добавляем (?i); технологии пишутся как есть, регистр задаёт сам шаблон
// ((?i)\bphp\b игнорирует регистр, а \bGo\b различает «Go» и «go»).
func compile(patterns []string, insensitive bool) ([]*regexp.Regexp, error) {
	var out []*regexp.Regexp
	for _, s := range patterns {
		if insensitive {
			s = "(?i)" + s
		}
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", s, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// Check применяет правила по порядку. Первое сработавшее правило определяет результат.
func (f *Filter) Check(v model.Vacancy) Verdict {
	reject := func(reason string) Verdict { return Verdict{Status: model.StatusFiltered, Reason: reason} }

	if f.p.IsBlacklisted(v.Company) {
		return reject("blacklist")
	}

	// Отложенные компании сохраняем независимо от возраста: после /unpark нужны их текущие вакансии,
	// а не только те, что появились за последние дни.
	parked := f.p.IsParked(v.Company) || f.parkedInPost(v)

	// Возраст: «только что появившиеся». Неизвестная дата считается свежей.
	if !parked && !v.PublishedAt.IsZero() && f.now().Sub(v.PublishedAt) > time.Duration(f.p.MaxAgeHours)*time.Hour {
		return reject("stale")
	}

	title := strings.ToLower(v.Title)
	for _, w := range f.p.StopWords {
		if w != "" && strings.Contains(title, strings.ToLower(w)) {
			return reject("stop_word:" + w)
		}
	}

	if !v.Trusted() { // в письме от сайта мало текста: роль и стек уже проверил сохранённый поиск
		if len(f.roles) > 0 && !matchAny(f.roles, v.Title) {
			return reject("role")
		}
		// «Go-To-Market» — не язык Go.
		text := reGoToMarket.ReplaceAllString(v.Title+"\n"+v.Description, "")
		if !matchAny(f.tech, text) {
			return reject("tech")
		}
	}

	loc := strings.ToLower(v.Location)
	for _, ex := range f.p.ExcludeLocations {
		if ex != "" && strings.Contains(loc, strings.ToLower(ex)) {
			return reject("location:" + ex)
		}
	}

	var notes []string
	switch {
	case v.Remote && f.p.Remote:
		// удалёнка подходит
	case containsAnyFold(v.Location, f.p.HybridCities):
		notes = append(notes, "офис/гибрид: "+v.Location)
	case v.Trusted():
		// формат работы в письме обычно не указан; фильтр по нему задан в сохранённом поиске на сайте
		notes = append(notes, "проверьте формат работы")
	default:
		return reject("location")
	}

	if note, ok := f.checkSalary(v); !ok {
		return reject("salary")
	} else if note != "" {
		notes = append(notes, note)
	}

	st := model.StatusNew
	if parked {
		st = model.StatusParked
	}
	return Verdict{Status: st, Notes: notes}
}

// checkSalary сравнивает верхнюю границу вилки (в месяц) с порогом в USD.
func (f *Filter) checkSalary(v model.Vacancy) (note string, ok bool) {
	top := v.SalaryTo
	if top == 0 {
		top = v.SalaryFrom
	}
	rate, known := f.rates[strings.ToUpper(v.Currency)]
	if top == 0 || !known {
		if f.p.AllowUnknownPay {
			return "ЗП не указана", true
		}
		return "", false
	}
	usd := float64(top) * rate
	if usd < float64(f.p.SalaryMinUSD) {
		return "", false
	}
	return fmt.Sprintf("до ~$%d/мес", int(usd)), true
}

func matchAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func containsAnyFold(s string, list []string) bool {
	s = strings.ToLower(s)
	for _, item := range list {
		if item != "" && strings.Contains(s, strings.ToLower(item)) {
			return true
		}
	}
	return false
}

// parkedInPost: у постов из Telegram и писем компания часто не выделена, поэтому ищем
// отложенные компании в заголовке и начале текста — только как отдельные слова (VK ≠ «avkus»).
func (f *Filter) parkedInPost(v model.Vacancy) bool {
	if !strings.HasPrefix(v.Source, "tg:") && !v.Trusted() {
		return false
	}
	for _, name := range f.p.ParkedCompanies {
		if v.Mentions(name) {
			return true
		}
	}
	return false
}
