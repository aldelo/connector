package service

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

// SVC-F2 coverage extension tests — closes C2-003 (runHook coverage gap).
//
// The original SVC-F2 fix wrapped the four named Before*/After*
// lifecycle hooks (BeforeServerStart, AfterServerStart,
// BeforeServerShutdown, AfterServerShutdown) in panic recovery via the
// runHook helper. Two additional user-supplied callback sites were
// outside that recovery:
//
//   1. RegisterServiceHandlers (called from setupServer at the
//      start of Serve). A panic here crashed the test binary instead
//      of failing Serve cleanly with an error.
//   2. WebServerConfig.CleanUp from the GracefulStop / ImmediateStop
//      pre-Serve fallback paths. A panic skipped the rest of
//      teardown and left the Service half-disconnected.
//
// These tests validate the new wrappers (runRegisterHandlers,
// runCleanup) close the gap.

import (
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

// TestService_RegisterServiceHandlers_PanicReturnsError verifies that
// runRegisterHandlers wraps a panicking registrar in a Go error
// instead of letting the panic propagate. This is the mechanism that
// keeps a buggy registrar from crashing Serve / setupServer.
func TestService_RegisterServiceHandlers_PanicReturnsError(t *testing.T) {
	s := &Service{}
	// Allocate a real *grpc.Server so the registrar argument is a
	// valid value the user code can dispatch on. The panic injection
	// happens inside the user closure, not on the server pointer.
	s._grpcServer = grpc.NewServer()

	panicker := func(_ *grpc.Server) {
		panic("simulated registrar panic")
	}

	err := s.runRegisterHandlers(panicker)
	if err == nil {
		t.Fatal("runRegisterHandlers returned nil for panicking registrar — panic was not recovered")
	}
	if !strings.Contains(err.Error(), "RegisterServiceHandlers panic") {
		t.Errorf("error message does not identify the panic source: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "simulated registrar panic") {
		t.Errorf("error message does not include the panic value: %q", err.Error())
	}
}

// TestService_RegisterServiceHandlers_RuntimeErrorPanic verifies the
// recovery handles runtime errors (nil pointer deref, etc.), not just
// string panics. This is the realistic failure mode: a typed-nil
// generated registrar that dispatches into a nil method table.
func TestService_RegisterServiceHandlers_RuntimeErrorPanic(t *testing.T) {
	s := &Service{}
	s._grpcServer = grpc.NewServer()

	panicker := func(_ *grpc.Server) {
		var p *struct{ Field int }
		_ = p.Field // nil pointer dereference -> runtime.Error
	}

	err := s.runRegisterHandlers(panicker)
	if err == nil {
		t.Fatal("runRegisterHandlers returned nil for nil-deref panic — runtime error was not recovered")
	}
	if !strings.Contains(err.Error(), "RegisterServiceHandlers panic") {
		t.Errorf("error message does not identify the panic source: %q", err.Error())
	}
}

// TestService_RegisterServiceHandlers_NilRegistrarReturnsNil verifies
// the helper handles the nil case gracefully — the canonical "missing
// registrar" check in setupServer reports a different error message,
// so the helper itself just no-ops on nil.
func TestService_RegisterServiceHandlers_NilRegistrarReturnsNil(t *testing.T) {
	s := &Service{}
	if err := s.runRegisterHandlers(nil); err != nil {
		t.Errorf("runRegisterHandlers(nil) = %v, want nil", err)
	}
}

// TestService_GracefulStop_CleanUpPanicRecovered exercises the
// pre-Serve fallback path of GracefulStop with a panicking CleanUp
// callback. Pre-fix this would crash the test binary; post-fix the
// runCleanup wrapper recovers the panic and the rest of the legacy
// fallback teardown runs to completion (we observe this by checking
// that GracefulStop returns at all).
func TestService_GracefulStop_CleanUpPanicRecovered(t *testing.T) {
	cleanupCalled := false
	s := &Service{
		WebServerConfig: &WebServerConfig{
			CleanUp: func() {
				cleanupCalled = true
				panic("simulated CleanUp panic")
			},
		},
	}

	// No Serve() call → _quit / _quitDone are nil → GracefulStop
	// takes the pre-Serve fallback path that calls runCleanup.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("GracefulStop propagated CleanUp panic instead of recovering: %v", r)
		}
	}()

	s.GracefulStop()

	if !cleanupCalled {
		t.Error("CleanUp callback was not invoked — pre-Serve fallback did not run runCleanup")
	}
}

// TestService_ImmediateStop_CleanUpPanicRecovered — same as above for
// ImmediateStop's pre-Serve fallback path.
func TestService_ImmediateStop_CleanUpPanicRecovered(t *testing.T) {
	cleanupCalled := false
	s := &Service{
		WebServerConfig: &WebServerConfig{
			CleanUp: func() {
				cleanupCalled = true
				panic("simulated CleanUp panic")
			},
		},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ImmediateStop propagated CleanUp panic instead of recovering: %v", r)
		}
	}()

	s.ImmediateStop()

	if !cleanupCalled {
		t.Error("CleanUp callback was not invoked — pre-Serve fallback did not run runCleanup")
	}
}

// TestRunCleanup_NilNoOp / TestRunCleanup_NormalReturns / TestRunCleanup_PanicRecovered
// pin the runCleanup helper contract directly so future regressions
// are caught at the helper boundary, not just at the call sites.
func TestRunCleanup_NilNoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("runCleanup(nil) panicked: %v", r)
		}
	}()
	runCleanup("nil-test", nil)
}

func TestRunCleanup_NormalReturns(t *testing.T) {
	called := false
	runCleanup("normal", func() { called = true })
	if !called {
		t.Error("runCleanup did not invoke the callback")
	}
}

func TestRunCleanup_PanicRecovered(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("runCleanup did not recover panic: %v", r)
		}
	}()
	runCleanup("panic-test", func() {
		panic("boom")
	})
}

// TestRunCleanup_QuitHandlerLabelIsolatesPanic pins the F2 pass-3
// contract: the in-Serve quit handler's WebServerConfig.CleanUp call
// must be routed through runCleanup so a panic in user-supplied
// CleanUp code does NOT unwind the quit-handler frame. Before the F2
// pass-3 fix, the quit-handler call site at service.go:1415 invoked
// s.WebServerConfig.CleanUp() directly; a panic there propagated
// through the safeGo recovery boundary and skipped every subsequent
// teardown step (setLocalAddress, setServing(false), Cloud Map
// deregister, SD/SNS/SQS Disconnect, fireShutdownCancel
// PreDrain/PostGraceExpiry, gs.GracefulStop/gs.Stop, lis.Close).
// safeGo kept the process alive but the per-step teardown invariants
// were silently violated.
//
// Behavioral pin: invoke runCleanup with the exact label string the
// F2 fix uses at the quit-handler call site, assert a panic recovers
// cleanly, and assert a post-cleanup sentinel still runs. If a future
// refactor extracts or renames this label, update this test AND the
// source-contract test below together.
func TestRunCleanup_QuitHandlerLabelIsolatesPanic(t *testing.T) {
	const quitHandlerLabel = "WebServerConfig.CleanUp(quit-handler)"

	cleanupEntered := false
	postCleanupRan := false

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("runCleanup propagated panic past its frame: %v", r)
			}
		}()
		runCleanup(quitHandlerLabel, func() {
			cleanupEntered = true
			panic("simulated WebServerConfig.CleanUp panic inside quit handler")
		})
		// Sentinel: this line represents the "next teardown step"
		// in the quit handler (setLocalAddress, setServing(false),
		// deregister, etc.). If runCleanup did its job, this runs.
		postCleanupRan = true
	}()

	if !cleanupEntered {
		t.Error("CleanUp callback did not run — runCleanup did not invoke it")
	}
	if !postCleanupRan {
		t.Fatal("code after runCleanup did not execute — F2 pass-3 regression: panic unwound the quit-handler frame")
	}
}

// TestService_QuitHandler_CleanUpWrapSourceContract is a source-file
// assertion that pins the F2 pass-3 fix in service.go. The real quit
// handler lives inside startServer() and is impractical to exercise
// in a unit test (requires full Service setup, real listener, Cloud
// Map, etc.), so we assert the source contains the expected
// runCleanup wrap at the quit-handler call site. If someone reverts
// the fix to a raw `s.WebServerConfig.CleanUp()` call, this test
// fires.
//
// This is not a substitute for the behavioral test above — it
// catches structural regressions that behavioral tests would miss
// because the regression replaces the wrap with a syntactically
// similar raw call that still compiles but bypasses the panic
// recovery.
func TestService_QuitHandler_CleanUpWrapSourceContract(t *testing.T) {
	data, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("cannot read service.go for source-contract check: %v", err)
	}
	src := string(data)

	// Must contain the exact runCleanup wrap with the quit-handler
	// label. This is the F2 pass-3 fix. Matching on the label
	// ensures we're pinned to the *quit-handler* call site, not the
	// pre-Serve fallback path which uses a different label.
	const expected = `runCleanup("WebServerConfig.CleanUp(quit-handler)", s.WebServerConfig.CleanUp)`
	if !strings.Contains(src, expected) {
		t.Fatalf("F2 pass-3 regression: service.go does not contain the expected quit-handler runCleanup wrap.\nExpected substring: %q\n"+
			"This means the quit handler's WebServerConfig.CleanUp call is NOT routed through runCleanup, and a panic in user-supplied CleanUp code will unwind the quit-handler frame through safeGo — skipping every subsequent teardown step (setLocalAddress, setServing(false), deregister, disconnect, gs.Stop, lis.Close).\nSee _src/docs/repos/connector/findings/2026-04-14-contrarian-pass3/F2-quit-handler-cleanup-not-wrapped-in-runcleanup.md",
			expected)
	}

	// Must NOT contain the raw pre-fix call form in the same
	// region. We scan for the specific unwrapped pattern that was
	// the bug. This is a best-effort belt-and-suspenders check;
	// false positives are possible if a future unrelated site adds
	// a similar call, but that would surface as a fix-required
	// decision rather than a silent regression.
	const forbidden = "s.WebServerConfig.CleanUp()"
	if strings.Contains(src, forbidden) {
		// Only fail if the forbidden raw call appears OUTSIDE a
		// runCleanup wrap. Since runCleanup's 2nd argument IS
		// s.WebServerConfig.CleanUp (not CleanUp()), the wrapped
		// form reads `s.WebServerConfig.CleanUp` without parens.
		// Any occurrence of `s.WebServerConfig.CleanUp()` (with
		// parens) is a raw call.
		t.Fatalf("F2 pass-3 regression: service.go contains a raw call %q. Raw CleanUp calls must be replaced with runCleanup(\"<label>\", s.WebServerConfig.CleanUp) so panics are recovered.",
			forbidden)
	}
}
