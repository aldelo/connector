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
	"context"
	"sync/atomic"
	"testing"
	"time"

	testpb "github.com/aldelo/connector/example/proto/test"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"log"
)

// -------- CL-F3 _lifecycleMu tests --------
// CL-F3 adds a per-Client mutex that serializes Dial and Close. Pre-fix,
// a concurrent Close arriving after Dial reset closed=false but before
// state init completed would leave the client "closed" while still
// holding freshly-initialized state (connection leak + caller confusion).
// These tests pin the serialization invariant: Close must wait for an
// in-progress Dial to finish, and a duplicate Close must NOT wait on
// _lifecycleMu because its closing-CAS fast path runs before the lock.

// TestCL_F3_CloseSerializesAgainstDialHoldingLifecycleMu holds
// _lifecycleMu directly (simulating Dial in progress), launches a
// concurrent Close, and verifies that Close blocks until the lock is
// released. This is the core regression test for the observable
// serialization contract.
func TestCL_F3_CloseSerializesAgainstDialHoldingLifecycleMu(t *testing.T) {
	c := &Client{}

	// Simulate Dial acquiring _lifecycleMu and beginning state init.
	c._lifecycleMu.Lock()

	closeReturned := make(chan struct{})
	go func() {
		defer close(closeReturned)
		c.Close()
	}()

	// Close must block while "Dial" holds the lock. Give it 150ms to
	// attempt the lock acquire + block.
	select {
	case <-closeReturned:
		t.Fatal("Close returned while _lifecycleMu was held — not serialized against Dial")
	case <-time.After(150 * time.Millisecond):
		// good: Close is blocked on the lock
	}

	// Release "Dial's" lock; Close must proceed and return promptly.
	c._lifecycleMu.Unlock()
	select {
	case <-closeReturned:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2s after _lifecycleMu release")
	}

	// After Close, closed flag should be set (proves Close actually ran
	// the body, not just the fast-path exit).
	if !c.closed.Load() {
		t.Error("Close ran but did not set closed=true; lock path may have exited early")
	}
}

// TestCL_F3_DuplicateCloseFastPathDoesNotBlockOnLifecycleMu verifies
// that a second concurrent Close returns immediately via its closing-CAS
// fast path instead of queueing on _lifecycleMu. This is important:
// panicked / repeated Close calls must not pile up behind an in-flight
// one, or a shutdown storm would serialize the entire shutdown sequence.
func TestCL_F3_DuplicateCloseFastPathDoesNotBlockOnLifecycleMu(t *testing.T) {
	c := &Client{}

	// Pre-set closing=true so the next Close hits the CAS-fail fast path.
	c.closing.Store(true)

	// Hold _lifecycleMu so a NEW caller who goes past the fast path
	// would get wedged. The test proves the fast path runs FIRST.
	c._lifecycleMu.Lock()
	defer c._lifecycleMu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Close() // must return immediately via CAS-fail path
	}()

	select {
	case <-done:
		// good: duplicate Close exited without touching the held lock
	case <-time.After(200 * time.Millisecond):
		t.Fatal("duplicate Close blocked on _lifecycleMu — fast path is broken")
	}
}

// TestCL_F3_NilClientCloseDoesNotTouchLifecycleMu verifies the nil
// receiver path in Close returns immediately without attempting to
// lock anything. If Close unconditionally acquired _lifecycleMu, a
// nil Client call would panic on the lock access.
func TestCL_F3_NilClientCloseDoesNotTouchLifecycleMu(t *testing.T) {
	var c *Client

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil Client Close panicked: %v", r)
		}
	}()

	c.Close() // must be a no-op, not a panic
}

// TestClient_Dial is an integration test that requires:
//   - A populated client.yaml with real Target.ServiceName / NamespaceName / Region
//   - A reachable AWS Cloud Map namespace for service discovery
//   - A running gRPC AnswerService at the discovered endpoint
//
// It is skipped under `go test -short` (and therefore in CI) because the
// default client.yaml has empty discovery fields and no server is running.
// Run manually with: `go test -run TestClient_Dial ./client/` after
// configuring client.yaml and starting the example server. P2-16.
func TestClient_Dial(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: requires configured client.yaml + running gRPC AnswerService")
	}

	cli := NewClient("testclient", "client", "")

	// cli.StatsHandler

	cli.BeforeClientDial = func(cli *Client) {
		log.Println("In - Before Client Dial")
	}

	cli.AfterClientDial = func(cli *Client) {
		log.Println("In - After Client Dial")
	}

	cli.BeforeClientClose = func(cli *Client) {
		log.Println("In - Before Client Close")
	}

	cli.AfterClientClose = func(cli *Client) {
		log.Println("In - After Client Close")
	}

	if err := cli.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}

	defer cli.Close()

	a := testpb.NewAnswerServiceClient(cli._conn)

	for i := 0; i < 1000; i++ {
		var header metadata.MD

		if result, e := a.Greeting(context.Background(), &testpb.Question{Question: "What's for dinner?"}, grpc.Header(&header)); e != nil {
			t.Error(e)
		} else {
			log.Println("Answer = " + result.Answer + ", From = " + header.Get("server")[0])
		}
	}

	if status, err := cli.HealthProbe(""); err != nil {
		log.Println("Health Probe Failed: " + err.Error())
	} else {
		log.Println(status.String())
	}
}

// -------- CL-F1 watchdog helper tests --------
// closeOnClosed bridges Client.Close()'s closedCh into an in-flight
// notifier Subscribe by calling a cancel func when closedCh fires first,
// and exiting cleanly when the local doneCh fires first. See client.go
// CL-F1 fix for context.

func TestCloseOnClosed_FiresCancelOnClosedCh(t *testing.T) {
	closedCh := make(chan struct{})
	doneCh := make(chan struct{})
	var called atomic.Int32
	fired := make(chan struct{})

	go func() {
		closeOnClosed(closedCh, doneCh, func() {
			called.Add(1)
			close(fired)
		})
	}()

	// Simulate Client.Close() firing the closedCh.
	close(closedCh)

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelFn not called within 2s after closedCh fired")
	}
	if got := called.Load(); got != 1 {
		t.Errorf("cancelFn called %d times, want 1", got)
	}
}

func TestCloseOnClosed_ExitsOnDone(t *testing.T) {
	closedCh := make(chan struct{})
	doneCh := make(chan struct{})
	var called atomic.Int32
	exited := make(chan struct{})

	go func() {
		defer close(exited)
		closeOnClosed(closedCh, doneCh, func() { called.Add(1) })
	}()

	// Simulate the Subscribe call returning normally: close doneCh
	// before closedCh ever fires.
	close(doneCh)

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("closeOnClosed did not exit within 2s after doneCh fired")
	}
	if got := called.Load(); got != 0 {
		t.Errorf("cancelFn called %d times on done-path, want 0", got)
	}
}

func TestCloseOnClosed_NilCancelFn(t *testing.T) {
	closedCh := make(chan struct{})
	doneCh := make(chan struct{})
	exited := make(chan struct{})

	go func() {
		defer close(exited)
		// nil cancelFn must not panic when closedCh fires.
		closeOnClosed(closedCh, doneCh, nil)
	}()

	close(closedCh)

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("closeOnClosed did not exit within 2s with nil cancelFn")
	}
}

func TestCloseOnClosed_ClosedChWinsRace(t *testing.T) {
	// If closedCh is already closed at entry, cancelFn must fire
	// (Go select picks a ready case; with both ready either can win,
	// but when only closedCh is ready at entry the result is
	// deterministic: cancelFn fires). This mirrors the realistic case
	// where Client.Close() has already run by the time
	// DoNotifierAlertService reaches the watchdog spawn point.
	closedCh := make(chan struct{})
	close(closedCh)
	doneCh := make(chan struct{})
	var called atomic.Int32
	exited := make(chan struct{})

	go func() {
		defer close(exited)
		closeOnClosed(closedCh, doneCh, func() { called.Add(1) })
	}()

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("closeOnClosed did not exit within 2s")
	}
	if got := called.Load(); got != 1 {
		t.Errorf("cancelFn called %d times, want 1", got)
	}
}
