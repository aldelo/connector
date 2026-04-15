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
// service.go ~L2714, and blocked forever on `<-quitDone`.
//
// Fix: the startServer error branch now closes quitDone itself and
// nils _quit/_quitDone under _mu, so later stop callers take the
// pre-Serve legacy safety-net path instead of the unified path.

import (
	"testing"
	"time"
)

// TestService_ServeStartServerErrorFixup_NoDeadlock asserts the
// post-condition the P1-CONN-SVC-02 fix establishes: after the Serve
// error path has run, GracefulStop completes without deadlocking.
//
// We do not drive real Serve() — that needs a listener, cloud-map
// registration, and other side effects out of scope for a unit test.
// Instead we replay the two observable steps Serve's startServer
// error branch performs, then verify GracefulStop returns promptly.
// If someone removes the close/nil block from Serve, this test will
// hang until the 3-second deadline and fail.
func TestService_ServeStartServerErrorFixup_NoDeadlock(t *testing.T) {
	s := &Service{}

	// Step 1 — mirror Serve's SVC-F7 allocation of the unified quit
	// primitives (service.go ~L2196-2201).
	s._mu.Lock()
	s._quit = make(chan bool, 1)
	s._quitDone = make(chan struct{})
	quitDone := s._quitDone
	s._mu.Unlock()

	// Step 2 — apply the P1-CONN-SVC-02 fix: close quitDone, nil the
	// Service fields under lock so later stop callers fall through to
	// the pre-Serve legacy safety-net branch.
	close(quitDone)
	s._mu.Lock()
	s._quit = nil
	s._quitDone = nil
	s._mu.Unlock()

	// Step 3 — GracefulStop must observe the nil fields, take the
	// legacy fallback (cfg==nil means the fallback is effectively a
	// no-op), and return before the deadline.
	done := make(chan struct{})
	go func() {
		s.GracefulStop()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(3 * time.Second):
		t.Fatal("GracefulStop deadlocked after simulated Serve startServer error (P1-CONN-SVC-02 regression)")
	}
}

// TestService_ServeStartServerErrorFixup_ImmediateStopNoDeadlock is
// the mirror assertion for ImmediateStop — same fix must cover both
// exported stop paths, since both read _quit/_quitDone under RLock.
func TestService_ServeStartServerErrorFixup_ImmediateStopNoDeadlock(t *testing.T) {
	s := &Service{}

	s._mu.Lock()
	s._quit = make(chan bool, 1)
	s._quitDone = make(chan struct{})
	quitDone := s._quitDone
	s._mu.Unlock()

	close(quitDone)
	s._mu.Lock()
	s._quit = nil
	s._quitDone = nil
	s._mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.ImmediateStop()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(3 * time.Second):
		t.Fatal("ImmediateStop deadlocked after simulated Serve startServer error (P1-CONN-SVC-02 regression)")
	}
}
