package ratelimiter

import (
	"testing"
	"time"
)

// mockRateLimiter is a minimal stub that satisfies RateLimiterIFace.
// It proves the interface is coherently defined and implementable.
type mockRateLimiter struct{}

func (m *mockRateLimiter) Take() time.Time {
	return time.Now()
}

// TestRateLimiterIFace_Implementable verifies that RateLimiterIFace is
// a coherent interface definition that a concrete type can satisfy.
// The assignment would fail at compile time if the mock's method
// signature drifted from the interface.
func TestRateLimiterIFace_Implementable(t *testing.T) {
	// Compile-time interface compliance check.
	var iface RateLimiterIFace = &mockRateLimiter{}

	// Verify Take returns a reasonable time (not zero-value).
	result := iface.Take()
	if result.IsZero() {
		t.Fatal("expected Take() to return a non-zero time")
	}
}
