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
	"reflect"
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
		{"k": `\\`}, // raw double-backslash
		{"a,b": "x"},  // delimiter inside a key, not just a value
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
