package notifierserver

import (
	"os"
	"strings"
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
// These tests verify the safego package from an external-package
// perspective using safego.GoWait for deterministic synchronization.

func TestSafeGo_NilFnIsNoOp(t *testing.T) {
	ch := safego.GoWait("nil-fn", nil)
	select {
	case <-ch:
		// channel was closed immediately (nil fn)
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait(nil) channel was not closed")
	}
}

func TestSafeGo_NormalFnCompletes(t *testing.T) {
	done := make(chan struct{})
	ch := safego.GoWait("normal-fn", func() {
		close(done)
	})

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo normal function did not complete within 2s")
	}

	// Also verify GoWait channel closes
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait channel not closed")
	}
}

func TestSafeGo_PanickingFnRecovered(t *testing.T) {
	entered := make(chan struct{})
	ch := safego.GoWait("panic-fn", func() {
		close(entered)
		panic("deliberate test panic (TG-4)")
	})

	select {
	case <-entered:
		// fn ran and panic was recovered — test binary survives
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo panicking function did not start within 2s")
	}

	// Wait for recovery + logging to complete
	select {
	case <-ch:
		// recovery done
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait channel not closed after recovery")
	}
}

func TestSafeGo_PanickingFnRuntimeError(t *testing.T) {
	entered := make(chan struct{})
	ch := safego.GoWait("nil-deref-fn", func() {
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

	// Wait for recovery + logging to complete
	select {
	case <-ch:
		// recovery done
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait channel not closed after recovery")
	}
}

func TestSafeGo_ConcurrentPanicsAllRecovered(t *testing.T) {
	const n = 50
	channels := make([]<-chan struct{}, n)
	for i := 0; i < n; i++ {
		i := i
		channels[i] = safego.GoWait("concurrent-panic", func() {
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
