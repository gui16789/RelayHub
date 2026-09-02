package relay

import (
	"testing"
	"time"
)

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker()

	// Record failures below threshold: should not trip
	for i := 0; i < breakerThreshold-1; i++ {
		if tripped := cb.RecordFailure("test-channel"); tripped {
			t.Errorf("breaker tripped too early (attempt %d/%d)", i+1, breakerThreshold)
		}
	}
	if cb.IsTripped("test-channel") {
		t.Error("breaker should not be tripped yet")
	}

	// Threshold failure: should trip
	if !cb.RecordFailure("test-channel") {
		t.Error("breaker should trip on threshold failure")
	}
	if !cb.IsTripped("test-channel") {
		t.Error("breaker should be tripped now")
	}

	// Wait for cooldown to expire
	time.Sleep(breakerCooldown + 10*time.Millisecond)
	if cb.IsTripped("test-channel") {
		t.Error("breaker should auto-reset after cooldown")
	}
}

func TestCircuitBreakerSuccess(t *testing.T) {
	cb := NewCircuitBreaker()

	// Build up failures
	cb.RecordFailure("test-channel")
	cb.RecordFailure("test-channel")

	// Success resets the count
	cb.RecordSuccess("test-channel")

	// Next failure should not trip (count was reset)
	if cb.RecordFailure("test-channel") {
		t.Error("breaker should not trip immediately after success reset")
	}
}

func TestCircuitBreakerWindow(t *testing.T) {
	cb := NewCircuitBreaker()

	// Record failures within window
	cb.RecordFailure("test-channel")
	cb.RecordFailure("test-channel")

	// Wait beyond the window
	time.Sleep(breakerWindow + 10*time.Millisecond)

	// Next failure should start a fresh count (not trip yet)
	if cb.RecordFailure("test-channel") {
		t.Error("breaker should start fresh count after window expires")
	}
}

func TestCircuitBreakerIsolation(t *testing.T) {
	cb := NewCircuitBreaker()

	// Trip one channel
	for i := 0; i < breakerThreshold; i++ {
		cb.RecordFailure("channel-a")
	}

	// Other channel should be unaffected
	if cb.IsTripped("channel-b") {
		t.Error("breaker for channel-b should not be affected by channel-a")
	}
	if !cb.IsTripped("channel-a") {
		t.Error("breaker for channel-a should be tripped")
	}
}
