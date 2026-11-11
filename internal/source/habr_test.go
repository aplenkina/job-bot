package source

import "testing"

const habrSample = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel><title>Вакансии</title>
<item>
  <title>Требуется «Backend-разработчик BItrix/PHP (cтажер)» (Москва)</title>
  <description>Компания «ИТ-компания «Лоция»» ищет специалиста. Москва (Россия). Можно удалённо. Требуемые навыки: #intern, #PHP.</description>
  <author>ИТ-компания «Лоция»</author>
  <pubDate>Fri, 06 Nov 2026 10:28:00 +0300</pubDate>
  <link>https://career.habr.com/vacancies/1000168358</link>
  <guid>1000168358</guid>
</item>
<item>
  <title>Требуется «Golang разработчик (с опытом в PHP)»</title>
  <description>Компания «Employ City» ищет специалиста. Полный рабочий день. Требуемые навыки: #senior, #Golang, #PHP.</description>
  <author>Employ City</author>
  <pubDate>Thu, 05 Nov 2026 15:41:50 +0300</pubDate>
  <link>https://career.habr.com/vacancies/1000169586</link>
  <guid>1000169586</guid>
</item>
<item><title>Без ссылки</title><guid></guid></item>
</channel></rss>`

func TestParseHabrRSS(t *testing.T) {
	vs, err := parseHabrRSS([]byte(habrSample))
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 {
		t.Fatalf("got %d: %+v", len(vs), vs)
	}
	a, b := vs[0], vs[1]
	if a.Title != "Backend-разработчик BItrix/PHP (cтажер)" || a.Location != "Москва" || !a.Remote ||
		a.Company != "ИТ-компания «Лоция»" || a.ExternalID != "1000168358" || a.Source != "habr" || a.PublishedAt.IsZero() {
		t.Errorf("unexpected a: %+v", a)
	}
	if b.Title != "Golang разработчик (с опытом в PHP)" || b.Location != "" || b.Remote {
		t.Errorf("unexpected b: %+v", b)
	}
}

func TestParseHabrRSSBroken(t *testing.T) {
	if _, err := parseHabrRSS([]byte("<html>not rss")); err == nil {
		t.Fatal("broken feed must return an error")
	}
}
