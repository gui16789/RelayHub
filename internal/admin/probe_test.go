package admin

// Integration test for the "edit channel does not lose keys" flow:
// key echo endpoint, probe fallback to stored keys, and preserving
// the enabled flag across a form-style update.

import (
	"encoding/json"
	"io"
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

func writeTestConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// startMockUpstream serves /v1/models and only authenticates goodKey.
func startMockUpstream(goodKey string) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+goodKey {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"mock-a"},{"id":"mock-b"}]}`))
	})
	return httptest.NewServer(mux)
}

func doJSON(t *testing.T, method, apiBase, path string, payload any) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(method, apiBase+path, strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	if result == nil {
		result = map[string]any{}
	}
	return resp.StatusCode, result
}

func TestEditChannelKeepsKeysAndEnabled(t *testing.T) {
	const goodKey = "sk-stored-good"
	upstream := startMockUpstream(goodKey)
	defer upstream.Close()

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeTestConfig(t, configPath, `server:
  listen: ":0"
channels:
  - name: testch
    type: openai
    base_url: `+upstream.URL+`
    api_keys:
      - `+goodKey+`
    models:
      - mock-a
    priority: 1
    enabled: false
`)

	cfgStore, err := store.NewStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	collector := stats.NewCollector()
	relayHandler := relay.NewHandler(cfgStore, collector)
	server := NewServer(cfgStore, relayHandler, collector)

	apiBase := httptest.NewServer(server.Mux())
	defer apiBase.Close()

	t.Run("keys endpoint returns unmasked key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/channels/testch/keys", nil)
		rec := httptest.NewRecorder()
		server.Mux().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		var result map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &result)
		keys, _ := result["api_keys"].([]any)
		if len(keys) != 1 || keys[0] != goodKey {
			t.Fatalf("expected [%s], got %v", goodKey, result["api_keys"])
		}
	})

	t.Run("form-style update keeps keys and enabled flag", func(t *testing.T) {
		// The form submits empty api_keys and no enabled field.
		status, _ := doJSON(t, http.MethodPut, apiBase.URL, "/admin/api/channels/testch", map[string]any{
			"name": "testch", "type": "openai", "base_url": upstream.URL,
			"api_keys": []string{}, "models": []string{"mock-a", "mock-b"}, "priority": 1,
		})
		if status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
		channel, err := cfgStore.GetChannel("testch")
		if err != nil {
			t.Fatal(err)
		}
		if len(channel.APIKeys) != 1 || channel.APIKeys[0] != goodKey {
			t.Fatalf("key lost on update: %v", channel.APIKeys)
		}
		if channel.Enabled == nil || *channel.Enabled != false {
			t.Fatalf("enabled flag was reset: %+v", channel.Enabled)
		}
		if len(channel.Models) != 2 {
			t.Fatalf("models not saved: %v", channel.Models)
		}
	})

	t.Run("probe falls back to stored key when form has none", func(t *testing.T) {
		// Simulates the edit-mode one-click fetch: form keys empty,
		// channel name provided, base_url as the mock upstream.
		status, result := doJSON(t, http.MethodPost, apiBase.URL, "/admin/api/probe-models", map[string]any{
			"channel": "testch", "type": "openai", "base_url": upstream.URL, "api_keys": []string{},
		})
		if status != http.StatusOK {
			t.Fatalf("probe status %d: %v", status, result)
		}
		models, _ := result["models"].([]any)
		if len(models) != 2 {
			t.Fatalf("expected 2 models from fallback probe, got %v", result["models"])
		}
	})

	t.Run("probe with wrong key still fails clearly", func(t *testing.T) {
		status, result := doJSON(t, http.MethodPost, apiBase.URL, "/admin/api/probe-models", map[string]any{
			"channel": "testch", "type": "openai", "base_url": upstream.URL, "api_keys": []string{"sk-wrong"},
		})
		if status != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d: %v", status, result)
		}
		if errText, _ := result["error"].(string); !strings.Contains(errText, "bad key") {
			t.Fatalf("expected upstream error text, got %v", result["error"])
		}
	})

	t.Run("unknown channel probe reports missing keys", func(t *testing.T) {
		status, result := doJSON(t, http.MethodPost, apiBase.URL, "/admin/api/probe-models", map[string]any{
			"channel": "ghost", "type": "openai", "base_url": upstream.URL, "api_keys": []string{},
		})
		if status != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %v", status, result)
		}
	})

	t.Run("server api key round-trips through the store", func(t *testing.T) {
		// Enable a key via the admin endpoint.
		status, _ := doJSON(t, http.MethodPut, apiBase.URL, "/admin/api/server", map[string]any{"api_key": "sk-third-party-key"})
		if status != http.StatusOK {
			t.Fatalf("PUT status %d", status)
		}
		snapshot := cfgStore.Snapshot()
		if snapshot.Server.APIKey != "sk-third-party-key" {
			t.Fatalf("api key not persisted: %q", snapshot.Server.APIKey)
		}
		// Status must expose it (local admin context).
		status, result := doJSON(t, http.MethodGet, apiBase.URL, "/admin/api/status", nil)
		if status != http.StatusOK {
			t.Fatalf("GET status %d", status)
		}
		if result["api_key"] != "sk-third-party-key" {
			t.Fatalf("status api_key: %v", result["api_key"])
		}
		if result["listen"] == "" {
			t.Fatalf("status listen missing: %v", result["listen"])
		}
		// Clear it again.
		status, _ = doJSON(t, http.MethodPut, apiBase.URL, "/admin/api/server", map[string]any{"api_key": ""})
		if status != http.StatusOK {
			t.Fatalf("clear PUT status %d", status)
		}
		if cfgStore.Snapshot().Server.APIKey != "" {
			t.Fatalf("api key not cleared")
		}
	})
}
