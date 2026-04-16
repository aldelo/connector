package notifierserver

import (
	"sync"
	"testing"
	"time"
)

// TG-4: safeGo tests for notifierserver.
// The safeGo function here is identical to service/service.go's version
// (package-private, duplicated because it is unexported). These tests
// verify the notifierserver copy independently.

func TestSafeGo_NilFnIsNoOp(t *testing.T) {
	// safeGo with nil fn must not panic or spawn a goroutine
	safeGo("nil-fn", nil)
	time.Sleep(20 * time.Millisecond)
}

func TestSafeGo_NormalFnCompletes(t *testing.T) {
	done := make(chan struct{})

	safeGo("normal-fn", func() {
		close(done)
	})

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo normal function did not complete within 2s")
	}
}

func TestSafeGo_PanickingFnRecovered(t *testing.T) {
	entered := make(chan struct{})

	safeGo("panic-fn", func() {
		close(entered)
		panic("deliberate test panic (TG-4)")
	})

	select {
	case <-entered:
		// fn ran and panic was recovered — test binary survives
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo panicking function did not start within 2s")
	}

	// give recovery time to complete
	time.Sleep(50 * time.Millisecond)
}

func TestSafeGo_PanickingFnRuntimeError(t *testing.T) {
	entered := make(chan struct{})

	safeGo("nil-deref-fn", func() {
		close(entered)
		var p *int
		_ = *p // nil pointer dereference — runtime panic
	})

	select {
	case <-entered:
		// runtime panic recovered
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo runtime-error function did not start within 2s")
	}

	time.Sleep(50 * time.Millisecond)
}

func TestSafeGo_ConcurrentPanicsAllRecovered(t *testing.T) {
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)

	for i := 0; i < n; i++ {
		i := i
		safeGo("concurrent-panic", func() {
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

	// give all recoveries time to log
	time.Sleep(100 * time.Millisecond)
}
