package relay

// Regression tests for the hardening fixes:
// - oversized client bodies are rejected with 413 instead of being read
//   into memory unboundedly
// - a 503 caused by all-keys-cooling-down carries a Retry-After header

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
)

func setupHardening(t *testing.T, configYAML string) *Handler {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgStore, err := store.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(cfgStore, stats.NewCollector())
}

func TestOversizedBodyRejected(t *testing.T) {
	handler := setupHardening(t, `server:
  listen: ":0"
channels:
  - name: ch
    type: openai
    base_url: http://127.0.0.1:1
    api_keys: [sk-test]
    models: [m]
    priority: 1
`)
	payload, _ := json.Marshal(map[string]any{
		"model":    "m",
		"messages": []map[string]string{{"role": "user", "content": strings.Repeat("x", int(maxRequestBody))}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRetryAfterOnAllCoolingDown(t *testing.T) {
	handler := setupHardening(t, `server:
  listen: ":0"
channels:
  - name: ch
    type: openai
    base_url: http://127.0.0.1:1
    api_keys: [sk-test]
    models: [m]
    priority: 1
`)
	// Park the only key so the next request finds no usable attempt.
	handler.state.Penalize("ch", "sk-test", 90*time.Second)

	payload, _ := json.Marshal(map[string]any{
		"model":    "m",
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(payload)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", rec.Code, rec.Body.String())
	}
	header := rec.Header().Get("Retry-After")
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds <= 0 || seconds > 90 {
		t.Fatalf("Retry-After %q: want a value in (0, 90]", header)
	}
}
