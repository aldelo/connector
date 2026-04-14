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
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// overflowLogThrottle bounds the rate at which MemorySink emits the
// cardinality-cap audit log line. A steady cardinality explosion would
// otherwise fire the log once per dropped sample — thousands of lines
// per second in pathological cases — drowning out every other signal
// and costing CloudWatch ingest. The throttle window ensures the log
// acts as a *notification* that drops are happening, not as a per-event
// record. Operators wanting an exact rate read OverflowDropped() from a
// dashboard; the log line is the wake-up.
//
// 60s is chosen as a compromise: short enough that an incident is
// visible in under a minute, long enough that even a burst of millions
// of drops produces at most a handful of log lines. Count-based
// throttling was considered and rejected — a 1-in-N strategy still
// spams under burst because N drops happen in microseconds.
const overflowLogThrottle = 60 * time.Second

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

	// labelLimit caps the total number of distinct (name, labels) series
	// across counters AND histograms combined. When a new series would
	// push the total above the cap, it is silently dropped and
	// overflowDropped is incremented instead. A value <= 0 means
	// unlimited (the default for NewMemorySink).
	//
	// Why a cap? Label values that drift with unbounded input — request
	// ID, raw URL path, tenant ID — can grow the map without bound,
	// holding arbitrary memory and degrading Snapshot() from O(series)
	// to O(unbounded). A hard cap turns the failure mode from "slow
	// OOM" into "visible dropped-metric counter", which an operator can
	// spot on a dashboard and fix by redesigning the labels.
	//
	// The cap is global (not per-name) to keep accounting trivial and
	// to avoid the "well-behaved name starves next to badly-behaved
	// name" dynamic that per-name caps create. The badly-behaved name
	// IS the signal you want to see.
	labelLimit      int
	overflowDropped atomic.Int64

	// overflowLogLastNS holds the time.Time.UnixNano() of the most
	// recent overflow audit log emission. Compared against time.Now()
	// on every drop so the log fires at most once per
	// overflowLogThrottle. Updated via CompareAndSwap so concurrent
	// drops serialize on a single log emission — the winner logs, the
	// losers silently increment overflowPendingDrops and move on.
	//
	// overflowPendingDrops accumulates the number of drops that
	// occurred since the last throttled log line; it is zeroed by the
	// goroutine that wins the CAS and is reported in the log body so
	// operators can see the true rate even while the log is throttled.
	overflowLogLastNS    atomic.Int64
	overflowPendingDrops atomic.Int64

	// overflowLogger is the destination for the throttled audit log.
	// Defaults to the standard library "log" package's Printf; tests
	// swap it for a captor to observe emissions deterministically
	// without racing against log.Default() global state. Never nil
	// after construction.
	overflowLogger func(format string, args ...interface{})
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

// NewMemorySink returns a fresh, empty MemorySink ready for use. It has
// no cardinality cap — every distinct (name, labels) pair allocates a
// new series. For bounded-memory behavior, use NewMemorySinkWithLimit.
func NewMemorySink() *MemorySink {
	return &MemorySink{
		counters:       make(map[string]*atomic.Int64),
		histograms:     make(map[string]*histogramState),
		overflowLogger: log.Printf,
	}
}

// NewMemorySinkWithLimit returns a MemorySink that caps the total
// number of distinct (name, labels) series across counters and
// histograms combined. When a new series would exceed limit, it is
// silently dropped and OverflowDropped() is incremented. A limit <= 0
// is equivalent to NewMemorySink (unlimited).
//
// Existing series continue to accept writes even after the cap is
// reached — only the *creation* of new series is gated. This preserves
// continuity for the metrics you already care about while bounding the
// blast radius of a label-design mistake.
func NewMemorySinkWithLimit(limit int) *MemorySink {
	m := NewMemorySink()
	m.labelLimit = limit
	return m
}

// OverflowDropped returns the cumulative count of Counter/Observe calls
// whose (name, labels) series was dropped because it would have
// exceeded the configured cardinality cap. Always 0 when the sink was
// constructed without a limit.
//
// Scrape this alongside Snapshot() on a debug endpoint — a non-zero
// value means some label key has unbounded cardinality and the metric
// surface is incomplete. This is the user-visible signal that replaces
// the silent OOM mode of an uncapped sink.
func (m *MemorySink) OverflowDropped() int64 {
	return m.overflowDropped.Load()
}

// recordOverflowDrop is called from the Counter and Observe drop paths
// whenever a new (name, labels) series is rejected by the cardinality
// cap. It increments both the authoritative OverflowDropped counter
// AND a per-window pending counter, then emits a single throttled
// audit log line if and only if the current caller wins the
// CompareAndSwap race on overflowLogLastNS for this window.
//
// Throttling rationale (M1-001):
// A cardinality-explosion incident can fire this path thousands of
// times per second. A per-drop log line would drown out every other
// signal in the same stream and impose nontrivial CloudWatch ingest
// cost. The OverflowDropped counter remains exact (it is the signal
// dashboards scrape); the log line is a low-frequency notification
// that drops are in progress, along with the true rate observed since
// the previous notification.
//
// The primitive is a pair of atomic.Int64 values — no channels, no
// tickers, no dependency on a background goroutine. This keeps the
// sink "a passive data structure" with the same lifecycle as before.
// Concurrency correctness: every caller attempts a single CAS. At most
// one caller per throttle window can succeed (the CAS compares against
// the *old* timestamp). The winner captures the pending count via
// atomic.Swap(0) and logs it. Losers silently increment the pending
// counter and return.
func (m *MemorySink) recordOverflowDrop() {
	m.overflowDropped.Add(1)
	m.overflowPendingDrops.Add(1)

	now := time.Now().UnixNano()
	last := m.overflowLogLastNS.Load()

	// First drop ever: seed the timestamp without logging. Otherwise
	// the first drop in a long-running process would always log
	// regardless of the throttle, defeating the "notification, not
	// per-event" intent.
	if last == 0 {
		if m.overflowLogLastNS.CompareAndSwap(0, now) {
			return
		}
		// Another goroutine won the seeding race; reload and fall
		// through to the normal throttle check below.
		last = m.overflowLogLastNS.Load()
	}

	if now-last < int64(overflowLogThrottle) {
		return
	}
	if !m.overflowLogLastNS.CompareAndSwap(last, now) {
		// Another goroutine has already emitted the log line for
		// this window; we become a silent drop.
		return
	}

	// We won the window. Capture every drop that accumulated since
	// the previous window closed and report the real rate.
	pending := m.overflowPendingDrops.Swap(0)
	m.overflowLogger(
		"metrics: MemorySink overflow — %d drops since last report (throttled to 1/%s); OverflowDropped total=%d",
		pending,
		overflowLogThrottle,
		m.overflowDropped.Load(),
	)
}

// Counter increments the named counter by delta. Negative deltas are
// silently dropped to preserve monotonicity. If the sink was
// constructed with a cardinality cap and this call would create a new
// series that exceeds the cap, the call is dropped and OverflowDropped
// is incremented.
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
		// Cardinality cap (if configured) is enforced under the write
		// lock, so the check-then-insert sequence is atomic: two
		// concurrent new-key writers serialize on the lock and the
		// second observes the incremented len. The cap is strict.
		if m.labelLimit > 0 && len(m.counters)+len(m.histograms) >= m.labelLimit {
			m.mu.Unlock()
			m.recordOverflowDrop()
			return
		}
		c = &atomic.Int64{}
		m.counters[key] = c
	}
	m.mu.Unlock()
	c.Add(delta)
}

// Observe records a sample for the named histogram series. If the sink
// was constructed with a cardinality cap and this call would create a
// new series that exceeds the cap, the sample is dropped entirely and
// OverflowDropped is incremented — an unobservable series cannot have
// its min/max/sum/count updated.
func (m *MemorySink) Observe(name string, labels map[string]string, value float64) {
	key := seriesKey(name, labels)

	m.mu.Lock()
	h, ok := m.histograms[key]
	if !ok {
		if m.labelLimit > 0 && len(m.counters)+len(m.histograms) >= m.labelLimit {
			// Release the write lock before invoking the throttled
			// audit-log helper — the configured overflowLogger may
			// block on I/O (stdlib log goes through a mutex + write
			// syscall) and we do not want that serialized behind
			// every other Observe/Counter call.
			m.mu.Unlock()
			m.recordOverflowDrop()
			return
		}
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
	m.mu.Unlock()
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

// escapeLabel backslash-escapes the three structural characters used by
// seriesKey so that label keys/values containing `,` or `=` cannot collide
// across different (key, value) pairs. The escape character itself is
// escaped FIRST so that double-escaping cannot conflate input bytes.
//
// MET-F1 fix: pre-fix, an unescaped `,` or `=` in a label value silently
// merged distinct series — e.g. {"path":"/a=b,c"} and
// {"path":"/a","extra":"b,c"} produced the same key. With escaping, both
// round-trip uniquely.
func escapeLabel(s string) string {
	if !strings.ContainsAny(s, `\,=`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' || c == ',' || c == '=' {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

// seriesKey builds a deterministic string key for a (name, labels) pair.
// Labels are sorted by key so the same logical series always hashes to
// the same map slot regardless of caller iteration order.
//
// Format: name|k1=v1,k2=v2,...   where each k and v is backslash-escaped
// (see escapeLabel). The metric name itself is not escaped because the
// package owns it (callers pass package-level constants like
// mServerRequests, never user input). Label keys and values may be
// user-controlled, so they are always escaped.
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
		b.WriteString(escapeLabel(k))
		b.WriteByte('=')
		b.WriteString(escapeLabel(labels[k]))
	}
	return b.String()
}

// parseSeriesKey is the inverse of seriesKey, used only by Snapshot to
// reconstruct the original (name, labels) pair from a stored map key.
//
// This is a deliberate round-trip — Snapshot needs the originals for
// CounterSnapshot/HistogramSnapshot. We accept the cost of re-parsing
// rather than storing duplicate (key, name, labels) tuples in the map.
//
// MET-F1 fix: byte-walk parser that respects backslash escapes for `,`
// (pair separator) and `=` (key/value separator). A literal `\` in input
// arrives as `\\` and is restored to a single `\`. Any unescaped `,` or
// `=` is treated as a structural delimiter, exactly as the encoder
// emits it.
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
	var keyB, valB strings.Builder
	inValue := false
	flush := func() {
		if keyB.Len() > 0 || valB.Len() > 0 {
			labels[keyB.String()] = valB.String()
		}
		keyB.Reset()
		valB.Reset()
		inValue = false
	}

	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c == '\\' && i+1 < len(rest) {
			// Escape: emit the next byte literally into the
			// current half (key or value). This restores `\`,
			// `,`, and `=` produced by escapeLabel.
			next := rest[i+1]
			if inValue {
				valB.WriteByte(next)
			} else {
				keyB.WriteByte(next)
			}
			i++ // consume the escaped byte
			continue
		}
		if c == '=' && !inValue {
			inValue = true
			continue
		}
		if c == ',' {
			flush()
			continue
		}
		if inValue {
			valB.WriteByte(c)
		} else {
			keyB.WriteByte(c)
		}
	}
	// trailing pair
	flush()

	if len(labels) == 0 {
		return name, nil
	}
	return name, labels
}
