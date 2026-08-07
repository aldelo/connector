package service

/*
 * Copyright 2020-2023 Aldelo, LP
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

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aldelo/common/wrapper/cloudmap"
	"github.com/aldelo/connector/adapters/registry/sdoperationstatus"
)

// fakeClock drives awaitSdOperation without real sleeping: every sleep and every
// simulated API call advances a virtual now, so a "30 second" budget costs no
// wall-clock time in tests and the elapsed-time accounting stays exact.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time        { return c.now }
func (c *fakeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

// installFakeSd swaps the clock and registry seams for one test.
//
// The seams are package globals, so a test that installs fakes must not run in
// parallel with another test that touches them or with a live Serve. Every test in
// this file is serial by construction (no t.Parallel) and production never assigns
// them.
func installFakeSd(t *testing.T, clock *fakeClock, statusFn func(sd *cloudmap.CloudMap, operationId string, timeout ...time.Duration) (sdoperationstatus.SdOperationStatus, error)) {
	t.Helper()

	prevNow, prevSleep, prevStatus := sdNow, sdSleep, sdGetOperationStatusF

	sdNow = clock.Now
	sdSleep = clock.Sleep
	sdGetOperationStatusF = statusFn

	t.Cleanup(func() {
		sdNow, sdSleep, sdGetOperationStatusF = prevNow, prevSleep, prevStatus
	})
}

// installFakeDeregister swaps the DeregisterInstance seam and records what it was
// asked to remove.
func installFakeDeregister(t *testing.T, fn func(sd *cloudmap.CloudMap, instanceId string, serviceId string, timeout ...time.Duration) (string, error)) {
	t.Helper()

	prev := sdDeregisterInstanceF
	sdDeregisterInstanceF = fn

	t.Cleanup(func() { sdDeregisterInstanceF = prev })
}

// assertPersistedInstanceId re-reads the config from disk and checks what was actually
// written, so a test cannot pass on an in-memory mutation that was never saved.
func assertPersistedInstanceId(cfg *config, want string) error {
	reread := &config{AppName: cfg.AppName, ConfigFileName: cfg.ConfigFileName}
	if err := reread.Read(); err != nil {
		return fmt.Errorf("re-reading persisted config: %w", err)
	}
	if reread.Instance.Id != want {
		return fmt.Errorf("persisted instance id is %q, want %q", reread.Instance.Id, want)
	}
	return nil
}

// installFakeRegister swaps the RegisterInstance seam.
func installFakeRegister(t *testing.T, fn func(sd *cloudmap.CloudMap, serviceId string, instancePrefix string, ip string, port uint, healthy bool, version string, timeout ...time.Duration) (string, string, error)) {
	t.Helper()

	prev := sdRegisterInstanceF
	sdRegisterInstanceF = fn

	t.Cleanup(func() { sdRegisterInstanceF = prev })
}

// newTestConfig builds a fully initialised config (viper backed, so SetInstanceId and
// Save actually work) inside a temp dir that Go cleans up.
func newTestConfig(t *testing.T, serviceId string, instanceId string) *config {
	t.Helper()

	t.Chdir(t.TempDir())

	cfg := &config{AppName: "sd-leak-test", ConfigFileName: "newtest"}
	if err := cfg.Read(); err != nil {
		t.Fatalf("config.Read: %v", err)
	}

	cfg.SetServiceId(serviceId)
	cfg.SetInstanceId(instanceId)

	// Persist the seed values. Without this the file on disk still holds an empty
	// instance_id, so asserting that a cleared id was persisted would pass even if the
	// production code never called Save() — the assertion would be comparing "" to the
	// "" that was already there.
	if err := cfg.Save(); err != nil {
		t.Fatalf("seeding config: %v", err)
	}
	if err := assertPersistedInstanceId(cfg, instanceId); err != nil {
		t.Fatalf("seeding config: %v", err)
	}

	return cfg
}

func TestAwaitSdOperation_SuccessReturnsNil(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	calls := 0

	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		calls++
		if calls < 3 {
			return sdoperationstatus.Pending, nil
		}
		return sdoperationstatus.Success, nil
	})

	s := &Service{}

	if err := s.awaitSdOperation(nil, "op-1", 30*time.Second, 5*time.Second, "Test"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 polls, got %d", calls)
	}
}

// Regression for the incident: the wait must be long enough that a registration
// which settles after several seconds is still confirmed rather than abandoned.
// The old budget was a hardcoded 20 x 250ms = 5s.
func TestAwaitSdOperation_DefaultBudgetOutlastsTheOldFiveSeconds(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	settleAt := clock.now.Add(12 * time.Second)

	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		if clock.now.Before(settleAt) {
			return sdoperationstatus.Pending, nil
		}
		return sdoperationstatus.Success, nil
	})

	s := &Service{}
	budget := sdRegisterWaitBudget(0)

	if budget <= 5*time.Second {
		t.Fatalf("default register wait budget is %v, must exceed the 5s it replaces", budget)
	}
	if err := s.awaitSdOperation(nil, "op-1", budget, 5*time.Second, "Test"); err != nil {
		t.Fatalf("a registration settling at 12s must still be confirmed, got %v", err)
	}
}

// The pathological case codex found: counting 250ms sleeps ignores the time each
// GetOperationStatus call itself burns, so a try-count "60 second" budget could run
// for ~21 minutes. The wall-clock deadline must hold even when every call takes the
// full API timeout.
func TestAwaitSdOperation_BudgetHoldsWhenEveryCallBurnsItsApiTimeout(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	start := clock.now
	apiTimeout := 5 * time.Second

	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, timeout ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		// a hung call consumes exactly the timeout it was given
		if len(timeout) > 0 {
			clock.now = clock.now.Add(timeout[0])
		}
		return sdoperationstatus.Pending, nil
	})

	s := &Service{}
	budget := 30 * time.Second

	err := s.awaitSdOperation(nil, "op-1", budget, apiTimeout, "Test")
	if err == nil {
		t.Fatal("expected a timeout error")
	}

	if elapsed := clock.now.Sub(start); elapsed > budget {
		t.Fatalf("wall-clock budget overrun: elapsed %v exceeds budget %v", elapsed, budget)
	}
}

// Every per-call timeout must be clamped to the time left, so one hung call cannot
// push the total past the deadline.
func TestAwaitSdOperation_ClampsCallTimeoutToRemaining(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	budget := 3 * time.Second
	var seen []time.Duration

	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, timeout ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		if len(timeout) > 0 {
			seen = append(seen, timeout[0])
			clock.now = clock.now.Add(timeout[0])
		}
		return sdoperationstatus.Pending, nil
	})

	s := &Service{}
	_ = s.awaitSdOperation(nil, "op-1", budget, 10*time.Second, "Test")

	if len(seen) == 0 {
		t.Fatal("expected at least one call")
	}
	for i, d := range seen {
		if d > budget {
			t.Fatalf("call %d got timeout %v, larger than the whole %v budget", i, d, budget)
		}
	}
}

// The adapter reports a terminal failure as (Fail, non-nil error). Checking the error
// first would make the Fail branch unreachable and retry a permanent failure until the
// budget ran out.
func TestAwaitSdOperation_TerminalFailIsDetectedDespiteAccompanyingError(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	start := clock.now
	calls := 0

	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		calls++
		return sdoperationstatus.Fail, errors.New("boom [ErrCode]")
	})

	s := &Service{}

	err := s.awaitSdOperation(nil, "op-1", 30*time.Second, 5*time.Second, "Test")
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Fatalf("a permanent Fail must not be retried, got %d calls", calls)
	}
	if elapsed := clock.now.Sub(start); elapsed != 0 {
		t.Fatalf("a permanent Fail must return immediately, burned %v", elapsed)
	}
}

// observedHealthyMaxSettle is the slowest Cloud Map registration measured across 132
// registrations in a staggered rollout of the 69 live DAL services on 2026-07-24
// (p50 1.73s, p99 3.16s). The default budget is sized against this number, so a change
// to either should be a conscious decision rather than a drift.
const observedHealthyMaxSettle = 3170 * time.Millisecond

func TestSdRegisterWaitBudget(t *testing.T) {
	if got := sdRegisterWaitBudget(0); got != defaultSdRegisterWaitSeconds*time.Second {
		t.Fatalf("unset budget = %v, want the %ds default", got, defaultSdRegisterWaitSeconds)
	}
	if got := sdRegisterWaitBudget(45); got != 45*time.Second {
		t.Fatalf("configured budget = %v, want 45s", got)
	}
	// The measurement bounds this from below, not from above: it shows the 5s it
	// replaces had barely 1.6x headroom over a healthy rollout, and says nothing about
	// where the contended tail sits — the wait is what truncates that tail, and the
	// give-up instrumentation does not remove the censoring, it only makes the
	// abandoned operation identifiable afterwards. So this asserts a floor, not a
	// derived value; the default is provisional.
	if got := sdRegisterWaitBudget(0); got <= observedHealthyMaxSettle {
		t.Fatalf("default budget %v does not even clear the observed healthy max %v",
			got, observedHealthyMaxSettle)
	}
	if got := sdRegisterWaitBudget(0); got <= 5*time.Second {
		t.Fatalf("default budget %v must exceed the 5s budget it replaces", got)
	}
	// a nonsense configuration must not wrap around into a zero or negative budget;
	// it is clamped to the documented cap, not merely to "something positive"
	if got := sdRegisterWaitBudget(1 << 40); got != maxSdWaitSeconds*time.Second {
		t.Fatalf("absurd configured budget produced %v, want the %ds cap", got, maxSdWaitSeconds)
	}
	if got := sdRegisterWaitBudget(maxSdWaitSeconds + 1); got != maxSdWaitSeconds*time.Second {
		t.Fatalf("budget just over the cap produced %v, want the %ds cap", got, maxSdWaitSeconds)
	}
}

// The status call closest to the deadline is the one most likely to be cut short by
// its own clamped context, and it surfaces as an error rather than a Pending. That
// path must still name the operation, or the instrumentation is blind in exactly the
// case it exists to measure.
func TestAwaitSdOperation_StatusCallErrorIsAlsoDiagnosable(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	var clampedTo time.Duration

	// Budget 7s, API timeout 3s: polls at 0s and ~3.25s succeed as Pending, and the
	// third call is clamped to the ~0.5s that remain — that clamped call is the one
	// that errors, which is the real-world shape this test exists for.
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, timeout ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		calls++
		if len(timeout) > 0 {
			clock.now = clock.now.Add(timeout[0])
			if timeout[0] < 3*time.Second {
				clampedTo = timeout[0]
				return sdoperationstatus.UNKNOWN, errors.New("context deadline exceeded")
			}
		}
		return sdoperationstatus.Pending, nil
	})

	s := &Service{}
	err := s.awaitSdOperation(nil, "op-late", 7*time.Second, 3*time.Second, "Test")
	if err == nil {
		t.Fatal("expected an error")
	}
	if clampedTo == 0 {
		t.Fatalf("no call was ever clamped below the API timeout (calls=%d); the clamped-final-poll path was not exercised", calls)
	}
	for _, want := range []string{"op-late", "outcome unknown", "context deadline exceeded"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("status-call error must contain %q, got: %v", want, err)
		}
	}
	// a status-call error means we do NOT know the operation settled
	if strings.Contains(err.Error(), "reported as failed by AWS") {
		t.Fatalf("an unknown outcome must not be reported as a terminal failure: %v", err)
	}
}

// A terminal Fail is settled, not pending — the give-up wording must not claim
// otherwise, and the timeout wording must not claim AWS reported anything.
func TestAwaitSdOperation_ErrorWordingMatchesWhatIsActuallyKnown(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		return sdoperationstatus.Fail, errors.New("boom")
	})
	s := &Service{}
	err := s.awaitSdOperation(nil, "op-f", 5*time.Second, time.Second, "Test")
	if err == nil || !strings.Contains(err.Error(), "reported as failed by AWS") {
		t.Fatalf("a terminal Fail must be described as settled, got: %v", err)
	}
	if strings.Contains(err.Error(), "may still complete") {
		t.Fatalf("a terminal Fail must not be described as still pending: %v", err)
	}

	clock2 := &fakeClock{now: time.Unix(0, 0)}
	installFakeSd(t, clock2, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		clock2.now = clock2.now.Add(time.Second)
		return sdoperationstatus.Pending, nil
	})
	err = s.awaitSdOperation(nil, "op-t", 3*time.Second, time.Second, "Test")
	if err == nil || !strings.Contains(err.Error(), "may still complete at AWS") {
		t.Fatalf("a give-up must be described as unresolved, got: %v", err)
	}
	if strings.Contains(err.Error(), "reported as failed") {
		t.Fatalf("a give-up must not claim AWS reported a failure: %v", err)
	}
}

// A terminal Fail must be diagnosable too.
func TestAwaitSdOperation_FailErrorIsAlsoDiagnosable(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		return sdoperationstatus.Fail, errors.New("boom [ErrCode]")
	})

	s := &Service{}
	err := s.awaitSdOperation(nil, "op-fail", 10*time.Second, 3*time.Second, "Test")
	if err == nil || !strings.Contains(err.Error(), "op-fail") {
		t.Fatalf("terminal Fail must name the operation, got: %v", err)
	}
}

// Regression for the shutdown side of the same race: if a registration publishes a
// different id while the shutdown deregister is in flight, the shutdown must not clear
// it. Clearing it would leave that instance registered in Cloud Map with nothing
// tracking it — the exact leak this change exists to stop.
//
// Sequence forced here: shutdown submits for A, a registration publishes B while the
// operation polls, A succeeds, and the clear must be a no-op.
func TestDoDeregisterInstance_DoesNotClearAnIdItDidNotRemove(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	cfg := newTestConfig(t, "srv-test", "ams-shutting-down")

	var deregisteredId string
	installFakeDeregister(t, func(_ *cloudmap.CloudMap, instanceId string, _ string, _ ...time.Duration) (string, error) {
		deregisteredId = instanceId
		// a registration confirms and publishes a different id mid-flight
		cfg.SetInstanceId("ams-newly-registered")
		return "op-shutdown", nil
	})
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		return sdoperationstatus.Success, nil
	})

	s := &Service{}
	s._sd = &cloudmap.CloudMap{}
	s._config = cfg

	if err := s.doDeregisterInstance(); err != nil {
		t.Fatalf("doDeregisterInstance: %v", err)
	}

	if deregisteredId != "ams-shutting-down" {
		t.Fatalf("shutdown deregistered %q, want the id it snapshotted at entry", deregisteredId)
	}
	if cfg.Instance.Id != "ams-newly-registered" {
		t.Fatalf("shutdown erased an id it never removed: %q", cfg.Instance.Id)
	}
}

// The synchronization claim needs a test that actually drives both sides at once:
// codex's point was that -race passing proves nothing while no test overlaps startup
// and shutdown. This runs the shutdown deregister against a concurrent writer that
// holds _mu, exactly as the registration path does. If the shutdown path re-read
// cfg.Instance.Id outside the lock, the race detector would fire here.
//
// Deliberately uses the real clock and real sleeps (no fake seams for timing) so the
// two goroutines genuinely interleave.
func TestDoDeregisterInstance_NoRaceWithAConcurrentRegistrationWriter(t *testing.T) {
	cfg := newTestConfig(t, "srv-test", "ams-shutting-down")

	installFakeDeregister(t, func(_ *cloudmap.CloudMap, _ string, _ string, _ ...time.Duration) (string, error) {
		time.Sleep(5 * time.Millisecond)
		return "op-shutdown", nil
	})

	prevStatus := sdGetOperationStatusF
	sdGetOperationStatusF = func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		time.Sleep(2 * time.Millisecond)
		return sdoperationstatus.Success, nil
	}
	t.Cleanup(func() { sdGetOperationStatusF = prevStatus })

	s := &Service{}
	s._sd = &cloudmap.CloudMap{}
	s._config = cfg

	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// mirrors registerInstance's publish: write under _mu
			s._mu.Lock()
			cfg.SetInstanceId("ams-registered-" + strconv.Itoa(i))
			s._mu.Unlock()
		}
	}()

	err := s.doDeregisterInstance()
	close(stop)
	<-done

	if err != nil {
		t.Fatalf("doDeregisterInstance: %v", err)
	}
}

// The ordinary case must still clear and persist.
func TestDoDeregisterInstance_ClearsAndPersistsTheIdItRemoved(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	cfg := newTestConfig(t, "srv-test", "ams-shutting-down")

	installFakeDeregister(t, func(_ *cloudmap.CloudMap, _ string, _ string, _ ...time.Duration) (string, error) {
		return "op-shutdown", nil
	})
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		return sdoperationstatus.Success, nil
	})

	s := &Service{}
	s._sd = &cloudmap.CloudMap{}
	s._config = cfg

	if err := s.doDeregisterInstance(); err != nil {
		t.Fatalf("doDeregisterInstance: %v", err)
	}
	if cfg.Instance.Id != "" {
		t.Fatalf("id not cleared after a confirmed shutdown deregister: %q", cfg.Instance.Id)
	}
	if err := assertPersistedInstanceId(cfg, ""); err != nil {
		t.Fatal(err)
	}
}

// Causal coverage of the other half of the fix: registerInstance must use the
// CONFIGURED budget, must not publish the id before Cloud Map confirms, and must
// publish and persist it once it does.
func TestRegisterInstance_PublishesIdOnlyAfterConfirmation(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	cfg := newTestConfig(t, "srv-test", "")
	cfg.SetSdRegisterWaitSeconds(20)
	cfg.Instance.AutoDeregisterPrior = false

	settleAt := clock.now.Add(9 * time.Second)
	var idDuringWait []string

	installFakeRegister(t, func(_ *cloudmap.CloudMap, serviceId string, _ string, _ string, _ uint, _ bool, _ string, _ ...time.Duration) (string, string, error) {
		if serviceId != "srv-test" {
			t.Errorf("registered against service %q, want srv-test", serviceId)
		}
		return "ams-new-instance", "op-reg", nil
	})
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		idDuringWait = append(idDuringWait, cfg.Instance.Id)
		if clock.now.Before(settleAt) {
			return sdoperationstatus.Pending, nil
		}
		return sdoperationstatus.Success, nil
	})

	s := &Service{}
	s._sd = &cloudmap.CloudMap{}
	s._config = cfg

	if err := s.registerInstance("10.0.0.1", 8080, true, "v1.0.0"); err != nil {
		t.Fatalf("registerInstance: %v", err)
	}

	for i, id := range idDuringWait {
		if id != "" {
			t.Fatalf("poll %d saw published id %q while the registration was still unconfirmed", i, id)
		}
	}
	if cfg.Instance.Id != "ams-new-instance" {
		t.Fatalf("confirmed id not published, got %q", cfg.Instance.Id)
	}
	// and it must reach disk, or the next launch cannot clean it up
	if err := assertPersistedInstanceId(cfg, "ams-new-instance"); err != nil {
		t.Fatal(err)
	}
}

// A registration that outlives the configured budget must leave nothing published —
// the health-report and SNS goroutines are already running and would otherwise
// advertise an id Cloud Map never confirmed.
func TestRegisterInstance_TimeoutPublishesNothingAndHonoursTheConfiguredBudget(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	start := clock.now
	cfg := newTestConfig(t, "srv-test", "")
	cfg.SetSdRegisterWaitSeconds(8)
	cfg.Instance.AutoDeregisterPrior = false

	installFakeRegister(t, func(_ *cloudmap.CloudMap, _ string, _ string, _ string, _ uint, _ bool, _ string, _ ...time.Duration) (string, string, error) {
		return "ams-never-confirmed", "op-slow", nil
	})
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		return sdoperationstatus.Pending, nil
	})

	s := &Service{}
	s._sd = &cloudmap.CloudMap{}
	s._config = cfg

	err := s.registerInstance("10.0.0.1", 8080, true, "v1.0.0")
	if err == nil {
		t.Fatal("expected registerInstance to fail once the budget expired")
	}
	if cfg.Instance.Id != "" {
		t.Fatalf("an unconfirmed registration was published as %q", cfg.Instance.Id)
	}
	if err := assertPersistedInstanceId(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(err.Error(), "op-slow") {
		t.Fatalf("the failure must name the still-pending operation, got: %v", err)
	}
	// the CONFIGURED 8s budget must be what bounded the wait, not the 30s default
	if elapsed := clock.now.Sub(start); elapsed > 8*time.Second {
		t.Fatalf("wait ran %v, longer than the configured 8s budget", elapsed)
	}
	if elapsed := clock.now.Sub(start); elapsed < 7*time.Second {
		t.Fatalf("wait ran only %v, the configured 8s budget was not used", elapsed)
	}
}

// Regression, exercised through the real configured path: the prior-instance cleanup
// must deregister the prior id explicitly, must clear it afterwards, and must NOT
// consume the one-shot deregister claim — consuming it is what made the shutdown
// deregister a silent no-op and let the instance outlive the process.
//
// An empty &Service{} would return before doing any of this and would stay green even
// if the claim were consumed again, so this test wires up a real config and fake AWS.
func TestDeregisterPriorInstance_RealPath(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		return sdoperationstatus.Success, nil
	})

	var gotInstanceId, gotServiceId string
	calls := 0
	installFakeDeregister(t, func(_ *cloudmap.CloudMap, instanceId string, serviceId string, _ ...time.Duration) (string, error) {
		calls++
		gotInstanceId, gotServiceId = instanceId, serviceId
		return "op-dereg", nil
	})

	cfg := newTestConfig(t, "srv-test", "ams-prior-instance")

	s := &Service{}
	s._sd = &cloudmap.CloudMap{}
	s._config = cfg

	s.deregisterPriorInstance()

	if calls != 1 {
		t.Fatalf("expected exactly 1 deregister call, got %d", calls)
	}
	if gotInstanceId != "ams-prior-instance" {
		t.Fatalf("deregistered %q, want the explicit prior id", gotInstanceId)
	}
	if gotServiceId != "srv-test" {
		t.Fatalf("deregistered against service %q, want srv-test", gotServiceId)
	}
	if cfg.Instance.Id != "" {
		t.Fatalf("prior id %q not cleared after a confirmed cleanup", cfg.Instance.Id)
	}
	// the clear must be persisted, not just in memory, or the next launch chases the
	// same dead id again
	if err := assertPersistedInstanceId(cfg, ""); err != nil {
		t.Fatal(err)
	}
	if s._deregFired.Load() {
		t.Fatal("prior cleanup consumed the one-shot claim; the shutdown deregister would be a silent no-op")
	}
	if !s._deregFired.CompareAndSwap(false, true) {
		t.Fatal("expected the claim to still be available to the shutdown deregister")
	}
}

// Compare-and-clear: if a registration replaced the id while the prior cleanup was in
// flight, the cleanup must not erase the new one.
func TestDeregisterPriorInstance_DoesNotEraseANewerId(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	cfg := newTestConfig(t, "srv-test", "ams-prior-instance")

	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		return sdoperationstatus.Success, nil
	})
	installFakeDeregister(t, func(_ *cloudmap.CloudMap, _ string, _ string, _ ...time.Duration) (string, error) {
		// a registration lands while the deregister operation is in flight
		cfg.SetInstanceId("ams-new-instance")
		return "op-dereg", nil
	})

	s := &Service{}
	s._sd = &cloudmap.CloudMap{}
	s._config = cfg

	s.deregisterPriorInstance()

	if cfg.Instance.Id != "ams-new-instance" {
		t.Fatalf("prior cleanup erased a newer registration: id is %q", cfg.Instance.Id)
	}
}

// A failed prior cleanup must not clear the id and must not abort startup.
func TestDeregisterPriorInstance_FailureLeavesIdForTheBackstop(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, _ ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		return sdoperationstatus.Pending, nil
	})
	installFakeDeregister(t, func(_ *cloudmap.CloudMap, _ string, _ string, _ ...time.Duration) (string, error) {
		return "", errors.New("cloud map unavailable")
	})

	cfg := newTestConfig(t, "srv-test", "ams-prior-instance")

	s := &Service{}
	s._sd = &cloudmap.CloudMap{}
	s._config = cfg

	s.deregisterPriorInstance()

	if cfg.Instance.Id != "ams-prior-instance" {
		t.Fatalf("a failed cleanup must keep the id so it can be retried or reaped, got %q", cfg.Instance.Id)
	}
	if s._deregFired.Load() {
		t.Fatal("a failed cleanup must still leave the shutdown claim available")
	}
}

// The give-up path must name the still-pending operation and the measured elapsed
// time, otherwise the real settle-time tail stays unobservable — which is exactly why
// the incident could not be sized from the logs.
func TestAwaitSdOperation_TimeoutErrorIsDiagnosable(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	installFakeSd(t, clock, func(_ *cloudmap.CloudMap, _ string, timeout ...time.Duration) (sdoperationstatus.SdOperationStatus, error) {
		if len(timeout) > 0 {
			clock.now = clock.now.Add(timeout[0])
		}
		return sdoperationstatus.Pending, nil
	})

	s := &Service{}
	err := s.awaitSdOperation(nil, "op-abc123", 10*time.Second, 3*time.Second, "Test")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !strings.Contains(err.Error(), "op-abc123") {
		t.Fatalf("timeout error must name the abandoned operation id, got: %v", err)
	}
	if !strings.Contains(err.Error(), "may still complete at AWS") {
		t.Fatalf("timeout error must say the operation outlives the wait, got: %v", err)
	}
}
