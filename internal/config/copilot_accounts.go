package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

// CopilotAccounts adds accounts to the existing CLI login and budget.
type CopilotAccounts struct {
	DefaultAccount string              `json:"defaultAccount"`
	Accounts       []CopilotAccount    `json:"accounts"`
	ModelRoutes    []CopilotModelRoute `json:"modelRoutes"`
}

type CopilotAccount struct {
	Name              string   `json:"name"`
	TokenEnv          string   `json:"tokenEnv,omitempty"`
	TokenFile         string   `json:"tokenFile,omitempty"`
	AccountType       string   `json:"accountType,omitempty"`
	BudgetUSD         *float64 `json:"budgetUSD"`
	BudgetSpentUSD    float64  `json:"budgetSpentUSD,omitempty"`
	BudgetReservedUSD float64  `json:"budgetReservedUSD,omitempty"`
}

type CopilotModelRoute struct {
	Account string   `json:"account"`
	Models  []string `json:"models"`
}

var accountName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,63}$`)

func LoadCopilotAccounts(filename string) (*CopilotAccounts, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var cfg *CopilotAccounts
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("invalid accounts file: %w", err)
	}
	if cfg == nil {
		return nil, fmt.Errorf("accounts file must contain a JSON object")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("accounts file must contain one JSON object")
	}
	if cfg.DefaultAccount == "" {
		cfg.DefaultAccount = "default"
	}
	if !accountName.MatchString(cfg.DefaultAccount) {
		return nil, fmt.Errorf("invalid default account name")
	}
	names := map[string]bool{cfg.DefaultAccount: true}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if !accountName.MatchString(a.Name) || names[a.Name] {
			return nil, fmt.Errorf("invalid or duplicate account name %q", a.Name)
		}
		names[a.Name] = true
		if (a.TokenEnv == "") == (a.TokenFile == "") {
			return nil, fmt.Errorf("account %q requires exactly one of tokenEnv or tokenFile", a.Name)
		}
		if a.TokenFile != "" && !filepath.IsAbs(a.TokenFile) {
			a.TokenFile = filepath.Join(filepath.Dir(filename), a.TokenFile)
		}
		if a.AccountType == "" {
			a.AccountType = "individual"
		}
		switch a.AccountType {
		case "individual", "business", "enterprise":
		default:
			return nil, fmt.Errorf("invalid accountType for account %q", a.Name)
		}
		if _, err := a.NewBudget(); err != nil {
			return nil, fmt.Errorf("account %q: %w", a.Name, err)
		}
	}
	for _, route := range cfg.ModelRoutes {
		if !names[route.Account] || len(route.Models) == 0 {
			return nil, fmt.Errorf("model route requires a known account and model patterns")
		}
		for _, pattern := range route.Models {
			if pattern == "" {
				return nil, fmt.Errorf("model route has an empty pattern")
			}
			if _, err := path.Match(pattern, ""); err != nil {
				return nil, fmt.Errorf("invalid model pattern %q: %w", pattern, err)
			}
		}
	}
	return cfg, nil
}

func (a CopilotAccount) NewBudget() (*state.TokenBudget, error) {
	if a.BudgetUSD == nil {
		return nil, fmt.Errorf("budgetUSD is required")
	}
	return state.NewTokenBudgetWithRestored(*a.BudgetUSD, a.BudgetSpentUSD, a.BudgetReservedUSD)
}

func (a CopilotAccount) LoadToken() (string, error) {
	var token string
	if a.TokenEnv != "" {
		token = os.Getenv(a.TokenEnv)
	} else {
		data, err := os.ReadFile(a.TokenFile)
		if err != nil {
			return "", fmt.Errorf("reading token file: %w", err)
		}
		token = string(data)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("account %q has an empty or missing token", a.Name)
	}
	return token, nil
}

// SelectAccount uses the original client model, with the first matching rule winning.
func (c CopilotAccounts) SelectAccount(model string) string {
	for _, route := range c.ModelRoutes {
		for _, pattern := range route.Models {
			if match, _ := path.Match(strings.ToLower(pattern), strings.ToLower(model)); match {
				return route.Account
			}
		}
	}
	return c.DefaultAccount
}
