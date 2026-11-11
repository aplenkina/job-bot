// dryrun: показывает, какие вакансии прошли бы фильтры прямо сейчас.
// Ничего не пишет в базу и не отправляет в Telegram.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/filter"
	"github.com/anastasia/job-bot/internal/match"
	"github.com/anastasia/job-bot/internal/model"
	"github.com/anastasia/job-bot/internal/source"
)

func main() {
	path := flag.String("config", "config.yaml", "путь к профилю поиска")
	flag.Bool("letters", false, "показывать черновики писем")
	maxAge := flag.Int("max-age-hours", 0, "переопределить max_age_hours (0 = из профиля)")
	flag.Parse()

	prof, err := config.LoadProfile(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *maxAge > 0 {
		prof.MaxAgeHours = *maxAge
	}
	f, err := filter.New(prof)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cand, err := match.LoadCandidate(prof.CandidateFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	assessor := match.RuleAssessor{Skills: prof.Skills, SalaryMinUSD: prof.SalaryMinUSD}
	writer := match.NewWriter(prof.LLM, cand)
	showLetters := flag.Lookup("letters").Value.String() == "true"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	reasons := map[string]int{}
	var shown, total int
	sources := source.Build(prof.Sources)
	if creds := config.IMAPFromEnv(); creds.User != "" {
		mailSrc, err := source.NewMail(prof.Sources.Mail, creds)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		sources = append(sources, mailSrc)
	}
	for _, src := range sources {
		vs, err := src.Fetch(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN %s: %v\n", src.Name(), err)
		}
		for _, v := range vs {
			total++
			verdict := f.Check(v)
			if verdict.Status == model.StatusFiltered {
				reasons[verdict.Reason]++
				continue
			}
			a, _ := assessor.Assess(ctx, v)
			if a.Score < prof.MatchThreshold {
				reasons[fmt.Sprintf("score<%d", prof.MatchThreshold)]++
				continue
			}
			shown++
			fmt.Printf("[%s] %d/100 %s — %s (%s)\n    %s | %v\n    %s\n", verdict.Status, a.Score, v.Title, v.Company, v.Location, a.Note, verdict.Notes, v.URL)
			if showLetters {
				l, _ := writer.Write(ctx, v, a)
				fmt.Printf("    --- письмо ---\n%s\n    --------------\n", l)
			}
		}
	}
	fmt.Printf("\nВсего вакансий: %d, подошло: %d\nПричины отсева: %v\n", total, shown, reasons)
}
