package relay

// P2: error-aware cooling (5xx streaks), proactive health probing and
// /v1/models consistency with routable channels.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
)

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
}

func writeChannelConfig(t *testing.T, channels string) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "server:\n  listen: \":0\"\nchannels:\n" + channels
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgStore, err := store.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfgStore
}

const twoChannelYAML = `  - name: bad
    type: openai
    base_url: %s
    api_keys: [sk-bad]
    models: [m]
    priority: 10
  - name: good
    type: openai
    base_url: %s
    api_keys: [sk-good]
    models: [m]
    priority: 1
`

// setupTwoChannel wires a config where "bad" (higher priority) always
// answers with the given status and "good" serves 200.
func setupTwoChannel(t *testing.T, badStatus int) (*Handler, *stats.Collector) {
	t.Helper()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(badStatus)
		_, _ = w.Write([]byte(`{"error":{"message":"bad upstream"}}`))
	}))
	t.Cleanup(bad.Close)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w)
	}))
	t.Cleanup(good.Close)

	cfgStore := writeChannelConfig(t, sprintfYAML(twoChannelYAML, bad.URL, good.URL))
	collector := stats.NewCollector()
	return NewHandler(cfgStore, collector), collector
}

func sprintfYAML(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// TestServerErrorStreakCoolsKey verifies that 3 consecutive 5xx from one
// key rest it (error-aware cooling), so the next request goes straight to
// the working channel instead of paying one dead attempt first.
func TestServerErrorStreakCoolsKey(t *testing.T) {
	handler, collector := setupTwoChannel(t, http.StatusInternalServerError)

	for i := 0; i < 3; i++ {
		rec := postModelRaw(t, handler, "m")
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d: %s", i+1, rec.Code, rec.Body.String())
		}
	}
	cooldowns := handler.State().Cooldowns()
	if len(cooldowns) != 1 {
		t.Fatalf("expected 1 cooldown after 3x 5xx, got %d: %+v", len(cooldowns), cooldowns)
	}
	if cooldowns[0].Channel != "bad" {
		t.Errorf("cooldown on channel %q, want bad", cooldowns[0].Channel)
	}

	// Request 4: the bad key is cooling, so only the good channel should run.
	rec := postModelRaw(t, handler, "m")
	if rec.Code != http.StatusOK {
		t.Fatalf("request 4: status %d: %s", rec.Code, rec.Body.String())
	}
	traces := collector.Traces(10)
	if len(traces) != 4 {
		t.Fatalf("expected 4 traces, got %d", len(traces))
	}
	last := traces[0]
	if len(last.Hops) != 1 || last.Hops[0].Channel != "good" {
		t.Errorf("request 4 hops = %+v, want a single good hop (bad cooled)", last.Hops)
	}
}

// TestServerErrorStreakResets verifies that an intervening success breaks
// the 5xx streak: 2 errors + success + 2 more errors must NOT cool the key.
func TestServerErrorStreakResets(t *testing.T) {
	var (
		mu    sync.Mutex
		calls int
	)
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		c := calls
		mu.Unlock()
		// Fail on calls 1,2,4,5; succeed on call 3 to break the streak.
		if c == 3 {
			writeOK(w)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"flaky"}}`))
	}))
	t.Cleanup(flaky.Close)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w)
	}))
	t.Cleanup(good.Close)

	cfgStore := writeChannelConfig(t, sprintfYAML(twoChannelYAML, flaky.URL, good.URL))
	handler := NewHandler(cfgStore, stats.NewCollector())

	for i := 0; i < 5; i++ {
		rec := postModelRaw(t, handler, "m")
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, rec.Code)
		}
	}
	if cooldowns := handler.State().Cooldowns(); len(cooldowns) != 0 {
		t.Errorf("streak was reset by the success, expected no cooldown, got %+v", cooldowns)
	}
}

// TestHealthProbeSkipsDownChannel verifies a channel the probe loop marks
// down is skipped by the router: with the only channel down the request
// gets a retryable 503, and after recovery it flows again.
func TestHealthProbeSkipsDownChannel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w)
	}))
	defer upstream.Close()

	cfgStore := writeChannelConfig(t, sprintfYAML(twoChannelYAML, upstream.URL, upstream.URL))
	collector := stats.NewCollector()
	handler := NewHandler(cfgStore, collector)
	probeUp := true
	handler.SetHealthProbe(func(channelType, baseURL string, apiKeys []string) bool { return probeUp })

	// Two consecutive probe failures mark the channels down.
	probeUp = false
	handler.probeOnce()
	handler.probeOnce()
	if !handler.State().IsDown("bad") {
		t.Fatal("channel should be down after 2 failed probes")
	}
	rec := postModelRaw(t, handler, "m")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("all-down model: status %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "no upstream is available") {
		t.Errorf("503 body = %q", rec.Body.String())
	}

	// A passing probe brings the channels back.
	probeUp = true
	handler.probeOnce()
	if handler.State().IsDown("bad") {
		t.Fatal("channel should recover after a successful probe")
	}
	rec = postModelRaw(t, handler, "m")
	if rec.Code != http.StatusOK {
		t.Fatalf("recovered model: status %d: %s", rec.Code, rec.Body.String())
	}
}

func eventsContain(events []stats.Event, needle string) bool {
	for _, event := range events {
		if strings.Contains(event.Message, needle) {
			return true
		}
	}
	return false
}

// TestHealthFlipEvents verifies the probe loop logs a warning when a
// channel flips down and an info line when it comes back.
func TestHealthFlipEvents(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w)
	}))
	defer upstream.Close()

	cfgStore := writeChannelConfig(t, sprintfYAML(twoChannelYAML, upstream.URL, upstream.URL))
	collector := stats.NewCollector()
	handler := NewHandler(cfgStore, collector)
	probeUp := true
	handler.SetHealthProbe(func(channelType, baseURL string, apiKeys []string) bool { return probeUp })

	probeUp = false
	handler.probeOnce()
	handler.probeOnce()
	if !eventsContain(collector.Events(50), "unreachable") {
		t.Errorf("expected a down-flip event, got %+v", collector.Events(50))
	}
	probeUp = true
	handler.probeOnce()
	if !eventsContain(collector.Events(50), "reachable again") {
		t.Errorf("expected a recovery event, got %+v", collector.Events(50))
	}
}

// TestHealthProbeSkipsChannelWithHigherPriority verifies that a down
// channel is skipped even when it outranks a working one (the probe
// overrides the static priority order).
func TestHealthProbeSkipsChannelWithHigherPriority(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w)
	}))
	t.Cleanup(bad.Close)
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeOK(w)
	}))
	t.Cleanup(good.Close)

	cfgStore := writeChannelConfig(t, sprintfYAML(twoChannelYAML, bad.URL, good.URL))
	collector := stats.NewCollector()
	handler := NewHandler(cfgStore, collector)
	handler.SetHealthProbe(func(channelType, baseURL string, apiKeys []string) bool {
		return baseURL != bad.URL
	})
	// Two probe passes so the consecutive-failure threshold is met.
	handler.probeOnce()
	handler.probeOnce()

	rec := postModelRaw(t, handler, "m")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	traces := collector.Traces(10)
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	if len(traces[0].Hops) != 1 || traces[0].Hops[0].Channel != "good" {
		t.Errorf("hops = %+v, want a single good hop (bad probed down)", traces[0].Hops)
	}
}

// TestStreakWindowExpiry verifies a 5xx streak older than the window does
// not count toward the threshold.
func TestStreakWindowExpiry(t *testing.T) {
	state := NewState()
	state.MarkServerError("ch", "sk")
	state.MarkServerError("ch", "sk")
	// Age the streak past the window.
	state.mu.Lock()
	entry := state.serverStreaks[keyIdentity("ch", "sk")]
	entry.lastSeen = time.Now().Add(-streakWindow - time.Minute)
	state.serverStreaks[keyIdentity("ch", "sk")] = entry
	state.mu.Unlock()

	if count := state.MarkServerError("ch", "sk"); count != 1 {
		t.Errorf("streak after window expiry = %d, want 1 (fresh count)", count)
	}
	state.ResetServerStreak("ch", "sk")
	state.MarkServerError("ch", "sk")
	state.MarkServerError("ch", "sk")
	if count := state.MarkServerError("ch", "sk"); count != 3 {
		t.Errorf("streak = %d, want 3", count)
	}
}
