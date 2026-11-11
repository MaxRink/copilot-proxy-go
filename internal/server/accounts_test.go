package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/server"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type accountCopilot struct {
	budgetCopilot
	countCalls int
}

func (c *accountCopilot) ProxyCountTokens(context.Context, []byte, string) (*http.Response, error) {
	c.countCalls++
	return budgetResponse(`{"input_tokens":42}`), nil
}

func accountServer(t *testing.T, privateLimit, businessLimit float64) (*http.Server, *accountCopilot, *accountCopilot, *state.TokenBudget, *state.TokenBudget) {
	t.Helper()
	makeAccount := func(limit float64) (*handler.Handler, *accountCopilot, *state.TokenBudget) {
		s := &state.State{}
		var models []state.Model
		for _, id := range []string{"gpt-6-luna", "claude-haiku-4.5", "claude-opus-5.5"} {
			model := pricedBudgetModel(t, []string{"/responses"})
			model.ID = id
			models = append(models, model)
		}
		s.SetModels(models)
		budget, err := state.NewTokenBudget(limit)
		if err != nil {
			t.Fatal(err)
		}
		client := &accountCopilot{}
		h := handler.New(handler.Dependencies{State: s, Copilot: client, Budget: budget, Metrics: state.NewMetricsStore()})
		return h, client, budget
	}
	private, pc, pb := makeAccount(privateLimit)
	business, bc, bb := makeAccount(businessLimit)
	cfg := &config.CopilotAccounts{
		DefaultAccount: "private",
		ModelRoutes: []config.CopilotModelRoute{
			{Account: "_dtag", Models: []string{"*luna*", "*haiku*"}},
		},
	}
	srv := server.NewWithHandler(server.Options{Accounts: map[string]*handler.Handler{"_dtag": business}, AccountConfig: cfg}, private)
	return srv, pc, bc, pb, bb
}

func TestAccountModelRoutingBudgetsAndExplicitOverride(t *testing.T) {
	srv, private, business, pb, bb := accountServer(t, 0.001, 10)
	send := func(path, model, wantAccount string, wantStatus int) {
		t.Helper()
		w := serveBudgetRequest(srv, http.MethodPost, path, `{"model":"`+model+`","input":"hello"}`)
		if w.Code != wantStatus || w.Header().Get("X-Copilot-Proxy-Account") != wantAccount {
			t.Fatalf("%s %s: status=%d account=%s body=%s", path, model, w.Code, w.Header().Get("X-Copilot-Proxy-Account"), w.Body)
		}
	}
	send("/v1/responses", "gpt-6-luna", "_dtag", http.StatusOK)
	send("/v1/responses", "claude-haiku-4-5", "_dtag", http.StatusOK)
	if pb.Snapshot().EstimatedSpentUSD != 0 || bb.Snapshot().EstimatedSpentUSD == 0 {
		t.Fatal("business traffic charged the private budget")
	}
	send("/v1/responses", "claude-opus-5-5", "private", http.StatusOK)
	send("/v1/responses", "claude-opus-5-5", "private", http.StatusTooManyRequests)
	send("/v1/responses", "gpt-6-luna", "_dtag", http.StatusOK)
	send("/accounts/private/v1/responses", "gpt-6-luna", "private", http.StatusTooManyRequests)
	send("/accounts/_dtag/v1/responses", "claude-opus-5-5", "_dtag", http.StatusOK)
	if private.calls != 1 || business.calls != 4 || pb.Snapshot().InFlight != 0 || bb.Snapshot().InFlight != 0 {
		t.Fatalf("accounts spilled or leaked admissions: private=%d business=%d", private.calls, business.calls)
	}
	for account, want := range map[string]int64{"private": 3, "_dtag": 4} {
		w := serveBudgetRequest(srv, http.MethodGet, "/accounts/"+account+"/api/stats", "")
		var stats struct {
			TotalRequests int64 `json:"total_requests"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil || stats.TotalRequests != want {
			t.Fatalf("account metrics crossed for %s: %s (%v)", account, w.Body, err)
		}
	}
}

func TestAccountSelectionAcrossModelEndpoints(t *testing.T) {
	for _, tc := range []struct {
		path, body string
	}{
		{"/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/messages", `{"model":"claude-haiku-4-5","max_tokens":20,"stream":true,"messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/messages/count_tokens", `{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello"}]}`},
		{"/v1/chat/completions", `{"model":"gpt-6-luna","messages":[{"role":"user","content":"hello"}]}`},
		{"/embeddings", `{"model":"gpt-6-luna","input":"hello"}`},
	} {
		t.Run(tc.path+tc.body, func(t *testing.T) {
			srv, private, business, pb, _ := accountServer(t, 10, 10)
			w := serveBudgetRequest(srv, http.MethodPost, tc.path, tc.body)
			if w.Code != http.StatusOK || w.Header().Get("X-Copilot-Proxy-Account") != "_dtag" ||
				private.calls != 0 || private.countCalls != 0 || business.calls+business.countCalls == 0 || pb.Snapshot().EstimatedSpentUSD != 0 {
				t.Fatalf("wrong account: status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestAccountCatalogDirectoryAndRoutingErrors(t *testing.T) {
	srv, private, business, _, _ := accountServer(t, 10, 20)
	w := serveBudgetRequest(srv, http.MethodGet, "/models", "")
	var models handler.ModelsListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &models); err != nil || len(models.Data) != 3 {
		t.Fatalf("routed catalog: %s (%v)", w.Body, err)
	}
	for _, m := range models.Data {
		want := "_dtag"
		if strings.Contains(m.ID, "opus") {
			want = "private"
		}
		if m.CopilotAccount != want {
			t.Fatalf("model %s advertised on wrong account: %s", m.ID, m.CopilotAccount)
		}
	}
	w = serveBudgetRequest(srv, http.MethodGet, "/api/accounts", "")
	var directory struct {
		Default  string `json:"default_account"`
		Accounts []struct {
			Name   string               `json:"name"`
			Budget state.BudgetSnapshot `json:"budget"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &directory); err != nil || directory.Default != "private" || len(directory.Accounts) != 2 {
		t.Fatalf("account directory: %s (%v)", w.Body, err)
	}
	for _, a := range directory.Accounts {
		want := float64(10)
		if a.Name == "_dtag" {
			want = 20
		}
		if a.Budget.LimitUSD != want {
			t.Fatalf("budget missing from account directory: %s", w.Body)
		}
	}
	for _, tc := range []struct {
		path, body string
		status     int
	}{
		{"/accounts/missing/v1/responses", `{"model":"gpt-6-luna"}`, http.StatusNotFound},
		{"/v1/responses", `{`, http.StatusBadRequest},
		{"/v1/responses", `{"model":42}`, http.StatusBadRequest},
	} {
		w := serveBudgetRequest(srv, http.MethodPost, tc.path, tc.body)
		if w.Code != tc.status || private.calls != 0 || business.calls != 0 {
			t.Fatalf("bad route reached an account: %d %s", w.Code, w.Body)
		}
	}
	w = serveBudgetRequest(srv, http.MethodGet, "/accounts/_dtag/dashboard", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "const ACCOUNT_PATH") {
		t.Fatalf("account dashboard unavailable: %d", w.Code)
	}
}

func TestAccountRoutingEnforcesBodyLimit(t *testing.T) {
	srv, private, business, _, _ := accountServer(t, 10, 10)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-6-luna"}`))
	req.ContentLength = handler.MaxRequestBodySize + 1
	setTestAuthorization(req)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge || private.calls != 0 || business.calls != 0 {
		t.Fatalf("oversize body reached an account: %d", w.Code)
	}
}

func TestAccountBudgetRaiseIsSameOriginAndPerAccount(t *testing.T) {
	srv, _, _, pb, bb := accountServer(t, 0.001, 10)
	raise := func(path, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"increase_usd":5}`))
		req.Host = "127.0.0.1:4141"
		setTestAuthorization(req)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		srv.Handler.ServeHTTP(w, req)
		return w
	}
	if w := raise("/api/accounts/private/budget", "https://evil.example"); w.Code != http.StatusForbidden || pb.Snapshot().LimitUSD != 0.001 {
		t.Fatalf("cross-origin raise accepted: %d %s", w.Code, w.Body)
	}
	if w := raise("/api/accounts/missing/budget", "http://127.0.0.1:4141"); w.Code != http.StatusNotFound {
		t.Fatalf("unknown account: %d", w.Code)
	}
	if w := raise("/api/accounts/private/budget", "http://127.0.0.1:4141"); w.Code != http.StatusOK || pb.Snapshot().LimitUSD != 5.001 || bb.Snapshot().LimitUSD != 10 {
		t.Fatalf("raise hit wrong account: %d %s", w.Code, w.Body)
	}
	w := serveBudgetRequest(srv, http.MethodPost, "/v1/responses", `{"model":"claude-opus-5-5","input":"hello"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("raised budget still refuses: %d %s", w.Code, w.Body)
	}
	w = serveBudgetRequest(srv, http.MethodGet, "/accounts/private/api/stats", "")
	var stats struct {
		Recent        []state.RequestRecord `json:"recent"`
		ModelSpendUSD map[string]float64    `json:"model_spend_usd"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil || len(stats.Recent) != 1 ||
		stats.Recent[0].EstimatedCostUSD <= 0 || stats.ModelSpendUSD["claude-opus-5.5"] != stats.Recent[0].EstimatedCostUSD {
		t.Fatalf("per-request cost missing: %s (%v)", w.Body, err)
	}
}
