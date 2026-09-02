package relay

import (
	"sync"
	"time"
)

// CircuitBreaker prevents a single request from exhausting all keys in a
// channel when upstream rate limits kick in. After N consecutive failures
// (429/5xx) within a time window, the breaker trips and the channel is
// temporarily skipped, giving keys time to recover.
type CircuitBreaker struct {
	mu sync.Mutex
	// state holds channel name -> breaker state
	state map[string]*breakerEntry
}

type breakerEntry struct {
	failures   int
	lastFail   time.Time
	trippedAt  time.Time
	isTripped  bool
}

const (
	// Trip the breaker after this many failures within the window
	breakerThreshold = 3
	// Reset failure count if the channel was quiet for this long
	breakerWindow = 30 * time.Second
	// Keep the breaker tripped for this duration
	breakerCooldown = 15 * time.Second
)

func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		state: make(map[string]*breakerEntry),
	}
}

// RecordFailure notes one failed attempt (429 or 5xx) for the channel.
// Returns true if the breaker should trip (threshold reached).
func (cb *CircuitBreaker) RecordFailure(channelName string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	entry, ok := cb.state[channelName]
	if !ok || now.Sub(entry.lastFail) > breakerWindow {
		entry = &breakerEntry{}
		cb.state[channelName] = entry
	}

	entry.failures++
	entry.lastFail = now

	if entry.failures >= breakerThreshold && !entry.isTripped {
		entry.isTripped = true
		entry.trippedAt = now
		return true
	}
	return false
}

// RecordSuccess resets the failure count for the channel after a 2xx.
func (cb *CircuitBreaker) RecordSuccess(channelName string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	delete(cb.state, channelName)
}

// IsTripped reports whether the breaker is currently open for this channel.
// A tripped breaker auto-resets after breakerCooldown.
func (cb *CircuitBreaker) IsTripped(channelName string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	entry, ok := cb.state[channelName]
	if !ok || !entry.isTripped {
		return false
	}

	if time.Since(entry.trippedAt) > breakerCooldown {
		// Auto-reset: the cooldown expired, allow probing again
		delete(cb.state, channelName)
		return false
	}
	return true
}
