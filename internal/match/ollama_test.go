package match

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anastasia/job-bot/internal/config"
	"github.com/anastasia/job-bot/internal/model"
)

const goodRU = `Здравствуйте!

Откликаюсь на вакансию «Senior PHP Developer» в компании Acme. Я Senior PHP-разработчик, 10 лет занимаюсь backend-разработкой в e-commerce. Отвечаю за 24 микросервиса на PHP 8 и Symfony с нагрузкой около 3 600 RPS, а также ускорила API листингов товаров в 2,7 раза с помощью отдельного индекса в Elasticsearch. Мне близки ваши задачи по высоконагруженным сервисам, и я с радостью расскажу о своём опыте подробнее на звонке.

С уважением,
Анна`

func fakeOllama(t *testing.T, reply string, status int, captured *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_, _ = rw.Write([]byte(`{"models":[{"name":"qwen2.5:7b-instruct"}]}`))
		case "/api/chat":
			if captured != nil {
				_ = json.NewDecoder(r.Body).Decode(captured)
			}
			rw.WriteHeader(status)
			b, _ := json.Marshal(map[string]any{"message": map[string]string{"role": "assistant", "content": reply}})
			_, _ = rw.Write(b)
		default:
			rw.WriteHeader(404)
		}
	}))
}

func writerFor(t *testing.T, url string) *OllamaWriter {
	t.Helper()
	w := NewWriter(config.LLM{Provider: "ollama", OllamaURL: url, TimeoutSeconds: 5}, candidate(t))
	ow, ok := w.(*OllamaWriter)
	if !ok {
		t.Fatalf("expected OllamaWriter, got %T", w)
	}
	return ow
}

var ruVacancy = model.Vacancy{Title: "Senior PHP Developer", Company: "Acme", Language: "ru",
	Description: "PHP, Symfony, Elasticsearch, высокая нагрузка"}

func TestOllamaGoodLetterIsUsed(t *testing.T) {
	var req map[string]any
	srv := fakeOllama(t, goodRU, 200, &req)
	defer srv.Close()
	w := writerFor(t, srv.URL)
	got, err := w.Write(context.Background(), ruVacancy, Assessment{})
	if err != nil || got != goodRU {
		t.Fatalf("letter must come from the model: err=%v\n%s", err, got)
	}
	// в запросе — факты кандидата и вакансия, без контактов
	msgs := req["messages"].([]any)
	user := msgs[1].(map[string]any)["content"].(string)
	for _, want := range []string{"CANDIDATE FACTS", "Senior PHP Developer", "Elasticsearch", "SIGNATURE: Анна"} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt misses %q", want)
		}
	}
	if strings.Contains(user, "@") || strings.Contains(user, "+375") {
		t.Error("prompt must not contain contacts")
	}
}

func TestOllamaFallsBackOnInventedNumber(t *testing.T) {
	bad := strings.Replace(goodRU, "10 лет", "15 лет", 1) // в резюме такого нет
	srv := fakeOllama(t, bad, 200, nil)
	defer srv.Close()
	got, err := writerFor(t, srv.URL).Write(context.Background(), ruVacancy, Assessment{Matched: []string{"symfony"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "15 лет") || !strings.Contains(got, "Что может быть полезно для вас") {
		t.Fatalf("must fall back to the template:\n%s", got)
	}
}

func TestOllamaFallsBackOnWrongLanguageContactsAndErrors(t *testing.T) {
	cases := map[string]struct {
		reply  string
		status int
	}{
		"english for ru vacancy": {strings.Repeat("Hello and best regards from a developer. ", 12), 200},
		"contacts":               {goodRU + "\nПишите: anastasia@example.com", 200},
		"too short":              {"Здравствуйте! Хочу работать.", 200},
		"server error":           {"oops", 500},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := fakeOllama(t, c.reply, c.status, nil)
			defer srv.Close()
			got, err := writerFor(t, srv.URL).Write(context.Background(), ruVacancy, Assessment{})
			if err != nil || !strings.Contains(got, "Что может быть полезно для вас") {
				t.Fatalf("expected template fallback, err=%v:\n%s", err, got)
			}
		})
	}
}

func TestOllamaUnreachableFallsBack(t *testing.T) {
	srv := fakeOllama(t, "", 200, nil)
	url := srv.URL
	srv.Close() // сервер выключен
	got, err := writerFor(t, url).Write(context.Background(), ruVacancy, Assessment{})
	if err != nil || !strings.Contains(got, "Здравствуйте!") {
		t.Fatalf("err=%v\n%s", err, got)
	}
}

func TestOllamaCheck(t *testing.T) {
	srv := fakeOllama(t, "", 200, nil)
	defer srv.Close()
	w := writerFor(t, srv.URL)
	if err := w.Check(context.Background()); err != nil {
		t.Fatalf("model is installed: %v", err)
	}
	w.Model = "other-model"
	if err := w.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "ollama pull") {
		t.Fatalf("missing model must give a hint: %v", err)
	}
}

func TestNoneProviderGivesTemplate(t *testing.T) {
	if _, ok := NewWriter(config.LLM{}, candidate(t)).(TemplateWriter); !ok {
		t.Fatal("default provider must be the template writer")
	}
}
