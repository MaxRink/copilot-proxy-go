package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

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
		Name     string                `json:"name"`
		BasePath string                `json:"base_path"`
		Budget   *state.BudgetSnapshot `json:"budget"`
	}
	accounts := make([]accountInfo, 0, len(a.names))
	for _, name := range a.names {
		accounts = append(accounts, accountInfo{name, "/accounts/" + name, a.handlers[name].BudgetSnapshot()})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		DefaultAccount string                     `json:"default_account"`
		ModelRoutes    []config.CopilotModelRoute `json:"model_routes"`
		Accounts       []accountInfo              `json:"accounts"`
	}{a.config.DefaultAccount, a.config.ModelRoutes, accounts})
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
