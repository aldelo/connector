package safego

import (
	"testing"
	"time"
)

// TestGo_NilFnIsNoOp verifies that GoWait(name, nil) returns an
// already-closed channel and does not spawn a goroutine.
func TestGo_NilFnIsNoOp(t *testing.T) {
	ch := GoWait("nil-fn", nil)
	select {
	case <-ch:
		// channel was closed immediately (nil fn)
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait(nil) channel was not closed")
	}
}

// TestGo_NormalFnCompletes verifies the happy path: a well-behaved fn
// runs in a goroutine and signals completion via channel.
func TestGo_NormalFnCompletes(t *testing.T) {
	done := make(chan struct{})
	ch := GoWait("normal-fn", func() {
		close(done)
	})

	select {
	case <-done:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("Go fn did not execute within 2s")
	}

	// Also verify GoWait channel closes
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait channel not closed")
	}
}

// TestGo_PanickingFnRecovered verifies that a panicking fn is recovered
// inside the goroutine. If the recovery did not work, the test binary
// would crash with a panic trace and `go test` would report FAIL.
func TestGo_PanickingFnRecovered(t *testing.T) {
	entered := make(chan struct{})
	ch := GoWait("panic-fn", func() {
		close(entered)
		panic("deliberate panic in safego test")
	})

	select {
	case <-entered:
		// fn started
	case <-time.After(2 * time.Second):
		t.Fatal("Go fn did not start within 2s")
	}

	// Wait for recovery + logging to complete
	select {
	case <-ch:
		// recovery done
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait channel not closed after recovery")
	}
}

// TestGo_RuntimeErrorRecovered verifies recovery of a runtime.Error
// panic (nil pointer dereference) — the most likely real-world shape.
func TestGo_RuntimeErrorRecovered(t *testing.T) {
	entered := make(chan struct{})
	ch := GoWait("nil-deref-fn", func() {
		close(entered)
		var p *int
		_ = *p // nil pointer dereference — runtime panic
	})

	select {
	case <-entered:
		// runtime panic recovered
	case <-time.After(2 * time.Second):
		t.Fatal("Go fn did not start within 2s")
	}

	// Wait for recovery + logging to complete
	select {
	case <-ch:
		// recovery done
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait channel not closed after recovery")
	}
}

// TestGo_ConcurrentPanicsAllRecovered launches 50 panicking goroutines
// and verifies all are recovered independently. A single escaped panic
// would crash the binary. Also verifies GoWait has no shared state causing
// recovery contention.
func TestGo_ConcurrentPanicsAllRecovered(t *testing.T) {
	const n = 50
	channels := make([]<-chan struct{}, n)
	for i := 0; i < n; i++ {
		i := i
		channels[i] = GoWait("concurrent-panic", func() {
			panic(i)
		})
	}

	for j, ch := range channels {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("goroutine %d did not complete within 5s", j)
		}
	}
}
