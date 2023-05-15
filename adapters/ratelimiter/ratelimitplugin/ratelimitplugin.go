package ratelimitplugin

/*
 * Copyright 2020-2023 Aldelo, LP
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
	"github.com/aldelo/common/wrapper/ratelimit"
	"time"
)

type RateLimitPlugin struct {
	RateLimit *ratelimit.RateLimiter
}

// NewRateLimitPlugin creates a hystrixgo plugin struct object
// this plugin implements the CircuitBreakerIFace interface
//
// rateLimitPerSecond = number of actions per second allowed, 0 for no rate limit control
// initializeWithoutSlack = true: no slack (disallow initial spike consideration)
func NewRateLimitPlugin(rateLimitPerSecond int,
	initializeWithoutSlack bool) *RateLimitPlugin {
	p := &RateLimitPlugin{
		RateLimit: &ratelimit.RateLimiter{
			RateLimitPerSecond:     rateLimitPerSecond,
			InitializeWithoutSlack: initializeWithoutSlack,
		},
	}

	p.RateLimit.Init()
	return p
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
	if p.RateLimit != nil {
		return p.RateLimit.Take()
	} else {
		return time.Time{}
	}
}
