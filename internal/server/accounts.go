package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type accountKey struct{}

type accountSelection struct {
	handler  *handler.Handler
	explicit bool
}

type accountRouter struct {
	config   config.CopilotAccounts
	handlers map[string]*handler.Handler
	names    []string
}

func newAccountRouter(defaultHandler *handler.Handler, additional map[string]*handler.Handler, cfg *config.CopilotAccounts) *accountRouter {
	a := &accountRouter{config: config.CopilotAccounts{DefaultAccount: "default"}, handlers: make(map[string]*handler.Handler)}
	if cfg != nil {
		a.config = *cfg
	}
	for name, h := range additional {
		a.handlers[name] = h
	}
	a.handlers[a.config.DefaultAccount] = defaultHandler
	for name := range a.handlers {
		a.names = append(a.names, name)
	}
	sort.Strings(a.names)
	return a
}

func (a *accountRouter) selectAccount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := a.config.DefaultAccount
		explicit := strings.HasPrefix(r.URL.Path, "/accounts/")
		if explicit {
			name = strings.SplitN(strings.TrimPrefix(r.URL.Path, "/accounts/"), "/", 2)[0]
		} else if len(a.config.ModelRoutes) > 0 && isModelRequest(r) {
			if r.ContentLength > handler.MaxRequestBodySize {
				api.ForwardError(w, &api.HTTPError{StatusCode: http.StatusRequestEntityTooLarge, Message: "request body too large"})
				return
			}
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, handler.MaxRequestBodySize))
			if err != nil {
				var limit *http.MaxBytesError
				if errors.As(err, &limit) {
					api.ForwardError(w, &api.HTTPError{StatusCode: http.StatusRequestEntityTooLarge, Message: "request body too large"})
				} else {
					api.ForwardError(w, api.InvalidRequest("reading account routing model", err))
				}
				return
			}
			var request struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				api.ForwardError(w, api.InvalidRequest("invalid request body", err))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			name = a.config.SelectAccount(request.Model)
		}
		h := a.handlers[name]
		if h == nil {
			api.ForwardError(w, &api.HTTPError{StatusCode: http.StatusNotFound, Message: "unknown Copilot account"})
			return
		}
		w.Header().Set("X-Copilot-Proxy-Account", name)
		ctx := context.WithValue(r.Context(), accountKey{}, accountSelection{handler: h, explicit: explicit})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func isModelRequest(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/chat/completions", "/v1/chat/completions", "/v1/messages", "/v1/messages/count_tokens",
		"/responses", "/v1/responses", "/embeddings", "/v1/embeddings":
		return true
	}
	return false
}

func (a *accountRouter) list(w http.ResponseWriter, r *http.Request) {
	type accountInfo struct {
		Name        string                `json:"name"`
		BasePath    string                `json:"base_path"`
		AccountType string                `json:"account_type"`
		Budget      *state.BudgetSnapshot `json:"budget"`
	}
	accounts := make([]accountInfo, 0, len(a.names))
	for _, name := range a.names {
		h := a.handlers[name]
		accounts = append(accounts, accountInfo{name, "/accounts/" + name, h.AccountType(), h.BudgetSnapshot()})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		DefaultAccount string                     `json:"default_account"`
		ModelRoutes    []config.CopilotModelRoute `json:"model_routes"`
		Accounts       []accountInfo              `json:"accounts"`
	}{a.config.DefaultAccount, a.config.ModelRoutes, accounts})
}

// raiseBudget lifts one account's limit for the running process only.
func (a *accountRouter) raiseBudget(w http.ResponseWriter, r *http.Request) {
	// CORS allows any origin, so without this any web page could lift the spend guard.
	if !sameOrigin(r) {
		api.ForwardError(w, &api.HTTPError{StatusCode: http.StatusForbidden, Message: "budget changes must come from the proxy's own dashboard"})
		return
	}
	name := chi.URLParam(r, "account")
	h := a.handlers[name]
	if h == nil {
		api.ForwardError(w, &api.HTTPError{StatusCode: http.StatusNotFound, Message: "unknown Copilot account"})
		return
	}
	var req struct {
		IncreaseUSD float64 `json:"increase_usd"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		api.ForwardError(w, api.InvalidRequest("invalid budget increase", err))
		return
	}
	snapshot, err := h.RaiseBudget(req.IncreaseUSD)
	if err != nil {
		api.ForwardError(w, &api.HTTPError{StatusCode: http.StatusBadRequest, Message: err.Error()})
		return
	}
	slog.Info("token budget raised", "account", name, "increase_usd", req.IncreaseUSD, "limit_usd", snapshot.LimitUSD,
		"estimated_spent_usd", snapshot.EstimatedSpentUSD, "reserved_usd", snapshot.ReservedUSD)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}

func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

func (a *accountRouter) models(w http.ResponseWriter, r *http.Request) {
	selected := r.Context().Value(accountKey{}).(accountSelection)
	if selected.explicit || len(a.config.ModelRoutes) == 0 {
		selected.handler.Models(w, r)
		return
	}
	entries := make([]handler.ModelEntry, 0)
	for _, name := range a.names {
		models, err := a.handlers[name].ListModels(r.Context())
		if err != nil {
			api.ForwardError(w, &api.HTTPError{StatusCode: http.StatusBadGateway, Message: "failed to load account model catalog"})
			return
		}
		for _, model := range models {
			if a.config.SelectAccount(model.ID) == name {
				model.CopilotAccount = name
				entries = append(entries, model)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(handler.ModelsListResponse{Object: "list", Data: entries})
}
