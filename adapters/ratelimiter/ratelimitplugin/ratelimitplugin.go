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
	RateLimit *ratelimit.RateLimiter
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
		RateLimit: &ratelimit.RateLimiter{
			RateLimitPerSecond:     sanitizeRateLimitPerSecond(rateLimitPerSecond),
			InitializeWithoutSlack: initializeWithoutSlack,
		},
		initOnce: new(sync.Once),
	}

	// initialize via initOnce so the first limiter is only initialized once
	p.lastInitTarget = p.RateLimit
	p.initOnce.Do(func() {
		p.RateLimit.Init()
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

	p.RateLimit = rl
	p.initOnce = new(sync.Once)
	p.lastInitTarget = rl

	p.mu.Unlock()
}

// RateLimiter accessor returns the initialized limiter.
func (p *RateLimitPlugin) RateLimiter() *ratelimit.RateLimiter {
	return p.ensureRateLimiter()
}

// ensureRateLimiter guarantees RateLimit is non-nil and initialized
func (p *RateLimitPlugin) ensureRateLimiter() *ratelimit.RateLimiter {
	p.mu.Lock()

	if p.initOnce == nil {
		p.initOnce = new(sync.Once)
	}

	if p.RateLimit == nil { // lazy create if missing
		p.RateLimit = &ratelimit.RateLimiter{
			RateLimitPerSecond:     0,     // unlimited default
			InitializeWithoutSlack: false, // default slack
		}
	}
	rl := p.RateLimit

	// if the RateLimit pointer was swapped externally, reset initOnce so we can init the new instance.
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
	current := p.RateLimit
	p.mu.Unlock()

	if current == rl {
		return rl
	}
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
	rl := p.ensureRateLimiter() // lazy, threadsafe init
	return rl.Take()            // always apply rate limiting
}
