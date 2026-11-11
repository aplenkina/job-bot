// Package model содержит общие типы данных.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// Vacancy — нормализованная вакансия из любого источника.
type Vacancy struct {
	Source      string // "hh", "greenhouse", "tg:<channel>" ...
	ExternalID  string // id в источнике
	URL         string
	Title       string
	Company     string
	Description string
	Location    string
	Remote      bool
	SalaryFrom  int    // 0 = не указана
	SalaryTo    int    // 0 = не указана
	Currency    string // ISO: RUR, USD, EUR, KZT, BYN
	Language    string // "ru" | "en"
	PublishedAt time.Time
}

// ContainsWord сообщает, встречается ли name в s как отдельное слово (без учёта регистра).
// Подстрока была бы ошибкой: «VK» нашлось бы внутри «DEVKIT».
func ContainsWord(s, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	re, err := regexp.Compile(`(?i)(^|[^\p{L}\p{N}])` + regexp.QuoteMeta(name) + `([^\p{L}\p{N}]|$)`)
	return err == nil && re.MatchString(s)
}

// Mentions — тот же поиск по «лицу» вакансии: компания, название и начало текста.
// У постов из Telegram и писем компания часто не выделена, поэтому смотрим и в текст.
func (v Vacancy) Mentions(name string) bool {
	head := []rune(v.Description)
	if len(head) > 600 {
		head = head[:600]
	}
	return ContainsWord(v.Company, name) || ContainsWord(v.Title, name) || ContainsWord(string(head), name)
}

// Trusted — вакансия из письма с рассылки: сайт уже подобрал её под ваш сохранённый поиск,
// поэтому проверки роли, технологии и формата работы по скудному тексту письма не применяются.
func (v Vacancy) Trusted() bool { return strings.HasPrefix(v.Source, "mail:") }

// Fingerprint — отпечаток содержимого для поиска дублей между каналами (кросспосты).
// Считается по первым 400 буквам и цифрам названия и описания, регистр и знаки не важны.
func (v Vacancy) Fingerprint() string {
	var sb strings.Builder
	n := 0
	for _, r := range v.Title + " " + v.Description {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			sb.WriteRune(unicode.ToLower(r))
			if n++; n >= 400 {
				break
			}
		}
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:8])
}

// Status — состояние вакансии в нашем пайплайне.
type Status string

const (
	StatusNew      Status = "new"      // найдена, ещё не обработана
	StatusParked   Status = "parked"   // компания в списке «отложено»
	StatusFiltered Status = "filtered" // отсеяна правилами
	StatusNotified Status = "notified" // отправлена в Telegram, ждёт решения
	StatusApplied  Status = "applied"  // отклик отправлен
	StatusSkipped  Status = "skipped"  // вы нажали «Пропустить»
)
