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
	"sync/atomic"
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
//
// SP-010 pass-5 A4-F2 (2026-04-15): cardinality cap on the label caches.
//
// The original design relied on server-side `method` being server-baked
// at RegisterServer time and therefore bounded by the .proto surface.
// That is correct for the server-side caches, but the client-side caches
// take `method` from the call site — a gRPC-Web bridge, dynamic codec,
// fuzzer, or a consumer that constructs method names from user input
// would grow `clientPairLabels` / `clientMethodLabels` without bound
// for the lifetime of the process.
//
// The MemorySink learned this lesson via SP-008 P2-CONN-1 and got
// DefaultMemorySinkLimit; the interceptor caches got the optimization
// without the corresponding cap. This change applies the same
// safe-by-default standard: each cache is capped at
// DefaultInterceptorLabelCacheLimit (4096) distinct entries. On
// cap-exceeded, getOrBuild* falls through to a fresh per-call
// allocation (slower path, but bounded memory — this is the same
// shape as the pre-P1-CONN-2 hot path, so the fall-through is simply
// a reversion to the pre-optimization cost model).
//
// Overflow is signalled via InterceptorLabelCacheOverflow() so operators
// have visibility into when the cap is being hit; this mirrors
// OverflowDropped() on MemorySink. A process that is healthy under the
// cap produces overflow=0; a process that hits the cap should be
// investigated for method-name hygiene on the client side.

// DefaultInterceptorLabelCacheLimit is the per-cache cardinality cap
// applied to each of the four process-global interceptor label caches.
// The value is chosen well above any realistic server's RPC surface
// (a .proto surface with >1000 distinct methods is pathological) and
// comfortably below the point where the fall-through allocation cost
// would dominate. Four separate caches × 4096 entries × ~64 B/entry
// ≈ 1 MiB steady-state worst case — negligible.
//
// On-overflow behavior: getOrBuild* returns a fresh per-call allocation
// and increments InterceptorLabelCacheOverflow(). The series remains
// observable downstream; only the hot-path optimization is dropped.
const DefaultInterceptorLabelCacheLimit = 4096

// boundedLabelCache wraps a sync.Map with an atomic size counter and
// an overflow counter. The size counter is maintained by the single
// caller path in getOrBuild* — it is incremented only on first-install
// (the LoadOrStore "stored" branch), so it cannot drift from the
// actual sync.Map length under concurrent writers.
//
// The cap check is deliberately advisory: we read size with an
// atomic.Load before LoadOrStore. Under a concurrent burst of N
// first-arrivers for N distinct keys, the cache can briefly exceed
// the cap by O(concurrent-writers) — but never by more than that,
// and the overflow accounting still catches the shape of the problem.
// No lock needed.
type boundedLabelCache struct {
	m        sync.Map
	size     atomic.Int64
	overflow atomic.Uint64
}

// Four process-global caches. The server-side pair also gets the cap
// even though its `method` is bounded by the .proto surface: (a) the
// cap is generous enough that no normal .proto surface trips it, and
// (b) uniform enforcement is simpler to reason about than "server is
// safe, client is not" asymmetry.
var (
	serverPairLabels   boundedLabelCache // key: "method\x00code" -> map[string]string{"method":..,"code":..}
	serverMethodLabels boundedLabelCache // key: "method"         -> map[string]string{"method":..}
	clientPairLabels   boundedLabelCache // key: "method\x00code" -> map[string]string{"method":..,"code":..}
	clientMethodLabels boundedLabelCache // key: "method"         -> map[string]string{"method":..}
)

// InterceptorLabelCacheOverflow returns the cumulative count of
// getOrBuild* fall-throughs across all four interceptor label caches.
// Non-zero is a signal that somewhere, a `method` label is taking
// unbounded values and should be investigated — most commonly a
// client-side gRPC invoker that constructs FullMethod strings from
// user input. Safe to call from any goroutine; read-only.
func InterceptorLabelCacheOverflow() uint64 {
	return serverPairLabels.overflow.Load() +
		serverMethodLabels.overflow.Load() +
		clientPairLabels.overflow.Load() +
		clientMethodLabels.overflow.Load()
}

// resetInterceptorLabelCaches drops all four caches and zeroes the
// overflow counters. Test-only: production code must not call this.
// Exported package-internally for the A4-F2 regression test; not part
// of the public API.
func resetInterceptorLabelCaches() {
	for _, c := range []*boundedLabelCache{
		&serverPairLabels, &serverMethodLabels,
		&clientPairLabels, &clientMethodLabels,
	} {
		c.m.Range(func(k, _ any) bool { c.m.Delete(k); return true })
		c.size.Store(0)
		c.overflow.Store(0)
	}
}

// pairKey builds a compact cache key for a (method, code) pair. The
// ASCII NUL separator is absent from every legal gRPC FullMethod and
// from every codes.Code.String(), so (a+\x00+b) is injective.
func pairKey(a, b string) string {
	return a + "\x00" + b
}

// getOrBuildPairLabels returns a cached {method, code} label map for
// the requested cache bucket, creating it on first use. If the cache
// size has reached DefaultInterceptorLabelCacheLimit, the call falls
// through to a fresh per-call allocation and increments the cache's
// overflow counter.
func getOrBuildPairLabels(cache *boundedLabelCache, method, code string) map[string]string {
	k := pairKey(method, code)
	if v, ok := cache.m.Load(k); ok {
		return v.(map[string]string)
	}
	m := map[string]string{"method": method, "code": code}
	if cache.size.Load() >= DefaultInterceptorLabelCacheLimit {
		// SP-010 pass-5 A4-F2: fall-through. Return a per-call
		// allocation so the RPC still emits its metrics. The cache
		// stops growing; overflow counter records the event.
		cache.overflow.Add(1)
		return m
	}
	actual, loaded := cache.m.LoadOrStore(k, m)
	if !loaded {
		cache.size.Add(1)
	}
	return actual.(map[string]string)
}

// getOrBuildMethodLabels returns a cached {method} label map for the
// requested cache bucket, creating it on first use. Same overflow
// fall-through as getOrBuildPairLabels.
func getOrBuildMethodLabels(cache *boundedLabelCache, method string) map[string]string {
	if v, ok := cache.m.Load(method); ok {
		return v.(map[string]string)
	}
	m := map[string]string{"method": method}
	if cache.size.Load() >= DefaultInterceptorLabelCacheLimit {
		cache.overflow.Add(1)
		return m
	}
	actual, loaded := cache.m.LoadOrStore(method, m)
	if !loaded {
		cache.size.Add(1)
	}
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
