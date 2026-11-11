// Package config загружает профиль поиска (YAML) и секреты (переменные окружения).
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/anastasia/job-bot/internal/model"
)

// Profile — что и где мы ищем.
type Profile struct {
	Roles           []string `yaml:"roles"`          // ключевые слова названий вакансий
	Skills          []string `yaml:"skills"`         // желательные технологии
	StopWords       []string `yaml:"stop_words"`     // если есть в названии/описании — отсекаем
	SalaryMinUSD    int      `yaml:"salary_min_usd"` // порог зарплаты
	AllowUnknownPay bool     `yaml:"allow_unknown_salary"`
	Remote          bool     `yaml:"remote"`           // удалёнка подходит
	HybridCities    []string `yaml:"hybrid_cities"`    // города, где подходит гибрид
	ParkedCompanies []string `yaml:"parked_companies"` // собираем, но не показываем
	Blacklist       []string `yaml:"blacklist"`        // не показываем никогда
	MatchThreshold  int      `yaml:"match_threshold"`  // 0..100, порог LLM-скоринга

	// Регулярки (без учёта регистра, кроме Go): название вакансии должно подходить под роль,
	// а в названии или описании должна встретиться одна из технологий.
	TitleRoles []string `yaml:"title_roles"`
	TechAny    []string `yaml:"tech_any"`
	// Если в локации вакансии есть одна из подстрок — отсекаем (например, "united states").
	ExcludeLocations []string `yaml:"exclude_locations"`

	PollMinutes int `yaml:"poll_minutes"`  // как часто опрашивать источники
	MaxAgeHours int `yaml:"max_age_hours"` // старше — не показываем
	MaxPerCycle int `yaml:"max_per_cycle"` // не больше N уведомлений за цикл

	// Курсы валют: сколько USD в 1 единице валюты. Переопределяют значения по умолчанию.
	USDRates map[string]float64 `yaml:"usd_rates"`

	LLM LLM `yaml:"llm"` // как писать письма: шаблоны или локальная модель

	CandidateFile string `yaml:"candidate_file"` // профиль кандидата для писем (по умолчанию candidate.yaml)

	Sources Sources `yaml:"sources"`
}

// LLM — настройки автора писем. provider: "none" (шаблоны, по умолчанию) или "ollama" (локальная модель).
type LLM struct {
	Provider       string `yaml:"provider"`
	OllamaURL      string `yaml:"ollama_url"`      // по умолчанию http://localhost:11434
	OllamaModel    string `yaml:"ollama_model"`    // по умолчанию qwen2.5:7b-instruct
	TimeoutSeconds int    `yaml:"timeout_seconds"` // ожидание ответа модели, по умолчанию 180
}

// Mail — настройки чтения почты. Адрес, логин и пароль приложения задаются в .env (IMAP_HOST, IMAP_USER, IMAP_PASSWORD).
type Mail struct {
	Folder       string       `yaml:"folder"`        // папка или ярлык, по умолчанию INBOX
	LookbackDays int          `yaml:"lookback_days"` // за сколько дней смотреть письма, по умолчанию 3
	Senders      []MailSender `yaml:"senders"`       // дополнительные отправители к hh.ru и geekjob.ru
}

// MailSender — отправитель рассылки: домен в поле From и регулярка ссылки на вакансию (первая группа — id).
type MailSender struct {
	Domain string `yaml:"domain"`
	Link   string `yaml:"link"`
	URL    string `yaml:"url"` // шаблон ссылки для карточки, %s — id; необязательно
}

// IMAP — доступ к почте, из окружения.
type IMAP struct{ Addr, User, Password string }

// IMAPFromEnv читает IMAP_HOST (по умолчанию imap.gmail.com), IMAP_USER, IMAP_PASSWORD.
// Если логин или пароль не заданы, чтение почты выключено (User пустой).
func IMAPFromEnv() IMAP {
	host := os.Getenv("IMAP_HOST")
	if host == "" {
		host = "imap.gmail.com"
	}
	if !strings.Contains(host, ":") {
		host += ":993"
	}
	i := IMAP{Addr: host, User: os.Getenv("IMAP_USER"), Password: os.Getenv("IMAP_PASSWORD")}
	if i.User == "" || i.Password == "" {
		return IMAP{}
	}
	return i
}

// Sources — списки компаний по системам найма (slug из адреса карьерной страницы).
type Sources struct {
	Greenhouse []string `yaml:"greenhouse"`
	Lever      []string `yaml:"lever"`
	Ashby      []string `yaml:"ashby"`
	Telegram   []string `yaml:"telegram"`    // публичные каналы с вакансиями (без @)
	Mail       Mail     `yaml:"mail"`        // рассылки вакансий на почту (IMAP)
	HabrCareer []string `yaml:"habr_career"` // запросы к RSS Хабр Карьеры, например "q=php&type=all&remote=true"
}

// Secrets — берутся только из окружения (.env), не из файла в репозитории.
type Secrets struct {
	TelegramToken string
	TelegramChat  int64
	DBPath        string
}

type Config struct {
	Profile Profile
	Secrets Secrets
}

// LoadProfile читает только YAML-профиль (без секретов) — для проверочных запусков.
func LoadProfile(path string) (Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, fmt.Errorf("read profile %s: %w", path, err)
	}
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return Profile{}, fmt.Errorf("parse profile: %w", err)
	}
	if p.SalaryMinUSD <= 0 {
		return Profile{}, fmt.Errorf("profile: salary_min_usd must be > 0")
	}
	if len(p.Roles) == 0 {
		return Profile{}, fmt.Errorf("profile: roles is empty")
	}
	if p.CandidateFile == "" {
		p.CandidateFile = "candidate.yaml"
	}
	if p.PollMinutes <= 0 {
		p.PollMinutes = 10
	}
	if p.MaxAgeHours <= 0 {
		p.MaxAgeHours = 72
	}
	if p.MaxPerCycle <= 0 {
		p.MaxPerCycle = 10
	}
	return p, nil
}

// Load читает YAML-профиль по пути path и секреты из окружения.
func Load(path string) (*Config, error) {
	p, err := LoadProfile(path)
	if err != nil {
		return nil, err
	}

	token := os.Getenv("TELEGRAM_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("env TELEGRAM_TOKEN is not set")
	}
	chat, err := strconv.ParseInt(os.Getenv("TELEGRAM_CHAT_ID"), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("env TELEGRAM_CHAT_ID must be a number: %w", err)
	}
	db := os.Getenv("DB_PATH")
	if db == "" {
		db = "jobbot.db"
	}
	return &Config{Profile: p, Secrets: Secrets{TelegramToken: token, TelegramChat: chat, DBPath: db}}, nil
}

// IsParked — компания из списка «отложено» (сравнение по подстроке, без регистра).
func (p Profile) IsParked(company string) bool { return containsAny(company, p.ParkedCompanies) }

// IsBlacklisted — компания в чёрном списке.
func (p Profile) IsBlacklisted(company string) bool { return containsAny(company, p.Blacklist) }

// containsAny: название компании совпадает с одним из элементов списка как отдельное слово.
func containsAny(s string, list []string) bool {
	for _, item := range list {
		if model.ContainsWord(s, item) {
			return true
		}
	}
	return false
}
