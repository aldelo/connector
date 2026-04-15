package metrics

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 */

// gRPC server and client interceptors that emit baseline RPC metrics
// through the Sink interface. Wire them via Service.UnaryServerInterceptors
// (server side) or grpc.WithUnaryInterceptor at Dial time (client side).
//
// Metric series emitted (all opt-in via the sink you choose):
//
//   counter   grpc_server_requests_total{method, code}
//   histogram grpc_server_request_duration_seconds{method}
//   counter   grpc_server_errors_total{method, code}     -- only on err
//   counter   grpc_client_requests_total{method, code}
//   histogram grpc_client_request_duration_seconds{method}
//   counter   grpc_client_errors_total{method, code}     -- only on err
//
// Naming convention follows Prometheus' gRPC ecosystem prefixes so a
// future Prometheus Sink will produce series names that are recognizable
// to existing dashboards.

import (
	"context"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	mServerRequests = "grpc_server_requests_total"
	mServerDuration = "grpc_server_request_duration_seconds"
	mServerErrors   = "grpc_server_errors_total"
	mClientRequests = "grpc_client_requests_total"
	mClientDuration = "grpc_client_request_duration_seconds"
	mClientErrors   = "grpc_client_errors_total"
)

// NewServerInterceptors returns a (unary, stream) interceptor pair that
// emits baseline RPC metrics for every served request. If sink is nil,
// NopSink is substituted so the call site can omit the nil check.
//
// Wire up:
//
//	uIntr, sIntr := metrics.NewServerInterceptors(mySink)
//	svc.UnaryServerInterceptors  = append(svc.UnaryServerInterceptors,  uIntr)
//	svc.StreamServerInterceptors = append(svc.StreamServerInterceptors, sIntr)
//
// Place these AFTER any context-decoration interceptors (auth, tenancy)
// and BEFORE recovery so panics still register as Internal errors in the
// metrics stream.
func NewServerInterceptors(sink Sink) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	s := sinkOrNop(sink)

	// Both paths use a deferred recorder so the metric is emitted on:
	//   (a) normal return — captures err via named return
	//   (b) panic — the panicked flag stays true; record Internal and
	//       let the panic propagate to the outer recovery interceptor.
	// This removes the pre-P1-CONN-MET-A bug where a panic inside
	// handler unwound past the inline recordServer call, leaving the
	// panic invisible to the metrics stream.

	unary := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		start := time.Now()
		panicked := true
		defer func() {
			if panicked {
				recordServer(s, info.FullMethod, time.Since(start), status.Error(codes.Internal, "panic"))
			} else {
				recordServer(s, info.FullMethod, time.Since(start), err)
			}
		}()
		resp, err = handler(ctx, req)
		panicked = false
		return
	}

	stream := func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		start := time.Now()
		panicked := true
		defer func() {
			if panicked {
				recordServer(s, info.FullMethod, time.Since(start), status.Error(codes.Internal, "panic"))
			} else {
				recordServer(s, info.FullMethod, time.Since(start), err)
			}
		}()
		err = handler(srv, ss)
		panicked = false
		return
	}

	return unary, stream
}

// NewClientInterceptors returns a (unary, stream) interceptor pair that
// emits baseline RPC metrics for every outbound request. If sink is nil,
// NopSink is substituted.
//
// Wire up via grpc.WithUnaryInterceptor / grpc.WithStreamInterceptor at
// Dial time, OR via the connector Client's interceptor slots if exposed.
func NewClientInterceptors(sink Sink) (grpc.UnaryClientInterceptor, grpc.StreamClientInterceptor) {
	s := sinkOrNop(sink)

	// See NewServerInterceptors for the deferred-recorder pattern — same
	// invariant applies: an invoker / streamer panic must not swallow the
	// client-side metric.

	unary := func(ctx context.Context, method string, req, reply interface{},
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) (err error) {
		start := time.Now()
		panicked := true
		defer func() {
			if panicked {
				recordClient(s, method, time.Since(start), status.Error(codes.Internal, "panic"))
			} else {
				recordClient(s, method, time.Since(start), err)
			}
		}()
		err = invoker(ctx, method, req, reply, cc, opts...)
		panicked = false
		return
	}

	stream := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn,
		method string, streamer grpc.Streamer, opts ...grpc.CallOption) (cs grpc.ClientStream, err error) {
		start := time.Now()
		panicked := true
		defer func() {
			// We instrument stream creation only — full-stream duration would
			// require wrapping the ClientStream which is out of scope for the
			// baseline. Per-message metrics belong in a separate, opt-in
			// wrapper.
			if panicked {
				recordClient(s, method, time.Since(start), status.Error(codes.Internal, "panic"))
			} else {
				recordClient(s, method, time.Since(start), err)
			}
		}()
		cs, err = streamer(ctx, desc, cc, method, opts...)
		panicked = false
		return
	}

	return unary, stream
}

// SP-008 P1-CONN-2 (2026-04-15): hot-path label-set caches.
//
// Before: recordServer / recordClient allocated TWO fresh
// map[string]string per RPC — one for {method, code} (counter + error
// counter) and one for {method} (duration histogram). At 10K RPS that
// is ~20K map allocations per second per interceptor, plus the
// entries, plus escape-analysis heap promotion, plus GC pressure.
//
// After: the maps are memoized by cache key. `method` is a gRPC
// FullMethod string like "/pkg.Service/Op" — bounded by the service's
// RPC surface. `code` is codes.Code.String() — ~16 distinct values in
// practice. So the Cartesian product `(method, code)` is bounded by
// a small constant, and the first RPC per pair allocates the shared
// map which every subsequent RPC reuses.
//
// The cached maps are treated as immutable after first publish.
// Implementations of Sink MUST NOT mutate label maps passed to
// Counter / Observe — see Sink godoc. Both provided implementations
// (NopSink, MemorySink) are read-only consumers of their labels.
//
// The cache uses sync.Map so that (a) the common hit path is
// lock-free and (b) we don't need to pre-declare the label surface.
// LoadOrStore handles the race between two first-arrivers for the
// same (method, code) pair — whoever wins installs the map, the
// loser discards their transient allocation.
var (
	serverPairLabels   sync.Map // key: "method\x00code" -> map[string]string{"method":..,"code":..}
	serverMethodLabels sync.Map // key: "method"         -> map[string]string{"method":..}
	clientPairLabels   sync.Map // key: "method\x00code" -> map[string]string{"method":..,"code":..}
	clientMethodLabels sync.Map // key: "method"         -> map[string]string{"method":..}
)

// pairKey builds a compact cache key for a (method, code) pair. The
// ASCII NUL separator is absent from every legal gRPC FullMethod and
// from every codes.Code.String(), so (a+\x00+b) is injective.
func pairKey(a, b string) string {
	return a + "\x00" + b
}

// getOrBuildPairLabels returns a cached {method, code} label map for
// the requested cache bucket, creating it on first use.
func getOrBuildPairLabels(cache *sync.Map, method, code string) map[string]string {
	k := pairKey(method, code)
	if v, ok := cache.Load(k); ok {
		return v.(map[string]string)
	}
	m := map[string]string{"method": method, "code": code}
	actual, _ := cache.LoadOrStore(k, m)
	return actual.(map[string]string)
}

// getOrBuildMethodLabels returns a cached {method} label map for the
// requested cache bucket, creating it on first use.
func getOrBuildMethodLabels(cache *sync.Map, method string) map[string]string {
	if v, ok := cache.Load(method); ok {
		return v.(map[string]string)
	}
	m := map[string]string{"method": method}
	actual, _ := cache.LoadOrStore(method, m)
	return actual.(map[string]string)
}

// recordServer emits the standard 3-metric set for one served RPC.
// Centralized so the unary and stream paths cannot drift on label keys.
func recordServer(s Sink, method string, dur time.Duration, err error) {
	code := codeOf(err).String()
	pair := getOrBuildPairLabels(&serverPairLabels, method, code)
	mOnly := getOrBuildMethodLabels(&serverMethodLabels, method)

	s.Counter(mServerRequests, pair, 1)
	s.Observe(mServerDuration, mOnly, dur.Seconds())
	if err != nil {
		s.Counter(mServerErrors, pair, 1)
	}
}

// recordClient emits the standard 3-metric set for one outbound RPC.
func recordClient(s Sink, method string, dur time.Duration, err error) {
	code := codeOf(err).String()
	pair := getOrBuildPairLabels(&clientPairLabels, method, code)
	mOnly := getOrBuildMethodLabels(&clientMethodLabels, method)

	s.Counter(mClientRequests, pair, 1)
	s.Observe(mClientDuration, mOnly, dur.Seconds())
	if err != nil {
		s.Counter(mClientErrors, pair, 1)
	}
}

// codeOf extracts the gRPC status code from an error. Mirrors the helper
// in adapters/logger — duplicated rather than imported to keep the
// metrics package free of cross-adapter dependencies.
func codeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	return status.Code(err)
}

// sinkOrNop returns sink if non-nil, otherwise NopSink. Centralized so
// every constructor handles nil identically.
func sinkOrNop(sink Sink) Sink {
	if sink == nil {
		return NopSink{}
	}
	return sink
}
