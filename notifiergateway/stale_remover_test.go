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
	"net"
	"testing"
	"time"

	"github.com/aldelo/connector/notifiergateway/model"
)

// staleRemoverSeams snapshots the remover's package-level seams and restores
// them (and the confirmation / write-failure marker state) when the test ends.
func staleRemoverSeams(t *testing.T) (listFn *func() ([]*model.HealthStatus, error), deregFn *func(string, string, string, string) error, refreshFn *func(string, string, string, string, string, string) error) {
	t.Helper()

	origList := listInactiveInstances
	origDereg := deregisterInstanceViaSd
	origRefresh := refreshInstanceRecord
	origDial := dialTCP

	t.Cleanup(func() {
		listInactiveInstances = origList
		deregisterInstanceViaSd = origDereg
		refreshInstanceRecord = origRefresh
		dialTCP = origDial
		updateStaleConfirmations(map[string]time.Time{})
		model.ResetLastInstanceHealthWriteFailure()
	})

	return &listInactiveInstances, &deregisterInstanceViaSd, &refreshInstanceRecord
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
		{name: "https url form", hostInfo: "https://i-10-2-142-18.dev-notifier-ws-aldelo.nae:8443", want: "i-10-2-142-18.dev-notifier-ws-aldelo.nae:8443"},
		{name: "url with path", hostInfo: "https://10.1.1.1:8443/snsrelay", want: "10.1.1.1:8443"},
		{name: "empty", hostInfo: "", wantErr: true},
		{name: "missing port", hostInfo: "10.2.105.213", wantErr: true},
		{name: "url missing port", hostInfo: "https://10.1.1.1", wantErr: true},
		{name: "garbage", hostInfo: "not a host", wantErr: true},
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
// marker used by the remover's stand-down guard.
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

	model.ResetLastInstanceHealthWriteFailure()

	if _, failed := model.TimeSinceLastInstanceHealthWriteFailure(); failed {
		t.Fatal("expected no write failure recorded after second reset")
	}
}

// TestRemoveInactive_StandsDownAfterWriteFailure: guard 1 — while the data
// store recently failed an instance-health write, the sweep must not even
// query for stale records (incident 2026-11-23 regression test).
func TestRemoveInactive_StandsDownAfterWriteFailure(t *testing.T) {
	listPtr, _, _ := staleRemoverSeams(t)

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

// TestRemoveInactive_NoStandDownWithoutFailure: with no write failure
// recorded, the sweep proceeds (queries the data store) normally.
func TestRemoveInactive_NoStandDownWithoutFailure(t *testing.T) {
	listPtr, _, _ := staleRemoverSeams(t)

	queried := false
	*listPtr = func() ([]*model.HealthStatus, error) {
		queried = true
		return []*model.HealthStatus{}, nil
	}

	model.ResetLastInstanceHealthWriteFailure()

	removeInactiveInstancesFromServiceDiscovery()

	if !queried {
		t.Fatal("stale sweep did not query the data store with no write failure recorded")
	}
}

// TestRemoveInactive_FirstSightingAwaitsConfirmation: guard 2 — a record that
// is stale for the first time must not be deregistered in the same cycle.
func TestRemoveInactive_FirstSightingAwaitsConfirmation(t *testing.T) {
	listPtr, deregPtr, _ := staleRemoverSeams(t)

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

	if _, seen := staleFirstSeenLookup("inst-first-sight"); !seen {
		t.Fatal("first stale sighting was not recorded for confirmation on the next cycle")
	}
}

// TestRemoveInactive_KeepsAliveEndpointAndRefreshesRecord: guard 3 — a stale
// record whose HostInfo endpoint answers a TCP dial must be KEPT, its record
// refreshed, and no deregistration issued.
func TestRemoveInactive_KeepsAliveEndpointAndRefreshesRecord(t *testing.T) {
	listPtr, deregPtr, refreshPtr := staleRemoverSeams(t)

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

	refreshed := 0
	*refreshPtr = func(namespaceId, serviceId, instanceId, awsRegion, serviceInfo, hostInfo string) error {
		refreshed++
		return nil
	}

	// seed as confirmed-stale from a prior cycle so guard 2 passes
	updateStaleConfirmations(map[string]time.Time{
		"inst-alive": time.Now().UTC().Add(-staleConfirmWindow - time.Minute),
	})

	removeInactiveInstancesFromServiceDiscovery()

	if deregistered != 0 {
		t.Fatalf("live endpoint was deregistered (%d calls) — liveness guard did not engage", deregistered)
	}

	if refreshed != 1 {
		t.Fatalf("expected record refresh for live endpoint, got %d calls", refreshed)
	}

	if _, stillTracked := staleFirstSeenLookup("inst-alive"); stillTracked {
		t.Fatal("kept live instance remains in stale-confirmation state")
	}
}

// TestRemoveInactive_DeregistersConfirmedUnreachable: the happy path still
// works — a confirmed-stale record whose endpoint is unreachable is
// deregistered.
func TestRemoveInactive_DeregistersConfirmedUnreachable(t *testing.T) {
	listPtr, deregPtr, refreshPtr := staleRemoverSeams(t)

	*listPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{newStaleRow("inst-dead", "127.0.0.1:1")}, nil
	}

	deregistered := 0
	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		deregistered++
		return nil
	}

	refreshed := 0
	*refreshPtr = func(namespaceId, serviceId, instanceId, awsRegion, serviceInfo, hostInfo string) error {
		refreshed++
		return nil
	}

	updateStaleConfirmations(map[string]time.Time{
		"inst-dead": time.Now().UTC().Add(-staleConfirmWindow - time.Minute),
	})

	removeInactiveInstancesFromServiceDiscovery()

	if deregistered != 1 {
		t.Fatalf("confirmed unreachable instance was not deregistered (got %d calls)", deregistered)
	}

	if refreshed != 0 {
		t.Fatalf("record refresh must not run for unreachable endpoint (got %d calls)", refreshed)
	}
}

// TestRemoveInactive_PerCycleCap: guard 4 — when more instances are confirmed
// stale than the per-cycle cap, only the first cap are deregistered.
func TestRemoveInactive_PerCycleCap(t *testing.T) {
	listPtr, deregPtr, _ := staleRemoverSeams(t)

	rows := []*model.HealthStatus{
		newStaleRow("inst-1", "127.0.0.1:1"),
		newStaleRow("inst-2", "127.0.0.1:1"),
		newStaleRow("inst-3", "127.0.0.1:1"),
		newStaleRow("inst-4", "127.0.0.1:1"),
		newStaleRow("inst-5", "127.0.0.1:1"),
	}

	*listPtr = func() ([]*model.HealthStatus, error) {
		return rows, nil
	}

	var deregistered []string
	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		deregistered = append(deregistered, instanceId)
		return nil
	}

	seed := map[string]time.Time{}
	old := time.Now().UTC().Add(-staleConfirmWindow - time.Minute)
	for _, r := range rows {
		seed[r.InstanceId] = old
	}
	updateStaleConfirmations(seed)

	removeInactiveInstancesFromServiceDiscovery()

	if len(deregistered) != maxDeregistersPerCycle {
		t.Fatalf("expected per-cycle cap of %d deregistrations, got %d", maxDeregistersPerCycle, len(deregistered))
	}

	for i, id := range deregistered {
		if id != rows[i].InstanceId {
			t.Fatalf("deregistration order not preserved (oldest-first): got %s at position %d, want %s", id, i, rows[i].InstanceId)
		}
	}
}

// TestRemoveInactive_ZeroFoundClearsConfirmations: an empty stale result must
// reset the confirmation state so ids from a past anomaly do not linger.
func TestRemoveInactive_ZeroFoundClearsConfirmations(t *testing.T) {
	listPtr, _, _ := staleRemoverSeams(t)

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
	listPtr, deregPtr, _ := staleRemoverSeams(t)

	*listPtr = func() ([]*model.HealthStatus, error) {
		return []*model.HealthStatus{newStaleRow("inst-garbage", "not a host")}, nil
	}

	deregistered := 0
	*deregPtr = func(namespaceId, serviceId, instanceId, awsRegion string) error {
		deregistered++
		return nil
	}

	updateStaleConfirmations(map[string]time.Time{
		"inst-garbage": time.Now().UTC().Add(-staleConfirmWindow - time.Minute),
	})

	removeInactiveInstancesFromServiceDiscovery()

	if deregistered != 1 {
		t.Fatalf("unparseable HostInfo with confirmed-stale age was not deregistered (got %d calls)", deregistered)
	}
}

// TestStandDownWindowMatchesStaleThreshold: the stand-down window must be at
// least the effective stale threshold plus one max heartbeat interval, so any
// live service gets a full heartbeat chance to re-write its record.
func TestStandDownWindowMatchesStaleThreshold(t *testing.T) {
	effective := time.Duration(model.EffectiveHealthReportRecordStaleMinutes()) * time.Minute
	standDown := effective + maxHealthReportInterval

	if standDown <= effective {
		t.Fatalf("stand-down window (%s) must exceed the stale threshold (%s)", standDown, effective)
	}

	if maxHealthReportInterval < 120*time.Second {
		t.Fatalf("maxHealthReportInterval (%s) must cover the default 120s service heartbeat", maxHealthReportInterval)
	}
}
