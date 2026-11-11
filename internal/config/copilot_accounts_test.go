package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopilotAccountsRoutingAndCredentials(t *testing.T) {
	dir := t.TempDir()
	filename := filepath.Join(dir, "accounts.json")
	if err := os.WriteFile(filepath.Join(dir, "business.token"), []byte("business-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(`{
		"defaultAccount":"private",
		"accounts":[{"name":"_dtag","tokenFile":"business.token","accountType":"business",
			"budgetUSD":100,"budgetSpentUSD":20,"budgetReservedUSD":5}],
		"modelRoutes":[{"account":"_dtag","models":["*luna*","*haiku*"]},
			{"account":"private","models":["*"]}]
	}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadCopilotAccounts(filename)
	if err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]string{
		"gpt-6-luna": "_dtag", "Claude-Haiku-4-5[1m]": "_dtag",
		"claude-opus-5.5": "private",
	} {
		if got := cfg.SelectAccount(model); got != want {
			t.Errorf("%s routed to %s, want %s", model, got, want)
		}
	}
	a := cfg.Accounts[0]
	if token, err := a.LoadToken(); err != nil || token != "business-test-token" {
		t.Fatalf("relative token file: %q, %v", token, err)
	}
	budget, err := a.NewBudget()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot := budget.Snapshot(); snapshot.EstimatedSpentUSD != 20 || snapshot.ReservedUSD != 5 || snapshot.RemainingUSD != 80 {
		t.Fatalf("restored account budget: %+v", snapshot)
	}
	t.Setenv("TEST_COPILOT_BUSINESS_TOKEN", " env-test-token\n")
	a.TokenEnv, a.TokenFile = "TEST_COPILOT_BUSINESS_TOKEN", ""
	if token, err := a.LoadToken(); err != nil || token != "env-test-token" {
		t.Fatalf("environment token: %q, %v", token, err)
	}
	t.Setenv("TEST_COPILOT_BUSINESS_TOKEN", "")
	if _, err := a.LoadToken(); err == nil {
		t.Fatal("missing environment token was accepted")
	}
}

func TestCopilotAccountsRejectUnsafeConfiguration(t *testing.T) {
	for name, body := range map[string]string{
		"duplicate default": `{"accounts":[{"name":"default","tokenEnv":"T","budgetUSD":10}]}`,
		"path name":         `{"accounts":[{"name":"../work","tokenEnv":"T","budgetUSD":10}]}`,
		"two sources":       `{"accounts":[{"name":"work","tokenEnv":"T","tokenFile":"x","budgetUSD":10}]}`,
		"missing budget":    `{"accounts":[{"name":"work","tokenEnv":"T"}]}`,
		"invalid budget":    `{"accounts":[{"name":"work","tokenEnv":"T","budgetUSD":-1}]}`,
		"invalid reserve":   `{"accounts":[{"name":"work","tokenEnv":"T","budgetUSD":10,"budgetReservedUSD":1}]}`,
		"unknown field":     `{"accounts":[{"name":"work","tokenEnv":"T","budgetDollars":10}]}`,
		"bad account type":  `{"accounts":[{"name":"work","tokenEnv":"T","budgetUSD":10,"accountType":"other"}]}`,
		"unknown target":    `{"modelRoutes":[{"account":"missing","models":["*"]}]}`,
		"bad pattern":       `{"modelRoutes":[{"account":"default","models":["["]}]}`,
		"empty patterns":    `{"modelRoutes":[{"account":"default","models":[]}]}`,
		"trailing JSON":     `{} {}`,
		"null config":       `null`,
	} {
		t.Run(name, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "accounts.json")
			if err := os.WriteFile(filename, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadCopilotAccounts(filename)
			if err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}
