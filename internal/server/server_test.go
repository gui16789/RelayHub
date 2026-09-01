package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/relayhub/internal/store"
)

func setupStore(t *testing.T, apiKeys string) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `server:
  listen: ":0"
  api_key: "` + apiKeys + `"
channels: []
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := store.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestAuthMiddlewareEnforcesKey verifies third-party clients must present
// server.api_key once it is set, and can call freely while it is empty.
func TestAuthMiddlewareEnforcesKey(t *testing.T) {
	t.Run("no key configured: open access", func(t *testing.T) {
		handler := New(setupStore(t, ""))
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		var payload map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("not json: %v", err)
		}
	})

	t.Run("key configured: wrong or missing key rejected", func(t *testing.T) {
		handler := New(setupStore(t, "sk-secret"))
		for _, authorization := range []string{"", "Bearer sk-wrong"} {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			if authorization != "" {
				req.Header.Set("Authorization", authorization)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("auth %q: expected 401, got %d", authorization, rec.Code)
			}
		}
	})

	t.Run("key configured: correct key accepted", func(t *testing.T) {
		handler := New(setupStore(t, "sk-secret"))
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer sk-secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("admin console stays open for loopback even with a key set", func(t *testing.T) {
		handler := New(setupStore(t, "sk-secret"))
		req := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
		req.RemoteAddr = "127.0.0.1:43210"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("loopback admin must be exempt, got %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "channels") {
			t.Fatalf("status body unexpected: %s", rec.Body.String())
		}
	})

	t.Run("admin rejects non-loopback without admin key", func(t *testing.T) {
		handler := New(setupStore(t, "sk-secret"))
		req := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("remote admin must be forbidden, got %d", rec.Code)
		}
	})
}
