package metrics

/*
 * Copyright 2020-2026 Aldelo, LP
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

// Package metrics provides a dependency-free metrics surface for the
// connector library and pluggable Sink interface so consumers can adapt
// any backend (Prometheus, StatsD, OpenTelemetry, CloudWatch EMF, ...)
// without forcing those dependencies into the connector module.
//
// Why no Prometheus dependency?
//
// connector ships into 36+ downstream services. Adding the
// prometheus/client_golang module would force every one of them to inherit
// the dep and its transitive graph (~10 packages, including expvar
// integration). Many of those services already use a different metrics
// system (CloudWatch EMF, statsd). The Sink interface lets each consumer
// choose at wiring time.
//
// Three pieces:
//
//  1. Sink — minimal counter + histogram surface. Implementations are
//     trivial: see NopSink (always discards) and MemorySink (in-process
//     atomic counters, useful for tests and small services).
//
//  2. NewServerInterceptors / NewClientInterceptors — gRPC interceptors
//     that translate each RPC into 3 metric events (request count,
//     duration, error count if applicable). Drop them into the existing
//     Service.UnaryServerInterceptors / Client interceptor slots.
//
//  3. MemorySink.Snapshot — a read-only view of the current counter and
//     histogram state. Consumers without a real metrics backend can scrape
//     this from a debug endpoint.
//
// All Sink implementations MUST be safe for concurrent use. The provided
// MemorySink uses sync/atomic on a sharded map under sync.RWMutex —
// contention is bounded by the number of distinct (name, label-set) pairs.

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Sink is the pluggable metrics backend interface. Every connector
// instrumentation point goes through this.
//
// Counter increments a named, labeled counter by delta. delta MUST be
// non-negative; negative deltas are silently dropped by NopSink and
// MemorySink. (Prometheus-style counters are monotonic.)
//
// Observe records a single sample for a named, labeled histogram.
// Implementations are free to bucket internally; the connector does not
// dictate a bucket layout.
//
// Both methods MUST be safe for concurrent use across goroutines.
//
// Implementations should treat a nil labels map as equivalent to an empty
// map (no labels). The provided implementations follow this convention.
type Sink interface {
	Counter(name string, labels map[string]string, delta int64)
	Observe(name string, labels map[string]string, value float64)
}

// NopSink discards every metric event. Use it as a safe default when no
// real sink has been wired — every connector instrumentation site checks
// for nil and substitutes NopSink, so passing nil is also acceptable.
type NopSink struct{}

// Counter is a no-op for NopSink.
func (NopSink) Counter(name string, labels map[string]string, delta int64) {}

// Observe is a no-op for NopSink.
func (NopSink) Observe(name string, labels map[string]string, value float64) {}

// MemorySink is an in-process, dependency-free Sink implementation
// suitable for tests, single-instance services, and debug endpoints. It
// stores all counters and a small running summary (count, sum, min, max)
// for each histogram series.
//
// MemorySink is NOT a substitute for a real metrics backend in
// multi-instance production deployments — there is no aggregation across
// processes. For production, implement Sink against your existing
// telemetry pipeline.
type MemorySink struct {
	mu         sync.RWMutex
	counters   map[string]*atomic.Int64
	histograms map[string]*histogramState
}

// histogramState tracks min/max/sum/count for a single histogram series.
// Mutated under MemorySink.mu (write lock for Observe, read lock for
// Snapshot reads). The 4 fields are written together so we keep them all
// behind a single lock rather than mixing atomics with non-atomic state.
type histogramState struct {
	count uint64
	sum   float64
	min   float64
	max   float64
}

// NewMemorySink returns a fresh, empty MemorySink ready for use.
func NewMemorySink() *MemorySink {
	return &MemorySink{
		counters:   make(map[string]*atomic.Int64),
		histograms: make(map[string]*histogramState),
	}
}

// Counter increments the named counter by delta. Negative deltas are
// silently dropped to preserve monotonicity.
func (m *MemorySink) Counter(name string, labels map[string]string, delta int64) {
	if delta < 0 {
		return
	}
	key := seriesKey(name, labels)

	// Fast path: counter exists.
	m.mu.RLock()
	c, ok := m.counters[key]
	m.mu.RUnlock()
	if ok {
		c.Add(delta)
		return
	}

	// Slow path: insert under write lock with double-check.
	m.mu.Lock()
	if c, ok = m.counters[key]; !ok {
		c = &atomic.Int64{}
		m.counters[key] = c
	}
	m.mu.Unlock()
	c.Add(delta)
}

// Observe records a sample for the named histogram series.
func (m *MemorySink) Observe(name string, labels map[string]string, value float64) {
	key := seriesKey(name, labels)

	m.mu.Lock()
	defer m.mu.Unlock()

	h, ok := m.histograms[key]
	if !ok {
		h = &histogramState{min: value, max: value}
		m.histograms[key] = h
	}
	h.count++
	h.sum += value
	if value < h.min {
		h.min = value
	}
	if value > h.max {
		h.max = value
	}
}

// CounterSnapshot is a point-in-time copy of one counter series.
type CounterSnapshot struct {
	Name   string
	Labels map[string]string
	Value  int64
}

// HistogramSnapshot is a point-in-time copy of one histogram series.
// Mean is provided for convenience (Sum / Count) when Count > 0.
type HistogramSnapshot struct {
	Name   string
	Labels map[string]string
	Count  uint64
	Sum    float64
	Min    float64
	Max    float64
	Mean   float64
}

// Snapshot returns a stable copy of the current counter and histogram
// state. The returned slices are sorted by series key for deterministic
// iteration order — useful for tests, dashboards, and snapshot diffing.
//
// Snapshot is read-only with respect to MemorySink. Concurrent Counter /
// Observe calls are safe but may or may not be reflected in the returned
// snapshot depending on timing.
func (m *MemorySink) Snapshot() ([]CounterSnapshot, []HistogramSnapshot) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cs := make([]CounterSnapshot, 0, len(m.counters))
	for k, c := range m.counters {
		name, labels := parseSeriesKey(k)
		cs = append(cs, CounterSnapshot{Name: name, Labels: labels, Value: c.Load()})
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Name != cs[j].Name {
			return cs[i].Name < cs[j].Name
		}
		return seriesKey(cs[i].Name, cs[i].Labels) < seriesKey(cs[j].Name, cs[j].Labels)
	})

	hs := make([]HistogramSnapshot, 0, len(m.histograms))
	for k, h := range m.histograms {
		name, labels := parseSeriesKey(k)
		mean := 0.0
		if h.count > 0 {
			mean = h.sum / float64(h.count)
		}
		hs = append(hs, HistogramSnapshot{
			Name: name, Labels: labels,
			Count: h.count, Sum: h.sum,
			Min: h.min, Max: h.max, Mean: mean,
		})
	}
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].Name != hs[j].Name {
			return hs[i].Name < hs[j].Name
		}
		return seriesKey(hs[i].Name, hs[i].Labels) < seriesKey(hs[j].Name, hs[j].Labels)
	})

	return cs, hs
}

// seriesKey builds a deterministic string key for a (name, labels) pair.
// Labels are sorted by key so the same logical series always hashes to
// the same map slot regardless of caller iteration order.
//
// Format: name|k1=v1,k2=v2,...
//
// The format is internal — callers should not parse it. Use parseSeriesKey
// (intentionally limited) to recover the original components.
func seriesKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name + "|"
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.Grow(len(name) + 1 + len(keys)*16)
	b.WriteString(name)
	b.WriteByte('|')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
	}
	return b.String()
}

// parseSeriesKey is the inverse of seriesKey, used only by Snapshot to
// reconstruct the original (name, labels) pair from a stored map key.
//
// This is a deliberate round-trip — Snapshot needs the originals for
// CounterSnapshot/HistogramSnapshot. We accept the cost of re-parsing
// rather than storing duplicate (key, name, labels) tuples in the map.
func parseSeriesKey(key string) (string, map[string]string) {
	pipe := strings.IndexByte(key, '|')
	if pipe < 0 {
		return key, nil
	}
	name := key[:pipe]
	rest := key[pipe+1:]
	if rest == "" {
		return name, nil
	}
	labels := make(map[string]string)
	for _, pair := range strings.Split(rest, ",") {
		eq := strings.IndexByte(pair, '=')
		if eq < 0 {
			continue
		}
		labels[pair[:eq]] = pair[eq+1:]
	}
	return name, labels
}
