package match

import (
	"context"
	"strings"
	"testing"

	"github.com/anastasia/job-bot/internal/model"
)

var skills = []string{"symfony", "kafka", "rabbitmq", "elasticsearch", "kubernetes", "microservices", "high-load"}

func assess(t *testing.T, v model.Vacancy) Assessment {
	t.Helper()
	a, err := RuleAssessor{Skills: skills, SalaryMinUSD: 3000}.Assess(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAssessOrdering(t *testing.T) {
	strong := assess(t, model.Vacancy{Title: "Senior PHP Developer", Remote: true,
		Description: "PHP 8, Symfony, Kafka, RabbitMQ, Elasticsearch, microservices, high-load", SalaryTo: 4000})
	weak := assess(t, model.Vacancy{Title: "Программист Bitrix24", Description: "PHP, Битрикс"})
	other := assess(t, model.Vacancy{Title: "Java Developer", Description: "Spring"})
	middle := assess(t, model.Vacancy{Title: "Middle PHP Developer", Description: "PHP, Symfony"})

	if strong.Score < 80 {
		t.Errorf("strong=%d, want >= 80 (%s)", strong.Score, strong.Note)
	}
	if !(strong.Score > middle.Score && middle.Score > weak.Score && weak.Score >= other.Score) {
		t.Errorf("ordering broken: strong=%d middle=%d weak=%d other=%d", strong.Score, middle.Score, weak.Score, other.Score)
	}
	if len(strong.Matched) < 5 {
		t.Errorf("matched=%v", strong.Matched)
	}
}

func TestAssessGoOnlyIsLowerThanPHP(t *testing.T) {
	php := assess(t, model.Vacancy{Title: "Senior PHP Developer", Description: "PHP, Kafka"})
	goo := assess(t, model.Vacancy{Title: "Senior Go Developer", Description: "Go, Kafka"})
	if goo.Score >= php.Score {
		t.Errorf("go=%d should be below php=%d", goo.Score, php.Score)
	}
}

func candidate(t *testing.T) *Candidate {
	t.Helper()
	c, err := LoadCandidate("../../candidate.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLetterRU(t *testing.T) {
	v := model.Vacancy{Title: "Senior PHP Developer", Company: "Acme", Language: "ru",
		Description: "PHP, Symfony, Elasticsearch, высокая нагрузка. Можно как самозанятый."}
	a := assess(t, v)
	l, err := TemplateWriter{C: candidate(t)}.Write(context.Background(), v, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Здравствуйте!", "«Senior PHP Developer»", "в компании Acme", "Elasticsearch", "самозанятая", "С уважением,\nАнна"} {
		if !strings.Contains(l, want) {
			t.Errorf("letter misses %q:\n%s", want, l)
		}
	}
	if strings.Contains(l, "+375") || strings.Contains(l, "@") {
		t.Errorf("letter must not contain contacts:\n%s", l)
	}
}

func TestLetterEN(t *testing.T) {
	v := model.Vacancy{Title: "Senior Backend Engineer", Company: "t.me/opento_dev", Language: "en",
		Description: "Go, Kubernetes, microservices"}
	l, err := TemplateWriter{C: candidate(t)}.Write(context.Background(), v, assess(t, v))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(l, "Hello,") || strings.Contains(l, "t.me/") || !strings.Contains(l, "Go in production") {
		t.Errorf("unexpected letter:\n%s", l)
	}
	if !strings.HasSuffix(l, "Best regards,\nAnna") {
		t.Errorf("English letter must be signed in Latin letters:\n%s", l)
	}
	if strings.Contains(l, "contractor") {
		t.Errorf("must not mention contractor when vacancy does not:\n%s", l)
	}
}

func TestLetterPicksThreeByTags(t *testing.T) {
	c := candidate(t)
	picked := TemplateWriter{C: c}.pick("elasticsearch, mongodb incident, kubernetes deploy")
	if len(picked) != 3 {
		t.Fatalf("picked=%v", picked)
	}
}
