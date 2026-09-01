package relay

// Tests for the global key_strategy setting (server.key_strategy):
// "round_robin" (default) rotates the starting key per request, while
// "preferred_first" always starts from the first configured key and only
// fails over to the next key when the preferred one errors or cools down.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/local/relayhub/internal/config"
	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newHandlerForTest(t *testing.T, cfgPath string) (*Handler, *stats.Collector) {
	t.Helper()
	cfgStore, err := store.NewStore(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	collector := stats.NewCollector()
	return NewHandler(cfgStore, collector), collector
}

// Unit: preferred_first always starts from the first key, and a cooled-down
// first key is skipped (failover to the remaining keys).
func TestOrderedKeysPreferredFirst(t *testing.T) {
	s := NewState()
	ch := config.Channel{Name: "ch", APIKeys: []string{"sk-a", "sk-b", "sk-c"}}

	k1 := s.OrderedKeys(ch, config.KeyStrategyPreferredFirst)
	k2 := s.OrderedKeys(ch, config.KeyStrategyPreferredFirst)
	if !reflect.DeepEqual(k1, []string{"sk-a", "sk-b", "sk-c"}) {
		t.Errorf("first call = %v, want [sk-a sk-b sk-c]", k1)
	}
	if !reflect.DeepEqual(k2, k1) {
		t.Errorf("second call = %v, want same order %v (sticky first key)", k2, k1)
	}

	// Cool down the preferred key: it must drop out of the order entirely.
	s.Penalize("ch", "sk-a", time.Minute)
	k3 := s.OrderedKeys(ch, config.KeyStrategyPreferredFirst)
	if !reflect.DeepEqual(k3, []string{"sk-b", "sk-c"}) {
		t.Errorf("after cooling sk-a = %v, want [sk-b sk-c]", k3)
	}
}

// Unit: the default (round_robin / empty) still rotates the starting key.
func TestOrderedKeysRoundRobinDefault(t *testing.T) {
	s := NewState()
	ch := config.Channel{Name: "ch", APIKeys: []string{"sk-a", "sk-b", "sk-c"}}

	got1 := s.OrderedKeys(ch, "")
	got2 := s.OrderedKeys(ch, config.KeyStrategyRoundRobin)
	got3 := s.OrderedKeys(ch, "")
	got4 := s.OrderedKeys(ch, "")
	if !reflect.DeepEqual(got1, []string{"sk-a", "sk-b", "sk-c"}) {
		t.Errorf("call 1 = %v, want [sk-a sk-b sk-c]", got1)
	}
	if !reflect.DeepEqual(got2, []string{"sk-b", "sk-c", "sk-a"}) {
		t.Errorf("call 2 = %v, want [sk-b sk-c sk-a]", got2)
	}
	if !reflect.DeepEqual(got3, []string{"sk-c", "sk-a", "sk-b"}) {
		t.Errorf("call 3 = %v, want [sk-c sk-a sk-b]", got3)
	}
	if !reflect.DeepEqual(got4, []string{"sk-a", "sk-b", "sk-c"}) {
		t.Errorf("call 4 = %v, want [sk-a sk-b sk-c]", got4)
	}
}

// End-to-end: with preferred_first, a 429 on the first key fails over to the
// second key within the same request, and the NEXT request goes straight to
// the second key (the first one is cooling down).
func TestPreferredFirstFailoverE2E(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("Authorization") {
		case "Bearer sk-first":
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
		default: // Bearer sk-second
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		}
	}))
	defer upstream.Close()

	content := `server:
  listen: ":0"
  key_strategy: preferred_first
channels:
  - name: ch
    type: openai
    base_url: ` + upstream.URL + `
    api_keys:
      - sk-first
      - sk-second
    models:
      - m
    priority: 1
`
	handler, collector := newHandlerForTest(t, writeConfig(t, content))

	// Request 1: sk-first 429s, fail over to sk-second, served.
	rec := postChat(t, handler, "m")
	if rec.Code != http.StatusOK {
		t.Fatalf("request 1 status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	traces := collector.Traces(10)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	trace := traces[0]
	if len(trace.Hops) != 2 {
		t.Fatalf("request 1 hops = %d, want 2 (sk-first failed, sk-second served)", len(trace.Hops))
	}
	if trace.Hops[0].KeyTail != "irst" || trace.Hops[0].Result != "failed" {
		t.Errorf("hop 1 = %+v, want sk-first failed", trace.Hops[0])
	}
	if trace.Hops[1].KeyTail != "cond" || trace.Hops[1].Result != "served" {
		t.Errorf("hop 2 = %+v, want sk-second served", trace.Hops[1])
	}

	// Request 2: sk-first is cooling down, so it must go straight to sk-second.
	rec = postChat(t, handler, "m")
	if rec.Code != http.StatusOK {
		t.Fatalf("request 2 status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	traces = collector.Traces(10)
	trace = traces[0]
	if len(trace.Hops) != 1 {
		t.Fatalf("request 2 hops = %d, want 1 (sk-first cooling down)", len(trace.Hops))
	}
	if trace.Hops[0].KeyTail != "cond" || trace.Hops[0].Result != "served" {
		t.Errorf("request 2 hop = %+v, want sk-second served", trace.Hops[0])
	}
}

// End-to-end: with preferred_first and healthy keys, consecutive requests
// all use the first key (no rotation), unlike round_robin which alternates.
func TestPreferredFirstStickyE2E(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	content := `server:
  listen: ":0"
  key_strategy: preferred_first
channels:
  - name: ch
    type: openai
    base_url: ` + upstream.URL + `
    api_keys:
      - sk-first
      - sk-second
    models:
      - m
    priority: 1
`
	handler, collector := newHandlerForTest(t, writeConfig(t, content))

	for i := 1; i <= 3; i++ {
		rec := postChat(t, handler, "m")
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, rec.Code)
		}
	}
	traces := collector.Traces(10)
	if len(traces) != 3 {
		t.Fatalf("expected 3 traces, got %d", len(traces))
	}
	for i, trace := range traces {
		if len(trace.Hops) != 1 || trace.Hops[0].KeyTail != "irst" {
			t.Errorf("request %d hops = %+v, want single hop on sk-first (sticky)", i+1, trace.Hops)
		}
	}
}
