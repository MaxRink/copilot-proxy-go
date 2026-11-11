package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type tokenRefresher struct {
	mu          sync.Mutex
	lastRefresh time.Time
	state       *state.State
	fetch       func(string, string) (*CopilotTokenResponse, error)
}

var defaultRefresher = &tokenRefresher{state: state.Global, fetch: FetchCopilotToken}

func RefreshCopilotTokenNow() error {
	return defaultRefresher.refreshNow()
}

func (t *tokenRefresher) refreshNow() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Since(t.lastRefresh) < 30*time.Second {
		return nil
	}
	_, err := t.refresh()
	return err
}

// refresh is called with t.mu held so scheduled and request-level refreshes cannot race.
func (t *tokenRefresher) refresh() (*CopilotTokenResponse, error) {
	githubToken := t.state.GetGithubToken()
	vsCodeVersion := t.state.GetVSCodeVersion()
	slog.Info("refreshing Copilot token...")
	copilotToken, err := t.fetch(githubToken, vsCodeVersion)
	if err != nil {
		return nil, fmt.Errorf("refreshing copilot token: %w", err)
	}
	t.state.SetCopilotToken(copilotToken.Token)
	t.lastRefresh = time.Now()
	slog.Info("Copilot token refreshed successfully")
	return copilotToken, nil
}

func StartTokenRefresh(expiresAt int64, refreshIn int) {
	StartTokenRefreshContext(context.Background(), expiresAt, refreshIn)
}

// StartTokenRefreshContext refreshes tokens until the application context is
// cancelled. Timers, rather than time.Sleep, make shutdown immediate.
func StartTokenRefreshContext(ctx context.Context, expiresAt int64, refreshIn int) {
	defaultRefresher.start(ctx, expiresAt, refreshIn)
}

// SetupAccount authenticates an additional account without reading or overwriting
// the default login's token file, state, or refresh cooldown.
func SetupAccount(ctx context.Context, s *state.State, githubToken string) (func() error, error) {
	t := &tokenRefresher{state: s, fetch: func(token, version string) (*CopilotTokenResponse, error) {
		return FetchCopilotTokenContext(ctx, token, version)
	}}
	s.SetGithubToken(githubToken)
	token, err := t.fetch(githubToken, s.GetVSCodeVersion())
	if err != nil {
		return nil, err
	}
	s.SetCopilotToken(token.Token)
	t.start(ctx, token.ExpiresAt, token.RefreshIn)
	return t.refreshNow, nil
}

func (t *tokenRefresher) start(ctx context.Context, expiresAt int64, refreshIn int) {
	refreshDuration := calcRefreshDuration(expiresAt, refreshIn)
	go func() {
		for {
			slog.Info("next Copilot token refresh scheduled", "in", refreshDuration.Round(time.Second))
			timer := time.NewTimer(refreshDuration)
			select {
			case <-ctx.Done():
				timer.Stop()
				slog.Debug("token refresh stopped", "reason", ctx.Err())
				return
			case <-timer.C:
			}
			t.mu.Lock()
			copilotToken, err := t.refresh()
			t.mu.Unlock()
			if err != nil {
				slog.Error("failed to refresh Copilot token", "error", err)
				refreshDuration = 30 * time.Second
				continue
			}
			if t.state.GetShowToken() {
				slog.Info("refreshed Copilot token", "token", copilotToken.Token)
			}
			refreshDuration = calcRefreshDuration(copilotToken.ExpiresAt, copilotToken.RefreshIn)
		}
	}()
}

func calcRefreshDuration(expiresAt int64, refreshIn int) time.Duration {
	const minDuration = 30 * time.Second
	if expiresAt > 0 {
		untilExpiry := time.Until(time.Unix(expiresAt, 0))
		d := untilExpiry - 2*time.Minute
		if d < minDuration {
			d = minDuration
		}
		slog.Info("token refresh timing",
			"expires_at", time.Unix(expiresAt, 0).Format(time.RFC3339),
			"until_expiry", untilExpiry.Round(time.Second),
			"refresh_in_from_api", refreshIn,
			"chosen_wait", d.Round(time.Second))
		return d
	}
	d := time.Duration(refreshIn-60) * time.Second
	if d < minDuration {
		d = minDuration
	}
	return d
}
