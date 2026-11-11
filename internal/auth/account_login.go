package auth

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tonghaoch/copilot-proxy-go/internal/api"
)

// LoginToFile leaves the default login untouched when adding another account.
func LoginToFile(ctx context.Context, filename string, force bool) error {
	var token string
	if !force {
		data, err := os.ReadFile(filename)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		token = strings.TrimSpace(string(data))
	}
	if token == "" {
		var err error
		token, err = authenticateDevice()
		if err != nil {
			return err
		}
	}
	if _, err := FetchCopilotTokenContext(ctx, token, api.FallbackVSCodeVersion); err != nil {
		return fmt.Errorf("verifying Copilot access: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".github-token-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(token); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filename)
}
