package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/anastasia/job-bot/internal/model"
)

// TelegramChannels читает публичные каналы через веб-превью https://t.me/s/<канал>
// (последние ~20 постов). Аккаунт Telegram и ключи не нужны; закрытые каналы недоступны.
type TelegramChannels struct{ Channels []string }

func (t TelegramChannels) Name() string {
	if len(t.Channels) == 1 { // по одному источнику на канал — алерты приходят по каждому каналу отдельно
		return "telegram:" + strings.TrimPrefix(strings.TrimSpace(t.Channels[0]), "@")
	}
	return "telegram"
}

func (t TelegramChannels) Fetch(ctx context.Context) ([]model.Vacancy, error) {
	var all []model.Vacancy
	var errs []string
	for _, ch := range t.Channels {
		ch = strings.TrimPrefix(strings.TrimSpace(ch), "@")
		page, err := fetchChannelPage(ctx, ch)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", ch, err))
			continue
		}
		vs, posts, err := parseChannelPage(page, ch)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", ch, err))
			continue
		}
		if posts == 0 {
			// Telegram отвечает 200 и для несуществующих, закрытых и переименованных каналов
			errs = append(errs, fmt.Sprintf("%s: в веб-превью нет постов — проверьте название, канал мог быть переименован или закрыт", ch))
			continue
		}
		all = append(all, vs...)
	}
	return all, joinErrs(errs)
}

func fetchChannelPage(ctx context.Context, channel string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://t.me/s/"+url.PathEscape(channel), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	return string(b), err
}

// parseChannelPage достаёт посты из HTML веб-превью и превращает их в вакансии.
// Второе значение — сколько всего постов найдено на странице (включая рекламу и посты без текста).
func parseChannelPage(page, channel string) ([]model.Vacancy, int, error) {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil, 0, err
	}
	var out []model.Vacancy
	posts := 0
	walk(doc, func(n *html.Node) bool {
		post := attr(n, "data-post")
		if n.Type != html.ElementNode || post == "" || !hasClass(n, "tgme_widget_message") {
			return true
		}
		_, idStr, found := strings.Cut(post, "/")
		if !found {
			return false
		}
		if _, err := strconv.Atoi(idStr); err != nil {
			return false
		}
		posts++
		var text string
		var published time.Time
		walk(n, func(c *html.Node) bool {
			if c.Type != html.ElementNode {
				return true
			}
			switch {
			case hasClass(c, "tgme_widget_message_text") && text == "":
				text = nodeText(c)
				return false
			case c.Data == "time" && published.IsZero():
				published = parseTime(attr(c, "datetime"))
			}
			return true
		})
		if strings.TrimSpace(text) == "" {
			return false
		}
		title, company, location, remote, ok := parsePost(text, channel)
		if !ok {
			return false
		}
		from, to, cur := parseSalary(text)
		out = append(out, model.Vacancy{
			Source:      "tg:" + channel,
			ExternalID:  idStr,
			URL:         "https://t.me/" + channel + "/" + idStr,
			Title:       title,
			Company:     company,
			Description: text,
			Location:    location,
			Remote:      remote,
			SalaryFrom:  from,
			SalaryTo:    to,
			Currency:    cur,
			Language:    detectLang(text),
			PublishedAt: published,
		})
		return false // внутрь поста второй раз не заходим
	})
	return out, posts, nil
}

// ---------- помощники по HTML ----------

func walk(n *html.Node, fn func(*html.Node) bool) {
	if !fn(n) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// nodeText собирает текст узла; <br> превращает в перевод строки.
func nodeText(n *html.Node) string {
	var sb strings.Builder
	var rec func(*html.Node)
	rec = func(c *html.Node) {
		switch {
		case c.Type == html.TextNode:
			sb.WriteString(c.Data)
		case c.Type == html.ElementNode && c.Data == "br":
			sb.WriteString("\n")
		}
		for ch := c.FirstChild; ch != nil; ch = ch.NextSibling {
			rec(ch)
		}
	}
	rec(n)
	return strings.TrimSpace(sb.String())
}
