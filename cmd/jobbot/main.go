package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/anastasia/job-bot/internal/alert"
	"github.com/anastasia/job-bot/internal/bot"
	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/filter"
	"github.com/anastasia/job-bot/internal/match"
	"github.com/anastasia/job-bot/internal/pipeline"
	"github.com/anastasia/job-bot/internal/source"
	"github.com/anastasia/job-bot/internal/store"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "путь к профилю поиска")
	flag.Parse()

	alerts := alert.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(slog.New(alerts))

	if err := run(*cfgPath, alerts); err != nil {
		// Бот мог не успеть запуститься (плохой конфиг), поэтому об этом сообщает ещё и сторож systemd.
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// notifyNow отправляет служебное сообщение с собственным таймаутом (контекст бота к этому моменту может быть отменён).
func notifyNow(b *bot.Bot, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Notify(ctx, text); err != nil {
		slog.Warn("служебное сообщение не отправлено", "err", err)
	}
}

// guard перехватывает панику: сообщает в Telegram и завершает процесс с ошибкой,
// чтобы systemd перезапустил бота. Без этого паника в горутине убила бы процесс молча.
func guard(b *bot.Bot, where string) {
	if r := recover(); r != nil {
		stack := string(debug.Stack())
		slog.Error("panic", "where", where, "panic", r, "stack", stack)
		notifyNow(b, fmt.Sprintf("❗ Бот упал из-за паники (%s): %v\nsystemd перезапустит его. Подробности: journalctl -u jobbot", where, r))
		os.Exit(2)
	}
}

func run(cfgPath string, alerts *alert.Handler) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.Secrets.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	b, err := bot.New(cfg.Secrets.TelegramToken, cfg.Secrets.TelegramChat, st, cfg.Profile)
	if err != nil {
		return err
	}

	// ошибки уровня ERROR из лога дублируем в Telegram (с защитой от спама)
	alerts.SetSender(func(text string) { notifyNow(b, text) })

	flt, err := filter.New(cfg.Profile)
	if err != nil {
		return err
	}
	cand, err := match.LoadCandidate(cfg.Profile.CandidateFile)
	if err != nil {
		return err
	}
	writer := match.NewWriter(cfg.Profile.LLM, cand)
	if ow, ok := writer.(*match.OllamaWriter); ok {
		chk, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := ow.Check(chk); err != nil {
			slog.Warn("локальная модель не готова, письма будут по шаблону", "err", err)
			_ = b.Notify(context.Background(), "❗ Локальная модель не готова: "+err.Error()+"\nПока письма пишутся по шаблону.")
		} else {
			slog.Info("локальная модель готова", "model", ow.Model)
		}
		cancel()
	}
	sources := source.Build(cfg.Profile.Sources)
	if creds := config.IMAPFromEnv(); creds.User != "" {
		mailSrc, err := source.NewMail(cfg.Profile.Sources.Mail, creds)
		if err != nil {
			return err
		}
		sources = append(sources, mailSrc)
	}
	pl := &pipeline.Pipeline{
		Store:       st,
		Filter:      flt,
		Sources:     sources,
		Notifier:    b,
		Assessor:    match.RuleAssessor{Skills: cfg.Profile.Skills, SalaryMinUSD: cfg.Profile.SalaryMinUSD},
		Writer:      writer,
		Threshold:   cfg.Profile.MatchThreshold,
		Interval:    time.Duration(cfg.Profile.PollMinutes) * time.Minute,
		MaxPerCycle: cfg.Profile.MaxPerCycle,
		SendDelay:   1100 * time.Millisecond,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := b.Notify(ctx, "Бот запущен. /status — состояние."); err != nil {
		slog.Warn("startup notification failed", "err", err)
	}
	slog.Info("bot started", "sources", len(pl.Sources), "poll", pl.Interval)
	go func() {
		defer guard(b, "цикл сбора вакансий")
		pl.Run(ctx)
	}()
	func() {
		defer guard(b, "приём команд Telegram")
		b.Run(ctx) // блокируется до сигнала остановки
	}()
	slog.Info("bot stopped")
	notifyNow(b, "Бот остановлен (получен сигнал остановки). Если вы его не перезапускали, проверьте сервер.")
	return nil
}
