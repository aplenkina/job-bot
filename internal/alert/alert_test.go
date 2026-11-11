package alert

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func setup() (*slog.Logger, *Handler, *[]string, *time.Time) {
	h := New(slog.NewTextHandler(io.Discard, nil))
	var got []string
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	h.st.now = func() time.Time { return now }
	h.st.run = func(f func()) { f() }
	h.SetSender(func(t string) { got = append(got, t) })
	return slog.New(h), h, &got, &now
}

func TestErrorsAreSentButNotInfoOrWarn(t *testing.T) {
	log, _, got, _ := setup()
	log.Info("обычное сообщение")
	log.Warn("предупреждение", "err", "x")
	log.Error("store insert", "err", "disk full", "url", "https://x.test/1")
	if len(*got) != 1 || !strings.Contains((*got)[0], "store insert") || !strings.Contains((*got)[0], "disk full") {
		t.Fatalf("got %v", *got)
	}
	if !strings.HasPrefix((*got)[0], "❗ ") {
		t.Errorf("error message must start with the red exclamation mark: %q", (*got)[0])
	}
}

func TestSameErrorIsDeduplicated(t *testing.T) {
	log, _, got, now := setup()
	for i := 0; i < 5; i++ {
		log.Error("store insert", "err", "disk full", "url", "u")
	}
	if len(*got) != 1 {
		t.Fatalf("same error must be sent once, got %d", len(*got))
	}
	// другая ошибка проходит сразу
	log.Error("store insert", "err", "locked")
	if len(*got) != 2 {
		t.Fatalf("different error must pass, got %d", len(*got))
	}
	// после окна — снова
	*now = now.Add(31 * time.Minute)
	log.Error("store insert", "err", "disk full")
	if len(*got) != 3 {
		t.Fatalf("same error after the window must pass, got %d", len(*got))
	}
}

func TestHourlyCap(t *testing.T) {
	log, _, got, _ := setup()
	for i := 0; i < 30; i++ {
		log.Error("boom", "err", strings.Repeat("e", i+1)) // все разные
	}
	if len(*got) != maxPerHour {
		t.Fatalf("want cap %d, got %d", maxPerHour, len(*got))
	}
}

func TestSilentAndNoSender(t *testing.T) {
	log, h, got, _ := setup()
	log.Error("send vacancy", "err", "forbidden") // про сам Telegram — не пересылаем
	log.Error("alert send failed", "err", "x")
	if len(*got) != 0 {
		t.Fatalf("silent messages must not be sent: %v", *got)
	}
	h.SetSender(nil)
	log.Error("boom", "err", "x") // без отправителя — просто в лог, без паники
}

func TestSenderPanicDoesNotCrash(t *testing.T) {
	h := New(slog.NewTextHandler(io.Discard, nil))
	h.st.run = func(f func()) { f() }
	h.SetSender(func(string) { panic("telegram down") })
	slog.New(h).Error("boom", "err", "x") // не должно упасть
}

func TestWithAttrsKeepsSharedState(t *testing.T) {
	_, h, got, _ := setup()
	l := slog.New(h).With("component", "pipeline")
	l.Error("boom", "err", "x")
	l.Error("boom", "err", "x")
	if len(*got) != 1 {
		t.Fatalf("dedupe state must be shared between derived loggers, got %d", len(*got))
	}
}
