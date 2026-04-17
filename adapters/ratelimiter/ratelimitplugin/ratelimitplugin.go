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
	"time"

	"github.com/aldelo/common/wrapper/ratelimit"
)

type RateLimitPlugin struct {
	rateLimit *ratelimit.RateLimiter // unexported: no external consumers access this directly
	mu        sync.Mutex

	// track per-pointer initialization so externally swapped limiters get Init() exactly once.
	initOnce       *sync.Once
	lastInitTarget *ratelimit.RateLimiter
}

// clamp negative input to 0 (unlimited).
func sanitizeRateLimitPerSecond(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// NewRateLimitPlugin creates a hystrixgo plugin struct object
// this plugin implements the CircuitBreakerIFace interface
//
// rateLimitPerSecond = number of actions per second allowed, 0 for no rate limit control
// initializeWithoutSlack = true: no slack (disallow initial spike consideration)
func NewRateLimitPlugin(rateLimitPerSecond int, initializeWithoutSlack bool) *RateLimitPlugin {
	p := &RateLimitPlugin{
		rateLimit: &ratelimit.RateLimiter{
			RateLimitPerSecond:     sanitizeRateLimitPerSecond(rateLimitPerSecond),
			InitializeWithoutSlack: initializeWithoutSlack,
		},
		initOnce: new(sync.Once),
	}

	// initialize via initOnce so the first limiter is only initialized once
	p.lastInitTarget = p.rateLimit
	p.initOnce.Do(func() {
		p.rateLimit.Init()
	})
	return p
}

// SetRateLimiter safely swaps the limiter, sanitizes input, and resets init state.
func (p *RateLimitPlugin) SetRateLimiter(rl *ratelimit.RateLimiter) {
	if rl == nil {
		rl = &ratelimit.RateLimiter{
			RateLimitPerSecond:     0,
			InitializeWithoutSlack: false,
		}
	}
	rl.RateLimitPerSecond = sanitizeRateLimitPerSecond(rl.RateLimitPerSecond)

	p.mu.Lock()

	// P3-CONN-2: `initOnce = new(sync.Once)` intentionally resets the once-guard when
	// the rateLimit pointer is replaced. A fresh Once is required so the new limiter's
	// Init() runs exactly once even though a previous one already fired on the old
	// instance. This assignment is safe only because p.mu is held — any concurrent
	// reader in ensureRateLimiter() serializes on the same mutex and will observe the
	// new *sync.Once consistently. Do NOT remove this reset: without it, a swapped-in
	// limiter would never call Init() and would fail closed at first use.
	p.rateLimit = rl
	p.initOnce = new(sync.Once)
	p.lastInitTarget = rl

	p.mu.Unlock()
}

// RateLimiter accessor returns the initialized limiter.
func (p *RateLimitPlugin) RateLimiter() *ratelimit.RateLimiter {
	return p.ensureRateLimiter()
}

// ensureRateLimiter guarantees rateLimit is non-nil and initialized
func (p *RateLimitPlugin) ensureRateLimiter() *ratelimit.RateLimiter {
	const maxRetries = 10
	for i := 0; i < maxRetries; i++ { // retry until we return the current, initialized limiter
		p.mu.Lock()

		if p.initOnce == nil {
			p.initOnce = new(sync.Once)
		}

		if p.rateLimit == nil { // lazy create if missing
			p.rateLimit = &ratelimit.RateLimiter{
				RateLimitPerSecond:     0,     // unlimited default
				InitializeWithoutSlack: false, // default slack
			}
		}
		rl := p.rateLimit

		// if the rateLimit pointer was swapped externally, reset initOnce so we can init the new instance.
		if p.lastInitTarget != rl {
			p.initOnce = new(sync.Once)
			p.lastInitTarget = rl
		}
		once := p.initOnce
		p.mu.Unlock()

		// sanitize before initializing to prevent negative rates from breaking Init()
		once.Do(func() {
			rl.RateLimitPerSecond = sanitizeRateLimitPerSecond(rl.RateLimitPerSecond)
			rl.Init()
		})

		// verify the limiter wasn't swapped during init; if it was, retry to init the new one
		p.mu.Lock()
		current := p.rateLimit
		p.mu.Unlock()

		if current == rl {
			return rl
		}
		// loop to initialize the newly swapped-in limiter instead of returning nil
	}
	// retries exhausted — return whatever is currently set (may be nil)
	p.mu.Lock()
	rl := p.rateLimit
	p.mu.Unlock()
	return rl
}

// Take is called by each method needing rate limit applied
//
// based on rate limit per second setting,
// given amount of time is slept before process continues
//
// for example, 1 second rate limit 100 = 100 milliseconds per call,
// this causes each call to Take sleep for 100 milliseconds before continuing
//
// returns time when Take took place
func (p *RateLimitPlugin) Take() time.Time {
	if p == nil {
		return time.Now() // No rate limiting if plugin is nil
	}

	rl := p.ensureRateLimiter() // lazy, threadsafe init
	if rl == nil {
		return time.Now() // Defensive fallback
	}

	return rl.Take() // always apply rate limiting
}
