// Package pipeline связывает источники, фильтры, хранилище и уведомления.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/anastasia/job-bot/internal/filter"
	"github.com/anastasia/job-bot/internal/match"
	"github.com/anastasia/job-bot/internal/model"
	"github.com/anastasia/job-bot/internal/source"
	"github.com/anastasia/job-bot/internal/store"
)

// Notifier отправляет карточку вакансии пользователю.
type Notifier interface {
	SendVacancy(ctx context.Context, r store.Row) error
	Notify(ctx context.Context, text string) error // служебные сообщения (алерты)
}

// quietSource — источник, для которого пустой ответ нормален (например, почта: новых писем может не быть часами).
type quietSource interface{ QuietOK() bool }

// srcHealth — состояние источника для алертов о поломках.
type srcHealth struct {
	fails, empties int
	seenData       bool // источник хотя бы раз возвращал вакансии
	alerted        bool
}

const (
	failAlertAfter  = 3 // подряд неудачных циклов
	emptyAlertAfter = 6 // подряд циклов без единой вакансии после того, как данные были
)

type Pipeline struct {
	Store       *store.Store
	Filter      *filter.Filter
	Sources     []source.Source
	Notifier    Notifier
	Assessor    match.Assessor // оценка соответствия
	Writer      match.Writer   // черновики писем
	Threshold   int            // ниже этой оценки вакансию не показываем
	Interval    time.Duration
	MaxPerCycle int
	SendDelay   time.Duration // пауза между сообщениями (лимиты Telegram)

	health map[string]*srcHealth
}

// Run выполняет цикл сразу и затем по таймеру, пока ctx не отменён.
func (p *Pipeline) Run(ctx context.Context) {
	p.RunOnce(ctx)
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.RunOnce(ctx)
		}
	}
}

// RunOnce: собрать → отфильтровать → сохранить → разослать накопленные.
func (p *Pipeline) RunOnce(ctx context.Context) {
	p.collect(ctx)
	p.flush(ctx)
}

func (p *Pipeline) collect(ctx context.Context) {
	for _, src := range p.Sources {
		vacancies, err := src.Fetch(ctx)
		if err != nil {
			// ошибка одной компании не мешает остальным: вакансии могли прийти частично
			slog.Warn("source error", "source", src.Name(), "err", err)
		}
		q, _ := src.(quietSource)
		p.track(ctx, src.Name(), len(vacancies), err, q != nil && q.QuietOK())
		var added int
		for _, v := range vacancies {
			verdict := p.Filter.Check(v)
			ok, err := p.Store.Insert(ctx, v, verdict.Status, joinNotes(verdict.Notes), verdict.Reason)
			if err != nil {
				slog.Error("store insert", "err", err, "url", v.URL)
				continue
			}
			if ok {
				added++
			}
		}
		slog.Info("source done", "source", src.Name(), "fetched", len(vacancies), "new", added)
	}
}

// track считает неудачные и пустые циклы источника и присылает алерт один раз
// при поломке и один раз при восстановлении.
func (p *Pipeline) track(ctx context.Context, name string, fetched int, fetchErr error, quietOK bool) {
	if p.health == nil {
		p.health = map[string]*srcHealth{}
	}
	h := p.health[name]
	if h == nil {
		h = &srcHealth{}
		p.health[name] = h
	}
	switch {
	case fetchErr != nil && fetched == 0:
		h.fails++
	case fetched == 0:
		h.fails = 0
		h.empties++
	default:
		h.fails, h.empties, h.seenData = 0, 0, true
	}

	var msg string
	switch {
	case h.fails >= failAlertAfter && !h.alerted:
		msg = fmt.Sprintf("❗ Источник %q не отвечает уже %d циклов подряд.\nОшибка: %v", name, h.fails, fetchErr)
	case h.seenData && !quietOK && h.empties >= emptyAlertAfter && !h.alerted:
		msg = fmt.Sprintf("❗ Источник %q вернул 0 вакансий %d циклов подряд — возможно, изменился формат.", name, h.empties)
	case h.alerted && h.fails == 0 && h.empties == 0:
		h.alerted = false
		msg = fmt.Sprintf("✅ Источник %q снова работает.", name)
		if err := p.Notifier.Notify(ctx, msg); err != nil {
			slog.Warn("alert send failed", "err", err)
		}
		return
	}
	if msg != "" {
		h.alerted = true
		if err := p.Notifier.Notify(ctx, msg); err != nil {
			slog.Warn("alert send failed", "err", err)
		}
	}
}

func (p *Pipeline) flush(ctx context.Context) {
	paused, err := p.Store.Paused(ctx)
	if err != nil {
		slog.Error("read pause", "err", err)
		return
	}
	if paused {
		return
	}
	// Берём больше, чем MaxPerCycle: часть вакансий отсеется оценкой и в лимит уведомлений не входит.
	rows, err := p.Store.ListByStatus(ctx, model.StatusNew, 200)
	if err != nil {
		slog.Error("list new", "err", err)
		return
	}
	sent := 0
	for _, r := range rows {
		if sent >= p.MaxPerCycle {
			return
		}
		if p.Assessor != nil {
			a, err := p.Assessor.Assess(ctx, r.Vacancy)
			if err != nil {
				slog.Error("assess", "err", err, "url", r.URL)
				return
			}
			// письма от сайтов содержат мало текста, оценка по ним занижена; порог к ним не применяем
			if a.Score < p.Threshold && !r.Vacancy.Trusted() {
				if err := p.Store.Reject(ctx, r.ID, a.Score, fmt.Sprintf("score:%d", a.Score)); err != nil {
					slog.Error("reject", "err", err)
					return
				}
				continue
			}
			letter := ""
			if p.Writer != nil {
				if letter, err = p.Writer.Write(ctx, r.Vacancy, a); err != nil {
					slog.Error("write letter", "err", err, "url", r.URL)
					return
				}
			}
			if err := p.Store.SaveMatch(ctx, r.ID, a.Score, a.Note, letter); err != nil {
				slog.Error("save match", "err", err)
				return
			}
			r.Score, r.Note, r.Letter = a.Score, a.Note, letter
		}
		if err := p.Notifier.SendVacancy(ctx, r); err != nil {
			slog.Error("send vacancy", "err", err, "url", r.URL)
			return // повторим в следующем цикле, статус остался new
		}
		if err := p.Store.SetStatusByID(ctx, r.ID, model.StatusNotified); err != nil {
			slog.Error("mark notified", "err", err)
			return
		}
		sent++
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.SendDelay):
		}
	}
}

func joinNotes(n []string) string {
	out := ""
	for i, s := range n {
		if i > 0 {
			out += "; "
		}
		out += s
	}
	return out
}
