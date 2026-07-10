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
)

// ReplaceServiceEndpoints reconciles the cached set to exactly the freshly discovered set.
// Unlike AddServiceEndpoints (merge/additive), it drops endpoints no longer present — this
// is what lets a forced refresh prune an instance that was deregistered from Cloud Map, so
// the client stops dialing a now-dead IP.

func hostSet(eps []*serviceEndpoint) map[string]bool {
	m := map[string]bool{}
	for _, e := range eps {
		if e != nil {
			m[e.Host] = true
		}
	}
	return m
}

func TestReplaceServiceEndpoints_PrunesRemoved(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
		ep("10.0.0.2", 8080, "v1", live()), // this one gets removed from the fleet
	})

	// fresh discovery returns only .1 (.2 was deregistered)
	c.ReplaceServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
	})

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 1 {
		t.Fatalf("expected 1 endpoint after replace, got %d", len(got))
	}
	if got[0].Host != "10.0.0.1" {
		t.Errorf("expected surviving host 10.0.0.1, got %q", got[0].Host)
	}
	if hostSet(got)["10.0.0.2"] {
		t.Error("removed host 10.0.0.2 must not survive a replace")
	}
}

func TestReplaceServiceEndpoints_AddsNewAndDropsOldTogether(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
		ep("10.0.0.2", 8080, "v1", live()),
	})

	// fleet shifted: .2 gone, .3 new; .1 stays
	c.ReplaceServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),
		ep("10.0.0.3", 8080, "v1", live()),
	})

	got := hostSet(c.GetServiceEndpoints("svc-a"))
	if len(got) != 2 || !got["10.0.0.1"] || !got["10.0.0.3"] || got["10.0.0.2"] {
		t.Fatalf("expected {10.0.0.1,10.0.0.3}, got %v", got)
	}
}

func TestReplaceServiceEndpoints_EmptyDeletesKey(t *testing.T) {
	c := newCache()
	c.AddServiceEndpoints("svc-a", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())})

	c.ReplaceServiceEndpoints("svc-a", nil)

	if got := c.GetServiceEndpoints("svc-a"); len(got) != 0 {
		t.Fatalf("expected key dropped on empty replace, got %d endpoints", len(got))
	}
}

func TestReplaceServiceEndpoints_SkipsNilInvalidAndExpired(t *testing.T) {
	c := newCache()
	c.ReplaceServiceEndpoints("svc-a", []*serviceEndpoint{
		ep("10.0.0.1", 8080, "v1", live()),    // keep
		nil,                                   // drop
		ep("", 8080, "v1", live()),            // drop: empty host
		ep("10.0.0.9", 0, "v1", live()),       // drop: zero port
		ep("10.0.0.8", 8080, "v1", expired()), // drop: already expired
	})

	got := c.GetServiceEndpoints("svc-a")
	if len(got) != 1 || got[0].Host != "10.0.0.1" {
		t.Fatalf("expected only the one valid live endpoint, got %v", hostSet(got))
	}
}

func TestReplaceServiceEndpoints_NilReceiver(t *testing.T) {
	var c *Cache
	// must not panic
	c.ReplaceServiceEndpoints("svc-a", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())})
}

func TestReplaceServiceEndpoints_EmptyServiceName(t *testing.T) {
	c := newCache()
	c.ReplaceServiceEndpoints("   ", []*serviceEndpoint{ep("10.0.0.1", 8080, "v1", live())})
	if got := c.GetServiceEndpoints("   "); len(got) != 0 {
		t.Fatalf("expected no-op on empty service name, got %d", len(got))
	}
}
