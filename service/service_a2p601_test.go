package service

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 */

// A2-P6-01 regression — gRPC Serve failure before signal handler
// registration must still trigger a quit-channel send so the service
// exits cleanly instead of becoming a zombie process.
//
// Background
// ----------
// At service.go ~L1194, when grpcSrv.Serve(lis) returns an error, the
// code gates the self-SIGTERM on _sigHandlerReady. Before A2-P6-01,
// when _sigHandlerReady was false (gRPC fails before awaitOsSigExit
// runs signal.Notify), NO exit mechanism fired — the goroutine simply
// returned, awaitOsSigExit stayed parked forever, and the process
// became a zombie.
//
// Fix: an `else` branch on the _sigHandlerReady check performs a
// non-blocking send on the quit channel (`select { case quit <- true:
// default: }`), giving the quit handler a chance to run cleanup.
//
// Test strategy
// -------------
// Constructs a minimal Service with:
//   - _quit channel allocated (buffered 1), _quitDone allocated
//   - _sigHandlerReady = false (zero value, NOT set)
//   - _grpcServer = grpc.NewServer() (real gRPC server, no services)
//   - A pre-closed net.Listener that will make grpcSrv.Serve() fail
//     immediately with "use of closed network connection"
//
// Calls the real startServer method. The "grpc-server" goroutine
// inside startServer calls grpcSrv.Serve(closedLis), which fails.
// With _sigHandlerReady=false, the A2-P6-01 else branch sends on
// quit. A fake quit handler (same pattern as SVC-F7 tests) receives
// the send and closes _quitDone.
//
// Mutation probe (SP-010 protocol):
//   1. Delete the `else { select { case quit <- true: ... } }` branch
//      from service.go ~L1198
//   2. Run: go test -run TestGRPCServeError_PreSignalHandler ./service/...
//   3. Expected: test times out — quit channel never receives, fake
//      quit handler never runs, exited channel never closes
//   4. Restore the else branch
//   5. Rerun: GREEN

import (
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// TestGRPCServeError_PreSignalHandler_QuitsCleanly_A2P601 verifies that
// when gRPC Serve fails before the signal handler is registered
// (_sigHandlerReady=false), the service still exits cleanly via the
// quit channel fallback instead of becoming a zombie process.
func TestGRPCServeError_PreSignalHandler_QuitsCleanly_A2P601(t *testing.T) {
	// Allocate a Service with quit primitives but NO signal handler.
	// _sigHandlerReady defaults to false (atomic.Bool zero value).
	s := &Service{}
	s._mu.Lock()
	s._quit = make(chan bool, 1)
	s._quitDone = make(chan struct{})
	s._grpcServer = grpc.NewServer()
	s._config = &config{AppName: "a2p601-test"}
	s._mu.Unlock()

	// Precondition: _sigHandlerReady must be false. If it were true,
	// the self-SIGTERM path would fire instead of the fallback.
	if s._sigHandlerReady.Load() {
		t.Fatal("precondition: _sigHandlerReady must be false (A2-P6-01 setup wrong)")
	}

	// Start a fake quit handler — same pattern as SVC-F7 tests.
	// It blocks on _quit, then closes _quitDone. We use a simple
	// version here since we only care about the quit send arriving.
	exited := make(chan struct{})
	safeGo("a2p601-test-quit-handler", func() {
		defer close(exited)
		<-s._quit
		close(s._quitDone)
	})

	// Create a listener and immediately close it. When grpcSrv.Serve()
	// is called with this listener, it fails with "use of closed
	// network connection" (or similar), which triggers the error branch
	// at service.go ~L1183.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create listener: %v", err)
	}
	_ = lis.Close()

	// Call the real startServer. It launches the "grpc-server" goroutine
	// asynchronously and returns nil (the goroutine error is not propagated
	// to the startServer return value — it routes through the quit channel
	// or self-SIGTERM instead). The goroutine calls grpcSrv.Serve(closedLis),
	// which fails immediately. With _sigHandlerReady=false, the A2-P6-01
	// else branch sends on quit.
	quit := s._quit
	quitDone := s._quitDone
	_ = s.startServer(lis, quit, quitDone)

	// Primary assertion: the fake quit handler must have received the
	// quit send and exited within a bounded deadline. If the A2-P6-01
	// else branch is deleted, quit never receives, the fake handler
	// stays parked, and this times out.
	select {
	case <-exited:
		// ok — the quit-channel fallback fired, quit handler ran
	case <-time.After(3 * time.Second):
		t.Fatal("A2-P6-01 regression: quit handler did not run within 3s — " +
			"gRPC Serve failure with _sigHandlerReady=false did not send on quit channel (zombie process)")
	}

	// Clean up the gRPC server to avoid goroutine leaks.
	s._mu.RLock()
	gs := s._grpcServer
	s._mu.RUnlock()
	if gs != nil {
		gs.Stop()
	}
}
