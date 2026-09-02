package relay

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/local/relayhub/internal/config"
)

// P0: a 429 must cool the key down for the upstream's Retry-After window
// (seconds or HTTP-date), capped at maxRetryAfter, falling back to the
// default quotaCooldown when the header is missing, invalid or expired.

func TestRetryAfterDuration(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{"seconds", "45", 45 * time.Second},
		{"default when absent", "", quotaCooldown},
		{"invalid value", "soon", quotaCooldown},
		{"zero", "0", quotaCooldown},
		{"negative", "-5", quotaCooldown},
		{"cap at max", "999999", maxRetryAfter},
		{"http date in past", "Fri, 01 Jan 2020 00:00:00 GMT", quotaCooldown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := make(http.Header)
			if tc.header != "" {
				header.Set("Retry-After", tc.header)
			}
			got := retryAfterDuration(&http.Response{StatusCode: http.StatusTooManyRequests, Header: header})
			if got != tc.want {
				t.Errorf("retryAfterDuration(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}

	t.Run("http date in future", func(t *testing.T) {
		when := time.Now().UTC().Add(30 * time.Second).UTC().Format(http.TimeFormat)
		header := make(http.Header)
		header.Set("Retry-After", when)
		got := retryAfterDuration(&http.Response{StatusCode: http.StatusTooManyRequests, Header: header})
		if got < 28*time.Second || got > 32*time.Second {
			t.Errorf("http-date retry after = %v, want ~30s", got)
		}
	})
}

func TestClassifyUpstream429(t *testing.T) {
	t.Run("honors seconds header", func(t *testing.T) {
		header := make(http.Header)
		header.Set("Retry-After", "30")
		outcome, cooldown, message := classifyUpstream(&http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
		}, "")
		if outcome != outcomeFailed {
			t.Fatalf("outcome = %v, want outcomeFailed", outcome)
		}
		if cooldown != 30*time.Second {
			t.Errorf("cooldown = %v, want 30s", cooldown)
		}
		if message == "" {
			t.Error("message empty")
		}
	})

	t.Run("no header uses default", func(t *testing.T) {
		_, cooldown, _ := classifyUpstream(&http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
		}, "")
		if cooldown != quotaCooldown {
			t.Errorf("cooldown = %v, want %v", cooldown, quotaCooldown)
		}
	})

	t.Run("quota body triggers backoff sentinel", func(t *testing.T) {
		_, cooldown, message := classifyUpstream(&http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
		}, `{"error":{"code":"DataQuotaExceed","message":"Data quota usage is exhausted."}}`)
		if cooldown != cooldownUseQuotaBackoff {
			t.Errorf("cooldown = %v, want quota backoff sentinel", cooldown)
		}
		if message == "" {
			t.Error("message empty")
		}
	})

	t.Run("rate-limit body stays short cooldown", func(t *testing.T) {
		_, cooldown, _ := classifyUpstream(&http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
		}, `{"error":{"message":"QPS rate limit exceeded"}}`)
		if cooldown != quotaCooldown {
			t.Errorf("cooldown = %v, want %v (rate limit, not quota)", cooldown, quotaCooldown)
		}
	})

	// SenseNova reports account-level TPM exhaustion as
	// ModelAccountTpmRateLimitExceeded. Its error code contains the substring
	// "ratelimit" and its message contains "tpm", so it must be classified
	// by the explicit modelaccount/tpm-exhausted markers BEFORE the generic
	// transient limits — otherwise it is retried every 60s on a window that
	// never refills that fast.
	t.Run("sensenova tpm exhaustion triggers quota backoff", func(t *testing.T) {
		_, cooldown, message := classifyUpstream(&http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
		}, `{"error":{"message":"inference tpm exhausted","type":"invalid_request_error","code":"ModelAccountTpmRateLimitExceeded"}}`)
		if cooldown != cooldownUseQuotaBackoff {
			t.Errorf("cooldown = %v, want quota backoff sentinel", cooldown)
		}
		if message == "" {
			t.Error("message empty")
		}
	})

	t.Run("auth errors unaffected", func(t *testing.T) {
		_, cooldown, _ := classifyUpstream(&http.Response{
			StatusCode: http.StatusUnauthorized,
			Header:     make(http.Header),
		}, "")
		if cooldown != authCooldown {
			t.Errorf("cooldown = %v, want %v", cooldown, authCooldown)
		}
	})
}

func TestQuotaResetHint(t *testing.T) {
	t.Run("epoch seconds header", func(t *testing.T) {
		header := make(http.Header)
		header.Set("X-Quota-Reset", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
		got := quotaResetHint(&http.Response{Header: header}, "")
		if got < 59*time.Minute || got > time.Hour {
			t.Errorf("hint = %v, want ~1h", got)
		}
	})

	t.Run("duration seconds header", func(t *testing.T) {
		header := make(http.Header)
		header.Set("X-RateLimit-Reset", "120")
		got := quotaResetHint(&http.Response{Header: header}, "")
		if got != 120*time.Second {
			t.Errorf("hint = %v, want 120s", got)
		}
	})

	t.Run("json body reset_at", func(t *testing.T) {
		reset := time.Now().Add(30 * time.Minute).Unix()
		body := `{"error":{"message":"quota exhausted","reset_at":` + strconv.FormatInt(reset, 10) + `}}`
		got := quotaResetHint(&http.Response{Header: make(http.Header)}, body)
		if got < 29*time.Minute || got > 30*time.Minute {
			t.Errorf("hint = %v, want ~30m", got)
		}
	})

	t.Run("no hint", func(t *testing.T) {
		got := quotaResetHint(&http.Response{Header: make(http.Header)}, `{"error":{"message":"quota exhausted"}}`)
		if got != 0 {
			t.Errorf("hint = %v, want 0", got)
		}
	})
}

// A quota 429 must park the key on the escalating ladder, not the flat 60s.
func TestQuotaExhaustionBackoffLadder(t *testing.T) {
	state := NewState()
	first := state.PenalizeQuota("ch", "sk-key", 0)
	if first < 4*time.Minute || first > 6*time.Minute {
		t.Fatalf("first strike = %v, want ~5m", first)
	}
	second := state.PenalizeQuota("ch", "sk-key", 0)
	if second < 8*time.Minute || second > 12*time.Minute {
		t.Fatalf("second strike = %v, want ~10m (escalated)", second)
	}

	// An explicit reset hint wins over the ladder.
	hinted := state.PenalizeQuota("ch", "sk-key2", 2*time.Hour)
	if hinted < 96*time.Minute || hinted > 144*time.Minute {
		t.Fatalf("hinted = %v, want ~2h (reset hint honored with jitter)", hinted)
	}

	// Success (half-open probe) resets the strike count.
	state.ResetQuota("ch", "sk-key")
	again := state.PenalizeQuota("ch", "sk-key", 0)
	if again < 4*time.Minute || again > 6*time.Minute {
		t.Fatalf("after reset = %v, want ~5m (ladder restarted)", again)
	}
}

// Quota cooldowns must survive a simulated restart via the persistence file,
// and must do so without writing a usable credential to disk.
func TestQuotaCooldownPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cooldowns.json")

	state := NewState()
	state.SetPersistence(path)
	state.PenalizeQuota("ch", "sk-persisted", time.Hour)
	state.Penalize("ch", "sk-short", time.Minute) // rate limit: NOT persisted

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("persistence file missing: %v", err)
	}
	// The security property: the file identifies the key by fingerprint, so
	// the key itself must not appear anywhere in it.
	if strings.Contains(string(raw), "sk-persisted") {
		t.Errorf("plaintext key must never be persisted: %s", raw)
	}
	if !strings.Contains(string(raw), keyFingerprint("sk-persisted")) {
		t.Errorf("fingerprint missing, entry cannot be restored: %s", raw)
	}
	// The tail is still there so the console can name the key for an operator.
	if !strings.Contains(string(raw), `"key_tail": "sted"`) {
		t.Errorf("key_tail missing, admin console cannot identify the key: %s", raw)
	}
	if strings.Contains(string(raw), "sk-short") {
		t.Fatalf("rate-limit entry must not persist: %s", raw)
	}

	restored := NewState()
	restored.SetPersistence(path)
	keys := restored.OrderedKeys(config.Channel{Name: "ch", APIKeys: []string{"sk-persisted", "sk-short"}}, config.KeyStrategyRoundRobin)
	if len(keys) != 1 || keys[0] != "sk-short" {
		t.Fatalf("restored keys = %v, want only sk-short usable", keys)
	}
	// The restored entry must still be attributable in the console.
	cooldowns := restored.Cooldowns()
	if len(cooldowns) != 1 || cooldowns[0].KeyTail != "sted" || cooldowns[0].Channel != "ch" {
		t.Errorf("restored cooldown lost its identity: %+v", cooldowns)
	}
}

// A cooldowns.json written before fingerprinting carries plaintext keys. It
// must still restore (otherwise an upgrade wakes every exhausted key at once)
// and the plaintext must be gone from disk once it has.
func TestLegacyPlaintextCooldownMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cooldowns.json")
	until := time.Now().Add(time.Hour).Format(time.RFC3339Nano)
	legacy := `{"quota":[{"channel":"ch","key_tail":"key1","key":"sk-legacy-key1",` +
		`"until":"` + until + `","attempts":2}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	state := NewState()
	state.SetPersistence(path)

	// The key is still parked, so it must be filtered out of the try order.
	keys := state.OrderedKeys(
		config.Channel{Name: "ch", APIKeys: []string{"sk-legacy-key1", "sk-other"}},
		config.KeyStrategyRoundRobin)
	if len(keys) != 1 || keys[0] != "sk-other" {
		t.Fatalf("legacy cooldown not honored, keys = %v", keys)
	}
	// The strike count has to survive too, or the backoff ladder restarts.
	cooldowns := state.Cooldowns()
	if len(cooldowns) != 1 || cooldowns[0].Attempts != 2 {
		t.Errorf("legacy attempts lost: %+v", cooldowns)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-legacy-key1") {
		t.Errorf("migration must rewrite the file without the plaintext key: %s", raw)
	}
	if !strings.Contains(string(raw), keyFingerprint("sk-legacy-key1")) {
		t.Errorf("migrated entry should carry the fingerprint: %s", raw)
	}
}

// End-to-end: a 429 with Retry-After: 90 must land the key in cooldown for
// ~90s, visible through the handler state.
func TestUpstream429RetryAfterCooldown(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer upstream.Close()

	handler, _ := setupRelay(t, upstream.URL)

	rec := postChat(t, handler, "good")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 after single failed attempt, got %d: %s", rec.Code, rec.Body.String())
	}

	cooldowns := handler.State().Cooldowns()
	if len(cooldowns) != 1 {
		t.Fatalf("expected 1 cooldown, got %d: %+v", len(cooldowns), cooldowns)
	}
	remainSec := cooldowns[0].RemainMS / 1000
	if remainSec < 88 || remainSec > 90 {
		t.Errorf("cooldown remain = %ds, want ~90s (Retry-After honored)", remainSec)
	}

	// With the only key cooling down, a retry must get a retryable 503
	// (not a misleading 404 or 502) so clients back off and retry.
	rec2 := postChat(t, handler, "good")
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("second request: expected 503 while cooled, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

// A model no enabled channel declares must stay a 404 (config gap), distinct
// from the "all keys cooling down" 503.
func TestUnroutedModelStill404(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	handler, _ := setupRelay(t, upstream.URL)
	rec := postChat(t, handler, "not-configured-anywhere")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unconfigured model, got %d: %s", rec.Code, rec.Body.String())
	}
}
