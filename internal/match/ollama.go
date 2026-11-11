package match

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/model"
)

// OllamaWriter пишет письмо локальной моделью через Ollama (http://localhost:11434).
// Письмо проходит проверку на выдумки; если модель недоступна или выдумала факты,
// возвращается шаблонное письмо — бот не остаётся без черновика.
type OllamaWriter struct {
	BaseURL  string
	Model    string
	C        *Candidate
	Fallback Writer
	Client   *http.Client
}

// NewWriter выбирает автора писем по настройкам: шаблоны или локальная модель.
func NewWriter(cfg config.LLM, c *Candidate) Writer {
	tmpl := TemplateWriter{C: c}
	switch strings.ToLower(cfg.Provider) {
	case "ollama":
		url := cfg.OllamaURL
		if url == "" {
			url = "http://localhost:11434"
		}
		model := cfg.OllamaModel
		if model == "" {
			model = "qwen2.5:7b-instruct"
		}
		timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 3 * time.Minute
		}
		return &OllamaWriter{BaseURL: strings.TrimRight(url, "/"), Model: model, C: c, Fallback: tmpl,
			Client: &http.Client{Timeout: timeout}}
	default:
		return tmpl
	}
}

// Check проверяет, что Ollama отвечает и модель скачана. Для сообщения при старте.
func (w *OllamaWriter) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.BaseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := w.Client.Do(req)
	if err != nil {
		return fmt.Errorf("Ollama недоступна по адресу %s: %w", w.BaseURL, err)
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tags); err != nil {
		return fmt.Errorf("ответ Ollama: %w", err)
	}
	for _, m := range tags.Models {
		if m.Name == w.Model || strings.TrimSuffix(m.Name, ":latest") == w.Model {
			return nil
		}
	}
	return fmt.Errorf("модель %q не скачана: выполните `ollama pull %s`", w.Model, w.Model)
}

func (w *OllamaWriter) Write(ctx context.Context, v model.Vacancy, a Assessment) (string, error) {
	letter, err := w.generate(ctx, v)
	if err == nil {
		err = w.validate(letter, v)
	}
	if err != nil {
		slog.Warn("локальная модель не подошла, использую шаблон", "err", err, "url", v.URL)
		return w.Fallback.Write(ctx, v, a)
	}
	return letter, nil
}

// ---------- запрос к модели ----------

const systemPrompt = `You write a short cover letter for a job application on behalf of the candidate.
Rules:
- Use ONLY the facts listed under CANDIDATE FACTS. Never invent employers, technologies, numbers, years, degrees or achievements.
- Pick the 2-3 facts most relevant to the vacancy and weave them into plain sentences. Keep the numbers exactly as given.
- Do not claim skills that are not in the facts. If the vacancy needs something the facts do not cover, simply do not mention it.
- 80-140 words. Greeting, 1 short paragraph about fit, a short closing. No bullet lists, no markdown, no emojis.
- No flattery or filler ("passionate", "dream job", "I am excited"). No contacts, links, phone numbers or emails.
- Mention the company by name only if it is given.
- Sign with the candidate's name exactly as given in SIGNATURE.
- Output only the letter text, nothing else.`

func (w *OllamaWriter) generate(ctx context.Context, v model.Vacancy) (string, error) {
	en := v.Language != "ru"
	lang := "Russian. The candidate is a woman: use feminine verb forms (откликаюсь, работала, готова)."
	sig := w.C.Name
	if en {
		lang = "English. Use short, simple sentences (B1 level), no complex grammar or idioms."
		if w.C.NameEN != "" {
			sig = w.C.NameEN
		}
	}
	var facts strings.Builder
	headline := w.C.HeadlineRU
	if en {
		headline = w.C.HeadlineEN
	}
	fmt.Fprintf(&facts, "- %s; %d years of PHP in e-commerce backends", headline, w.C.YearsPHP)
	if w.C.GoYears > 0 {
		fmt.Fprintf(&facts, "; about %d year of Go in production", w.C.GoYears)
	}
	facts.WriteString("\n")
	for _, ach := range w.C.Achievements {
		t := ach.RU
		if en {
			t = ach.EN
		}
		fmt.Fprintf(&facts, "- %s\n", t)
	}

	desc := truncateRunes(v.Description, 3000)
	company := v.Company
	if strings.HasPrefix(company, "t.me/") {
		company = "(not given)"
	}
	user := fmt.Sprintf("LANGUAGE: %s\nSIGNATURE: %s\n\nCANDIDATE FACTS:\n%s\nVACANCY\nTitle: %s\nCompany: %s\nDescription:\n%s\n",
		lang, sig, facts.String(), v.Title, company, desc)

	body, _ := json.Marshal(map[string]any{
		"model":      w.Model,
		"stream":     false,
		"keep_alive": "30m",
		"options":    map[string]any{"temperature": 0.3, "num_ctx": 8192},
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": user},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("запрос к Ollama: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Ollama вернула %d: %s", resp.StatusCode, truncateRunes(string(raw), 200))
	}
	var out struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("разбор ответа Ollama: %w", err)
	}
	return cleanLetter(out.Message.Content), nil
}

// cleanLetter убирает обрамление, которое модели иногда добавляют (```, «Письмо:»).
func cleanLetter(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	return s
}

// ---------- проверка на выдумки ----------

var (
	reNumber  = regexp.MustCompile(`\d[\d \x{00A0}.,]*\d|\d`)
	reContact = regexp.MustCompile(`(?i)@|https?://|t\.me/|www\.|\+\d`)
)

// validate отклоняет письмо, если в нём есть числа, которых нет в фактах и вакансии,
// контакты, неверный язык или неподходящая длина.
func (w *OllamaWriter) validate(letter string, v model.Vacancy) error {
	words := len(strings.Fields(letter))
	if words < 40 || words > 260 {
		return fmt.Errorf("длина письма %d слов", words)
	}
	if reContact.MatchString(letter) {
		return fmt.Errorf("в письме есть контакты или ссылки")
	}
	cyr, lat := 0, 0
	for _, r := range letter {
		switch {
		case unicode.Is(unicode.Cyrillic, r):
			cyr++
		case unicode.Is(unicode.Latin, r):
			lat++
		}
	}
	if total := cyr + lat; total > 0 {
		share := float64(cyr) / float64(total)
		if v.Language == "ru" && share < 0.5 {
			return fmt.Errorf("письмо должно быть на русском")
		}
		if v.Language != "ru" && share > 0.15 {
			return fmt.Errorf("письмо должно быть на английском")
		}
	}

	allowed := map[string]bool{}
	addNumbers := func(text string) {
		for _, n := range reNumber.FindAllString(text, -1) {
			allowed[digitsOnly(n)] = true
		}
	}
	addNumbers(w.C.HeadlineRU + " " + w.C.HeadlineEN)
	for _, a := range w.C.Achievements {
		addNumbers(a.RU + " " + a.EN)
	}
	addNumbers(fmt.Sprintf("%d %d", w.C.YearsPHP, w.C.GoYears))
	addNumbers(v.Title + " " + v.Description)
	for _, n := range reNumber.FindAllString(letter, -1) {
		if d := digitsOnly(n); !allowed[d] {
			return fmt.Errorf("в письме число %q, которого нет в фактах и вакансии", n)
		}
	}
	return nil
}

func digitsOnly(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
