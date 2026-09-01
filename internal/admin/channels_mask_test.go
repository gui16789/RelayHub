package admin

// Regression test: GET /admin/api/channels must mask API keys the same way
// /admin/api/status does — the list endpoint previously serialized raw keys.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/relayhub/internal/relay"
	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
)

func TestChannelListMasksKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `server:
  listen: ":0"
channels:
  - name: ch
    type: openai
    base_url: https://example.com
    api_keys:
      - sk-very-secret-key-1234
    models: [m]
    priority: 1
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgStore, err := store.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	collector := stats.NewCollector()
	server := NewServer(cfgStore, relay.NewHandler(cfgStore, collector), collector)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/channels", nil)
	rec := httptest.NewRecorder()
	server.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sk-very-secret-key-1234") {
		t.Fatalf("channel list leaked a raw API key: %s", rec.Body.String())
	}
	var views []channelView
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("not json: %v", err)
	}
	if len(views) != 1 || len(views[0].APIKeys) != 1 || !strings.HasPrefix(views[0].APIKeys[0], "****") {
		t.Fatalf("expected one masked key, got %+v", views)
	}
}
