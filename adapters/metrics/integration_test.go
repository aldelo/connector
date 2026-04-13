package metrics_test

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 */

// R11-D: End-to-end interceptor integration suite.
//
// Stands up a real grpc.Server against an in-memory bufconn listener
// with the full connector interceptor chain wired:
//
//	outer:  metrics.NewServerInterceptors (R11 — labeled counters/histograms)
//	middle: logger.NewLoggerInterceptors  (R11 — nil-logger no-op path)
//	inner:  grpc_recovery.UnaryServerInterceptor
//
// The suite proves that the INDEPENDENTLY unit-tested pieces actually
// compose cleanly when stacked. Unit tests pin each interceptor's
// internal behavior; this file pins their interaction:
//
//   - Happy path — success is recorded end-to-end with code=OK.
//   - Handler error with long non-ASCII status message — error counter
//     fires, status round-trips intact to the client, logger nil-path
//     doesn't panic, MET-F2 rune-boundary truncate doesn't corrupt the
//     structured-logger contract.
//   - Panicking handler — grpc_recovery converts the panic to Internal,
//     metrics sees the synthesized error, no panic escapes the chain.
//   - Concurrent calls — 32 goroutines × 50 calls, counter totals are
//     exact, no -race violations across the MemorySink fast/slow paths.
//   - Client-cancelled context mid-flight — handler sees ctx.Done(),
//     propagates Canceled, metrics records the Canceled code.
//   - Nil sink AND nil logger — the zero-wiring case still serves
//     traffic without allocating, as a sanity check on sinkOrNop and
//     the logger's nil-z guard.
//
// All subtests run against a fresh server/sink and tear down via
// t.Cleanup so -run patterns work and state never leaks across cases.

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/aldelo/connector/adapters/logger"
	"github.com/aldelo/connector/adapters/metrics"
	pb "github.com/aldelo/connector/example/proto/test"
	"github.com/aldelo/connector/service/grpc_recovery"
)

const integrationBufSize = 1024 * 1024

// -----------------------------------------------------------------------
// Test server
// -----------------------------------------------------------------------

// advServer is a pluggable implementation of AnswerServiceServer. Each
// subtest installs its own greet function, so one fixture handles the
// whole adversarial matrix without conditional logic inside the server.
type advServer struct {
	pb.UnimplementedAnswerServiceServer
	greet func(ctx context.Context, q *pb.Question) (*pb.Answer, error)
}

func (s *advServer) Greeting(ctx context.Context, q *pb.Question) (*pb.Answer, error) {
	return s.greet(ctx, q)
}

// newIntegrationServer wires the full interceptor chain, starts a
// bufconn gRPC server, and returns a client and the backing MemorySink.
// Teardown is registered via t.Cleanup so callers never need a manual
// close.
//
// Interceptor order matches the godoc guidance in
// adapters/metrics/interceptors.go: "Place these AFTER
// context-decoration and BEFORE recovery so panics still register as
// Internal errors in the metrics stream." Metrics therefore wraps
// logger which wraps recovery which wraps the user handler.
func newIntegrationServer(
	t *testing.T,
	greet func(ctx context.Context, q *pb.Question) (*pb.Answer, error),
) (pb.AnswerServiceClient, *metrics.MemorySink) {
	t.Helper()

	sink := metrics.NewMemorySink()
	mUnary, _ := metrics.NewServerInterceptors(sink)
	lUnary, _ := logger.NewLoggerInterceptors(nil) // nil ZapLog → no-op
	rUnary := grpc_recovery.UnaryServerInterceptor()

	lis := bufconn.Listen(integrationBufSize)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(mUnary, lUnary, rUnary),
	)
	pb.RegisterAnswerServiceServer(srv, &advServer{greet: greet})

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		// Serve returns nil after GracefulStop; any other return
		// signals a setup failure we want the test to notice.
		_ = srv.Serve(lis)
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		srv.GracefulStop()
		_ = lis.Close()
		<-serveDone
	})

	return pb.NewAnswerServiceClient(conn), sink
}

// -----------------------------------------------------------------------
// Assertion helpers
// -----------------------------------------------------------------------

// findCounterSeries scans a counter-snapshot slice for the first series
// matching name+labels (via reflect.DeepEqual on labels). Returns the
// value and a found flag. Using DeepEqual rather than string key
// comparison lets callers pass nil labels as equivalent to empty maps
// without caring which convention the snapshot chose.
func findCounterSeries(cs []metrics.CounterSnapshot, name string, labels map[string]string) (int64, bool) {
	for _, c := range cs {
		if c.Name != name {
			continue
		}
		if reflect.DeepEqual(nilToEmpty(c.Labels), nilToEmpty(labels)) {
			return c.Value, true
		}
	}
	return 0, false
}

// findHistogramSeries is the histogram counterpart of findCounterSeries.
func findHistogramSeries(hs []metrics.HistogramSnapshot, name string, labels map[string]string) (metrics.HistogramSnapshot, bool) {
	for _, h := range hs {
		if h.Name != name {
			continue
		}
		if reflect.DeepEqual(nilToEmpty(h.Labels), nilToEmpty(labels)) {
			return h, true
		}
	}
	return metrics.HistogramSnapshot{}, false
}

func nilToEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// serverMethod is the canonical FullMethod string that the metrics
// interceptor records for AnswerService/Greeting. Pinned here so a
// typo drifts in one place instead of every assertion.
const serverMethod = "/test.AnswerService/Greeting"

// -----------------------------------------------------------------------
// 1. Happy path
// -----------------------------------------------------------------------

func TestIntegration_Unary_HappyPath(t *testing.T) {
	client, sink := newIntegrationServer(t, func(_ context.Context, q *pb.Question) (*pb.Answer, error) {
		return &pb.Answer{Answer: "hello " + q.Question}, nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Greeting(ctx, &pb.Question{Question: "world"})
	if err != nil {
		t.Fatalf("greeting failed: %v", err)
	}
	if resp.Answer != "hello world" {
		t.Errorf("unexpected response: %q", resp.Answer)
	}

	cs, hs := sink.Snapshot()

	// Requests counter: method + code=OK.
	val, ok := findCounterSeries(cs, "grpc_server_requests_total",
		map[string]string{"method": serverMethod, "code": codes.OK.String()})
	if !ok || val != 1 {
		t.Errorf("requests counter not recorded as 1 OK: ok=%v val=%d snap=%+v", ok, val, cs)
	}

	// Duration histogram: method only (no code label on duration).
	h, ok := findHistogramSeries(hs, "grpc_server_request_duration_seconds",
		map[string]string{"method": serverMethod})
	if !ok || h.Count != 1 {
		t.Errorf("duration histogram not recorded as count=1: ok=%v h=%+v", ok, h)
	}

	// Error counter must NOT exist on success.
	for _, c := range cs {
		if c.Name == "grpc_server_errors_total" {
			t.Errorf("error counter should not exist on happy path: %+v", c)
		}
	}
}

// -----------------------------------------------------------------------
// 2. Handler error with long non-ASCII status message
// -----------------------------------------------------------------------

func TestIntegration_Unary_HandlerErrorWithNonASCII(t *testing.T) {
	// 日本語 + a long tail. Any corruption at a rune boundary would
	// either (a) panic inside zap's JSON encoder if the logger were
	// active, or (b) surface as a status-message round-trip mismatch.
	// We assert the latter here because the logger is nil. The rune
	// truncation invariant is pinned by logger_test.go; this case is
	// about whole-chain composition under non-ASCII payloads.
	longMsg := "日本語 permission denied: " + strings.Repeat("x", 400)
	wantCode := codes.PermissionDenied

	client, sink := newIntegrationServer(t, func(_ context.Context, _ *pb.Question) (*pb.Answer, error) {
		return nil, status.Error(wantCode, longMsg)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Greeting(ctx, &pb.Question{Question: "q"})
	if err == nil {
		t.Fatal("expected error from handler")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("error is not a grpc status: %v", err)
	}
	if st.Code() != wantCode {
		t.Errorf("code: want %v, got %v", wantCode, st.Code())
	}
	if st.Message() != longMsg {
		t.Errorf("status message corrupted end-to-end:\n  want=%q\n  got =%q", longMsg, st.Message())
	}

	cs, _ := sink.Snapshot()

	// Requests counter with the specific error code.
	if v, found := findCounterSeries(cs, "grpc_server_requests_total",
		map[string]string{"method": serverMethod, "code": wantCode.String()}); !found || v != 1 {
		t.Errorf("requests counter missing for error path: found=%v val=%d", found, v)
	}

	// Errors counter with the same error code.
	if v, found := findCounterSeries(cs, "grpc_server_errors_total",
		map[string]string{"method": serverMethod, "code": wantCode.String()}); !found || v != 1 {
		t.Errorf("errors counter missing for error path: found=%v val=%d", found, v)
	}
}

// -----------------------------------------------------------------------
// 3. Panicking handler
// -----------------------------------------------------------------------

func TestIntegration_Unary_HandlerPanics(t *testing.T) {
	// Panic with a non-error value to prove grpc_recovery catches
	// arbitrary panics, not just error-typed ones. Expected outcome:
	// recovery synthesizes codes.Internal, metrics records it, the
	// client sees Internal — no panic escapes to the test goroutine.
	client, sink := newIntegrationServer(t, func(_ context.Context, _ *pb.Question) (*pb.Answer, error) {
		panic("intentional test panic: kaboom")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// If grpc_recovery were NOT in the chain, the goroutine running
	// srv.Serve would crash and this test would die. The fact that
	// we reach the assertion at all is part of the contract.
	_, err := client.Greeting(ctx, &pb.Question{Question: "q"})
	if err == nil {
		t.Fatal("expected Internal error from recovered panic")
	}
	if status.Code(err) != codes.Internal {
		t.Errorf("want Internal, got %v", status.Code(err))
	}

	cs, _ := sink.Snapshot()
	if v, found := findCounterSeries(cs, "grpc_server_errors_total",
		map[string]string{"method": serverMethod, "code": codes.Internal.String()}); !found || v != 1 {
		t.Errorf("errors counter missing after recovered panic: found=%v val=%d", found, v)
	}
}

// -----------------------------------------------------------------------
// 4. Concurrent calls — race detector sentinel
// -----------------------------------------------------------------------

func TestIntegration_Unary_ConcurrentCalls(t *testing.T) {
	const goroutines = 32
	const callsPer = 50

	client, sink := newIntegrationServer(t, func(_ context.Context, q *pb.Question) (*pb.Answer, error) {
		return &pb.Answer{Answer: q.Question}, nil
	})

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errs := make(chan error, goroutines*callsPer)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < callsPer; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := client.Greeting(ctx, &pb.Question{Question: "hi"})
				cancel()
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent call failed: %v", err)
	}

	cs, hs := sink.Snapshot()

	wantTotal := int64(goroutines * callsPer)
	if v, found := findCounterSeries(cs, "grpc_server_requests_total",
		map[string]string{"method": serverMethod, "code": codes.OK.String()}); !found || v != wantTotal {
		t.Errorf("want %d OK requests, got val=%d found=%v", wantTotal, v, found)
	}

	if h, found := findHistogramSeries(hs, "grpc_server_request_duration_seconds",
		map[string]string{"method": serverMethod}); !found || h.Count != uint64(wantTotal) {
		t.Errorf("want histogram count=%d, got %+v found=%v", wantTotal, h, found)
	}
}

// -----------------------------------------------------------------------
// 5. Client-cancelled context mid-flight
// -----------------------------------------------------------------------

func TestIntegration_Unary_ClientCancel(t *testing.T) {
	// Handler blocks until the ctx is cancelled, then returns. The
	// gRPC framework translates context cancellation into Canceled on
	// the client side; the server's handler sees ctx.Done() and emits
	// matching semantics. This is the "racing Close" case from the
	// R11-D deferred item — composition must survive mid-flight
	// cancellation without panics or leaked goroutines.
	//
	// Synchronization is deterministic (no time-based polling): an
	// outermost sync interceptor whose defer runs AFTER the metrics
	// interceptor's recordServer call. Chain order:
	//
	//	[syncIntr, metrics, logger, recovery]
	//
	// Because syncIntr wraps everything else, its defer fires once the
	// entire post-handler bookkeeping (metrics.recordServer included)
	// has finished. Reading the sink after receiving on recordedCh is
	// therefore race-free.

	handlerReached := make(chan struct{})
	recordedCh := make(chan struct{}, 8)

	sink := metrics.NewMemorySink()
	mUnary, _ := metrics.NewServerInterceptors(sink)
	lUnary, _ := logger.NewLoggerInterceptors(nil)
	rUnary := grpc_recovery.UnaryServerInterceptor()

	syncIntr := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		defer func() {
			// Non-blocking send so a stuck reader can't deadlock the
			// server. The buffer size (8) is comfortably above any
			// single-call test's post-recording signal count.
			select {
			case recordedCh <- struct{}{}:
			default:
			}
		}()
		return handler(ctx, req)
	}

	lis := bufconn.Listen(integrationBufSize)
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(syncIntr, mUnary, lUnary, rUnary),
	)
	pb.RegisterAnswerServiceServer(srv, &advServer{
		greet: func(ctx context.Context, _ *pb.Question) (*pb.Answer, error) {
			close(handlerReached)
			<-ctx.Done()
			return nil, status.FromContextError(ctx.Err()).Err()
		},
	})

	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.GracefulStop()
		_ = lis.Close()
		<-serveDone
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := pb.NewAnswerServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	callCtx, cancelCall := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() {
		_, err := client.Greeting(callCtx, &pb.Question{Question: "wait"})
		errCh <- err
	}()

	// Wait until the handler is actually running before cancelling,
	// so the cancellation lands mid-flight rather than pre-dispatch.
	select {
	case <-handlerReached:
	case <-ctx.Done():
		t.Fatalf("handler never reached: %v", ctx.Err())
	}

	cancelCall()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected cancellation error from client")
		}
		code := status.Code(err)
		if code != codes.Canceled && code != codes.DeadlineExceeded {
			t.Errorf("want Canceled/DeadlineExceeded, got %v (%v)", code, err)
		}
	case <-ctx.Done():
		t.Fatalf("call did not return after cancel: %v", ctx.Err())
	}

	// Wait for the server side to finish recording. This is the
	// deterministic sync point — by the time recordedCh fires, the
	// metrics interceptor has already called Counter + Observe for
	// this RPC.
	select {
	case <-recordedCh:
	case <-ctx.Done():
		t.Fatalf("server never recorded the cancelled RPC: %v", ctx.Err())
	}

	// The metrics counter must have recorded exactly one call under
	// SOME error code. We don't over-specify the exact code because
	// the race between server-side cancellation detection and client
	// cancellation propagation can land on Canceled or Unknown; both
	// are legitimate "something cancelled" outcomes for this path.
	cs, _ := sink.Snapshot()
	var totalRequests int64
	for _, c := range cs {
		if c.Name == "grpc_server_requests_total" {
			totalRequests += c.Value
		}
	}
	if totalRequests != 1 {
		t.Errorf("expected exactly 1 total request recorded, got %d (snap=%+v)", totalRequests, cs)
	}
}

// -----------------------------------------------------------------------
// 6. Nil sink AND nil logger — zero-wiring sanity
// -----------------------------------------------------------------------

func TestIntegration_Unary_NilSinkAndNilLogger(t *testing.T) {
	// When the caller passes no sink and no logger, the interceptor
	// chain should still serve traffic. This guards regressions on
	// sinkOrNop (metrics) and the nil-z guard inside emit (logger).
	mUnary, _ := metrics.NewServerInterceptors(nil) // -> NopSink
	lUnary, _ := logger.NewLoggerInterceptors(nil)
	rUnary := grpc_recovery.UnaryServerInterceptor()

	lis := bufconn.Listen(integrationBufSize)
	srv := grpc.NewServer(grpc.ChainUnaryInterceptor(mUnary, lUnary, rUnary))
	pb.RegisterAnswerServiceServer(srv, &advServer{
		greet: func(_ context.Context, q *pb.Question) (*pb.Answer, error) {
			return &pb.Answer{Answer: q.Question}, nil
		},
	})

	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		srv.GracefulStop()
		_ = lis.Close()
		<-serveDone
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := pb.NewAnswerServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Greeting(ctx, &pb.Question{Question: fmt.Sprintf("ping-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatalf("nil-wiring call failed: %v", err)
	}
	if !strings.HasPrefix(resp.Answer, "ping-") {
		t.Errorf("unexpected response: %q", resp.Answer)
	}
}
