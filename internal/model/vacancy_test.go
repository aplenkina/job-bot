package model

import "testing"

func TestContainsWord(t *testing.T) {
	yes := [][2]string{{"VK Tech", "VK"}, {"Ozon.Tech", "ozon"}, {"ООО «Озон Банк»", "Озон"}, {"Группа VK", "vk"}, {"Т-Банк (Тинькофф)", "Т-Банк"}}
	no := [][2]string{{"DEVKIT", "VK"}, {"Ozonetel", "Ozon"}, {"Яндекс", "VK"}, {"anything", ""}}
	for _, c := range yes {
		if !ContainsWord(c[0], c[1]) {
			t.Errorf("%q must contain %q", c[0], c[1])
		}
	}
	for _, c := range no {
		if ContainsWord(c[0], c[1]) {
			t.Errorf("%q must NOT contain %q", c[0], c[1])
		}
	}
}

func TestMentionsLooksAtCompanyTitleAndTextHead(t *testing.T) {
	v := Vacancy{Company: "t.me/x", Title: "PHP", Description: "Команда Ozon Tech"}
	if !v.Mentions("Ozon") || v.Mentions("Avito") {
		t.Error("Mentions must find the company in the description head only as a word")
	}
	long := Vacancy{Description: string(make([]rune, 700)) + " Ozon"}
	if long.Mentions("Ozon") {
		t.Error("only the first 600 characters of the text are examined")
	}
}
