package relay

// P1: channels can translate the client-facing model name into their
// upstream's own name (model_map), and every request leaves a routing trace
// in the collector so the console can show which channels were tried.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
)

// setupRelayMapped writes a config where the channel serves "client-model"
// but maps it to "upstream-model", and returns a handler + collector.
func setupRelayMapped(t *testing.T, upstreamURL, channelType string) (*Handler, *stats.Collector) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `server:
  listen: ":0"
channels:
  - name: ch
    type: ` + channelType + `
    base_url: ` + upstreamURL + `
    api_keys:
      - sk-test
    models:
      - client-model
    model_map:
      client-model: upstream-model
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
	return NewHandler(cfgStore, collector), collector
}

func postModelRaw(t *testing.T, handler http.Handler, model string) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestModelMapOpenAI(t *testing.T) {
	var receivedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		receivedModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	handler, _ := setupRelayMapped(t, upstream.URL, "openai")
	rec := postModelRaw(t, handler, "client-model")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if receivedModel != "upstream-model" {
		t.Errorf("upstream got model %q, want %q (model_map applied)", receivedModel, "upstream-model")
	}
	// The client-facing response must keep the client's model name.
	var resp struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Model != "client-model" {
		t.Errorf("response model = %q, want client-model", resp.Model)
	}
}

func TestModelMapGemini(t *testing.T) {
	var path string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":5}}`))
	}))
	defer upstream.Close()

	handler, _ := setupRelayMapped(t, upstream.URL, "gemini")
	rec := postModelRaw(t, handler, "client-model")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(path, "upstream-model") {
		t.Errorf("gemini path = %q, want it to contain upstream-model (mapped)", path)
	}
	if strings.Contains(path, "client-model") {
		t.Errorf("gemini path still references client-model: %q", path)
	}
}

func TestModelMapAnthropic(t *testing.T) {
	var receivedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		receivedModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":6,"output_tokens":7}}`))
	}))
	defer upstream.Close()

	handler, _ := setupRelayMapped(t, upstream.URL, "anthropic")
	rec := postModelRaw(t, handler, "client-model")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if receivedModel != "upstream-model" {
		t.Errorf("anthropic got model %q, want upstream-model (model_map applied)", receivedModel)
	}
}

// A request with no model_map entry must pass the model through unchanged.
func TestNoMapPassthrough(t *testing.T) {
	var receivedModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		receivedModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	// Channel declares "passthrough-model" with no model_map at all.
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `server:
  listen: ":0"
channels:
  - name: ch
    type: openai
    base_url: ` + upstream.URL + `
    api_keys: [sk-test]
    models: [passthrough-model]
    priority: 1
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgStore, err := store.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(cfgStore, stats.NewCollector())
	rec := postModelRaw(t, handler, "passthrough-model")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if receivedModel != "passthrough-model" {
		t.Errorf("model = %q, want passthrough-model (unchanged)", receivedModel)
	}
}

// TestTraceCaptured verifies a served request leaves a trace naming the
// channel, the hop outcome, and the tokens.
func TestTraceCaptured(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":11,"total_tokens":20}}`))
	}))
	defer upstream.Close()

	handler, collector := setupRelayMapped(t, upstream.URL, "openai")
	rec := postModelRaw(t, handler, "client-model")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	traces := collector.Traces(10)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	trace := traces[0]
	if trace.Model != "client-model" {
		t.Errorf("trace model = %q", trace.Model)
	}
	if trace.FinalStatus != http.StatusOK {
		t.Errorf("final status = %d", trace.FinalStatus)
	}
	if trace.FinalChannel != "ch" {
		t.Errorf("final channel = %q, want ch", trace.FinalChannel)
	}
	if trace.PromptTokens != 9 || trace.CompletionTokens != 11 {
		t.Errorf("trace tokens = %d/%d, want 9/11", trace.PromptTokens, trace.CompletionTokens)
	}
	if len(trace.Hops) != 1 || trace.Hops[0].Result != "served" {
		t.Errorf("hops = %+v, want one served hop", trace.Hops)
	}
}

// TestTraceFailover verifies a failed-then-served request records both hops
// and lands on the channel that ultimately served it.
func TestTraceFailover(t *testing.T) {
	// First channel always 500s; second serves.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
	}))
	defer failing.Close()
	working := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer working.Close()

	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `server:
  listen: ":0"
channels:
  - name: good
    type: openai
    base_url: ` + working.URL + `
    api_keys: [sk-a]
    models: [m]
    priority: 1
  - name: bad
    type: openai
    base_url: ` + failing.URL + `
    api_keys: [sk-b]
    models: [m]
    priority: 10
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgStore, err := store.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	collector := stats.NewCollector()
	handler := NewHandler(cfgStore, collector)

	rec := postModelRaw(t, handler, "m")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	traces := collector.Traces(10)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	trace := traces[0]
	if len(trace.Hops) != 2 {
		t.Fatalf("expected 2 hops (bad then good), got %d: %+v", len(trace.Hops), trace.Hops)
	}
	// Higher priority "bad" (10) is tried first and fails, then "good" serves.
	if trace.Hops[0].Channel != "bad" || trace.Hops[0].Result != "failed" {
		t.Errorf("first hop = %+v, want bad/failed", trace.Hops[0])
	}
	if trace.Hops[1].Channel != "good" || trace.Hops[1].Result != "served" {
		t.Errorf("second hop = %+v, want good/served", trace.Hops[1])
	}
	if trace.FinalChannel != "good" {
		t.Errorf("final channel = %q, want good", trace.FinalChannel)
	}
}
