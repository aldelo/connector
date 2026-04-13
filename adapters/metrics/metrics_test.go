package metrics

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 */

// Tests pin the contract of the metrics adapter — Sink interface, MemorySink
// implementation, gRPC interceptor wiring, and label/series isolation.
//
// Specifically guarded:
//   - NopSink never panics on any input
//   - MemorySink.Counter is monotonic (negative deltas dropped)
//   - MemorySink.Counter aggregates correctly under concurrent writers
//   - Distinct label sets do NOT collide on the same series key
//   - Snapshot returns a deterministic, sorted view
//   - Server interceptors record success and failure paths with the correct code label
//   - sinkOrNop substitutes NopSink for nil

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// -----------------------------------------------------------------------
// NopSink
// -----------------------------------------------------------------------

func TestNopSink_NeverPanics(t *testing.T) {
	var s Sink = NopSink{}
	s.Counter("any", nil, 100)
	s.Counter("any", map[string]string{"k": "v"}, -1)
	s.Observe("any", nil, 0.5)
	// No assertion — the contract is "does not panic, does not allocate
	// observable state". If any of the above panicked, the test fails.
}

// -----------------------------------------------------------------------
// MemorySink — correctness
// -----------------------------------------------------------------------

func TestMemorySink_Counter_Monotonic(t *testing.T) {
	m := NewMemorySink()
	m.Counter("hits", nil, 5)
	m.Counter("hits", nil, 3)
	m.Counter("hits", nil, -10) // should be dropped

	cs, _ := m.Snapshot()
	if len(cs) != 1 {
		t.Fatalf("expected 1 counter series, got %d", len(cs))
	}
	if cs[0].Value != 8 {
		t.Errorf("expected 8 (5+3, drop -10), got %d", cs[0].Value)
	}
}

func TestMemorySink_Counter_LabelIsolation(t *testing.T) {
	m := NewMemorySink()
	m.Counter("rpc", map[string]string{"method": "A", "code": "OK"}, 1)
	m.Counter("rpc", map[string]string{"method": "A", "code": "OK"}, 1)
	m.Counter("rpc", map[string]string{"method": "B", "code": "OK"}, 1)
	m.Counter("rpc", map[string]string{"method": "A", "code": "Internal"}, 1)

	cs, _ := m.Snapshot()
	if len(cs) != 3 {
		t.Fatalf("expected 3 distinct series, got %d", len(cs))
	}

	// Aggregate by method+code -- the {A,OK} series should have value 2.
	want := map[string]int64{
		"rpc|code=OK,method=A":       2,
		"rpc|code=OK,method=B":       1,
		"rpc|code=Internal,method=A": 1,
	}
	for _, s := range cs {
		key := seriesKey(s.Name, s.Labels)
		if want[key] != s.Value {
			t.Errorf("series %q: want %d, got %d", key, want[key], s.Value)
		}
	}
}

func TestMemorySink_Observe_TracksMinMaxSumCount(t *testing.T) {
	m := NewMemorySink()
	m.Observe("latency", nil, 0.1)
	m.Observe("latency", nil, 0.5)
	m.Observe("latency", nil, 0.3)

	_, hs := m.Snapshot()
	if len(hs) != 1 {
		t.Fatalf("expected 1 histogram, got %d", len(hs))
	}
	h := hs[0]
	if h.Count != 3 {
		t.Errorf("count: want 3, got %d", h.Count)
	}
	if h.Min != 0.1 {
		t.Errorf("min: want 0.1, got %f", h.Min)
	}
	if h.Max != 0.5 {
		t.Errorf("max: want 0.5, got %f", h.Max)
	}
	if h.Sum < 0.89 || h.Sum > 0.91 {
		t.Errorf("sum: want ~0.9, got %f", h.Sum)
	}
	if h.Mean < 0.29 || h.Mean > 0.31 {
		t.Errorf("mean: want ~0.3, got %f", h.Mean)
	}
}

// -----------------------------------------------------------------------
// MemorySink — concurrency
// -----------------------------------------------------------------------

func TestMemorySink_Counter_ConcurrentWriters(t *testing.T) {
	const goroutines = 64
	const itersPer = 1000

	m := NewMemorySink()
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < itersPer; i++ {
				m.Counter("hits", nil, 1)
			}
		}()
	}
	wg.Wait()

	cs, _ := m.Snapshot()
	if len(cs) != 1 {
		t.Fatalf("expected 1 series, got %d", len(cs))
	}
	want := int64(goroutines * itersPer)
	if cs[0].Value != want {
		t.Errorf("want %d, got %d", want, cs[0].Value)
	}
}

// -----------------------------------------------------------------------
// MemorySink — cardinality cap
// -----------------------------------------------------------------------
//
// Pin the R12 WithLabelCardinalityLimit contract: a sink constructed
// with a positive limit drops NEW series past the cap (counters and
// histograms share one global budget), existing series continue to
// update, and OverflowDropped() reports the cumulative loss. A plain
// NewMemorySink remains unbounded and never reports drops.

func TestMemorySink_CardinalityCap_ZeroMeansUnlimited(t *testing.T) {
	m := NewMemorySinkWithLimit(0)
	for i := 0; i < 1000; i++ {
		m.Counter("hits", map[string]string{"id": fmt.Sprintf("%d", i)}, 1)
	}
	cs, _ := m.Snapshot()
	if len(cs) != 1000 {
		t.Errorf("limit=0 should be unlimited, got %d series", len(cs))
	}
	if got := m.OverflowDropped(); got != 0 {
		t.Errorf("limit=0 should never drop, got %d", got)
	}
}

func TestMemorySink_CardinalityCap_CounterDropped(t *testing.T) {
	m := NewMemorySinkWithLimit(3)
	m.Counter("rpc", map[string]string{"id": "a"}, 1)
	m.Counter("rpc", map[string]string{"id": "b"}, 1)
	m.Counter("rpc", map[string]string{"id": "c"}, 1)
	m.Counter("rpc", map[string]string{"id": "d"}, 1) // dropped
	m.Counter("rpc", map[string]string{"id": "e"}, 1) // dropped

	cs, _ := m.Snapshot()
	if len(cs) != 3 {
		t.Errorf("cap=3, want 3 series, got %d", len(cs))
	}
	if got := m.OverflowDropped(); got != 2 {
		t.Errorf("want 2 overflow drops, got %d", got)
	}
}

func TestMemorySink_CardinalityCap_HistogramDropped(t *testing.T) {
	m := NewMemorySinkWithLimit(2)
	m.Observe("lat", map[string]string{"id": "a"}, 0.1)
	m.Observe("lat", map[string]string{"id": "b"}, 0.2)
	m.Observe("lat", map[string]string{"id": "c"}, 0.3) // dropped

	_, hs := m.Snapshot()
	if len(hs) != 2 {
		t.Errorf("cap=2, want 2 histograms, got %d", len(hs))
	}
	if got := m.OverflowDropped(); got != 1 {
		t.Errorf("want 1 overflow drop, got %d", got)
	}
}

func TestMemorySink_CardinalityCap_MixedDropped(t *testing.T) {
	// Cap is global across counters AND histograms combined.
	m := NewMemorySinkWithLimit(3)
	m.Counter("c1", map[string]string{"k": "a"}, 1)   // series 1
	m.Observe("h1", map[string]string{"k": "b"}, 0.5) // series 2
	m.Counter("c2", map[string]string{"k": "c"}, 1)   // series 3
	m.Observe("h2", map[string]string{"k": "d"}, 0.5) // dropped
	m.Counter("c3", map[string]string{"k": "e"}, 1)   // dropped

	cs, hs := m.Snapshot()
	if total := len(cs) + len(hs); total != 3 {
		t.Errorf("cap=3 (global), got %d counters + %d histograms = %d",
			len(cs), len(hs), total)
	}
	if got := m.OverflowDropped(); got != 2 {
		t.Errorf("want 2 overflow drops, got %d", got)
	}
}

func TestMemorySink_CardinalityCap_ExistingSeriesStillUpdatable(t *testing.T) {
	// Once the cap is hit, existing series must still accept new writes —
	// only NEW series are gated. This preserves continuity on the metrics
	// you already care about when a label-design bug floods new keys.
	m := NewMemorySinkWithLimit(1)
	m.Counter("rpc", map[string]string{"id": "a"}, 5)
	m.Counter("rpc", map[string]string{"id": "b"}, 1) // dropped (new series)
	m.Counter("rpc", map[string]string{"id": "a"}, 3) // accepted (existing)

	cs, _ := m.Snapshot()
	if len(cs) != 1 {
		t.Fatalf("want 1 series, got %d", len(cs))
	}
	if cs[0].Value != 8 {
		t.Errorf("existing series should update past the cap: want 8, got %d", cs[0].Value)
	}
	if got := m.OverflowDropped(); got != 1 {
		t.Errorf("want 1 overflow drop, got %d", got)
	}

	// Existing histogram series should also keep updating past the cap.
	m2 := NewMemorySinkWithLimit(1)
	m2.Observe("lat", map[string]string{"id": "a"}, 0.1)
	m2.Observe("lat", map[string]string{"id": "b"}, 0.5) // dropped
	m2.Observe("lat", map[string]string{"id": "a"}, 0.3) // accepted
	_, hs := m2.Snapshot()
	if len(hs) != 1 || hs[0].Count != 2 {
		t.Errorf("existing histogram should update past the cap: hs=%+v", hs)
	}
	if got := m2.OverflowDropped(); got != 1 {
		t.Errorf("want 1 overflow drop on histogram, got %d", got)
	}
}

func TestMemorySink_CardinalityCap_Concurrent(t *testing.T) {
	// Race-detector sentinel: hammering the cap from many goroutines
	// must not corrupt state, panic, or lose accounting. Because the
	// cap check is performed under the write lock, it is strict — the
	// map size can never exceed the limit. Every call must be
	// accounted for as either an accepted series OR an overflow drop.
	const goroutines = 32
	const itersPer = 100
	const limit = 50

	m := NewMemorySinkWithLimit(limit)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		gg := g
		go func() {
			defer wg.Done()
			for i := 0; i < itersPer; i++ {
				m.Counter("rpc", map[string]string{"id": fmt.Sprintf("%d-%d", gg, i)}, 1)
			}
		}()
	}
	wg.Wait()

	cs, _ := m.Snapshot()
	if len(cs) > limit {
		t.Errorf("len=%d > limit=%d — cap breached under concurrent writers", len(cs), limit)
	}
	total := m.OverflowDropped() + int64(len(cs))
	want := int64(goroutines * itersPer)
	if total != want {
		t.Errorf("lost metrics under concurrency: accepted=%d dropped=%d total=%d want=%d",
			len(cs), m.OverflowDropped(), total, want)
	}
}

func TestMemorySink_CardinalityCap_OverflowDroppedNoLimit(t *testing.T) {
	// Plain NewMemorySink should never report any drops.
	m := NewMemorySink()
	for i := 0; i < 100; i++ {
		m.Counter("rpc", map[string]string{"id": fmt.Sprintf("%d", i)}, 1)
	}
	if got := m.OverflowDropped(); got != 0 {
		t.Errorf("unbounded sink should never drop, got %d", got)
	}
}

// -----------------------------------------------------------------------
// seriesKey — round trip
// -----------------------------------------------------------------------

func TestSeriesKey_DeterministicOrder(t *testing.T) {
	a := seriesKey("rpc", map[string]string{"a": "1", "b": "2", "c": "3"})
	b := seriesKey("rpc", map[string]string{"c": "3", "a": "1", "b": "2"})
	if a != b {
		t.Errorf("keys must be order-independent: %q vs %q", a, b)
	}
}

func TestSeriesKey_Roundtrip(t *testing.T) {
	in := map[string]string{"method": "/x.Y/Z", "code": "OK"}
	key := seriesKey("rpc", in)
	name, out := parseSeriesKey(key)
	if name != "rpc" {
		t.Errorf("name lost: %q", name)
	}
	if len(out) != len(in) {
		t.Fatalf("label count mismatch: %v vs %v", out, in)
	}
	for k, v := range in {
		if out[k] != v {
			t.Errorf("label %q: want %q, got %q", k, v, out[k])
		}
	}
}

func TestSeriesKey_NoLabels(t *testing.T) {
	key := seriesKey("rpc", nil)
	name, labels := parseSeriesKey(key)
	if name != "rpc" {
		t.Errorf("name lost: %q", name)
	}
	if len(labels) != 0 {
		t.Errorf("expected nil/empty labels, got %v", labels)
	}
}

// TestSeriesKey_AdversarialLabelValues pins MET-F1: label values
// containing the structural delimiters `,` and `=` (and the escape
// character `\`) must NOT collide across distinct logical series, and
// must round-trip exactly through seriesKey/parseSeriesKey.
//
// Pre-fix, the encoder concatenated label values raw, so:
//
//	{"path":"/a=b,c"}              ->  rpc|path=/a=b,c
//	{"path":"/a","extra":"b,c"}    ->  rpc|extra=b,c,path=/a   (different)
//
// while a more realistic collision arose when two label values shared
// the same delimiter pattern. The fix backslash-escapes both keys and
// values on encode, and a byte-walking parser respects the escapes on
// decode.
func TestSeriesKey_AdversarialLabelValues(t *testing.T) {
	cases := []map[string]string{
		{"path": "/a=b,c"},
		{"path": "/a", "extra": "b,c"},
		{"name": "tenant=acme"},
		{"k": `v1\,v2=v3`},
		{"k": `\\`},  // raw double-backslash
		{"a,b": "x"}, // delimiter inside a key, not just a value
		{"a=b": "y"},
		{"empty": ""},
	}
	seen := map[string]map[string]string{}
	for _, c := range cases {
		k := seriesKey("rpc", c)
		if prev, ok := seen[k]; ok && !reflect.DeepEqual(prev, c) {
			t.Errorf("collision: %v and %v both produced %q", prev, c, k)
		}
		seen[k] = c

		// Round-trip must restore the exact map.
		name, out := parseSeriesKey(k)
		if name != "rpc" {
			t.Errorf("name lost: in=%v key=%q name=%q", c, k, name)
		}
		if !reflect.DeepEqual(out, c) {
			t.Errorf("round-trip lost data: in=%v key=%q out=%v", c, k, out)
		}
	}
}

// TestSeriesKey_CounterAggregationDoesNotCollideAcrossAdversarialLabels
// is the production-shaped reproducer: pre-fix, two distinct Counter
// calls with adversarial label values would silently sum into a single
// series. Post-fix, each call increments its own series.
func TestSeriesKey_CounterAggregationDoesNotCollideAcrossAdversarialLabels(t *testing.T) {
	m := NewMemorySink()
	a := map[string]string{"path": "/a=b,c"}
	b := map[string]string{"path": "/a", "extra": "b,c"}
	m.Counter("rpc", a, 1)
	m.Counter("rpc", b, 1)

	cs, _ := m.Snapshot()
	if len(cs) < 2 {
		t.Fatalf("MET-F1 regression: expected at least 2 distinct counters, got %d", len(cs))
	}

	var foundA, foundB bool
	for _, c := range cs {
		if c.Name == "rpc" && reflect.DeepEqual(c.Labels, a) && c.Value == 1 {
			foundA = true
		}
		if c.Name == "rpc" && reflect.DeepEqual(c.Labels, b) && c.Value == 1 {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Errorf("MET-F1 regression: distinct series collapsed — foundA=%v foundB=%v cs=%+v", foundA, foundB, cs)
	}
}

// -----------------------------------------------------------------------
// seriesKey — fuzz target
// -----------------------------------------------------------------------

// FuzzSeriesKey_Roundtrip is the MET-F1 long-term follow-up. The
// hand-written TestSeriesKey_AdversarialLabelValues pins 8 specific
// delimiter-bearing cases, but that does not prove the escaping state
// machine holds across arbitrary byte inputs. This fuzz target drives
// seriesKey/parseSeriesKey with two-label maps built from
// fuzzer-supplied strings and asserts the round-trip invariant:
//
//	parseSeriesKey(seriesKey(name, labels)) == (name, labels)
//
// The fuzzer is seeded with the 8 adversarial cases from
// TestSeriesKey_AdversarialLabelValues plus a handful of byte-level
// boundary inputs, so corpus mutation starts from known-hard regions.
//
// Two inputs are pre-filtered (not bugs, just out-of-contract):
//
//  1. A metric name containing `|`. Per the seriesKey godoc, names are
//     package-level constants (mServerRequests etc.) and are not
//     escaped. parseSeriesKey splits on the first `|`, so a name
//     containing `|` would be truncated by design.
//
//  2. A labels map containing {"": ""}. parseSeriesKey's flush() guard
//     skips a pair when both key and value are empty, because the
//     encoder emits `name|=` which is structurally indistinguishable
//     from a zero-label trailing boundary. Empty-on-empty is
//     nonsensical as a label anyway — no real caller produces it.
//
// Run with:
//
//	go test -run=^$ -fuzz=FuzzSeriesKey_Roundtrip -fuzztime=30s \
//	  ./adapters/metrics/...
//
// The `-run=^$` disables normal tests during the fuzz run so only the
// fuzzer executes. Without `-fuzz`, this function compiles into the
// test binary but does NOT run — `go test ./...` skips it, so CI is
// unaffected.
func FuzzSeriesKey_Roundtrip(f *testing.F) {
	// Seed 1: the 8 adversarial cases from
	// TestSeriesKey_AdversarialLabelValues.
	f.Add("rpc", "path", "/a=b,c", "", "")
	f.Add("rpc", "path", "/a", "extra", "b,c")
	f.Add("rpc", "name", "tenant=acme", "", "")
	f.Add("rpc", "k", `v1\,v2=v3`, "", "")
	f.Add("rpc", "k", `\\`, "", "")
	f.Add("rpc", "a,b", "x", "a=b", "y")
	f.Add("rpc", "empty", "x", "", "")
	f.Add("rpc", "method", "/x.Y/Z", "code", "OK")

	// Seed 2: byte-level boundary inputs to kick the mutator into
	// non-ASCII regions quickly.
	f.Add("rpc", "\x00", "\x01", "\xff", "\xfe")
	f.Add("rpc", `\\\`, "===", ",,,", `\=,`)
	f.Add("rpc", "日本語", "値", "ключ", "значение")

	f.Fuzz(func(t *testing.T, name, k1, v1, k2, v2 string) {
		// Pre-filter 1: skip metric names containing `|`. The
		// package contract says names are not user-controlled and
		// therefore not escaped; parseSeriesKey splits on the first
		// `|`. A name containing `|` is out of contract.
		if strings.ContainsRune(name, '|') {
			t.Skip()
		}

		// Build the input map. Go map writes dedupe equal keys, so
		// if k1 == k2 the map ends up with 1 entry (v2 wins). That
		// matches what the encoder then receives — there is no
		// parallel "list of pairs" view, only the deduplicated map.
		in := map[string]string{}
		in[k1] = v1
		in[k2] = v2

		// Pre-filter 2: skip {"": ""} pairs. parseSeriesKey drops
		// them via the flush() empty-guard. Not a contract we test.
		if v, ok := in[""]; ok && v == "" {
			t.Skip()
		}

		key := seriesKey(name, in)
		outName, outLabels := parseSeriesKey(key)

		if outName != name {
			t.Fatalf("name lost: in=%q key=%q out=%q", name, key, outName)
		}

		if len(outLabels) != len(in) {
			t.Fatalf("label count mismatch: in=%v out=%v key=%q", in, outLabels, key)
		}

		for k, v := range in {
			ov, ok := outLabels[k]
			if !ok {
				t.Fatalf("label key missing after round-trip: in[%q]=%q key=%q out=%v", k, v, key, outLabels)
			}
			if ov != v {
				t.Fatalf("label value drifted: in[%q]=%q out[%q]=%q key=%q", k, v, k, ov, key)
			}
		}
	})
}

// -----------------------------------------------------------------------
// Interceptors — happy path
// -----------------------------------------------------------------------

func TestNewServerInterceptors_Unary_Success(t *testing.T) {
	sink := NewMemorySink()
	uIntr, _ := NewServerInterceptors(sink)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	resp, err := uIntr(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/x.Y/Z"}, handler)
	if err != nil || resp != "ok" {
		t.Fatalf("handler not invoked correctly: resp=%v err=%v", resp, err)
	}

	cs, hs := sink.Snapshot()

	// Should have: requests counter (1), no errors counter (success path).
	wantReq := seriesKey(mServerRequests, map[string]string{"method": "/x.Y/Z", "code": "OK"})
	wantDur := seriesKey(mServerDuration, map[string]string{"method": "/x.Y/Z"})

	if !findCounter(cs, wantReq, 1) {
		t.Errorf("missing request counter %q", wantReq)
	}
	if !findHistogram(hs, wantDur, 1) {
		t.Errorf("missing duration histogram %q", wantDur)
	}
	for _, c := range cs {
		if c.Name == mServerErrors {
			t.Errorf("error counter should not exist on success path")
		}
	}
}

func TestNewServerInterceptors_Unary_Error(t *testing.T) {
	sink := NewMemorySink()
	uIntr, _ := NewServerInterceptors(sink)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, status.Error(codes.PermissionDenied, "no")
	}
	_, _ = uIntr(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/x.Y/Z"}, handler)

	cs, _ := sink.Snapshot()

	// Error path should record: requests AND errors, both with code=PermissionDenied.
	wantReq := seriesKey(mServerRequests,
		map[string]string{"method": "/x.Y/Z", "code": "PermissionDenied"})
	wantErr := seriesKey(mServerErrors,
		map[string]string{"method": "/x.Y/Z", "code": "PermissionDenied"})

	if !findCounter(cs, wantReq, 1) {
		t.Errorf("missing request counter for error path %q", wantReq)
	}
	if !findCounter(cs, wantErr, 1) {
		t.Errorf("missing error counter %q", wantErr)
	}
}

func TestNewServerInterceptors_NilSink_Noop(t *testing.T) {
	uIntr, sIntr := NewServerInterceptors(nil)
	if uIntr == nil || sIntr == nil {
		t.Fatal("nil sink should still produce interceptors")
	}
	// Should not panic.
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}
	_, _ = uIntr(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/x.Y/Z"}, handler)
}

// -----------------------------------------------------------------------
// codeOf
// -----------------------------------------------------------------------

func TestCodeOf_NilOK(t *testing.T) {
	if codeOf(nil) != codes.OK {
		t.Errorf("nil err -> want OK")
	}
}

func TestCodeOf_StatusError(t *testing.T) {
	if codeOf(status.Error(codes.NotFound, "x")) != codes.NotFound {
		t.Errorf("want NotFound")
	}
}

// -----------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------

func findCounter(cs []CounterSnapshot, key string, want int64) bool {
	for _, c := range cs {
		if seriesKey(c.Name, c.Labels) == key && c.Value == want {
			return true
		}
	}
	return false
}

func findHistogram(hs []HistogramSnapshot, key string, wantCount uint64) bool {
	for _, h := range hs {
		if seriesKey(h.Name, h.Labels) == key && h.Count == wantCount {
			return true
		}
	}
	return false
}
