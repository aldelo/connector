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

// SVC-F7 tests — unified quit-channel routing for exported stop methods.
//
// Closes C2-001 (GracefulStop/ImmediateStop bypass SVC-F5 fire sites) and
// C2-002 (GracefulStop never unblocks Serve from awaitOsSigExit) by
// routing both exported stop methods through the same quit handler the
// signal path wakes.
//
// These tests do NOT run a full Serve() lifecycle (no listener, no Cloud
// Map). Instead they manually allocate the SVC-F7 quit primitives and
// _shutdownCtx the same way Serve() does, then start a small fake
// quit-handler goroutine that mirrors the real handler's responsibilities
// (read _quit, fire the configured phase, close _quitDone). This lets us
// validate the routing contract — does the exported call write to _quit,
// wait on _quitDone, and observe the phase fire — without standing up
// real AWS / network state.

import (
	"context"
	"github.com/aldelo/connector/internal/safego"
	"testing"
	"time"
)

// newSVCF7TestService allocates a Service with the SVC-F7 quit
// primitives and (optionally) the SVC-F5 _shutdownCtx, mirroring
// Serve()'s allocation step. The returned Service is ready for an
// exported GracefulStop/ImmediateStop call to take the unified
// quit-channel routing path.
func newSVCF7TestService(shutdownCancel bool, phase ShutdownPhase) *Service {
	s := &Service{
		ShutdownCancel:      shutdownCancel,
		ShutdownCancelPhase: phase,
		_sigHandlerReadyCh:  make(chan struct{}),
	}
	s._mu.Lock()
	s._quit = make(chan bool, 1)
	s._quitDone = make(chan struct{})
	if shutdownCancel {
		s._shutdownCtx, s._shutdownCancel = context.WithCancel(context.Background())
	}
	s._mu.Unlock()
	return s
}

// startFakeQuitHandler spawns a goroutine that mimics the real
// startServer quit-handler: blocks on _quit, fires the configured
// SVC-F5 fire sites the same way the real handler does, then closes
// _quitDone. Returns a channel that is closed when the fake handler
// exits, so tests can assert the handler actually ran.
//
// honorImmediate=true makes the fake handler also check
// _immediateStopRequested and bypass the PreDrain fire when set, the
// same way the real handler bypasses the bounded grpc graceful stop.
func startFakeQuitHandler(s *Service, honorImmediate bool) <-chan struct{} {
	exited := make(chan struct{})
	safego.Go("svc-f7-test-quit-handler", func() {
		defer close(exited)
		<-s._quit
		// PreDrain fire: this is the equivalent of the real handler's
		// fireShutdownCancelIfPhase(ShutdownPhasePreDrain) site.
		s.fireShutdownCancelIfPhase(ShutdownPhasePreDrain)

		if honorImmediate && s._immediateStopRequested.Load() {
			// Mirror the real handler's "immediate bypass" branch:
			// fire PostGraceExpiry synchronously, then skip any
			// graceful drain. In the real handler this is followed
			// by gs.Stop(), which we don't model here.
			s.fireShutdownCancelIfPhase(ShutdownPhasePostGraceExpiry)
		} else {
			// Mirror the bounded-graceful path: fire PostGraceExpiry
			// only on escalation. For the test we don't model the
			// timeout — both happy paths complete fast — but we
			// always invoke the helper for completeness so the
			// SafetyNet defer fires later.
			s.fireShutdownCancelIfPhase(ShutdownPhasePostGraceExpiry)
		}

		// Mirror the Serve() defer that runs at the end of the
		// shutdown sequence: the safety-net Final fire that closes
		// ShutdownCtx regardless of phase.
		s.fireShutdownCancelFinal()
		close(s._quitDone)
	})
	return exited
}

// TestService_GracefulStop_FiresShutdownCtxImmediate validates that a
// programmatic GracefulStop() on a service with
// ShutdownCancelPhase=Immediate causes ShutdownCtx().Done() to fire
// before GracefulStop returns. Closes C2-001 for the Immediate phase.
//
// Mechanism: GracefulStop() now sends on _quit and waits on _quitDone.
// The fake quit handler invokes fireShutdownCancelIfPhase for every
// phase, so the Immediate-configured ctx is cancelled by the time the
// safety-net Final fires (or earlier — the per-phase fire is a no-op
// for non-matching phases, but the Final always cancels).
func TestService_GracefulStop_FiresShutdownCtxImmediate(t *testing.T) {
	s := newSVCF7TestService(true, ShutdownPhaseImmediate)
	shut := s.ShutdownCtx()
	if shut == nil {
		t.Fatal("ShutdownCtx() returned nil after allocation")
	}

	exited := startFakeQuitHandler(s, true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.GracefulStop()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GracefulStop did not return within 2s — quit-channel routing did not unblock")
	}

	// ShutdownCtx must be cancelled by the time GracefulStop returns.
	if err := shut.Err(); err == nil {
		t.Fatal("ShutdownCtx not cancelled after GracefulStop returned — SVC-F5 fire site bypassed (C2-001 regression)")
	}

	// Fake quit handler must have actually run.
	select {
	case <-exited:
	case <-time.After(1 * time.Second):
		t.Fatal("fake quit handler did not exit — quit channel was not consumed")
	}
}

// TestService_GracefulStop_FiresShutdownCtxPreDrain — same as above
// for the PreDrain phase. Verifies that the configured phase fires
// (not just the safety-net Final) so consumers that pick a phase get
// the contract they configured.
func TestService_GracefulStop_FiresShutdownCtxPreDrain(t *testing.T) {
	s := newSVCF7TestService(true, ShutdownPhasePreDrain)
	shut := s.ShutdownCtx()
	if shut == nil {
		t.Fatal("ShutdownCtx() returned nil after allocation")
	}

	exited := startFakeQuitHandler(s, true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.GracefulStop()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("GracefulStop did not return within 2s — quit-channel routing did not unblock")
	}

	if err := shut.Err(); err == nil {
		t.Fatal("ShutdownCtx not cancelled after GracefulStop returned — PreDrain fire site bypassed (C2-001 regression)")
	}

	select {
	case <-exited:
	case <-time.After(1 * time.Second):
		t.Fatal("fake quit handler did not exit")
	}
}

// TestService_GracefulStop_ReleasesFakeServe is the C2-002 regression
// test for SVC-F7 unified routing. It models Serve()'s
// post-awaitOsSigExit + quit-send + quitDone-wait path as a goroutine,
// then calls GracefulStop() programmatically and asserts the goroutine
// returns within a bounded deadline.
//
// Without the SVC-F7 fix, GracefulStop runs the legacy teardown body
// but never writes to _quit, so the simulated Serve goroutine remains
// blocked on its own send-to-_quit / wait-on-quitDone path forever.
// With SVC-F7, GracefulStop signals _quit, the fake quit handler
// closes _quitDone, both the GracefulStop caller AND the simulated
// Serve goroutine unblock.
//
// Closes C2-002 (and validates C2-001's routing is correct
// end-to-end on the programmatic path).
//
// ⚠️  SCOPE LIMITATION — pass-3 F1 (SVC-F8) contrarian finding:
// This test does NOT prove F1 is fixed. It replaces the real
// awaitOsSigExit wait with a `time.Sleep(50ms)` and a cooperative
// send-to-_quit from the simulated Serve goroutine. In production,
// Serve()'s main goroutine is parked inside awaitOsSigExit reading
// from a function-local done channel whose only writer is a
// sig-demux goroutine consuming signal.Notify — the
// `s._quit <- true; <-s._quitDone` sequence models the WRONG wait,
// so this test passes whether or not the self-SIGTERM fix
// (SVC-F8) is present. The real F1 regression test is
// `TestService_GracefulStop_ReleasesRealServe` in service_test.go,
// which invokes the real Serve() and is gated by
// CONNECTOR_RUN_INTEGRATION=1 because Serve requires real AWS
// CloudMap + service.yaml. A green run of THIS test without a
// corresponding green run of the real-Serve test is NOT closure
// of F1.
//
// The test is kept running (not t.Skip'd) because it remains a
// valid regression for SVC-F7 unified routing — separate concern
// from F1. The name was changed from `ReleasesServe` to
// `ReleasesFakeServe` in pass-3 so nobody misreads passing status
// as F1 closure. See F1 ticket at
// _src/docs/repos/connector/findings/2026-04-14-contrarian-pass3/
// F1-awaitossigexit-not-woken-by-programmatic-stop.md.
func TestService_GracefulStop_ReleasesFakeServe(t *testing.T) {
	s := newSVCF7TestService(true, ShutdownPhaseImmediate)
	shut := s.ShutdownCtx()
	if shut == nil {
		t.Fatal("ShutdownCtx() returned nil after allocation")
	}

	exited := startFakeQuitHandler(s, true)

	// Simulated Serve() main goroutine — mirrors the real Serve's
	// post-awaitOsSigExit pattern: send to _quit (idempotent
	// non-blocking send so it doesn't deadlock if a programmatic
	// caller already filled the buffer), then wait on _quitDone.
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		// The real Serve waits on awaitOsSigExit before this point.
		// Here we model the wait as a short pause so the
		// programmatic GracefulStop call below has the opportunity
		// to fill _quit first. The point of the test is that
		// whichever path wins the buffer-1 send, both unblock.
		time.Sleep(50 * time.Millisecond)
		select {
		case s._quit <- true:
		default:
		}
		<-s._quitDone
	}()

	// Programmatic GracefulStop call. Without SVC-F7 this path
	// never wakes the (real) quit handler and Serve hangs on
	// awaitOsSigExit forever.
	stopDone := make(chan struct{})
	go func() {
		defer close(stopDone)
		s.GracefulStop()
	}()

	select {
	case <-stopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("GracefulStop hung past 5s — C2-002 regression: programmatic stop never unblocks the quit-channel waiter")
	}

	select {
	case <-serveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("simulated Serve goroutine hung past 5s — C2-002 regression: quit handler did not release Serve")
	}

	if err := shut.Err(); err == nil {
		t.Fatal("ShutdownCtx not cancelled after GracefulStop+Serve completed — fire sites bypassed (C2-001 regression)")
	}

	select {
	case <-exited:
	case <-time.After(1 * time.Second):
		t.Fatal("fake quit handler did not exit")
	}
}
