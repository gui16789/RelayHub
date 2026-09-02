package store

// The console must not be able to save a config that config.Load would then
// refuse. Before the name checks were case-insensitive here, adding "OpenAI"
// next to an existing "openai" wrote a file the loader rejects on the next
// start, leaving the proxy unable to boot from its own saved state.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/relayhub/internal/config"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := `server:
  listen: ":8787"
channels:
  - name: openai
    type: openai
    base_url: https://a.example
    api_keys: [sk-a]
    models: [m]
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgStore, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfgStore
}

func sampleChannel(name string) config.Channel {
	return config.Channel{
		Name:    name,
		Type:    config.TypeOpenAI,
		BaseURL: "https://b.example",
		APIKeys: []string{"sk-b"},
		Models:  []string{"m"},
	}
}

func TestAddChannelRejectsExactDuplicate(t *testing.T) {
	cfgStore := newTestStore(t)
	if err := cfgStore.AddChannel(sampleChannel("openai")); err == nil {
		t.Fatal("adding an existing channel name should fail")
	}
}

func TestAddChannelRejectsCaseOnlyDuplicate(t *testing.T) {
	cfgStore := newTestStore(t)
	err := cfgStore.AddChannel(sampleChannel("OpenAI"))
	if err == nil {
		t.Fatal("adding a case-variant of an existing name should fail")
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Errorf("error should name the existing channel, got: %v", err)
	}

	// The saved file must still be loadable: the whole point of the check.
	if _, err := config.Load(cfgStore.Path()); err != nil {
		t.Errorf("config on disk must remain loadable: %v", err)
	}
}

func TestAddChannelAcceptsDistinctName(t *testing.T) {
	cfgStore := newTestStore(t)
	if err := cfgStore.AddChannel(sampleChannel("anthropic")); err != nil {
		t.Fatalf("distinct name should be accepted: %v", err)
	}
	if _, err := config.Load(cfgStore.Path()); err != nil {
		t.Errorf("config on disk must remain loadable: %v", err)
	}
}

// Renaming onto a case-variant of another channel is the same trap as adding
// one, but reached through a different code path.
func TestUpdateChannelRejectsCaseOnlyDuplicate(t *testing.T) {
	cfgStore := newTestStore(t)
	if err := cfgStore.AddChannel(sampleChannel("backup")); err != nil {
		t.Fatal(err)
	}
	renamed := sampleChannel("OPENAI")
	if err := cfgStore.UpdateChannel("backup", renamed); err == nil {
		t.Fatal("renaming onto a case-variant should fail")
	}
	if _, err := config.Load(cfgStore.Path()); err != nil {
		t.Errorf("config on disk must remain loadable: %v", err)
	}
}

// Updating a channel without changing its name must not trip the check
// against itself.
func TestUpdateChannelKeepingOwnNameSucceeds(t *testing.T) {
	cfgStore := newTestStore(t)
	same := sampleChannel("openai")
	same.BaseURL = "https://changed.example"
	if err := cfgStore.UpdateChannel("openai", same); err != nil {
		t.Fatalf("updating a channel in place should succeed: %v", err)
	}
	channel, err := cfgStore.GetChannel("openai")
	if err != nil || channel.BaseURL != "https://changed.example" {
		t.Errorf("update did not apply: %+v (err: %v)", channel, err)
	}
}
