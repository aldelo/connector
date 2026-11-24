package notifiergateway

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
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/aldelo/common/wrapper/cloudmap"
	"github.com/aldelo/connector/notifiergateway/model"
)

// reconcileSeams snapshots the reconciler's package-level seams and restores
// them (and cursor state) when the test ends.
func reconcileSeams(t *testing.T) (listAllPtr *func() ([]*model.HealthStatus, error)) {
	t.Helper()

	origListAll := listAllInstances
	origListRegistered := sdListRegisteredFn
	origRegisterExisting := sdRegisterExistingFn
	origClientFor := sdClientForFn
	origDial := dialTCP

	t.Cleanup(func() {
		listAllInstances = origListAll
		sdListRegisteredFn = origListRegistered
		sdRegisterExistingFn = origRegisterExisting
		sdClientForFn = origClientFor
		dialTCP = origDial

		reconcileMu.Lock()
		reconcileCursor = 0
		reconcileMu.Unlock()
	})

	// default stubs: no Cloud Map client, no registered instances
	sdClientForFn = func(namespaceId, awsRegion string) (*cloudmap.CloudMap, error) {
		return nil, nil
	}

	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		return map[string]bool{}, nil
	}

	return &listAllInstances
}

func freshRow(instanceId string, hostInfo string) *model.HealthStatus {
	return &model.HealthStatus{
		NamespaceId:   "ns-test",
		ServiceId:     "srv-0123456789abcdef",
		InstanceId:    instanceId,
		AwsRegion:     "us-east-1",
		ServiceInfo:   "svc-test.dev.aldelo.nae",
		HostInfo:      hostInfo,
		LastTimestamp: time.Now().UTC().Unix(),
	}
}

func staleRow(instanceId string, hostInfo string) *model.HealthStatus {
	r := freshRow(instanceId, hostInfo)
	r.LastTimestamp = time.Now().UTC().Add(-2 * time.Hour).Unix()
	return r
}

type registeredCall struct {
	serviceId  string
	instanceId string
	ip         string
	port       uint
	healthy    bool
}

// TestReconcile_ReRegistersFreshMissingInstance: a fresh record whose
// instance is absent from Cloud Map, with a reachable endpoint, must be
// re-registered under its ORIGINAL instance id with the parsed ip/port.
func TestReconcile_ReRegistersFreshMissingInstance(t *testing.T) {
	listAllPtr := reconcileSeams(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test listener failed: %v", err)
	}
	defer ln.Close()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())

	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		return map[string]bool{"x-nae-other-inst": true}, nil
	}

	var calls []registeredCall
	var callsMu sync.Mutex
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		callsMu.Lock()
		calls = append(calls, registeredCall{serviceId, instanceId, ip, port, healthy})
		callsMu.Unlock()
		return "op-test", nil
	}

	*listAllPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-fiserv-provider-d831ae2e", host+":"+portStr)}, nil
	}

	reconcileMissingRegistrations()

	callsMu.Lock()
	defer callsMu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected 1 re-registration, got %d", len(calls))
	}

	c := calls[0]
	if c.instanceId != "x-nae-fiserv-provider-d831ae2e" {
		t.Fatalf("re-registered under %q — must preserve the original instance id", c.instanceId)
	}
	if c.ip != host || c.port == 0 {
		t.Fatalf("re-registered with ip=%q port=%d — want host %q and the parsed port", c.ip, c.port, host)
	}
	if !c.healthy {
		t.Fatal("re-registration must mark the instance healthy")
	}
}

// TestReconcile_SkipsAlreadyRegistered: a fresh record whose instance is
// already registered must not trigger a re-registration.
func TestReconcile_SkipsAlreadyRegistered(t *testing.T) {
	listAllPtr := reconcileSeams(t)

	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		return map[string]bool{"x-nae-present": true}, nil
	}

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listAllPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-present", "127.0.0.1:12345")}, nil
	}

	reconcileMissingRegistrations()

	if registered != 0 {
		t.Fatalf("already-registered instance was re-registered (%d calls)", registered)
	}
}

// TestReconcile_IgnoresStaleRecords: stale records are the stale sweep's
// domain — the reconciler must not even inspect their Cloud Map service.
func TestReconcile_IgnoresStaleRecords(t *testing.T) {
	listAllPtr := reconcileSeams(t)

	inspected := 0
	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		inspected++
		return map[string]bool{}, nil
	}

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listAllPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{staleRow("x-nae-stale", "127.0.0.1:12345")}, nil
	}

	reconcileMissingRegistrations()

	if inspected != 0 {
		t.Fatalf("stale record's service was inspected (%d calls) — reconciler must skip stale records", inspected)
	}

	if registered != 0 {
		t.Fatalf("stale record was re-registered (%d calls)", registered)
	}
}

// TestReconcile_DoesNotResurrectUnreachableEndpoint: a fresh record whose
// endpoint is unreachable must NOT be re-registered (never resurrect a dead
// endpoint into discovery) — this state is logged as an @@@ alarm instead.
func TestReconcile_DoesNotResurrectUnreachableEndpoint(t *testing.T) {
	listAllPtr := reconcileSeams(t)

	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		return map[string]bool{}, nil
	}

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listAllPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-fresh-dead", "127.0.0.1:1")}, nil
	}

	reconcileMissingRegistrations()

	if registered != 0 {
		t.Fatalf("fresh-but-unreachable endpoint was re-registered (%d calls)", registered)
	}
}

// TestReconcile_URLHostInfoForm: legacy URL-form HostInfo rows are parsed and
// re-registered with host and port extracted from the URL.
func TestReconcile_URLHostInfoForm(t *testing.T) {
	listAllPtr := reconcileSeams(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test listener failed: %v", err)
	}
	defer ln.Close()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())

	var got registeredCall
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		got = registeredCall{serviceId, instanceId, ip, port, healthy}
		return "op-test", nil
	}

	*listAllPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-legacy-url", "https://"+host+":"+portStr+"/snsrelay")}, nil
	}

	reconcileMissingRegistrations()

	if got.instanceId != "x-nae-legacy-url" || got.ip != host || got.port == 0 {
		t.Fatalf("URL-form HostInfo mishandled: got %+v", got)
	}
}

// TestReconcile_ServiceBudgetRotates: with more fresh services than the
// per-cycle budget, successive cycles must cover different services and the
// union must cover everything.
func TestReconcile_ServiceBudgetRotates(t *testing.T) {
	listAllPtr := reconcileSeams(t)

	const total = maxReconcileServicesPerCycle + 10

	var rows []*model.HealthStatus
	for i := 0; i < total; i++ {
		r := freshRow("x-nae-inst", "127.0.0.1:12345")
		r.ServiceId = "srv-0123456789abcde" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
		r.InstanceId = "x-nae-inst-" + r.ServiceId
		rows = append(rows, r)
	}

	inspected := map[string]int{}
	cycleSeen := map[string]bool{}
	var mu sync.Mutex

	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		mu.Lock()
		inspected[serviceId]++
		cycleSeen[serviceId] = true
		mu.Unlock()
		return map[string]bool{"x-nae-inst-" + serviceId: true}, nil // already registered → no re-register
	}

	*listAllPtr = func() ([]*model.HealthStatus, error) {
		return rows, nil
	}

	reconcileMissingRegistrations()

	mu.Lock()
	firstCycle := len(cycleSeen)
	cycleSeen = map[string]bool{}
	mu.Unlock()

	reconcileMissingRegistrations()

	mu.Lock()
	defer mu.Unlock()

	if firstCycle > maxReconcileServicesPerCycle {
		t.Fatalf("first cycle inspected %d services — budget not enforced", firstCycle)
	}

	if len(inspected) != total {
		t.Fatalf("expected all %d services covered across two rotating cycles, got %d", total, len(inspected))
	}
}

// TestReconcile_PerCycleRestoreCap: a single service with more missing
// registrations than the per-cycle restore cap defers the remainder.
func TestReconcile_PerCycleRestoreCap(t *testing.T) {
	listAllPtr := reconcileSeams(t)

	const missing = maxReRegistersPerCycle + 4

	var rows []*model.HealthStatus
	for i := 0; i < missing; i++ {
		rows = append(rows, freshRow(fmt.Sprintf("x-nae-missing-inst-%02d", i), "127.0.0.1:12345"))
	}

	var registered int
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	// endpoint never dialed-reachable? dial must succeed to re-register —
	// use a live listener so the cap, not the dial, is what limits
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test listener failed: %v", err)
	}
	defer ln.Close()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	for _, r := range rows {
		r.HostInfo = "127.0.0.1:" + portStr
	}

	*listAllPtr = func() ([]*model.HealthStatus, error) {
		return rows, nil
	}

	reconcileMissingRegistrations()

	if registered > maxReRegistersPerCycle {
		t.Fatalf("re-registrations (%d) exceeded per-cycle restore cap (%d)", registered, maxReRegistersPerCycle)
	}

	if registered != maxReRegistersPerCycle {
		t.Fatalf("expected the restore cap to be reached, got %d", registered)
	}
}

// TestReconcile_EmptyStoreIsNoop: an empty record set must be a clean no-op.
func TestReconcile_EmptyStoreIsNoop(t *testing.T) {
	listAllPtr := reconcileSeams(t)

	*listAllPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{}, nil
	}

	// must not panic; nothing to assert beyond that
	reconcileMissingRegistrations()
}

// TestReconcile_ListErrorSkipsQuietly: a data-store read failure must skip
// the cycle without side effects.
func TestReconcile_ListErrorSkipsQuietly(t *testing.T) {
	listAllPtr := reconcileSeams(t)

	*listAllPtr = func() ([]*model.HealthStatus, error) {
		return nil, fmt.Errorf("data store unavailable")
	}

	inspected := 0
	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		inspected++
		return map[string]bool{}, nil
	}

	reconcileMissingRegistrations()

	if inspected != 0 {
		t.Fatalf("reconciler inspected services despite a data-store read failure (%d calls)", inspected)
	}
}
