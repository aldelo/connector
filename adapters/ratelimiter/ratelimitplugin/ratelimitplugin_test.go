package ratelimitplugin

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import (
	"sync"
	"testing"
	"time"

	"github.com/aldelo/common/wrapper/ratelimit"
)

// ---------------------------------------------------------------------------
// sanitizeRateLimitPerSecond (unexported helper — white-box, same package)
// ---------------------------------------------------------------------------

func TestSanitizeRateLimitPerSecond_NegativeClampedToZero(t *testing.T) {
	if got := sanitizeRateLimitPerSecond(-1); got != 0 {
		t.Errorf("sanitizeRateLimitPerSecond(-1) = %d, want 0", got)
	}
	if got := sanitizeRateLimitPerSecond(-999); got != 0 {
		t.Errorf("sanitizeRateLimitPerSecond(-999) = %d, want 0", got)
	}
}

func TestSanitizeRateLimitPerSecond_ZeroPassedThrough(t *testing.T) {
	if got := sanitizeRateLimitPerSecond(0); got != 0 {
		t.Errorf("sanitizeRateLimitPerSecond(0) = %d, want 0", got)
	}
}

func TestSanitizeRateLimitPerSecond_PositivePassedThrough(t *testing.T) {
	if got := sanitizeRateLimitPerSecond(1); got != 1 {
		t.Errorf("sanitizeRateLimitPerSecond(1) = %d, want 1", got)
	}
	if got := sanitizeRateLimitPerSecond(500); got != 500 {
		t.Errorf("sanitizeRateLimitPerSecond(500) = %d, want 500", got)
	}
}

// ---------------------------------------------------------------------------
// NewRateLimitPlugin
// ---------------------------------------------------------------------------

func TestNewRateLimitPlugin_ReturnsNonNil(t *testing.T) {
	p := NewRateLimitPlugin(0, false)
	if p == nil {
		t.Fatal("NewRateLimitPlugin(0, false) returned nil")
	}
}

func TestNewRateLimitPlugin_ZeroRate_Unlimited(t *testing.T) {
	p := NewRateLimitPlugin(0, false)
	if p.rateLimit == nil {
		t.Fatal("rateLimit is nil after construction")
	}
	if p.rateLimit.RateLimitPerSecond != 0 {
		t.Errorf("RateLimitPerSecond = %d, want 0", p.rateLimit.RateLimitPerSecond)
	}
}

func TestNewRateLimitPlugin_PositiveRate(t *testing.T) {
	p := NewRateLimitPlugin(100, true)
	if p.rateLimit.RateLimitPerSecond != 100 {
		t.Errorf("RateLimitPerSecond = %d, want 100", p.rateLimit.RateLimitPerSecond)
	}
	if !p.rateLimit.InitializeWithoutSlack {
		t.Error("InitializeWithoutSlack should be true")
	}
}

func TestNewRateLimitPlugin_NegativeRate_ClampedToZero(t *testing.T) {
	p := NewRateLimitPlugin(-5, false)
	if p.rateLimit.RateLimitPerSecond != 0 {
		t.Errorf("RateLimitPerSecond = %d, want 0 (clamped from -5)", p.rateLimit.RateLimitPerSecond)
	}
}

func TestNewRateLimitPlugin_InitOnceNotNil(t *testing.T) {
	p := NewRateLimitPlugin(10, false)
	if p.initOnce == nil {
		t.Fatal("initOnce should not be nil after construction")
	}
}

// ---------------------------------------------------------------------------
// SetRateLimiter
// ---------------------------------------------------------------------------

func TestSetRateLimiter_NilInput_CreatesDefault(t *testing.T) {
	p := NewRateLimitPlugin(50, true)
	p.SetRateLimiter(nil)

	if p.rateLimit == nil {
		t.Fatal("rateLimit should not be nil after SetRateLimiter(nil)")
	}
	if p.rateLimit.RateLimitPerSecond != 0 {
		t.Errorf("RateLimitPerSecond = %d, want 0 (default)", p.rateLimit.RateLimitPerSecond)
	}
	if p.rateLimit.InitializeWithoutSlack {
		t.Error("InitializeWithoutSlack should be false (default)")
	}
}

func TestSetRateLimiter_ValidInput_Stored(t *testing.T) {
	p := NewRateLimitPlugin(0, false)
	rl := &ratelimit.RateLimiter{
		RateLimitPerSecond:     200,
		InitializeWithoutSlack: true,
	}
	p.SetRateLimiter(rl)

	if p.rateLimit != rl {
		t.Error("rateLimit pointer should be the same as the one passed in")
	}
	if p.rateLimit.RateLimitPerSecond != 200 {
		t.Errorf("RateLimitPerSecond = %d, want 200", p.rateLimit.RateLimitPerSecond)
	}
}

func TestSetRateLimiter_NegativeRate_Sanitized(t *testing.T) {
	p := NewRateLimitPlugin(10, false)
	rl := &ratelimit.RateLimiter{
		RateLimitPerSecond: -10,
	}
	p.SetRateLimiter(rl)

	if p.rateLimit.RateLimitPerSecond != 0 {
		t.Errorf("RateLimitPerSecond = %d, want 0 (sanitized from -10)", p.rateLimit.RateLimitPerSecond)
	}
}

func TestSetRateLimiter_ResetsInitOnce(t *testing.T) {
	p := NewRateLimitPlugin(10, false)
	origOnce := p.initOnce

	rl := &ratelimit.RateLimiter{RateLimitPerSecond: 50}
	p.SetRateLimiter(rl)

	if p.initOnce == origOnce {
		t.Error("initOnce should be a new sync.Once after SetRateLimiter")
	}
}

func TestSetRateLimiter_UpdatesLastInitTarget(t *testing.T) {
	p := NewRateLimitPlugin(10, false)
	rl := &ratelimit.RateLimiter{RateLimitPerSecond: 75}
	p.SetRateLimiter(rl)

	if p.lastInitTarget != rl {
		t.Error("lastInitTarget should point to the new rate limiter")
	}
}

// ---------------------------------------------------------------------------
// RateLimiter() accessor
// ---------------------------------------------------------------------------

func TestRateLimiter_ReturnsNonNil(t *testing.T) {
	p := NewRateLimitPlugin(0, false)
	rl := p.RateLimiter()
	if rl == nil {
		t.Fatal("RateLimiter() returned nil")
	}
}

func TestRateLimiter_LazyInit_WhenRateLimitNil(t *testing.T) {
	// Construct a plugin with nil rateLimit to exercise lazy creation path.
	p := &RateLimitPlugin{
		initOnce: new(sync.Once),
	}
	rl := p.RateLimiter()
	if rl == nil {
		t.Fatal("RateLimiter() should lazy-init and return non-nil")
	}
	if rl.RateLimitPerSecond != 0 {
		t.Errorf("RateLimitPerSecond = %d, want 0 (lazy default)", rl.RateLimitPerSecond)
	}
}

func TestRateLimiter_ReturnsSameInstance(t *testing.T) {
	p := NewRateLimitPlugin(10, false)
	a := p.RateLimiter()
	b := p.RateLimiter()
	if a != b {
		t.Error("consecutive RateLimiter() calls should return the same pointer")
	}
}

func TestRateLimiter_AfterSet_ReturnsNewInstance(t *testing.T) {
	p := NewRateLimitPlugin(10, false)
	oldRL := p.RateLimiter()

	newRL := &ratelimit.RateLimiter{RateLimitPerSecond: 50}
	p.SetRateLimiter(newRL)

	got := p.RateLimiter()
	if got == oldRL {
		t.Error("RateLimiter() should return the newly set limiter, not the old one")
	}
	if got != newRL {
		t.Error("RateLimiter() should return the exact pointer passed to SetRateLimiter")
	}
}

// ---------------------------------------------------------------------------
// Take()
// ---------------------------------------------------------------------------

func TestTake_NilReceiver_ReturnsNow(t *testing.T) {
	var p *RateLimitPlugin
	before := time.Now()
	got := p.Take()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("nil receiver Take() returned %v, expected between %v and %v", got, before, after)
	}
}

func TestTake_NonNil_ReturnsValidTime(t *testing.T) {
	p := NewRateLimitPlugin(0, false) // unlimited
	before := time.Now()
	got := p.Take()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("Take() returned %v, expected between %v and %v", got, before, after)
	}
}

func TestTake_UnlimitedRate_Fast(t *testing.T) {
	p := NewRateLimitPlugin(0, false) // unlimited — should not sleep
	start := time.Now()
	for i := 0; i < 100; i++ {
		p.Take()
	}
	elapsed := time.Since(start)

	// Unlimited rate: 100 calls should complete well under 100ms.
	if elapsed > 100*time.Millisecond {
		t.Errorf("100 unlimited Take() calls took %v, expected < 100ms", elapsed)
	}
}

func TestTake_WithRateLimit_ProgressesTime(t *testing.T) {
	// 1000/sec with no slack = each call ~1ms apart.
	p := NewRateLimitPlugin(1000, true)
	t1 := p.Take()
	t2 := p.Take()
	t3 := p.Take()

	if t2.Before(t1) {
		t.Error("t2 should not be before t1")
	}
	if t3.Before(t2) {
		t.Error("t3 should not be before t2")
	}
}

func TestTake_LazyInitFromZeroValue(t *testing.T) {
	// A plugin with nil rateLimit should lazy-init and not panic on Take().
	p := &RateLimitPlugin{
		initOnce: new(sync.Once),
	}
	before := time.Now()
	got := p.Take()
	after := time.Now()

	if got.Before(before) || got.After(after) {
		t.Errorf("Take() after lazy init returned %v, expected between %v and %v", got, before, after)
	}
}

// ---------------------------------------------------------------------------
// Concurrency safety
// ---------------------------------------------------------------------------

func TestTake_ConcurrentAccess_NoPanic(t *testing.T) {
	p := NewRateLimitPlugin(0, false) // unlimited for speed
	const goroutines = 50
	const callsPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				_ = p.Take()
			}
		}()
	}

	wg.Wait()
	// If we reach here without panic or deadlock, concurrency is safe.
}

func TestSetRateLimiter_ConcurrentWithTake(t *testing.T) {
	p := NewRateLimitPlugin(0, false)
	const goroutines = 20

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Half the goroutines call Take().
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = p.Take()
			}
		}()
	}

	// Half the goroutines call SetRateLimiter().
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				p.SetRateLimiter(&ratelimit.RateLimiter{
					RateLimitPerSecond: idx * 10,
				})
			}
		}(i)
	}

	wg.Wait()
	// Success: no race, panic, or deadlock.
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	p := NewRateLimitPlugin(0, false)
	const goroutines = 30

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			rl := p.RateLimiter()
			if rl == nil {
				t.Error("RateLimiter() returned nil under concurrent access")
			}
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestNewRateLimitPlugin_LargePositiveRate(t *testing.T) {
	p := NewRateLimitPlugin(1_000_000, false)
	if p.rateLimit.RateLimitPerSecond != 1_000_000 {
		t.Errorf("RateLimitPerSecond = %d, want 1000000", p.rateLimit.RateLimitPerSecond)
	}
}

func TestNewRateLimitPlugin_WithSlackFalse(t *testing.T) {
	p := NewRateLimitPlugin(10, false)
	if p.rateLimit.InitializeWithoutSlack {
		t.Error("InitializeWithoutSlack should be false")
	}
}

func TestNewRateLimitPlugin_WithSlackTrue(t *testing.T) {
	p := NewRateLimitPlugin(10, true)
	if !p.rateLimit.InitializeWithoutSlack {
		t.Error("InitializeWithoutSlack should be true")
	}
}

func TestSetRateLimiter_MultipleTimes(t *testing.T) {
	p := NewRateLimitPlugin(0, false)

	for i := 1; i <= 5; i++ {
		rl := &ratelimit.RateLimiter{RateLimitPerSecond: i * 100}
		p.SetRateLimiter(rl)

		got := p.RateLimiter()
		if got != rl {
			t.Errorf("iteration %d: RateLimiter() did not return the set limiter", i)
		}
		if got.RateLimitPerSecond != i*100 {
			t.Errorf("iteration %d: RateLimitPerSecond = %d, want %d", i, got.RateLimitPerSecond, i*100)
		}
	}
}

func TestEnsureRateLimiter_NilInitOnce_Recovered(t *testing.T) {
	// Construct a plugin with nil initOnce to verify ensureRateLimiter handles it.
	p := &RateLimitPlugin{}
	rl := p.ensureRateLimiter()
	if rl == nil {
		t.Fatal("ensureRateLimiter() should recover from nil initOnce and return non-nil")
	}
}
