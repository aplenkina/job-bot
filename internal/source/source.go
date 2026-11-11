// Package source — сборщики вакансий. Каждый источник приводит данные к model.Vacancy.
package source

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/anastasia/job-bot/internal/model"
)

// Source возвращает текущие открытые вакансии.
type Source interface {
	Name() string
	Fetch(ctx context.Context) ([]model.Vacancy, error)
}

// userAgent — обезличенный, без контактных данных.
const userAgent = "jobbot/0.1"

const maxBody = 50 << 20 // 50 МБ: защита от неожиданно огромных ответов

var httpClient = &http.Client{Timeout: 30 * time.Second}

// getJSON выполняет GET и декодирует JSON в out.
func getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out)
}

var (
	reTags  = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace = regexp.MustCompile(`[ \t\r\f\v]+`)
	reLines = regexp.MustCompile(`\n{3,}`)
	reBlock = regexp.MustCompile(`(?i)</?(p|div|br|li|ul|ol|h[1-6])[^>]*>`)
)

// stripHTML превращает HTML в читаемый текст.
func stripHTML(s string) string {
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTags.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpace.ReplaceAllString(s, " ")
	s = reLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// detectLang: "ru", если заметная доля букв — кириллица, иначе "en".
func detectLang(s string) string {
	var cyr, letters int
	for _, r := range s {
		switch {
		case r >= 'а' && r <= 'я', r >= 'А' && r <= 'Я', r == 'ё', r == 'Ё':
			cyr++
			letters++
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			letters++
		}
	}
	if letters > 0 && float64(cyr)/float64(letters) > 0.3 {
		return "ru"
	}
	return "en"
}

func isRemote(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "remote") || strings.Contains(s, "удал") || strings.Contains(s, "anywhere")
}

// monthly приводит зарплату к месяцу по периоду источника.
func monthly(amount float64, perYear bool) int {
	if perYear {
		amount /= 12
	}
	return int(amount)
}
