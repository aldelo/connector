package safego

import (
	"sync"
	"testing"
	"time"
)

// TestGo_NilFnIsNoOp verifies that Go(name, nil) does not spawn a
// goroutine and does not panic at the call site. This is the defensive
// guard for callers that pass a potentially-nil callback.
func TestGo_NilFnIsNoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Go(nil) panicked at the call site: %v", r)
		}
	}()

	Go("nil-fn", nil)
	// Give any accidentally-spawned goroutine a moment to run.
	time.Sleep(20 * time.Millisecond)
}

// TestGo_NormalFnCompletes verifies the happy path: a well-behaved fn
// runs in a goroutine and signals completion via channel.
func TestGo_NormalFnCompletes(t *testing.T) {
	done := make(chan struct{})

	Go("normal-fn", func() {
		close(done)
	})

	select {
	case <-done:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("Go fn did not execute within 2s")
	}
}

// TestGo_PanickingFnRecovered verifies that a panicking fn is recovered
// inside the goroutine. If the recovery did not work, the test binary
// would crash with a panic trace and `go test` would report FAIL.
func TestGo_PanickingFnRecovered(t *testing.T) {
	entered := make(chan struct{})

	Go("panic-fn", func() {
		close(entered)
		panic("deliberate panic in safego test")
	})

	select {
	case <-entered:
		// fn ran and panic was recovered — test binary survives
	case <-time.After(2 * time.Second):
		t.Fatal("Go fn did not start within 2s")
	}

	// Give the panic + recovery + log a moment to complete.
	time.Sleep(50 * time.Millisecond)
}

// TestGo_RuntimeErrorRecovered verifies recovery of a runtime.Error
// panic (nil pointer dereference) — the most likely real-world shape.
func TestGo_RuntimeErrorRecovered(t *testing.T) {
	entered := make(chan struct{})

	Go("nil-deref-fn", func() {
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

	time.Sleep(50 * time.Millisecond)
}

// TestGo_ConcurrentPanicsAllRecovered launches 50 panicking goroutines
// and verifies all are recovered independently. A single escaped panic
// would crash the binary. Also verifies Go has no shared state causing
// recovery contention.
func TestGo_ConcurrentPanicsAllRecovered(t *testing.T) {
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		i := i
		Go("concurrent-panic", func() {
			wg.Done()
			panic(i)
		})
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// all 50 goroutines ran and panicked — all recovered
	case <-time.After(5 * time.Second):
		t.Fatal("not all concurrent panicking goroutines started within 5s")
	}

	// Give all recoveries time to log.
	time.Sleep(100 * time.Millisecond)
}
