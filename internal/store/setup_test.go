package store

import (
	"path/filepath"
	"testing"

	"github.com/local/relayhub/internal/config"
)

// TestApplySetupCreatesConfigFile verifies first-boot initialization writes
// a brand-new config file and refuses to run twice.
func TestApplySetupCreatesConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	st, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	channel := &config.Channel{
		Name: "openai", Type: config.TypeOpenAI,
		BaseURL: "https://api.openai.com", APIKeys: []string{"sk-up"}, Models: []string{"gpt-4o"},
	}
	if err := st.ApplySetup("adm", "sk-client", channel); err != nil {
		t.Fatal(err)
	}

	loaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("setup must create the config file: %v", err)
	}
	if loaded.Server.AdminKey != "adm" || loaded.Server.APIKey != "sk-client" {
		t.Fatalf("keys not persisted: %+v", loaded.Server)
	}
	if len(loaded.Channels) != 1 || loaded.Channels[0].Name != "openai" {
		t.Fatalf("channel not persisted: %+v", loaded.Channels)
	}

	if err := st.ApplySetup("other", "", nil); err == nil {
		t.Fatal("second ApplySetup must fail")
	}
}
