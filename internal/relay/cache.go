package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// ResponseCache caches upstream responses to avoid redundant API calls for
// identical requests. Particularly valuable for embeddings, moderations, and
// other deterministic endpoints where the same input always produces the same
// output.
//
// Cache keys are SHA-256(endpoint + model + request body), so a byte-for-byte
// identical request hits the cache regardless of which client sent it.
type ResponseCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
	// inflight tracks in-progress requests to deduplicate concurrent calls:
	// multiple clients asking for the same thing at once share one upstream
	// call instead of hammering the API.
	inflight map[string]*inflightEntry
}

type cacheEntry struct {
	response     []byte
	status       int
	contentType  string
	promptTokens int
	completionTokens int
	expires      time.Time
}

// inflightEntry is a shared future: the first request goes upstream, and
// concurrent requests for the same key wait on the done channel. When done
// closes, result/err hold what the upstream returned.
type inflightEntry struct {
	done   chan struct{}
	result *cacheEntry
	err    error
}

// CacheConfig holds cache TTLs per endpoint category.
type CacheConfig struct {
	// Embeddings are deterministic and rarely change; cache aggressively.
	EmbeddingsTTL time.Duration
	// Moderations are also deterministic.
	ModerationsTTL time.Duration
	// Chat completions are NOT cached by default (temperature > 0 means
	// non-deterministic), but requests with temperature=0 could opt in.
	ChatTTL time.Duration
	// Images/audio/responses: not cached (generation endpoints).
	// Models list: could cache but changes infrequently; not implemented here.
}

// DefaultCacheConfig provides sensible cache TTLs.
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		EmbeddingsTTL:  24 * time.Hour, // embeddings for the same text never change
		ModerationsTTL: 1 * time.Hour,  // moderation policies could update, but slowly
		ChatTTL:        5 * time.Minute, // only for temperature=0 deterministic requests
	}
}

func NewResponseCache() *ResponseCache {
	return &ResponseCache{
		entries:  make(map[string]*cacheEntry),
		inflight: make(map[string]*inflightEntry),
	}
}

// cacheKey computes a stable key from the request. Endpoint and model are
// explicit to prevent cross-endpoint collisions (unlikely but possible with
// hash truncation). The body is the full request JSON.
func cacheKey(endpoint, model string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(endpoint))
	h.Write([]byte("\x00"))
	h.Write([]byte(model))
	h.Write([]byte("\x00"))
	h.Write(body)
	sum := h.Sum(nil)
	// 16 bytes = 128 bits is far beyond what a proxy needs to avoid collisions.
	return hex.EncodeToString(sum[:16])
}

// Get checks the cache for a response. ok=false means cache miss; the caller
// should go upstream, then call Set. If an identical request is already
// in-flight (another goroutine is fetching it), this blocks until that
// completes and returns the shared result (request coalescing).
func (c *ResponseCache) Get(endpoint, model string, body []byte) (entry *cacheEntry, ok bool) {
	key := cacheKey(endpoint, model, body)
	now := time.Now()

	c.mu.RLock()
	// Check inflight first: if another goroutine is fetching this, wait for it.
	if inflight, waiting := c.inflight[key]; waiting {
		c.mu.RUnlock()
		<-inflight.done // block until the in-progress request finishes
		if inflight.err != nil {
			return nil, false // upstream failed, caller should retry
		}
		return inflight.result, true // shared result from the racing request
	}

	// Check cache.
	entry, cached := c.entries[key]
	c.mu.RUnlock()

	if cached && now.Before(entry.expires) {
		return entry, true
	}
	return nil, false
}

// StartInflight marks a request as in-progress so concurrent calls wait
// instead of duplicating the upstream call. Returns a done channel the caller
// must close after Set/SetError, and a boolean indicating whether this caller
// won the race (true = go upstream; false = another goroutine is already on it,
// you should call Get again to wait).
func (c *ResponseCache) StartInflight(endpoint, model string, body []byte) (done chan struct{}, won bool) {
	key := cacheKey(endpoint, model, body)

	c.mu.Lock()
	defer c.mu.Unlock()

	// If already inflight, this caller lost the race.
	if _, exists := c.inflight[key]; exists {
		return nil, false
	}

	// This caller won: mark it inflight.
	done = make(chan struct{})
	c.inflight[key] = &inflightEntry{done: done}
	return done, true
}

// Set stores a successful upstream response in the cache. ttl controls how
// long the entry lives. Must be called after StartInflight (won=true) and
// before closing the done channel.
func (c *ResponseCache) Set(endpoint, model string, body []byte, response []byte, status int, contentType string, promptTokens, completionTokens int, ttl time.Duration) {
	if ttl <= 0 {
		return // caching disabled for this endpoint
	}

	key := cacheKey(endpoint, model, body)
	entry := &cacheEntry{
		response:         response,
		status:           status,
		contentType:      contentType,
		promptTokens:     promptTokens,
		completionTokens: completionTokens,
		expires:          time.Now().Add(ttl),
	}

	c.mu.Lock()
	c.entries[key] = entry
	// Also store in the inflight entry so waiters get it.
	if inflight, ok := c.inflight[key]; ok {
		inflight.result = entry
	}
	c.mu.Unlock()
}

// SetError records that the upstream attempt failed. Waiters on the inflight
// entry will see the error and retry themselves.
func (c *ResponseCache) SetError(endpoint, model string, body []byte, err error) {
	key := cacheKey(endpoint, model, body)

	c.mu.Lock()
	if inflight, ok := c.inflight[key]; ok {
		inflight.err = err
	}
	c.mu.Unlock()
}

// FinishInflight closes the done channel and removes the inflight marker.
// Must be called (defer it) after StartInflight(won=true), even if Set was
// never called (e.g. the upstream errored).
func (c *ResponseCache) FinishInflight(endpoint, model string, body []byte) {
	key := cacheKey(endpoint, model, body)

	c.mu.Lock()
	inflight, ok := c.inflight[key]
	if ok {
		close(inflight.done)
		delete(c.inflight, key)
	}
	c.mu.Unlock()
}

// Evict removes expired entries. Call periodically (e.g. every 5 minutes).
func (c *ResponseCache) Evict() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, key)
		}
	}
}

// Stats reports cache size and hit/miss counters for observability.
type CacheStats struct {
	Entries  int `json:"entries"`
	Inflight int `json:"inflight"`
}

func (c *ResponseCache) Stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CacheStats{
		Entries:  len(c.entries),
		Inflight: len(c.inflight),
	}
}
