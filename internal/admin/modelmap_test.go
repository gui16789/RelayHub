package admin

// Test for the inline model_map editor endpoint:
// PUT /admin/api/channels/{name}/model-map must update only the mapping
// (never touching API keys or the enabled flag) and reject malformed maps.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/relayhub/internal/relay"
	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
)

func newMapTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeTestConfig(t, configPath, `server:
  listen: ":0"
channels:
  - name: testch
    type: openai
    base_url: https://example.com
    api_keys:
      - sk-real-secret-key
    models:
      - mock-a
    priority: 1
`)
	cfgStore, err := store.NewStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	collector := stats.NewCollector()
	server := NewServer(cfgStore, relay.NewHandler(cfgStore, collector), collector)
	return server, cfgStore
}

// putMap sends a raw model-map PUT and returns the response recorder.
func putMap(server *Server, path, payload string) *httptest.ResponseRecorder {
	req, err := http.NewRequest(http.MethodPut, path, strings.NewReader(payload))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Mux().ServeHTTP(rec, req)
	return rec
}

func TestModelMapEndpoint(t *testing.T) {
	server, cfgStore := newMapTestServer(t)
	const path = "/admin/api/channels/testch/model-map"

	t.Run("update mapping keeps keys and enabled flag", func(t *testing.T) {
		rec := putMap(server, path, `{"model_map":{"client-a":"upstream-a","client-b":"upstream-b"}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		channel, err := cfgStore.GetChannel("testch")
		if err != nil {
			t.Fatal(err)
		}
		if len(channel.ModelMap) != 2 || channel.ModelMap["client-a"] != "upstream-a" || channel.ModelMap["client-b"] != "upstream-b" {
			t.Errorf("model_map = %v", channel.ModelMap)
		}
		if len(channel.APIKeys) != 1 || channel.APIKeys[0] != "sk-real-secret-key" {
			t.Errorf("api_keys changed: %v", channel.APIKeys)
		}
		if !channel.IsEnabled() {
			t.Error("enabled flag changed")
		}
	})

	t.Run("empty map clears the mapping", func(t *testing.T) {
		rec := putMap(server, path, `{"model_map":{}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		channel, err := cfgStore.GetChannel("testch")
		if err != nil {
			t.Fatal(err)
		}
		if len(channel.ModelMap) != 0 {
			t.Errorf("expected cleared model_map, got %v", channel.ModelMap)
		}
	})

	t.Run("empty-side entries rejected", func(t *testing.T) {
		if rec := putMap(server, path, `{"model_map":{"":"upstream-only"}}`); rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for empty client name, got %d", rec.Code)
		}
		if rec := putMap(server, path, `{"model_map":{"client-only":""}}`); rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for empty upstream name, got %d", rec.Code)
		}
	})

	t.Run("unknown channel 404s", func(t *testing.T) {
		rec := putMap(server, "/admin/api/channels/nope/model-map", `{"model_map":{"a":"b"}}`)
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", rec.Code)
		}
	})
}
