// Package settings persists user-editable CLI configuration (gateway URL,
// API key, model) in ~/.agy/settings.json. Environment variables always take
// precedence over the persisted file at startup.
package settings

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

const settingsFileName = "settings.json"

// Settings is the persisted configuration blob.
type Settings struct {
	GatewayURL    string `json:"gateway_url,omitempty"`
	APIKey        string `json:"api_key,omitempty"`
	Model         string `json:"model,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	// MaxSteps overrides the agent's continuation cap (0 = CLI default).
	MaxSteps int `json:"max_steps,omitempty"`
	// AutoCompactTokenLimit is the context budget that triggers a
	// summarization checkpoint (0 = derived from ContextWindow).
	AutoCompactTokenLimit int `json:"auto_compact_token_limit,omitempty"`
	// Permission is the sandbox mode: readonly / workspace / full / confirm
	// (empty = confirm, the legacy per-mutation approval flow).
	Permission string `json:"permission_mode,omitempty"`
}

// Path returns the settings file path (~/.agy/settings.json).
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".agy", settingsFileName)
}

// Load reads the persisted settings. A missing file yields zero settings and
// no error so first-run users start clean.
func Load() (Settings, error) {
	var s Settings
	path := Path()
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Settings{}, err
	}
	return s, nil
}

// Save writes the settings atomically with owner-only permissions (0600),
// since the file may contain an API key.
func Save(s Settings) error {
	path := Path()
	if path == "" {
		return errors.New("cannot resolve settings path (no home directory)")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
