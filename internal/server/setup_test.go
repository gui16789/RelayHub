package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/relayhub/internal/store"
)

// missingStore builds a store whose config file does NOT exist — the
// first-boot scenario the setup wizard is designed for.
func missingStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.NewStore(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func remoteRequest(method, path, body string) *http.Request {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	req.RemoteAddr = "192.0.2.1:1234" // documentation range: guaranteed remote
	return req
}

// TestMissingConfigStartsWithDefaults verifies the zero-config first boot:
// no config file, server still comes up with the default listen address.
func TestMissingConfigStartsWithDefaults(t *testing.T) {
	st := missingStore(t)
	snapshot := st.Snapshot()
	if snapshot.Server.Listen != ":8787" {
		t.Fatalf("default listen = %q, want :8787", snapshot.Server.Listen)
	}
	if len(snapshot.Channels) != 0 {
		t.Fatalf("expected no channels, got %d", len(snapshot.Channels))
	}
}

// TestSetupWizardFlow covers the whole remote first-boot journey:
// setup open -> wizard initializes keys+channel -> wizard closes and the
// normal admin_key rules take over.
func TestSetupWizardFlow(t *testing.T) {
	handler := New(missingStore(t))

	// Before setup: remote admin API is forbidden, but the wizard is open.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, remoteRequest(http.MethodGet, "/admin/api/status", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status before setup: got %d, want 403", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, remoteRequest(http.MethodGet, "/admin/api/setup", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"needs_setup":true`) {
		t.Fatalf("setup status: got %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, remoteRequest(http.MethodGet, "/admin/setup", ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "首次初始化") {
		t.Fatalf("setup page: got %d", rec.Code)
	}

	// Root redirects a remote first-time visitor to the wizard.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, remoteRequest(http.MethodGet, "/", ""))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/admin/setup" {
		t.Fatalf("root redirect: got %d -> %q", rec.Code, rec.Header().Get("Location"))
	}

	// Complete setup in one POST.
	body := `{
	  "admin_key": "adm-secret",
	  "api_key": "sk-client",
	  "channel": {
	    "name": "openai",
	    "type": "openai",
	    "base_url": "https://api.openai.com",
	    "api_keys": ["sk-upstream"],
	    "models": ["gpt-4o"]
	  }
	}`
	req := remoteRequest(http.MethodPost, "/admin/api/setup", body)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup POST: got %d %s", rec.Code, rec.Body.String())
	}

	// Wizard is now closed: replay is refused, the page redirects away.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("setup replay: got %d, want 403", rec.Code)
	}
	// The wizard page itself is behind admin auth now: without the key it is
	// forbidden; with the key it redirects to the console.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, remoteRequest(http.MethodGet, "/admin/setup", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("setup page after init without key: got %d, want 403", rec.Code)
	}
	req = remoteRequest(http.MethodGet, "/admin/setup", "")
	req.Header.Set("Authorization", "Bearer adm-secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("setup page after init with key: got %d, want redirect", rec.Code)
	}

	// Remote admin API now requires the admin key and accepts it.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, remoteRequest(http.MethodGet, "/admin/api/status", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status without admin key: got %d, want 403", rec.Code)
	}
	req = remoteRequest(http.MethodGet, "/admin/api/status", "")
	req.Header.Set("Authorization", "Bearer adm-secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status with admin key: got %d", rec.Code)
	}
	var status struct {
		Channels []struct {
			Name string `json:"name"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Channels) != 1 || status.Channels[0].Name != "openai" {
		t.Fatalf("wizard channel missing from status: %+v", status.Channels)
	}

	// And the client API key is enforced on /v1/*.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, remoteRequest(http.MethodGet, "/v1/models", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/v1/models without api key: got %d, want 401", rec.Code)
	}
	req = remoteRequest(http.MethodGet, "/v1/models", "")
	req.Header.Set("Authorization", "Bearer sk-client")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "gpt-4o") {
		t.Fatalf("/v1/models with api key: got %d %s", rec.Code, rec.Body.String())
	}
}

// TestSetupRejectsBadInput ensures a failed wizard POST leaves setup open.
func TestSetupRejectsBadInput(t *testing.T) {
	handler := New(missingStore(t))

	req := remoteRequest(http.MethodPost, "/admin/api/setup", `{"admin_key": ""}`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty admin_key: got %d, want 400", rec.Code)
	}

	// Invalid channel must not apply anything: admin key stays unset.
	req = remoteRequest(http.MethodPost, "/admin/api/setup",
		`{"admin_key":"adm-secret","channel":{"name":"bad","base_url":"","api_keys":[],"models":[]}}`)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid channel: got %d, want 400", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, remoteRequest(http.MethodGet, "/admin/api/setup", ""))
	if !strings.Contains(rec.Body.String(), `"needs_setup":true`) {
		t.Fatalf("setup must stay open after a failed POST: %s", rec.Body.String())
	}
}
