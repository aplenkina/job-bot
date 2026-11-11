package pipeline

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/filter"
	"github.com/anastasia/job-bot/internal/match"
	"github.com/anastasia/job-bot/internal/model"
	"github.com/anastasia/job-bot/internal/source"
	"github.com/anastasia/job-bot/internal/store"
)

type fakeSource struct{ vs []model.Vacancy }

func (f fakeSource) Name() string                                   { return "fake" }
func (f fakeSource) Fetch(context.Context) ([]model.Vacancy, error) { return f.vs, nil }

type fakeNotifier struct {
	sent   []store.Row
	alerts []string
}

func (n *fakeNotifier) Notify(_ context.Context, text string) error {
	n.alerts = append(n.alerts, text)
	return nil
}

func (n *fakeNotifier) SendVacancy(_ context.Context, r store.Row) error {
	n.sent = append(n.sent, r)
	return nil
}

func setup(t *testing.T, vs []model.Vacancy) (*Pipeline, *fakeNotifier, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	prof := config.Profile{SalaryMinUSD: 3000, AllowUnknownPay: true, Remote: true,
		TitleRoles: []string{"developer"}, ParkedCompanies: []string{"Ozon"}, MaxAgeHours: 72}
	flt, err := filter.New(prof)
	if err != nil {
		t.Fatal(err)
	}
	n := &fakeNotifier{}
	return &Pipeline{Store: st, Filter: flt, Sources: []source.Source{fakeSource{vs}}, Notifier: n,
		Interval: time.Minute, MaxPerCycle: 10}, n, st
}

func vac(id, title, company string) model.Vacancy {
	return model.Vacancy{Source: "fake", ExternalID: id, URL: "u" + id, Title: title, Company: company,
		Description: "PHP", Remote: true, Location: "Remote", PublishedAt: time.Now()}
}

func TestPipelineNotifiesOnceAndParks(t *testing.T) {
	ctx := context.Background()
	p, n, st := setup(t, []model.Vacancy{
		vac("1", "PHP Developer", "Acme"),
		vac("2", "PHP Developer", "Ozon Tech"),
		vac("3", "Sales Manager", "Acme"),
	})
	p.RunOnce(ctx)
	p.RunOnce(ctx) // повторный цикл не должен дублировать уведомления

	if len(n.sent) != 1 || n.sent[0].ExternalID != "1" {
		t.Fatalf("sent=%+v", n.sent)
	}
	c, _ := st.Counts(ctx)
	if c[model.StatusNotified] != 1 || c[model.StatusParked] != 1 || c[model.StatusFiltered] != 1 {
		t.Fatalf("counts=%v", c)
	}
}

func TestPipelinePausedKeepsQueue(t *testing.T) {
	ctx := context.Background()
	p, n, st := setup(t, []model.Vacancy{vac("1", "PHP Developer", "Acme")})
	st.SetPaused(ctx, true)
	p.RunOnce(ctx)
	if len(n.sent) != 0 {
		t.Fatal("must not send while paused")
	}
	st.SetPaused(ctx, false)
	p.RunOnce(ctx)
	if len(n.sent) != 1 {
		t.Fatalf("must send after resume, sent=%d", len(n.sent))
	}
}

// flakySource отдаёт сценарий по циклам: ошибка или список вакансий.
type flakySource struct {
	steps []func() ([]model.Vacancy, error)
	i     int
}

func (f *flakySource) Name() string { return "flaky" }
func (f *flakySource) Fetch(context.Context) ([]model.Vacancy, error) {
	s := f.steps[min(f.i, len(f.steps)-1)]
	f.i++
	return s()
}

func TestSourceHealthAlerts(t *testing.T) {
	ctx := context.Background()
	fail := func() ([]model.Vacancy, error) { return nil, errors.New("status 503") }
	ok := func() ([]model.Vacancy, error) { return []model.Vacancy{vac("1", "PHP Developer", "Acme")}, nil }
	p, n, _ := setup(t, nil)
	p.Sources = []source.Source{&flakySource{steps: []func() ([]model.Vacancy, error){fail, fail, fail, fail, fail, ok}}}

	for i := 0; i < 2; i++ {
		p.RunOnce(ctx)
	}
	if len(n.alerts) != 0 {
		t.Fatalf("no alert before 3 failures: %v", n.alerts)
	}
	p.RunOnce(ctx) // третья ошибка подряд
	if len(n.alerts) == 1 && !strings.HasPrefix(n.alerts[0], "❗ ") {
		t.Fatalf("source failure alert must start with the red exclamation mark: %q", n.alerts[0])
	}
	if len(n.alerts) != 1 || !strings.Contains(n.alerts[0], "503") {
		t.Fatalf("want one failure alert: %v", n.alerts)
	}
	p.RunOnce(ctx)
	p.RunOnce(ctx) // дальше молчим, пока не восстановится
	if len(n.alerts) != 1 {
		t.Fatalf("alert must not repeat: %v", n.alerts)
	}
	p.RunOnce(ctx) // источник ожил
	if len(n.alerts) != 2 || !strings.Contains(n.alerts[1], "снова работает") {
		t.Fatalf("want recovery message: %v", n.alerts)
	}
}

func TestEmptySourceAlertOnlyAfterData(t *testing.T) {
	ctx := context.Background()
	empty := func() ([]model.Vacancy, error) { return nil, nil }
	ok := func() ([]model.Vacancy, error) { return []model.Vacancy{vac("1", "PHP Developer", "Acme")}, nil }

	// источник всегда пуст и данных не было — не считаем поломкой
	p, n, _ := setup(t, nil)
	p.Sources = []source.Source{&flakySource{steps: []func() ([]model.Vacancy, error){empty}}}
	for i := 0; i < 8; i++ {
		p.RunOnce(ctx)
	}
	if len(n.alerts) != 0 {
		t.Fatalf("never-seen-data source must not alert: %v", n.alerts)
	}

	// источник отдавал данные, потом замолчал — алерт
	p2, n2, _ := setup(t, nil)
	steps := []func() ([]model.Vacancy, error){ok, empty, empty, empty, empty, empty, empty}
	p2.Sources = []source.Source{&flakySource{steps: steps}}
	for i := 0; i < len(steps); i++ {
		p2.RunOnce(ctx)
	}
	if len(n2.alerts) != 1 || !strings.Contains(n2.alerts[0], "0 вакансий") {
		t.Fatalf("want empty-source alert: %v", n2.alerts)
	}
}

type quietFlaky struct{ flakySource }

func (q *quietFlaky) QuietOK() bool { return true }

func TestQuietSourceNeverAlertsAboutSilence(t *testing.T) {
	ctx := context.Background()
	ok := func() ([]model.Vacancy, error) { return []model.Vacancy{vac("1", "PHP Developer", "Acme")}, nil }
	empty := func() ([]model.Vacancy, error) { return nil, nil }
	p, n, _ := setup(t, nil)
	steps := []func() ([]model.Vacancy, error){ok, empty}
	p.Sources = []source.Source{&quietFlaky{flakySource{steps: steps}}}
	for i := 0; i < 12; i++ {
		p.RunOnce(ctx)
	}
	if len(n.alerts) != 0 {
		t.Fatalf("a quiet source (mail) must not alert when it is silent: %v", n.alerts)
	}
}

func TestMailVacanciesBypassScoreThreshold(t *testing.T) {
	ctx := context.Background()
	p, n, st := setup(t, nil)
	p.Assessor = match.RuleAssessor{Skills: []string{"symfony"}, SalaryMinUSD: 3000}
	p.Threshold = 35
	p.Filter = mustFilter(t, config.Profile{SalaryMinUSD: 3000, AllowUnknownPay: true, Remote: true, MaxAgeHours: 72})
	// у письма скудный текст, оценка по нему низкая (около 5), но вакансию отобрал сохранённый поиск на сайте
	mailVac := model.Vacancy{Source: "mail:hh.ru", ExternalID: "1", URL: "https://hh.ru/vacancy/1", Title: "Руководитель отдела",
		Company: "Acme", Description: "Acme", Remote: true, PublishedAt: time.Now()}
	// обычная вакансия с такой же низкой оценкой (PHP 25 + удалёнка 5 = 30 < 35) порог не проходит
	siteVac := model.Vacancy{Source: "greenhouse:x", ExternalID: "2", URL: "https://x.test/2", Title: "PHP Developer",
		Company: "Acme", Description: "PHP", Remote: true, PublishedAt: time.Now()}
	p.Sources = []source.Source{fakeSource{[]model.Vacancy{mailVac, siteVac}}}
	p.RunOnce(ctx)
	c, _ := st.Counts(ctx)
	if len(n.sent) != 1 || n.sent[0].ExternalID != "1" || c[model.StatusNotified] != 1 || c[model.StatusFiltered] != 1 {
		t.Fatalf("mail vacancy must bypass the threshold, the site one must not: sent=%+v counts=%v", n.sent, c)
	}
}

func mustFilter(t *testing.T, p config.Profile) *filter.Filter {
	t.Helper()
	f, err := filter.New(p)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
