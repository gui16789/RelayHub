package relay

import (
	"bytes"
	"testing"
	"time"
)

func TestCacheHitMiss(t *testing.T) {
	cache := NewResponseCache()
	endpoint := "/v1/embeddings"
	model := "text-embedding-3-small"
	body := []byte(`{"input":"hello world"}`)

	// Miss on first request
	if _, ok := cache.Get(endpoint, model, body); ok {
		t.Error("expected cache miss on first request")
	}

	// Set a response
	response := []byte(`{"data":[{"embedding":[0.1,0.2]}]}`)
	cache.Set(endpoint, model, body, response, 200, "application/json", 2, 5, 1*time.Hour)

	// Hit on second request
	entry, ok := cache.Get(endpoint, model, body)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if !bytes.Equal(entry.response, response) {
		t.Error("cached response does not match")
	}
	if entry.status != 200 {
		t.Errorf("expected status 200, got %d", entry.status)
	}
}

func TestCacheExpiry(t *testing.T) {
	cache := NewResponseCache()
	endpoint := "/v1/embeddings"
	model := "text-embedding-3-small"
	body := []byte(`{"input":"test"}`)

	// Set with very short TTL
	cache.Set(endpoint, model, body, []byte("response"), 200, "application/json", 0, 0, 10*time.Millisecond)

	// Should hit immediately
	if _, ok := cache.Get(endpoint, model, body); !ok {
		t.Error("expected cache hit before expiry")
	}

	// Wait for expiry
	time.Sleep(20 * time.Millisecond)

	// Should miss after expiry
	if _, ok := cache.Get(endpoint, model, body); ok {
		t.Error("expected cache miss after expiry")
	}
}

func TestCacheIsolation(t *testing.T) {
	cache := NewResponseCache()
	endpoint := "/v1/embeddings"
	model := "text-embedding-3-small"
	body1 := []byte(`{"input":"hello"}`)
	body2 := []byte(`{"input":"world"}`)

	cache.Set(endpoint, model, body1, []byte("response1"), 200, "application/json", 0, 0, 1*time.Hour)
	cache.Set(endpoint, model, body2, []byte("response2"), 200, "application/json", 0, 0, 1*time.Hour)

	// Different bodies should have different cache entries
	entry1, ok1 := cache.Get(endpoint, model, body1)
	entry2, ok2 := cache.Get(endpoint, model, body2)

	if !ok1 || !ok2 {
		t.Fatal("expected both entries to be cached")
	}
	if bytes.Equal(entry1.response, entry2.response) {
		t.Error("different requests should not share cache entries")
	}
}

func TestRequestCoalescing(t *testing.T) {
	cache := NewResponseCache()
	endpoint := "/v1/embeddings"
	model := "text-embedding-3-small"
	body := []byte(`{"input":"coalesce test"}`)

	// First goroutine wins the race
	_, won1 := cache.StartInflight(endpoint, model, body)
	if !won1 {
		t.Fatal("first caller should win")
	}

	// Second goroutine should lose (another is already fetching)
	_, won2 := cache.StartInflight(endpoint, model, body)
	if won2 {
		t.Error("second caller should not win while first is inflight")
	}

	// Simulate the first goroutine completing
	go func() {
		defer cache.FinishInflight(endpoint, model, body)
		time.Sleep(50 * time.Millisecond)
		cache.Set(endpoint, model, body, []byte("shared result"), 200, "application/json", 0, 0, 1*time.Hour)
	}()

	// Second goroutine should now Get and block until done
	start := time.Now()
	entry, ok := cache.Get(endpoint, model, body)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("expected to get coalesced result")
	}
	if string(entry.response) != "shared result" {
		t.Errorf("got %q, want %q", string(entry.response), "shared result")
	}
	if elapsed < 40*time.Millisecond {
		t.Error("Get should have blocked until inflight completed")
	}
}

func TestCacheEviction(t *testing.T) {
	cache := NewResponseCache()
	endpoint := "/v1/embeddings"
	model := "text-embedding-3-small"

	// Add expired entry
	body1 := []byte(`{"input":"expired"}`)
	cache.Set(endpoint, model, body1, []byte("old"), 200, "application/json", 0, 0, 1*time.Millisecond)

	// Add fresh entry
	body2 := []byte(`{"input":"fresh"}`)
	cache.Set(endpoint, model, body2, []byte("new"), 200, "application/json", 0, 0, 1*time.Hour)

	time.Sleep(10 * time.Millisecond)

	// Before eviction, both are in the map (but expired one won't be returned by Get)
	statsBefore := cache.Stats()
	if statsBefore.Entries != 2 {
		t.Errorf("expected 2 entries before eviction, got %d", statsBefore.Entries)
	}

	// Evict expired entries
	cache.Evict()

	// After eviction, only fresh entry remains
	statsAfter := cache.Stats()
	if statsAfter.Entries != 1 {
		t.Errorf("expected 1 entry after eviction, got %d", statsAfter.Entries)
	}

	// Fresh entry should still be accessible
	if _, ok := cache.Get(endpoint, model, body2); !ok {
		t.Error("fresh entry should survive eviction")
	}
}

func TestCacheKeyStability(t *testing.T) {
	endpoint := "/v1/embeddings"
	model := "text-embedding-3-small"
	body := []byte(`{"input":"stable"}`)

	// Same inputs should produce same key
	key1 := cacheKey(endpoint, model, body)
	key2 := cacheKey(endpoint, model, body)

	if key1 != key2 {
		t.Error("cache key should be deterministic")
	}

	// Different endpoint should produce different key
	key3 := cacheKey("/v1/chat/completions", model, body)
	if key1 == key3 {
		t.Error("different endpoints should produce different keys")
	}

	// Different body should produce different key
	key4 := cacheKey(endpoint, model, []byte(`{"input":"different"}`))
	if key1 == key4 {
		t.Error("different bodies should produce different keys")
	}
}
