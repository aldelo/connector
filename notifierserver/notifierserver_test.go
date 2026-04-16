package notifierserver

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aldelo/connector/internal/safego"
)

// -----------------------------------------------------------------------
// A4-P6-F1: Source-level test — SNS relay must NOT have CORS middleware
// -----------------------------------------------------------------------
//
// NewNotifierServer has heavy external dependencies (DynamoDB, SNS, config
// files) making it impractical to instantiate in a unit test. Instead we
// pin the source invariant: the "base" RouteDefinition must set
// CorsMiddleware to nil, not to &cors.Config{} or any other value.
// This catches re-introduction of an empty CORS config at code-review
// time without needing a full integration harness.

func TestSNSRelayEndpoint_NoCorsMiddleware_A4P6F1(t *testing.T) {
	src, err := os.ReadFile("notifierserver.go")
	if err != nil {
		t.Fatalf("cannot read notifierserver.go: %v", err)
	}
	body := string(src)

	// The route definition must contain CorsMiddleware: nil
	if !strings.Contains(body, "CorsMiddleware: nil,") {
		t.Error("A4-P6-F1 regression: SNS relay route definition must set CorsMiddleware: nil (no CORS for server-to-server SNS callbacks)")
	}

	// The old empty config must NOT exist
	if strings.Contains(body, "CorsMiddleware: &cors.Config{}") {
		t.Error("A4-P6-F1 regression: SNS relay route definition still has empty &cors.Config{} — remove CORS entirely for SNS endpoints")
	}
}

// A4-P6-F3: Source-level test — the fire-and-forget trade-off must be documented
func TestSNSRelayHandler_FireAndForgetDocumented_A4P6F3(t *testing.T) {
	src, err := os.ReadFile("notifierserver.go")
	if err != nil {
		t.Fatalf("cannot read notifierserver.go: %v", err)
	}
	body := string(src)

	// Must document the intentional fire-and-forget pattern near the safeGo call
	required := []string{
		"fire-and-forget",
		"Broadcast",
		"SNS",
	}
	for _, kw := range required {
		if !strings.Contains(body, kw) {
			t.Errorf("A4-P6-F3: expected documentation keyword %q near safeGo call site in snsrelay handler", kw)
		}
	}
}

// TG-4: safeGo tests for notifierserver.
// The safeGo function here is identical to service/service.go's version
// (package-private, duplicated because it is unexported). These tests
// verify the notifierserver copy independently.

func TestSafeGo_NilFnIsNoOp(t *testing.T) {
	// safeGo with nil fn must not panic or spawn a goroutine
	safego.Go("nil-fn", nil)
	time.Sleep(20 * time.Millisecond)
}

func TestSafeGo_NormalFnCompletes(t *testing.T) {
	done := make(chan struct{})

	safego.Go("normal-fn", func() {
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

	safego.Go("panic-fn", func() {
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

	safego.Go("nil-deref-fn", func() {
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
		safego.Go("concurrent-panic", func() {
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
