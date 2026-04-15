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

## [Unreleased]

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
