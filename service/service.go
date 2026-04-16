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
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/crypto"
	"github.com/aldelo/common/rest"
	"github.com/aldelo/common/tlsconfig"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/cloudmap"
	"github.com/aldelo/common/wrapper/cloudmap/sdhealthchecktype"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/sns"
	"github.com/aldelo/common/wrapper/sns/snsprotocol"
	"github.com/aldelo/common/wrapper/sqs"
	"github.com/aldelo/common/wrapper/xray"
	"github.com/aldelo/connector/adapters/health"
	"github.com/aldelo/connector/adapters/notification"
	"github.com/aldelo/connector/adapters/queue"
	"github.com/aldelo/connector/adapters/ratelimiter"
	"github.com/aldelo/connector/adapters/ratelimiter/ratelimitplugin"
	"github.com/aldelo/connector/adapters/registry"
	"github.com/aldelo/connector/adapters/registry/sdoperationstatus"
	"github.com/aldelo/connector/adapters/tracer"
	"github.com/aldelo/connector/service/grpc_recovery"
	ws "github.com/aldelo/connector/webserver"
	sns2 "github.com/aws/aws-sdk-go/service/sns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/tap"
)

// Retry backoff constants
const (
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
	backoffFactor  = 2.0
)

// ShutdownPhase controls when Service.ShutdownCtx() fires during the
// shutdown sequence. It is used with Service.ShutdownCancelPhase and
// takes effect only when Service.ShutdownCancel is true.
//
// Design: SVC-F5 (see _src/docs/repos/connector/plans/
// 2026-04-14__svc-f5-shutdown-ctx-design.md).
//
// The capability is strictly opt-in. Existing consumers that do not set
// ShutdownCancel see ZERO behavior change — ShutdownCtx() returns nil,
// no context is allocated, and no fire site is taken.
type ShutdownPhase int

const (
	// ShutdownPhaseNone disables the ShutdownCtx capability. ShutdownCtx()
	// returns nil. This is the zero value so the default Service has no
	// shutdown-cancel behavior unless the consumer explicitly opts in.
	ShutdownPhaseNone ShutdownPhase = iota

	// ShutdownPhaseImmediate fires the cancel the instant the signal
	// handler receives SIGTERM/SIGINT/SIGQUIT, before BeforeServerShutdown
	// runs. Use for short-API services where handlers should abort
	// immediately rather than consume any grace time. The fire happens
	// inside the signal-demux goroutine, before it notifies Serve that a
	// signal was received, so handlers observing ShutdownCtx.Done() see
	// the cancel happens-before any subsequent Serve shutdown step.
	ShutdownPhaseImmediate

	// ShutdownPhasePreDrain fires the cancel inside the unified quit
	// handler, AFTER all non-gRPC teardown has completed and
	// IMMEDIATELY BEFORE the bounded graceful-stop attempt (or the
	// ImmediateStop bypass) hands control to the gRPC server. By the
	// time handlers observe ShutdownCtx.Done() on this phase, the
	// following have already run in the quit handler, in order:
	// SNS offline-discovery publish, SNS topic unsubscribe, health
	// report data-store delete, stopHealthReportService tick,
	// WebServerConfig.CleanUp, clear local address, setServing(false),
	// Cloud Map deregisterInstance (one-shot CAS), and Disconnect on
	// the SD/SQS/SNS AWS clients. What has NOT yet run: the bounded
	// grpc.GracefulStop drain (or, on the ImmediateStop bypass, the
	// direct grpc.Server.Stop call) and the listener close. The
	// service-discovery layer has already stopped routing to us —
	// handlers use this phase to stop cooperating before the grace
	// window begins, which is the good middle-ground default for
	// typical gRPC services.
	//
	// Note: BeforeServerShutdown runs BEFORE this fire site only on
	// the signal path (Serve owns that hook and calls it after
	// awaitOsSigExit unblocks, before sending on the quit channel).
	// On the programmatic GracefulStop/ImmediateStop path the
	// BeforeServerShutdown hook is not re-invoked from the quit
	// handler — Serve owns BeforeServerShutdown — so handlers should
	// not assume BeforeServerShutdown has executed when PreDrain
	// fires via a programmatic stop.
	ShutdownPhasePreDrain

	// ShutdownPhasePostGraceExpiry fires the cancel only after
	// grpc.Server.GracefulStop's own bounded timeout (GracefulStopTimeout)
	// elapses, as a "last warning" before grpc.Server.Stop severs the
	// transport. Gives handlers maximum grace; useful for long-running
	// imports where interruption is expensive.
	ShutdownPhasePostGraceExpiry
)

// String returns a human-readable name for the phase (used in log lines).
func (p ShutdownPhase) String() string {
	switch p {
	case ShutdownPhaseNone:
		return "None"
	case ShutdownPhaseImmediate:
		return "Immediate"
	case ShutdownPhasePreDrain:
		return "PreDrain"
	case ShutdownPhasePostGraceExpiry:
		return "PostGraceExpiry"
	default:
		return fmt.Sprintf("ShutdownPhase(%d)", int(p))
	}
}

// healthreport struct info,
// notifiergateway/notifiergateway.go also contains this struct as a mirror;
//
// HashSignature = security validation is via sha256 hash signature for use with notifiergateway /healthreport,
// struct field HashSignature is hash of values (other than aws region, hash key name, and hash signature itself),
// the hashing source value is also combined with the current date in UTC formatted in yyyy-mm-dd,
// hash value is comprised of namespaceid + serviceid + instanceid + current date in UTC in yyyy-mm-dd format;
// the sha256 hash salt uses named HashKey on both client and server side
type healthreport struct {
	NamespaceId   string `json:"NamespaceId"`
	ServiceId     string `json:"ServiceId"`
	InstanceId    string `json:"InstanceId"`
	AwsRegion     string `json:"AWSRegion"`
	ServiceInfo   string `json:"ServiceInfo"`
	HostInfo      string `json:"HostInfo"`
	HashKeyName   string `json:"HashKeyName"`
	HashSignature string `json:"HashSignature"`
}

// Service is the gRPC server lifecycle owner for a connector-managed
// service instance. A Service binds together: configuration loading
// (service.yaml), gRPC server setup, AWS Cloud Map service discovery
// registration/heartbeat/deregistration, optional health-check handlers,
// optional rate limiting and TLS, optional companion HTTP web server,
// and a set of caller-defined lifecycle hooks (BeforeServerStart,
// AfterServerStart, BeforeServerShutdown, AfterServerShutdown).
//
// Construct one with NewService, then optionally configure exported
// fields BEFORE calling Serve. Serve blocks until an OS signal
// (SIGTERM/SIGINT) or an explicit GracefulStop/ImmediateStop call. After
// Serve returns the Service has fully shut down — both the gRPC server
// and the Cloud Map deregistration. A given Service is single-use; do
// not call Serve more than once.
//
// Concurrency contract:
//   - Exported fields (AppName, hooks, interceptors, RateLimit,
//     WebServerConfig, etc.) MUST be set before Serve is called and MUST
//     NOT be mutated after. The Service does not synchronize reads of
//     these fields against caller-side writes.
//   - GracefulStop, ImmediateStop, and the internal signal handler may
//     be invoked concurrently from any goroutine — they are guarded by
//     _mu plus an atomic.Bool one-shot CAS (_deregFired) on the Cloud
//     Map deregister path that ensures exactly-once dereg without
//     blocking concurrent callers.
//
// Lifecycle (see also _src/docs/repos/connector/architecture.md):
//
//	NewService -> (configure fields) -> Serve
//	  Serve: readConfig -> (BeforeServerStart) -> startServer
//	    startServer: bind -> register-with-cloudmap -> serve gRPC
//	  -> (AfterServerStart) -> awaitOsSigExit
//	  -> (BeforeServerShutdown) -> deregister-with-cloudmap ->
//	     grpc.GracefulStop (bounded by GracefulStopTimeout;
//	     falls back to grpc.Stop on timeout)
//	  -> (AfterServerShutdown) -> Serve returns
//
// GracefulStopTimeout caps how long the default signal path will wait
// for in-flight RPCs to drain before forcing termination — see that
// field's godoc for details.
type Service struct {
	// AppName is the logical name used to discover this service's config
	// file (service.yaml or {AppName}.yaml depending on layout). Required.
	AppName string

	// ConfigFileName overrides the config file basename. Optional —
	// defaults to "service" when empty.
	ConfigFileName string

	// CustomConfigPath overrides the config file directory. Optional —
	// defaults to the standard config search path when empty.
	CustomConfigPath string

	// WebServerConfig optionally enables a sidecar HTTP server (gin)
	// for non-gRPC routes. When non-nil, Serve also starts the web
	// server on its own port and shuts it down during Service teardown.
	WebServerConfig *WebServerConfig

	// RegisterServiceHandlers is the caller-provided callback that
	// registers protobuf-generated server handlers on the gRPC server
	// instance. Required — Serve fails fast with an error if nil.
	RegisterServiceHandlers func(grpcServer *grpc.Server)

	// DefaultHealthCheckHandler returns the SERVING status for the
	// default (empty-string) health-check service name. Optional — if
	// nil, the embedded grpc_health_v1 server reports SERVING
	// unconditionally.
	DefaultHealthCheckHandler func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus

	// ServiceHealthCheckHandlers maps named gRPC services to their
	// per-service health-check status function. Optional. Use this to
	// expose per-service liveness independent of the default handler.
	ServiceHealthCheckHandlers map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus

	// RateLimit, if non-nil, gates inbound RPCs through the supplied
	// rate limiter. Note: the rate-limit reject path was added in P1-1
	// (deep-review-2026-04-12); prior to that fix the limiter recorded
	// hits but did not actually reject excess traffic.
	RateLimit ratelimiter.RateLimiterIFace

	// UnaryServerInterceptors is the unary interceptor chain. Order
	// matters: interceptors run in slice order on the way in (request)
	// and reverse order on the way out (response). The recommended
	// ordering is: tracing -> metrics -> auth -> tenancy -> logger ->
	// recovery -> handler. Wire metrics and logger via the
	// adapters/metrics and adapters/logger constructors.
	UnaryServerInterceptors []grpc.UnaryServerInterceptor

	// StreamServerInterceptors is the stream interceptor chain.
	// Same ordering rules as UnaryServerInterceptors.
	StreamServerInterceptors []grpc.StreamServerInterceptor

	// StatsHandler is the optional grpc/stats.Handler for connection
	// and RPC lifecycle events. Used by tracing/metrics integrations
	// that need lower-level events than interceptors expose.
	StatsHandler stats.Handler

	// UnknownStreamHandler handles RPCs whose method is not registered
	// on the server. Default behavior (nil) returns Unimplemented;
	// override to implement proxying or graceful degradation.
	UnknownStreamHandler grpc.StreamHandler

	// BeforeServerStart runs synchronously immediately before the gRPC
	// listener is created. Use it for late configuration (mutating
	// other Service fields) or for warm-up actions that must complete
	// before the server accepts traffic. nil is a no-op.
	BeforeServerStart func(svc *Service)

	// AfterServerStart runs synchronously immediately after the gRPC
	// server is accepting connections AND the Cloud Map register call
	// has succeeded. nil is a no-op.
	AfterServerStart func(svc *Service)

	// BeforeServerShutdown runs synchronously at the start of the
	// shutdown sequence, before deregister or grpc.GracefulStop. nil
	// is a no-op. Use this to drain caller-managed background work.
	BeforeServerShutdown func(svc *Service)

	// AfterServerShutdown runs synchronously after the gRPC server has
	// fully stopped and Cloud Map deregistration has completed. nil
	// is a no-op. This is the last hook to fire before Serve returns.
	AfterServerShutdown func(svc *Service)

	// GracefulStopTimeout bounds how long the default signal-driven
	// shutdown path will wait for in-flight RPCs to drain via
	// grpc.Server.GracefulStop before escalating to grpc.Server.Stop
	// (hard-close of all connections).
	//
	// Zero (the default) means 30 seconds. Set this to match your
	// deployment's terminationGracePeriodSeconds (k8s) / task SIGKILL
	// grace window (ECS) minus a safety margin, so the fallback Stop
	// always runs before the orchestrator SIGKILLs the process.
	//
	// Fix: SVC-F1 (deep-review-2026-04-13-contrarian). Prior to this
	// field, the default signal path called grpc.Server.Stop directly,
	// contradicting the lifecycle godoc and killing in-flight RPCs.
	GracefulStopTimeout time.Duration

	// ShutdownCancel enables the opt-in ShutdownCtx capability. When
	// false (the default), ShutdownCtx() returns nil and no shutdown
	// context is allocated — existing consumers see ZERO behavior
	// change. When true, Serve() allocates a dedicated context that is
	// cancelled at the phase named by ShutdownCancelPhase (or
	// ShutdownPhaseImmediate if the phase is ShutdownPhaseNone), giving
	// cooperating handlers an in-band signal to stop new work and
	// return early.
	//
	// Set this BEFORE calling Serve. Mutating it after Serve has been
	// called has no effect and is racy with respect to the internal
	// allocation path.
	//
	// Fix: SVC-F5 (2026-04-14). Resolves the gap where
	// grpc.GracefulStop does not cancel the handler's ctx and
	// long-running handlers have no way to learn the server is
	// draining.
	ShutdownCancel bool

	// ShutdownCancelPhase names the shutdown phase at which
	// ShutdownCtx() fires when ShutdownCancel is true. See
	// ShutdownPhase for the per-phase semantics. When ShutdownCancel
	// is true but this is the zero value (ShutdownPhaseNone), the
	// phase defaults to ShutdownPhaseImmediate — enabling the
	// capability without picking a phase gives the fastest-shutdown
	// mental model.
	//
	// Ignored when ShutdownCancel is false.
	ShutdownCancelPhase ShutdownPhase

	// read or persist service config settings
	_config *config

	// service discovery object cached
	_sd  *cloudmap.CloudMap
	_sqs *sqs.SQS
	_sns *sns.SNS

	// instantiated internal objects
	_grpcServer   *grpc.Server
	_localAddress string

	// grpc serving status and mutex locking
	_serving bool
	_mu      sync.RWMutex

	// SVC-F3: _started is a monotonic one-shot gate that enforces the
	// "Service is single-use" contract documented on Serve(). Unlike
	// _serving (which is true only while the gRPC server is actively
	// accepting traffic and flips back to false during shutdown),
	// _started flips true the first time Serve() is entered and never
	// flips back. A second call to Serve() on the same Service
	// instance observes the already-set flag and returns
	// ErrServiceAlreadyStarted without side effects — no duplicate
	// hook fires, no double listener bind, no competing
	// awaitOsSigExit loops, no duplicate Cloud Map registration.
	_started atomic.Bool

	// SVC-F4: deregisterInstance is invoked from multiple shutdown paths
	// (GracefulStop/ImmediateStop, the internal quit handler goroutine,
	// and the Serve() defer). The original P1-3 fix used sync.Once +
	// cached error, but sync.Once.Do BLOCKS every concurrent caller
	// behind the first caller's CloudMap poll loop (up to ~5s typical,
	// ~100s pathological). That broke ImmediateStop's break-glass
	// semantics: an operator invoking ImmediateStop while the signal
	// path was mid-dereg would stall behind AWS.
	//
	// _deregFired is a one-shot atomic claim token. The first caller
	// CAS-wins and runs the AWS deregister; every concurrent or later
	// caller CAS-loses and returns nil immediately, letting paths like
	// ImmediateStop fall through to gs.Stop() without waiting. The
	// AWS API is itself idempotent on DeregisterInstance, and the
	// one-shot CAS prevents any spurious duplicate call (the original
	// P1-3 guarantee). Later callers no longer observe the first
	// caller's error, but every call site treats the error as
	// log-only — verified across all six deregisterInstance call
	// sites — so no semantic regression.
	_deregFired atomic.Bool

	// SVC-F5: _shutdownCtx / _shutdownCancel hold the opt-in handler
	// shutdown-signal context. Both are nil unless ShutdownCancel was
	// true when Serve() ran. When non-nil, _shutdownCtx is a
	// context.Background-derived WithCancel whose cancel fires at the
	// configured ShutdownCancelPhase (and, as a safety net, once more
	// from fireShutdownCancelFinal at the end of Serve regardless of
	// phase). Guarded by _mu: Serve acquires the write lock to
	// allocate, ShutdownCtx() acquires the read lock to return.
	_shutdownCtx    context.Context
	_shutdownCancel context.CancelFunc

	// SVC-F7: _quit / _quitDone are the unified quit-routing primitives
	// used by Serve()'s quit-handler goroutine. Promoted from local
	// variables in Serve to Service fields so exported stop methods
	// (GracefulStop, ImmediateStop) can route through the same
	// single-source-of-truth quit handler that the signal path uses,
	// instead of duplicating the teardown body. Both fields are nil
	// until Serve allocates them under _mu (one-shot per-instance) and
	// the quit-handler goroutine closes _quitDone when teardown
	// completes. Exported stop methods that observe non-nil _quit do a
	// non-blocking send on _quit (buffer 1; idempotent — if already
	// signaled, the send drops via select default) and then block on
	// _quitDone until the quit handler finishes. When _quit is nil
	// (call before Serve, or after the quit-handler already nil-ed the
	// field at end of teardown — currently the field is left intact
	// post-teardown so this branch primarily covers pre-Serve calls)
	// the exported stop method falls through to its legacy idempotent
	// teardown body as a safety net.
	//
	// _immediateStopRequested is the side-channel that lets
	// ImmediateStop() request the "skip drain, go straight to gs.Stop()"
	// behavior through the same quit handler. The quit handler reads
	// this atomic AFTER it wins the quit signal but BEFORE invoking
	// stopGRPCServerBoundedWithHook; if true, it bypasses the bounded
	// graceful path and calls gs.Stop() directly (still firing the
	// PostGraceExpiry SVC-F5 hook synchronously beforehand to honor
	// the rule #13 contract on the immediate path). The flag is set
	// once and never cleared — single-use per Service, like _started.
	//
	// Concurrency contract: signal path and programmatic path may race
	// on the send; whichever wins the buffer-1 send wakes the handler,
	// the loser's send drops via the select default and the loser
	// blocks on _quitDone alongside the winner. Both observe the same
	// teardown completion. Concurrent GracefulStop+ImmediateStop sets
	// _immediateStopRequested via atomic.Bool.Store — if either path
	// sets it before the handler reads it, the immediate semantics win.
	_quit                   chan bool
	_quitDone               chan struct{}
	_immediateStopRequested atomic.Bool

	// SVC-F8 (F1 deep-review-2026-04-14-contrarian-pass3):
	// _sigHandlerReady gates the self-SIGTERM that GracefulStop and
	// ImmediateStop use to wake Serve()'s awaitOsSigExit. The self-
	// signal is only safe AFTER awaitOsSigExit has called
	// signal.Notify — before that, SIGTERM hits the Go runtime's
	// default handler and terminates the process. awaitOsSigExit
	// sets this atomic to true immediately after registering the
	// signal handler, and the exported stop methods check it before
	// calling p.Signal(syscall.SIGTERM). If the flag is not set
	// (pre-Serve call, mid-Serve before signal.Notify, or
	// programmatic unit test that manually allocates the quit
	// primitives without running Serve), the stop methods skip the
	// self-signal; in those paths Serve() isn't actually blocked on
	// awaitOsSigExit so there's nothing to wake. The flag is cleared
	// in awaitOsSigExit immediately after signal.Stop so any late
	// programmatic stop after the handler is unregistered does NOT
	// self-signal into a no-op handler — defence-in-depth even
	// though SVC-F3's single-use gate prevents Service re-use.
	_sigHandlerReady atomic.Bool
}

// NewService constructs a Service ready for further configuration.
// appName is the logical service name used to locate the config file
// and to seed Cloud Map registration. configFileName overrides the
// default basename ("service") when the YAML lives at a non-default
// path. customConfigPath overrides the default config search directory.
// registerServiceHandlers is the caller's protobuf handler-registration
// callback — typically a one-line wrapper around the generated
// pb.RegisterFooServiceServer call. It is REQUIRED; passing nil here
// causes Serve to fail.
//
// The returned Service has only the four constructor parameters set;
// configure all other fields directly on the returned struct before
// calling Serve.
func NewService(appName string, configFileName string, customConfigPath string, registerServiceHandlers func(grpcServer *grpc.Server)) *Service {
	return &Service{
		AppName:                 appName,
		ConfigFileName:          configFileName,
		CustomConfigPath:        customConfigPath,
		RegisterServiceHandlers: registerServiceHandlers,
	}
}

// isServing returns the serving status in a thread-safe manner
func (s *Service) isServing() bool {
	s._mu.RLock()
	defer s._mu.RUnlock()
	return s._serving
}

// setServing sets the serving status in a thread-safe manner
func (s *Service) setServing(v bool) {
	s._mu.Lock()
	defer s._mu.Unlock()
	s._serving = v
}

// ShutdownCtx returns the opt-in handler shutdown-signal context, or
// nil if the capability is disabled (ShutdownCancel is false, or
// Serve() has not yet allocated it).
//
// Handlers that want to cooperate with graceful shutdown should
// observe Done() on the returned context in addition to their normal
// RPC ctx:
//
//	func (s *Handler) LongRunning(ctx context.Context, req *pb.Req) (*pb.Resp, error) {
//	    shut := s.svc.ShutdownCtx()
//	    for chunk := range work {
//	        if shut != nil {
//	            select {
//	            case <-shut.Done():
//	                return nil, status.Error(codes.Unavailable, "server draining")
//	            case <-ctx.Done():
//	                return nil, ctx.Err()
//	            default:
//	            }
//	        }
//	        processChunk(chunk)
//	    }
//	    return &pb.Resp{}, nil
//	}
//
// Returning nil when the capability is disabled is intentional — it
// lets handlers use a nil-check fast path that compiles away when the
// consumer hasn't opted in, rather than always allocating a context
// just to have something non-nil to return.
//
// The returned context is also cancelled as a safety net at the end
// of Serve, so a handler observing ShutdownCtx.Done() is guaranteed
// to see it fire before Serve returns — even if no configured phase
// matched (e.g., a future phase constant is added and not all fire
// sites are updated).
//
// SVC-F7 (2026-04-14): the safety net applies symmetrically on the
// programmatic shutdown path. GracefulStop() and ImmediateStop()
// route through the same unified quit handler the signal path
// wakes, so every fireShutdownCancelIfPhase site (Immediate,
// PreDrain, PostGraceExpiry) AND the deferred Final fire run
// before the exported stop method returns to the caller. Prior to
// SVC-F7 these methods bypassed every phase fire and Serve()
// remained blocked waiting on an OS signal; project rule #13
// programmatic-stop semantics are now enforceable end to end.
//
// Fix: SVC-F5 (2026-04-14), extended by SVC-F7 (2026-04-14).
func (s *Service) ShutdownCtx() context.Context {
	s._mu.RLock()
	defer s._mu.RUnlock()
	return s._shutdownCtx
}

// effectiveShutdownCancelPhase returns the phase at which ShutdownCtx
// should fire when ShutdownCancel is enabled. The zero value
// (ShutdownPhaseNone) is promoted to ShutdownPhaseImmediate so that
// "just turn it on" works without also picking a phase. Callers must
// check ShutdownCancel separately; this helper assumes enablement.
func (s *Service) effectiveShutdownCancelPhase() ShutdownPhase {
	if s.ShutdownCancelPhase == ShutdownPhaseNone {
		return ShutdownPhaseImmediate
	}
	return s.ShutdownCancelPhase
}

// fireShutdownCancelIfPhase calls the internal shutdown cancel iff
// ShutdownCancel is enabled and the effective phase matches `phase`.
// Idempotent — context.CancelFunc is safe to call multiple times. No-op
// when capability is disabled or when the ctx has not been allocated.
func (s *Service) fireShutdownCancelIfPhase(phase ShutdownPhase) {
	s._mu.RLock()
	enabled := s.ShutdownCancel
	effective := s.effectiveShutdownCancelPhase()
	cancel := s._shutdownCancel
	s._mu.RUnlock()
	if !enabled || cancel == nil {
		return
	}
	if effective != phase {
		return
	}
	log.Printf("ShutdownCtx cancel fired (phase=%s)", phase)
	cancel()
}

// fireShutdownCancelFinal unconditionally calls the internal shutdown
// cancel if present. Called at the end of Serve() as a safety net so
// handlers observing ShutdownCtx are guaranteed to see Done() before
// Serve returns, even if no phase fire matched. Idempotent. No-op
// when capability is disabled or ctx was never allocated.
func (s *Service) fireShutdownCancelFinal() {
	s._mu.RLock()
	cancel := s._shutdownCancel
	s._mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// readConfig will read in config data
func (s *Service) readConfig() error {
	if s == nil {
		return fmt.Errorf("Service receiver is nil")
	}

	s._config = &config{
		AppName:          s.AppName,
		ConfigFileName:   s.ConfigFileName,
		CustomConfigPath: s.CustomConfigPath,
	}

	if err := s._config.Read(); err != nil {
		return fmt.Errorf("Read Config Failed: %w", err)
	}

	if s._config.Instance.Port > 65535 {
		return fmt.Errorf("Configured Instance Port Not Valid: %s", "Tcp Port Max is 65535")
	}

	return nil
}

// retryWithBackoff executes an operation with exponential backoff
func retryWithBackoff(ctx context.Context, maxRetries int, operation func() error) error {
	backoff := initialBackoff
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		lastErr = operation()
		if lastErr == nil {
			return nil
		}

		// Skip backoff sleep after the last attempt — no point waiting.
		if attempt == maxRetries-1 {
			break
		}

		// FIX #14: Use time.NewTimer instead of time.After to avoid goroutine leak
		// when ctx.Done fires first.
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		backoff = time.Duration(float64(backoff) * backoffFactor)
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	return fmt.Errorf("operation failed after %d retries: %w", maxRetries, lastErr)
}

// setupServer sets up tcp listener, and creates grpc server
func (s *Service) setupServer() (lis net.Listener, ip string, port uint, err error) {
	if s._config == nil {
		return nil, "", 0, fmt.Errorf("Config Data Not Loaded")
	}

	if s.RegisterServiceHandlers == nil {
		return nil, "", 0, fmt.Errorf("Register Service Handlers Required")
	}

	if lis, err = util.GetNetListener(s._config.Instance.Port); err != nil {
		lis = nil
		ip = ""
		port = 0
		return
	} else {
		// enable xray if configured
		if s._config.Service.TracerUseXRay {
			_ = xray.Init("127.0.0.1:2000", "1.2.0")
			xray.SetXRayServiceOn()
		}

		// if rest target ca cert files defined, load self-signed ca certs so that this service may use those host resources
		if util.LenTrim(s._config.Service.RestTargetCACertFiles) > 0 {
			if err := rest.AppendServerCAPemFiles(strings.Split(s._config.Service.RestTargetCACertFiles, ",")...); err != nil {
				log.Println("!!! Load Rest Target Self-Signed CA Cert Files '" + s._config.Service.RestTargetCACertFiles + "' Failed: " + err.Error() + " !!!")
			}
		}

		//
		// config server options
		//
		var opts []grpc.ServerOption

		if s._config.Grpc.ConnectionTimeout > 0 {
			opts = append(opts, grpc.ConnectionTimeout(time.Duration(s._config.Grpc.ConnectionTimeout)*time.Second))
		}

		if util.LenTrim(s._config.Grpc.ServerCertFile) > 0 && util.LenTrim(s._config.Grpc.ServerKeyFile) > 0 {
			tls := new(tlsconfig.TlsConfig)
			// FIX: strings.Split("", ",") produces [""] not [] — guard against empty input
			var clientCACerts []string
			if util.LenTrim(s._config.Grpc.ClientCACertFiles) > 0 {
				clientCACerts = strings.Split(s._config.Grpc.ClientCACertFiles, ",")
			}
			if tc, e := tls.GetServerTlsConfig(s._config.Grpc.ServerCertFile, s._config.Grpc.ServerKeyFile, clientCACerts); e != nil {
				// FIX #5: Was log.Fatal which calls os.Exit(1) and bypasses all deferred
				// cleanup (listener close, SD deregister, etc.). Return error instead.
				return nil, "", 0, fmt.Errorf("Setup gRPC Server TLS Failed: %s", e.Error())
			} else {
				if len(s._config.Grpc.ClientCACertFiles) == 0 {
					log.Println("^^^ Server On TLS ^^^")
				} else {
					log.Println("^^^ Server On mTLS ^^^")
				}

				opts = append(opts, grpc.Creds(credentials.NewTLS(tc)))
			}
		} else {
			log.Println("~~~ Server Unsecured, Not On TLS ~~~")
		}

		// FIX #6: Original condition `KeepAliveMinWait >= 0` is always true for uint.
		// This caused the enforcement policy block to execute unconditionally, even when
		// not configured. Changed to `> 0` so the block only fires when explicitly set.
		if s._config.Grpc.KeepAliveMinWait > 0 || s._config.Grpc.KeepAlivePermitWithoutStream {
			minTime := 10 * time.Second

			if s._config.Grpc.KeepAliveMinWait > 0 {
				minTime = time.Duration(s._config.Grpc.KeepAliveMinWait) * time.Second
			}

			opts = append(opts, grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
				MinTime:             minTime,
				PermitWithoutStream: s._config.Grpc.KeepAlivePermitWithoutStream,
			}))
		}

		svrParam := keepalive.ServerParameters{}
		svrParamCount := 0

		if s._config.Grpc.KeepAliveMaxConnIdle > 0 {
			svrParam.MaxConnectionIdle = time.Duration(s._config.Grpc.KeepAliveMaxConnIdle) * time.Second
			svrParamCount++
		}

		if s._config.Grpc.KeepAliveMaxConnAge > 0 {
			svrParam.MaxConnectionAge = time.Duration(s._config.Grpc.KeepAliveMaxConnAge) * time.Second
			svrParamCount++
		}

		if s._config.Grpc.KeepAliveMaxConnAgeGrace > 0 {
			svrParam.MaxConnectionAgeGrace = time.Duration(s._config.Grpc.KeepAliveMaxConnAgeGrace) * time.Second
			svrParamCount++
		}

		if s._config.Grpc.KeepAliveInactivePingTimeTrigger > 0 {
			svrParam.Time = time.Duration(s._config.Grpc.KeepAliveInactivePingTimeTrigger) * time.Second
			svrParamCount++
		}

		if s._config.Grpc.KeepAliveInactivePingTimeout > 0 {
			svrParam.Timeout = time.Duration(s._config.Grpc.KeepAliveInactivePingTimeout) * time.Second
			svrParamCount++
		}

		if svrParamCount > 0 {
			opts = append(opts, grpc.KeepaliveParams(svrParam))
		}

		if s._config.Grpc.ReadBufferSize > 0 {
			opts = append(opts, grpc.ReadBufferSize(int(s._config.Grpc.ReadBufferSize)))
		}

		if s._config.Grpc.WriteBufferSize > 0 {
			opts = append(opts, grpc.WriteBufferSize(int(s._config.Grpc.WriteBufferSize)))
		}

		if s._config.Grpc.MaxReceiveMessageSize > 0 {
			opts = append(opts, grpc.MaxRecvMsgSize(int(s._config.Grpc.MaxReceiveMessageSize)))
		}

		if s._config.Grpc.MaxSendMessageSize > 0 {
			opts = append(opts, grpc.MaxSendMsgSize(int(s._config.Grpc.MaxSendMessageSize)))
		}

		if s._config.Grpc.MaxConcurrentStreams > 0 {
			opts = append(opts, grpc.MaxConcurrentStreams(uint32(s._config.Grpc.MaxConcurrentStreams)))
		}

		if s._config.Grpc.NumStreamWorkers > 0 {
			opts = append(opts, grpc.NumStreamWorkers(uint32(s._config.Grpc.NumStreamWorkers)))
		}

		// add unary server interceptors
		if xray.XRayServiceOn() {
			s.UnaryServerInterceptors = append(s.UnaryServerInterceptors, tracer.TracerUnaryServerInterceptor(s._config.Service.Name+"-Server"))
		}

		s.UnaryServerInterceptors = append(s.UnaryServerInterceptors, grpc_recovery.UnaryServerInterceptor())

		count := len(s.UnaryServerInterceptors)

		if count == 1 {
			opts = append(opts, grpc.UnaryInterceptor(s.UnaryServerInterceptors[0]))
		} else if count > 1 {
			opts = append(opts, grpc.ChainUnaryInterceptor(s.UnaryServerInterceptors...))
		}

		// add stream server interceptors
		if xray.XRayServiceOn() {
			s.StreamServerInterceptors = append(s.StreamServerInterceptors, tracer.TracerStreamServerInterceptor)
		}

		s.StreamServerInterceptors = append(s.StreamServerInterceptors, grpc_recovery.StreamServerInterceptor())

		count = len(s.StreamServerInterceptors)

		if count == 1 {
			opts = append(opts, grpc.StreamInterceptor(s.StreamServerInterceptors[0]))
		} else if count > 1 {
			opts = append(opts, grpc.ChainStreamInterceptor(s.StreamServerInterceptors...))
		}

		// rate limit control
		if s.RateLimit == nil {
			log.Println("Rate Limiter Nil, Checking If Need To Create...")

			if s._config.Grpc.RateLimitPerSecond > 0 {
				log.Println("Creating Default Rate Limiter...")
				s.RateLimit = ratelimitplugin.NewRateLimitPlugin(int(s._config.Grpc.RateLimitPerSecond), false)
			} else {
				log.Println("Rate Limiter Config Per Second = ", s._config.Grpc.RateLimitPerSecond)
			}
		}

		if s.RateLimit != nil {
			log.Println("Setup Rate Limiter - In Tap Handle")

			// InTapHandle is invoked by gRPC BEFORE the server allocates a stream
			// for the RPC. It is the cheapest place to throttle/reject excess
			// traffic. The underlying RateLimit.Take() is a blocking leaky bucket
			// (see adapters/ratelimiter/ratelimitplugin): it returns a time.Time
			// after sleeping for the configured interval, and has no "denied"
			// return. So the rate limit is enforced by *blocking* — not by
			// rejecting — but we still surface saturation to the caller when
			// the caller's deadline expired while we were waiting in the bucket.
			// That gives callers a clear codes.ResourceExhausted rather than
			// an eventual DeadlineExceeded from somewhere deeper in the stack.
			//
			// Per-RPC info-level logging was removed from here: at even modest
			// QPS it generated two log lines per call, overwhelming stdout.
			opts = append(opts, grpc.InTapHandle(func(ctx context.Context, info *tap.Info) (context.Context, error) {
				// Fast-path: if the context is already done before we even wait,
				// the caller gave up. Surface it as ResourceExhausted because
				// this is the rate-limit gate — callers should retry with
				// backoff, not assume a transient network failure.
				if err := ctx.Err(); err != nil {
					return ctx, status.Errorf(codes.ResourceExhausted,
						"rate limit gate: context done before take (%s): %v",
						info.FullMethodName, err)
				}

				s.RateLimit.Take() // blocks for the configured per-RPC interval

				// Post-wait check: if the caller's deadline expired while we
				// blocked in the bucket, the system is saturated — reject
				// with ResourceExhausted instead of letting the RPC proceed
				// into a guaranteed DeadlineExceeded.
				if err := ctx.Err(); err != nil {
					return ctx, status.Errorf(codes.ResourceExhausted,
						"rate limit exceeded: caller deadline elapsed during take (%s): %v",
						info.FullMethodName, err)
				}

				return ctx, nil
			}))
		}

		// for monitoring use
		if s.StatsHandler != nil {
			opts = append(opts, grpc.StatsHandler(s.StatsHandler))
		}

		// bi-di stream handler for unknown requests
		if s.UnknownStreamHandler != nil {
			opts = append(opts, grpc.UnknownServiceHandler(s.UnknownStreamHandler))
		}

		//
		// create server with options if any
		//
		s._grpcServer = grpc.NewServer(opts...)
		// SVC-F2 coverage extension (C2-003): wrap the caller-supplied
		// RegisterServiceHandlers in panic recovery so a buggy
		// registrar fails Serve cleanly instead of crashing the
		// startup goroutine. A typed-nil generated registrar that
		// panics on first method dispatch is a realistic failure
		// mode; without this wrapper it crashes the test binary.
		// Close the listener on the error path so the bind doesn't
		// leak — pre-SVC-F2-extension siblings (FavorPublicIP error
		// returns) leak too, but new code should not.
		if regErr := s.runRegisterHandlers(s.RegisterServiceHandlers); regErr != nil {
			_ = lis.Close()
			return nil, "", 0, regErr
		}

		ip = util.GetLocalIP()

		//
		// if instance prefers public ip, will attempt to acquire thru public ip discovery gateway
		//
		if s._config.Instance.FavorPublicIP && util.LenTrim(s._config.Instance.PublicIPGateway) > 0 && util.LenTrim(s._config.Instance.PublicIPGatewayKey) > 0 {
			validationToken := crypto.Sha256(util.FormatDate(time.Now().UTC()), s._config.Instance.PublicIPGatewayKey)

			publicIPSeg := xray.NewSegmentNullable("GrpcService-SetupServer")
			if publicIPSeg != nil {
				_ = publicIPSeg.Seg.AddMetadata("Public-IP-Gateway", s._config.Instance.PublicIPGateway)
				_ = publicIPSeg.Seg.AddMetadata("Hash-Date", util.FormatDate(time.Now().UTC()))
				_ = publicIPSeg.Seg.AddMetadata("Hash-Validation-Token", validationToken)
			}

			if status, body, err := rest.GET(s._config.Instance.PublicIPGateway, []*rest.HeaderKeyValue{
				{
					Key:   "x-nts-gateway-token",
					Value: validationToken,
				},
			}); err != nil {
				buf := "!!! Get Instance Public IP via '" + s._config.Instance.PublicIPGateway + "' with Header 'x-nts-gateway-token' Failed: " + err.Error() + ", Service Launch Stopped !!!"
				log.Println(buf)

				if publicIPSeg != nil {
					_ = publicIPSeg.Seg.AddError(errors.New(buf))
					publicIPSeg.Close()
				}

				return nil, "", 0, errors.New(buf)
			} else if status != 200 {
				buf := "!!! Get Instance Public IP via '" + s._config.Instance.PublicIPGateway + "' with Header 'x-nts-gateway-token' Not Successful: Status Code " + util.Itoa(status) + ", Service Launch Stopped !!!"
				log.Println(buf)

				if publicIPSeg != nil {
					_ = publicIPSeg.Seg.AddError(errors.New(buf))
					publicIPSeg.Close()
				}

				return nil, "", 0, errors.New(buf)
			} else {
				if net.ParseIP(strings.TrimSpace(body)) != nil {
					if publicIPSeg != nil {
						_ = publicIPSeg.Seg.AddMetadata("Result-Private-IP", ip)
						_ = publicIPSeg.Seg.AddMetadata("Result-Public-IP", body)
						publicIPSeg.Close()
					}

					log.Println("=== Instance Using Public IP '" + body + "' From Discovery Gateway Per Service Config, Original LocalIP was '" + ip + "' ===")
					ip = body
				} else {
					buf := "!!! Get Instance Public IP via '" + s._config.Instance.PublicIPGateway + "' with Header 'x-nts-gateway-token' Not Successful: Status Code 200, But Content Not IP: " + body + ", Service Launch Stopped !!!"
					log.Println(buf)

					if publicIPSeg != nil {
						_ = publicIPSeg.Seg.AddError(errors.New(buf))
						publicIPSeg.Close()
					}

					return nil, "", 0, errors.New(buf)
				}
			}
		} else {
			if s._config.Instance.FavorPublicIP {
				buf := "!!! Instance Favors Public IP, However, Service Config Missing Public IP Discovery Gateway and/or Public IP Gateway Key, Service Launch Stopped !!!"
				log.Println(buf)
				return nil, "", 0, fmt.Errorf("%s", buf)
			} else {
				log.Println("=== Instance Using LocalIP Per Service Config Setting ===")
			}
		}

		// set port
		port = util.StrToUint(util.SplitString(lis.Addr().String(), ":", -1))
		s.setLocalAddress(fmt.Sprintf("%s:%d", ip, port))

		//
		// setup sqs and sns if needed
		//
		if s._config.Service.DiscoveryUseSqsSns || s._config.Service.LoggerUseSqs {
			var snsTopicArns []string
			needConfigSave := false

			if s._sqs, err = queue.NewQueueAdapter(awsregion.GetAwsRegion(s._config.Target.Region), nil); err != nil {
				return nil, "", 0, fmt.Errorf("Get SQS Queue Adapter Failed: %s", err)
			}

			if s._config.Service.DiscoveryUseSqsSns {
				if s._sns, err = notification.NewNotificationAdapter(awsregion.GetAwsRegion(s._config.Target.Region), nil); err != nil {
					return nil, "", 0, fmt.Errorf("Get SNS Notification Adapter Failed: %s", err)
				}

				if snsTopicArns, err = notification.ListTopics(s._sns, time.Duration(s._config.Instance.SdTimeout)*time.Second); err != nil {
					return nil, "", 0, fmt.Errorf("Get SNS Topics List Failed: %s", err)
				}

				if util.LenTrim(s._config.Topics.SnsDiscoveryTopicArn) == 0 || !util.StringSliceContains(&snsTopicArns, s._config.Topics.SnsDiscoveryTopicArn) {
					discoverySnsTopic := s._config.Topics.SnsDiscoveryTopicNamePrefix + s._config.Service.Name + "." + s._config.Namespace.Name
					discoverySnsTopic, extractErr := util.ExtractAlphaNumericUnderscoreDash(util.Replace(discoverySnsTopic, ".", "-"))
					if extractErr != nil {
						log.Printf("warning: ExtractAlphaNumericUnderscoreDash failed for SNS topic %q: %v", discoverySnsTopic, extractErr)
					}

					if topicArn, e := notification.CreateTopic(s._sns, discoverySnsTopic, time.Duration(s._config.Instance.SdTimeout)*time.Second); e != nil {
						return nil, "", 0, fmt.Errorf("Create SNS Topic %s Failed: %s", discoverySnsTopic, e)
					} else {
						snsTopicArns = append(snsTopicArns, topicArn)
						s._config.SetSnsDiscoveryTopicArn(topicArn)
						needConfigSave = true
					}
				}

				if util.LenTrim(s._config.Queues.SqsDiscoveryQueueArn) == 0 || util.LenTrim(s._config.Queues.SqsDiscoveryQueueUrl) == 0 {
					discoveryQueueName := s._config.Queues.SqsDiscoveryQueueNamePrefix + s._config.Service.Name + "." + s._config.Namespace.Name
					discoveryQueueName, extractErr := util.ExtractAlphaNumericUnderscoreDash(util.Replace(discoveryQueueName, ".", "-"))
					if extractErr != nil {
						log.Printf("warning: ExtractAlphaNumericUnderscoreDash failed for SQS discovery queue %q: %v", discoveryQueueName, extractErr)
					}

					if url, arn, e := queue.GetQueue(s._sqs, discoveryQueueName, s._config.Queues.SqsDiscoveryMessageRetentionSeconds, s._config.Topics.SnsDiscoveryTopicArn, time.Duration(s._config.Instance.SdTimeout)*time.Second); e != nil {
						return nil, "", 0, fmt.Errorf("Create SQS Queue %s Failed: %s", discoveryQueueName, e)
					} else {
						s._config.SetSqsDiscoveryQueueUrl(url)
						s._config.SetSqsDiscoveryQueueArn(arn)
						needConfigSave = true
					}
				}
			}

			if s._config.Service.LoggerUseSqs {
				if util.LenTrim(s._config.Queues.SqsLoggerQueueArn) == 0 || util.LenTrim(s._config.Queues.SqsLoggerQueueUrl) == 0 {
					loggerQueueName := s._config.Queues.SqsLoggerQueueNamePrefix + s._config.Service.Name + "." + s._config.Namespace.Name
					loggerQueueName, extractErr := util.ExtractAlphaNumericUnderscoreDash(util.Replace(loggerQueueName, ".", "-"))
					if extractErr != nil {
						log.Printf("warning: ExtractAlphaNumericUnderscoreDash failed for SQS logger queue %q: %v", loggerQueueName, extractErr)
					}

					if url, arn, e := queue.GetQueue(s._sqs, loggerQueueName, s._config.Queues.SqsLoggerMessageRetentionSeconds, "", time.Duration(s._config.Instance.SdTimeout)*time.Second); e != nil {
						return nil, "", 0, fmt.Errorf("Create SQS Queue %s Failed: %s", loggerQueueName, e)
					} else {
						s._config.SetSqsLoggerQueueUrl(url)
						s._config.SetSqsLoggerQueueArn(arn)
						needConfigSave = true
					}
				}
			}

			if needConfigSave {
				if e := s._config.Save(); e != nil {
					return nil, "", 0, fmt.Errorf("Save Config for SNS SQS ARNs Failed: %s", e)
				}
			}
		}

		err = nil
		return
	}
}

// connectSd will try to establish service discovery object to struct
func (s *Service) connectSd() error {
	// FIX #7: Guard against nil config to prevent panic if called before readConfig
	if s._config == nil {
		return fmt.Errorf("Connect SD Failed: Config Data Not Loaded")
	}

	if util.LenTrim(s._config.Namespace.Id) > 0 && util.LenTrim(s._config.Target.Region) > 0 {
		s._sd = &cloudmap.CloudMap{
			AwsRegion: awsregion.GetAwsRegion(s._config.Target.Region),
		}

		if err := s._sd.Connect(); err != nil {
			return fmt.Errorf("Connect SD Failed: %w", err)
		}
	} else {
		s._sd = nil
	}

	return nil
}

// startHealthChecker will launch the grpc health v1 health service
func (s *Service) startHealthChecker() error {
	s._mu.RLock()
	gs := s._grpcServer
	s._mu.RUnlock()

	if gs == nil {
		return fmt.Errorf("Health Check Server Can't Start: gRPC Server Not Started")
	}

	grpc_health_v1.RegisterHealthServer(gs, health.NewHealthServer(s.DefaultHealthCheckHandler, s.ServiceHealthCheckHandlers))

	return nil
}

// CurrentlyServing indicates if this service health status indicates currently serving mode or not
func (s *Service) CurrentlyServing() bool {
	if s == nil {
		return false
	}
	s._mu.RLock()
	defer s._mu.RUnlock()
	return s._serving
}

// startServerFn is the indirection used by Serve() to invoke startServer.
// Production code MUST leave this as the default (*Service).startServer
// method-value — it exists solely so the P1-CONN-SVC-02 regression test
// at service/service_svc_serve_error_test.go can inject a synthetic
// startServer failure and exercise the real Serve() error branch end
// to end. See SP-010 pass-5 finding A2-F-2B (2026-04-15) and lesson L18
// for the causal-path-vs-postcondition distinction that motivated this
// injection point.
//
// Concurrency: this var is written only during test setup (before a
// Serve() call) and restored via defer at test teardown. Tests must NOT
// mutate it concurrently with a running Serve(), nor from parallel
// tests; the service package owns the single Serve() contract and the
// P1-CONN-SVC-02 tests run sequentially.
var startServerFn = (*Service).startServer

// startServer will start and serve grpc services, it will run in goroutine until terminated.
// FIX #8: Replaced busy-wait (default + time.Sleep(10ms)) with blocking on quit channel
// after server startup is complete.
func (s *Service) startServer(lis net.Listener, quit chan bool, quitDone chan struct{}) (err error) {
	seg := xray.NewSegmentNullable("GrpcService-StartServer")
	if seg != nil {
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.SafeAddError(err)
			}
		}()
	}

	if s._grpcServer == nil {
		err = fmt.Errorf("gRPC Server Not Setup")
		return err
	}

	// set service default serving mode to 'not serving'
	s.setServing(false)

	stopHealthReportService := make(chan bool, 1)

	// Launch server startup in a goroutine
	safeGo("startup-orchestrator", func() {
		if s.BeforeServerStart != nil {
			log.Println("Before gRPC Server Starts Begin...")
			s.runHook("BeforeServerStart", s.BeforeServerStart)
			log.Println("... Before gRPC Server Starts End")
		}

		log.Println("Initiating gRPC Server Startup...")

		safeGo("grpc-server", func() {
			log.Println("Starting gRPC Health Server...")

			if startErr := s.startHealthChecker(); startErr != nil {
				log.Println("!!! gRPC Health Server Fail To Start: " + startErr.Error() + " !!!")
			} else {
				log.Println("... gRPC Health Server Started")
			}

			// Snapshot shared state under lock to prevent race with quit handler
			s._mu.RLock()
			grpcSrv := s._grpcServer
			appName := s._config.AppName
			s._mu.RUnlock()

			if grpcSrv == nil {
				log.Println("!!! gRPC Server is nil, cannot serve !!!")
				return
			}

			if serveErr := grpcSrv.Serve(lis); serveErr != nil {
				// FIX #13: Was log.Fatalf which calls os.Exit(1) and bypasses ALL deferred
				// cleanup (SD deregister, SNS unsubscribe, health report removal, etc.).
				// Send SIGTERM to self instead, which triggers awaitOsSigExit() and runs
				// the full graceful shutdown path.
				//
				// CONN-R2-003: Gate self-SIGTERM on _sigHandlerReady — if Serve fails
				// before awaitOsSigExit calls signal.Notify, the SIGTERM would hit Go's
				// default handler and kill the process. Same pattern as GracefulStop and
				// ImmediateStop.
				log.Printf("Serve gRPC Service %s on %s Failed: (Server Halt) %s", appName, s.LocalAddress(), serveErr.Error())
				if s._sigHandlerReady.Load() {
					if p, findErr := os.FindProcess(os.Getpid()); findErr == nil {
						_ = p.Signal(syscall.SIGTERM)
					}
				}
			} else {
				log.Println("... gRPC Server Quit Command Received")
			}
		})

		log.Println("... gRPC Server Startup Initiated")

		if s.WebServerConfig != nil {
			if util.LenTrim(s.WebServerConfig.ConfigFileName) > 0 {
				log.Println("Starting Http Web Server...")
				startWebServerFail := make(chan bool, 1)

				safeGo("web-server", func() {
					if webServerErr := s.startWebServer(); webServerErr != nil {
						log.Printf("!!! Serve Http Web Server %s Failed: %s !!!\n", s.WebServerConfig.AppName, webServerErr)
						startWebServerFail <- true
					} else {
						log.Println("... Http Web Server Quit Command Received")
					}
				})

				time.Sleep(150 * time.Millisecond)

				select {
				case <-startWebServerFail:
					log.Println("... Http Web Server Fail to Start")
				default:
					log.Printf("... Http Web Server Started: %s\n", s.WebServerConfig.WebServerLocalAddress)
				}
			}
		}

		// trigger sd initial health update
		safeGo("sd-health-reporter", func() {
			s._mu.RLock()
			healthFailThreshold := s._config.SvcCreateData.HealthFailThreshold
			s._mu.RUnlock()
			waitTime := int(healthFailThreshold * 45)

			log.Println(">>> Instance Health Check Warm-Up: " + util.Itoa(waitTime) + " Seconds - Please Wait >>>")
			warmupTimer := time.NewTimer(time.Duration(waitTime) * time.Second)
			select {
			case <-warmupTimer.C:
			case <-stopHealthReportService:
				warmupTimer.Stop()
				log.Println("<<< Instance Health Check Warm-Up: Interrupted by shutdown <<<")
				return
			}
			log.Println("<<< Instance Health Check Warm-Up: OK <<<")

			log.Println("+++ Updating Instance as Healthy with Service Discovery: Please Wait +++")

			var continueProcessing bool

			if healthErr := s.updateHealth(true); healthErr != nil {
				if strings.Contains(healthErr.Error(), "ServiceNotFound") {
					log.Println("~~~ Service Discovery Not Ready - Waiting 45 More Seconds ~~~")
					retryTimer := time.NewTimer(45 * time.Second)
					select {
					case <-retryTimer.C:
					case <-stopHealthReportService:
						retryTimer.Stop()
						log.Println("<<< Instance Health Check Retry: Interrupted by shutdown <<<")
						return
					}

					if healthErr = s.updateHealth(true); healthErr != nil {
						log.Println("!!! Update Instance Health Status with Service Discovery Failed: (With Retry) " + healthErr.Error() + " !!!")
					} else {
						continueProcessing = true
					}
				} else {
					log.Println("!!! Update Instance Health Status with Service Discovery Failed: " + healthErr.Error() + " !!!")
				}
			} else {
				continueProcessing = true
			}

			if continueProcessing {
				log.Println("+++ Update Instance as Healthy with Service Discovery: OK +++")

				// Snapshot config fields + shared pointers under single RLock for thread safety
				s._mu.RLock()
				useSqsSns := s._config.Service.DiscoveryUseSqsSns
				sqsLocal := s._sqs
				snsLocal := s._sns
				cfgQueueArn := s._config.Queues.SqsDiscoveryQueueArn
				cfgQueueUrl := s._config.Queues.SqsDiscoveryQueueUrl
				cfgTopicArn := s._config.Topics.SnsDiscoveryTopicArn
				cfgTopicSubArn := s._config.Topics.SnsDiscoverySubscriptionArn
				cfgSdTimeout := s._config.Instance.SdTimeout
				s._mu.RUnlock()

				if useSqsSns {
					log.Println("~~~ Service Discovery Push Notification Begin ~~~")

					if sqsLocal == nil {
						log.Println("!!! Service Discovery Push Notification Skipped - SQS Not Initialized, Check Config !!!")
					} else if snsLocal == nil {
						log.Println("!!! Service Discovery Push Notification Skipped - SNS Not Initialized, Check Config !!!")
					} else {
						qArn := cfgQueueArn
						qUrl := cfgQueueUrl
						tArn := cfgTopicArn
						tSubId := cfgTopicSubArn

						if util.LenTrim(qArn) == 0 {
							log.Println("!!! Service Discovery Push Notification Skipped - SQS Queue Not Auto Created (Missing QueueARN) !!!")
						} else if util.LenTrim(qUrl) == 0 {
							log.Println("!!! Service Discovery Push Notification Skipped - SQS Queue Not Auto Created (Missing QueueURL) !!!")
						} else if util.LenTrim(tArn) == 0 {
							log.Println("!!! Service Discovery Push Notification Skipped - SNS Topic Not Auto Created (Missing TopicARN) !!!")
						} else {
							pubOk := false

							if util.LenTrim(tSubId) == 0 {
								log.Println("+++ Instance Subscribing to SNS Topic '" + tArn + "' with SQS Queue '" + qArn + "' for Service Discovery Publishing +++")

								if subId, subErr := notification.Subscribe(snsLocal, tArn, snsprotocol.Sqs, qArn, time.Duration(cfgSdTimeout)*time.Second); subErr != nil {
									log.Println("!!! Service Discovery Push Notification Skipped - Instance Queue Topic Subscribe Failed: " + subErr.Error() + " !!!")
								} else {
									log.Println("+++ Instance Subscribing to SNS Topic '" + tArn + "' with SQS Queue '" + qArn + "' for Service Discovery Publishing: OK +++")

									s._mu.Lock()
									s._config.SetSnsDiscoverySubscriptionArn(subId)
									s._mu.Unlock()

									if cErr := s._config.Save(); cErr != nil {
										log.Println("!!! Service Discovery Push Notification Skipped - Instance Queue Topic Subscription Persist To Config Failed: " + cErr.Error() + " !!!")

										if uErr := notification.Unsubscribe(snsLocal, subId, time.Duration(cfgSdTimeout)*time.Second); uErr != nil {
											log.Println("!!! Service Discovery Push Notification Skipped - Instance Queue Save to Config Failed, Auto Unsubscribe Failed: " + uErr.Error() + " !!!")
										} else {
											log.Println("!!! Service Discovery Push Notification Skipped - Instance Queue Save to Config Failed, Auto Unsubscribe Successful !!!")
										}

										log.Println("!!! Service Discovery Push Notification Skipped - Publish Service Host Will Not Be Performed !!!")
									} else {
										pubOk = true
									}
								}
							} else {
								log.Println("+++ Instance Subscription Already Exists for SNS Topic '" + tArn + "' with SQS Queue '" + qArn + "'")
								pubOk = true
							}

							if pubOk {
								// P2-15: If shutdown was requested during the warmup/subscribe
								// window, skip the startup publish. publishToSNS wraps an SDK
								// call with only an internal timeout -- no context cancellation
								// -- so a blocking publish here would delay shutdown by up to
								// sdTimeout seconds. Re-signal the channel so any downstream
								// waiter (the ticker loop below, the quit handler) still fires.
								select {
								case <-stopHealthReportService:
									select {
									case stopHealthReportService <- true:
									default:
									}
									log.Println("### Service Discovery Push Notification: Skipped - Shutdown Requested ###")
									return
								default:
									log.Println("+++ Service Discovery Push Notification: Publish Host Info OK +++")
									s.publishToSNS(tArn, "Discovery Push Notification", s.getHostDiscoveryMessage(true), nil)
								}
							} else {
								log.Println("!!! Service Discovery Push Notification: Nothing to Publish, Possible Error Encountered !!!")
							}
						}
					}
				} else {
					log.Println("--- Service Discovery Push Notification Disabled ---")
				}

				// set service serving mode to true (serving)
				s.setServing(true)

				// -----------------------------------------------------------------------------------------
				// start service live timestamp reporting to data store
				// FIX #9: Replaced busy-wait (default + time.Sleep) with time.NewTicker
				// so the stop signal is honored immediately instead of after the sleep completes.
				// -----------------------------------------------------------------------------------------
				s._mu.RLock()
				freq := s._config.Instance.HealthReportUpdateFrequencySeconds
				s._mu.RUnlock()

				if freq == 0 {
					freq = 120
				} else if freq < 30 {
					freq = 30
				} else if freq > 300 {
					freq = 300
				}

				ticker := time.NewTicker(time.Duration(freq) * time.Second)
				defer ticker.Stop()

				// Perform initial report immediately
				if !s.setServiceHealthReportUpdateToDataStore() {
					log.Println("### Health Report Update To Data Store Service Stopped: Update Action Exception ###")
					return
				}

				for {
					select {
					case <-stopHealthReportService:
						log.Println("### Health Report Update To Data Store Service Stopped: Stop Signal Received ###")
						return
					case <-ticker.C:
						if !s.setServiceHealthReportUpdateToDataStore() {
							log.Println("### Health Report Update To Data Store Service Stopped: Update Action Exception ###")
							return
						}
					}
				}
			}
		})

		// trigger after server start event
		if s.AfterServerStart != nil {
			s.runHook("AfterServerStart", s.AfterServerStart)
		}
	})

	// Quit handler goroutine: performs full graceful cleanup on shutdown signal,
	// matching the cleanup done by GracefulStop()/ImmediateStop().
	// Closes quitDone when all cleanup is complete so Serve() can wait.
	safeGo("quit-handler", func() {
		defer close(quitDone)
		<-quit

		log.Println("gRPC Server Quit Invoked")

		// Publish offline notification via SNS
		s._mu.RLock()
		cfg := s._config
		s._mu.RUnlock()
		if cfg != nil {
			s.publishToSNS(cfg.Topics.SnsDiscoveryTopicArn, "Discovery Push Notification", s.getHostDiscoveryMessage(false), nil)
		}

		// Unsubscribe from SNS topics
		s.unsubscribeSNS()

		// Delete health report from data store
		s._mu.RLock()
		instanceId := s._config.Instance.Id
		s._mu.RUnlock()

		if util.LenTrim(instanceId) > 0 {
			log.Println("Removing Health Report Service Record From Data Store for InstanceID '" + instanceId + "'...")

			if e := s.deleteServiceHealthReportFromDataStore(instanceId); e != nil {
				log.Println("!!! Removing Health Report Service Record From Data Store for InstanceID '" + instanceId + "' Failed: " + e.Error() + " !!!")
			} else {
				log.Println("...Removing Health Report Service Record From Data Store for InstanceID '" + instanceId + "': OK")
			}
		} else {
			log.Println("!!! Remove Health Report Service Record From Data Store Not Needed: InstanceID is Blank !!!")
		}

		// stop health report service
		select {
		case stopHealthReportService <- true:
		default:
		}

		// clean up web server dns recordset if any
		//
		// F2 (deep-review-2026-04-14-contrarian-pass3): MUST use
		// runCleanup, not a raw call. The surrounding quit-handler
		// body is wrapped in safeGo which recovers panics to keep
		// the PROCESS alive, but a raw panic here still unwinds the
		// quit-handler frame through the panic path — skipping every
		// subsequent step (setLocalAddress, setServing(false), Cloud
		// Map deregister, SD/SNS/SQS Disconnect, fireShutdownCancel
		// PreDrain/PostGraceExpiry, gs.GracefulStop/gs.Stop,
		// lis.Close). safeGo keeps the process up; runCleanup keeps
		// the per-step teardown invariants. Both are needed.
		if s.WebServerConfig != nil {
			runCleanup("WebServerConfig.CleanUp(quit-handler)", s.WebServerConfig.CleanUp)
		}

		// clear local address
		s.setLocalAddress("")

		// on exit, stop serving
		s.setServing(false)

		// Deregister SD instance
		s._mu.RLock()
		hasSd := s._sd != nil
		s._mu.RUnlock()
		if hasSd {
			if err := s.deregisterInstance(); err != nil {
				log.Println("De-Register Instance Failed From Serve Shutdown: " + err.Error())
			} else {
				log.Println("De-Register Instance OK From Serve Shutdown")
			}
		}

		// Disconnect AWS clients and stop gRPC server under lock
		s._mu.Lock()
		sd := s._sd
		s._sd = nil
		sqsC := s._sqs
		s._sqs = nil
		snsC := s._sns
		s._sns = nil
		gs := s._grpcServer
		s._grpcServer = nil
		s._mu.Unlock()

		if sd != nil {
			sd.Disconnect()
		}
		if sqsC != nil {
			sqsC.Disconnect()
		}
		if snsC != nil {
			snsC.Disconnect()
		}
		// SVC-F5: fire PreDrain BEFORE entering the bounded graceful
		// stop, so handlers that opted into ShutdownCtx learn to stop
		// cooperating at the same lifecycle point where we stop
		// routing new traffic. No-op unless ShutdownCancel is true
		// and the configured phase is PreDrain.
		s.fireShutdownCancelIfPhase(ShutdownPhasePreDrain)

		// SVC-F7: if an exported caller invoked ImmediateStop() — even
		// concurrently with the signal path — bypass the bounded
		// graceful drain entirely and call gs.Stop() directly. The
		// PostGraceExpiry SVC-F5 hook still fires synchronously
		// beforehand so handlers observing ShutdownCtx see the cancel
		// strictly before the transport is severed (rule #13). This
		// preserves ImmediateStop's break-glass semantics on the
		// unified routing path.
		if s._immediateStopRequested.Load() {
			if gs != nil {
				log.Println("ImmediateStop requested — bypassing graceful drain")
				s.fireShutdownCancelIfPhase(ShutdownPhasePostGraceExpiry)
				gs.Stop()
			}
		} else {
			// SVC-F1 fix: honor the lifecycle godoc on the default signal
			// path (GracefulStop with bounded fallback to Stop) instead of
			// going straight to Stop.
			//
			// SVC-F5: the WithHook variant gives us a synchronous fire
			// site at the moment the grace timeout expires and Stop() is
			// about to be invoked. No-op unless ShutdownCancel is true
			// and the configured phase is PostGraceExpiry.
			stopGRPCServerBoundedWithHook(gs, s.GracefulStopTimeout, func() {
				s.fireShutdownCancelIfPhase(ShutdownPhasePostGraceExpiry)
			})
		}
		_ = lis.Close()
	})

	return nil
}

// gracefulStopper is the subset of *grpc.Server used by
// stopGRPCServerBounded. Extracted so the timeout/escalation logic can
// be unit-tested without standing up a real gRPC listener (SVC-F1).
type gracefulStopper interface {
	GracefulStop()
	Stop()
}

// stopGRPCServerBounded calls gs.GracefulStop, escalating to gs.Stop
// if the graceful drain exceeds timeout. Returns only after the
// GracefulStop goroutine has returned (so callers can safely close
// listeners / release resources afterward). A nil gs is a no-op.
// A zero or negative timeout defaults to 30 seconds.
//
// This replaces the pre-SVC-F1 behavior where the default signal path
// called gs.Stop directly, silently contradicting the Service's
// lifecycle godoc and killing in-flight RPCs.
//
// Call sites that need to observe the "grace exceeded, about to
// escalate" boundary (for example, to fire a SVC-F5 ShutdownCtx
// cancel) should use stopGRPCServerBoundedWithHook instead. This
// function is a thin wrapper that passes a nil hook.
func stopGRPCServerBounded(gs gracefulStopper, timeout time.Duration) {
	stopGRPCServerBoundedWithHook(gs, timeout, nil)
}

// stopGRPCServerBoundedWithHook is the same as stopGRPCServerBounded
// but invokes onEscalate exactly once, synchronously, immediately
// before gs.Stop() is called when the graceful drain exceeds timeout.
// If the graceful drain completes within the timeout, onEscalate is
// NOT invoked. A nil hook is a no-op. A nil gs is a no-op
// (onEscalate is not called).
//
// The hook is the SVC-F5 PostGraceExpiry fire site — it runs on the
// caller's goroutine (NOT the graceful-stop-escalator goroutine), so
// the cancel happens-before the subsequent Stop() call without any
// cross-goroutine coordination.
func stopGRPCServerBoundedWithHook(gs gracefulStopper, timeout time.Duration, onEscalate func()) {
	if gs == nil {
		return
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	stopped := make(chan struct{})
	safeGo("graceful-stop-escalator", func() {
		defer close(stopped)
		gs.GracefulStop()
	})
	// FIX #14: use time.NewTimer + explicit Stop() instead of time.After,
	// so the timer goroutine does not leak when the graceful drain
	// completes before the timeout elapses (time.After cannot be GC'd
	// until it fires).
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-stopped:
	case <-t.C:
		log.Printf("GracefulStop exceeded %s — escalating to Stop", timeout)
		if onEscalate != nil {
			safeCallEscalateHook(onEscalate)
		}
		gs.Stop()
		<-stopped // ensure the GracefulStop goroutine has returned
	}
}

// safeCallEscalateHook calls fn inside a recover guard. The hook is
// supplied by connector-internal code (fireShutdownCancelIfPhase), not
// consumer code, so a panic here would be a bug — but we still recover
// so the escalation path reliably falls through to gs.Stop() no matter
// what the hook does.
func safeCallEscalateHook(fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("!!! stopGRPCServerBounded onEscalate panic recovered: %v\n%s !!!", r, debug.Stack())
		}
	}()
	fn()
}

// getHostDiscoveryMessage returns json string formatted with online / offline status indicator along with host address info
func (s *Service) getHostDiscoveryMessage(online bool) string {
	if s == nil {
		return ""
	}

	onlineStatus := ""

	if online {
		onlineStatus = "online"
	} else {
		onlineStatus = "offline"
	}

	// Escape host for JSON safety (defense-in-depth against malformed gateway responses)
	host := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s.LocalAddress())
	return fmt.Sprintf(`{"msg_type":"host-discovery", "action":"%s", "host":"%s"}`, onlineStatus, host)
}

// publishToSNS publishes message to an sns topic, if sns is setup
func (s *Service) publishToSNS(topicArn string, actionName string, message string, attributes map[string]*sns2.MessageAttributeValue) {
	s._mu.RLock()
	snsClient := s._sns
	sdTimeout := uint(0)
	if s._config != nil {
		sdTimeout = s._config.Instance.SdTimeout
	}
	s._mu.RUnlock()

	if snsClient == nil {
		return
	}

	if util.LenTrim(topicArn) == 0 {
		return
	}

	if util.LenTrim(message) == 0 {
		return
	}

	if id, err := notification.Publish(snsClient, topicArn, message, attributes, time.Duration(sdTimeout)*time.Second); err != nil {
		log.Println("!!! " + actionName + " - Publish Failed: " + err.Error() + " !!!")
	} else {
		log.Println("... " + actionName + " - Publish OK: " + id)
	}
}

// registerSd registers instance to sd
func (s *Service) registerSd(ip string, port uint) error {
	s._mu.RLock()
	cfg := s._config
	sd := s._sd
	s._mu.RUnlock()

	if cfg == nil || sd == nil {
		return nil
	}

	if err := s.registerInstance(ip, port, !cfg.Instance.InitialUnhealthy, cfg.Instance.Version); err != nil {
		if util.LenTrim(cfg.Instance.Id) > 0 {
			log.Println("Instance Registered Has Error: (Will Auto De-Register) " + err.Error())
			if err1 := s.deregisterInstance(); err1 != nil {
				log.Println("... De-Register Instance Failed: " + err1.Error())
			} else {
				log.Println("... De-Register Instance OK")
			}
		}
		return fmt.Errorf("Register Instance Failed: %w", err)
	}

	return nil
}

// awaitOsSigExit handles os exit event for clean up
func (s *Service) awaitOsSigExit() {
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)

	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)

	// SVC-F8 (F1 fix): mark the handler as ready so exported stop
	// methods know it's safe to self-deliver SIGTERM. Must be set
	// AFTER signal.Notify so a concurrent GracefulStop observing
	// the flag sees a handler that actually catches the signal;
	// otherwise the self-signal hits the Go runtime default handler
	// and terminates the process. Clear (via Store(false)) during
	// signal.Stop at function exit to keep the contract honest if
	// the Service were ever re-used — though SVC-F3's single-use
	// gate prevents re-use in practice.
	s._sigHandlerReady.Store(true)

	safeGo("sig-demux", func() {
		sig := <-sigs
		log.Println("OS Sig Exit Command: ", sig)
		// SVC-F5: fire Immediate phase BEFORE notifying Serve that a
		// signal arrived. Firing here (same goroutine, before the
		// channel send) creates a happens-before edge with every step
		// Serve takes after <-done returns — so handlers observing
		// ShutdownCtx.Done() see the cancel strictly before
		// BeforeServerShutdown and any subsequent shutdown step.
		// No-op unless ShutdownCancel is true and the configured
		// phase is Immediate.
		s.fireShutdownCancelIfPhase(ShutdownPhaseImmediate)
		done <- true
	})

	log.Println("=== Press 'Ctrl + C' to Shutdown ===")
	s._mu.RLock()
	hft := s._config.SvcCreateData.HealthFailThreshold
	s._mu.RUnlock()
	log.Printf("Please Wait for Instance Service Discovery To Complete... (This may take %v Seconds)", hft*45)

	<-done

	// FIX #29: Stop signal delivery so the channel does not leak
	signal.Stop(sigs)

	// SVC-F8 (F1 fix): clear readiness so any late programmatic stop
	// after signal.Stop does NOT self-signal into a no-op handler.
	// Single-use Service per SVC-F3 means this is defence-in-depth.
	s._sigHandlerReady.Store(false)

	log.Println("*** Shutdown Invoked ***")
}

// deleteServiceHealthReportFromDataStore will remove the health report service record from data store based on instanceId
func (s *Service) deleteServiceHealthReportFromDataStore(instanceId string) (err error) {
	seg := xray.NewSegmentNullable("GrpcService-DeleteServiceHealthReportFromDataStore")
	if seg != nil {
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.SafeAddError(err)
			}
		}()
	}

	if util.LenTrim(instanceId) == 0 {
		err = fmt.Errorf("Delete Health Report Service Record Requires InstanceID")
		return err
	}

	s._mu.RLock()
	cfg := s._config
	s._mu.RUnlock()

	if cfg == nil {
		err = fmt.Errorf("Delete Health Report Service Record Requires Config Object")
		return err
	}

	if util.LenTrim(cfg.Instance.HealthReportServiceUrl) == 0 {
		err = fmt.Errorf("Delete Health Report Service Record Requires HealthReportServiceUrl")
		return err
	}

	if util.LenTrim(cfg.Instance.HashKeyName) == 0 {
		err = fmt.Errorf("Delete Health Report Service Record Requires HashKeyName")
		return err
	}

	if util.LenTrim(cfg.Instance.HashKeySecret) == 0 {
		err = fmt.Errorf("Delete Health Report Service Record Requires HashKeySecret")
		return err
	}

	var subSeg *xray.XSegment

	if seg != nil {
		subSeg = seg.NewSubSegment("REST DEL: " + cfg.Instance.HealthReportServiceUrl)
		_ = subSeg.Seg.AddMetadata("x-nts-gateway-hash-name", cfg.Instance.HashKeyName)
	}

	statusCode, _, e := rest.DELETE(cfg.Instance.HealthReportServiceUrl+"/"+url.PathEscape(instanceId), []*rest.HeaderKeyValue{
		{
			Key:   "Content-Type",
			Value: "application/json",
		},
		{
			Key:   "x-nts-gateway-hash-name",
			Value: cfg.Instance.HashKeyName,
		},
		{
			Key:   "x-nts-gateway-hash-signature",
			Value: crypto.Sha256(instanceId+util.FormatDate(time.Now().UTC()), cfg.Instance.HashKeySecret),
		},
	})

	if subSeg != nil {
		subSeg.Close()
	}

	if e != nil {
		err = fmt.Errorf("Delete Health Report Service Record Failed: %s", e.Error())
		return err
	}

	if statusCode != 200 {
		err = fmt.Errorf("Delete Health Report Service Record Failed: %s", "Status Code "+util.Itoa(statusCode))
		return err
	}

	return nil
}

// setServiceHealthReportUpdateToDataStore updates this service with dynamodb Common-Hosts with last hit timestamp
func (s *Service) setServiceHealthReportUpdateToDataStore() bool {
	var err error

	seg := xray.NewSegmentNullable("GrpcService-setServiceHealthReportUpdateToDataStore")
	if seg != nil {
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.SafeAddError(err)
			}
		}()
	}

	// Snapshot config pointer under lock to prevent race with concurrent access
	s._mu.RLock()
	cfg := s._config
	s._mu.RUnlock()

	if cfg == nil {
		err = fmt.Errorf("Set Service Health Report Update To Data Store Stopped: %s", "Service Config Nil")
		log.Println(err.Error())
		return false
	}

	if util.LenTrim(cfg.Instance.HealthReportServiceUrl) == 0 {
		return false
	}

	if util.LenTrim(cfg.Instance.HashKeyName) == 0 {
		err = fmt.Errorf("Set Service Health Report Update To Data Store Stopped: %s", "Hash Key Name Not Defined in Config")
		log.Println(err.Error())
		return false
	}

	if util.LenTrim(cfg.Instance.HashKeySecret) == 0 {
		err = fmt.Errorf("Set Service Health Report Update To Data Store Stopped: %s", "Hash Key Secret Not Defined in Config")
		log.Println(err.Error())
		return false
	}

	if util.LenTrim(cfg.Target.Region) == 0 {
		err = fmt.Errorf("Set Service Health Report Update To Data Store Stopped: %s", "AWS Region Not Defined in Config")
		log.Println(err.Error())
		return false
	}

	if util.LenTrim(cfg.Namespace.Id) == 0 {
		err = fmt.Errorf("Set Service Health Report Update To Data Store Stopped: %s", "NamespaceID Not Defined in Config")
		log.Println(err.Error())
		return false
	}

	if util.LenTrim(cfg.Namespace.Name) == 0 {
		err = fmt.Errorf("Set Service Health Report Update To Data Store Stopped: %s", "NamespaceName Not Defined in Config")
		log.Println(err.Error())
		return false
	}

	if util.LenTrim(cfg.Service.Name) == 0 {
		err = fmt.Errorf("Set Service Health Report Update To Data Store Stopped: %s", "ServiceName Not Defined in Config")
		log.Println(err.Error())
		return false
	}

	if util.LenTrim(cfg.Service.Id) == 0 {
		msg := "Set Service Health Report Update To Data Store Skipped: " + "ServiceID Not Defined in Config"

		if seg != nil {
			_ = seg.SafeAddMetadata("Skipped-Reason", msg)
		}

		log.Println(msg)
		return true
	}

	if util.LenTrim(cfg.Instance.Id) == 0 {
		msg := "Set Service Health Report Update To Data Store Skipped: " + "InstanceID Not Defined in Config"

		if seg != nil {
			_ = seg.SafeAddMetadata("Skipped-Reason", msg)
		}

		log.Println(msg)
		return true
	}

	localAddr := s.LocalAddress()
	if util.LenTrim(localAddr) == 0 {
		msg := "Set Service Health Report Update To Data Store Skipped: " + "Service Host Info Not Ready"

		if seg != nil {
			_ = seg.SafeAddMetadata("Skipped-Reason", msg)
		}

		log.Println(msg)
		return true
	}

	hashSignature := crypto.Sha256(cfg.Namespace.Id+cfg.Service.Id+cfg.Instance.Id+util.FormatDate(time.Now().UTC()), cfg.Instance.HashKeySecret)

	data := &healthreport{
		NamespaceId:   cfg.Namespace.Id,
		ServiceId:     cfg.Service.Id,
		InstanceId:    cfg.Instance.Id,
		AwsRegion:     cfg.Target.Region,
		ServiceInfo:   strings.ToLower(cfg.Service.Name + "." + cfg.Namespace.Name),
		HostInfo:      localAddr,
		HashKeyName:   cfg.Instance.HashKeyName,
		HashSignature: hashSignature,
	}

	jsonData, e := util.MarshalJSONCompact(data)

	if e != nil {
		err = fmt.Errorf("Set Service Health Report Update To Data Store Failed: %s", e.Error())
		log.Println(err.Error())
		return false // stop the ticker loop — marshal failure is deterministic and will repeat
	}

	var subSeg *xray.XSegment

	if seg != nil {
		subSeg = seg.NewSubSegment("REST POST: " + cfg.Instance.HealthReportServiceUrl)
		_ = subSeg.Seg.AddMetadata("Post-Data", jsonData)
	}

	if statusCode, RespBody, e := rest.POST(cfg.Instance.HealthReportServiceUrl, []*rest.HeaderKeyValue{
		{
			Key:   "Content-Type",
			Value: "application/json",
		},
	}, jsonData); e != nil {
		err = fmt.Errorf("set service health report update to data store failed: (invoke REST POST '%s' error) %w", cfg.Instance.HealthReportServiceUrl, e)
		log.Println(err.Error())

	} else if statusCode != 200 {
		err = fmt.Errorf("set service health report update to data store failed: (invoke REST POST '%s' result status %d) %s", cfg.Instance.HealthReportServiceUrl, statusCode, RespBody)
		log.Println(err.Error())

	} else {
		log.Println("Set Service Health Report Update To Data Store OK")
	}

	if subSeg != nil {
		subSeg.Close()
	}

	return true
}

// ErrServiceAlreadyStarted is returned by Serve() when it is invoked
// more than once on the same Service instance. Serve is single-use by
// contract; after the first call (whether still running, returned
// normally, or returned with an error), subsequent calls are refused
// without side effects. Callers who need to re-start a service should
// construct a fresh Service instance. See SVC-F3 for the full
// rationale and the hazards of double-Serve (duplicate hook fires,
// competing signal handlers, leaked goroutines, double Cloud Map
// registration).
var ErrServiceAlreadyStarted = errors.New("connector/service: Service.Serve called more than once (Service is single-use; construct a new Service instance to re-start)")

// runHook invokes a user-provided lifecycle hook (BeforeServerStart,
// AfterServerStart, BeforeServerShutdown, AfterServerShutdown) with
// panic recovery so a buggy or unstable hook cannot crash the Service
// lifecycle. If hook is nil, runHook is a no-op.
//
// Fix: SVC-F2 (deep-review-2026-04-13-contrarian). Before this helper,
// panics in BeforeServerStart / AfterServerStart crashed the startup
// goroutine with zero diagnostic; panics in BeforeServerShutdown /
// AfterServerShutdown crashed Serve() itself mid-cleanup, preventing
// the function from returning cleanly and leaking the signal handler,
// quit-handler goroutine, and listener from the earlier phase.
//
// Recovery is intentional: lifecycle hooks are user-supplied callbacks
// whose failure must not bring down the Service. The recovered panic
// is logged with the hook name, the panic value, and the full stack
// trace so the issue is not silent. The hook name is hard-coded at
// each call site so the log message clearly identifies which hook
// misbehaved.
func (s *Service) runHook(name string, hook func(*Service)) {
	if hook == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("!!! %s hook panic recovered: %v\n%s !!!", name, r, debug.Stack())
		}
	}()
	hook(s)
}

// runRegisterHandlers invokes the caller-supplied
// RegisterServiceHandlers callback with panic recovery. Unlike runHook,
// it returns an error on panic so setupServer / Serve can fail cleanly
// (returning the wrapped panic as a normal Go error) instead of
// crashing the startup goroutine and leaking listener / Cloud Map state
// from earlier setup steps.
//
// Fix: SVC-F2 coverage extension (C2-003, deep-review-2026-04-14-contrarian).
// SVC-F2's original scope was the four named Before*/After* lifecycle
// hooks; RegisterServiceHandlers escaped that enumeration because of
// its constructor-shaped name, even though it is semantically the same
// thing — caller-supplied code that must not crash the Service.
//
// reg is the RegisterServiceHandlers callback. If reg is nil the
// helper returns nil without error (the caller's nil check at
// setupServer is the canonical place to fail on a nil registrar).
func (s *Service) runRegisterHandlers(reg func(*grpc.Server)) (err error) {
	if reg == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("!!! RegisterServiceHandlers panic recovered: %v\n%s !!!", r, debug.Stack())
			err = fmt.Errorf("RegisterServiceHandlers panic: %v", r)
		}
	}()
	reg(s._grpcServer)
	return nil
}

// runCleanup invokes a WebServerConfig.CleanUp callback with panic
// recovery. Mirrors runHook's log-and-continue contract: a panicking
// CleanUp is logged with full stack but does NOT propagate, so the
// surrounding teardown sequence (SD / SNS / SQS dereg, gs.Stop) keeps
// running and leaves the Service in a fully torn-down state instead
// of half-disconnected.
//
// Fix: SVC-F2 coverage extension (C2-003, deep-review-2026-04-14-contrarian)
// wrapped the two CleanUp call sites in the GracefulStop and
// ImmediateStop pre-Serve fallback paths. Pass-3 F2 extended the
// wrap to the in-Serve quit-handler call site as well — safeGo
// recovers panics to keep the PROCESS alive but does NOT preserve
// the per-step teardown invariants; a raw panic inside the quit
// handler unwinds through every remaining teardown step (set
// local address, deregister, transport close) and leaves the
// Service in a half-torn-down state even though the process
// stays up. runCleanup isolates the panic to the CleanUp call
// itself so the remaining teardown steps execute normally. nil
// cleanup is a no-op.
func runCleanup(name string, cleanup func()) {
	if cleanup == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("!!! %s cleanup panic recovered: %v\n%s !!!", name, r, debug.Stack())
		}
	}()
	cleanup()
}

// safeGo runs fn in a new goroutine with panic recovery. If fn panics,
// the panic is logged with its value and full stack trace, and the
// goroutine exits cleanly — the process keeps running.
//
// Fix: SVC-F6 (deep-review-2026-04-13-contrarian). grpc_recovery is a
// handler interceptor and covers ONLY RPC handler invocations; it does
// nothing for goroutines launched by service.go itself (startup
// orchestrator, gRPC Serve, web server, SD health reporter, quit
// handler, GracefulStop escalator, signal demux). Pre-fix, a panic
// in any of these crashed the entire process — a single AWS SDK panic
// in the SNS warmup goroutine would take the pod down. With safeGo,
// the panic is logged but the process survives long enough for the
// normal shutdown path to run or for another pod to take over.
//
// Use safeGo for every service-internal goroutine spawn. Intentional
// "background forever" goroutines (tickers, signal demuxes) exit on
// panic and log — per-iteration recovery for tickers is a separate
// refinement that can be layered on top when needed. The priority is
// "process stays alive" not "ticker stays alive".
func safeGo(name string, fn func()) {
	if fn == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("!!! safeGo panic recovered in %q: %v\n%s !!!", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// Serve runs the full service lifecycle and blocks until the gRPC
// server has shut down. The sequence is:
//
//  1. Read config from service.yaml.
//  2. Bind the gRPC listener and (optionally) the sidecar HTTP server.
//  3. Run BeforeServerStart hook.
//  4. Register with AWS Cloud Map (if Cloud Map is configured).
//  5. Start the gRPC server in a worker goroutine.
//  6. Run AfterServerStart hook.
//  7. Block on awaitOsSigExit waiting for SIGTERM/SIGINT, an internal
//     panic from the server goroutine, or an explicit GracefulStop /
//     ImmediateStop call.
//  8. Run BeforeServerShutdown hook.
//  9. Deregister from Cloud Map (one-shot atomic.Bool CAS-guarded —
//     see _deregFired).
//  10. grpc.Server.GracefulStop (or Stop, depending on shutdown path).
//  11. Run AfterServerShutdown hook.
//
// Returns the first error encountered along the way. A successful
// shutdown returns nil. Serve is single-use: calling Serve more than
// once on the same Service instance returns ErrServiceAlreadyStarted
// without side effects. The single-use contract is enforced atomically
// via a compare-and-swap on s._started, so concurrent Serve calls from
// two goroutines are safe: exactly one proceeds, the other receives
// the sentinel error (SVC-F3).
//
// Terminal-failure contract: any error returned by Serve — including
// readConfig / setupServer failures that occur BEFORE the gRPC server
// binds a port — puts this *Service into a terminal state. The CAS
// guard on s._started has already claimed the instance, so a second
// Serve() call on the same value returns ErrServiceAlreadyStarted
// even though no gRPC work ever ran. Consumers MUST construct a new
// *Service via NewService(...) to retry; they CANNOT reuse this one.
// The single-use CAS (SVC-F3) exists to protect one-shot resources
// (shutdown signal context, sync.Once-guarded hooks, Cloud Map
// registration ledger) from re-entry, and that protection is exactly
// what makes the instance unrecoverable after an early failure.
//
// All exported fields on Service must be set before calling Serve;
// post-Serve mutation is not synchronized.
func (s *Service) Serve() error {
	// SVC-F3: one-shot re-entry guard. CAS returns false if _started
	// was already true, meaning a prior (or concurrent) Serve call
	// already claimed this Service. Refuse without touching any
	// lifecycle state.
	if !s._started.CompareAndSwap(false, true) {
		return ErrServiceAlreadyStarted
	}

	// SVC-F5: allocate the opt-in shutdown-signal context BEFORE any
	// lifecycle step that could fire a phase cancel. When
	// ShutdownCancel is false (the default) this is a pure no-op,
	// preserving byte-identical behavior for every existing consumer.
	if s.ShutdownCancel {
		s._mu.Lock()
		s._shutdownCtx, s._shutdownCancel = context.WithCancel(context.Background())
		s._mu.Unlock()
	}

	// Safety net: guarantee ShutdownCtx.Done() fires before Serve
	// returns, regardless of phase. Idempotent — a prior phase fire
	// is a no-op here. No-op when capability is disabled.
	defer s.fireShutdownCancelFinal()

	s.setLocalAddress("")

	if err := s.readConfig(); err != nil {
		return err
	}

	lis, ip, port, err := s.setupServer()

	if err != nil {
		return err
	}

	log.Println("Service " + s._config.AppName + " Starting On " + s.LocalAddress() + "...")

	if err = s.connectSd(); err != nil {
		_ = lis.Close()
		return err
	}

	if err = s.autoCreateService(); err != nil {
		_ = lis.Close()
		return err
	}

	// SVC-F7: promote quit/quitDone to Service fields under _mu so
	// exported stop methods (GracefulStop / ImmediateStop) can route
	// through the same single-source-of-truth quit handler that the
	// signal path uses. Buffer 1 on _quit so a non-blocking send from
	// the exported path is idempotent; close-only on _quitDone so
	// every concurrent waiter unblocks.
	s._mu.Lock()
	s._quit = make(chan bool, 1)
	s._quitDone = make(chan struct{})
	quit := s._quit
	quitDone := s._quitDone
	s._mu.Unlock()

	if err = startServerFn(s, lis, quit, quitDone); err != nil {
		_ = lis.Close()
		// P1-CONN-SVC-02 (2026-04-15): startServer failed BEFORE the
		// quit-handler goroutine was installed. _quit and _quitDone are
		// non-nil (we allocated them above), but nothing will ever close
		// quitDone. A subsequent GracefulStop / ImmediateStop call would
		// read the non-nil fields under RLock and deadlock forever on
		// `<-quitDone`. Close quitDone ourselves and nil the Service
		// fields so later stop callers fall through to the pre-Serve
		// legacy safety-net branch (see ~L2749).
		close(quitDone)
		s._mu.Lock()
		s._quit = nil
		s._quitDone = nil
		s._mu.Unlock()
		return err
	}

	// FIX: If registerSd fails after startServer, the server goroutines are
	// already running. Signal quit to trigger cleanup (stop gRPC, close listener)
	// instead of leaking goroutines and the listener.
	if err = s.registerSd(ip, port); err != nil {
		// Non-blocking send: a concurrent exported stop may already
		// have signaled. The quit handler runs exactly once regardless.
		select {
		case quit <- true:
		default:
		}
		<-quitDone
		return err
	}

	s.awaitOsSigExit()

	if s.BeforeServerShutdown != nil {
		log.Println("Before gRPC Server Shutdown Begin...")
		s.runHook("BeforeServerShutdown", s.BeforeServerShutdown)
		log.Println("... Before gRPC Server Shutdown End")
	}

	// Non-blocking send: an exported stop call (GracefulStop /
	// ImmediateStop) may have raced ahead and already filled the
	// buffer. The quit handler is one-shot — it doesn't matter who
	// wins the send, only that the handler runs exactly once. The
	// drop branch here means "someone else already woke the handler;
	// just wait for it to finish".
	select {
	case quit <- true:
	default:
	}
	<-quitDone // wait for quit handler to complete full cleanup before proceeding

	if s.AfterServerShutdown != nil {
		log.Println("After gRPC Server Shutdown Begin...")
		s.runHook("AfterServerShutdown", s.AfterServerShutdown)
		log.Println("... After gRPC Server Shutdown End")
	}

	return nil
}

// autoCreateService internally creates cloud map service if not currently ready
func (s *Service) autoCreateService() error {
	if s._config != nil && s._sd != nil {
		if util.LenTrim(s._config.Service.Id) == 0 && util.LenTrim(s._config.Namespace.Id) > 0 {
			name := s._config.Service.Name

			if util.LenTrim(name) == 0 {
				name = s.AppName
			}

			if util.LenTrim(name) == 0 {
				name = s._config.Target.AppName
			}

			ttl := uint(300)

			if s._config.SvcCreateData.DnsTTL > 0 {
				ttl = s._config.SvcCreateData.DnsTTL
			}

			multivalue := true

			if strings.ToLower(s._config.SvcCreateData.DnsRouting) == "weighted" {
				multivalue = false
			}

			srv := true

			if strings.ToLower(s._config.SvcCreateData.DnsType) == "a" {
				srv = false
			}

			dnsConf := &cloudmap.DnsConf{
				TTL:        int64(ttl),
				MultiValue: multivalue,
				SRV:        srv,
			}

			if util.LenTrim(s._config.SvcCreateData.DnsRouting) == 0 {
				dnsConf = nil
			}

			failThreshold := s._config.SvcCreateData.HealthFailThreshold

			if failThreshold == 0 {
				failThreshold = 1
			}

			healthType := sdhealthchecktype.UNKNOWN
			healthPath := ""

			switch strings.ToLower(s._config.SvcCreateData.HealthPubDnsType) {
			case "http":
				if !s._config.SvcCreateData.HealthCustom {
					healthType = sdhealthchecktype.HTTP
					healthPath = s._config.SvcCreateData.HealthPubDnsPath

					if util.LenTrim(healthPath) == 0 {
						return fmt.Errorf("Public DNS Http Health Check Requires Resource Path Endpoint")
					}
				}
			case "https":
				if !s._config.SvcCreateData.HealthCustom {
					healthType = sdhealthchecktype.HTTPS
					healthPath = s._config.SvcCreateData.HealthPubDnsPath

					if util.LenTrim(healthPath) == 0 {
						return fmt.Errorf("Public DNS Https Health Check Requires Resource Path Endpoint")
					}
				}
			case "tcp":
				if !s._config.SvcCreateData.HealthCustom {
					healthType = sdhealthchecktype.TCP
				}
			}

			healthConf := &cloudmap.HealthCheckConf{
				Custom:                  s._config.SvcCreateData.HealthCustom,
				FailureThreshold:        int64(failThreshold),
				PubDns_HealthCheck_Type: healthType,
				PubDns_HealthCheck_Path: healthPath,
			}

			var timeoutDuration []time.Duration

			if s._config.Instance.SdTimeout > 0 {
				timeoutDuration = append(timeoutDuration, time.Duration(s._config.Instance.SdTimeout)*time.Second)
			}

			if svcId, err := registry.CreateService(s._sd,
				name,
				s._config.Namespace.Id,
				dnsConf,
				healthConf,
				"", timeoutDuration...); err != nil {
				buf := err.Error()

				if strings.Contains(strings.ToLower(buf), "service already exists") {
					buf = util.SplitString(strings.ToLower(buf), `serviceid: "`, -1)
					buf = util.Trim(util.SplitString(buf, `"`, 0))

					if len(buf) > 0 {
						svcId = buf
						log.Println("Auto Create Service OK: (Found Existing SvcID) " + svcId + " - " + name)

						s._config.SetServiceId(svcId)
						s._config.SetServiceName(name)

						if e := s._config.Save(); e != nil {
							return e
						} else {
							return nil
						}
					} else {
						log.Println("Auto Create Service Failed: " + err.Error())
						return err
					}
				} else {
					log.Println("Auto Create Service Failed: " + err.Error())
					return err
				}
			} else {
				log.Println("Auto Create Service OK: " + svcId + " - " + name)

				s._config.SetServiceId(svcId)
				s._config.SetServiceName(name)

				if e := s._config.Save(); e != nil {
					return e
				} else {
					return nil
				}
			}
		} else {
			return nil
		}
	} else {
		return nil
	}
}

// registerInstance will call cloud map to register service instance.
// FIX #10: Added sdoperationstatus.Fail check to return immediately on permanent failure.
// FIX #11: Fixed log message from "(100ms)" to "(250ms)" to match actual sleep duration.
func (s *Service) registerInstance(ip string, port uint, healthy bool, version string) error {
	s._mu.RLock()
	sd := s._sd
	cfg := s._config
	s._mu.RUnlock()

	if sd == nil || cfg == nil || len(cfg.Service.Id) == 0 {
		return nil
	}

	var timeoutDuration []time.Duration
	if cfg.Instance.SdTimeout > 0 {
		timeoutDuration = append(timeoutDuration, time.Duration(cfg.Instance.SdTimeout)*time.Second)
	}

	if cfg.Instance.AutoDeregisterPrior {
		_ = s.deregisterInstance()
	}

	if instanceId, operationId, err := registry.RegisterInstance(sd, cfg.Service.Id, cfg.Instance.Prefix, ip, port, healthy, version, timeoutDuration...); err != nil {
		log.Println("Auto Register Instance Failed: " + err.Error())
		return err
	} else {
		tryCount := 0

		log.Println("Auto Register Instance Initiated... " + instanceId)

		time.Sleep(250 * time.Millisecond)

		for {
			if status, e := registry.GetOperationStatus(sd, operationId, timeoutDuration...); e != nil {
				log.Println("... Auto Register Instance Failed: " + e.Error())
				return e
			} else {
				if status == sdoperationstatus.Success {
					log.Println("... Auto Register Instance OK: " + instanceId)

					cfg.SetInstanceId(instanceId)

					if e2 := cfg.Save(); e2 != nil {
						log.Println("... Update Config with Registered Instance Failed: " + e2.Error())
						return fmt.Errorf("Register Instance Fail When Save Config Errored: %s", e2.Error())
					} else {
						log.Println("... Update Config with Registered Instance OK")
						return nil
					}
				} else if status == sdoperationstatus.Fail {
					// FIX #10: Permanent failure — do not retry
					log.Println("... Auto Register Instance Failed: Operation returned Fail status")
					return fmt.Errorf("Register Instance Failed: Operation returned permanent Fail status")
				} else {
					if tryCount < 20 {
						tryCount++
						// FIX #11: Log message said "(100ms)" but actual sleep is 250ms
						log.Println("... Checking Register Instance Completion Status, Attempt " + strconv.Itoa(tryCount) + " (250ms)")
						time.Sleep(250 * time.Millisecond)
					} else {
						log.Println("... Auto Register Instance Failed: Operation Timeout After 5 Seconds")
						return fmt.Errorf("Register Instance Fail When Operation Timed Out After 5 Seconds")
					}
				}
			}
		}
	}
}

// updateHealth will update instance health
func (s *Service) updateHealth(healthy bool) error {
	s._mu.RLock()
	sd := s._sd
	cfg := s._config
	s._mu.RUnlock()

	if sd == nil || cfg == nil || util.LenTrim(cfg.Service.Id) == 0 || util.LenTrim(cfg.Instance.Id) == 0 {
		return nil
	}

	var timeoutDuration []time.Duration
	if cfg.Instance.SdTimeout > 0 {
		timeoutDuration = append(timeoutDuration, time.Duration(cfg.Instance.SdTimeout)*time.Second)
	}

	return registry.UpdateHealthStatus(sd, cfg.Instance.Id, cfg.Service.Id, healthy, timeoutDuration...)
}

// deregisterInstance will remove instance from cloudmap and route 53.
// FIX #12: Added sdoperationstatus.Fail check to return immediately on permanent failure.
//
// SVC-F4: Guarded by _deregFired (atomic.Bool one-shot CAS) so
// concurrent shutdown paths (GracefulStop/ImmediateStop, the internal
// quit-handler goroutine, the Serve() defer) cannot issue duplicate
// CloudMap DeregisterInstance requests. Unlike the prior sync.Once
// approach, later callers DO NOT block behind the first caller's
// AWS poll loop — they CAS-fail and return nil immediately. This
// preserves ImmediateStop's break-glass semantics: an operator
// hitting the kill switch while the signal path is mid-dereg falls
// through to gs.Stop() without waiting ~5s (or worse) on AWS.
//
// Returned error semantics: only the winning caller observes the
// real deregister result; every other caller gets nil. All call
// sites use the error for logging only (verified), so this is a
// safe trade-off. The AWS DeregisterInstance API is idempotent, so
// external correctness is unaffected.
func (s *Service) deregisterInstance() error {
	if !s._deregFired.CompareAndSwap(false, true) {
		// Another goroutine already claimed the deregister. Return
		// without blocking — this is the core SVC-F4 fix.
		return nil
	}
	return s.doDeregisterInstance()
}

// doDeregisterInstance performs the actual CloudMap deregister. It is
// only called through the _deregFired CAS guard in deregisterInstance,
// so it is guaranteed to run at most once per Service lifetime.
func (s *Service) doDeregisterInstance() error {
	s._mu.RLock()
	sd := s._sd
	cfg := s._config
	s._mu.RUnlock()

	if sd == nil || cfg == nil || util.LenTrim(cfg.Service.Id) == 0 || util.LenTrim(cfg.Instance.Id) == 0 {
		return nil
	}

	log.Println("De-Register Instance Begin...")

	var timeoutDuration []time.Duration
	if cfg.Instance.SdTimeout > 0 {
		timeoutDuration = append(timeoutDuration, time.Duration(cfg.Instance.SdTimeout)*time.Second)
	}

	if operationId, err := registry.DeregisterInstance(sd, cfg.Instance.Id, cfg.Service.Id, timeoutDuration...); err != nil {
		log.Println("... De-Register Instance Failed: " + err.Error())
		return fmt.Errorf("De-Register Instance Fail: %w", err)
	} else {
		tryCount := 0

		time.Sleep(250 * time.Millisecond)

		for {
			if status, e := registry.GetOperationStatus(sd, operationId, timeoutDuration...); e != nil {
				log.Println("... De-Register Instance Failed: " + e.Error())
				return fmt.Errorf("De-Register Instance Fail: %s", e.Error())
			} else {
				if status == sdoperationstatus.Success {
					log.Println("... De-Register Instance OK")

					cfg.SetInstanceId("")

					if e2 := cfg.Save(); e2 != nil {
						log.Println("... Update Config with De-Registered Instance Failed: " + e2.Error())
						return fmt.Errorf("De-Register Instance Fail When Save Config Errored: %s", e2.Error())
					} else {
						log.Println("... Update Config with De-Registered Instance OK")
						return nil
					}
				} else if status == sdoperationstatus.Fail {
					// FIX #12: Permanent failure — do not retry
					log.Println("... De-Register Instance Failed: Operation returned Fail status")
					return fmt.Errorf("De-Register Instance Failed: Operation returned permanent Fail status")
				} else {
					if tryCount < 20 {
						tryCount++
						log.Println("... Checking De-Register Instance Completion Status, Attempt " + strconv.Itoa(tryCount) + " (250ms)")
						time.Sleep(250 * time.Millisecond)
					} else {
						log.Println("... De-Register Instance Failed: Operation Timeout After 5 Seconds")
						return fmt.Errorf("De-register Instance Fail When Operation Timed Out After 5 Seconds")
					}
				}
			}
		}
	}
}

// unsubscribe all existing sns subscriptions if any
func (s *Service) unsubscribeSNS() {
	s._mu.RLock()
	snsClient := s._sns
	cfg := s._config
	s._mu.RUnlock()

	if snsClient == nil || cfg == nil {
		return
	}

	log.Println("Notification/Queue Services Unsubscribe Begin...")

	doSave := false

	if cfg.Service.DiscoveryUseSqsSns {
		if util.LenTrim(cfg.Topics.SnsDiscoverySubscriptionArn) > 0 {
			if err := notification.Unsubscribe(snsClient, cfg.Topics.SnsDiscoverySubscriptionArn, time.Duration(cfg.Instance.SdTimeout)*time.Second); err != nil {
				log.Println("!!! Unsubscribe Discovery Subscription Failed: " + err.Error() + " !!!")
			} else {
				log.Println("... Unsubscribe Discovery Subscription OK")
				cfg.SetSnsDiscoverySubscriptionArn("")
				doSave = true
			}
		} else {
			log.Println("--- No Discovery Subscription to Unsubscribe ---")
		}
	} else {
		log.Println("--- Discovery Push Notification is Disabled ---")
	}

	log.Println("... Notification/Queue Services Unsubscribe End")

	if doSave {
		if err := cfg.Save(); err != nil {
			log.Println("!!! Persist Unsubscribed Info To Config Failed: " + err.Error() + " !!!")
		}
	}
}

// GracefulStop initiates an orderly shutdown of the Service.
//
// Behavior depends on whether Serve() has been called yet:
//
//   - In the normal "Serve is running" case, GracefulStop signals
//     the unified quit handler (the same one the OS-signal path
//     wakes) and BLOCKS until that handler completes the full
//     teardown: BeforeServerShutdown is NOT re-invoked from this
//     path (Serve owns BeforeServerShutdown), but the quit handler
//     publishes the offline notification, unsubscribes from SNS,
//     deletes the health report, runs WebServerConfig.CleanUp,
//     deregisters from Cloud Map, fires the SVC-F5 PreDrain /
//     PostGraceExpiry phases, and runs the bounded
//     grpc.Server.GracefulStop -> Stop escalation. By the time
//     GracefulStop returns, the gRPC server has fully stopped and
//     Serve()'s blocking awaitOsSigExit + quit-wait have unblocked
//     (Serve will run AfterServerShutdown next and then return nil).
//
//     Serve()'s awaitOsSigExit only wakes on an OS signal delivered
//     via signal.Notify, so this method first sends SIGTERM to its
//     own pid (SVC-F8) to unblock the sig-demux goroutine; the Go
//     runtime routes that signal to awaitOsSigExit's local channel
//     rather than the process default handler, so the process is
//     NOT terminated by the signal itself. This mirrors the
//     self-SIGTERM pattern already used on the grpc-server error
//     path (FIX #13, service.go:1138-1149).
//
//   - In the rare pre-Serve case (the Service was constructed but
//     Serve was never called), GracefulStop falls through to a
//     legacy idempotent teardown body that publishes the offline
//     notification, runs CleanUp, deregisters, and calls
//     grpc.Server.GracefulStop directly. This path is provided as
//     a safety net for callers that perform partial setup outside
//     Serve. Most callers will never hit it.
//
// GracefulStop is safe to call concurrently with the internal
// signal-driven shutdown path and with ImmediateStop. The unified
// quit channel (SVC-F7) routes both paths into one quit-handler
// invocation; the buffer-1 send on _quit is idempotent (a second
// concurrent caller's send drops via select default but the caller
// still blocks on _quitDone alongside the winner). The Cloud Map
// deregister step inside the quit handler is additionally guarded by
// a one-shot atomic.Bool CAS (_deregFired, see SVC-F4) so concurrent
// programmatic + signal callers cannot double-fire the AWS API.
//
// Re-entry from a gRPC handler: calling GracefulStop from inside a
// gRPC unary/stream handler (e.g. an admin RPC that triggers
// shutdown) STALLS for up to GracefulStopTimeout (default 30s) and
// then returns. The reason is that the quit handler's teardown
// invokes grpc.Server.GracefulStop, which blocks on ALL in-flight
// RPCs — including the handler that called this method, which is
// parked inside GracefulStop waiting on _quitDone. The bounded
// drain helper escalates to grpc.Server.Stop after
// GracefulStopTimeout, severing the transport; the handler's
// response is not delivered and its client observes an
// Unavailable / Canceled error.
//
// If you must trigger shutdown from a gRPC handler, spawn a
// goroutine so the handler can return normally before teardown
// begins:
//
//	go s.GracefulStop()
//	return &AdminResponse{Ok: true}, nil
//
// GracefulStop is safe to call from a goroutine — the unified quit
// channel (SVC-F7) serializes all shutdown callers.
//
// SVC-F5 / rule #13: this routing makes the ShutdownCtx() promise
// (every fire-site phase runs before Serve returns) hold on the
// programmatic-stop path, not just the signal path. A consumer that
// opted in to ShutdownCancel=true and triggers shutdown via
// GracefulStop() will see the configured phase fire before this
// method returns (and the safety-net Final fire from the Serve()
// defer fires before Serve unblocks).
//
// Fix: SVC-F7 (C2-001 + C2-002) established the unified quit
// routing. SVC-F8 (F1, deep-review-2026-04-14-contrarian-pass3)
// added the self-SIGTERM so the unified routing actually wakes
// Serve()'s awaitOsSigExit. Prior to SVC-F8 the unified routing
// completed teardown correctly but Serve()'s main goroutine stayed
// parked in awaitOsSigExit until a real OS signal arrived — a
// zombie-process hazard on programmatic-only stop paths.
func (s *Service) GracefulStop() {
	// SVC-F7: take the unified routing path if Serve has allocated
	// the quit primitives; otherwise fall through to the legacy
	// pre-Serve teardown.
	s._mu.RLock()
	quit := s._quit
	quitDone := s._quitDone
	s._mu.RUnlock()
	if quit != nil && quitDone != nil {
		log.Println("GracefulStop invoked — routing through unified quit handler")
		// SVC-F8 (F1 fix): wake Serve()'s awaitOsSigExit so the
		// main Serve goroutine returns after the unified quit
		// handler completes teardown. awaitOsSigExit reads from a
		// function-local done channel whose only writer is a
		// sig-demux goroutine consuming from signal.Notify; the
		// only way to unblock it is to deliver a registered signal
		// to our own pid. signal.Notify routes the signal to the
		// Go channel rather than the process default handler, so
		// this self-signal does NOT terminate the process — it
		// just releases the awaitOsSigExit wait.
		//
		// Placement matters: send BEFORE the quit-send so the
		// unified quit handler and awaitOsSigExit both get a chance
		// to complete in parallel. The quit handler drives the
		// teardown (Cloud Map deregister, gRPC stop, etc.) while
		// awaitOsSigExit returns and lets Serve() fall through to
		// its post-teardown hooks (AfterServerShutdown).
		//
		// Readiness gate (_sigHandlerReady): only self-signal if
		// awaitOsSigExit has actually registered its signal.Notify
		// handler. Before that point (or after it has called
		// signal.Stop at the end of Serve), SIGTERM would hit the
		// Go runtime default handler and TERMINATE the process.
		// Unit tests that manually allocate the quit primitives
		// without calling Serve() hit this branch — no handler is
		// registered, so we skip the self-signal; the unified quit
		// handler still runs correctly for those callers since
		// they are not actually parked in awaitOsSigExit.
		if s._sigHandlerReady.Load() {
			if p, findErr := os.FindProcess(os.Getpid()); findErr == nil {
				_ = p.Signal(syscall.SIGTERM)
			}
		}
		// Non-blocking send: another path (signal demux / a prior
		// concurrent stop call) may have already filled the buffer.
		// In either case the quit handler runs exactly once and
		// closes _quitDone when teardown completes, so blocking
		// here on _quitDone is the source-of-truth wait.
		select {
		case quit <- true:
		default:
		}
		<-quitDone
		return
	}

	// ---- Legacy pre-Serve safety net -----------------------------
	// Serve was never called (or quit primitives were never
	// allocated). Run the original idempotent teardown body so
	// callers that performed partial setup outside Serve can still
	// release whatever resources are live.
	s._mu.RLock()
	cfg := s._config
	s._mu.RUnlock()

	if cfg != nil {
		s.publishToSNS(cfg.Topics.SnsDiscoveryTopicArn, "Discovery Push Notification", s.getHostDiscoveryMessage(false), nil)
	}

	s.unsubscribeSNS()

	if cfg != nil {
		_ = s.deleteServiceHealthReportFromDataStore(cfg.Instance.Id)
	}

	// SVC-F2 coverage extension (C2-003): user-supplied CleanUp must
	// not crash the rest of teardown.
	if s.WebServerConfig != nil {
		runCleanup("WebServerConfig.CleanUp(GracefulStop)", s.WebServerConfig.CleanUp)
	}

	s.setLocalAddress("")

	log.Println("Stopping gRPC Server (Graceful, pre-Serve fallback)")

	s._mu.RLock()
	hasSd := s._sd != nil
	s._mu.RUnlock()
	if hasSd {
		if err := s.deregisterInstance(); err != nil {
			log.Println("De-Register Instance Failed From GracefulStop: " + err.Error())
		} else {
			log.Println("De-Register Instance OK From GracefulStop")
		}
	}

	// Copy and nil out shared fields under lock to prevent races with quit handler goroutine
	s._mu.Lock()
	sd := s._sd
	s._sd = nil
	sqsC := s._sqs
	s._sqs = nil
	snsC := s._sns
	s._sns = nil
	gs := s._grpcServer
	s._grpcServer = nil
	s._mu.Unlock()

	if sd != nil {
		sd.Disconnect()
	}
	if sqsC != nil {
		sqsC.Disconnect()
	}
	if snsC != nil {
		snsC.Disconnect()
	}
	if gs != nil {
		gs.GracefulStop()
	}
}

// ImmediateStop forcibly tears down the Service without waiting for
// in-flight RPCs to complete.
//
// Behavior depends on whether Serve() has been called yet:
//
//   - In the normal "Serve is running" case, ImmediateStop sets the
//     internal "_immediateStopRequested" atomic, signals the unified
//     quit handler, and BLOCKS until the handler completes teardown.
//     The quit handler observes _immediateStopRequested and bypasses
//     the bounded grpc.GracefulStop drain entirely, calling
//     grpc.Server.Stop directly. The SVC-F5 PostGraceExpiry hook
//     still fires synchronously before gs.Stop, so handlers
//     observing ShutdownCtx see the cancel happens-before the
//     transport is severed.
//
//     As with GracefulStop, ImmediateStop sends SIGTERM to its own
//     pid (SVC-F8) so Serve()'s awaitOsSigExit releases; the Go
//     runtime routes the signal to the signal.Notify channel
//     rather than the process default handler, so this self-signal
//     does NOT terminate the process.
//
//   - In the rare pre-Serve case, ImmediateStop falls through to a
//     legacy idempotent teardown body that ends with
//     grpc.Server.Stop directly. As with GracefulStop, this is a
//     safety net for callers that performed partial setup outside
//     Serve.
//
// Use this for hard shutdown paths (panic recovery, fatal config
// error, shutdown deadline exceeded). Prefer GracefulStop in the
// normal case.
//
// Like GracefulStop, this method is safe to invoke concurrently with
// the signal-driven shutdown path; the unified quit channel (SVC-F7)
// guarantees the quit handler runs exactly once regardless of how
// many call sites raced. ImmediateStop's "immediate" semantics are
// PRESERVED under concurrent shutdown: setting _immediateStopRequested
// before the quit handler reads it forces the handler down the
// gs.Stop()-direct path even if a concurrent GracefulStop call won
// the buffer-1 send. The Cloud Map deregister inside the quit
// handler is guarded by a one-shot atomic.Bool CAS (_deregFired,
// see SVC-F4) so concurrent callers cannot stall on each other's
// AWS poll loop.
//
// Re-entry from a gRPC handler: calling ImmediateStop from inside a
// gRPC handler does NOT stall 30s the way GracefulStop does — the
// quit handler observes _immediateStopRequested and skips the
// bounded grpc.GracefulStop drain entirely, calling gs.Stop()
// directly, which severs the transport without waiting for
// in-flight RPCs. The calling handler's response is not delivered
// and its client observes an Unavailable / Canceled error
// immediately. If the handler must return a response to its
// client before shutdown severs the transport, spawn a goroutine:
//
//	go s.ImmediateStop()
//	return &AdminResponse{Ok: true}, nil
//
// ImmediateStop is safe to call from a goroutine — the unified
// quit channel (SVC-F7) serializes all shutdown callers and
// _immediateStopRequested is a one-way latch.
//
// SVC-F5 / rule #13: this routing makes the ShutdownCtx() promise
// hold on the programmatic ImmediateStop path — the configured fire
// site runs before this method returns even when shutdown is
// triggered without an OS signal. This promise applies ONLY to the
// normal "Serve is running" case. In the pre-Serve fallback no
// ShutdownCancelPhase fire runs, because Serve() is the only site
// that allocates _shutdownCtx; consumers observing ShutdownCtx()
// before Serve() is entered will see a nil context regardless of
// ShutdownCancelPhase, which is the expected contract for any
// consumer opting in via ShutdownCancel=true.
//
// Fix: SVC-F7 (C2-001 + C2-002) established the unified quit
// routing. SVC-F8 (F1, deep-review-2026-04-14-contrarian-pass3)
// added the self-SIGTERM so the unified routing actually wakes
// Serve()'s awaitOsSigExit. Godoc precision: C2-007 (pass-2), F3
// re-entry contract (pass-3).
func (s *Service) ImmediateStop() {
	// SVC-F7: declare the immediate intent BEFORE signaling the quit
	// channel. The quit handler reads this atomic between waking and
	// invoking the gRPC stop step, so setting it first guarantees
	// the immediate semantics regardless of concurrent send races.
	s._immediateStopRequested.Store(true)

	s._mu.RLock()
	quit := s._quit
	quitDone := s._quitDone
	s._mu.RUnlock()
	if quit != nil && quitDone != nil {
		log.Println("ImmediateStop invoked — routing through unified quit handler")
		// SVC-F8 (F1 fix): wake Serve()'s awaitOsSigExit. See
		// GracefulStop for the full rationale — same mechanism,
		// same safety (signal.Notify routes to Go channel), same
		// readiness gate (_sigHandlerReady) so unit tests that
		// allocate the quit primitives without running Serve do
		// not terminate the test process.
		if s._sigHandlerReady.Load() {
			if p, findErr := os.FindProcess(os.Getpid()); findErr == nil {
				_ = p.Signal(syscall.SIGTERM)
			}
		}
		select {
		case quit <- true:
		default:
		}
		<-quitDone
		return
	}

	// ---- Legacy pre-Serve safety net -----------------------------
	s._mu.RLock()
	cfg := s._config
	s._mu.RUnlock()

	if cfg != nil {
		s.publishToSNS(cfg.Topics.SnsDiscoveryTopicArn, "Discovery Push Notification", s.getHostDiscoveryMessage(false), nil)
	}

	s.unsubscribeSNS()

	if cfg != nil {
		_ = s.deleteServiceHealthReportFromDataStore(cfg.Instance.Id)
	}

	// SVC-F2 coverage extension (C2-003): user-supplied CleanUp must
	// not crash the rest of teardown.
	if s.WebServerConfig != nil {
		runCleanup("WebServerConfig.CleanUp(ImmediateStop)", s.WebServerConfig.CleanUp)
	}

	s.setLocalAddress("")

	log.Println("Stopping gRPC Server (Immediate, pre-Serve fallback)")

	s._mu.RLock()
	hasSd := s._sd != nil
	s._mu.RUnlock()
	if hasSd {
		if err := s.deregisterInstance(); err != nil {
			log.Println("De-Register Instance Failed From ImmediateStop: " + err.Error())
		} else {
			log.Println("De-Register Instance OK From ImmediateStop")
		}
	}

	// Copy and nil out shared fields under lock to prevent races with quit handler goroutine
	s._mu.Lock()
	sd := s._sd
	s._sd = nil
	sqsC := s._sqs
	s._sqs = nil
	snsC := s._sns
	s._sns = nil
	gs := s._grpcServer
	s._grpcServer = nil
	s._mu.Unlock()

	if sd != nil {
		sd.Disconnect()
	}
	if sqsC != nil {
		sqsC.Disconnect()
	}
	if snsC != nil {
		snsC.Disconnect()
	}
	if gs != nil {
		gs.Stop()
	}
}

// LocalAddress returns the service server's address and port
func (s *Service) LocalAddress() string {
	if s == nil {
		return ""
	}
	s._mu.RLock()
	defer s._mu.RUnlock()
	return s._localAddress
}

// setLocalAddress sets the local address in a thread-safe manner
func (s *Service) setLocalAddress(addr string) {
	s._mu.Lock()
	defer s._mu.Unlock()
	s._localAddress = addr
}

// =====================================================================================================================
// HTTP WEB SERVER
// =====================================================================================================================

type WebServerConfig struct {
	AppName          string
	ConfigFileName   string
	CustomConfigPath string

	// define web server router info
	WebServerRoutes map[string]*ginw.RouteDefinition

	// getter only
	WebServerLocalAddress string

	// clean up func
	CleanUp func()
}

// GetWebServerLocalAddress returns FQDN url to the local web server
func (w *WebServerConfig) GetWebServerLocalAddress() string {
	return w.WebServerLocalAddress
}

func (s *Service) startWebServer() error {
	if s.WebServerConfig == nil {
		return fmt.Errorf("Start Web Server Failed: Web Server Config Not Setup")
	}

	if util.LenTrim(s.WebServerConfig.AppName) == 0 {
		return fmt.Errorf("Start Web Server Failed: Web Server Config App Name Not Set")
	}

	if util.LenTrim(s.WebServerConfig.ConfigFileName) == 0 {
		return fmt.Errorf("Start Web Server Failed: Web Server Config Custom File Name Not Set")
	}

	if s.WebServerConfig.WebServerRoutes == nil {
		return fmt.Errorf("Start Web Server Failed: Web Server Routes Not Defined (Map Nil)")
	}

	if len(s.WebServerConfig.WebServerRoutes) == 0 {
		return fmt.Errorf("Start Web Server Failed: Web Server Routes Not Set (Count Zero)")
	}

	server, err := ws.NewWebServer(s.WebServerConfig.AppName, s.WebServerConfig.ConfigFileName, s.WebServerConfig.CustomConfigPath)
	if err != nil {
		return fmt.Errorf("Start Web Server Failed: %s", err)
	}

	server.Routes = s.WebServerConfig.WebServerRoutes

	httpVerb := ""

	if server.UseTls() {
		httpVerb = "https"
	} else {
		httpVerb = "http"
	}

	s.WebServerConfig.WebServerLocalAddress = fmt.Sprintf("%s://%s:%d", httpVerb, server.GetHostAddress(), server.Port())
	s.WebServerConfig.CleanUp = func() {
		server.RemoveDNSRecordset()
	}
	log.Println("Web Server Host Starting On: " + s.WebServerConfig.WebServerLocalAddress)

	if err := server.Serve(); err != nil {
		server.RemoveDNSRecordset()
		return fmt.Errorf("Start Web Server Failed: (Serve Error) %s", err)
	}

	return nil
}
