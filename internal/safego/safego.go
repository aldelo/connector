package safego

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
	"log"
	"runtime/debug"
)

// Go launches fn in a new goroutine with panic recovery and stack trace
// capture. If fn panics, the panic value and a full goroutine stack are
// logged via the standard logger, and the goroutine exits — the process
// survives long enough for the normal shutdown path to run or for
// another pod to take over.
//
// If fn is nil, Go is a no-op: no goroutine is spawned.
//
// Use Go for every service-internal goroutine spawn. Intentional
// "background forever" goroutines (tickers, signal demuxes) exit on
// panic and log — per-iteration recovery for tickers is a separate
// refinement that can be layered on top when needed. The priority is
// "process stays alive" not "ticker stays alive".
//
// name identifies the goroutine in the log line for post-mortem triage.
func Go(name string, fn func()) {
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("!!! safeGo panic recovered in %q: %v\n%s !!!", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// GoWait is like Go but returns a channel that is closed when the goroutine
// exits (whether normally or via panic recovery). Use in tests for
// deterministic synchronization instead of time.Sleep.
//
// If fn is nil, GoWait returns an already-closed channel (no goroutine spawned).
func GoWait(name string, fn func()) <-chan struct{} {
	done := make(chan struct{})
	if fn == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				log.Printf("!!! safeGo panic recovered in %q: %v\n%s !!!", name, r, debug.Stack())
			}
		}()
		fn()
	}()
	return done
}
