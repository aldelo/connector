package client

import (
	"errors"
	"testing"
	"time"
)

// drainStart runs the loop in a goroutine and returns channels to drive it deterministically.
// Ticks are unbuffered and each refresh signals on `ran`, so the test can establish a strict
// happens-before between "deliver tick" and "refresh completed" — no sleeps, race-clean.
func startLoop(c *Client, refresh func() error) (stop chan struct{}, tick chan time.Time, ran chan struct{}, done chan struct{}) {
	stop = make(chan struct{})
	tick = make(chan time.Time)
	ran = make(chan struct{}, 1)
	done = make(chan struct{})
	go func() {
		c.runEndpointRefreshLoop(stop, tick, func() error {
			err := refresh()
			ran <- struct{}{}
			return err
		})
		close(done)
	}()
	return
}

func TestRunEndpointRefreshLoop_RefreshesPerTickAndStops(t *testing.T) {
	c := &Client{}
	var n int // written only by the loop goroutine; read after <-done (happens-after)
	stop, tick, ran, done := startLoop(c, func() error { n++; return nil })

	for i := 0; i < 3; i++ {
		tick <- time.Time{} // blocks until the loop receives
		<-ran               // blocks until that refresh completed
	}
	close(stop)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not return after stop closed")
	}
	if n != 3 {
		t.Fatalf("expected 3 refreshes, got %d", n)
	}
}

func TestRunEndpointRefreshLoop_ContinuesOnRefreshError(t *testing.T) {
	c := &Client{}
	var n int
	stop, tick, ran, done := startLoop(c, func() error { n++; return errors.New("boom") })

	// A refresh error must NOT abort the loop — all 3 ticks still fire.
	for i := 0; i < 3; i++ {
		tick <- time.Time{}
		<-ran
	}
	close(stop)
	<-done
	if n != 3 {
		t.Fatalf("expected 3 refreshes despite errors, got %d", n)
	}
}

func TestRunEndpointRefreshLoop_StopsWhenClientClosed(t *testing.T) {
	c := &Client{}
	var n int
	stop, tick, ran, done := startLoop(c, func() error { n++; return nil })

	tick <- time.Time{} // first refresh
	<-ran

	c.closed.Store(true) // mark client closed
	tick <- time.Time{}  // next tick: loop observes closed and returns WITHOUT refreshing

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not return after client marked closed")
	}
	close(stop) // no-op now; loop already returned
	if n != 1 {
		t.Fatalf("expected exactly 1 refresh before close, got %d", n)
	}
}

func TestEffectiveEndpointRefreshInterval(t *testing.T) {
	// nil config => 30s default (default-on)
	if got := (&Client{}).effectiveEndpointRefreshInterval(); got != 30*time.Second {
		t.Fatalf("nil-config default: got %v, want 30s", got)
	}
	// explicit override
	c := &Client{}
	c._config.Store(&config{Target: targetData{SdEndpointRefreshSeconds: 5}})
	if got := c.effectiveEndpointRefreshInterval(); got != 5*time.Second {
		t.Fatalf("override: got %v, want 5s", got)
	}
	// zero in config => 30s default
	c2 := &Client{}
	c2._config.Store(&config{Target: targetData{SdEndpointRefreshSeconds: 0}})
	if got := c2.effectiveEndpointRefreshInterval(); got != 30*time.Second {
		t.Fatalf("zero-config: got %v, want 30s", got)
	}
}
