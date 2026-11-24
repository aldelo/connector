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

	"github.com/aldelo/common/crypto"
	"github.com/aldelo/common/wrapper/cloudmap"
	healthreportsig "github.com/aldelo/connector/internal/healthreport"
	"github.com/aldelo/connector/notifiergateway/model"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/servicediscovery"
)

// reconcileSeams snapshots the reconciler's package-level seams and restores
// them (and confirmation/cursor/budget state) when the test ends.
func reconcileSeams(t *testing.T) (listFreshPtr *func() ([]*model.HealthStatus, error)) {
	t.Helper()

	origListFresh := listFreshInstances
	origListRegistered := sdListRegisteredFn
	origListPage := sdListInstancesPageFn
	origRegisterExisting := sdRegisterExistingFn
	origClientFor := sdClientForFn
	origDial := dialTCP
	origLookup := lookupIPv4Fn

	t.Cleanup(func() {
		listFreshInstances = origListFresh
		sdListRegisteredFn = origListRegistered
		sdListInstancesPageFn = origListPage
		sdRegisterExistingFn = origRegisterExisting
		sdClientForFn = origClientFor
		dialTCP = origDial
		lookupIPv4Fn = origLookup

		reconcileStateMu.Lock()
		reconcileCursor = 0
		reconcileSeeded = false
		reconcileFirstSeen = map[string]time.Time{}
		lastReRegisteredAt = map[string]time.Time{}
		unhealableLoggedAt = map[string]time.Time{}
		cycleProbeBudgetRemaining = 0
		reconcileStateMu.Unlock()

		resetCycleProbeBudget()
	})

	// default stubs: no Cloud Map client, no registered instances
	sdClientForFn = func(namespaceId, awsRegion string) (*cloudmap.CloudMap, error) {
		return nil, nil
	}

	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		return map[string]bool{}, nil
	}

	lookupIPv4Fn = func(host string) (string, error) {
		return "", fmt.Errorf("lookup disabled in test")
	}

	resetCycleProbeBudget()

	return &listFreshInstances
}

func freshRow(instanceId string, hostInfo string) *model.HealthStatus {
	return &model.HealthStatus{
		NamespaceId:      "ns-test",
		ServiceId:        "srv-0123456789abcdef",
		InstanceId:       instanceId,
		AwsRegion:        "us-east-1",
		ServiceInfo:      "svc-test.dev.aldelo.nae",
		HostInfo:         hostInfo,
		LastTimestamp:    time.Now().UTC().Unix(),
		HostInfoVerified: true,
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

// seedConfirmedMissing marks an instance as observed fresh-but-missing since
// the given age, satisfying the confirmation window.
func seedConfirmedMissing(instanceId string, age time.Duration) {
	reconcileStateMu.Lock()
	reconcileFirstSeen[instanceId] = time.Now().UTC().Add(-age)
	reconcileStateMu.Unlock()
}

func seedRecentlyRestored(instanceId string, age time.Duration) {
	reconcileStateMu.Lock()
	lastReRegisteredAt[instanceId] = time.Now().UTC().Add(-age)
	reconcileStateMu.Unlock()
}

// TestReconcile_ReRegistersFreshMissingVerifiedInstance: a fresh, VERIFIED
// record, confirmed missing, with a reachable endpoint, must be re-registered
// under its ORIGINAL instance id with the parsed ip/port.
func TestReconcile_ReRegistersFreshMissingVerifiedInstance(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

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

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-fiserv-provider-d831ae2e", host+":"+portStr)}, nil
	}

	seedConfirmedMissing("x-nae-fiserv-provider-d831ae2e", reconcileConfirmationWindow()+time.Minute)

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

// TestReconcile_IgnoresUnverifiedRows: rows without a verified v2 signature
// (v1-only senders, replayed/forged HostInfo) must never be registered into
// discovery — not even inspected.
func TestReconcile_IgnoresUnverifiedRows(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

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

	row := freshRow("x-nae-v1-only", "127.0.0.1:12345")
	row.HostInfoVerified = false

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{row}, nil
	}

	reconcileMissingRegistrations()

	if inspected != 0 || registered != 0 {
		t.Fatalf("unverified row acted upon (inspected=%d registered=%d) — trust gate failed", inspected, registered)
	}
}

// TestReconcile_FirstSightingAwaitsConfirmation: the first observation of
// fresh-but-missing must record and defer, not register — deliberate
// deregistration (ops removing a bad instance) gets a cycle of quiet.
func TestReconcile_FirstSightingAwaitsConfirmation(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-first-sight", "127.0.0.1:1")}, nil
	}

	reconcileMissingRegistrations()

	if registered != 0 {
		t.Fatalf("first fresh-but-missing sighting was re-registered immediately (%d calls) — confirmation guard failed", registered)
	}

	reconcileStateMu.Lock()
	_, seen := reconcileFirstSeen["x-nae-first-sight"]
	reconcileStateMu.Unlock()

	if !seen {
		t.Fatal("first sighting was not recorded for confirmation")
	}
}

// TestReconcile_BackoffAfterRestore: an instance the gateway re-registered
// that goes missing again within the backoff window must NOT be re-registered
// (deliberate removal) — the window passing restores normal behavior.
func TestReconcile_BackoffAfterRestore(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test listener failed: %v", err)
	}
	defer ln.Close()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-flapping", "127.0.0.1:"+portStr)}, nil
	}

	// instance was restored 1 cycle ago and is missing again → stand down
	seedConfirmedMissing("x-nae-flapping", reconcileConfirmationWindow()+time.Minute)
	seedRecentlyRestored("x-nae-flapping", time.Minute)

	reconcileMissingRegistrations()

	if registered != 0 {
		t.Fatalf("recently-restored instance was re-registered again (%d calls) — deliberate-removal backoff failed", registered)
	}

	// after the backoff window passes, normal healing resumes
	seedRecentlyRestored("x-nae-flapping", reRegisterBackoffWindow()+time.Minute)

	reconcileMissingRegistrations()

	if registered != 1 {
		t.Fatalf("healing did not resume after the backoff window (registered=%d)", registered)
	}
}

// TestReconcile_SkipsAlreadyRegistered: a fresh record whose instance is
// already registered must not trigger a re-registration, and its
// reconciliation state must be cleared.
func TestReconcile_SkipsAlreadyRegistered(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		return map[string]bool{"x-nae-present": true}, nil
	}

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-present", "127.0.0.1:12345")}, nil
	}

	seedConfirmedMissing("x-nae-present", time.Hour) // stale confirmation state
	seedRecentlyRestored("x-nae-present", time.Hour)

	reconcileMissingRegistrations()

	if registered != 0 {
		t.Fatalf("already-registered instance was re-registered (%d calls)", registered)
	}

	reconcileStateMu.Lock()
	_, stillTracked := reconcileFirstSeen["x-nae-present"]
	_, stillRestored := lastReRegisteredAt["x-nae-present"]
	reconcileStateMu.Unlock()

	if stillTracked || stillRestored {
		t.Fatal("reconciliation state not cleared for an instance that is registered again")
	}
}

// TestReconcile_IgnoresStaleRecords: stale records are the stale sweep's
// domain — the reconciler must not even inspect their Cloud Map service.
func TestReconcile_IgnoresStaleRecords(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

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

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{staleRow("x-nae-stale", "127.0.0.1:12345")}, nil
	}

	reconcileMissingRegistrations()

	if inspected != 0 || registered != 0 {
		t.Fatalf("stale record acted upon (inspected=%d registered=%d)", inspected, registered)
	}
}

// TestReconcile_DoesNotResurrectUnreachableEndpoint: a fresh record whose
// endpoint is unreachable must NOT be re-registered.
func TestReconcile_DoesNotResurrectUnreachableEndpoint(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-fresh-dead", "127.0.0.1:1")}, nil
	}

	seedConfirmedMissing("x-nae-fresh-dead", reconcileConfirmationWindow()+time.Minute)

	reconcileMissingRegistrations()

	if registered != 0 {
		t.Fatalf("fresh-but-unreachable endpoint was re-registered (%d calls)", registered)
	}
}

// TestReconcile_HostnameResolvedToIPv4: legacy URL/hostname HostInfo rows are
// resolved to an IPv4 literal before registration.
func TestReconcile_HostnameResolvedToIPv4(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test listener failed: %v", err)
	}
	defer ln.Close()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())

	lookupIPv4Fn = func(host string) (string, error) {
		if host != "legacy.internal.aldelo.nae" {
			return "", fmt.Errorf("unexpected host %q", host)
		}
		return "127.0.0.1", nil
	}

	var got registeredCall
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		got = registeredCall{serviceId, instanceId, ip, port, healthy}
		return "op-test", nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-legacy", "https://legacy.internal.aldelo.nae:"+portStr+"/snsrelay")}, nil
	}

	seedConfirmedMissing("x-nae-legacy", reconcileConfirmationWindow()+time.Minute)

	reconcileMissingRegistrations()

	if got.instanceId != "x-nae-legacy" || got.ip != "127.0.0.1" || got.port == 0 {
		t.Fatalf("hostname HostInfo mishandled: got %+v", got)
	}
}

// TestReconcile_UnhealableServiceIdSkipsRegistration: rows whose service id
// can never be registered must be skipped without any register call or probe.
func TestReconcile_UnhealableServiceIdSkipsRegistration(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	row := freshRow("x-nae-bad-svc", "127.0.0.1:12345")
	row.ServiceId = "not-a-cloudmap-service-id"

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{row}, nil
	}

	seedConfirmedMissing("x-nae-bad-svc", reconcileConfirmationWindow()+time.Minute)

	reconcileMissingRegistrations()

	if registered != 0 {
		t.Fatalf("unhealable service-id row was re-registered (%d calls)", registered)
	}
}

// TestReconcile_PerCycleRestoreCapAcrossServices: the restore cap is per
// CYCLE across ALL services, not per service.
func TestReconcile_PerCycleRestoreCapAcrossServices(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test listener failed: %v", err)
	}
	defer ln.Close()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())

	const services = 5
	const perService = maxReRegistersPerCycle + 2

	var rows []*model.HealthStatus
	for s := 0; s < services; s++ {
		for i := 0; i < perService; i++ {
			r := freshRow(fmt.Sprintf("x-nae-s%02d-i%02d", s, i), "127.0.0.1:"+portStr)
			r.ServiceId = fmt.Sprintf("srv-0123456789abcd%02d", s)
			rows = append(rows, r)
		}
	}

	var registered int
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return rows, nil
	}

	for _, r := range rows {
		seedConfirmedMissing(r.InstanceId, reconcileConfirmationWindow()+time.Minute)
	}

	reconcileMissingRegistrations()

	if registered > maxReRegistersPerCycle {
		t.Fatalf("re-registrations (%d) exceeded the PER-CYCLE cap (%d) — cap is per service", registered, maxReRegistersPerCycle)
	}
}

// TestReconcile_SharedProbeBudget: the TCP probe budget is shared with the
// stale sweep and bounded per cycle.
func TestReconcile_SharedProbeBudget(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	reconcileStateMu.Lock()
	cycleProbeBudgetRemaining = 2
	reconcileStateMu.Unlock()

	dialed := 0
	origDial := dialTCP
	dialTCP = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		dialed++
		return origDial(network, addr, timeout)
	}

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	var rows []*model.HealthStatus
	for i := 0; i < 10; i++ {
		rows = append(rows, freshRow(fmt.Sprintf("x-nae-budget-%02d", i), "127.0.0.1:1"))
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return rows, nil
	}

	for _, r := range rows {
		seedConfirmedMissing(r.InstanceId, reconcileConfirmationWindow()+time.Minute)
	}

	reconcileMissingRegistrations()

	if dialed > 2 {
		t.Fatalf("dialed %d endpoints with a budget of 2 — shared probe budget not enforced", dialed)
	}
}

// TestReconcile_DuplicateInstanceErrorConverges: a duplicate-instance error
// from Cloud Map (the other gateway task converged first) is treated as
// benign — logged, no failure page — but it does NOT consume the restore
// budget or impose the backoff, because DuplicateRequest means a same-creator
// request is in flight, not that it succeeded.
func TestReconcile_DuplicateInstanceErrorConverges(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test listener failed: %v", err)
	}
	defer ln.Close()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())

	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		return "", fmt.Errorf("DuplicateRequest: a request with the same creatorRequestId is in progress")
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-dupe", "127.0.0.1:"+portStr)}, nil
	}

	seedConfirmedMissing("x-nae-dupe", reconcileConfirmationWindow()+time.Minute)

	// must not panic; convergence is NOT recorded as a restore
	reconcileMissingRegistrations()

	reconcileStateMu.Lock()
	_, restored := lastReRegisteredAt["x-nae-dupe"]
	reconcileStateMu.Unlock()

	if restored {
		t.Fatal("duplicate-instance convergence was recorded as a restore — in-flight requests must not consume budget or impose backoff")
	}
}

// TestReconcile_PaginationFollowsToken: the registered-id collection follows
// ListInstances pagination — instances beyond the first page must be seen as
// registered, not missing.
func TestReconcile_PaginationFollowsToken(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	// restore the real (paginated) sdListRegistered so the page seam is
	// actually exercised — the default seam stub short-circuits it
	sdListRegisteredFn = sdListRegistered

	// the real sdListRegistered requires a non-nil Cloud Map client; the
	// page seam is stubbed so no network call happens
	sdClientForFn = func(namespaceId, awsRegion string) (*cloudmap.CloudMap, error) {
		return &cloudmap.CloudMap{}, nil
	}

	page1 := []*servicediscovery.InstanceSummary{
		{Id: aws.String("x-nae-page1-a")},
	}
	page2 := []*servicediscovery.InstanceSummary{
		{Id: aws.String("x-nae-page2-b")},
	}

	calls := 0
	sdListInstancesPageFn = func(sd *cloudmap.CloudMap, serviceId string, maxResults *int64, nextToken string, timeoutDuration time.Duration) ([]*servicediscovery.InstanceSummary, string, error) {
		calls++
		if nextToken == "" {
			return page1, "token-2", nil
		}
		return page2, "", nil
	}

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{
			freshRow("x-nae-page1-a", "127.0.0.1:1"),
			freshRow("x-nae-page2-b", "127.0.0.1:1"),
		}, nil
	}

	seedConfirmedMissing("x-nae-page1-a", reconcileConfirmationWindow()+time.Minute)
	seedConfirmedMissing("x-nae-page2-b", reconcileConfirmationWindow()+time.Minute)

	reconcileMissingRegistrations()

	if calls != 2 {
		t.Fatalf("expected 2 ListInstances pages, got %d — pagination token dropped", calls)
	}

	if registered != 0 {
		t.Fatalf("instances found on page 2 were treated as missing (%d register calls) — pagination bug", registered)
	}
}

// TestReconcile_ServiceBudgetRotates: with more fresh services than the
// per-cycle budget, successive cycles cover different services and the union
// covers everything.
func TestReconcile_ServiceBudgetRotates(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	const total = maxReconcileServicesPerCycle + 10

	var rows []*model.HealthStatus
	for i := 0; i < total; i++ {
		r := freshRow("x-nae-inst", "127.0.0.1:12345")
		// 16 lowercase hex chars after "srv-" to satisfy the Cloud Map id pattern
		r.ServiceId = fmt.Sprintf("srv-%016x", i)
		r.InstanceId = "x-nae-inst-" + r.ServiceId
		rows = append(rows, r)
	}

	inspected := map[string]int{}
	var mu sync.Mutex

	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		mu.Lock()
		inspected[serviceId]++
		mu.Unlock()
		return map[string]bool{"x-nae-inst-" + serviceId: true}, nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return rows, nil
	}

	reconcileMissingRegistrations()

	mu.Lock()
	firstCycle := len(inspected)
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

// TestReconcile_EmptyStoreIsNoop and read-error skip: no side effects.
func TestReconcile_EmptyStoreAndReadError(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{}, nil
	}

	reconcileMissingRegistrations() // must not panic

	inspected := 0
	sdListRegisteredFn = func(sd *cloudmap.CloudMap, serviceId string) (map[string]bool, error) {
		inspected++
		return map[string]bool{}, nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return nil, fmt.Errorf("data store unavailable")
	}

	reconcileMissingRegistrations()

	if inspected != 0 {
		t.Fatalf("reconciler inspected services despite a read failure (%d calls)", inspected)
	}
}

// TestReconcileConfirmationWindowAtLeastOneCycle: the confirmation window
// must be at least one full sweep interval so deliberate deregistration is
// not fought within a single cycle.
func TestReconcileConfirmationWindowAtLeastOneCycle(t *testing.T) {
	if reconcileConfirmationWindow() < minReconcileConfirmWindow {
		t.Fatal("confirmation window below its floor")
	}
}

// TestReconcile_PaginationCapFailsClosed: when ListInstances hits the page
// cap with a next token outstanding, the registered-set view is INCOMPLETE —
// the service must be skipped entirely (fail closed), never acted upon.
// Acting on a truncated view would mark every instance beyond the cap as
// missing forever: attribute-wiping re-registers plus backoff paging.
func TestReconcile_PaginationCapFailsClosed(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	sdListRegisteredFn = sdListRegistered
	sdClientForFn = func(namespaceId, awsRegion string) (*cloudmap.CloudMap, error) {
		return &cloudmap.CloudMap{}, nil
	}

	pages := 0
	sdListInstancesPageFn = func(sd *cloudmap.CloudMap, serviceId string, maxResults *int64, nextToken string, timeoutDuration time.Duration) ([]*servicediscovery.InstanceSummary, string, error) {
		pages++
		// every page carries the row's instance AND a dangling next token
		return []*servicediscovery.InstanceSummary{{Id: aws.String("x-nae-beyond-cap")}}, "more-please", nil
	}

	registered := 0
	sdRegisterExistingFn = func(sd *cloudmap.CloudMap, serviceId, instanceId, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, error) {
		registered++
		return "op-test", nil
	}

	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{freshRow("x-nae-beyond-cap", "127.0.0.1:12345")}, nil
	}

	seedConfirmedMissing("x-nae-beyond-cap", reconcileConfirmationWindow()+time.Minute)

	reconcileMissingRegistrations()

	if pages != maxListInstancePages {
		t.Fatalf("expected the page cap of %d to be hit, got %d pages", maxListInstancePages, pages)
	}

	if registered != 0 {
		t.Fatalf("acted on a truncated registered-set view (%d register calls) — must fail closed", registered)
	}
}

// TestReconcile_StatePrunedForDecommissionedInstances: reconciliation state
// entries older than reconcileStateMaxAge are pruned on every cycle, so
// decommissioned instances do not leak entries forever; recent entries stay.
func TestReconcile_StatePrunedForDecommissionedInstances(t *testing.T) {
	listFreshPtr := reconcileSeams(t)

	ancient := time.Now().UTC().Add(-reconcileStateMaxAge - time.Hour)
	recent := time.Now().UTC().Add(-time.Minute)

	reconcileStateMu.Lock()
	reconcileFirstSeen = map[string]time.Time{"x-nae-old": ancient, "x-nae-new": recent}
	lastReRegisteredAt = map[string]time.Time{"x-nae-old": ancient, "x-nae-new": recent}
	unhealableLoggedAt = map[string]time.Time{"x-nae-old": ancient, "x-nae-new": recent}
	reconcileStateMu.Unlock()

	// idle cycle (empty fresh list) still prunes
	*listFreshPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{}, nil
	}

	reconcileMissingRegistrations()

	reconcileStateMu.Lock()
	defer reconcileStateMu.Unlock()

	for _, m := range []map[string]time.Time{reconcileFirstSeen, lastReRegisteredAt, unhealableLoggedAt} {
		if _, ok := m["x-nae-old"]; ok {
			t.Fatal("state for a decommissioned (ancient) instance was not pruned")
		}
		if _, ok := m["x-nae-new"]; !ok {
			t.Fatal("recent state was pruned — active entries must survive")
		}
	}
}

// TestVerifyHealthReportSignatures pins the gateway-side signature matrix:
// v1 is mandatory, v2 is additive, and every combination degrades safely.
func TestVerifyHealthReportSignatures(t *testing.T) {
	secret := "test-hash-secret"
	date := "2026-11-24"
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	valid := func(v2 string) *healthreport {
		d := &healthreport{
			NamespaceId:   "ns-test",
			ServiceId:     "srv-0123456789abcdef",
			InstanceId:    "x-nae-a",
			AwsRegion:     "us-east-1",
			ServiceInfo:   "svc-test.dev.aldelo.nae",
			HostInfo:      "10.2.0.1:50000",
			HashSignature: crypto.Sha256(healthreportsig.V1Source("ns-test", "srv-0123456789abcdef", "x-nae-a", date), secret),
		}
		if v2 != "" {
			d.HashSignatureV2 = v2
		}
		return d
	}

	validV2 := func(d *healthreport) string {
		return crypto.Sha256(healthreportsig.V2Source(d.NamespaceId, d.ServiceId, d.InstanceId, d.AwsRegion, d.ServiceInfo, d.HostInfo, date), secret)
	}

	t.Run("v1 ok, no v2", func(t *testing.T) {
		verified, ok, _ := verifyHealthReportSignatures(valid(""), secret, now)
		if !ok || verified {
			t.Fatalf("v1-only report: ok=%v verified=%v, want ok=true verified=false", ok, verified)
		}
	})

	t.Run("v1 ok, v2 ok", func(t *testing.T) {
		d := valid("")
		d.HashSignatureV2 = validV2(d)
		verified, ok, _ := verifyHealthReportSignatures(d, secret, now)
		if !ok || !verified {
			t.Fatalf("fully-signed report: ok=%v verified=%v, want ok=true verified=true", ok, verified)
		}
	})

	t.Run("v1 mismatch", func(t *testing.T) {
		d := valid("")
		d.HashSignature = "wrong"
		_, ok, _ := verifyHealthReportSignatures(d, secret, now)
		if ok {
			t.Fatal("v1 mismatch must reject the report")
		}
	})

	t.Run("v1 ok, v2 mismatch", func(t *testing.T) {
		d := valid("")
		d.HashSignatureV2 = "wrong"
		_, ok, _ := verifyHealthReportSignatures(d, secret, now)
		if ok {
			t.Fatal("v2 mismatch must reject the report even when v1 passes")
		}
	})

	t.Run("v2 cannot bypass v1", func(t *testing.T) {
		d := valid("")
		d.HashSignature = "wrong"
		d.HashSignatureV2 = validV2(d)
		_, ok, _ := verifyHealthReportSignatures(d, secret, now)
		if ok {
			t.Fatal("a valid v2 must not bypass a failed v1")
		}
	})

	t.Run("midnight rollover fails both", func(t *testing.T) {
		d := valid("")
		d.HashSignatureV2 = validV2(d)
		// verify "the next day" — both signatures are date-bound
		nextDay := time.Date(2026, 11, 25, 0, 30, 0, 0, time.UTC)
		_, ok, _ := verifyHealthReportSignatures(d, secret, nextDay)
		if ok {
			t.Fatal("date rollover must fail both signatures (client retries)")
		}
	})

	t.Run("forged hostinfo breaks v2", func(t *testing.T) {
		d := valid("")
		d.HashSignatureV2 = validV2(d)
		d.HostInfo = "9.9.9.9:53" // tampered after signing
		verified, ok, _ := verifyHealthReportSignatures(d, secret, now)
		if ok || verified {
			t.Fatal("tampered HostInfo with stale v2 must be rejected")
		}
	})
}
