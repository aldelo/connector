package service

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 */

// P1-CONN-SVC-02 regression — Serve's startServer error branch must
// leave the Service in a state where a subsequent GracefulStop /
// ImmediateStop cannot deadlock on a never-closed quitDone channel.
//
// Pre-fix bug: Serve allocated _quit/_quitDone under _mu, then called
// startServer which may return an error BEFORE the quit-handler
// goroutine is installed. The error branch returned without touching
// the quit primitives, leaving _quit/_quitDone non-nil but with no
// goroutine that would ever close quitDone. A later GracefulStop read
// the non-nil fields under RLock, entered the unified-routing path at
// service.go ~L2721, and blocked forever on `<-quitDone`.
//
// Fix: the startServer error branch now closes quitDone itself and
// nils _quit/_quitDone under _mu, so later stop callers take the
// pre-Serve legacy safety-net path instead of the unified path.
//
// ── SP-010 pass-5 A2-F-2B rewrite (2026-04-15) ────────────────────
//
// The original regression tests in this file inlined the fix block
// (close(quitDone); s._quit = nil; s._quitDone = nil) into their own
// setup and then asserted that GracefulStop / ImmediateStop returned
// promptly. Mutation-deleting the fix at service.go:2219-2234 left
// both tests GREEN because the tests themselves were driving the
// post-fix state they purported to verify — a tautology.
//
// The rewritten tests below drive real Serve() end-to-end by injecting
// a synthetic startServer failure via the package-level startServerFn
// indirection. Serve proceeds normally through readConfig, setupServer,
// connectSd, autoCreateService, allocates _quit/_quitDone under _mu,
// then calls startServerFn which returns the injected error. The only
// code path that can transform "_quitDone open" into "_quitDone closed"
// between Serve returning and GracefulStop running is the fix block at
// service.go:2219-2234. If the fix block is deleted, GracefulStop
// enters unified routing, parks on `<-quitDone`, and the 2-second
// deadline fires.
//
// Mutation probe validation (run by the PR author, documented in the
// commit message):
//   1. Delete `close(quitDone)` from service.go:2219-2234
//   2. `go test -run TestService_Serve_StartServerError ./service/...`
//   3. Expect: tests turn RED (GracefulStop/ImmediateStop deadlock)
//   4. Restore the fix
//   5. Rerun: expect GREEN
//
// See lesson L18 for the causal-path-vs-postcondition distinction.

import (
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// startServerFnTestMu serializes the global startServerFn override
// across the Serve-error tests so go test -count=N or parallel
// dispatch within this file cannot race on the package-level var.
//
// A2-P6-05 (2026-04-16): Fragility documentation.
//
// What it guards: the package-level `startServerFn` variable, which is
// the test-injection seam for Serve()'s startServer call. Any test that
// overrides startServerFn MUST hold this mutex for the entire duration
// of the override (including the Serve() call and any assertions that
// depend on the injected behavior).
//
// Why it exists: startServerFn is a package-level var, not a per-Service
// field. Two tests overriding it concurrently would race on the write
// and could restore the wrong original value. The mutex makes the
// override-use-restore cycle atomic across tests.
//
// Rule for future tests: any new test that touches startServerFn MUST
// use withInjectedStartServerFailure (below) or acquire startServerFnTestMu
// directly. Do NOT call t.Parallel() on any test that holds this mutex —
// the lock serializes against other startServerFn tests in the same binary,
// but t.Parallel() would allow other non-mutex tests to interleave, which
// is fine, EXCEPT if those other tests also touch startServerFn without
// the mutex. Grep for `startServerFn` before adding t.Parallel() to any
// test in this file.
var startServerFnTestMu sync.Mutex

// newServeErrorTestService builds a Service that is able to reach the
// Serve() startServer call — it loads the existing service.yaml fixture
// (empty namespace makes connectSd + autoCreateService no-ops), binds an
// ephemeral TCP listener via port 0, and registers an empty gRPC handler
// set so setupServer accepts the configuration.
func newServeErrorTestService() *Service {
	return NewService("test-connector-service", "service", "", func(grpcServer *grpc.Server) {})
}

// withInjectedStartServerFailure overrides startServerFn for the
// duration of fn, restoring the original method-value on return even
// if fn panics. Serializes on startServerFnTestMu so two tests that
// both inject a failure cannot race each other.
func withInjectedStartServerFailure(t *testing.T, injected error, fn func()) {
	t.Helper()
	startServerFnTestMu.Lock()
	defer startServerFnTestMu.Unlock()

	orig := startServerFn
	startServerFn = func(s *Service, lis net.Listener, quit chan bool, quitDone chan struct{}) error {
		return injected
	}
	defer func() { startServerFn = orig }()
	fn()
}

// TestService_Serve_StartServerError_GracefulStopDoesNotDeadlock is
// the real-causal-path rewrite of the old tautological regression
// test. It drives Serve() all the way to the startServer call, forces
// startServer to return a synthetic failure, verifies Serve propagates
// the error, and asserts that a subsequent GracefulStop returns within
// 2 seconds — which it can only do if the P1-CONN-SVC-02 fix block at
// service.go:2219-2234 closed quitDone and nilled the fields.
//
// Mutation probe (executed manually, documented in commit message):
// reverting the fix block makes GracefulStop park on `<-quitDone` and
// this test turns RED on the 2-second deadline.
func TestService_Serve_StartServerError_GracefulStopDoesNotDeadlock(t *testing.T) {
	injected := errors.New("A2-F-2B injected startServer failure")

	withInjectedStartServerFailure(t, injected, func() {
		svc := newServeErrorTestService()

		serveErr := svc.Serve()
		if serveErr == nil || !strings.Contains(serveErr.Error(), "A2-F-2B injected") {
			t.Fatalf("expected Serve to return the injected startServer error, got: %v", serveErr)
		}

		// The P1-CONN-SVC-02 fix must have closed quitDone and nilled
		// _quit/_quitDone. GracefulStop should observe the nil fields
		// via _mu RLock, fall through to the pre-Serve legacy safety
		// net, and return promptly. If the fix were reverted, the
		// fields would be non-nil and GracefulStop would enter the
		// unified-routing branch and park on `<-quitDone` forever.
		done := make(chan struct{})
		go func() {
			svc.GracefulStop()
			close(done)
		}()

		select {
		case <-done:
			// pass
		case <-time.After(2 * time.Second):
			t.Fatal("P1-CONN-SVC-02 regression: GracefulStop deadlocked after Serve's startServer-error path (A2-F-2B causal test)")
		}
	})
}

// TestService_Serve_StartServerError_ImmediateStopDoesNotDeadlock is
// the mirror assertion for ImmediateStop, which uses the same
// _quit/_quitDone observation and would exhibit the same deadlock if
// the fix block were reverted.
func TestService_Serve_StartServerError_ImmediateStopDoesNotDeadlock(t *testing.T) {
	injected := errors.New("A2-F-2B injected startServer failure (immediate)")

	withInjectedStartServerFailure(t, injected, func() {
		svc := newServeErrorTestService()

		serveErr := svc.Serve()
		if serveErr == nil || !strings.Contains(serveErr.Error(), "A2-F-2B injected") {
			t.Fatalf("expected Serve to return the injected startServer error, got: %v", serveErr)
		}

		done := make(chan struct{})
		go func() {
			svc.ImmediateStop()
			close(done)
		}()

		select {
		case <-done:
			// pass
		case <-time.After(2 * time.Second):
			t.Fatal("P1-CONN-SVC-02 regression: ImmediateStop deadlocked after Serve's startServer-error path (A2-F-2B causal test)")
		}
	})
}
