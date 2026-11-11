// Package match оценивает соответствие вакансии профилю и готовит черновик письма.
// Реализация не зависит от нейросетей: правила и шаблоны. Интерфейсы позволяют
// позже подключить LLM, не меняя остальной код.
package match

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/anastasia/job-bot/internal/model"
)

// Assessment — результат оценки вакансии.
type Assessment struct {
	Score   int      // 0..100
	Matched []string // что совпало со стеком
	Note    string   // короткое объяснение для карточки
}

// Assessor оценивает вакансию.
type Assessor interface {
	Assess(ctx context.Context, v model.Vacancy) (Assessment, error)
}

// Writer готовит черновик сопроводительного письма.
type Writer interface {
	Write(ctx context.Context, v model.Vacancy, a Assessment) (string, error)
}

// ---------- профиль кандидата ----------

type Achievement struct {
	Tags []string `yaml:"tags"`
	RU   string   `yaml:"ru"`
	EN   string   `yaml:"en"`
}

type Candidate struct {
	Name                string        `yaml:"name"`
	NameEN              string        `yaml:"name_en"` // подпись в английских письмах (по умолчанию name)
	HeadlineRU          string        `yaml:"headline_ru"`
	HeadlineEN          string        `yaml:"headline_en"`
	YearsPHP            int           `yaml:"years_php"`
	GoYears             int           `yaml:"go_years"`
	Achievements        []Achievement `yaml:"achievements"`
	DefaultAchievements []int         `yaml:"default_achievements"`
}

// LoadCandidate читает candidate.yaml.
func LoadCandidate(path string) (*Candidate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read candidate %s: %w", path, err)
	}
	var c Candidate
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse candidate: %w", err)
	}
	if c.Name == "" || len(c.Achievements) == 0 {
		return nil, fmt.Errorf("candidate: name and achievements are required")
	}
	for _, i := range c.DefaultAchievements {
		if i < 0 || i >= len(c.Achievements) {
			return nil, fmt.Errorf("candidate: default_achievements index %d out of range", i)
		}
	}
	return &c, nil
}

// ---------- оценка по правилам ----------

var (
	rePHP      = regexp.MustCompile(`(?i)\bphp\b`)
	reGo       = regexp.MustCompile(`\bGo\b|(?i:\bgolang\b)`)
	reSenior   = regexp.MustCompile(`(?i)senior|lead|principal|staff|старш|ведущ|тимлид|team lead|tech lead`)
	reMiddle   = regexp.MustCompile(`(?i)middle|мидл|\bmid\b`)
	reOtherLng = regexp.MustCompile(`(?i)\b(java|kotlin|c#|\.net|python|ruby|node\.?js|react|vue|angular|ios|android|swift|scala|rust|c\+\+)\b`)
	reOffTopic = regexp.MustCompile(`(?i)qa\b|тестировщик|devops|data (engineer|scien|analy)|аналитик|frontend|front-end|фронтенд|designer|менеджер|manager`)
	reLegacy   = regexp.MustCompile(`(?i)bitrix|битрикс|wordpress|1с|drupal`)
	reContract = regexp.MustCompile(`(?i)самозанят|\bип\b|b2b|contractor|контракт|гпх|freelance|фриланс`)
)

// RuleAssessor считает баллы по совпадению стека, роли и условий.
type RuleAssessor struct {
	Skills       []string // из профиля поиска (skills)
	SalaryMinUSD int
}

func (r RuleAssessor) Assess(_ context.Context, v model.Vacancy) (Assessment, error) {
	text := strings.ToLower(v.Title + "\n" + v.Description)
	score := 0
	var notes []string

	hasPHP, hasGo := rePHP.MatchString(v.Title+" "+v.Description), reGo.MatchString(v.Title+" "+v.Description)
	switch {
	case hasPHP:
		score += 25
	case hasGo:
		score += 15
		notes = append(notes, "Go — ваш опыт около года")
	}

	var matched []string
	for _, s := range r.Skills {
		if s != "" && strings.Contains(text, strings.ToLower(s)) {
			matched = append(matched, s)
		}
	}
	score += min(len(matched)*8, 48)

	switch {
	case reSenior.MatchString(v.Title):
		score += 12
	case reMiddle.MatchString(v.Title):
		score -= 10
		notes = append(notes, "уровень Middle")
	}
	if v.Remote {
		score += 5
	}
	if r.SalaryMinUSD > 0 && (v.SalaryFrom > 0 || v.SalaryTo > 0) {
		score += 5
	}
	if reOtherLng.MatchString(v.Title) && !hasPHP && !hasGo {
		score -= 25
		notes = append(notes, "в названии другой стек")
	} else if reOtherLng.MatchString(v.Title) {
		score -= 8
	}
	if reOffTopic.MatchString(v.Title) {
		score -= 20
		notes = append(notes, "роль не backend-разработка")
	}
	if reLegacy.MatchString(text) {
		score -= 15
		notes = append(notes, "упоминается Bitrix/WordPress")
	}
	score = max(0, min(100, score))

	note := ""
	if len(matched) > 0 {
		note = "Совпало: " + strings.Join(matched, ", ")
	}
	if len(notes) > 0 {
		if note != "" {
			note += ". "
		}
		note += strings.Join(notes, "; ")
	}
	return Assessment{Score: score, Matched: matched, Note: note}, nil
}

// ---------- письма из шаблонов ----------

// TemplateWriter собирает письмо из достижений кандидата, выбирая подходящие по тегам.
type TemplateWriter struct{ C *Candidate }

func (w TemplateWriter) Write(_ context.Context, v model.Vacancy, a Assessment) (string, error) {
	if w.C == nil {
		return "", fmt.Errorf("candidate is nil")
	}
	picked := w.pick(strings.ToLower(v.Title + "\n" + v.Description))
	en := v.Language != "ru"
	contract := reContract.MatchString(v.Title + " " + v.Description)
	needsGo := reGo.MatchString(v.Title + " " + v.Description)
	hasPHP := rePHP.MatchString(v.Title + " " + v.Description)

	var sb strings.Builder
	if en {
		sb.WriteString("Hello,\n\n")
		fmt.Fprintf(&sb, "I am writing about the \"%s\" position%s. I am a %s with %d years in e-commerce backends.\n\n",
			v.Title, companySuffix(v.Company, true), w.C.HeadlineEN, w.C.YearsPHP)
		sb.WriteString("What may be useful for you:\n")
		for _, i := range picked {
			fmt.Fprintf(&sb, "- %s\n", w.C.Achievements[i].EN)
		}
		if needsGo && !hasPHP && w.C.GoYears > 0 {
			fmt.Fprintf(&sb, "\nI also write Go in production (about %d year).\n", w.C.GoYears)
		}
		if len(a.Matched) > 0 {
			fmt.Fprintf(&sb, "\nMy stack matches yours: %s.\n", strings.Join(a.Matched, ", "))
		}
		sb.WriteString("\nI am ready to answer your questions and to talk about the details.")
		if contract {
			sb.WriteString(" I can work as a contractor.")
		}
		name := w.C.NameEN
		if name == "" {
			name = w.C.Name
		}
		fmt.Fprintf(&sb, "\n\nBest regards,\n%s", name)
		return sb.String(), nil
	}

	sb.WriteString("Здравствуйте!\n\n")
	fmt.Fprintf(&sb, "Откликаюсь на вакансию «%s»%s. Я — %s, %d лет занимаюсь backend-разработкой в e-commerce.\n\n",
		v.Title, companySuffix(v.Company, false), w.C.HeadlineRU, w.C.YearsPHP)
	sb.WriteString("Что может быть полезно для вас:\n")
	for _, i := range picked {
		fmt.Fprintf(&sb, "— %s\n", w.C.Achievements[i].RU)
	}
	if needsGo && !hasPHP && w.C.GoYears > 0 {
		sb.WriteString("\nТакже пишу на Go в продакшене (около года).\n")
	}
	if len(a.Matched) > 0 {
		fmt.Fprintf(&sb, "\nСтек совпадает с вашим: %s.\n", strings.Join(a.Matched, ", "))
	}
	sb.WriteString("\nГотова ответить на вопросы и обсудить детали.")
	if contract {
		sb.WriteString(" Рассматриваю сотрудничество как самозанятая.")
	}
	fmt.Fprintf(&sb, "\n\nС уважением,\n%s", w.C.Name)
	return sb.String(), nil
}

// pick выбирает до трёх достижений с наибольшим числом тегов, найденных в тексте вакансии.
func (w TemplateWriter) pick(text string) []int {
	type scored struct{ idx, n int }
	var s []scored
	for i, ach := range w.C.Achievements {
		n := 0
		for _, t := range ach.Tags {
			if strings.Contains(text, strings.ToLower(t)) {
				n++
			}
		}
		if n > 0 {
			s = append(s, scored{i, n})
		}
	}
	sort.SliceStable(s, func(a, b int) bool { return s[a].n > s[b].n })
	var out []int
	for _, x := range s {
		out = append(out, x.idx)
		if len(out) == 3 {
			return out
		}
	}
	for _, i := range w.C.DefaultAchievements { // добираем до трёх по умолчанию
		if len(out) == 3 {
			break
		}
		if !containsInt(out, i) {
			out = append(out, i)
		}
	}
	return out
}

func containsInt(s []int, x int) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// companySuffix: « в компании X» только если название компании настоящее (не t.me/канал).
func companySuffix(company string, en bool) string {
	if company == "" || strings.HasPrefix(company, "t.me/") {
		return ""
	}
	if en {
		return " at " + company
	}
	return " в компании " + company
}
