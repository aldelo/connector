package client

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
	"testing"
	"time"
)

// ReconcileServiceEndpoints reconciles the cached set for a service to the freshly discovered
// set, but WITHIN a single version scope and with per-endpoint grace/hysteresis:
//   - endpoints of a DIFFERENT version are preserved untouched (kills cross-version cache thrash
//     on the process-global cache),
//   - an endpoint absent from the fresh result is dropped only after `graceCycles` consecutive
//     absences (absorbs a transient partial discovery — health flap, Cloud Map eventual
//     consistency, >MaxResults truncation — so the resolver is never shrunk on a single blip),
//   - a reappearing endpoint resets its grace counter.

func hostSet(eps []*serviceEndpoint) map[string]bool {
	m := map[string]bool{}
	for _, e := range eps {
		if e != nil {
			m[e.Host] = true
		}
	}
	return m
}

func TestReconcile_PrunesRemovedAfterGrace(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
		ep("10.0.0.2", 8080, "v1", live()), // will be removed from the fleet
	})

	// grace=3: .2 absent for 3 consecutive forced discoveries before it is dropped.
	for cycle := 1; cycle <= 2; cycle++ {
		c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())}, 3)
		got := hostSet(c.GetServiceEndpoints("svc-a"))
		if !got["10.0.0.2"] {
			t.Fatalf("cycle %d: removed endpoint dropped before grace threshold (should be kept)", cycle)
		}
	}
	// 3rd consecutive absence reaches grace -> dropped
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())}, 3)
	got := hostSet(c.GetServiceEndpoints("svc-a"))
	if got["10.0.0.2"] {
		t.Fatalf("endpoint should be pruned after 3 consecutive absences, still present: %v", got)
	}
	if !got["10.0.0.1"] || len(got) != 1 {
		t.Fatalf("expected only 10.0.0.1 to survive, got %v", got)
	}
}

func TestReconcile_TransientAbsenceKeepsEndpointAndResets(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
		ep("10.0.0.2", 8080, "v1", live()),
	})
	// .2 flaps out for one cycle (missing=1), then comes back -> counter must reset.
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())}, 3)
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
		ep("10.0.0.2", 8080, "v1", live()),
	}, 3)
	// .2 flaps out again for two cycles; because the counter reset, it must still survive both.
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())}, 3)
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())}, 3)
	got := hostSet(c.GetServiceEndpoints("svc-a"))
	if !got["10.0.0.2"] {
		t.Fatalf("endpoint that reappeared should have its grace counter reset and survive later flaps, got %v", got)
	}
}

func TestReconcile_VersionScopePreservesOtherVersions(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
		ep("10.0.0.9", 8080, "v2", live()), // a different-version client's entry on the shared key
	})
	// A v1-scoped reconcile (grace=1 => immediate within scope) must NOT touch the v2 entry.
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{ep("10.0.0.2", 8080, "v1", live())}, 1)
	got := hostSet(c.GetServiceEndpoints("svc-a"))
	if !got["10.0.0.9"] {
		t.Fatalf("v2 endpoint must be preserved by a v1-scoped reconcile (no cross-version wipe), got %v", got)
	}
	if !got["10.0.0.2"] || got["10.0.0.1"] {
		t.Fatalf("within v1 scope, expected .1 replaced by .2; got %v", got)
	}
}

func TestReconcile_GraceOneReplacesImmediatelyWithinScope(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())})
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{ep("10.0.0.2", 8080, "v1", live())}, 1)
	got := hostSet(c.GetServiceEndpoints("svc-a"))
	if len(got) != 1 || !got["10.0.0.2"] || got["10.0.0.1"] {
		t.Fatalf("grace=1 should prune absent within scope immediately, got %v", got)
	}
}

func TestReconcile_AddsNewEndpoints(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())})
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
		ep("10.0.0.2", 8080, "v1", live()),
	}, 3)
	got := hostSet(c.GetServiceEndpoints("svc-a"))
	if len(got) != 2 || !got["10.0.0.1"] || !got["10.0.0.2"] {
		t.Fatalf("expected {.1,.2}, got %v", got)
	}
}

func TestReconcile_EmptyFreshIsNoop(t *testing.T) {
	// The discovery caller fails fast on an empty result before reconciling; the method must
	// therefore never shrink on empty — an empty fresh set leaves the cache untouched.
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())})
	c.ReconcileServiceEndpoints("svc-a", "v1", nil, 1)
	if got := c.GetServiceEndpoints("svc-a"); len(got) != 1 {
		t.Fatalf("empty fresh must be a no-op, got %d endpoints", len(got))
	}
}

func TestReconcile_SkipsNilInvalidAndExpiredInFresh(t *testing.T) {
	c := newCache()
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),    // keep
		nil,                                   // drop
		ep("", 8080, "v1", live()),            // drop empty host
		ep("10.0.0.9", 0, "v1", live()),       // drop zero port
		ep("10.0.0.8", 8080, "v1", expired()), // drop expired
	}, 1)
	got := hostSet(c.GetServiceEndpoints("svc-a"))
	if len(got) != 1 || !got["10.0.0.1"] {
		t.Fatalf("expected only the one valid live endpoint, got %v", got)
	}
}

func TestReconcile_NilReceiver(t *testing.T) {
	var c *Cache
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())}, 3)
}

func TestReconcile_EmptyServiceNameIsNoop(t *testing.T) {
	c := newCache()
	c.ReconcileServiceEndpoints("   ", "v1", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())}, 1)
	if got := c.GetServiceEndpoints("   "); len(got) != 0 {
		t.Fatalf("empty service name must be a no-op, got %d", len(got))
	}
}

// DEFAULT-CONFIG case: the client filters with an empty version scope (instance_version unset),
// but the servers register a real version (e.g. v1.0.0). An unfiltered ("") discovery is
// authoritative for the WHOLE service, so a removed instance must still prune after grace —
// regardless of the version its endpoints carry. (Regression guard for the empty-scope bug.)
func TestReconcile_EmptyScopePrunesVersionedInstances(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1.0.0", live()),
		ep("10.0.0.2", 8080, "v1.0.0", live()),
	})
	// unfiltered discovery (scope "") returns only .1; both instances carry their real version.
	for cycle := 1; cycle <= 3; cycle++ {
		c.ReconcileServiceEndpoints("svc-a", "", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1.0.0", live())}, 3)
	}
	got := hostSet(c.GetServiceEndpoints("svc-a"))
	if got["10.0.0.2"] {
		t.Fatalf("empty scope must prune a removed versioned instance after grace, still present: %v", got)
	}
	if len(got) != 1 || !got["10.0.0.1"] {
		t.Fatalf("expected only 10.0.0.1 to survive, got %v", got)
	}
}

// An in-grace (absent-but-kept) endpoint must have its CacheExpire refreshed to the fresh batch's
// expiry, so a short SdEndpointCacheExpires cannot TTL-prune it before grace elapses.
func TestReconcile_InGraceEndpointExpiryIsRefreshed(t *testing.T) {
	c := newCache()
	soon := time.Now().Add(40 * time.Millisecond) // A is about to expire
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
		ep("10.0.0.2", 8080, "v1", soon),
	})
	// .2 is absent this cycle (grace=3 keeps it); fresh batch carries a long expiry.
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())}, 3)
	var two *serviceEndpoint
	for _, e := range c.GetServiceEndpoints("svc-a") {
		if e.Host == "10.0.0.2" {
			two = e
		}
	}
	if two == nil {
		t.Fatalf("in-grace endpoint 10.0.0.2 must be retained")
	}
	if !two.CacheExpire.After(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("in-grace endpoint's CacheExpire must be refreshed to the fresh batch expiry, got %v", two.CacheExpire)
	}
}

// A rotating >MaxResults fleet: each cycle returns a different subset. Because absences are
// graced, endpoints that rotate out briefly are retained and the cached set trends to the union
// (fixing the churn a blunt replace would cause for >100-instance fleets).
func TestReconcile_RotatingSubsetAccumulatesUnderGrace(t *testing.T) {
	c := newCache()
	// cycle 1 returns {A,B}; cycle 2 returns {A,C} (B rotated out); cycle 3 {B,C} (A rotated out)
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()), ep("10.0.0.2", 8080, "v1", live()),
	}, 3)
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()), ep("10.0.0.3", 8080, "v1", live()),
	}, 3)
	c.ReconcileServiceEndpoints("svc-a", "v1", []*serviceEndpoint{
		ep("10.0.0.2", 8080, "v1", live()), ep("10.0.0.3", 8080, "v1", live()),
	}, 3)
	got := hostSet(c.GetServiceEndpoints("svc-a"))
	// none was absent 3 consecutive cycles, so all three survive (no churn)
	if len(got) != 3 || !got["10.0.0.1"] || !got["10.0.0.2"] || !got["10.0.0.3"] {
		t.Fatalf("rotating subset should accumulate to full set under grace, got %v", got)
	}
}
