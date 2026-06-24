package client

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
	"testing"
)

// CB-TRACE gating tests.
//
// A prod incident traced ~380K circuit-breaker INFO lines in 20 minutes
// from the per-call CB handlers (hot path). cbTrace gates those trace
// lines behind Client.DebugCircuitBreaker (default false = silent) while
// ERROR logging stays unconditional. These tests pin the single gating
// seam: cbTrace must route to logLine ONLY when the flag is enabled.
//
// logLine ultimately writes through ZapLog (or the std log fallback),
// neither of which is deterministically capturable in a unit context
// (ZapLog is a no-op when DisableLogger=true, otherwise writes to its own
// console sink). To assert the gating boolean deterministically we swap
// the cbTrace sink for a counter; the production default routes to the
// real logLine, so behavior is unchanged outside the test.

func TestCB_TraceGatedOffByDefault(t *testing.T) {
	c := &Client{} // zero value: DebugCircuitBreaker == false

	calls := 0
	orig := cbTraceSink
	cbTraceSink = func(_ *Client, _ string) { calls++ }
	defer func() { cbTraceSink = orig }()

	c.cbTrace("In - Unary Circuit Breaker Handler: /svc/Method")

	if calls != 0 {
		t.Fatalf("cbTrace must NOT emit when DebugCircuitBreaker is false; got %d sink calls", calls)
	}
}

func TestCB_TraceEmitsWhenDebugEnabled(t *testing.T) {
	c := &Client{DebugCircuitBreaker: true}

	calls := 0
	var lastMsg string
	orig := cbTraceSink
	cbTraceSink = func(_ *Client, msg string) {
		calls++
		lastMsg = msg
	}
	defer func() { cbTraceSink = orig }()

	c.cbTrace("Run Circuit Breaker Action for: /svc/Method...")

	if calls != 1 {
		t.Fatalf("cbTrace must emit exactly once when DebugCircuitBreaker is true; got %d sink calls", calls)
	}
	if lastMsg != "Run Circuit Breaker Action for: /svc/Method..." {
		t.Fatalf("cbTrace forwarded wrong message: %q", lastMsg)
	}
}

// TestCB_TraceNilClientIsSafe pins that the gate tolerates a nil receiver
// (the CB handlers guard nil, but cbTrace must not be the thing that panics).
func TestCB_TraceNilClientIsSafe(t *testing.T) {
	var c *Client // nil
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("cbTrace on nil client must not panic; got %v", r)
		}
	}()
	c.cbTrace("should be a no-op on nil client")
}
