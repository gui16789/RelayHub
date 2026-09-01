package relay

// Custom per-channel request headers (Channel.Headers) must reach the
// upstream, and reserved names (authorization / content-type) must not.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
)

func TestChannelHeadersForwarded(t *testing.T) {
	gotXTitle := ""
	gotAuth := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXTitle = r.Header.Get("X-Title")
		gotAuth = r.Header.Get("Authorization")
		writeOK(w)
	}))
	defer upstream.Close()

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `server:
  listen: ":0"
channels:
  - name: ch
    type: openai
    base_url: ` + upstream.URL + `
    api_keys: [sk-real]
    models: [m]
    priority: 1
    headers:
      X-Title: my-proxy
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgStore, err := store.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(cfgStore, stats.NewCollector())

	rec := postModelRaw(t, handler, "m")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if gotXTitle != "my-proxy" {
		t.Errorf("upstream X-Title = %q, want my-proxy", gotXTitle)
	}
	if gotAuth != "Bearer sk-real" {
		t.Errorf("upstream Authorization = %q, want Bearer sk-real", gotAuth)
	}
}

// TestChannelHeadersCannotOverrideAuth verifies a channel trying to set
// Authorization is rejected at save time (config validation), so the
// proxy's own key always wins.
func TestChannelHeadersCannotOverrideAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `server:
  listen: ":0"
channels:
  - name: ch
    type: openai
    base_url: https://example.com
    api_keys: [sk-real]
    models: [m]
    headers:
      authorization: Bearer evil
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewStore(path); err == nil {
		t.Fatal("expected validation error for reserved authorization header, got nil")
	}
}
