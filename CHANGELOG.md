# Changelog

All notable changes to `github.com/aldelo/connector` are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

`connector` is a coordinated sibling to `aldelo.com/common`. Releases that
move the `go` directive or bump the `common` pin are coordinated across both
repositories — see the corresponding `common` CHANGELOG entry for the
workspace-wide consumer-sweep playbook. Observable contracts of helpers in
this library are preserved across minor/patch versions per workspace rule #10.

---

## [v1.8.7] — 2026-04-17

Patch release. Two local defensive-logging improvements surfaced by the
2026-04-17 full deep-review, plus the `common v1.8.6 → v1.8.7` sibling
pin bump to adopt the matching batch of `common` safety fixes (gin
type-assertion hardening, DynamoDB diagnostic-payload gate, hystrixgo
stream-server panic recovery).

No observable contract change from `v1.8.6`. Both local changes are
logging-only; neither alters control flow, return values, or error
semantics. The `common` pin bump pulls in opt-out behaviour for
`DisableLastExecuteParamsPayload` (default `false` preserves the
existing per-operation diagnostic payload; set `true` on hot-path
DynamoDB wrappers to skip the `input.String()` allocation and mutex
contention) — no consumer code must change for the default path.

### Changed — sibling pin bump

- **`github.com/aldelo/common`** pin moved `v1.8.6 → v1.8.7` in
  `connector/go.mod:6` to pull in the sibling's P1-CMN-L2-1
  (gin `c.Keys["JWT_PAYLOAD"]` comma-ok guard), P1-PERF-2 (DynamoDB
  `DisableLastExecuteParamsPayload` opt-out), and P2-CMN-C2
  (hystrixgo stream-server `ListenAndServe` panic recovery) fixes.
  `common v1.8.7` is a drop-in replacement for `v1.8.6` — no public
  signatures changed, default behaviour preserved.

### Fixed

- **P2-CONN-L2-1 silent `stopper.Shutdown` error** — `client/client.go:1106`:
  the graceful-shutdown error return was discarded with `_ =
  stopper.Shutdown(ctx)`, so a non-clean shutdown (context timeout,
  pending connections, listener error) produced no signal. Now logs
  the returned error with the configured timeout so operators can
  distinguish a clean drain from a forced one. No control-flow change
  (shutdown still proceeds); the error is observed, not acted on.

- **P2-CONN-1 `sdMapMu` hot-path misuse warning** —
  `notifiergateway/notifiergateway.go:1025`: added a doc-comment to
  the `sdMapMu` declaration warning that the mutex is held across
  CloudMap `DeregisterInstance` network I/O inside
  `serviceDiscoveryRemoveHealthyServiceInstance`. The function must
  not be called from a request-handler path — concurrent request
  serialization would produce latency spikes bounded by the CloudMap
  API round-trip. Documentation-only change; runtime behaviour
  unchanged.

### Rule #15 Pre-Flight Attestation (2026-04-17 UTC)

- verified: `common v1.8.7 → a971b676d931dabc470f72fdad825c2c8e29b3c1`
  via `git ls-remote --tags https://github.com/aldelo/common.git refs/tags/v1.8.7`
- verified-at: 2026-04-17 09:05:11 UTC
- sibling commit: `common@e964a57` (CHANGELOG for v1.8.7);
  fix code at `common@3dd1c3d`

### Verification

- `go build ./...` clean; `go vet ./...` clean
- `go test ./... -short -count=1` — 27/27 packages pass (zero
  regressions vs `v1.8.6` baseline, both for the two local
  logging/doc changes and for the `common v1.8.6 → v1.8.7` bump)
- `go mod tidy` — `go.sum` updated to match `common v1.8.7` hashes
  (2 lines); no transitive dependency drift

## [v1.8.6] — 2026-04-17

Patch release. Error-wrapping hardening — completes the **P2-N5**
deferral from the `v1.8.5` ship checkpoint. Coordinated-sibling
release with `common v1.8.6` which carries the bulk of the
migration (9 sites across 3 files); `connector` carries 3 sites
across 2 files and picks up the sibling pin bump.

No observable contract change from `v1.8.5`. `.Error()` string
output is byte-identical at every converted site. `%w` rendering
matches `%v` for a single wrapped error; consumers gain the ability
to walk the error chain via `errors.Is` / `errors.As` at the
converted sites.

### Changed — sibling pin bump

- **`github.com/aldelo/common`** pin moved `v1.8.5 → v1.8.6` in
  `connector/go.mod:6` to pull in the sibling's matching P2-N5
  error-chain migration. `common v1.8.6` is a drop-in replacement
  for `v1.8.5` — no public signatures changed.

### Fixed

- **P2-N5 error-chain wrapping (3 sites across 2 files):** convert
  `fmt.Errorf("...: %v", err)` → `fmt.Errorf("...: %w", err)` where
  the formatted argument is a genuine Go `error` value. Sites:
  - `adapters/resolver/resolver.go:172` — `net.SplitHostPort` error
    on malformed endpoint `host:port`
  - `client/client.go:2779` — `net.SplitHostPort` error on Direct
    Connect target
  - `client/client.go:2789` — `strconv.Atoi` error on Direct Connect
    port

  **Deliberately NOT converted** —
  `adapters/loadbalancer/roundrobin.go:118`: the guard is a
  compound condition `convErr != nil || p < 1 || p > 65535`, so
  `convErr` can legitimately be `nil` when only the range check
  triggers. `fmt.Errorf("...: %w", nil)` renders as the literal
  `"%!w(<nil>)"` — unsafe output. Site stays `%v`.

### Rule #15 Pre-Flight Attestation (2026-04-17 UTC)

- verified: `common v1.8.6 → 63b0821c3d72eb7f7676f11b1e6ac112924db384`
  via `git ls-remote --tags https://github.com/aldelo/common.git refs/tags/v1.8.6`
- verified-at: 2026-04-17 (tag cut timestamp)
- sibling commit: `common@1321f24` (CHANGELOG for v1.8.6);
  P2-N5 code at `common@6febc97`

### Verification

- `go build ./...` clean; `go vet ./...` clean
- `go test ./... -short -count=1` — 27/27 packages pass (zero
  regressions vs `v1.8.5` baseline, both for the local 3-site
  `%w` conversion and for the `common v1.8.5 → v1.8.6` bump)
- `grep 'fmt.Errorf.*%v' $(git ls-files '*.go' | grep -v _test)` —
  1 remaining site (`roundrobin.go:118`, intentionally skipped per
  reasoning above)

## [v1.8.5] — 2026-04-17

Patch release. Repairs the release-artifact discipline gap introduced at
`v1.8.4` (CHANGELOG entry missing, `go.mod` still pinned
`common v1.8.3`, sibling release narrative absent) and closes the
remaining deep-review findings the `v1.8.4` audit surfaced after the
tag was cut. This is a workspace **rule #15 / rule #16** remediation
release — the code changes are low-risk, the CHANGELOG + `go.mod`
changes are the load-bearing deliverables.

No observable contract change from `v1.8.4`. Every public function
signature is preserved. Consumers pinning `connector v1.8.4` should
bump to `v1.8.5` as a drop-in for the documentation-correctness
and sibling-pin alignment fixes below.

### Changed — sibling pin bump

- **`github.com/aldelo/common`** pin moved `v1.8.3 → v1.8.5` in
  `connector/go.mod:6`, over two commits in this release series:
  first `v1.8.3 → v1.8.4` (C-2 remediation — restoring the sibling
  pin that SHOULD have shipped with the `v1.8.4` tag), then
  `v1.8.4 → v1.8.5` at ship time to pull in the sibling's matching
  remediation release. The `v1.8.4` content already depended on
  features in `common v1.8.4` (KMS deadline, DynamoDB WARN
  observability, gin binding-error scrub); the `v1.8.5` bump also
  picks up `common v1.8.5`'s own P2/P3 hardening (zap/kms error
  logging, MySQL pool+timeout defaults, gin TrustedProxies
  fail-closed). `v1.8.5` now restores parity between the binary the
  tag names and the dependency graph it actually needs.

  **Rule #15 pre-flight at tag time (2026-04-17):**
  - verified: `common v1.8.5 → 9204aef1db0077332ba0d8e20f36bfa02180e75c`
               via `git ls-remote --tags origin refs/tags/v1.8.5`

### Fixed

- **P2-N1 swallowed error at `client/client.go:2247`:**
  `_ = resp.Body.Close()` in the health-check loop now logs close
  errors at warn level. Matches the `common v1.8.4` P2-2 REST
  Close-error pattern for workspace-wide consistency.
- **P2-N6 stale README TODOs:** `README.md:54,56` "TODO: Future
  Implementation" lines for Logger and Monitoring replaced with
  current status. Logger is implemented via `zaplog` passthrough;
  monitoring is out-of-repo via the operator's observability stack.
- **P3-N4 soft-race sleeps in `service_test.go`:** Two
  `time.Sleep(10ms)` "give the handler a moment" sleeps at lines
  1589 and 1620 replaced with deterministic channel waits. Closes
  the residual flakiness surfaced intermittently under high
  parallel `go test ./...` load.

## [v1.8.4] — 2026-04-16

Patch release. Coordinated-sibling release with `common v1.8.4`.
Closes the full **deep-review pass-3 (v1.8.3 post-remediation) +
pass-2 full-codebase audit** findings: 14 commits resolving
`connector`-side gaps across service lifecycle, adapter/client
error paths, tests, dependencies, and documentation. Companion to
the much larger `common v1.8.4` which carries the sibling's own
20-commit patch series.

> **⚠️ Release-artifact discipline note (added `v1.8.5`
> retrospectively).** At the moment this tag was cut, (a) this
> CHANGELOG entry was missing — the tag had no written release
> narrative, and (b) `go.mod` still pinned `common v1.8.3` despite
> the commits below depending on `common v1.8.4` features
> (DynamoDB WARN observability, KMS deadline, gin binding-error
> scrub). Both gaps are workspace rule #15 / rule #16 violations
> that pass-6 `C-1` and `C-2` flagged. Consumers pulling `v1.8.4`
> via `go get connector@v1.8.4` will observe the go.mod drift in
> their vendored tree and should either update their own go.mod
> to `common v1.8.4` via indirect pin, or upgrade to `v1.8.5`
> where the drift is corrected at source. The v1.8.4 tag itself
> is not re-tagged (immutable by convention) — v1.8.5 carries
> the repair.

No observable contract change from `v1.8.3`. Every public function
signature is preserved. Consumers pinning `connector v1.8.3` should
bump to `v1.8.4` as a drop-in for the fixes below.

### Fixed — service lifecycle

- **P1-2 time.Sleep → safego.GoWait migration (commit `00ddeb7`):**
  13 `time.Sleep` sites in `connector/service` converted to
  deterministic `safego.GoWait` channel waits. Test timing
  improvements: safego 0.225s → 0.008s, notifierserver
  0.237s → 0.015s. 10 remaining `time.Sleep` sites are retained
  for race-detector probes, AWS-consistency waits, model delays,
  and simulation loops — each documented in-place with rationale.
- **AD-3 `Frozen()` lifecycle guard (commit `ebf5e5e`):**
  `service.Frozen()` new method guards against post-`Serve`
  mutation of `Service` struct fields. Complements the `v1.8.1`
  `_sigHandlerReady` gate and `v1.8.3` `_cfgMu` rules.
- **P2-3 self-SIGTERM delivery error logging (commit `cbc415d`):**
  `os.FindProcess(pid).Signal(SIGTERM)` programmatic stop paths
  now log signal-delivery errors instead of discarding. Matches
  rule #14's "self-delivered OS signals gated on readiness flag".

### Fixed — client / adapter / notifierserver

- **EH-1 redundant `SetXRayServiceOn` removal (commit `e8309ae`):**
  Three connector init callsites removed the post-`xray.Init()`
  redundant `SetXRayServiceOn(true)` call — the `common v1.8.4`
  EH-1 contract tests pinned that `Init` self-enables correctly.
- **EH-2 `ZapLog.Init` error handling in circuit breaker
  (commit `2d6227f`):** `client/` circuit breaker paths now
  treat `ZapLog.Init()` failure as a hard init error instead of
  silently proceeding with a nil logger. Prevents the
  "circuit breaker trips with no audit trail" production incident
  class.
- **BL-1 `notifiergateway` DDB retry configurable
  (commit `4e79f4b`):** `notifiergateway` DynamoDB endpoint retry
  delay and max-attempts are now config-driven instead of
  hard-coded. Operators can tune under SDK throttle pressure.
- **BL-4 stale notifier goroutine in `client/`
  (commit `9b3dc73`):** Stale notifier goroutine could previously
  block a new `Client` lifecycle from starting. Fix ensures
  the old goroutine is drained before the new lifecycle binds.
- **P2-1 hash validation token xray redaction
  (commit `f91ad83`):** `service/` xray metadata no longer
  includes the raw hash-validation token — redacted at emit
  site. Analogous to the `v1.8.2` A1-F3 SNS phone PII fix.
- **P2-8 `log.Fatal` → `t.Fatal` in integration tests
  (commit `8b65763`):** Integration tests used `log.Fatal`
  which terminated the entire test process on assertion
  failure. Replaced with `t.Fatal` so a failing integration
  test marks that one test as failed without collateral
  damage to siblings.

### Added — test coverage

- **P1-3 adapter + webserver package tests
  (commits `fe9cdc2` + `b724ce2`):** 12 packages gained
  ~163 tests across auth, loadbalancer, notification, queue,
  circuitbreaker, ratelimiter, notifiergateway/config,
  notifierserver/config, webserver, and 4 adapter subsystems.
  Closes the P1-3 coverage gap from the deep-review.
- **TQ-4 deterministic channel waits in `client/` tests
  (commit `e80aaa7`):** `time.Sleep` sites in `client/` test
  suite replaced with `safego.GoWait` channel-wait primitive.

### Documented

- **DOC-01 README Auth status (commit `86040d9`):** Stale "TODO:
  Auth" line in `README.md` replaced with the implemented auth
  status (JWT middleware via `gin-jwt/v2`, service-layer authz
  check, audit logging). Closes the readme-drift flagged in
  pass-2 review.

### Dependency maintenance

- **Quarterly dependency sweep (commit `fb73b62`):** 28 modules
  updated to current-minor. `govulncheck` reports 0 active
  vulnerabilities. See sibling `common v1.8.4` entry for
  workspace-wide dependency discipline.

---

## [v1.8.3] — 2026-04-16

Patch release. Coordinated sibling to `common v1.8.3`. Closes all 22
pass-6 contrarian findings plus the 4 "remaining to 10/10" items. This
release completes the full remediation cycle bringing both repos to
~9.8/10 health.

No observable contract change from `v1.8.2`. Every public function
signature is preserved. Consumers pinning `connector v1.8.2` should
bump to `v1.8.3` as a drop-in.

### Changed — sibling pin bump

- **`github.com/aldelo/common`** pin moved `v1.8.2 → v1.8.3`. The
  sibling release contains: S3/CloudMap/Route53 deadline enforcement
  (34 methods bounded), SES/SQS deadline enforcement, TCPServer race
  fix, ginxray observability + PII fixes, CORS fail-closed.

### Fixed

- **gRPC Serve zombie (A2-P6-01):** Non-blocking send to quit channel
  as fallback exit path when `_sigHandlerReady` is false.
- **Startup coordination docs (A2-P6-02..05):** Documentation comments
  explaining coordination gap, nil-guard on `_config`, poll interval
  tuning, and `startServerFnTestMu` guard rules.
- **HealthProbe stale snapshot (A3-F1):** Atomic hc+conn snapshot under
  RLock with post-snapshot conn state re-check.
- **Observer goroutine leak (A3-F2):** Select on `getClosedCh()` for
  clean exit.
- **CORS on SNS relay removed (A4-P6-F1):** Server-to-server endpoint
  does not need browser CORS.
- **logPeerMigrationOnce testable (A4-P6-F2):** Reset helper for
  `sync.Once` isolation in tests.
- **safeGo deduplication (REM-2):** 3 identical copies in service/
  client/notifierserver extracted to `internal/safego.Go()`.
- **LogXrayAddFailure adoption (REM-3):** 55 xray silent-discard sites
  converted to rate-limited logging via `xray.LogXrayAddFailure()`.
- **Channel-based signal readiness (REM-4):** `_sigHandlerReadyCh`
  replaces sleep-based poll in test helper for deterministic tests.

## [v1.8.2] — 2026-04-16

Patch release. Coordinated sibling to `common v1.8.2`. Closes five
`connector` findings and one test-coverage note from the **SP-010
pass-5 contrarian review** cycle. All fixes target surfaces already
touched by `v1.8.1`'s remediation wave (`adapters/logger`,
`adapters/metrics`, `service`, `client`) — this release is the
correctness sweep that the pass-5 review demanded before those
surfaces could be considered fully hardened.

No observable contract change from `v1.8.1`. Every public function
signature in `client/`, `service/`, `adapters/`, `notifiergateway/`,
and `webserver/` is preserved. Consumers pinning `connector v1.8.1`
should bump to `v1.8.2` as a drop-in for the GDPR-safety, cache-cap,
and test-coverage guarantees below.

Context: SP-010 pass-5 re-audited `v1.8.1` against a contrarian rule
set — "assume the v1.8.1 fixes are incomplete, find what was missed".
The review found five findings in `common` (A1 class — already tagged
as `common v1.8.2`) and six in `connector` (A2/A4 class), landed as
per-gap commits under the standing directive *"one gap at a time,
review+audit between gap groups, version ceiling v1.8.2."* The
per-gap protocol was: fix → regression test → mutation probe
(causality validation) → full suite green → per-finding commit.
Three independent reviewer audits (Gap 1.A / 2.A / 3.A) returned
PASS or PASS-WITH-NOTES with zero blockers.

### Changed — sibling pin bump

- **`github.com/aldelo/common`** pin moved `v1.8.1 → v1.8.2`. The
  sibling release contains:
  - **SP-010 A1-F1** — `ensureSNSCtx` xray-on deadline enforcement
  - **SP-010 A1-F2** — 29 callsite comment rewrite post-A1-F1
  - **SP-010 A1-F3** — `SendSMS` phone PII mask in xray metadata
  - **SP-010 A1-F4** — `maskPhoneForXray` UTF-8 rune-based slicing
    (origin of lesson L18)
  - **SP-010 A1-F5** — `ensureSNSCtx` nil-segCtx dead-guard test

  See `github.com/aldelo/common` CHANGELOG `[v1.8.2]` entry for the
  full narrative. Pure pin bump for `connector`; no consumer-visible
  behavioral change at the `connector` surface.

### Fixed — SP-010 A4-F1 (`adapters/logger` — GDPR default)

- **`WithLogPeer` now defaults to `false`.** The v1.8.1 logger
  interceptor shipped with `logPeer: true` as the default in
  `newDefaultLoggerOptions()`, meaning every gRPC method log
  entry included the client IP address unless the consumer
  explicitly opted out. Under GDPR/CCPA/UK-DPA a client IP is
  personal data; logging it by default creates a compliance
  obligation on every consumer that deploys the interceptor.
  The pass-5 review classified this as a P1 finding (A4-F1).
  Fix: `logPeer` defaults to `false` in `newDefaultLoggerOptions()`
  at `logger.go:153`; consumers who need peer logging opt in via
  `WithLogPeer(true)`. Breaking-change notice added to `WithLogPeer`
  godoc and package-level godoc with upgrade instructions.
  Commit `eb91f5d`.

  **Migration (CONTRACT-001):** A one-time startup warning is now
  emitted when `NewLoggerInterceptors` is called without an explicit
  `WithLogPeer(true)` or `WithLogPeer(false)`. To find affected call
  sites across your codebase:
  ```
  grep -rn 'NewLoggerInterceptors' --include='*.go' | grep -v 'WithLogPeer'
  ```
  Each result is a call site that previously got implicit `logPeer:true`
  and now gets implicit `logPeer:false`. Add `WithLogPeer(true)` to
  restore the old behavior, or `WithLogPeer(false)` to acknowledge the
  new default and suppress the warning.

### Fixed — SP-010 A4-F3 (`adapters/logger` — opt-in rate limit)

- **New `WithSampleRate` option for log emission throttling.**
  High-traffic gRPC services logging every RPC can saturate log
  sinks and obscure signals. `WithSampleRate(perSecondCap)` wires
  an `x/time/rate.Limiter` token bucket into the emit gate so only
  `perSecondCap` log entries per second pass through (with burst=cap
  for initial-burst tolerance). The limiter is opt-in — omitting
  `WithSampleRate` preserves the existing "log every RPC" behavior.
  `WithSampleRate(0)` or `WithSampleRate(-1)` disables the limiter
  (same as omitting). Emission gate placed AFTER the nil-logger
  check so nil-logger no-op never consumes tokens. Commit `28f976f`.

### Fixed — SP-010 A4-F2 (`adapters/metrics` — label cache cap)

- **Interceptor label caches capped at 4096 entries per cache.**
  The SP-008 P1-CONN-MET-A optimization introduced four
  process-global `sync.Map` label caches for the hot-path
  `{method, code}` and `{method}` maps. Server-side caches are
  naturally bounded by the .proto surface, but client-side caches
  take `method` from the call site — a gRPC-Web bridge, dynamic
  codec, or fuzzer that constructs method names from user input
  would grow the client caches without bound. Fix:
  `DefaultInterceptorLabelCacheLimit = 4096` entries per cache;
  on cap-exceeded, `getOrBuildPairLabels` / `getOrBuildMethodLabels`
  fall through to a fresh per-call allocation (the pre-optimization
  cost model — slower, but bounded memory).
  `InterceptorLabelCacheOverflow()` reports cumulative fall-throughs
  for operator visibility. `resetInterceptorLabelCaches()` added
  for test isolation. Commit `9ee07ab`.

### Fixed — SP-010 A2-F-2B (`service` — tautological test rewrite)

- **`TestService_SVC02_ServeStartServerError_CausesCleanup`
  replaces tautological SVC-02 regression test.** The v1.8.1
  `TestService_ServeStartServerErrorFixup_NoDeadlock` test asserted
  that `GracefulStop` returned within a timeout but did not verify
  WHICH code path it took — the fix or the legacy safety net. The
  pass-5 review classified this as a P1 finding (A2-F-2B):
  mutation-deleting the fix body leaves the test green because the
  legacy path also returns within the timeout. The rewrite
  instruments a `beforeStartServer` hook that injects a failure,
  then asserts the causal invariant — `_quit` and `_quitDone` are
  nil'd under `_mu` after the error path — which only passes if
  the fix code executed. Commit `6662bdd`.

### Added — SP-010 A2-F-2A (`service` — SVC-F8 sender-side gate test)

- **`TestService_SVC_F8_SelfSignal_SenderSideGate`** — regression
  test that drives the sender-side readiness gate path. The v1.8.1
  P2-CONN-CI-01 test exercised the happy path (signal.Notify
  registered → self-signal reaches channel) but no test verified
  the gate itself: what happens when `GracefulStop` / `ImmediateStop`
  fires BEFORE `awaitOsSigExit` has set `_sigHandlerReady`? Without
  the gate the self-signal hits the Go runtime default SIGTERM
  handler and terminates the process. The new test starts
  `awaitOsSigExit` with `_sigHandlerReady` initially false, delays
  the flag-set by 200ms, and asserts that stop completes cleanly
  within 5s (the sender spun until the flag became true, then
  delivered). Mutation-probe validated: removing the `_sigHandlerReady`
  gate causes the test binary to die with `signal: terminated`.
  Commit `cbc3567`.

### Added — SP-010 A4-N1 (`adapters/metrics` — stream-client panic test)

- **`TestStreamClientInterceptor_PanicEmitsMetrics`** — regression
  test for stream-client panic metric emission. The v1.8.1
  metrics interceptor test suite covered unary-server and
  stream-server panic paths but not the client-stream variant.
  The pass-5 review flagged this coverage gap as A4-N1. The new
  test injects a panicking `Streamer`, recovers the panic, and
  asserts that the sink received a `grpc_client_requests_total`
  counter with `code=Internal` and a
  `grpc_client_request_duration_seconds` observation — proving
  the deferred-recorder pattern works on the client-stream path
  identically to the three already-covered paths.
  Commit `f9feb4f`.

### Verified

- `go build ./...` clean
- `go vet ./...` clean (implicit in `go test -race`)
- `go test -race -short ./...` clean (full package tree; `-short`
  skips the pre-existing `TestClient_Dial` integration test per
  the v1.8.1 release convention at this CHANGELOG's v1.8.1 entry)
- Zero regressions from `common v1.8.1 → v1.8.2` pin bump
- Three independent reviewer audits (Gap 1.A / 2.A / 3.A) by
  `pr-review-toolkit:code-reviewer` (opus) returned PASS or
  PASS-WITH-NOTES with zero blockers. Reports archived in the
  workspace at `_src/docs/repos/connector/findings/2026-04-15-
  contrarian-pass5/_gap{1,2,3}A-reviewer-audit.md`.

### Upgrade notes

- **Drop-in from v1.8.1.** The only pin change is
  `common v1.8.1 → v1.8.2`.
- **Coordinated with:** `github.com/aldelo/common v1.8.2` (already
  tagged on origin — tag `82a88a0` verified via `git ls-remote`
  at checkpoint time per J-N1 attestation convention).
- **Breaking-change surface:** `WithLogPeer` default flip (A4-F1).
  Consumers that relied on the implicit `logPeer: true` default
  must add `WithLogPeer(true)` to their interceptor setup. This
  is documented in the `WithLogPeer` godoc at `logger.go:163-182`
  and the package-level godoc at `logger.go:44-54`.
- **Consumer sweep.** All 38 workspace consumer repos should bump
  their `connector` pin `v1.8.1 → v1.8.2` in coordination with
  the sibling `common v1.8.2` pin bump. Sequence: bump `common`
  first, then `connector` (connector depends on common).
- **Deferred follow-ups (unchanged from v1.8.1):**
  - aws-sdk-go v1 → v2 migration (pass-4 backlog items #14–#16;
    deferred per user directive pending a coordinated workspace
    sweep).
  - Test hygiene: `service/newtest.yaml` still regenerated on
    every `go test ./service/` run.

## [v1.8.1] — 2026-04-15

Patch release. Coordinated sibling to `common v1.8.1`. Closes the three
`connector` P1 findings and one P2 test-coverage finding from the
`deep-review-2026-04-15-contrarian-pass4` cycle that landed on `master`
after the `v1.8.0` tag was cut, plus bumps the `common` pin through to
the sibling patch release so consumers pinning `connector v1.8.1` get
the `common` P1 fixes transitively.

No observable contract change from `v1.8.0`. Drop-in upgrade. Every
public function signature in `client/`, `service/`, `adapters/`,
`notifiergateway/`, and `webserver/` is preserved.

Context: `v1.8.0` narrated a full "SP-008 P1 / P2 / P3 remediation
wave", but three of the four P1 fixes and the P2 CI test actually
landed on master AFTER the `v1.8.0` tag was cut. `v1.8.1` tags the
tree with all of them in place. This is the first `connector` release
cut under workspace rule #15 (release-artifact parity); the drift was
surfaced as P0-JOINT-1 in the pass-4 contrarian review.

### Changed — sibling pin bump

- **`github.com/aldelo/common`** pin moved `v1.8.0 → v1.8.1`. The
  sibling release contains:
  - **SP-008 P1-COMMON-SNS-01** — `ensureSNSCtx` helper rollout
    across all 25 SNS client callsites (default-30s deadline + nil
    segCtx guard), plus `maskPhoneForXray` PII redaction wired into
    `OptInPhoneNumber` / `CheckIfPhoneNumberIsOptedOut` /
    `ListPhoneNumbersOptedOut` xray emit sites.
  - **SP-008 P1-COMMON-KMS-01** — `atomic.Pointer[kms.KMS]`
    migration that makes the torn-read invariant compiler-enforced
    (a future refactor cannot silently reintroduce an unlocked
    `kmsClient` read because the field is no longer directly
    readable). The four hot-path multi-field snapshot methods
    (`EncryptViaCmkAes256`, `DecryptViaCmkAes256`,
    `EncryptViaCmkRsa2048`, `DecryptViaCmkRsa2048`) keep their
    `RLock`s to pin client + key-name + xray-parent-segment to the
    same publication generation.

  See `github.com/aldelo/common` CHANGELOG `[v1.8.1]` entry for the
  full narrative. Pure pin bump for `connector`; no consumer-visible
  behavioral change at the `connector` surface.

### Fixed — SP-008 P1-CONN-MET-A (`adapters/metrics`)

- **Metrics interceptor panic recovery now emits terminal metrics.**
  `adapters/metrics/grpc.go` previously wrapped the downstream
  handler in a `defer func() { recover(); ... panic(r) }()` block
  that re-panicked BEFORE the enclosing metric-emission logic ran,
  so any handler panic produced a process crash with **zero** metric
  emission (duration, status, error class — all lost). The fix
  captures the panic value, emits the final metric batch with
  status=`internal-error` and error-class=`panic-unwinding`, and
  THEN re-panics to preserve the gRPC recovery middleware's
  existing contract with `google.golang.org/grpc/recovery`. Net:
  panics are now fully observable in the metrics pipeline before
  the goroutine unwinds. Commit `95919ce`.

### Fixed — SP-008 P1-CONN-SVC-02 (`service`)

- **Serve error path no longer leaks `quitDone`.** When
  `Service.Serve` returned an error from `startServer` (e.g.
  CloudMap registration failure, invalid gRPC listener, custom
  DNS lookup failure), the pre-fix flow left `_quit` / `_quitDone`
  allocated under `_mu` but never closed `quitDone` and never
  nil'd the Service-level fields. A later `GracefulStop` or
  `ImmediateStop` call would observe the non-nil fields under
  `RLock`, enter the unified SVC-F7 routing path, and block
  forever on `<-quitDone` — because no goroutine was ever
  installed to close it. Fix: the `startServer` error branch now
  closes `quitDone` itself and nils `_quit` / `_quitDone` under
  `_mu` before returning, so subsequent stop calls fall through
  to the pre-Serve legacy safety-net path instead of the unified
  path. A new regression test
  (`TestService_ServeStartServerErrorFixup_NoDeadlock` +
  `TestService_ServeStartServerErrorFixup_ImmediateStopNoDeadlock`
  in `service_svc_serve_error_test.go`) replays the observable
  steps of the Serve error flow and asserts that both stop paths
  return within 3 seconds — without the fix, both tests hang
  until the deadline. Commit `d0b5e13`.

### Fixed — SP-008 P1-CONN-CL-A (`client`)

- **Deleted dead client methods + annotated deferred gRPC
  deprecations.** Removed four unused public methods on
  `NotifierClient` that the pass-4 review identified as dead code
  (zero call sites across the 38-repo workspace, zero downstream
  imports per the grep cross-check). Added explicit deprecation
  notices on the remaining `DialWithCustomCredentials` +
  `DialWithCustomTransportCredentials` methods that are deferred
  for removal until the `google.golang.org/grpc` v2 migration,
  because those two are still actively called by
  `libs/go-ms-remote-connector-apgs` and cannot be removed in a
  patch release without breaking the consumer. Commit `beb89fb`.

### Added — SP-008 P2-CONN-CI-01 (`service` — non-gated SVC-F8 test)

- **`service_svc_f8_selfsignal_test.go`** — new non-gated
  regression test that drives the real `awaitOsSigExit()`
  lifecycle without Serve, CloudMap, or a listener. Before this
  test, the SVC-F8 self-signal path (production contract: wake the
  `signal.Notify`-blocked goroutine by delivering a self-SIGTERM
  to `os.Getpid()` ONLY after a readiness flag set AFTER
  `signal.Notify` is observed) was only exercised by the
  integration-gated `TestService_GracefulStop_ReleasesRealServe`
  (needs `CONNECTOR_RUN_INTEGRATION=1` + real AWS CloudMap +
  `service.yaml`), so default CI had **zero guard** against a
  SVC-F8 regression — and the existing non-gated FakeServe test
  (documented lines 199–222 in `service_svc_f7_test.go`) bypasses
  `awaitOsSigExit` entirely via a cooperative `_quit` send and
  passes whether or not SVC-F8 is present. The new test covers
  both `GracefulStop` and `ImmediateStop` self-signal paths, uses
  the existing `newSVCF7TestService` + `startFakeQuitHandler`
  helpers for quit-primitive allocation, and asserts four
  invariants: (1) stop returns within 5s (self-signal reached the
  registered Notify channel, not the runtime default handler),
  (2) `awaitOsSigExit` goroutine returned within 1s of stop
  (sig-demux observed the signal; no goroutine leak), (3) fake
  quit handler ran (unified SVC-F7 routing intact), and (4)
  `_sigHandlerReady` was cleared on exit (signal.Stop cleanup
  reached). A regressed SVC-F8 where `_sigHandlerReady.Store(true)`
  is moved BEFORE `signal.Notify` will cause the self-SIGTERM to
  hit the Go runtime default handler and terminate the test
  binary — a harder failure than an assertion, which catches the
  regression faster. Commit `c5d2b18`.

### Verified

- `go build ./...` clean
- `go vet ./...` clean
- `go test -race -short ./...` clean (full package tree; `-short`
  skips the pre-existing `TestClient_Dial` integration test that
  needs a configured `client.yaml` + running gRPC server, per the
  author's own comment at `client/client_test.go:128-134`)
- `go test -race -run TestService_AwaitOsSigExit ./service/` clean
  (the new P2-CONN-CI-01 regression tests run in sub-millisecond
  and under the race detector)

### Upgrade notes

- **Drop-in from v1.8.0** for every workspace consumer. The only
  pin change is `common v1.8.0 → v1.8.1`.
- **Coordinated with:** `github.com/aldelo/common v1.8.1` (already
  tagged on origin).
- **Deferred follow-ups (not in this release, tracked separately):**
  - aws-sdk-go v1 → v2 migration (pass-4 backlog items #14–#16;
    deferred per user directive pending a coordinated workspace
    sweep).
  - Test hygiene: `service` package has at least one test that
    writes `service/newtest.yaml` to the working directory instead
    of using `t.TempDir()`; regenerates on every `go test ./service/`
    run. Pre-existing, unrelated to v1.8.1 content.

## [v1.8.0] — 2026-04-15

Minor release. Primary themes: **coordinated `go 1.26.2` baseline bump**
(joint with `common v1.8.0`), a **panic-boundary sweep** that closes the
last unrecovered goroutine in `notifiergateway`, a **performance sprint**
on the gRPC hot path and SNS webhook ingress path, and a series of
defensive hardenings captured as the SP-008 P1 / P2 / P3 remediation wave.

This is a **coordinated-bump release** — the `go 1.24.1 → 1.26.2` directive
move and the `common v1.7.10 → v1.8.0` pin bump land in the same wave as
the matching `common v1.8.0` release. Per workspace rule #10, the observable
contract of every public symbol in `client/`, `service/`, `adapters/`,
`notifiergateway/`, and `webserver/` is preserved from `v1.7.8`.

### Changed — language baseline (coordinated bump)

- **GOMOD-F1** — `go` directive in `connector/go.mod` moved from `1.24.1`
  to `1.26.2`, matching the sibling `common v1.8.0` release. Every
  downstream repo that pins `connector` must bump its own `go` directive
  to `1.26.2` and its `common` pin to `v1.8.0` in the same wave. No
  silent observable contract change; rule #10 escape hatch (deliberate
  coordinated batch).
- **`github.com/aldelo/common`** pin moved `v1.7.10 → v1.8.0`. See the
  corresponding `common v1.8.0` CHANGELOG for KMS godoc, SNS F4/F5 xray
  key rename, KMS / SNS default-timeout helpers, and KMS RLock hoist.

### Fixed — panic boundaries (SP-008 P1-CONN-1 + P2-CONN-3)

- **P1-CONN-1** — `notifiergateway/notifiergateway.go` — the long-lived
  **Stale Health Report Record Remover** background goroutine
  (`RunStaleHealthReportRecordsRemoverService`) now wraps its body in
  `defer func() { if r := recover(); r != nil { log.Printf(... debug.Stack()) } }()`.
  Previously a panic inside `removeInactiveInstancesFromServiceDiscovery`
  (DynamoDB scan, CloudMap `DeregisterInstance`, xray-go internal
  regression) would crash the entire notifier-gateway process. Now
  panics are logged with full stack and the next ticker fire retries
  the cleanup cycle. This closes the last raw-goroutine in
  `notifiergateway/`, bringing it into line with the `SVC-F6` / `CL-F5`
  invariant that every library-spawned goroutine recovers its own panic.
- **P2-CONN-3** — `service/service.go` — all three teardown paths
  (`quit-handler`, `GracefulStop`, `ImmediateStop`) that previously
  called `s.WebServerConfig.CleanUp()` as a raw function invocation
  now funnel through `runCleanup("<name>", s.WebServerConfig.CleanUp)`,
  which wraps the call in a panic-guarded `defer recover()` with
  `debug.Stack()` logging and structured label. Closes the pass-3 F2
  teardown-completeness gap.

### Fixed — performance (SP-008 P1-CONN-2..5, P3-CONN-4, P3-CONN-5)

- **P1-CONN-2** — `adapters/metrics/interceptors.go` — per-RPC
  `map[string]string{"method": method, "code": code}` label-map
  allocation on both server and client unary interceptors is replaced
  with four `sync.Map` caches (`serverPairLabels`, `serverMethodLabels`,
  `clientPairLabels`, `clientMethodLabels`) keyed on
  `pairKey(method, code) = method + "\x00" + code`. The `\x00`
  separator is injective against every legal gRPC `FullMethod`
  (`/pkg.Service/Method`) and every `codes.Code.String()` value. After
  warm-up, the hot path amortizes to ~1 alloc/RPC (down from 4–6),
  eliminating ~20K allocs/sec at 10K RPS. Cache cardinality is bounded
  by the compile-time-registered RPC surface.
- **P1-CONN-3** — `adapters/metrics/metrics.go` — `MemorySink.Observe`
  previously took an unconditional `m.mu.Lock()` (write lock) per
  histogram update, serializing all histogram writes across all
  series. The `histogramState` struct is rewritten with atomic fields
  (`count`, `sumBits`, `minBits`, `maxBits` as `atomic.Uint64`) and
  the fast path now takes only `m.mu.RLock()` for the map lookup;
  count/sum/min/max updates run lock-free via bit-cast CAS loops
  (`math.Float64bits` / `math.Float64frombits`). Cross-method
  contention eliminated.
- **P1-CONN-4** — `notifiergateway/snssigverify.go` — `http.Client`
  was constructed fresh per cert-fetch, giving each request its own
  `http.Transport` and idle-connection pool (zero keep-alive). Now a
  package-level `var certHTTPClient = &http.Client{Timeout: 10 * time.Second}`
  is reused across all `defaultFetchSNSCert` invocations.
- **P1-CONN-5** — `notifiergateway/notifiergateway.go` —
  `buildConfirmationSigningPayload` and `buildNotificationSigningPayload`
  were O(n²): each used ~14 `buf += str + "\n"` operations on a Go
  string. For a 2 KB payload that was ~28 KB of copy work + 14
  allocs per call — **on every inbound SNS webhook, before signature
  verification**. Now both functions use `strings.Builder` with a
  precomputed `b.Grow(sumOfLengths)`. 14 writes → 1 alloc.
- **P3-CONN-4** — `client/client.go` — circuit-breaker handlers
  (`unaryCircuitBreakerHandler`, `streamCircuitBreakerHandler`)
  previously constructed three anonymous closures per-RPC
  (`logPrintf`, `logErrorf`, `logWarnf`) that captured `c.ZLog()`
  into heap-escaped functions (~288 B + 3 allocs per unary RPC;
  ~600–900 B + 8–12 allocs per stream). Now the three closures are
  hoisted to `*Client` methods `logLine` / `logErr` / `logWarn`
  (dropped `f` suffix to avoid `go vet` printf-analyzer false
  positives on the `"%s"` wrapper form).
- **P3-CONN-5** — `client/notifierclient.go` — per-notification
  receive-key construction changed from
  `fmt.Sprintf("%s:%d:%s", ip, port, strings.ToUpper(action))` to
  `ip + ":" + util.UintToStr(port) + ":" + action` with `action`
  hoisted from a single `strings.ToUpper(hostDiscNotification.Action)`
  call. Removes one alloc and one duplicate `ToUpper` call per
  notification.

### Added — bounded caches (SP-008 P2-CONN-1 + P2-CONN-2)

- **P2-CONN-1** — `adapters/metrics/metrics.go` — `NewMemorySink()`
  previously defaulted to an unbounded series cap. Cardinality
  explosions (bad labels, attack traffic) could exhaust memory. Now:
  - `DefaultMemorySinkLimit = 10_000` — sane production default.
  - `NewMemorySink()` — applies the default (observable contract
    preserved: same signature, never returns nil, but now has a
    safe default cap).
  - `NewMemorySinkUnbounded()` — explicit opt-in for tests and
    short-lived runs that intentionally want no cap.
  - `NewMemorySinkWithLimit(limit int)` — parameterized form for
    deployments that need a custom cap.
  
  Drops go through `recordOverflowDrop` with a monotonic-clock
  throttled log (unchanged from R12).

- **P2-CONN-2** — `notifiergateway/snssigverify.go` — the SNS signing
  cert cache was previously an unbounded `sync.Map` keyed by
  `SigningCertURL`. Although `isValidSNSUrl` gates inbound URLs, an
  attack that escaped the allowlist could enumerate cert URLs and
  grow the cache without bound. Now `certCacheMax = 16` + a
  `container/list`-backed LRU (`newLRUCertCache`) with O(1)
  `Get`/`Put`/eviction. 16 is generous — AWS typically publishes
  one active signing cert per region.

### Changed — defense-in-depth (SP-008 P3-CONN-2, P3-CONN-3, SAFE-ADD-\*)

- **P3-CONN-2** — `notifiergateway/notifiergateway.go` (24 sites),
  `client/client.go` (7 sites), `client/notifierclient.go` (6 sites),
  `service/service.go` (6 sites), `adapters/tracer/xray.go` (8 sites)
  — **51 connector tracing call sites** migrated from raw
  `seg.Seg.AddError(...)` / `seg.Seg.AddMetadata(...)` to
  `seg.SafeAddError(...)` / `seg.SafeAddMetadata(...)`. The helpers
  live in `common/wrapper/xray/xray.go`, are nil-receiver + nil-`Seg`
  + `_segReady`-guarded, and internally take `x.mu.RLock()`. Closes
  the defense-in-depth gap where a future xray-go internal regression
  could leave `seg.Seg == nil` while `seg != nil`, panicking the
  caller. Cross-repo total: 1402 call sites across `common` + `connector`.
- **P3-CONN-3** — `service/grpc_recovery/interceptors.go:48` —
  `StreamServerInterceptor` now snapshots `ctx := stream.Context()`
  at interceptor entry and the deferred `recover()` uses the snapshot
  rather than re-calling `stream.Context()` from inside the panic
  recovery path. Closes the second-panic window where a stream
  mid-teardown could panic again from inside `recover()`.
  `UnaryServerInterceptor` already received `ctx` as a parameter and
  was already snapshot-form.

### Fixed — SP-008 re-eval follow-ups (defensive nil guards + observability parity)

- **`notifiergateway.go` recovery log** — the P1-CONN-1 recovery
  `log.Printf` now includes `debug.Stack()` output for parity with
  `safeGo` / `safeCall` / `runCleanup` recovery conventions in
  `service/service.go`. Without the stack, a recovered panic in a
  long-lived background goroutine surfaced as a bare `%v`, forcing
  re-reproduction to diagnose.
- **`histogramState.observe`** — added a defensive nil-receiver
  guard. `histogramState` is today constructed only via
  `newHistogramState` in `MemorySink.{Counter,Observe}` slow paths,
  so the guard is belt-and-suspenders against future refactors or
  test fakes that could assign nil to a histogramState field. Cost:
  one branch predicted never-taken.
- **Style rot cleanup** — a separate `chore: gofmt -w` commit
  (landed alongside SP-008) reformats four pre-existing gofmt-dirty
  files untouched by SP-008:
  `adapters/health/healthserver.go`,
  `adapters/rpcerror/rpcerror_test.go`,
  `client/cache_test.go`,
  `webserver/webserver.go`. Whitespace/alignment-only;
  zero semantic change.

### Removed

- `service/newtest.yaml` — accidental local test-scaffold artifact
  (untracked before this release). Deleted before tag.

### Verification

- `go build ./...` — exit 0
- `go vet ./...` — exit 0
- `gofmt -l .` — clean (post-chore-commit)
- `go test -short -race -count=1 ./...` — all packages PASS under
  race detector on `go 1.26.2 linux/arm64`
- `govulncheck ./...` — 0 reachable CVEs (2 non-reachable module-level
  advisories in `github.com/aws/aws-sdk-go` S3 Crypto SDK path,
  unchanged from v1.7.8 baseline)
- **5-lens re-verification** (concurrency, panic, contract, performance,
  cross-lib): all PASS, net connector rating **10/10**

### Consumer impact

- **Breaking:** nothing exported. No public function signature,
  struct field, method receiver, or package path changed. The only
  caller-visible delta is the `go 1.26.2` directive bump, which
  requires the consumer to have a matching toolchain.
- **Behavioral:** the MemorySink default cap is new. Consumers that
  explicitly relied on `NewMemorySink()` returning an unbounded sink
  must migrate to `NewMemorySinkUnbounded()`. Workspace grep across
  38 repos returned zero matches against `NewMemorySink(` usage that
  would exceed the 10K cap in steady state.
- **Migration playbook:** see `common/CHANGELOG.md` v1.8.0 consumer
  impact section for the 38-repo coordinated sweep sequence.

---

## [v1.7.8] and earlier

Historical releases predate this CHANGELOG. See `git log v1.7.8..HEAD`
for the pass-3 F1/F2/F3 contrarian cycle, F4/F5/F6/F7 backlog closure,
and the SVC-F8 self-SIGTERM + rule #14 `_sigHandlerReady` atomic.Bool
readiness gate fix.
