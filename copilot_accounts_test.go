package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type accountClientTransport func(*http.Request) (*http.Response, error)

func (f accountClientTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCopilotAccountClientsRefreshOnlyTheirOwnCredentials(t *testing.T) {
	old := state.Global.GetGithubToken()
	state.Global.SetGithubToken("private-test-token")
	defer state.Global.SetGithubToken(old)
	t.Setenv("TEST_BUSINESS_GH", "business-test-token")
	t.Setenv("TEST_ENTERPRISE_GH", "enterprise-test-token")
	exchanges := map[string]int{}
	posts := map[string]int{}
	restore := api.SetHTTPClient(&http.Client{Transport: accountClientTransport(func(r *http.Request) (*http.Response, error) {
		response := func(status int, body string) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		if r.URL.Host == "api.github.com" {
			account := "business"
			if strings.Contains(r.Header.Get("Authorization"), "enterprise") {
				account = "enterprise"
			} else if !strings.Contains(r.Header.Get("Authorization"), "business") {
				t.Error("default credentials were used for an additional account")
			}
			exchanges[account]++
			return response(http.StatusOK, fmt.Sprintf(`{"token":%q,"expires_at":%d}`, fmt.Sprintf("%s-copilot-%d", account, exchanges[account]), time.Now().Add(time.Hour).Unix()))
		}
		account := "business"
		if r.URL.Host == "api.enterprise.githubcopilot.com" {
			account = "enterprise"
		} else if r.URL.Host != "api.business.githubcopilot.com" {
			t.Errorf("wrong account host: %s", r.URL.Host)
		}
		want := fmt.Sprintf("Bearer %s-copilot-%d", account, exchanges[account])
		if r.Header.Get("Authorization") != want {
			t.Error("Copilot request used another account's credential")
		}
		if r.URL.Path == "/models" {
			return response(http.StatusOK, `{"data":[{"id":"test-model"}]}`)
		}
		posts[account]++
		if posts[account] == 1 {
			return response(http.StatusUnauthorized, `{"error":{"message":"expired"}}`)
		}
		return response(http.StatusOK, `{}`)
	})})
	defer restore()
	limit := 100.0
	cfg := &config.CopilotAccounts{DefaultAccount: "private", Accounts: []config.CopilotAccount{
		{Name: "_dtag", TokenEnv: "TEST_BUSINESS_GH", AccountType: "business", BudgetUSD: &limit},
		{Name: "other", TokenEnv: "TEST_ENTERPRISE_GH", AccountType: "enterprise", BudgetUSD: &limit},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accounts, err := setupCopilotAccounts(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		resp, err := account.client.ProxyMessages(ctx, []byte(`{}`), "", false, false)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	for _, name := range []string{"business", "enterprise"} {
		if exchanges[name] != 2 || posts[name] != 2 {
			t.Fatalf("%s did not independently refresh after 401: exchanges=%d requests=%d", name, exchanges[name], posts[name])
		}
	}
	if accounts[0].budget == accounts[1].budget || state.Global.GetGithubToken() != "private-test-token" {
		t.Fatal("account setup reused the default state or budget")
	}
}
