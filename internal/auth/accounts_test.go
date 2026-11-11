package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
	"github.com/tonghaoch/copilot-proxy-go/internal/state"
)

type accountTransport func(*http.Request) (*http.Response, error)

func (f accountTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAccountRefreshStateAndCooldownAreIsolated(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	restore := api.SetHTTPClient(&http.Client{Transport: accountTransport(func(r *http.Request) (*http.Response, error) {
		token := r.Header.Get("Authorization")
		mu.Lock()
		calls[token]++
		count := calls[token]
		mu.Unlock()
		body := fmt.Sprintf(`{"token":%q,"expires_at":%d}`, token+fmt.Sprint(count), time.Now().Add(time.Hour).Unix())
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	defer restore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := &state.State{}, &state.State{}
	a.SetVSCodeVersion("test")
	b.SetVSCodeVersion("test")
	refreshA, err := SetupAccount(ctx, a, "account-a")
	if err != nil {
		t.Fatal(err)
	}
	refreshB, err := SetupAccount(ctx, b, "account-b")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		for _, refresh := range []func() error{refreshA, refreshB} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := refresh(); err != nil {
					t.Error(err)
				}
			}()
		}
	}
	wg.Wait()
	if a.GetCopilotToken() == b.GetCopilotToken() ||
		!strings.Contains(a.GetCopilotToken(), "account-a2") || !strings.Contains(b.GetCopilotToken(), "account-b2") {
		t.Fatalf("account refreshes or cooldowns crossed: a=%q b=%q", a.GetCopilotToken(), b.GetCopilotToken())
	}
	mu.Lock()
	defer mu.Unlock()
	for token, count := range calls {
		if count != 2 {
			t.Errorf("credential %s refreshed %d times, want one initial and one coalesced refresh", token, count)
		}
	}
}

func TestLoginToFilePreservesDefaultStateAndSecuresToken(t *testing.T) {
	restore := api.SetHTTPClient(&http.Client{Transport: accountTransport(func(r *http.Request) (*http.Response, error) {
		if !strings.Contains(r.Header.Get("Authorization"), "additional-account") {
			t.Errorf("wrong login credential")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"token":"copilot-test"}`))}, nil
	})})
	defer restore()
	oldToken := state.Global.GetGithubToken()
	filename := filepath.Join(t.TempDir(), "business.token")
	if err := os.WriteFile(filename, []byte("additional-account\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := LoginToFile(context.Background(), filename, false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(filename)
	if err != nil || string(token) != "additional-account" || info.Mode().Perm() != 0600 || state.Global.GetGithubToken() != oldToken {
		t.Fatalf("login did not preserve state or secure token: mode=%v err=%v", info.Mode().Perm(), err)
	}
}
