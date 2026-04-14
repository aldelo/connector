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
