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

	"github.com/aldelo/common/wrapper/cloudmap"
	"github.com/aldelo/connector/adapters/registry"
)

// These tests drive the real setApiDiscoveredIpPorts wiring with a fake service-discovery
// function (via the discoverInstancesFn seam), so the forced-refresh bypass and the
// grace-reconcile-into-resolver behavior are covered end-to-end without an AWS Cloud Map client.

func iinfo(ip string, port uint, ver string) *registry.InstanceInfo {
	return &registry.InstanceInfo{InstanceIP: ip, InstancePort: port, InstanceVersion: ver, InstanceId: ip, InstanceHealthy: true}
}

func newApiClient() *Client {
	c := &Client{}
	c._sd = &cloudmap.CloudMap{} // non-nil; the fake discoverer ignores it
	return c
}

// installFakeDiscover swaps discoverInstancesFn for the duration of a test and returns the
// restore func plus a pointer to a call counter and a settable result.
func installFakeDiscover(t *testing.T, result *[]*registry.InstanceInfo, calls *int) func() {
	t.Helper()
	orig := discoverInstancesFn
	discoverInstancesFn = func(sd *cloudmap.CloudMap, serviceName, namespaceName string, healthy bool,
		customAttributes map[string]string, maxResults *int64, timeoutDuration ...time.Duration) ([]*registry.InstanceInfo, error) {
		*calls++
		return *result, nil
	}
	return func() { discoverInstancesFn = orig }
}

func TestWiring_ForcedRefreshBypassesWarmCacheAndDrivesResolver(t *testing.T) {
	ClearEndpointCache()
	calls := 0
	result := []*registry.InstanceInfo{iinfo("10.0.0.1", 8080, ""), iinfo("10.0.0.2", 8080, "")}
	defer installFakeDiscover(t, &result, &calls)()

	c := newApiClient()
	exp := time.Now().Add(5 * time.Minute)

	if err := c.setApiDiscoveredIpPorts(exp, "svc", "ns", "", 100, 0, true, 3); err != nil {
		t.Fatalf("forced refresh returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("forced refresh must query discovery; calls=%d want 1", calls)
	}
	if got := hostSet(c.endpointsSnapshot()); len(got) != 2 || !got["10.0.0.1"] || !got["10.0.0.2"] {
		t.Fatalf("resolver set should be {.1,.2}, got %v", got)
	}

	// second forced refresh must query AGAIN despite a warm cache — the original bug was that
	// forceRefresh was smuggled in as ignoreExpired and the warm cache short-circuited the query.
	if err := c.setApiDiscoveredIpPorts(exp, "svc", "ns", "", 100, 0, true, 3); err != nil {
		t.Fatalf("err: %v", err)
	}
	if calls != 2 {
		t.Fatalf("forced refresh must bypass the warm cache; calls=%d want 2", calls)
	}
}

func TestWiring_NonForcedRefreshServesFromCache(t *testing.T) {
	ClearEndpointCache()
	calls := 0
	result := []*registry.InstanceInfo{iinfo("10.0.0.1", 8080, "")}
	defer installFakeDiscover(t, &result, &calls)()

	c := newApiClient()
	exp := time.Now().Add(5 * time.Minute)

	if err := c.setApiDiscoveredIpPorts(exp, "svc", "ns", "", 100, 0, true, 3); err != nil { // populate cache
		t.Fatalf("err: %v", err)
	}
	if calls != 1 {
		t.Fatalf("forced populate should query once; calls=%d", calls)
	}
	// non-forced within TTL must be served by the cache and NOT re-query
	if err := c.setApiDiscoveredIpPorts(exp, "svc", "ns", "", 100, 0, false, 3); err != nil {
		t.Fatalf("err: %v", err)
	}
	if calls != 1 {
		t.Fatalf("non-forced refresh must serve from cache (no query); calls=%d want 1", calls)
	}
}

func TestWiring_TransientPartialDoesNotShrinkResolverUntilGrace(t *testing.T) {
	ClearEndpointCache()
	calls := 0
	full := []*registry.InstanceInfo{iinfo("10.0.0.1", 8080, ""), iinfo("10.0.0.2", 8080, ""), iinfo("10.0.0.3", 8080, "")}
	partial := []*registry.InstanceInfo{iinfo("10.0.0.1", 8080, "")}
	current := full
	orig := discoverInstancesFn
	discoverInstancesFn = func(sd *cloudmap.CloudMap, s, n string, h bool, a map[string]string, m *int64, to ...time.Duration) ([]*registry.InstanceInfo, error) {
		calls++
		return current, nil
	}
	defer func() { discoverInstancesFn = orig }()

	c := newApiClient()
	exp := time.Now().Add(5 * time.Minute)
	force := func() {
		if err := c.setApiDiscoveredIpPorts(exp, "svc", "ns", "", 100, 0, true, 3); err != nil {
			t.Fatalf("err: %v", err)
		}
	}

	force() // establishes {1,2,3}
	if n := len(c.endpointsSnapshot()); n != 3 {
		t.Fatalf("expected 3 endpoints, got %d", n)
	}

	// .2 and .3 flap out (partial healthy result). With grace=3 they must be RETAINED for the
	// next two cycles — the resolver must NOT collapse onto .1 (the capacity-collapse regression).
	current = partial
	force()
	if got := hostSet(c.endpointsSnapshot()); len(got) != 3 {
		t.Fatalf("transient partial result must not shrink resolver (cycle1); got %v", got)
	}
	force()
	if got := hostSet(c.endpointsSnapshot()); len(got) != 3 {
		t.Fatalf("transient partial result must not shrink resolver (cycle2); got %v", got)
	}
	// third consecutive absence reaches grace -> now they prune (genuine removal path)
	force()
	if got := hostSet(c.endpointsSnapshot()); len(got) != 1 || !got["10.0.0.1"] {
		t.Fatalf("after grace, genuinely-absent endpoints prune to {.1}; got %v", got)
	}
}

func TestWiring_EmptyDiscoveryDoesNotShrinkResolver(t *testing.T) {
	ClearEndpointCache()
	calls := 0
	current := []*registry.InstanceInfo{iinfo("10.0.0.1", 8080, ""), iinfo("10.0.0.2", 8080, "")}
	orig := discoverInstancesFn
	discoverInstancesFn = func(sd *cloudmap.CloudMap, s, n string, h bool, a map[string]string, m *int64, to ...time.Duration) ([]*registry.InstanceInfo, error) {
		calls++
		return current, nil
	}
	defer func() { discoverInstancesFn = orig }()

	c := newApiClient()
	exp := time.Now().Add(5 * time.Minute)
	if err := c.setApiDiscoveredIpPorts(exp, "svc", "ns", "", 100, 0, true, 3); err != nil { // {1,2}
		t.Fatalf("err: %v", err)
	}
	// a total-empty discovery must fail fast and leave the resolver at last-known-good
	current = []*registry.InstanceInfo{}
	if err := c.setApiDiscoveredIpPorts(exp, "svc", "ns", "", 100, 0, true, 3); err == nil {
		t.Fatalf("empty discovery should return an error (fail fast), got nil")
	}
	if got := hostSet(c.endpointsSnapshot()); len(got) != 2 {
		t.Fatalf("empty discovery must not shrink the resolver; got %v", got)
	}
}
