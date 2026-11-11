// preview: отправляет в ваш Telegram пример карточки и письма, чтобы оценить оформление.
// В базу ничего не пишет.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/anastasia/job-bot/internal/bot"
	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/match"
	"github.com/anastasia/job-bot/internal/model"
	"github.com/anastasia/job-bot/internal/store"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "путь к профилю поиска")
	flag.Parse()
	if err := run(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	st, err := store.Open(os.TempDir() + "/jobbot-preview.db")
	if err != nil {
		return err
	}
	defer st.Close()
	b, err := bot.New(cfg.Secrets.TelegramToken, cfg.Secrets.TelegramChat, st, cfg.Profile)
	if err != nil {
		return err
	}
	cand, err := match.LoadCandidate(cfg.Profile.CandidateFile)
	if err != nil {
		return err
	}

	v := model.Vacancy{
		Source: "tg:GolangJobsit", URL: "https://t.me/GolangJobsit/1144", Title: "Senior Golang разработчик",
		Company: "Top Selection", Location: "Формат работы: можно удалённо", Remote: true,
		SalaryFrom: 245000, SalaryTo: 260000, Currency: "RUR", Language: "ru",
		Description: "Go, Kafka, RabbitMQ, Kubernetes, микросервисы. Можно как самозанятый.",
		PublishedAt: time.Now().Add(-2 * time.Hour),
	}
	a, err := match.RuleAssessor{Skills: cfg.Profile.Skills, SalaryMinUSD: cfg.Profile.SalaryMinUSD}.Assess(context.Background(), v)
	if err != nil {
		return err
	}
	letter, err := match.NewWriter(cfg.Profile.LLM, cand).Write(context.Background(), v, a)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := b.SendVacancy(ctx, store.Row{ID: 0, Score: a.Score, Note: a.Note, Notes: "до ~$3250/мес", Letter: letter, Vacancy: v}); err != nil {
		return err
	}
	fmt.Println("пример отправлен")
	return nil
}
