package service

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 */

// P2-CONN-CI-01 remediation — non-gated regression coverage for the
// SVC-F8 self-signal lifecycle path.
//
// Background
// ----------
// Pass-3 F1 (self-delivered SIGTERM must be gated on a readiness flag
// set AFTER signal.Notify) and its fix SVC-F8 are only meaningfully
// exercised by TestService_GracefulStop_ReleasesRealServe in
// service_test.go — and that test is gated behind
// CONNECTOR_RUN_INTEGRATION=1 because it calls the real Serve(), which
// requires real AWS CloudMap + service.yaml to pass startup validation.
//
// The non-gated TestService_GracefulStop_ReleasesFakeServe in
// service_svc_f7_test.go explicitly documents (lines 199-222) that it
// replaces awaitOsSigExit with a time.Sleep + cooperative _quit send,
// and therefore passes whether or not SVC-F8 is present. Default CI
// has no signal-path regression guard at all.
//
// Pass-4 P2-CONN-CI-01 (deep-review-2026-04-15-contrarian-pass4 §4
// findings table L148) captured this gap: "default CI cannot detect
// SVC-F8 regression. The only non-env-gated lifecycle tests drive
// _quit cooperatively and bypass awaitOsSigExit entirely".
//
// What this test does
// -------------------
// Drives awaitOsSigExit() directly from the test goroutine — no
// Serve(), no CloudMap, no listener. awaitOsSigExit is AWS-free: its
// only dependencies are signal.Notify (process-level, but no external
// state), a local sigs/done channel pair, and a read of
// _config.SvcCreateData.HealthFailThreshold (nil-guarded here by
// assigning a zero-value &config{}).
//
// The test then observes _sigHandlerReady becoming true (meaning
// signal.Notify is live) and calls GracefulStop — which must traverse
// the SVC-F8 self-signal branch, deliver SIGTERM to the test process
// pid, have that signal routed to awaitOsSigExit's local sigs channel
// by the Go runtime's signal.Notify wiring (NOT the default handler,
// which would terminate the test binary), wake the sig-demux
// goroutine, close done, and let awaitOsSigExit return.
//
// Without SVC-F8: _sigHandlerReady never becomes true, GracefulStop
// skips the self-signal entirely, awaitOsSigExit stays parked
// forever, the test blows its 5s deadline.
//
// With a regressed SVC-F8 (e.g., readiness flag removed, self-signal
// un-gated, or Store(true) moved BEFORE signal.Notify): the self-
// signal reaches the Go runtime default handler and terminates the
// test binary — the entire `go test` run dies with "signal:
// terminated", which is a *harder* failure than a test assertion
// and therefore catches the regression faster.
//
// Test safety
// -----------
// 1. signal.Notify is process-wide. This is the ONLY test in the
//    connector service package that installs a signal handler on a
//    non-integration path, so it cannot conflict with other tests in
//    the same binary. awaitOsSigExit calls signal.Stop at exit, so
//    the handler is cleaned up even on normal completion.
// 2. A 5s deadline with a goroutine-leak check ensures the test fails
//    loudly rather than hanging CI if the SVC-F8 path is broken.
// 3. The test does NOT call t.Parallel() — signal state is process-
//    wide and must not race with any hypothetical future signal test.
// 4. The self-SIGTERM is only ever sent AFTER the test has verified
//    _sigHandlerReady is true, mirroring the production contract
//    GracefulStop implements. There is no path where SIGTERM can
//    reach the Go runtime default handler from this test.

import (
	"syscall"
	"testing"
	"time"
)

// waitForSigHandlerReady blocks until the Service's _sigHandlerReadyCh
// is closed (signalling that awaitOsSigExit has called signal.Notify
// and set _sigHandlerReady=true), or the timeout expires.
//
// REM-4 (2026-04-16): Replaced the sleep-based poll loop with a
// channel-based select. The production code closes _sigHandlerReadyCh
// immediately after _sigHandlerReady.Store(true) in awaitOsSigExit,
// providing a deterministic synchronization point. The time.After
// fallback is kept as a safety net — if awaitOsSigExit never
// progresses past its preamble, the test fails with a clear message
// rather than hanging.
func waitForSigHandlerReady(t *testing.T, s *Service, timeout time.Duration) {
	t.Helper()
	select {
	case <-s._sigHandlerReadyCh:
		// Channel closed — awaitOsSigExit has completed signal.Notify
		// registration and set _sigHandlerReady=true.
	case <-time.After(timeout):
		t.Fatalf("SVC-F8 regression: awaitOsSigExit did not set _sigHandlerReady within %s — signal.Notify registration stage did not complete", timeout)
	}
}

// TestService_AwaitOsSigExit_WokenByGracefulStopSelfSignal is the
// P2-CONN-CI-01 non-gated regression test for SVC-F8. It drives the
// real awaitOsSigExit lifecycle — including the real signal.Notify
// registration and real SIGTERM delivery to the test pid — without
// any AWS dependency.
//
// Failure modes this test catches that the SVC-F7 FakeServe test
// cannot:
//   - SVC-F8 self-signal removed or gated on the wrong condition
//     → awaitOsSigExit never wakes, GracefulStop blocks forever,
//       test fails on the 5s deadline.
//   - _sigHandlerReady.Store(true) moved BEFORE signal.Notify
//     → the self-signal hits the Go runtime default handler and
//       terminates the test binary (`go test` exits with "signal:
//       terminated").
//   - signal.Stop removed from awaitOsSigExit
//     → _sigHandlerReady cleanup check at the end fails; also
//       contaminates subsequent tests in the binary.
//   - SIGTERM sent to the wrong pid (e.g., getppid instead of
//     getpid) → awaitOsSigExit never wakes, 5s deadline fires.
func TestService_AwaitOsSigExit_WokenByGracefulStopSelfSignal(t *testing.T) {
	// Reuse the SVC-F7 helper to allocate _quit/_quitDone/_shutdownCtx
	// the same way Serve() does. Pass shutdownCancel=false because
	// this test does not assert on ShutdownCtx semantics — only on
	// the self-signal wake-up path.
	s := newSVCF7TestService(false, ShutdownPhaseImmediate)

	// awaitOsSigExit reads s._config.SvcCreateData.HealthFailThreshold
	// inside an RLock at line ~1719 for a startup log message. A zero-
	// value config satisfies this without any real service.yaml load.
	s._mu.Lock()
	s._config = &config{}
	s._mu.Unlock()

	// Start the fake quit handler — same pattern as the existing
	// SVC-F7 tests. This runs the unified quit-handler body (fire
	// shutdown cancel phases, close _quitDone) when GracefulStop
	// sends on _quit.
	exited := startFakeQuitHandler(s, true)

	// Run the real awaitOsSigExit in a goroutine. This installs
	// signal.Notify on the process, sets _sigHandlerReady=true, and
	// blocks on the sig-demux done channel.
	awaitDone := make(chan struct{})
	go func() {
		defer close(awaitDone)
		s.awaitOsSigExit()
	}()

	// Wait until awaitOsSigExit has finished its preamble (signal
	// handler installed + readiness flag set). Only after this is
	// it safe for GracefulStop to deliver the self-SIGTERM — which
	// is exactly the contract SVC-F8 enforces.
	waitForSigHandlerReady(t, s, 2*time.Second)

	// Call GracefulStop from a separate goroutine. In the SVC-F8-
	// correct path this will:
	//   1. Observe _sigHandlerReady=true under RLock
	//   2. Send SIGTERM to os.Getpid()
	//   3. signal.Notify routes SIGTERM to awaitOsSigExit's local
	//      sigs channel (NOT the Go runtime default handler)
	//   4. sig-demux goroutine writes done<-true
	//   5. awaitOsSigExit calls signal.Stop, clears _sigHandlerReady,
	//      and returns
	//   6. In parallel, GracefulStop sends on _quit, the fake quit
	//      handler closes _quitDone, GracefulStop unblocks
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		s.GracefulStop()
	}()

	// Primary SVC-F8 assertion: GracefulStop must return within a
	// bounded deadline. Production GracefulStop returns in ~1s
	// after self-signal; 5s is a generous CI margin.
	select {
	case <-stopDone:
		// ok — GracefulStop unblocked within deadline
	case <-time.After(5 * time.Second):
		t.Fatal("SVC-F8 regression: GracefulStop did not return within 5s — self-SIGTERM did not wake awaitOsSigExit (or readiness gate blocked the self-signal)")
	}

	// Secondary assertion: the awaitOsSigExit goroutine must also
	// have returned. This catches the specific regression where
	// GracefulStop's legacy fallback unblocks the caller but leaves
	// awaitOsSigExit parked — a silent goroutine leak that a real
	// Serve() would expose as a hung process, but which a Fake-Serve
	// test would miss entirely.
	select {
	case <-awaitDone:
		// ok — awaitOsSigExit returned, which means sig-demux
		// received the signal and closed done
	case <-time.After(1 * time.Second):
		t.Fatal("SVC-F8 regression: awaitOsSigExit goroutine did not return — sig-demux did not observe self-SIGTERM, or signal.Notify was not actually registered")
	}

	// Tertiary assertion: the fake quit handler must have run. This
	// confirms the unified SVC-F7 routing path is also intact — the
	// _quit send reached the handler and _quitDone was closed.
	select {
	case <-exited:
		// ok
	case <-time.After(1 * time.Second):
		t.Fatal("unified quit handler did not run — _quit send from GracefulStop was lost")
	}

	// Post-condition: _sigHandlerReady must be false after
	// awaitOsSigExit returned, confirming signal.Stop + cleanup
	// ran to completion. A lingering true value would leak the
	// signal handler and potentially affect subsequent tests.
	if s._sigHandlerReady.Load() {
		t.Fatal("SVC-F8 regression: _sigHandlerReady is still true after awaitOsSigExit returned — signal.Stop cleanup was not reached")
	}
}

// TestService_AwaitOsSigExit_WokenByImmediateStopSelfSignal mirrors the
// above test for ImmediateStop. Both exported stop methods traverse
// the same SVC-F8 self-signal branch, and both must be covered by
// default-CI regression tests because a regression in either one
// would ship undetected if only GracefulStop were tested.
//
// The divergence from the GracefulStop test is in the quit-handler
// behavior: ImmediateStop sets _immediateStopRequested=true before
// the _quit send, which causes the fake quit handler's
// honorImmediate=true branch to fire PostGraceExpiry synchronously.
// The assertion surface — self-signal delivery, awaitOsSigExit
// wake-up, quit handler ran, readiness flag cleared — is identical.
func TestService_AwaitOsSigExit_WokenByImmediateStopSelfSignal(t *testing.T) {
	s := newSVCF7TestService(false, ShutdownPhaseImmediate)

	s._mu.Lock()
	s._config = &config{}
	s._mu.Unlock()

	exited := startFakeQuitHandler(s, true)

	awaitDone := make(chan struct{})
	go func() {
		defer close(awaitDone)
		s.awaitOsSigExit()
	}()

	waitForSigHandlerReady(t, s, 2*time.Second)

	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		s.ImmediateStop()
	}()

	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("SVC-F8 regression: ImmediateStop did not return within 5s — self-SIGTERM did not wake awaitOsSigExit")
	}

	select {
	case <-awaitDone:
	case <-time.After(1 * time.Second):
		t.Fatal("SVC-F8 regression: awaitOsSigExit goroutine did not return after ImmediateStop — sig-demux did not observe self-SIGTERM")
	}

	select {
	case <-exited:
	case <-time.After(1 * time.Second):
		t.Fatal("unified quit handler did not run — _quit send from ImmediateStop was lost")
	}

	if s._sigHandlerReady.Load() {
		t.Fatal("SVC-F8 regression: _sigHandlerReady is still true after awaitOsSigExit returned")
	}

	// ImmediateStop-specific post-condition: the immediate-request
	// atomic must be true (the stop actually traversed the immediate
	// code path, not the legacy fallback).
	if !s._immediateStopRequested.Load() {
		t.Fatal("ImmediateStop did not set _immediateStopRequested — legacy fallback taken instead of unified SVC-F7 routing")
	}
}


// TestSigHandlerReadyChannel_ClosedAfterNotify validates that the
// REM-4 channel-based readiness mechanism fires correctly: start
// awaitOsSigExit in a goroutine, wait on the _sigHandlerReadyCh
// channel, and verify it closes before the timeout. This is the
// direct test for the channel — distinct from the existing tests
// which use the channel indirectly via waitForSigHandlerReady.
//
// The test also confirms the atomic.Bool and channel are consistent:
// once the channel fires, _sigHandlerReady.Load() must return true.
func TestSigHandlerReadyChannel_ClosedAfterNotify(t *testing.T) {
	s := newSVCF7TestService(false, ShutdownPhaseImmediate)

	s._mu.Lock()
	s._config = &config{}
	s._mu.Unlock()

	exited := startFakeQuitHandler(s, true)

	awaitDone := make(chan struct{})
	go func() {
		defer close(awaitDone)
		s.awaitOsSigExit()
	}()

	// Primary assertion: the readiness channel fires within a
	// bounded deadline. This is the REM-4 deterministic wait — no
	// polling, no sleep intervals, pure channel synchronization.
	select {
	case <-s._sigHandlerReadyCh:
		// Channel closed — signal.Notify registration completed.
	case <-time.After(2 * time.Second):
		t.Fatal("REM-4 regression: _sigHandlerReadyCh was not closed within 2s — awaitOsSigExit did not progress past signal.Notify registration")
	}

	// Secondary: the atomic.Bool must be consistent with the channel.
	// The channel is closed immediately after Store(true), so they
	// must agree.
	if !s._sigHandlerReady.Load() {
		t.Fatal("REM-4 regression: _sigHandlerReadyCh was closed but _sigHandlerReady is false — Store(true) and close(ch) are out of order")
	}

	// Clean up: stop the service so awaitOsSigExit returns cleanly.
	go s.GracefulStop()

	select {
	case <-awaitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup: awaitOsSigExit did not return within 5s after GracefulStop")
	}

	select {
	case <-exited:
	case <-time.After(1 * time.Second):
		t.Fatal("cleanup: fake quit handler did not run")
	}
}

// silenceUnusedSyscallImport keeps the `syscall` import honest. The
// test file does not directly reference syscall constants — the
// self-signal is delivered by production code inside GracefulStop /
// ImmediateStop — but keeping the import documents that SIGTERM is
// the signal under test and lets a future maintainer grep for
// `syscall.SIGTERM` and land in this file.
var _ = syscall.SIGTERM

// -----------------------------------------------------------------------
// SP-010 pass-5 A2-F-2A — sender-side readiness gate regression tests
// -----------------------------------------------------------------------
//
// Motivation: the two tests above (WokenByGracefulStopSelfSignal /
// WokenByImmediateStopSelfSignal) exercise the HAPPY path — awaitOsSigExit
// is running, _sigHandlerReady is true, self-signal fires and reaches
// the registered Go channel. That covers the receiver-side contract.
//
// A2-F-2A identifies the missing companion: the SENDER-side gate check
// at service.go L2760 (GracefulStop) and L2923 (ImmediateStop) — the
// `if s._sigHandlerReady.Load()` wrapper. If a future refactor removes
// that wrapper, the fleet-wide behavior change is:
//
//   Before Serve() is called (or after Serve() returns), a programmatic
//   GracefulStop / ImmediateStop would emit SIGTERM to the process PID.
//   With NO signal.Notify handler registered, the Go runtime routes
//   that SIGTERM to the default handler, which terminates the process.
//
// The existing tests CANNOT catch this regression: they install the
// signal handler first, so the self-signal always reaches the Go
// channel regardless of whether the sender-side gate is in place.
//
// These tests close the asymmetry by driving the OTHER code path:
// call Stop WITHOUT running awaitOsSigExit, prove the process is
// still alive afterward. If the gate is removed, the test binary
// dies with "signal: terminated" and the `go test` run fails loudly.
//
// The assertions are intentionally minimal — the primary correctness
// signal is "did the test process survive" (implicit: the test
// function returns normally). The supporting assertions confirm
// the _quit handler still ran so we know we actually exercised the
// stop path and didn't just silently no-op.
//
// Mutation-probe record (SP-010 protocol):
//   Probe: remove the `if s._sigHandlerReady.Load()` wrapper from
//   service.go L2760 (GracefulStop). Run
//   TestService_GracefulStop_BeforeAwaitOsSigExit_NoSelfSignal_A2F2A.
//   The test binary dies with "signal: terminated" before the test
//   even reports a result — `go test` exits with a non-zero status
//   and the failure is visible in CI. Restored: GREEN.

// TestService_GracefulStop_BeforeAwaitOsSigExit_NoSelfSignal_A2F2A
// asserts that GracefulStop is safe to call on a Service whose
// awaitOsSigExit preamble has NOT run (common in unit tests and in
// any code path that performs partial Serve setup before bailing).
// The readiness gate at service.go L2760 must short-circuit the
// self-signal; the unified quit path must still run.
func TestService_GracefulStop_BeforeAwaitOsSigExit_NoSelfSignal_A2F2A(t *testing.T) {
	s := newSVCF7TestService(false, ShutdownPhaseImmediate)
	s._mu.Lock()
	s._config = &config{}
	s._mu.Unlock()

	// Precondition: no awaitOsSigExit → no signal.Notify → readiness
	// flag is false. If this were true, the test would not be
	// exercising the sender-side gate branch.
	if s._sigHandlerReady.Load() {
		t.Fatal("precondition: _sigHandlerReady must be false before awaitOsSigExit runs (A2-F-2A setup wrong)")
	}

	exited := startFakeQuitHandler(s, true)

	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		// If the sender-side gate at L2760 is removed, this
		// line's self-SIGTERM hits the Go runtime default handler
		// and terminates the test binary before stopDone is ever
		// closed. `go test` then reports "signal: terminated".
		s.GracefulStop()
	}()

	// Primary assertion: GracefulStop returns within a bounded
	// deadline. Because no self-signal is sent (gate holds false),
	// the stop routes through the _quit-send branch only, which
	// the fake quit handler consumes immediately.
	select {
	case <-stopDone:
		// ok — process survived, GracefulStop returned cleanly
	case <-time.After(5 * time.Second):
		t.Fatal("A2-F-2A regression: GracefulStop did not return within 5s with readiness=false — the _quit-send-only path is broken")
	}

	// Secondary: the _quit handler must have run. This proves we
	// actually traversed the quit-routing branch — if we took some
	// legacy pre-Serve fallback path instead, the fake handler
	// would never see a _quit send.
	select {
	case <-exited:
	case <-time.After(1 * time.Second):
		t.Fatal("A2-F-2A regression: fake quit handler did not run — _quit send was lost on the readiness=false path")
	}

	// Tertiary: the readiness flag must STILL be false. Nothing in
	// this path should have set it to true (only awaitOsSigExit
	// sets it). If it flipped to true, some other code path is
	// scribbling on the signal handler — a fresh regression.
	if s._sigHandlerReady.Load() {
		t.Fatal("A2-F-2A regression: _sigHandlerReady flipped to true without awaitOsSigExit running — signal handler side effect leaked")
	}
}

// TestService_ImmediateStop_BeforeAwaitOsSigExit_NoSelfSignal_A2F2A
// is the ImmediateStop twin of the above. Both exported stop methods
// have their own self-signal site (GracefulStop L2760, ImmediateStop
// L2923) and both need independent sender-side gate coverage — a
// regression on only ImmediateStop would ship if only GracefulStop
// were tested.
func TestService_ImmediateStop_BeforeAwaitOsSigExit_NoSelfSignal_A2F2A(t *testing.T) {
	s := newSVCF7TestService(false, ShutdownPhaseImmediate)
	s._mu.Lock()
	s._config = &config{}
	s._mu.Unlock()

	if s._sigHandlerReady.Load() {
		t.Fatal("precondition: _sigHandlerReady must be false before awaitOsSigExit runs (A2-F-2A setup wrong)")
	}

	exited := startFakeQuitHandler(s, true)

	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		s.ImmediateStop()
	}()

	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("A2-F-2A regression: ImmediateStop did not return within 5s with readiness=false — the _quit-send-only path is broken")
	}

	select {
	case <-exited:
	case <-time.After(1 * time.Second):
		t.Fatal("A2-F-2A regression: fake quit handler did not run — _quit send was lost on the readiness=false path")
	}

	if s._sigHandlerReady.Load() {
		t.Fatal("A2-F-2A regression: _sigHandlerReady flipped to true without awaitOsSigExit running")
	}

	// ImmediateStop-specific post-condition.
	if !s._immediateStopRequested.Load() {
		t.Fatal("A2-F-2A regression: ImmediateStop did not set _immediateStopRequested — legacy fallback taken instead of unified SVC-F7 routing")
	}
}
