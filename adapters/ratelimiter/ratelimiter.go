package ratelimiter

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
	"time"
)

type RateLimiterIFace interface {
	// Take is called by each method needing rate limit applied
	//
	// based on rate limit per second setting,
	// given amount of time is slept before process continues
	//
	// for example, 1 second rate limit 100 = 100 milliseconds per call,
	// this causes each call to Take sleep for 100 milliseconds before continuing
	//
	// returns time when Take took place
	Take() time.Time
}
