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
	"testing"
	"time"

	"github.com/aldelo/connector/notifiergateway/model"
)

// staleRemoverSeams snapshots the remover's package-level seams and restores
// them (and the confirmation / cap / write-failure marker state) when the test
// ends.
func staleRemoverSeams(t *testing.T) (listFn *func() ([]*model.HealthStatus, error), deregFn *func(string, string, string, string) error) {
	t.Helper()

	origList := listInactiveInstances
	origDereg := deregisterInstanceViaSd
	origDial := dialTCP

	t.Cleanup(func() {
		listInactiveInstances = origList
		deregisterInstanceViaSd = origDereg
		dialTCP = origDial
		updateStaleConfirmations(map[string]time.Time{})
		cappedCycle(false)
		model.ResetLastInstanceHealthWriteFailure()
	})

	return &listInactiveInstances, &deregisterInstanceViaSd
}

func newStaleRow(instanceId string, hostInfo string) *model.HealthStatus {
	return &model.HealthStatus{
		NamespaceId: "ns-test",
		ServiceId:   "srv-test",
		InstanceId:  instanceId,
		AwsRegion:   "us-east-1",
		ServiceInfo: "svc-test.dev.aldelo.nae",
		HostInfo:    hostInfo,
	}
}

// oldFirstSeen returns a timestamp safely past the confirmation window.
func oldFirstSeen() time.Time {
	return time.Now().UTC().Add(-staleConfirmationWindow() - time.Minute)
}

// TestParseDialTarget covers HostInfo normalization for the liveness probe:
// plain host:port (go-ms service format from Service.LocalAddress) and URL
// forms (legacy notifier records), plus rejection cases.
func TestParseDialTarget(t *testing.T) {
	cases := []struct {
		name     string
		hostInfo string
		want     string
		wantErr  bool
	}{
		{name: "plain ipv4 port", hostInfo: "10.2.105.213:35267", want: "10.2.105.213:35267"},
		{name: "plain host port", hostInfo: "ip-10-2-105-213.ec2.internal:35267", want: "ip-10-2-105-213.ec2.internal:35267"},
		{name: "bracketed ipv6", hostInfo: "[::1]:35267", want: "[::1]:35267"},
		{name: "https url form", hostInfo: "https://i-10-2-142-18.dev-notifier-ws-aldelo.nae:8443", want: "i-10-2-142-18.dev-notifier-ws-aldelo.nae:8443"},
		{name: "url with path", hostInfo: "https://10.1.1.1:8443/snsrelay", want: "10.1.1.1:8443"},
		{name: "empty", hostInfo: "", wantErr: true},
		{name: "missing port", hostInfo: "10.2.105.213", wantErr: true},
		{name: "url missing port", hostInfo: "https://10.1.1.1", wantErr: true},
		{name: "garbage", hostInfo: "not a host", wantErr: true},
		{name: "log injection payload", hostInfo: "1.2.3.4::80\n~~~ forged log line ~~~", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDialTarget(tc.hostInfo)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseDialTarget(%q) expected error, got %q", tc.hostInfo, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("parseDialTarget(%q) unexpected error: %v", tc.hostInfo, err)
			}

			if got != tc.want {
				t.Fatalf("parseDialTarget(%q) = %q, want %q", tc.hostInfo, got, tc.want)
			}
		})
	}
}

// TestInstanceHealthWriteFailureMarker verifies the model-side write-failure
// marker used by the remover's stand-down guard, including explicit aging.
func TestInstanceHealthWriteFailureMarker(t *testing.T) {
	model.ResetLastInstanceHealthWriteFailure()

	if _, failed := model.TimeSinceLastInstanceHealthWriteFailure(); failed {
		t.Fatal("expected no write failure recorded after reset")
	}

	model.MarkLastInstanceHealthWriteFailure()

	elapsed, failed := model.TimeSinceLastInstanceHealthWriteFailure()
	if !failed {
		t.Fatal("expected write failure recorded after mark")
	}

	if elapsed < 0 || elapsed > time.Minute {
		t.Fatalf("unexpected elapsed since write failure: %s", elapsed)
	}

	model.SetLastInstanceHealthWriteFailureAt(time.Now().UTC().Add(-time.Hour))

	elapsed, failed = model.TimeSinceLastInstanceHealthWriteFailure()
	if !failed {
		t.Fatal("expected write failure recorded after explicit set")
	}

	if elapsed < 55*time.Minute {
		t.Fatalf("expected aged write failure (~1h), got %s", elapsed)
	}

	model.ResetLastInstanceHealthWriteFailure()

	if _, failed := model.TimeSinceLastInstanceHealthWriteFailure(); failed {
		t.Fatal("expected no write failure recorded after second reset")
	}
}

// TestStaleConfirmationWindowAtLeastStaleThreshold: the confirmation window
// must be at least the effective stale threshold so a short write outage
// cannot carry a record from fresh to confirmed-stale without a full further
// window of continuous staleness.
func TestStaleConfirmationWindowAtLeastStaleThreshold(t *testing.T) {
	stale := time.Duration(model.EffectiveHealthReportRecordStaleMinutes()) * time.Minute
	window := staleConfirmationWindow()

	if window < stale {
		t.Fatalf("confirmation window (%s) must be >= stale threshold (%s)", window, stale)
	}

	if window < minStaleConfirmWindow {
		t.Fatalf("confirmation window (%s) must be >= floor (%s)", window, minStaleConfirmWindow)
	}
}

// TestRemoveInactive_StandsDownAfterWriteFailure: guard 1 — while the data
// store recently failed an instance-health write, the sweep must not even
// query for stale records (incident 2026-11-23 regression test).
func TestRemoveInactive_StandsDownAfterWriteFailure(t *testing.T) {
	listPtr, _ := staleRemoverSeams(t)

	queried := false
	*listPtr = func() ([]*model.HealthStatus, error) {
		queried = true
		return []*model.HealthStatus{newStaleRow("inst-stand-down", "127.0.0.1:1")}, nil
	}

	model.MarkLastInstanceHealthWriteFailure()

	removeInactiveInstancesFromServiceDiscovery()

	if queried {
		t.Fatal("stale sweep queried the data store despite a recent write failure — stand-down guard did not engage")
	}
}

// TestRemoveInactive_StandDownExpires: an aged write failure (older than the
// stand-down window) must NOT block the sweep — guards must lapse.
func TestRemoveInactive_StandDownExpires(t *testing.T) {
	listPtr, _ := staleRemoverSeams(t)

	queried := false
	*listPtr = func() ([]*model.HealthStatus, error) {
		queried = true
		return []*model.HealthStatus{}, nil
	}

	standDown := time.Duration(model.EffectiveHealthReportRecordStaleMinutes())*time.Minute + maxHealthReportInterval
	model.SetLastInstanceHealthWriteFailureAt(time.Now().UTC().Add(-standDown - time.Minute))

	removeInactiveInstancesFromServiceDiscovery()

	if !queried {
		t.Fatal("stale sweep stood down on an aged write failure — stand-down window does not lapse")
	}
}

// TestRemoveInactive_FirstSightingDoesNotRestamp: two consecutive real cycles
// with no seeding — the first sighting must not deregister, and the second
// cycle must not re-stamp the first-seen time (a re-stamping regression would
// make the confirmation window never engage in production while still passing
// seeded tests).
func TestRemoveInactive_FirstSightingDoesNotRestamp(t *testing.T) {
	listPtr, deregPtr := staleRemoverSeams(t)

	*listPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{newStaleRow("inst-first-sight", "127.0.0.1:1")}, nil
	}

	deregistered := 0
	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		deregistered++
		return nil
	}

	updateStaleConfirmations(map[string]time.Time{})

	removeInactiveInstancesFromServiceDiscovery()

	if deregistered != 0 {
		t.Fatalf("first stale sighting was deregistered immediately (%d calls) — confirmation guard did not engage", deregistered)
	}

	firstSeenAt, seen := staleFirstSeenLookup("inst-first-sight")
	if !seen {
		t.Fatal("first stale sighting was not recorded for confirmation on the next cycle")
	}

	time.Sleep(50 * time.Millisecond)
	removeInactiveInstancesFromServiceDiscovery()

	secondSeenAt, seen := staleFirstSeenLookup("inst-first-sight")
	if !seen {
		t.Fatal("sighting disappeared on the second cycle while still stale")
	}

	if secondSeenAt.After(firstSeenAt) {
		t.Fatalf("first-seen time was re-stamped on the second cycle (%s -> %s) — confirmation window can never engage", firstSeenAt, secondSeenAt)
	}
}

// TestRemoveInactive_KeepsAliveEndpointWithoutForgingHeartbeat: guard 3 — a
// stale record whose HostInfo endpoint answers a TCP dial must be KEPT across
// multiple cycles, never deregistered, and (critically) the sweeper must NOT
// write a heartbeat-shaped record: with no real data-store write possible
// (nil store), any attempted refresh would set the write-failure marker —
// its absence proves no forgery.
func TestRemoveInactive_KeepsAliveEndpointWithoutForgingHeartbeat(t *testing.T) {
	listPtr, deregPtr := staleRemoverSeams(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("test listener failed: %v", err)
	}
	defer ln.Close()

	aliveAddr := ln.Addr().String()

	*listPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{newStaleRow("inst-alive", aliveAddr)}, nil
	}

	deregistered := 0
	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		deregistered++
		return nil
	}

	updateStaleConfirmations(map[string]time.Time{
		"inst-alive": oldFirstSeen(),
	})

	for cycle := 0; cycle < 3; cycle++ {
		removeInactiveInstancesFromServiceDiscovery()
	}

	if deregistered != 0 {
		t.Fatalf("live endpoint was deregistered (%d calls) — liveness guard did not engage", deregistered)
	}

	if _, failed := model.TimeSinceLastInstanceHealthWriteFailure(); failed {
		t.Fatal("write-failure marker set during keep cycles — the sweeper attempted a data-store write (forged heartbeat) for a kept instance")
	}

	// kept instances must remain tracked so they are re-probed next cycle
	if _, tracked := staleFirstSeenLookup("inst-alive"); !tracked {
		t.Fatal("kept live instance dropped from confirmation state — next cycle would re-await the confirmation window before re-probing")
	}
}

// TestRemoveInactive_DeregistersConfirmedUnreachable: the happy path still
// works — a confirmed-stale record whose endpoint is unreachable is
// deregistered.
func TestRemoveInactive_DeregistersConfirmedUnreachable(t *testing.T) {
	listPtr, deregPtr := staleRemoverSeams(t)

	*listPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{newStaleRow("inst-dead", "127.0.0.1:1")}, nil
	}

	deregistered := 0
	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		deregistered++
		return nil
	}

	updateStaleConfirmations(map[string]time.Time{
		"inst-dead": oldFirstSeen(),
	})

	removeInactiveInstancesFromServiceDiscovery()

	if deregistered != 1 {
		t.Fatalf("confirmed unreachable instance was not deregistered (got %d calls)", deregistered)
	}
}

// TestRemoveInactive_CapOrdersByConfirmationAge: guard 4 truncation must be
// oldest-confirmation-age-first, not query-order — a row confirmed stale for
// hours must not wait behind rows confirmed minutes ago.
func TestRemoveInactive_CapOrdersByConfirmationAge(t *testing.T) {
	listPtr, deregPtr := staleRemoverSeams(t)

	now := time.Now().UTC()

	// deliberately NOT in age order
	rows := []*model.HealthStatus{
		newStaleRow("inst-young-1", "127.0.0.1:1"),
		newStaleRow("inst-oldest", "127.0.0.1:1"),
		newStaleRow("inst-young-2", "127.0.0.1:1"),
		newStaleRow("inst-old", "127.0.0.1:1"),
	}

	*listPtr = func() ([]*model.HealthStatus, error) {
		return rows, nil
	}

	var deregistered []string
	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		deregistered = append(deregistered, instanceId)
		return nil
	}

	updateStaleConfirmations(map[string]time.Time{
		"inst-young-1": now.Add(-staleConfirmationWindow() - 1*time.Minute),
		"inst-oldest":  now.Add(-staleConfirmationWindow() - 10*time.Minute),
		"inst-young-2": now.Add(-staleConfirmationWindow() - 2*time.Minute),
		"inst-old":     now.Add(-staleConfirmationWindow() - 5*time.Minute),
	})

	removeInactiveInstancesFromServiceDiscovery()

	if len(deregistered) != maxDeregistersPerCycle {
		t.Fatalf("expected per-cycle cap of %d deregistrations, got %d", maxDeregistersPerCycle, len(deregistered))
	}

	want := []string{"inst-oldest", "inst-old", "inst-young-2"}
	for i, id := range deregistered {
		if id != want[i] {
			t.Fatalf("deregistration order = %v, want %v (oldest confirmation age first)", deregistered, want)
		}
	}
}

// TestRemoveInactive_CapEscalatesOnlyAfterConsecutiveCycles: the !!! severity
// token for hitting the cap must only fire once the cap has bound for
// capEscalationCycles consecutive cycles.
func TestRemoveInactive_CapEscalatesOnlyAfterConsecutiveCycles(t *testing.T) {
	listPtr, deregPtr := staleRemoverSeams(t)

	rows := []*model.HealthStatus{
		newStaleRow("inst-1", "127.0.0.1:1"),
		newStaleRow("inst-2", "127.0.0.1:1"),
		newStaleRow("inst-3", "127.0.0.1:1"),
		newStaleRow("inst-4", "127.0.0.1:1"),
	}

	*listPtr = func() ([]*model.HealthStatus, error) {
		return rows, nil
	}

	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		return nil
	}

	seed := map[string]time.Time{}
	for _, r := range rows {
		seed[r.InstanceId] = oldFirstSeen()
	}
	updateStaleConfirmations(seed)

	if cappedEscalated() {
		t.Fatal("cap escalation must start disengaged")
	}

	for i := 0; i < capEscalationCycles-1; i++ {
		removeInactiveInstancesFromServiceDiscovery()
		if cappedEscalated() {
			t.Fatalf("cap escalated after only %d capped cycles (threshold %d)", i+1, capEscalationCycles)
		}
	}

	removeInactiveInstancesFromServiceDiscovery()

	if !cappedEscalated() {
		t.Fatalf("cap did not escalate after %d consecutive capped cycles", capEscalationCycles)
	}
}

// TestRemoveInactive_ZeroFoundClearsConfirmations: an empty stale result must
// reset the confirmation state so ids from a past anomaly do not linger.
func TestRemoveInactive_ZeroFoundClearsConfirmations(t *testing.T) {
	listPtr, _ := staleRemoverSeams(t)

	*listPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{}, nil
	}

	updateStaleConfirmations(map[string]time.Time{
		"inst-lingering": time.Now().UTC().Add(-time.Hour),
	})

	removeInactiveInstancesFromServiceDiscovery()

	if _, seen := staleFirstSeenLookup("inst-lingering"); seen {
		t.Fatal("stale-confirmation state was not cleared on an all-OK cycle")
	}
}

// TestRemoveInactive_UnparseableHostInfoProceedsOnAge: a record whose
// HostInfo cannot be parsed for a liveness probe still flows through the
// age-based path (records with garbage endpoints must not be retained
// forever) — the other guards still apply.
func TestRemoveInactive_UnparseableHostInfoProceedsOnAge(t *testing.T) {
	listPtr, deregPtr := staleRemoverSeams(t)

	*listPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{newStaleRow("inst-garbage", "not a host")}, nil
	}

	deregistered := 0
	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		deregistered++
		return nil
	}

	updateStaleConfirmations(map[string]time.Time{
		"inst-garbage": oldFirstSeen(),
	})

	removeInactiveInstancesFromServiceDiscovery()

	if deregistered != 1 {
		t.Fatalf("unparseable HostInfo with confirmed-stale age was not deregistered (got %d calls)", deregistered)
	}
}

// TestRemoveInactive_ProbeBudgetDefers: when more rows pass the confirmation
// window than the per-cycle liveness probe budget, the remainder must be
// deferred (no dial, no deregister) and picked up by later cycles.
func TestRemoveInactive_ProbeBudgetDefers(t *testing.T) {
	listPtr, deregPtr := staleRemoverSeams(t)

	var rows []*model.HealthStatus
	for i := 0; i < maxLivenessProbesPerCycle+5; i++ {
		rows = append(rows, newStaleRow(fmt.Sprintf("inst-budget-%02d", i), "127.0.0.1:1"))
	}

	*listPtr = func() ([]*model.HealthStatus, error) {
		return rows, nil
	}

	deregistered := 0
	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		deregistered++
		return nil
	}

	seed := map[string]time.Time{}
	for _, r := range rows {
		seed[r.InstanceId] = oldFirstSeen()
	}
	updateStaleConfirmations(seed)

	removeInactiveInstancesFromServiceDiscovery()

	if deregistered > maxDeregistersPerCycle {
		t.Fatalf("deregistrations (%d) exceeded the per-cycle cap (%d)", deregistered, maxDeregistersPerCycle)
	}

	if deregistered != maxDeregistersPerCycle {
		t.Fatalf("expected the cap to be reached even under probe deferral, got %d", deregistered)
	}
}
