package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProfile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadExample(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "x")
	t.Setenv("TELEGRAM_CHAT_ID", "123")
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profile.SalaryMinUSD != 3000 {
		t.Fatalf("salary: %d", cfg.Profile.SalaryMinUSD)
	}
	for _, c := range []string{"Ozon", "Авито", "Т-Банк", "VK", "Wildberries"} {
		if !cfg.Profile.IsParked(c) {
			t.Errorf("%s must be parked", c)
		}
	}
	if cfg.Profile.IsParked("Яндекс") {
		t.Error("Яндекс must not be parked")
	}
}

func TestLoadErrors(t *testing.T) {
	t.Setenv("TELEGRAM_CHAT_ID", "123")
	t.Setenv("TELEGRAM_TOKEN", "")
	if _, err := Load(writeProfile(t, "roles: [php]\nsalary_min_usd: 3000\n")); err == nil {
		t.Error("expected error for missing token")
	}
	t.Setenv("TELEGRAM_TOKEN", "x")
	if _, err := Load(writeProfile(t, "roles: [php]\n")); err == nil {
		t.Error("expected error for missing salary")
	}
	if _, err := Load(writeProfile(t, "salary_min_usd: 3000\n")); err == nil {
		t.Error("expected error for empty roles")
	}
}

func TestParkedMatchesWholeWordsOnly(t *testing.T) {
	p := Profile{ParkedCompanies: []string{"VK", "Ozon", "Озон", "Т-Банк"}, Blacklist: []string{"Acme"}}
	for _, c := range []string{"VK", "VK Tech", "Ozon.Tech", "ООО «Озон Банк»", "ozon", "Т-Банк (Тинькофф)", "Группа VK"} {
		if !p.IsParked(c) {
			t.Errorf("%q must be parked", c)
		}
	}
	for _, c := range []string{"DEVKIT Studio", "Tvkom", "Ozonetel", "Яндекс", "Avkus", ""} {
		if p.IsParked(c) {
			t.Errorf("%q must NOT be parked (совпадение внутри слова)", c)
		}
	}
	if !p.IsBlacklisted("Acme Corp") || p.IsBlacklisted("Acmeinc") {
		t.Error("blacklist must use the same whole-word rule")
	}
}
