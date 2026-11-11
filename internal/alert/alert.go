// Package alert пересылает записи лога уровня ERROR в Telegram с защитой от спама:
// одинаковые ошибки не чаще раза в полчаса, не больше десяти сообщений в час.
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

const (
	dedupeWindow = 30 * time.Minute
	maxPerHour   = 10
)

// silent — ошибки, которые нельзя или бессмысленно пересылать: они сами про отправку в Telegram
// (канал недоступен — сообщение не дойдёт) или уже имеют собственное оповещение.
var silent = map[string]bool{
	"send vacancy":                true,
	"alert send failed":           true,
	"startup notification failed": true,
	"telegram send failed":        true,
}

type shared struct {
	mu   sync.Mutex
	send func(text string)
	seen map[string]time.Time
	sent []time.Time
	now  func() time.Time
	run  func(func()) // как запускать отправку; в тестах — синхронно
}

// Handler оборачивает обычный обработчик slog.
type Handler struct {
	inner slog.Handler
	st    *shared
}

// New создаёт обработчик. Пока не вызван SetSender, ошибки только пишутся в лог.
func New(inner slog.Handler) *Handler {
	return &Handler{inner: inner, st: &shared{
		seen: map[string]time.Time{},
		now:  time.Now,
		run:  func(f func()) { go f() },
	}}
}

// SetSender задаёт функцию отправки (например, бота).
func (h *Handler) SetSender(send func(text string)) {
	h.st.mu.Lock()
	defer h.st.mu.Unlock()
	h.st.send = send
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *Handler) WithAttrs(a []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(a), st: h.st}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), st: h.st}
}

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	err := h.inner.Handle(ctx, r)
	if r.Level >= slog.LevelError && !silent[r.Message] {
		h.st.maybeSend(r)
	}
	return err
}

func (s *shared) maybeSend(r slog.Record) {
	var attrs []string
	key := r.Message
	r.Attrs(func(a slog.Attr) bool {
		v := fmt.Sprint(a.Value.Any())
		attrs = append(attrs, a.Key+"="+truncate(v, 200))
		if a.Key == "err" {
			key += "|" + v // одинаковая ошибка — одно сообщение, а url у каждой свой
		}
		return true
	})

	s.mu.Lock()
	send := s.send
	now := s.now()
	if send == nil {
		s.mu.Unlock()
		return
	}
	if t, ok := s.seen[key]; ok && now.Sub(t) < dedupeWindow {
		s.mu.Unlock()
		return
	}
	// не больше maxPerHour сообщений в час
	recent := s.sent[:0]
	for _, t := range s.sent {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	s.sent = recent
	if len(s.sent) >= maxPerHour {
		s.mu.Unlock()
		return
	}
	s.sent = append(s.sent, now)
	s.seen[key] = now
	s.mu.Unlock()

	text := "❗ Ошибка в боте: " + r.Message
	if len(attrs) > 0 {
		text += "\n" + strings.Join(attrs, "\n")
	}
	text += "\nПодробности: journalctl -u jobbot"
	s.run(func() {
		defer func() { _ = recover() }() // алерт не должен ронять бота
		send(truncate(text, 900))
	})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
