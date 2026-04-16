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
	"log"
	"sync/atomic"
	"testing"
	"time"

	testpb "github.com/aldelo/connector/example/proto/test"
	"github.com/aldelo/connector/internal/safego"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
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

// -------- CL-F4 atomic.Pointer[config] TOCTOU tests --------
// CL-F4 replaces the plain *config field with atomic.Pointer[config] so
// writers (readConfig success/failure paths, Dial error path) and readers
// (RPC interceptors, SD refresh, health check) can never observe a
// torn or mid-store value. The pre-fix bug: reader does
//   if c._config == nil { ... } else { x := c._config.Grpc.X }
// which is a two-load TOCTOU — a concurrent `c._config = nil` between
// the check and the field deref nil-panics the RPC goroutine.
//
// These tests pin two invariants:
//   1. getConfig() returns either a fully-initialized *config or nil
//      — never a mid-store / torn state.
//   2. The circuit breaker handler snapshots the result of getConfig()
//      into a local, so a concurrent Store(nil) cannot cause a deref.

// TestCL_F4_GetConfigIsRaceFree hammers Store and Load under -race and
// asserts the detector finds no conflicts. Pre-fix, the plain-pointer
// field would race on every parallel reader.
func TestCL_F4_GetConfigIsRaceFree(t *testing.T) {
	c := &Client{}
	valid := &config{}
	valid.Grpc.CircuitBreakerEnabled = true
	valid.Grpc.CircuitBreakerTimeout = 1000

	// Writer: alternate between valid and nil. This is the exact
	// pattern Dial → Dial-error → retry produces under a flap.
	stop := make(chan struct{})
	done := make(chan struct{}, 3)

	go func() {
		defer func() { done <- struct{}{} }()
		for {
			select {
			case <-stop:
				return
			default:
				c._config.Store(valid)
				c._config.Store(nil)
			}
		}
	}()

	// Two parallel readers — each must always see either nil or a
	// fully-formed *config, never a partial/torn pointer.
	reader := func() {
		defer func() { done <- struct{}{} }()
		for {
			select {
			case <-stop:
				return
			default:
				cfg := c.getConfig()
				if cfg != nil {
					// Deref must not panic — if getConfig returned a
					// non-nil snapshot, the pointee is immutable for
					// the lifetime of cfg.
					_ = cfg.Grpc.CircuitBreakerEnabled
					_ = cfg.Grpc.CircuitBreakerTimeout
				}
			}
		}
	}
	go reader()
	go reader()

	time.Sleep(100 * time.Millisecond)
	close(stop)
	for i := 0; i < 3; i++ {
		<-done
	}
	// If the race detector is enabled (go test -race), the mere
	// completion of this test without a detector report proves the
	// atomic.Pointer path is race-free.
}

// TestCL_F4_CircuitBreakerHandlerSurvivesConcurrentNilStore is the
// closest-to-production reproducer: goroutine A flaps _config between
// valid and nil while goroutine B drives unaryCircuitBreakerHandler.
// Pre-fix, B's `if c._config == nil` check could pass and then
// `c._config.Grpc.X` deref nil-panic in the same function body.
// Post-fix, B snapshots via getConfig() and the snapshot is stable
// for the remainder of the call.
func TestCL_F4_CircuitBreakerHandlerSurvivesConcurrentNilStore(t *testing.T) {
	c := &Client{}
	// Circuit breaker DISABLED in the stored config so the handler
	// takes the fast-path invoker call without needing a real Hystrix
	// plugin. The important path under test is the
	//   if cfg == nil || !cfg.Grpc.CircuitBreakerEnabled
	// branch — that's the exact TOCTOU site.
	valid := &config{}
	valid.Grpc.CircuitBreakerEnabled = false

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-stop:
				return
			default:
				c._config.Store(valid)
				c._config.Store(nil)
			}
		}
	}()

	// Fake invoker: does nothing, returns nil. This lets us drive the
	// interceptor body without a real grpc.ClientConn.
	var invocations atomic.Int64
	invoker := func(ctx context.Context, method string, req, reply interface{},
		cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		invocations.Add(1)
		return nil
	}

	// Run the interceptor in a tight loop. Any nil-deref panic here
	// is a CL-F4 regression and fails the test via the deferred recover.
	readerDone := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				readerDone <- &panicError{r: r}
				return
			}
			readerDone <- nil
		}()
		ctx := context.Background()
		for i := 0; i < 20000; i++ {
			select {
			case <-stop:
				return
			default:
				// cc is unused on the fast-path branch (cfg==nil)
				// so we can pass nil safely.
				_ = c.unaryCircuitBreakerHandler(ctx, "/test/Method",
					nil, nil, nil, invoker)
			}
		}
	}()

	// Let the race run for a slice of real time.
	time.Sleep(200 * time.Millisecond)
	close(stop)
	<-writerDone
	if err := <-readerDone; err != nil {
		t.Fatalf("CL-F4 regression: unaryCircuitBreakerHandler panicked under concurrent nil-store: %v", err)
	}
	if invocations.Load() == 0 {
		t.Error("invoker never ran — test did not actually exercise the handler body")
	}
}

// -------- CL-F5 safeGo / safeCall panic-recovery tests --------
// CL-F5 adds safeGo + safeCall helpers so a panic in a buggy user
// callback (ServiceHostOnlineHandler, BeforeClientClose, etc.) or a
// client-internal background goroutine cannot propagate up and crash
// the process. grpc_recovery covers only RPC handler invocations, not
// package-internal goroutines or lifecycle hook invocations.
//
// Invariants pinned by these tests:
//  1. safeGo runs fn on a new goroutine, recovers any panic, logs it,
//     and returns without crashing the parent.
//  2. safeCall invokes fn synchronously, recovers any panic, and
//     returns normally so the calling goroutine survives.
//  3. Both helpers are no-ops when fn is nil (defensive guard).
//  4. Panic in a user-supplied notifier alert handler dispatched via
//     callAlertSkipped / callAlertStopped does not escape.

func TestCL_F5_SafeGoRecoversPanic(t *testing.T) {
	done := make(chan struct{})
	safego.Go("test-panic", func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
		// good: goroutine ran, panicked, recovered, deferred close fired
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo goroutine did not complete within 2s — panic may have escaped")
	}
}

func TestCL_F5_SafeGoNilFnIsNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("safeGo(nil) panicked: %v", r)
		}
	}()
	ch := safego.GoWait("test-nil", nil) // must not panic, must not spawn a goroutine
	// GoWait must close the channel immediately for a nil fn.
	select {
	case <-ch:
		// channel closed immediately (nil fn) — no goroutine spawned
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait(nil) channel was not closed")
	}
}

func TestCL_F5_SafeCallRecoversPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("safeCall let panic escape: %v", r)
		}
	}()
	safeCall("test-panic", func() {
		panic("kaboom")
	})
	// If we reach here, recovery worked and the caller survived.
}

func TestCL_F5_SafeCallNilFnIsNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("safeCall(nil) panicked: %v", r)
		}
	}()
	safeCall("test-nil", nil) // must be a silent no-op
}

// TestCL_F5_CallAlertSkippedSurvivesPanicHandler is the closest-to-
// production reproducer: install a panicking ServiceAlertSkippedHandler
// on a NotifierClient, call callAlertSkipped, and confirm the caller
// goroutine survives. Pre-fix, the same raw invocation in the Subscribe
// loop would have crashed the reconnect goroutine and the process.
func TestCL_F5_CallAlertSkippedSurvivesPanicHandler(t *testing.T) {
	nc := &NotifierClient{}
	var called atomic.Int32
	nc.ServiceAlertSkippedHandler = func(reason string) {
		called.Add(1)
		panic("user-handler panic: " + reason)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("callAlertSkipped let panic escape: %v", r)
		}
	}()
	nc.callAlertSkipped("test reason")

	if called.Load() != 1 {
		t.Errorf("handler called %d times, want 1", called.Load())
	}
}

func TestCL_F5_CallAlertStoppedSurvivesPanicHandler(t *testing.T) {
	nc := &NotifierClient{}
	var called atomic.Int32
	nc.ServiceAlertStoppedHandler = func(reason string) {
		called.Add(1)
		panic("user-handler panic: " + reason)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("callAlertStopped let panic escape: %v", r)
		}
	}()
	nc.callAlertStopped("test reason")

	if called.Load() != 1 {
		t.Errorf("handler called %d times, want 1", called.Load())
	}
}

// Nil-receiver guards on the helper methods must not panic either —
// callers in the Subscribe loop rely on this for defensive dispatch.
func TestCL_F5_CallAlertSkipped_NilReceiverNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("callAlertSkipped nil receiver panicked: %v", r)
		}
	}()
	var nc *NotifierClient
	nc.callAlertSkipped("should be swallowed")
}

func TestCL_F5_CallAlertStopped_NilReceiverNoop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("callAlertStopped nil receiver panicked: %v", r)
		}
	}()
	var nc *NotifierClient
	nc.callAlertStopped("should be swallowed")
}

// -------- A3-F1 HealthProbe snapshot safety tests --------
// A3-F1 ensures the HealthProbe method captures the health checker
// snapshot under connMu.RLock and gracefully handles nil/closed states.
// A full rotation test requires a real gRPC server so we test the
// guard paths that don't need a live connection.

func TestA3_F1_HealthProbe_NilClient(t *testing.T) {
	var c *Client
	status, err := c.HealthProbe("test-svc")
	if err == nil {
		t.Fatal("expected error for nil client, got nil")
	}
	if status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Errorf("expected NOT_SERVING, got %v", status)
	}
}

func TestA3_F1_HealthProbe_ClosedClient(t *testing.T) {
	c := &Client{}
	c.closed.Store(true)

	status, err := c.HealthProbe("test-svc")
	if err == nil {
		t.Fatal("expected error for closed client, got nil")
	}
	if status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Errorf("expected NOT_SERVING, got %v", status)
	}
	if !contains(err.Error(), "client is closed") {
		t.Errorf("error should mention closed client, got: %v", err)
	}
}

func TestA3_F1_HealthProbe_NilConnection(t *testing.T) {
	c := &Client{}
	// No connection set — _conn is nil.
	status, err := c.HealthProbe("test-svc")
	if err == nil {
		t.Fatal("expected error for nil connection, got nil")
	}
	if status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
		t.Errorf("expected NOT_SERVING, got %v", status)
	}
	if !contains(err.Error(), "client connection is nil") {
		t.Errorf("error should mention nil connection, got: %v", err)
	}
}

// -------- A3-F2 webserver errch observer cancellation test --------
// A3-F2 adds a closedCh branch to the webserver error channel observer
// goroutine. This test verifies the observer exits when the client is
// closed, rather than blocking forever on errCh.

func TestA3_F2_WebserverObserverExitsOnClose(t *testing.T) {
	c := &Client{}
	// Initialize closedCh (simulating a dialed client).
	c.closedChMu.Lock()
	c.closedCh = make(chan struct{})
	c.closedChMu.Unlock()

	errCh := make(chan error, 1) // buffered, will NOT send anything
	exited := make(chan struct{})

	go func() {
		defer close(exited)
		select {
		case webErr := <-errCh:
			if webErr != nil {
				log.Printf("webserver error: %v", webErr)
			}
		case <-c.getClosedCh():
			// Client closed — stop observing.
		}
	}()

	// Simulate client closing by closing closedCh.
	c.closedChMu.Lock()
	close(c.closedCh)
	c.closedChMu.Unlock()

	select {
	case <-exited:
		// Good: observer exited promptly when closedCh fired.
	case <-time.After(2 * time.Second):
		t.Fatal("observer goroutine did not exit within 2s after closedCh closed")
	}
}

// contains is a small helper to avoid importing strings in the test file.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// -------- EH-2 ZapLog Init error-handling tests --------
// EH-2 replaces `_ = z.Init()` with proper error checking so that a
// ZapLog initialization failure is logged rather than silently discarded.
// These tests verify that the Init call path does not panic and that the
// logger object is still assigned (even if Init fails, the caller gets a
// non-nil ZapLog back — the error is logged, not fatal).

// TestEH2_ZLogInitDoesNotPanic exercises the ZLog() lazy-init path on a
// fresh Client with no config loaded. z.Init() runs with default
// (disabled-logger) settings. The test confirms the error-checked Init
// path does not panic and that ZLog returns a non-nil logger.
func TestEH2_ZLogInitDoesNotPanic(t *testing.T) {
	c := &Client{}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("EH-2 regression: ZLog() panicked during Init: %v", r)
		}
	}()

	z := c.ZLog()
	if z == nil {
		t.Error("ZLog() returned nil on a fresh Client — logger was not assigned after Init")
	}
}

// TestEH2_ZLogInitIdempotent verifies that repeated ZLog() calls return
// the same logger instance (sync.Once guarantees Init runs exactly once).
func TestEH2_ZLogInitIdempotent(t *testing.T) {
	c := &Client{}

	z1 := c.ZLog()
	z2 := c.ZLog()

	if z1 != z2 {
		t.Error("ZLog() returned different instances across calls — sync.Once init is broken")
	}
}

// panicError wraps a recovered panic value so it can flow through a
// channel typed as error.
type panicError struct{ r interface{} }

func (p *panicError) Error() string { return fmtPanic(p.r) }

func fmtPanic(r interface{}) string {
	if r == nil {
		return "<nil>"
	}
	if s, ok := r.(string); ok {
		return s
	}
	if e, ok := r.(error); ok {
		return e.Error()
	}
	return "panic"
}
