package source

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/anastasia/job-bot/internal/model"
)

// HabrCareer читает публичную RSS-ленту вакансий Хабр Карьеры
// (https://career.habr.com/vacancies/rss). Запросы задаются строками параметров,
// например "q=php&type=all&remote=true" — как в адресе поиска на сайте.
type HabrCareer struct{ Queries []string }

func (h HabrCareer) Name() string { return "habr" }

type rssFeed struct {
	Items []struct {
		Title       string `xml:"title"`
		Description string `xml:"description"`
		Author      string `xml:"author"`
		PubDate     string `xml:"pubDate"`
		Link        string `xml:"link"`
		GUID        string `xml:"guid"`
	} `xml:"channel>item"`
}

func (h HabrCareer) Fetch(ctx context.Context) ([]model.Vacancy, error) {
	var all []model.Vacancy
	var errs []string
	for _, q := range h.Queries {
		body, err := fetchBytes(ctx, "https://career.habr.com/vacancies/rss?"+strings.TrimPrefix(q, "?"))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%q: %v", q, err))
			continue
		}
		vs, err := parseHabrRSS(body)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%q: %v", q, err))
			continue
		}
		all = append(all, vs...)
	}
	return all, joinErrs(errs)
}

func fetchBytes(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

var (
	reHabrTitle  = regexp.MustCompile(`^Требуется «(.*)»(?:\s*\(([^()]*)\))?\s*$`)
	reHabrRemote = regexp.MustCompile(`(?i)можно удал[её]нно`)
)

func parseHabrRSS(data []byte) ([]model.Vacancy, error) {
	var feed rssFeed
	if err := xml.Unmarshal(data, &feed); err != nil {
		return nil, fmt.Errorf("разбор RSS: %w", err)
	}
	var out []model.Vacancy
	for _, it := range feed.Items {
		title, city := strings.TrimSpace(it.Title), ""
		if m := reHabrTitle.FindStringSubmatch(title); m != nil {
			title, city = m[1], m[2]
		}
		published, _ := time.Parse(time.RFC1123Z, strings.TrimSpace(it.PubDate))
		id := strings.TrimSpace(it.GUID)
		if id == "" || strings.TrimSpace(it.Link) == "" {
			continue
		}
		out = append(out, model.Vacancy{
			Source:      "habr",
			ExternalID:  id,
			URL:         strings.TrimSpace(it.Link),
			Title:       title,
			Company:     strings.TrimSpace(it.Author),
			Description: strings.TrimSpace(it.Description),
			Location:    city,
			Remote:      reHabrRemote.MatchString(it.Description),
			Language:    "ru",
			PublishedAt: published,
		})
	}
	return out, nil
}
