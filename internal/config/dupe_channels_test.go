package config

// A channel's name is the key for its cooldowns, 5xx streaks, probe health and
// statistics. Two channels sharing a name would silently share all of that, so
// the loader has to reject it rather than produce a config that misbehaves in
// ways an operator cannot see.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDuplicateChannelNameRejected(t *testing.T) {
	path := writeConfig(t, `server:
  listen: ":8787"
channels:
  - name: relay
    type: openai
    base_url: https://a.example
    api_keys: [sk-a]
    models: [m]
  - name: relay
    type: openai
    base_url: https://b.example
    api_keys: [sk-b]
    models: [m]
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("duplicate channel name should be rejected")
	}
	if !strings.Contains(err.Error(), "relay") {
		t.Errorf("error should name the offending channel, got: %v", err)
	}
}

// Names differing only in case are kept apart by the state maps but are
// indistinguishable in the console, so they are rejected too.
func TestCaseOnlyDuplicateChannelNameRejected(t *testing.T) {
	path := writeConfig(t, `server:
  listen: ":8787"
channels:
  - name: OpenAI
    type: openai
    base_url: https://a.example
    api_keys: [sk-a]
    models: [m]
  - name: openai
    type: openai
    base_url: https://b.example
    api_keys: [sk-b]
    models: [m]
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("names differing only in case should be rejected")
	}
	if !strings.Contains(err.Error(), "case") {
		t.Errorf("error should explain the case collision, got: %v", err)
	}
}

func TestDistinctChannelNamesAccepted(t *testing.T) {
	path := writeConfig(t, `server:
  listen: ":8787"
channels:
  - name: primary
    type: openai
    base_url: https://a.example
    api_keys: [sk-a]
    models: [m]
  - name: backup
    type: anthropic
    base_url: https://b.example
    api_keys: [sk-b]
    models: [m]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("distinct names must load: %v", err)
	}
	if len(cfg.Channels) != 2 {
		t.Errorf("expected 2 channels, got %d", len(cfg.Channels))
	}
}
