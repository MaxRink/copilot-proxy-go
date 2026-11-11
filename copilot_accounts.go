package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/auth"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/service"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type copilotAccount struct {
	name   string
	state  *state.State
	client *service.Client
	budget *state.TokenBudget
}

func copilotClientFor(s *state.State, refresh func() error) *service.Client {
	return service.NewClientWithOptions(service.ClientOptions{
		HTTPClient: api.HTTPClient(), RefreshToken: refresh,
		BuildHeaders: func() http.Header {
			return api.BuildCopilotHeaders(s.GetCopilotToken(), s.GetVSCodeVersion())
		},
		BuildURL: func(path string) string {
			return api.GetBaseURL(s.GetAccountType()) + path
		},
	})
}

func setupCopilotAccounts(ctx context.Context, cfg *config.CopilotAccounts) ([]copilotAccount, error) {
	if cfg == nil {
		return nil, nil
	}
	accounts := make([]copilotAccount, 0, len(cfg.Accounts))
	tokens := map[string]bool{state.Global.GetGithubToken(): true}
	for _, profile := range cfg.Accounts {
		token, err := profile.LoadToken()
		if err != nil {
			return nil, fmt.Errorf("account %q: %w", profile.Name, err)
		}
		if tokens[token] {
			return nil, fmt.Errorf("account %q reuses another account's GitHub token", profile.Name)
		}
		tokens[token] = true
		s := &state.State{}
		s.SetAccountType(profile.AccountType)
		s.SetVSCodeVersion(state.Global.GetVSCodeVersion())
		refresh, err := auth.SetupAccount(ctx, s, token)
		if err != nil {
			return nil, fmt.Errorf("authenticating account %q: %w", profile.Name, err)
		}
		client := copilotClientFor(s, refresh)
		models, err := client.FetchModels(ctx)
		if err != nil {
			return nil, fmt.Errorf("loading models for account %q: %w", profile.Name, err)
		}
		s.SetModels(models)
		budget, err := profile.NewBudget()
		if err != nil {
			return nil, fmt.Errorf("account %q: %w", profile.Name, err)
		}
		accounts = append(accounts, copilotAccount{profile.Name, s, client, budget})
	}
	return accounts, nil
}
