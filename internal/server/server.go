package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/tonghaoch/copilot-proxy-go/internal/config"
	"github.com/tonghaoch/copilot-proxy-go/internal/handler"
	"github.com/tonghaoch/copilot-proxy-go/internal/middleware"
)

// Options configures the server behavior.
type Options struct {
	Host          string
	Port          int
	Accounts      map[string]*handler.Handler
	AccountConfig *config.CopilotAccounts
}

// New creates a new HTTP server with all routes and middleware configured.
func New(opts Options) *http.Server {
	return NewWithHandler(opts, handler.New(handler.Dependencies{}))
}

// NewWithHandler creates a server with explicit endpoint dependencies.
func NewWithHandler(opts Options, endpoints *handler.Handler) *http.Server {
	r := chi.NewRouter()

	// Core middleware
	r.Use(chimw.RequestID)
	r.Use(requestIDHeader)
	r.Use(requestLogger)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"X-Copilot-Proxy-Account"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
	r.Use(chimw.Recoverer)

	// API key authentication
	r.Use(middleware.Auth)
	accounts := newAccountRouter(endpoints, opts.Accounts, opts.AccountConfig)
	r.Use(accounts.selectAccount)
	r.Get("/api/accounts", accounts.list)
	r.Post("/api/accounts/{account}/budget", accounts.raiseBudget)
	registerRoutes(r, accounts)
	r.Route("/accounts/{account}", func(r chi.Router) {
		registerRoutes(r, accounts)
	})

	host := opts.Host
	if host == "" {
		host = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", host, opts.Port)

	return &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       5 * time.Minute,
		// Streaming responses may legitimately outlive a fixed write deadline.
		// Shutdown still bounds server termination at the application layer.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}
}

func registerRoutes(r chi.Router, accounts *accountRouter) {
	handle := func(fn func(*handler.Handler, http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			fn(r.Context().Value(accountKey{}).(accountSelection).handler, w, r)
		}
	}

	// Routes
	r.Get("/", handle((*handler.Handler).Health))
	r.Get("/token", handle((*handler.Handler).Token))
	r.Get("/usage", handle((*handler.Handler).Usage))
	r.Get("/dashboard", handle((*handler.Handler).Dashboard))
	r.Get("/api/stats", handle((*handler.Handler).Stats))

	// Models
	r.Get("/models", accounts.models)
	r.Get("/v1/models", accounts.models)

	// Chat Completions
	r.Post("/chat/completions", handle((*handler.Handler).ChatCompletions))
	r.Post("/v1/chat/completions", handle((*handler.Handler).ChatCompletions))

	// Messages (Anthropic-compatible)
	r.Post("/v1/messages", handle((*handler.Handler).Messages))
	r.Post("/v1/messages/count_tokens", handle((*handler.Handler).CountTokens))

	// Responses (OpenAI Responses API)
	r.Post("/responses", handle((*handler.Handler).Responses))
	r.Post("/v1/responses", handle((*handler.Handler).Responses))

	// Embeddings
	r.Post("/embeddings", handle((*handler.Handler).Embeddings))
	r.Post("/v1/embeddings", handle((*handler.Handler).Embeddings))
}

func requestIDHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requestID := chimw.GetReqID(r.Context()); requestID != "" {
			w.Header().Set("X-Request-ID", requestID)
		}
		next.ServeHTTP(w, r)
	})
}

// requestLogger is a simple request logging middleware.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("request completed",
			"request_id", chimw.GetReqID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration_ms", time.Since(start).Milliseconds(),
			"account", ww.Header().Get("X-Copilot-Proxy-Account"),
		)
	})
}
