package source

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/model"
)

// ---------- Greenhouse ----------

type Greenhouse struct{ Slugs []string }

func (g Greenhouse) Name() string { return "greenhouse" }

func (g Greenhouse) Fetch(ctx context.Context) ([]model.Vacancy, error) {
	var all []model.Vacancy
	var errs []string
	for _, slug := range g.Slugs {
		var resp struct {
			Jobs []struct {
				ID             int64  `json:"id"`
				Title          string `json:"title"`
				AbsoluteURL    string `json:"absolute_url"`
				CompanyName    string `json:"company_name"`
				Content        string `json:"content"`
				UpdatedAt      string `json:"updated_at"`
				FirstPublished string `json:"first_published"`
				Location       struct {
					Name string `json:"name"`
				} `json:"location"`
			} `json:"jobs"`
		}
		u := "https://boards-api.greenhouse.io/v1/boards/" + url.PathEscape(slug) + "/jobs?content=true"
		if err := getJSON(ctx, u, &resp); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", slug, err))
			continue
		}
		for _, j := range resp.Jobs {
			company := j.CompanyName
			if company == "" {
				company = slug
			}
			desc := stripHTML(j.Content)
			all = append(all, model.Vacancy{
				Source:      "greenhouse:" + slug,
				ExternalID:  fmt.Sprint(j.ID),
				URL:         j.AbsoluteURL,
				Title:       j.Title,
				Company:     company,
				Description: desc,
				Location:    j.Location.Name,
				Remote:      isRemote(j.Location.Name) || isRemote(j.Title),
				Language:    detectLang(j.Title + " " + desc),
				PublishedAt: parseTime(firstNonEmpty(j.FirstPublished, j.UpdatedAt)),
			})
		}
	}
	return all, joinErrs(errs)
}

// ---------- Lever ----------

type Lever struct{ Slugs []string }

func (l Lever) Name() string { return "lever" }

func (l Lever) Fetch(ctx context.Context) ([]model.Vacancy, error) {
	var all []model.Vacancy
	var errs []string
	for _, slug := range l.Slugs {
		var jobs []struct {
			ID         string `json:"id"`
			Text       string `json:"text"`
			HostedURL  string `json:"hostedUrl"`
			CreatedAt  int64  `json:"createdAt"`
			Workplace  string `json:"workplaceType"`
			DescPlain  string `json:"descriptionPlain"`
			Categories struct {
				Location string `json:"location"`
			} `json:"categories"`
			SalaryRange *struct {
				Currency string  `json:"currency"`
				Interval string  `json:"interval"`
				Min      float64 `json:"min"`
				Max      float64 `json:"max"`
			} `json:"salaryRange"`
		}
		u := "https://api.lever.co/v0/postings/" + url.PathEscape(slug) + "?mode=json"
		if err := getJSON(ctx, u, &jobs); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", slug, err))
			continue
		}
		for _, j := range jobs {
			v := model.Vacancy{
				Source:      "lever:" + slug,
				ExternalID:  j.ID,
				URL:         j.HostedURL,
				Title:       j.Text,
				Company:     slug,
				Description: j.DescPlain,
				Location:    j.Categories.Location,
				Remote:      j.Workplace == "remote" || isRemote(j.Categories.Location),
				Language:    detectLang(j.Text + " " + j.DescPlain),
				PublishedAt: time.UnixMilli(j.CreatedAt),
			}
			if s := j.SalaryRange; s != nil {
				perYear := strings.Contains(s.Interval, "year")
				v.SalaryFrom, v.SalaryTo, v.Currency = monthly(s.Min, perYear), monthly(s.Max, perYear), s.Currency
			}
			all = append(all, v)
		}
	}
	return all, joinErrs(errs)
}

// ---------- Ashby ----------

type Ashby struct{ Slugs []string }

func (a Ashby) Name() string { return "ashby" }

func (a Ashby) Fetch(ctx context.Context) ([]model.Vacancy, error) {
	var all []model.Vacancy
	var errs []string
	for _, slug := range a.Slugs {
		var resp struct {
			Jobs []struct {
				ID        string `json:"id"`
				Title     string `json:"title"`
				Location  string `json:"location"`
				IsListed  bool   `json:"isListed"`
				IsRemote  bool   `json:"isRemote"`
				Published string `json:"publishedAt"`
				JobURL    string `json:"jobUrl"`
				DescPlain string `json:"descriptionPlain"`
			} `json:"jobs"`
		}
		u := "https://api.ashbyhq.com/posting-api/job-board/" + url.PathEscape(slug)
		if err := getJSON(ctx, u, &resp); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", slug, err))
			continue
		}
		for _, j := range resp.Jobs {
			if !j.IsListed {
				continue
			}
			all = append(all, model.Vacancy{
				Source:      "ashby:" + slug,
				ExternalID:  j.ID,
				URL:         j.JobURL,
				Title:       j.Title,
				Company:     slug,
				Description: j.DescPlain,
				Location:    j.Location,
				Remote:      j.IsRemote || isRemote(j.Location),
				Language:    detectLang(j.Title + " " + j.DescPlain),
				PublishedAt: parseTime(j.Published),
			})
		}
	}
	return all, joinErrs(errs)
}

// ---------- общее ----------

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.000-07:00", "2006-01-02T15:04:05.000Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// joinErrs: ошибки отдельных компаний не должны ронять весь источник —
// вакансии остальных компаний возвращаются вместе с ошибкой.
func joinErrs(errs []string) error {
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}

// Build собирает источники из конфигурации.
func Build(c config.Sources) []Source {
	var out []Source
	if len(c.Greenhouse) > 0 {
		out = append(out, Greenhouse{Slugs: c.Greenhouse})
	}
	if len(c.Lever) > 0 {
		out = append(out, Lever{Slugs: c.Lever})
	}
	if len(c.Ashby) > 0 {
		out = append(out, Ashby{Slugs: c.Ashby})
	}
	if len(c.HabrCareer) > 0 {
		out = append(out, HabrCareer{Queries: c.HabrCareer})
	}
	for _, ch := range c.Telegram { // отдельный источник на канал: алерты и ошибки по каждому каналу
		out = append(out, TelegramChannels{Channels: []string{ch}})
	}
	return out
}
