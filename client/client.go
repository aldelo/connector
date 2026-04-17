package client

/*
 * Copyright 2020-2026 Aldelo, LP
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
	"net/http"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/rest"
	"github.com/aldelo/common/tlsconfig"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/cloudmap"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/sqs"
	"github.com/aldelo/common/wrapper/xray"
	data "github.com/aldelo/common/wrapper/zap"
	"github.com/aldelo/connector/adapters/circuitbreaker"
	"github.com/aldelo/connector/internal/safego"
	"github.com/aldelo/connector/adapters/circuitbreaker/plugins"
	"github.com/aldelo/connector/adapters/health"
	"github.com/aldelo/connector/adapters/loadbalancer"
	"github.com/aldelo/connector/adapters/queue"
	"github.com/aldelo/connector/adapters/registry"
	res "github.com/aldelo/connector/adapters/resolver"
	"github.com/aldelo/connector/adapters/tracer"
	ws "github.com/aldelo/connector/webserver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"
)

// Issue #16: Define constants for magic numbers
const (
	defaultCacheExpireSeconds     = 300
	defaultDialTimeoutSeconds     = 5
	defaultHealthCheckInterval    = 250 * time.Millisecond
	defaultWebServerCheckInterval = 500 * time.Millisecond
	defaultNotifierReconnectDelay = 5 * time.Second
	maxTCPPort                    = 65535
	defaultMaxDiscoveryResults    = 100
	connectionCloseDelay          = 100 * time.Millisecond
	// P2-13: max time Dial() will wait for a stale notifier reconnect
	// goroutine from a prior lifecycle to drain. Should be longer than
	// a single DoNotifierAlertService attempt (which has its own SDK
	// timeout). 5s matches defaultNotifierReconnectDelay.
	notifierReconnectDrainTimeout   = 5 * time.Second
	webServerShutdownTimeout    = 5 * time.Second
	webServerStopChannelTimeout = 6 * time.Second
)

// client side cache
var (
	_cache  *Cache
	cacheMu sync.RWMutex
)

func init() {
	cacheMu.Lock()
	_cache = new(Cache)
	cacheMu.Unlock()
}

func ClearEndpointCache() {
	cacheMu.Lock()
	_cache = new(Cache)
	cacheMu.Unlock()
}

// cache helper shims to serialize access and avoid races
func cacheDisableLogging(disable bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if _cache != nil {
		// FIX: Acquire _mu as well, since Cache methods read DisableLogging under _mu
		_cache._mu.Lock()
		_cache.DisableLogging = disable
		_cache._mu.Unlock()
	}
}

func cacheAddServiceEndpoints(key string, eps []*serviceEndpoint) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if _cache != nil {
		_cache.AddServiceEndpoints(key, eps)
	}
}

func cacheGetLiveServiceEndpoints(key, version string, force ...bool) []*serviceEndpoint {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	if _cache == nil {
		return []*serviceEndpoint{}
	}

	eps := _cache.GetLiveServiceEndpoints(key, version, force...)
	if len(eps) == 0 {
		return []*serviceEndpoint{}
	}

	// Issue #1: Perform deep copy of endpoint structs to prevent concurrent modification
	out := make([]*serviceEndpoint, len(eps))
	for i, ep := range eps {
		if ep == nil {
			continue
		}
		copied := *ep
		out[i] = &copied
	}

	return out
}

func cachePurgeServiceEndpointByHostAndPort(key, host string, port uint) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if _cache != nil {
		_cache.PurgeServiceEndpointByHostAndPort(key, host, port)
	}
}

// sets connection state under lock
func (c *Client) setConnection(conn *grpc.ClientConn, remote string) {
	if c == nil {
		return
	}

	// A3-F3: refuse to bind a new connection when the client is already
	// closed. This read of c.closed is outside connMu — that is safe
	// because setConnection is only reachable from Dial, which is
	// serialized by _lifecycleMu. No concurrent writer can flip closed
	// between this check and the connMu.Lock below without first
	// acquiring _lifecycleMu (which Close() also acquires).
	if c.closed.Load() {
		if conn != nil {
			_ = conn.Close() // avoid leak and zombie connection
		}
		return
	}

	c.connMu.Lock()
	old := c._conn
	c._conn = conn
	c._remoteAddress = remote

	if conn != nil {
		// capture and handle possible health client init error to avoid nil deref later
		hc, err := health.NewHealthClient(conn)
		if err != nil {
			c._healthManualChecker = nil
			if z := c.ZLog(); z != nil {
				z.Errorf("Init health client failed: %v", err)
			} else {
				log.Printf("Init health client failed: %v", err)
			}
		} else {
			c._healthManualChecker = hc
		}
	} else {
		c._healthManualChecker = nil
	}
	c.connMu.Unlock()

	// Issue #9: Close prior connection asynchronously with a small delay to allow in-flight RPCs to complete
	if old != nil && old != conn {
		// P2-14: prefer caller-configured drain window; fall back to default
		closeDelay := c.ConnectionCloseDelay
		if closeDelay <= 0 {
			closeDelay = connectionCloseDelay
		}
		// CL-F5: wrap in safeGo so a panic in grpc.ClientConn.Close
		// (e.g., from a race with a pending in-flight RPC, or a
		// buggy resolver) cannot tear down the whole process.
		oldConn := old
		logger := c.ZLog()
		d := closeDelay
		safego.Go("close-old-conn", func() {
			time.Sleep(d) // Allow in-flight RPCs to complete
			if err := oldConn.Close(); err != nil {
				if logger != nil {
					logger.Warnf("Failed to close old connection: %v", err)
				} else {
					log.Printf("Failed to close old connection: %v", err)
				}
			}
		})
	}
}

// closeOnClosed is a small watchdog goroutine body used to bridge a
// Client shutdown signal into an in-flight blocking call that the Client
// cannot otherwise cancel directly. It selects on closedCh (normally
// c.getClosedCh()) and doneCh (a per-call local channel). When closedCh
// fires first, cancelFn is invoked exactly once; when doneCh fires first
// (the blocking call has returned on its own), it exits without calling
// cancelFn. Either side exiting unblocks the other.
//
// Fix: CL-F1 (deep-review-2026-04-13-contrarian). Before this helper,
// Client.Close() cancelled the notifier Subscribe.Recv loop via a direct
// getNotifierClient -> nc.Close() call. That path has a TOCTOU window
// around setNotifierClient(nil) in the reconnect goroutine, where Close()
// can read nil and miss the freshly-stored new notifier client
// DoNotifierAlertService stores moments later. The watchdog closes that
// window by living alongside the blocking Subscribe call and firing
// nc.Close() regardless of which order Close() and setNotifierClient(nc)
// interleaved in.
func closeOnClosed(closedCh <-chan struct{}, doneCh <-chan struct{}, cancelFn func()) {
	select {
	case <-closedCh:
		if cancelFn != nil {
			cancelFn()
		}
	case <-doneCh:
	}
}

// clears connection state under lock and returns old conn for closing
func (c *Client) clearConnection() *grpc.ClientConn {
	if c == nil {
		return nil
	}
	c.connMu.Lock()
	conn := c._conn
	c._conn = nil
	c._remoteAddress = ""
	c._healthManualChecker = nil
	c.connMu.Unlock()
	return conn
}

// centralized helper to drop expired endpoints and normalize live list
func pruneExpiredEndpoints(eps []*serviceEndpoint) []*serviceEndpoint {
	now := time.Now()
	live := make([]*serviceEndpoint, 0, len(eps))
	seen := make(map[string]struct{})
	for _, ep := range eps {
		if ep == nil {
			continue
		}
		if !ep.CacheExpire.IsZero() && ep.CacheExpire.Before(now) {
			continue
		}
		key := fmt.Sprintf("%s:%d", ep.Host, ep.Port)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		live = append(live, ep)
	}
	return live
}

// safely set notifier client instance
func (c *Client) setNotifierClient(nc *NotifierClient) {
	if c == nil {
		return
	}

	c.notifierMu.Lock()
	// close old notifier if it differs from the new one
	if c._notifierClient != nil && c._notifierClient != nc {
		c._notifierClient.Close()
	}
	c._notifierClient = nc
	c.notifierMu.Unlock()
}

// safely get notifier client instance
func (c *Client) getNotifierClient() *NotifierClient {
	if c == nil {
		return nil
	}
	c.notifierMu.Lock()
	defer c.notifierMu.Unlock()
	return c._notifierClient
}

// Issue #19: Document global mutex _mux
// _mux is a global mutex used to serialize gRPC DialContext calls across all client instances.
// This is necessary because gRPC's internal resolver registration is not thread-safe.
// Concurrent calls to grpc.DialContext can cause race conditions in the resolver map.
// Performance impact: Dial operations are serialized, but this only affects startup/reconnect,
// not normal RPC operations which proceed concurrently after dial completes.
var _mux sync.Mutex

// Client is the gRPC client lifecycle owner for a connector-managed
// outbound connection. A Client encapsulates: configuration loading
// (client.yaml), service-discovery resolution (Cloud Map / DNS / direct),
// gRPC connection establishment with TLS or plaintext, an optional
// notifier sub-client for push-based service-discovery updates, an
// optional sidecar HTTP server for non-gRPC routes, automatic reconnect
// on notifier-driven service changes, and a graceful Close path that
// drains in-flight RPCs before tearing down the connection.
//
// Construct one with NewClient, then optionally configure exported
// fields BEFORE calling Dial. Dial returns once the gRPC connection is
// READY (or fails fast on misconfiguration). Use the underlying
// *grpc.ClientConn (accessed via the resolver hooks) to make RPCs after
// Dial returns. Call Close exactly once when done — Close is idempotent
// against repeated invocation but a Client is not designed for
// re-Dial-after-Close cycles unless the caller explicitly resets state.
//
// Concurrency:
//   - Exported fields (AppName, WebServerConfig, WaitForServerReady,
//     ConnectionCloseDelay, ...) MUST be set before Dial and MUST NOT
//     be mutated after.
//   - Dial and Close are guarded internally; Dial drains any stale
//     notifier-reconnect goroutine from a prior lifecycle (P2-13)
//     before flipping the closed flag.
//   - The notifier reconnect goroutine is single-instance via the
//     notifierReconnectActive atomic CAS guard and a defer-clear
//     pattern (see Close + Dial drain logic).
//
// Notes:
//
//  1. Using Compressor with RPC
//     a) import "google.golang.org/grpc/encoding/gzip"
//     b) in RPC Call, pass grpc.UseCompressor(gzip.Name)) in the third parameter
//     example: RPCCall(ctx, &pb.Request{...}, grpc.UseCompressor(gzip.Name))
//
//  2. Notifier Client yaml
//     a) xyz-notifier-client.yaml
//     where xyz is the target gRPC service endpoint name
type Client struct {
	// client properties
	AppName          string
	ConfigFileName   string
	CustomConfigPath string

	// web server config - for optional gin web server to be launched upon grpc client dial
	WebServerConfig *WebServerConfig

	// indicate if after dial, client will wait for target service health probe success before continuing to allow rpc
	WaitForServerReady bool

	// P2-14: ConnectionCloseDelay controls how long setConnection waits
	// before closing a replaced gRPC connection, allowing in-flight RPCs
	// to drain. Zero or negative falls back to the 100ms default
	// (connectionCloseDelay). Raise this for services with long-running
	// unary calls; lower it for tight test loops.
	ConnectionCloseDelay time.Duration

	// define oauth2 token fetch handler - if client use oauth2 to authorize per rpc call
	// once this handler is set, dial option will be configured for per rpc auth action
	// FetchOAuth2Token func() *oauth2.Token
	// TODO:

	// one or more unary client interceptors for handling wrapping actions
	UnaryClientInterceptors []grpc.UnaryClientInterceptor

	// one or more stream client interceptors for handling wrapping actions
	StreamClientInterceptors []grpc.StreamClientInterceptor

	// typically wrapper action to handle monitoring
	StatsHandler stats.Handler

	// handler to invoke before gRPC client dial is to start
	BeforeClientDial func(cli *Client)

	// handler to invoke after gRPC client dial performed
	AfterClientDial func(cli *Client)

	// handler to invoke before gRPC client connection is to close
	BeforeClientClose func(cli *Client)

	// handler to invoke after gRPC client connection has closed
	AfterClientClose func(cli *Client)

	// CL-F4: read or persist client config settings via atomic.Pointer so
	// Dial-side writes (initial readConfig on NewClient, nil on Dial error
	// paths) are race-free against concurrent RPC-side reads
	// (unaryCircuitBreakerHandler, health check interceptors, SD refresh,
	// etc.). Every reader MUST snapshot via getConfig() into a local before
	// dereferencing — a direct field read followed by a field deref is a
	// two-load TOCTOU under the Go memory model and would nil-deref panic
	// if a Dial error path fires between check and deref.
	_config atomic.Pointer[config]

	// service discovery object
	_sd *cloudmap.CloudMap

	// sqs object
	_sqs *sqs.SQS

	// define circuit breaker commands
	_circuitBreakers map[string]circuitbreaker.CircuitBreakerIFace

	// discovered endpoints for client load balancer use
	_endpoints []*serviceEndpoint

	// thread-safety guards
	endpointsMu sync.RWMutex // protects _endpoints
	cbMu        sync.RWMutex // protects _circuitBreakers
	connMu      sync.RWMutex // protects _conn, _remoteAddress, _healthManualChecker

	// instantiated internal objects
	_conn          *grpc.ClientConn
	_remoteAddress string

	// upon dial completion successfully,
	// auto instantiate a manual help checker
	_healthManualChecker *health.HealthClient

	// upon dial completion successfully,
	// auto instantiate a notifier client connection to the notifier server,
	// for auto service discovery callback notifications
	_notifierClient *NotifierClient

	// zap logger instead of standard log
	_z  *data.ZapLog
	zMu sync.Mutex

	closed  atomic.Bool
	closing atomic.Bool

	// CL-F3: _lifecycleMu serializes Dial and Close for a single Client
	// instance. Without this, a concurrent Close arriving after Dial has
	// reset closed=false and begun spinning up fresh state can flip
	// closed=true mid-setup, leaving the client "closed" but holding a
	// newly-dialed gRPC connection that nothing will reclaim. The result
	// is a connection leak plus caller confusion ("I just dialed and
	// Close hasn't been called from my side"). The window is small but
	// shows up under real contention — e.g., a parent orchestrator
	// calling Close() on shutdown while a reconnect loop is calling
	// Dial() from another goroutine.
	//
	// The mutex is deliberately coarse: Dial and Close are rare,
	// lifecycle-level operations. RPC fast paths use connMu/endpointsMu
	// and never touch _lifecycleMu, so serializing Dial against Close
	// has no effect on steady-state throughput.
	_lifecycleMu sync.Mutex

	// P1-5: closedCh is closed when the client transitions from open to
	// closed. Long-lived select loops (reconnect, health-check retry)
	// use this channel so they wake *immediately* on Close() rather
	// than polling c.closed.Load() or evaluating an IIFE once per
	// select entry (which suffers a TOCTOU gap between evaluation and
	// the timer firing). Guarded by closedChMu because Dial() can
	// re-allocate a fresh channel when the client is re-dialed after
	// a previous Close.
	closedChMu sync.Mutex
	closedCh   chan struct{}

	// guard to avoid spawning overlapping notifier reconnect loops
	notifierReconnectActive atomic.Bool
	notifierStartMu         sync.Mutex

	zOnce sync.Once

	// guard notifier client access to avoid races across callbacks/reconnect/close
	notifierMu sync.Mutex

	webServer       *ws.WebServer // track started web server for shutdown
	webServerStop   chan struct{} // signal channel to stop web server
	webServerMu     sync.Mutex
	_origWebCleanUp func() // FIX #33: original user-provided cleanup, prevents closure accumulation
}

// resetClosedChLocked allocates a fresh closedCh for a new client
// lifecycle. Called from Dial() so a re-dialed client gets a clean
// shutdown signal separate from any previous lifecycle's channel.
func (c *Client) resetClosedCh() {
	c.closedChMu.Lock()
	defer c.closedChMu.Unlock()
	c.closedCh = make(chan struct{})
}

// getClosedCh returns the current closedCh. Long-lived select loops
// snapshot this once per iteration so they always observe the channel
// that was current when they entered the select, even if Dial() has
// since swapped in a new one for a subsequent lifecycle. Returns a
// pre-closed channel if closedCh has never been initialized (treating
// an uninitialized client as not-yet-blockable — callers should
// always check c.closed.Load() first).
func (c *Client) getClosedCh() <-chan struct{} {
	c.closedChMu.Lock()
	defer c.closedChMu.Unlock()
	if c.closedCh == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return c.closedCh
}

// markClosedCh fires the closedCh exactly once. Idempotent: subsequent
// calls within the same lifecycle are no-ops. Must be called from the
// Close() path AFTER the closed CAS flip so that observers which wake
// on the channel also see c.closed.Load() == true.
func (c *Client) markClosedCh() {
	c.closedChMu.Lock()
	defer c.closedChMu.Unlock()
	if c.closedCh == nil {
		return
	}
	select {
	case <-c.closedCh:
		// already closed, no-op
	default:
		close(c.closedCh)
	}
}

// safely replace the current endpoints slice
func (c *Client) setEndpoints(eps []*serviceEndpoint) {
	c.endpointsMu.Lock()
	defer c.endpointsMu.Unlock()

	live := pruneExpiredEndpoints(eps)

	// Issue #3: Deep copy endpoints to own the data and prevent data races
	copied := make([]*serviceEndpoint, len(live))
	for i, ep := range live {
		if ep == nil {
			continue
		}
		epCopy := *ep
		copied[i] = &epCopy
	}
	c._endpoints = copied
}

// snapshot current endpoints (deep copy) under write lock for atomic read-prune-write
func (c *Client) endpointsSnapshot() []*serviceEndpoint {
	if c == nil {
		return nil
	}

	// Issue #2: Use write lock for atomic read-prune-write operation
	c.endpointsMu.Lock()
	defer c.endpointsMu.Unlock()

	// Prune expired endpoints
	live := pruneExpiredEndpoints(c._endpoints)

	// Update internal state if pruning removed entries
	if len(live) != len(c._endpoints) {
		c._endpoints = live
	}

	// Deep copy to prevent caller from modifying internal state
	out := make([]*serviceEndpoint, len(live))
	for i, ep := range live {
		if ep == nil {
			continue
		}
		copied := *ep
		out[i] = &copied
	}
	return out
}

// getCircuitBreaker retrieves a circuit breaker by name in a thread-safe manner
func (c *Client) getCircuitBreaker(name string) circuitbreaker.CircuitBreakerIFace {
	if c == nil {
		return nil
	}
	c.cbMu.RLock()
	defer c.cbMu.RUnlock()
	if c._circuitBreakers == nil {
		return nil
	}
	return c._circuitBreakers[name]
}

// setCircuitBreaker sets a circuit breaker by name in a thread-safe manner
func (c *Client) setCircuitBreaker(name string, cb circuitbreaker.CircuitBreakerIFace) {
	if c == nil {
		return
	}
	c.cbMu.Lock()
	defer c.cbMu.Unlock()
	if c._circuitBreakers == nil {
		c._circuitBreakers = make(map[string]circuitbreaker.CircuitBreakerIFace)
	}
	c._circuitBreakers[name] = cb
}

// helper to (re)wire notifier handlers every time before dial
func (c *Client) configureNotifierHandlers(nc *NotifierClient) {
	if c == nil || nc == nil || c.getConfig() == nil {
		return
	}

	nc.ServiceHostOnlineHandler = func(host string, port uint) {
		// CL-F4: snapshot config once per handler invocation.
		cfg := c.getConfig()
		if c == nil || c.closed.Load() || cfg == nil {
			return
		}
		cacheExpSeconds := cfg.Target.SdEndpointCacheExpires
		if cacheExpSeconds == 0 {
			cacheExpSeconds = defaultCacheExpireSeconds
		}

		svcKey := strings.ToLower(cfg.Target.ServiceName + "." + cfg.Target.NamespaceName)
		cacheAddServiceEndpoints(svcKey, []*serviceEndpoint{
			{
				SdType:      cfg.Target.ServiceDiscoveryType,
				Host:        host,
				Port:        port,
				InstanceId:  "",
				ServiceId:   "",
				Version:     cfg.Target.InstanceVersion,
				CacheExpire: time.Now().Add(time.Duration(cacheExpSeconds) * time.Second),
			},
		})
		c.setEndpoints(cacheGetLiveServiceEndpoints(svcKey, cfg.Target.InstanceVersion, true))
		if resolverErr := c.UpdateLoadBalanceResolver(); resolverErr != nil {
			log.Printf("warning: UpdateLoadBalanceResolver failed in ServiceHostOnlineHandler: %v", resolverErr)
		}
	}

	nc.ServiceHostOfflineHandler = func(host string, port uint) {
		// CL-F4: snapshot config once per handler invocation.
		cfg := c.getConfig()
		if c == nil || c.closed.Load() || cfg == nil {
			return
		}
		svcKey := strings.ToLower(cfg.Target.ServiceName + "." + cfg.Target.NamespaceName)
		cachePurgeServiceEndpointByHostAndPort(svcKey, host, port)
		c.setEndpoints(cacheGetLiveServiceEndpoints(svcKey, cfg.Target.InstanceVersion, true))
		if resolverErr := c.UpdateLoadBalanceResolver(); resolverErr != nil {
			log.Printf("warning: UpdateLoadBalanceResolver failed in ServiceHostOfflineHandler: %v", resolverErr)
		}
	}

	nc.ServiceAlertStoppedHandler = func(reason string) {
		if !strings.Contains(strings.ToLower(reason), "transport is closing") {
			if z := c.ZLog(); z != nil {
				z.Printf("--- Notifier Client Service Disconnected Normally: %s ---", reason)
			} else {
				log.Printf("--- Notifier Client Service Disconnected Normally: %s ---", reason)
			}
			return
		}

		if c == nil || c.closed.Load() {
			return
		}

		if !c.notifierReconnectActive.CompareAndSwap(false, true) {
			if z := c.ZLog(); z != nil {
				z.Warnf("Notifier reconnect already in progress; skipping duplicate attempt")
			} else {
				log.Printf("Notifier reconnect already in progress; skipping duplicate attempt")
			}
			return
		}

		if z := c.ZLog(); z != nil {
			z.Warnf("!!! Notifier Client Service Disconnected - Re-Attempting Connection in 5 Seconds...!!!")
		} else {
			log.Printf("!!! Notifier Client Service Disconnected - Re-Attempting Connection in 5 Seconds...!!!")
		}

		// Issue #4: Simplify the reconnect loop with proper cancellation
		// P1-4: Previously this used a 100ms checkTimer to poll
		// c.closed.Load() 50 times per 5-second reconnect cycle across
		// every notifier-enabled client in the fleet. Replaced with a
		// persistent closedCh fed into the select so Close() wakes the
		// goroutine immediately and no polling is needed.
		// CL-F5: wrapped in safeGo so a panic in
		// DoNotifierAlertService → Subscribe → user-supplied
		// ServiceHostOnlineHandler does NOT tear down the process.
		safego.Go("notifier-reconnect-loop", func() {
			defer c.notifierReconnectActive.Store(false)

			ticker := time.NewTicker(defaultNotifierReconnectDelay)
			defer ticker.Stop()

			closedCh := c.getClosedCh()

			for {
				// Check if closed before waiting
				if c.closed.Load() {
					return
				}

				// Wait for ticker tick or Close() signal — no polling.
				select {
				case <-ticker.C:
					// Continue to reconnection attempt
				case <-closedCh:
					return
				}

				// Re-check after ticker fires; also probe closedCh for
				// the stale-lifecycle case where c.closed was reset by a
				// new Dial (BL-4).
				if c.closed.Load() {
					return
				}
				select {
				case <-closedCh:
					return
				default:
				}

				if current := c.getNotifierClient(); current != nil {
					current.Close() // close leaked notifier connection before dropping it
					c.setNotifierClient(nil)
				}
				if c.closed.Load() {
					return
				}
				// BL-4: guard against stale goroutine outliving its lifecycle.
				// After Close()+Dial(), c.closed is reset to false by the new
				// lifecycle, so the Load() check above passes. But the captured
				// closedCh still refers to the OLD lifecycle's channel which
				// Close() already closed. A non-blocking probe on closedCh
				// catches the stale goroutine before it enters the
				// potentially-long-blocking DoNotifierAlertService call, which
				// holds notifierStartMu and would delay the new lifecycle's
				// notifier startup until the stale Subscribe stream terminates.
				select {
				case <-closedCh:
					return
				default:
				}

				// let DoNotifierAlertService create and wire a fresh client
				if err := c.DoNotifierAlertService(); err != nil {
					if z := c.ZLog(); z != nil {
						z.Errorf("... Reconnect Notifier Server Failed: %s (Will Retry in 5 Seconds)", err.Error())
					} else {
						log.Printf("... Reconnect Notifier Server Failed: %s (Will Retry in 5 Seconds)", err.Error())
					}
					continue
				}
				return
			}
		})
	}
}

// serviceEndpoint represents a specific service endpoint connection target
type serviceEndpoint struct {
	SdType string // srv, api, direct

	Host string
	Port uint

	InstanceId string
	ServiceId  string
	Version    string

	CacheExpire time.Time
}

// NewClient constructs a Client with only AppName / ConfigFileName /
// CustomConfigPath set. Configure all other exported fields (such as
// WebServerConfig, WaitForServerReady, ConnectionCloseDelay, the
// notifier handlers) on the returned struct BEFORE calling Dial.
//
// Unlike NewService for the server side, this constructor does not
// validate inputs — validation happens at Dial time when the config
// file is read. A returned Client whose AppName is empty will fail at
// Dial with a config-load error.
func NewClient(appName string, configFileName string, customConfigPath string) *Client {
	return &Client{
		AppName:          appName,
		ConfigFileName:   configFileName,
		CustomConfigPath: customConfigPath,
	}
}

// getConfig returns an atomic snapshot of the client's config. Every
// reader MUST route through this helper (not direct c._config access)
// because concurrent Dial error paths may store nil at any moment.
// Capture the returned pointer into a local variable and dereference
// the local — never re-read c._config inside the same logical operation
// (CL-F4 invariant).
func (c *Client) getConfig() *config {
	if c == nil {
		return nil
	}
	return c._config.Load()
}

// safeGo is provided by internal/safego.Go — see that package for the
// full documentation. All goroutine spawns in this file use safego.Go.

// safeCall invokes a user-provided callback (notifier handler,
// lifecycle hook, etc.) with panic recovery. If fn panics, the
// value and full stack trace are logged and control returns
// normally — the caller goroutine survives.
//
// Fix: CL-F5 (deep-review-2026-04-13-contrarian). Wrap every
// invocation of a user-supplied func field so a buggy consumer
// cannot crash the reconnect goroutine, the Close path, or an
// in-flight notification. Use this for BeforeClientDial /
// AfterClientDial / BeforeClientClose / AfterClientClose and for
// ServiceHostOnlineHandler / ServiceHostOfflineHandler /
// ServiceAlertSkippedHandler / ServiceAlertStoppedHandler.
func safeCall(name string, fn func()) {
	if fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("!!! client.safeCall panic recovered in %q: %v\n%s !!!", name, r, debug.Stack())
		}
	}()
	fn()
}

// readConfig will read in config data
func (c *Client) readConfig() error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	// CL-F4: build the *config locally and publish atomically. Error paths
	// nil-store so readers see a consistent transition.
	newCfg := &config{
		AppName:          c.AppName,
		ConfigFileName:   c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,
	}

	if err := newCfg.Read(); err != nil {
		c._config.Store(nil)
		return fmt.Errorf("Read Config Failed: %w", err)
	}

	if newCfg.Target.InstancePort > 65535 {
		c._config.Store(nil)
		return fmt.Errorf("Configured Instance Port Not Valid: %s", "Tcp Port Max is 65535")
	}

	// publish the fully-initialized config BEFORE wiring the logger so
	// that any reader racing on getConfig() sees a usable value.
	c._config.Store(newCfg)

	// setup logger
	c.zMu.Lock()
	c._z = &data.ZapLog{
		DisableLogger:   !newCfg.Target.ZapLogEnabled,
		OutputToConsole: newCfg.Target.ZapLogOutputConsole,
		AppName:         newCfg.AppName,
	}
	if e := c._z.Init(); e != nil {
		c.zMu.Unlock()
		return fmt.Errorf("Init ZapLog Failed: %s", e.Error())
	}
	c.zMu.Unlock()

	cacheDisableLogging(!newCfg.Target.ZapLogEnabled)

	return nil
}

// buildDialOptions returns slice of dial options built from client struct fields
func (c *Client) buildDialOptions(loadBalancerPolicy string) (opts []grpc.DialOption, err error) {
	if c == nil {
		return []grpc.DialOption{}, fmt.Errorf("Client Object Nil")
	}

	// Issue #8 / CL-F4: snapshot config once at function entry to avoid
	// TOCTOU between nil-check and subsequent field dereferences.
	cfg := c.getConfig()
	if cfg == nil {
		return []grpc.DialOption{}, fmt.Errorf("Config Data Not Loaded")
	}

	logger := c.ZLog() // ensure logger is initialized
	logPrintf := func(msg string) {
		if logger != nil {
			logger.Printf("%s", msg)
		} else {
			log.Printf("%s", msg)
		}
	}

	//
	// config client options
	//

	// set tls credential dial option
	if util.LenTrim(cfg.Grpc.ServerCACertFiles) > 0 {
		tls := new(tlsconfig.TlsConfig)
		if tc, e := tls.GetClientTlsConfig(strings.Split(cfg.Grpc.ServerCACertFiles, ","), cfg.Grpc.ClientCertFile, cfg.Grpc.ClientKeyFile); e != nil {
			return []grpc.DialOption{}, fmt.Errorf("Set Dial Option Client TLS Failed: %s", e.Error())
		} else {
			opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tc)))
		}
	} else {
		// if not tls secured, use inSecure dial option
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	//
	// setup xray is configured via yaml
	//
	if cfg.Target.TraceUseXRay {
		if err := xray.Init("127.0.0.1:2000", "1.2.0"); err != nil {
			log.Println("!!! X-Ray Init Failed: " + err.Error() + " — tracing disabled !!!")
		}
	}

	/*
		// set per rpc auth via oauth2
		// TODO:
		if c.FetchOAuth2Token != nil {
			perRpc := oauth.NewOauthAccess(c.FetchOAuth2Token())

			if perRpc != nil {
				opts = append(opts, grpc.WithPerRPCCredentials(perRpc))
			}
		}
	*/

	// set user agent if defined
	if util.LenTrim(cfg.Grpc.UserAgent) > 0 {
		opts = append(opts, grpc.WithUserAgent(cfg.Grpc.UserAgent))
	}

	// set with block option,
	// with block will halt code execution until after dial completes
	//
	// P1-CONN-CL-A (2026-04-15): SA1019 deferred to connector v2.0.0 per
	// rule #10 — grpc.NewClient uses lazy-dial semantics where Dial
	// returns before the connection is established. Switching would
	// break every consumer that relies on "Dial returns means the
	// connection is up" and must be done in a coordinated v2.0.0 batch.
	// See: _src/docs/repos/connector/findings/2026-04-15-contrarian-pass4/P1-CONN-CL-A.md
	if cfg.Grpc.DialBlockingMode {
		opts = append(opts, grpc.WithBlock()) //nolint:staticcheck // SA1019 deferred to v2.0.0
	}

	// set default server config for load balancer and/or health check
	defSvrConf := ""

	if cfg.Grpc.UseLoadBalancer && util.LenTrim(loadBalancerPolicy) > 0 {
		defSvrConf = loadBalancerPolicy
	}

	if cfg.Grpc.UseHealthCheck {
		if util.LenTrim(defSvrConf) > 0 {
			defSvrConf += ", "
		}

		defSvrConf += `"healthCheckConfig":{"serviceName":""}`
	}

	if util.LenTrim(defSvrConf) > 0 {
		opts = append(opts, grpc.WithDefaultServiceConfig(fmt.Sprintf(`{%s}`, defSvrConf)))
	}

	// set connect timeout value
	if cfg.Grpc.DialMinConnectTimeout > 0 {
		opts = append(opts, grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: time.Duration(cfg.Grpc.DialMinConnectTimeout) * time.Second,
		}))
	}

	// set keep alive dial options
	ka := keepalive.ClientParameters{
		PermitWithoutStream: cfg.Grpc.KeepAlivePermitWithoutStream,
	}

	if cfg.Grpc.KeepAliveInactivePingTimeTrigger > 0 {
		ka.Time = time.Duration(cfg.Grpc.KeepAliveInactivePingTimeTrigger) * time.Second
	}

	if cfg.Grpc.KeepAliveInactivePingTimeout > 0 {
		ka.Timeout = time.Duration(cfg.Grpc.KeepAliveInactivePingTimeout) * time.Second
	}

	opts = append(opts, grpc.WithKeepaliveParams(ka))

	// set read buffer dial option, 32 kb default, 32 * 1024
	if cfg.Grpc.ReadBufferSize > 0 {
		opts = append(opts, grpc.WithReadBufferSize(int(cfg.Grpc.ReadBufferSize)))
	}

	// set write buffer dial option, 32 kb default, 32 * 1024
	if cfg.Grpc.WriteBufferSize > 0 {
		opts = append(opts, grpc.WithWriteBufferSize(int(cfg.Grpc.WriteBufferSize)))
	}

	// turn off retry when retry default is enabled in the future framework versions
	opts = append(opts, grpc.WithDisableRetry())

	// Build interceptor chains from a fresh local slice to avoid duplicate appends across re-dials.
	unaryInts := append([]grpc.UnaryClientInterceptor{}, c.UnaryClientInterceptors...)
	if cfg.Grpc.CircuitBreakerEnabled {
		logPrintf("Setup Unary Circuit Breaker Interceptor")
		unaryInts = append(unaryInts, c.unaryCircuitBreakerHandler)
	}

	if xray.XRayServiceOn() {
		logPrintf("Setup Unary XRay Tracer Interceptor")
		// P1-CONN-CL-A (2026-04-15): the old c.unaryXRayTracerHandler
		// was superseded by tracer.TracerUnaryClientInterceptor; the
		// dead method has been removed.
		unaryInts = append(unaryInts, tracer.TracerUnaryClientInterceptor(cfg.Target.AppName+"-Client"))
	}

	count := len(unaryInts)

	if count == 1 {
		opts = append(opts, grpc.WithUnaryInterceptor(unaryInts[0]))
	} else if count > 1 {
		opts = append(opts, grpc.WithChainUnaryInterceptor(unaryInts...))
	}

	// Build stream interceptors from a fresh local slice to avoid duplicate appends across re-dials.
	streamInts := append([]grpc.StreamClientInterceptor{}, c.StreamClientInterceptors...)
	if cfg.Grpc.CircuitBreakerEnabled {
		logPrintf("Setup Stream Circuit Breaker Interceptor")
		streamInts = append(streamInts, c.streamCircuitBreakerHandler)
	}

	if xray.XRayServiceOn() {
		logPrintf("Setup Stream XRay Tracer Interceptor")
		streamInts = append(streamInts, c.streamXRayTracerHandler)
	}

	count = len(streamInts)

	if count == 1 {
		opts = append(opts, grpc.WithStreamInterceptor(streamInts[0]))
	} else if count > 1 {
		opts = append(opts, grpc.WithChainStreamInterceptor(streamInts...))
	}

	// for monitoring use
	if c.StatsHandler != nil {
		opts = append(opts, grpc.WithStatsHandler(c.StatsHandler))
	}

	// verbose dial error
	//
	// P1-CONN-CL-A (2026-04-15): SA1019 deferred to connector v2.0.0
	// along with the grpc.WithBlock / grpc.DialContext migration — the
	// three deprecations are coupled to the eager-dial → lazy-dial
	// semantic flip and must migrate together. See ~L953.
	opts = append(opts, grpc.FailOnNonTempDialError(true)) //nolint:staticcheck // SA1019 deferred to v2.0.0

	//
	// complete
	//
	err = nil
	return
}

func (c *Client) stopWebServerFast(timeout time.Duration) { // fast shutdown helper for dial failures
	if c == nil {
		return
	}

	c.webServerMu.Lock()
	defer c.webServerMu.Unlock()

	c.shutdownWebServerLocked(timeout, true)
}

// Issue #18: Extract common web server shutdown logic
// shutdownWebServerLocked is a shared helper to shutdown the web server (must be called with webServerMu held)
// If callCleanup is true, invokes WebServerConfig.CleanUp (typically for Route53 DNS cleanup)
func (c *Client) shutdownWebServerLocked(timeout time.Duration, callCleanup bool) {
	if c.webServer == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	if stopper, ok := interface{}(c.webServer).(interface{ Shutdown(context.Context) error }); ok {
		if err := stopper.Shutdown(ctx); err != nil {
			log.Printf("client web server shutdown returned error (timeout=%s): %v", timeout, err)
		}
	}
	cancel()

	// invoke cleanup hook if requested (e.g., Route53 DNS cleanup)
	// CONN-R3-001: wrap with safeCall — user callback panic must not crash process
	if callCleanup && c.WebServerConfig != nil && c.WebServerConfig.CleanUp != nil {
		safeCall("WebServerConfig.CleanUp", c.WebServerConfig.CleanUp)
	}

	// wait for server goroutine to signal completion or timeout
	// FIX: Use time.NewTimer instead of time.After to avoid goroutine leak
	if c.webServerStop != nil {
		stopTimer := time.NewTimer(timeout + time.Second)
		select {
		case <-c.webServerStop:
			stopTimer.Stop()
		case <-stopTimer.C:
		}
		c.webServerStop = nil
	}
	c.webServer = nil
}

// ZLog returns the internal *data.ZapLog used by the Client for
// structured logging. The logger is created during Dial from the
// loaded config (Target.ZapLogEnabled / ZapLogOutputConsole). If
// Dial has not yet been called, or if the receiver is nil, ZLog
// returns nil — callers should nil-check before logging.
//
// The returned logger is shared across the Client and any
// notifier sub-client it owns; do not replace it after Dial.
func (c *Client) ZLog() *data.ZapLog {
	if c == nil {
		log.Println("ZLog(): Client Object Nil")
		return nil
	}

	// Issue #7: Use sync.Once for thread-safe lazy initialization with fast-path check
	// Fast path check without lock
	c.zMu.Lock()
	if c._z != nil {
		z := c._z
		c.zMu.Unlock()
		return z
	}
	c.zMu.Unlock()

	// Slow path: initialize logger
	c.zOnce.Do(func() {
		c.zMu.Lock()
		defer c.zMu.Unlock()

		// Double-check after acquiring lock
		if c._z != nil {
			return
		}

		appName := "Default-BeforeConfigLoad"
		disableLogger := true
		outputConsole := true

		// CL-F4: snapshot to avoid two-load TOCTOU.
		if cfg := c.getConfig(); cfg != nil {
			appName = cfg.AppName
			disableLogger = !cfg.Target.ZapLogEnabled
			outputConsole = cfg.Target.ZapLogOutputConsole
		}

		z := &data.ZapLog{
			DisableLogger:   disableLogger,
			OutputToConsole: outputConsole,
			AppName:         appName,
		}
		if err := z.Init(); err != nil {
			log.Println("!!! ZapLog Init Failed: " + err.Error() + " !!!")
		}
		c._z = z
	})

	c.zMu.Lock()
	z := c._z
	c.zMu.Unlock()
	return z
}

// SP-008 P3-CONN-4 (2026-04-15): canonical logging helpers used by the
// circuit-breaker handlers. Previously captured via per-call closures
// that allocated ~288 B + 3 allocs per unary RPC and ~600-900 B per
// stream. Hoisting to methods eliminates those allocations entirely on
// the CB path.
//
// Naming note: the methods intentionally DO NOT end in `f` (Printf /
// Errorf / Warnf) — the `go vet` printf analyzer treats an `f`-suffix
// as "format string first arg" and would flag every literal-concat
// call site. These helpers receive pre-formatted messages and pass
// them through `"%s"` so a `%` character in a message body cannot be
// misread as a format specifier — this preserves the closure's
// original safety property. If you need format-style logging from
// inside the client, call `c.ZLog().Printf(format, args...)` directly.
//
// logLine logs an info-level message.
func (c *Client) logLine(msg string) {
	if z := c.ZLog(); z != nil {
		z.Printf("%s", msg)
	} else {
		log.Printf("%s", msg)
	}
}

// logErr logs an error-level message.
func (c *Client) logErr(msg string) {
	if z := c.ZLog(); z != nil {
		z.Errorf("%s", msg)
	} else {
		log.Printf("%s", msg)
	}
}

// logWarn logs a warning-level message.
func (c *Client) logWarn(msg string) {
	if z := c.ZLog(); z != nil {
		z.Warnf("%s", msg)
	} else {
		log.Printf("%s", msg)
	}
}

// PreloadConfigData will load the config data before Dial()
func (c *Client) PreloadConfigData() error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	if err := c.readConfig(); err != nil {
		return err
	} else {
		return nil
	}
}

// ConfiguredDialMinConnectTimeoutSeconds gets the timeout seconds from config yaml
func (c *Client) ConfiguredDialMinConnectTimeoutSeconds() uint {
	if c == nil {
		log.Println("ConfiguredDialMinConnectTimeoutSeconds(): Client Object Nil")
		return 5
	}

	// CL-F4: snapshot to avoid two-load TOCTOU.
	if cfg := c.getConfig(); cfg != nil {
		if cfg.Grpc.DialMinConnectTimeout > 0 {
			return cfg.Grpc.DialMinConnectTimeout
		}
	}

	return 5
}

// ConfiguredForClientDial checks if the config yaml is ready for client dial operation
func (c *Client) ConfiguredForClientDial() bool {
	if c == nil {
		log.Println("ConfiguredForClientDial(): Client Object Nil")
		return false
	}

	// CL-F4: snapshot to avoid two-load TOCTOU.
	cfg := c.getConfig()
	if cfg == nil {
		return false
	}

	if util.LenTrim(cfg.Target.AppName) == 0 {
		return false
	}

	if util.LenTrim(cfg.Target.ServiceDiscoveryType) == 0 {
		return false
	}

	if util.LenTrim(cfg.Target.ServiceName) == 0 {
		return false
	}

	if util.LenTrim(cfg.Target.NamespaceName) == 0 {
		return false
	}

	if util.LenTrim(cfg.Target.Region) == 0 {
		return false
	}

	return true
}

// ConfiguredForSNSDiscoveryTopicArn indicates if the sns topic arn for service discovery is configured within the config yaml
func (c *Client) ConfiguredForSNSDiscoveryTopicArn() bool {
	if c == nil {
		log.Println("ConfiguredForSNSDiscoveryTopicArn(): Client Object Nil")
		return false
	}

	// CL-F4: snapshot to avoid two-load TOCTOU.
	cfg := c.getConfig()
	if cfg == nil {
		return false
	}

	if util.LenTrim(cfg.Topics.SnsDiscoveryTopicArn) == 0 {
		return false
	}

	return true
}

// ConfiguredSNSDiscoveryTopicArn returns the sns discovery topic arn as configured in config yaml
func (c *Client) ConfiguredSNSDiscoveryTopicArn() string {
	if c == nil {
		log.Println("ConfiguredSNSDiscoveryTopicArn(): Client Object Nil")
		return ""
	}

	// CL-F4: snapshot to avoid two-load TOCTOU.
	cfg := c.getConfig()
	if cfg == nil {
		return ""
	}

	return cfg.Topics.SnsDiscoveryTopicArn
}

// Ready indicates client connection is ready to invoke grpc methods
func (c *Client) Ready() bool {
	if c == nil {
		log.Println("Ready(): Client Object Nil")
		return false
	}

	if c.closed.Load() { // ensure closed clients are never reported ready
		return false
	}

	eps := c.endpointsSnapshot() // protect _endpoints read

	c.connMu.RLock()
	conn := c._conn
	c.connMu.RUnlock()

	if conn != nil && len(eps) > 0 {
		state := conn.GetState() // avoid multiple state reads
		if state == connectivity.Ready || state == connectivity.Idle {
			return true
		}
	}

	return false
}

// Dial loads the client configuration, resolves the target service
// (Cloud Map, DNS, or direct address depending on the config), opens
// a gRPC connection, optionally starts the sidecar HTTP server, and
// initializes the notifier sub-client when configured.
//
// The supplied context governs only this Dial call's deadline /
// cancellation — once Dial returns, the underlying gRPC connection
// outlives the context. Pass nil for context.Background semantics.
//
// Dial is single-use per logical lifecycle: calling it again after
// Close on the same Client is unusual and not the primary supported
// pattern. When Dial is invoked on a previously-closed Client, the
// implementation drains any stale notifier-reconnect goroutine from
// the prior lifecycle (P2-13) before flipping the closed flag back to
// false. The drain is bounded by notifierReconnectDrainTimeout and
// will log a warning if a stale goroutine is still in-flight after
// the drain window.
//
// Returns an error if any step fails — the Client is in a partially-
// initialized state on failure and should be Closed before discarding.
//
// Concurrency: Dial is internally serialized through the global _mux
// because gRPC's resolver registration is not thread-safe. This
// serialization is bounded to Dial only — established connections
// proceed concurrently for normal RPCs.
func (c *Client) Dial(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	// CL-F3: serialize Dial against Close for this Client. Without this
	// lock a concurrent Close arriving after closed.Store(false) below
	// can shut the client down while state init is still in flight,
	// leaking the freshly-dialed gRPC connection.
	c._lifecycleMu.Lock()
	defer c._lifecycleMu.Unlock()

	cleanupConn := false // track gRPC conn cleanup on failure
	cleanupWeb := false  // track web server cleanup on failure
	cleanupSd := false   // track CloudMap cleanup on failure
	cleanupSqs := false  // track SQS cleanup on failure
	defer func() {       // unified cleanup for partial failures
		if cleanupWeb {
			c.stopWebServerFast(webServerShutdownTimeout)
		}
		if cleanupConn {
			if closedConn := c.clearConnection(); closedConn != nil {
				_ = closedConn.Close()
			}
		}
		if cleanupSqs {
			c.connMu.Lock()
			sqsCleanup := c._sqs
			c._sqs = nil
			c.connMu.Unlock()
			if sqsCleanup != nil {
				sqsCleanup.Disconnect()
			}
		}
		if cleanupSd {
			c.connMu.Lock()
			sdCleanup := c._sd
			c._sd = nil
			c.connMu.Unlock()
			if sdCleanup != nil {
				sdCleanup.Disconnect()
			}
		}
	}()

	// P2-13 / C3-005: drain any in-flight notifier reconnect goroutine from
	// a prior lifecycle BEFORE flipping closed back to false. The reconnect
	// loop captures closedCh once at spawn-time (the OLD lifecycle's
	// channel, already closed by Close() via markClosedCh); the new
	// lifecycle will allocate a FRESH closedCh a few lines below via
	// resetClosedCh(). Because the stale goroutine holds its own pointer to
	// the already-closed channel, it cannot observe the new lifecycle's
	// closedCh -- that isolation is what makes it self-terminating: every
	// iteration of its select hits the <-closedCh case and returns cleanly.
	//
	// Why we still drain: the stale goroutine can be parked inside
	// DoNotifierAlertService, which is an uncancellable SDK call that must
	// run to completion before the goroutine loops back to its select and
	// observes the closed channel. We wait (bounded) so the new lifecycle
	// does not begin its own Subscribe while a stale Subscribe is still
	// mid-call on the old _notifierClient. The warning below is a
	// diagnostic -- if it trips, an SDK call is taking longer than the
	// drain budget, not a correctness bug in the closedCh handoff.
	if c.notifierReconnectActive.Load() {
		drainDeadline := time.Now().Add(notifierReconnectDrainTimeout)
		for c.notifierReconnectActive.Load() && time.Now().Before(drainDeadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if c.notifierReconnectActive.Load() {
			if z := c.ZLog(); z != nil {
				z.Warnf("Dial: stale notifier reconnect goroutine still active after %s drain; proceeding (stale goroutine will self-terminate on its captured closedCh)", notifierReconnectDrainTimeout)
			} else {
				log.Printf("Dial: stale notifier reconnect goroutine still active after %s drain; proceeding (stale goroutine will self-terminate on its captured closedCh)", notifierReconnectDrainTimeout)
			}
		}
	}

	// Issue #6: Reset both closed and closing flags at the start of Dial to allow re-dial
	c.closed.Store(false)
	c.closing.Store(false)
	// P1-5: allocate a fresh closedCh for this dial lifecycle. Long-lived
	// selects (reconnect loop, health-check retry) will use this channel
	// as an immediate wake signal on Close(). Must be done AFTER the
	// closed flag reset so any stray goroutine that was still observing
	// the previous lifecycle's channel does not race in here.
	c.resetClosedCh()
	c.setConnection(nil, "")

	// read client config data in
	if c.getConfig() == nil {
		if err := c.readConfig(); err != nil {
			return err
		}
	}

	if !c.ConfiguredForClientDial() {
		c._config.Store(nil)
		return fmt.Errorf("%s not yet configured for gRPC client dial, please check config file", c.ConfigFileName)
	}

	// CL-F4: snapshot the config pointer ONCE for the rest of Dial. All
	// subsequent reads dereference the local — never c._config directly —
	// so a concurrent Dial error path storing nil cannot TOCTOU us.
	cfg := c.getConfig()
	if cfg == nil {
		return fmt.Errorf("%s config went nil after dial readiness check", c.ConfigFileName)
	}

	// if rest target ca cert files defined, load self-signed ca certs so that this service may use those host resources
	if util.LenTrim(cfg.Target.RestTargetCACertFiles) > 0 {
		if err := rest.AppendServerCAPemFiles(strings.Split(cfg.Target.RestTargetCACertFiles, ",")...); err != nil {
			if z := c.ZLog(); z != nil {
				z.Errorf("!!! Load Rest Target Self-Signed CA Cert Files '" + cfg.Target.RestTargetCACertFiles + "' Failed: " + err.Error() + " !!!")
			}
		}
	}

	if z := c.ZLog(); z != nil {
		z.Printf("Client " + cfg.AppName + " Starting to Connect with " + cfg.Target.ServiceName + "." + cfg.Target.NamespaceName + "...")
	}

	// setup sqs and sns if configured
	if util.LenTrim(cfg.Queues.SqsLoggerQueueUrl) > 0 {
		sqsAdapter, e := queue.NewQueueAdapter(awsregion.GetAwsRegion(cfg.Target.Region), nil)
		if e != nil {
			if z := c.ZLog(); z != nil {
				z.Errorf("Get SQS Queue Adapter Failed: %s", e.Error())
			}
			sqsAdapter = nil
		}
		c.connMu.Lock()
		c._sqs = sqsAdapter
		c.connMu.Unlock()
		cleanupSqs = sqsAdapter != nil
	} else {
		c.connMu.Lock()
		c._sqs = nil
		c.connMu.Unlock()
	}

	// circuit breakers prep
	c.cbMu.Lock()
	c._circuitBreakers = map[string]circuitbreaker.CircuitBreakerIFace{}
	c.cbMu.Unlock()

	// connect sd
	if err := c.connectSd(); err != nil {
		return err
	}
	c.connMu.RLock()
	cleanupSd = c._sd != nil
	c.connMu.RUnlock()

	// discover service endpoints
	if err := c.discoverEndpoints(true); err != nil {
		return err
	}
	eps := c.endpointsSnapshot() // protect _endpoints read
	if len(eps) == 0 {
		return fmt.Errorf("no service endpoints discovered for %s.%s", cfg.Target.ServiceName, cfg.Target.NamespaceName)
	}

	if z := c.ZLog(); z != nil {
		z.Printf("... Service Discovery for " + cfg.Target.ServiceName + "." + cfg.Target.NamespaceName + " Found " + strconv.Itoa(len(eps)) + " Endpoints:")
	}

	// get endpoint addresses
	endpointAddrs := make([]string, 0, len(eps))
	for i, ep := range eps {
		endpointAddrs = append(endpointAddrs, fmt.Sprintf("%s:%s", ep.Host, util.UintToStr(ep.Port)))

		info := strconv.Itoa(i+1) + ") "
		info += ep.SdType + "=" + ep.Host + ":" + util.UintToStr(ep.Port) + ", "
		info += "Version=" + ep.Version + ", "
		info += "CacheExpires=" + util.FormatDateTime(ep.CacheExpire)

		if z := c.ZLog(); z != nil {
			z.Printf("       - " + info)
		}
	}

	// setup resolver and setup load balancer
	var target string
	var loadBalancerPolicy string

	if cfg.Target.ServiceDiscoveryType != "direct" {
		var err error

		// very important: client load balancer scheme name must be alpha and lower cased
		//                 if scheme name is not valid, error will occur: transport error, tcp port unknown
		schemeName, _ := util.ExtractAlpha(cfg.AppName)
		schemeName = strings.ToLower("clb" + schemeName)

		target, loadBalancerPolicy, err = loadbalancer.WithRoundRobin(schemeName, fmt.Sprintf("%s.%s", cfg.Target.ServiceName, cfg.Target.NamespaceName), endpointAddrs)

		if err != nil {
			return fmt.Errorf("build client load balancer failed: %w", err)
		}
	} else {
		target = fmt.Sprintf("%s:///%s", "passthrough", endpointAddrs[0])
		loadBalancerPolicy = ""
	}

	// build dial options
	if opts, err := c.buildDialOptions(loadBalancerPolicy); err != nil {
		return fmt.Errorf("build gRPC client dial options failed: %w", err)
	} else {
		if c.BeforeClientDial != nil {
			if z := c.ZLog(); z != nil {
				z.Printf("Before gRPC Client Dial Begin...")
			}

			// CL-F5: invoke user callback with panic recovery.
			safeCall("BeforeClientDial", func() { c.BeforeClientDial(c) })

			if z := c.ZLog(); z != nil {
				z.Printf("... Before gRPC Client Dial End")
			}
		}

		dialSuccess := false
		defer func() {
			if dialSuccess && c.AfterClientDial != nil {
				if z := c.ZLog(); z != nil {
					z.Printf("After gRPC Client Dial Begin...")
				}

				// CL-F5: invoke user callback with panic recovery.
				safeCall("AfterClientDial", func() { c.AfterClientDial(c) })

				if z := c.ZLog(); z != nil {
					z.Printf("... After gRPC Client Dial End")
				}
			}
		}()

		if z := c.ZLog(); z != nil {
			z.Printf("Dialing gRPC Service @ " + target + "...")
		}

		dialSec := cfg.Grpc.DialMinConnectTimeout
		if dialSec == 0 {
			dialSec = 5
		}

		ctxWithTimeout, cancel := context.WithTimeout(ctx, time.Duration(dialSec)*time.Second)
		defer cancel()

		seg := xray.NewSegmentNullable("GrpcClient-Dial")
		if seg != nil {
			defer seg.Close()
		}

		conn, err := muxDialContext(ctxWithTimeout, target, opts...)
		if err != nil {
			if z := c.ZLog(); z != nil {
				z.Errorf("Dial Failed: (If TLS/mTLS, Check Certificate SAN) %s", err.Error())
			}
			e := fmt.Errorf("gRPC Client Dial Service Endpoint %s Failed: (If TLS/mTLS, Check Certificate SAN) %w", target, err)
			if seg != nil {
				xray.LogXrayAddFailure("Client", seg.SafeAddError(e))
			}
			return e
		}

		// dial grpc service endpoint success
		if z := c.ZLog(); z != nil {
			z.Printf("Dial Successful")
		}

		c.setConnection(conn, target)
		cleanupConn = true

		if z := c.ZLog(); z != nil {
			z.Printf("Remote Address = " + target)
		}

		if z := c.ZLog(); z != nil {
			z.Printf("... gRPC Service @ " + target + " [" + c.RemoteAddress() + "] Connected")
		}

		if c.WaitForServerReady {
			healthCtx, healthCancel := context.WithTimeout(ctx, time.Duration(dialSec)*time.Second)
			defer healthCancel() // FIX #32: Ensure cancel is always called, even on panic
			if e := c.waitForEndpointReady(healthCtx, time.Duration(dialSec)*time.Second); e != nil {
				// health probe failed
				if closedConn := c.clearConnection(); closedConn != nil {
					_ = closedConn.Close()
				}
				cleanupConn = false
				if seg != nil {
					xray.LogXrayAddFailure("Client", seg.SafeAddError(fmt.Errorf("gRPC service server not ready: %w", e)))
				}
				return fmt.Errorf("gRPC service server not ready: %w", e)
			}
		}

		// dial successful, now start web server for notification callbacks (webhook)
		if c.WebServerConfig != nil && util.LenTrim(c.WebServerConfig.ConfigFileName) > 0 {
			if z := c.ZLog(); z != nil {
				z.Printf("Starting Http Web Server...")
			}
			serveErrCh := make(chan error, 1)

			// start server and capture immediate config/start errors
			if err := c.startWebServer(serveErrCh); err != nil {
				if seg != nil {
					xray.LogXrayAddFailure("Client", seg.SafeAddError(fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, err)))
				}
				return fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, err)
			}
			cleanupWeb = true

			// wait for readiness first; propagate deterministic readiness failures
			if e := c.waitForWebServerReady(ctx, time.Duration(cfg.Target.SdTimeout)*time.Second); e != nil {
				if z := c.ZLog(); z != nil {
					z.Errorf("!!! Http Web Server %s Failed: %s !!!", c.WebServerConfig.AppName, e)
				}
				if seg != nil {
					xray.LogXrayAddFailure("Client", seg.SafeAddError(fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, e)))
				}
				return e
			}

			// after readiness, non-blocking capture any immediate Serve error if it already occurred
			consumed := false
			select {
			case webErr := <-serveErrCh:
				consumed = true
				if webErr != nil {
					if seg != nil {
						xray.LogXrayAddFailure("Client", seg.SafeAddError(fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, webErr)))
					}
					return fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, webErr)
				}
			default:
			}

			// keep observing the server goroutine so late startup failures are not silent
			if !consumed {
				// CL-F5: safeGo so a panic on late errCh delivery does
				// not crash the process.
				// A3-F2: select on closedCh so the observer exits when the
				// client is closed, instead of leaking forever if the
				// webserver never sends on errCh.
				safego.Go("webserver-errch-observer", func() {
					select {
					case webErr := <-serveErrCh:
						if webErr != nil {
							if z := c.ZLog(); z != nil {
								z.Errorf("Http Web Server %s exited: %v", c.WebServerConfig.AppName, webErr)
							} else {
								log.Printf("Http Web Server %s exited: %v", c.WebServerConfig.AppName, webErr)
							}
						}
					case <-c.getClosedCh():
						// Client is closing; stop observing the webserver error channel.
					}
				})
			}

			if z := c.ZLog(); z != nil {
				z.Printf("... Http Web Server Started: %s", c.WebServerConfig.WebServerLocalAddress)
			}
		}

		//
		// dial completed
		//
		cleanupConn = false // success, keep connection open
		cleanupWeb = false
		cleanupSd = false
		cleanupSqs = false
		dialSuccess = true
		return nil
	}
}

func muxDialContext(ctx context.Context, target string, opts ...grpc.DialOption) (conn *grpc.ClientConn, err error) {
	_mux.Lock()
	defer _mux.Unlock()

	// P1-CONN-CL-A (2026-04-15): SA1019 deferred to connector v2.0.0.
	// grpc.NewClient uses lazy-dial semantics — it returns before the
	// connection is established — while grpc.DialContext with
	// WithBlock is eager. Switching here without migrating
	// grpc.WithBlock / FailOnNonTempDialError at the same time would
	// break every consumer that expects "Dial returns means
	// connection is up". Coordinated v2.0.0 migration required.
	return grpc.DialContext(ctx, target, opts...) //nolint:staticcheck // SA1019 deferred to v2.0.0
}

// GetLiveEndpointsCount queries cloudmap to retrieve live endpoints count,
// optionally update endpoints into client cache
//
// if updateEndpointsToLoadBalanceResolver = true, then endpoint addresses will force refresh from cloudmap
func (c *Client) GetLiveEndpointsCount(updateEndpointsToLoadBalanceResolver bool) (int, error) {
	if c == nil {
		return 0, fmt.Errorf("Client Object Nil")
	}
	if c.closed.Load() { // guard against use-after-close
		return 0, fmt.Errorf("GetLiveEndpointsCount requires open client")
	}

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}
	errorf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Errorf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	// CL-F4: snapshot config once — all subsequent dereferences use cfg.
	cfg := c.getConfig()
	if cfg == nil {
		return 0, fmt.Errorf("Config Data Not Loaded")
	}

	c.connMu.RLock()
	connReady := c._conn != nil
	connState := connectivity.Shutdown
	if c._conn != nil {
		connState = c._conn.GetState()
	}
	c.connMu.RUnlock()

	if !connReady || connState == connectivity.Shutdown {
		errorf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Requires Current Client Connection Already Established First",
			cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)
		return 0, fmt.Errorf("GetLiveEndpointsCount requires current client connection already established first")
	}

	if cfg.Target.ServiceDiscoveryType == "direct" {
		count := len(c.endpointsSnapshot())
		printf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Aborted: Service Discovery Type is Direct = %d",
			cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName, count)
		return count, nil
	}

	printf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Started...", cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)

	forceRefresh := len(c.endpointsSnapshot()) == 0 || updateEndpointsToLoadBalanceResolver

	if e := c.discoverEndpoints(forceRefresh); e != nil {
		s := fmt.Sprintf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Failed: (Discover Endpoints From Cloudmap) %s",
			cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName, e.Error())
		errorf("%s", s)
		return 0, errors.New(s)
	}

	eps := c.endpointsSnapshot()
	if len(eps) == 0 {
		s := fmt.Sprintf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Failed: (Discover Endpoints From Cloudmap) No Live Endpoints",
			cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)
		errorf("%s", s)
		return 0, errors.New(s)
	}

	if updateEndpointsToLoadBalanceResolver {
		// get endpoint addresses
		endpointAddrs := []string{}

		for i, ep := range eps {
			endpointAddrs = append(endpointAddrs, fmt.Sprintf("%s:%s", ep.Host, util.UintToStr(ep.Port)))

			info := strconv.Itoa(i+1) + ") "
			info += ep.SdType + "=" + ep.Host + ":" + util.UintToStr(ep.Port) + ", "
			info += "Version=" + ep.Version + ", "
			info += "CacheExpires=" + util.FormatDateTime(ep.CacheExpire)

			printf("%s", "       - "+info)
		}

		if len(endpointAddrs) == 0 {
			s := fmt.Sprintf("GetLiveEndpointsCount-UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Aborted: Endpoint Addresses Required",
				cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)
			errorf("%s", s)
			return 0, errors.New(s)
		}

		// update load balance resolver with new endpoint addresses
		serviceName := fmt.Sprintf("%s.%s", cfg.Target.ServiceName, cfg.Target.NamespaceName)
		schemeName, _ := util.ExtractAlpha(cfg.AppName)
		schemeName = strings.ToLower("clb" + schemeName)

		if e := res.UpdateManualResolver(schemeName, serviceName, endpointAddrs); e != nil {
			errorf("GetLiveEndpointsCount-UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Failed: %s",
				cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName, e.Error())
			return 0, e
		}

		printf("GetLiveEndpointsCount-UpdateLoadBalanceResolver for Client %s with Service '%s.%s' OK",
			cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)
	}

	printf("GetLiveEndpointsCount for Client %s with Service '%s.%s' OK",
		cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)
	return len(eps), nil
}

// UpdateLoadBalanceResolves updates client load balancer resolver state with new endpoint addresses
func (c *Client) UpdateLoadBalanceResolver() error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}
	if c.closed.Load() { // guard against use-after-close
		return fmt.Errorf("UpdateLoadBalanceResolver requires open client")
	}

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}
	errorf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Errorf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	// CL-F4: snapshot config once — all subsequent dereferences use cfg.
	cfg := c.getConfig()
	if cfg == nil {
		return fmt.Errorf("Config Data Not Loaded")
	}

	c.connMu.RLock()
	connReady := c._conn != nil
	connState := connectivity.Shutdown
	if c._conn != nil {
		connState = c._conn.GetState()
	}
	c.connMu.RUnlock()

	if !connReady || connState == connectivity.Shutdown {
		errorf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Requires Current Client Connection Already Established First",
			cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)
		return fmt.Errorf("UpdateLoadBalanceResolver Requires Current Client Connection Already Established First")
	}

	if cfg.Target.ServiceDiscoveryType == "direct" {
		printf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Aborted: Service Discovery Type is Direct",
			cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)
		return nil
	}

	printf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Started...",
		cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)

	eps := c.endpointsSnapshot()
	if len(eps) == 0 {
		if e := c.discoverEndpoints(false); e != nil {
			s := fmt.Sprintf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Failed: (Discover Endpoints From Cloudmap) %s",
				cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName, e.Error())
			errorf("%s", s)
			return errors.New(s)
		}
		// refresh snapshot after discovery to use newly populated endpoints
		eps = c.endpointsSnapshot()
	}

	if len(eps) == 0 {
		s := fmt.Sprintf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Aborted: Endpoint Addresses Required",
			cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)
		errorf("%s", s)
		return errors.New(s)
	}

	// get endpoint addresses
	endpointAddrs := []string{}
	for i, ep := range eps {
		endpointAddrs = append(endpointAddrs, fmt.Sprintf("%s:%s", ep.Host, util.UintToStr(ep.Port)))

		info := strconv.Itoa(i+1) + ") "
		info += ep.SdType + "=" + ep.Host + ":" + util.UintToStr(ep.Port) + ", "
		info += "Version=" + ep.Version + ", "
		info += "CacheExpires=" + util.FormatDateTime(ep.CacheExpire)

		printf("%s", "       - "+info)
	}

	// update load balance resolver with new endpoint addresses
	serviceName := fmt.Sprintf("%s.%s", cfg.Target.ServiceName, cfg.Target.NamespaceName)

	schemeName, _ := util.ExtractAlpha(cfg.AppName)
	schemeName = strings.ToLower("clb" + schemeName)

	if e := res.UpdateManualResolver(schemeName, serviceName, endpointAddrs); e != nil {
		errorf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Failed: %s",
			cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName, e.Error())
		return e
	}

	printf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' OK",
		cfg.AppName, cfg.Target.ServiceName, cfg.Target.NamespaceName)
	return nil
}

// DoNotifierAlertService should be called from goroutine after the client dial completes,
// this service is to subscribe and receive callbacks from notifier server of service host online offline statuses
//
// Example:
//
//	go func() {
//				  svc1Cli.DoNotifierAlertService()
//			  }()
func (c *Client) DoNotifierAlertService() (err error) {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	// Fail fast if the client is closed to avoid doing work under lock.
	if c.closed.Load() {
		return fmt.Errorf("Client is closed")
	}

	c.notifierStartMu.Lock()
	defer c.notifierStartMu.Unlock()

	// if the client is closed, eagerly clean up any existing notifier client to avoid leaks
	if c.closed.Load() {
		if existing := c.getNotifierClient(); existing != nil {
			existing.Close()
			c.setNotifierClient(nil)
		}
		return fmt.Errorf("Client is closed")
	}

	// CL-F4: guard against nil config (prevents panic if called before Dial)
	if c.getConfig() == nil {
		return fmt.Errorf("Client config not loaded; call Dial() first")
	}

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}
	errorf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Errorf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	nc := c.getNotifierClient()
	doConnection := nc != nil

	// Always verify notifier config file exists; if missing, close and clear any stale client.
	cfgPath := path.Join(c.CustomConfigPath, c.ConfigFileName+"-notifier-client.yaml")
	if !util.FileExists(cfgPath) {
		if nc != nil {
			nc.Close()
			c.setNotifierClient(nil)
		}
		printf("### Notifier Client Service Skipped, config file missing: %s ###", cfgPath)
		return nil
	}

	if !doConnection {
		nc = NewNotifierClient(c.AppName+"-Notifier-Client", c.ConfigFileName+"-notifier-client", c.CustomConfigPath)
		c.setNotifierClient(nc)
	}

	// finally, run notifier client to subscribe for notification callbacks
	// the notifier client uses the same client config yaml, but a copy of it to keep the scope separated
	// within the notifier client config yaml, named xyz-notifier-client.yaml, where xyz is the endpoint service name,
	// the discovery topicArn as pre-created on aws is stored within, this enables callback for this specific topicArn from notifier server,
	// note that the service discovery of notifier client is to the notifier server cluster

	// always (re)wire handlers, even on reconnects
	c.configureNotifierHandlers(nc)

	// Distinguish missing notifier client from closed state.
	if nc == nil {
		return fmt.Errorf("Notifier client not initialized (missing config)")
	}
	if c.closed.Load() {
		nc.Close()
		c.setNotifierClient(nil)
		return fmt.Errorf("Client is closed")
	}

	// guard against empty discovery topic ARN to avoid failed subscribes and dangling client
	arn := nc.ConfiguredSNSDiscoveryTopicArn()
	if !nc.ConfiguredForNotifierClientDial() || util.LenTrim(arn) == 0 {
		// ensure an existing notifier client is cleanly closed instead of silently dropped
		if nc != nil {
			nc.Close()
		}
		c.setNotifierClient(nil) // avoid keeping a half-initialized notifier client
		printf("### Notifier Client Service Skipped, Not Yet Configured for Dial or TopicArn Missing ###")
		return nil
	}

	// dial notifier client to notifier server endpoint and begin service operations
	if err = nc.Dial(); err != nil {
		if nc != nil {
			errorf("!!! Notifier Client Service Dial Failed: %s !!!", err.Error())
			nc.Close()
		}
		c.setNotifierClient(nil)
		return err
	}

	if c.closed.Load() { // abort if client was closed during/after dial
		nc.Close()
		c.setNotifierClient(nil)
		return fmt.Errorf("Client is closed")
	}

	// CL-F1: bridge Client.Close() -> nc.Close() for this Subscribe call.
	// This closes the TOCTOU window where the reconnect goroutine's
	// setNotifierClient(nil) is briefly visible to Close()'s direct
	// getNotifierClient path, after which we store the new nc here and
	// block in Subscribe. The watchdog lives for exactly the duration of
	// this Subscribe call: defer close(watchdogDone) terminates it on the
	// Subscribe-returns-normally path, and closedCh firing terminates it
	// via nc.Close() -> cancel of the in-flight Recv loop on the closed
	// path. nc.Close() is idempotent (NotifierClient guards via
	// _notificationServicesStarted), and re-entry into Client.Close()
	// through this path is a no-op because Client.Close() already holds
	// the single-shot CAS on c.closed.
	// C3-001: wrap watchdog in safeGo so a panic inside nc.Close()
	// (grpc.ClientConn.Close edge cases, see CL-F5 comment at line 203)
	// cannot crash the process. Preserves CL-F5 invariant at line 803.
	watchdogDone := make(chan struct{})
	safego.Go("subscribe-close-on-closed-watchdog", func() {
		closeOnClosed(c.getClosedCh(), watchdogDone, func() {
			if nc != nil {
				nc.Close()
			}
		})
	})
	defer close(watchdogDone)

	if err = nc.Subscribe(arn); err != nil {
		if nc != nil {
			errorf("!!! Notifier Client Service Subscribe Failed: %s !!!", err.Error())
			nc.Close()
		}
		c.setNotifierClient(nil)
		return err
	}

	if c.closed.Load() { // abort if client closed after subscribe
		// Issue #10: Add nil check before calling methods on nc
		if nc != nil {
			_ = nc.Unsubscribe() // ignore error during cleanup
			nc.Close()
		}
		c.setNotifierClient(nil)
		return fmt.Errorf("Client is closed")
	}

	printf("~~~ Notifier Client Service Started ~~~")
	return nil
}

// waitForWebServerReady is called after web server is expected to start,
// this function will wait a short time for web server startup success or timeout
func (c *Client) waitForWebServerReady(ctx context.Context, timeoutDuration ...time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}
	if c.WebServerConfig == nil { // guard nil config to avoid panic
		return fmt.Errorf("Web Server Config Not Set")
	}
	if c.closed.Load() { // stop probing after client close
		return fmt.Errorf("Web Server Health Check Failed: client is closed")
	}

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}
	warnf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Warnf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	if util.LenTrim(c.WebServerConfig.WebServerLocalAddress) == 0 {
		return fmt.Errorf("Web Server Host Address is Empty")
	}

	timeout := 5 * time.Second
	if len(timeoutDuration) > 0 && timeoutDuration[0] > 0 {
		timeout = timeoutDuration[0]
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(defaultWebServerCheckInterval)
	defer ticker.Stop()

	healthUrl := c.WebServerConfig.WebServerLocalAddress + "/health"

	for {
		// unified cancellation / timeout check before each attempt.
		if c.closed.Load() {
			return fmt.Errorf("Web Server Health Check Failed: client is closed")
		}
		if time.Now().After(deadline) {
			warnf("Web Server Health Check Timeout")
			return fmt.Errorf("Web Server Health Check Failed: Timeout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		remaining := time.Until(deadline)
		perAttempt := remaining
		if perAttempt > 2*time.Second {
			perAttempt = 2 * time.Second
		}

		reqCtx, cancel := context.WithTimeout(ctx, perAttempt)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, healthUrl, nil)
		if err != nil {
			cancel()
			warnf("Web Server Health Check Failed: %s", err.Error())
			continue
		}

		resp, e := (&http.Client{Timeout: perAttempt}).Do(req)
		if resp != nil {
			if cerr := resp.Body.Close(); cerr != nil {
				warnf("Web Server Health Check Body.Close Failed: %s", cerr.Error())
			}
		}
		cancel()

		if e != nil {
			warnf("Web Server Health Check Failed: %s", e.Error())
			continue
		}
		if resp != nil && resp.StatusCode == 200 {
			printf("Web Server Health OK")
			return nil
		}

		// non-200 is also retried until timeout instead of failing fast
		statusCode := 0
		if resp != nil {
			statusCode = resp.StatusCode
		}
		warnf("Web Server Not Ready (status %d), retrying...", statusCode)
	}
}

// waitForEndpointReady is called after Dial to check if target service is ready as reported by health probe
func (c *Client) waitForEndpointReady(ctx context.Context, timeoutDuration ...time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}
	if c.closed.Load() { // avoid waiting when client already closed
		return fmt.Errorf("Health Status Check Failed: client is closed")
	}

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}
	warnf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Warnf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	// default health-check target to configured service name when available
	// CL-F4: snapshot to avoid two-load TOCTOU.
	serviceName := ""
	if cfg := c.getConfig(); cfg != nil && util.LenTrim(cfg.Target.ServiceName) > 0 {
		serviceName = cfg.Target.ServiceName
	}

	timeout := 5 * time.Second
	if len(timeoutDuration) > 0 {
		timeout = timeoutDuration[0]
	}

	deadline := time.Now().Add(timeout)
	interval := defaultHealthCheckInterval
	maxPerAttempt := 2 * time.Second

	for {
		// cancellation/timeout checks each loop.
		if c.closed.Load() {
			return fmt.Errorf("Health Status Check Failed: client is closed")
		}
		if time.Now().After(deadline) {
			warnf("Health Status Check Timeout")
			return fmt.Errorf("Health Status Check Failed: Timeout")
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		c.connMu.RLock()
		conn := c._conn
		state := connectivity.Shutdown
		if conn != nil {
			state = conn.GetState()
		}
		c.connMu.RUnlock()

		if conn == nil {
			return fmt.Errorf("Health Status Check Failed: client connection is nil")
		}
		if state == connectivity.Shutdown {
			return fmt.Errorf("Health Status Check Failed: client connection is shutdown")
		}
		if state == connectivity.TransientFailure {
			warnf("Health Status Check: connection in transient failure; retrying until deadline...")
			// FIX: Use time.NewTimer instead of time.After to avoid goroutine leak
			// when ctx.Done or closed fires first.
			// P1-5: Use persistent closedCh rather than an IIFE that was
			// evaluated once at select entry — the IIFE couldn't observe
			// a close() that happened *after* the select started.
			backoffTimer := time.NewTimer(interval)
			closedCh := c.getClosedCh()
			select {
			case <-ctx.Done():
				backoffTimer.Stop()
				return ctx.Err()
			case <-backoffTimer.C:
			case <-closedCh:
				backoffTimer.Stop()
				return fmt.Errorf("Health Status Check Failed: client is closed")
			}
			continue
		}

		remaining := time.Until(deadline)
		perAttempt := remaining
		if perAttempt > maxPerAttempt {
			perAttempt = maxPerAttempt
		}

		if status, e := c.HealthProbe(serviceName, perAttempt); e != nil {
			warnf("Health Status Check Not Ready Yet: %s", e.Error())
		} else if status == grpc_health_v1.HealthCheckResponse_SERVING {
			printf("Serving Status Detected")
			printf("Heath Status Check Finalized...")
			return nil
		} else {
			warnf("Not Serving!")
		}

		sleep := interval
		if sleep > remaining {
			sleep = remaining
		}

		// FIX: Use time.NewTimer instead of time.After to avoid goroutine leak
		// P1-5: Use persistent closedCh instead of IIFE — closes after
		// select entry are now observed immediately.
		sleepTimer := time.NewTimer(sleep)
		closedCh := c.getClosedCh()
		select { // honor ctx/closed during sleep
		case <-ctx.Done():
			sleepTimer.Stop()
			return ctx.Err()
		case <-sleepTimer.C:
		case <-closedCh:
			sleepTimer.Stop()
			return fmt.Errorf("Health Status Check Failed: client is closed")
		}
	}
}

// setupHealthManualChecker sets up the HealthChecker for manual use by HealthProbe method
func (c *Client) setupHealthManualChecker() {
	if c == nil {
		log.Println("setupHealthManualChecker(): Client Object Nil")
		return
	}

	c.connMu.Lock()
	conn := c._conn
	if conn == nil {
		c.connMu.Unlock()
		return
	}

	// handle potential init error; keep state consistent
	hc, err := health.NewHealthClient(conn)
	if err != nil {
		c._healthManualChecker = nil
		if z := c.ZLog(); z != nil {
			z.Errorf("Init health client failed: %v", err)
		} else {
			log.Printf("Init health client failed: %v", err)
		}
		c.connMu.Unlock()
		return
	}
	c._healthManualChecker = hc
	c.connMu.Unlock()
}

// HealthProbe manually checks service serving health status
func (c *Client) HealthProbe(serviceName string, timeoutDuration ...time.Duration) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
	if c == nil {
		return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Client Object Nil")
	}

	if c.closed.Load() {
		return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Health Probe Failed: client is closed")
	}

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	// A3-F1: capture hc and conn atomically under RLock. The snapshot may
	// become stale if setConnection rotates the connection concurrently,
	// but hc.Check() will return an appropriate gRPC error in that case
	// (the old connection enters Shutdown state). Callers should retry on
	// transient errors. Holding RLock through the Check() call is avoided
	// because Check() performs a blocking RPC and would starve writers.
	c.connMu.RLock()
	hc := c._healthManualChecker
	conn := c._conn
	c.connMu.RUnlock()

	if conn == nil {
		return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Health Probe Failed: client connection is nil")
	}
	if conn.GetState() == connectivity.Shutdown { // guard against shutdown connections
		return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Health Probe Failed: client connection is shutdown")
	}

	if hc == nil {
		// conn is guaranteed non-nil here (checked above)
		c.setupHealthManualChecker()

		// A3-F1: re-snapshot hc AND conn under a single RLock to ensure the
		// health checker we use belongs to the current connection. If a
		// rotation happened between the first snapshot and this one, we get
		// the new hc for the new conn, not a stale hc for the old conn.
		c.connMu.RLock()
		hc = c._healthManualChecker
		conn = c._conn
		c.connMu.RUnlock()

		if hc == nil {
			return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Health Probe Failed: (Auto Instantiate) %s", "Health Manual Checker is Nil")
		}
		// Re-check conn after re-snapshot since rotation may have closed it.
		if conn == nil || conn.GetState() == connectivity.Shutdown {
			return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Health Probe Failed: connection rotated during health check setup")
		}
	}

	printf("Health Probe - Manual Check Begin...")
	defer printf("... Health Probe - Manual Check End")

	status, err := hc.Check(serviceName, timeoutDuration...)
	if err != nil {
		return status, fmt.Errorf("Health Probe Failed: %w", err) // preserve root cause
	}
	return status, nil
}

// GetState returns the current grpc client connection's state
func (c *Client) GetState() connectivity.State {
	if c == nil {
		log.Println("GetState(): Client Object Nil")
		return connectivity.Shutdown
	}

	c.connMu.RLock()
	conn := c._conn
	c.connMu.RUnlock()
	if conn != nil {
		return conn.GetState()
	} else {
		return connectivity.Shutdown
	}
}

// Close tears down the Client: it sets the closed flag, signals the
// notifier reconnect goroutine (if running) to exit on its next
// iteration, closes the gRPC connection (after a short
// ConnectionCloseDelay drain window), and stops the optional sidecar
// HTTP server.
//
// Close is idempotent — calling it more than once on the same Client
// is safe but only the first call has effect.
//
// Note: Close does NOT eagerly clear the notifierReconnectActive
// flag (see P2-13). The reconnect goroutine clears that flag itself
// from its own defer. A subsequent Dial on the same Client drains
// any in-flight notifier work before flipping the closed flag back
// to false to avoid spawning a second reconnect goroutine that races
// with a still-running stale one.
func (c *Client) Close() {
	if c == nil {
		log.Println("Close(): Client Object Nil")
		return
	}

	// make Close idempotent to avoid double teardown in concurrent/duplicate calls.
	// This fast path intentionally sits OUTSIDE _lifecycleMu so a second
	// concurrent Close returns immediately instead of waiting on the
	// first caller's teardown.
	if !c.closing.CompareAndSwap(false, true) {
		return
	}

	// CL-F3: serialize Close against Dial for this Client. If a Dial is
	// currently spinning up fresh state (closed already flipped to
	// false, TCP dial in flight), we wait for it to finish before tearing
	// down. This prevents the leak where Close observes a half-initialized
	// client and nil's out only part of the state that Dial is about to
	// set. Note: the closing CAS above still makes duplicate Close calls
	// return without contending for this lock.
	c._lifecycleMu.Lock()
	defer c._lifecycleMu.Unlock()

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}
	errorf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Errorf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	if c.BeforeClientClose != nil {
		printf("Before gRPC Client Close Begin...")
		// CL-F5: invoke user callback with panic recovery.
		safeCall("BeforeClientClose", func() { c.BeforeClientClose(c) })
		printf("... Before gRPC Client Close End")
	}

	// now mark closed; this prevents use-after-close while keeping hook observable.
	if !c.closed.CompareAndSwap(false, true) {
		// already closed by another goroutine after the hook finished
		return
	}
	// P1-5: fire closedCh so any goroutine blocked in a select (reconnect
	// loop, health-check retry) wakes immediately instead of waiting for
	// the next timer tick or loop iteration. Must be after the CAS so
	// observers that wake on the channel also see c.closed.Load() == true.
	c.markClosedCh()

	defer func() {
		if c.AfterClientClose != nil {
			printf("After gRPC Client Close Begin...")
			// CL-F5: invoke user callback with panic recovery.
			safeCall("AfterClientClose", func() { c.AfterClientClose(c) })
			printf("... After gRPC Client Close End")
		}
	}()

	// clean up web server route53 dns and shutdown web server
	c.webServerMu.Lock()
	// CONN-R3-001: wrap with safeCall — user callback panic must not crash process
	if c.WebServerConfig != nil && c.WebServerConfig.CleanUp != nil {
		safeCall("WebServerConfig.CleanUp", c.WebServerConfig.CleanUp)
	}
	// Issue #18: Use shared shutdown helper
	c.shutdownWebServerLocked(webServerShutdownTimeout, false) // CleanUp already called above
	c.webServerMu.Unlock()

	// clean up notifier client connection
	if nc := c.getNotifierClient(); nc != nil { // use guarded getter to avoid races
		if nc.NotifierClientAlertServicesStarted() {
			if err := nc.Unsubscribe(); err != nil {
				errorf("%s", "!!! Notifier Client Alert Services Unsubscribe Failed: "+err.Error()+" !!!")
			}
		}

		nc.Close()
		c.setNotifierClient(nil) // synchronized setter
	}
	// P2-13: Do NOT eagerly clear notifierReconnectActive here. The reconnect
	// goroutine's own defer (client.go:588) sets it to false when it exits.
	// Force-clearing it here used to allow a subsequent Dial() to spawn a
	// fresh reconnect goroutine that races with a still-in-flight stale one
	// (which is uncancellable mid-DoNotifierAlertService). The drain loop at
	// the top of Dial() now waits for the stale goroutine's defer to fire.

	c.connMu.Lock()
	sqsLocal := c._sqs
	c._sqs = nil
	sdLocal := c._sd
	c._sd = nil
	c.connMu.Unlock()

	if sqsLocal != nil {
		sqsLocal.Disconnect()
	}

	if sdLocal != nil {
		sdLocal.Disconnect()
	}

	if conn := c.clearConnection(); conn != nil {
		_ = conn.Close()
	}

	// clear circuit breaker and endpoints state to avoid reuse across dials
	c.cbMu.Lock()
	c._circuitBreakers = nil
	c.cbMu.Unlock()
	c.setEndpoints(nil)
}

// ClientConnection returns the currently loaded grpc client connection
func (c *Client) ClientConnection() grpc.ClientConnInterface {
	if c == nil {
		log.Println("ClientConnection(): Client Object Nil")
		return nil
	}

	c.connMu.RLock()
	conn := c._conn
	c.connMu.RUnlock()
	return conn
}

// RemoteAddress gets the remote endpoint address currently connected to
func (c *Client) RemoteAddress() string {
	if c == nil {
		log.Println("RemoteAddress(): Client Object Nil")
		return ""
	}

	c.connMu.RLock()
	addr := c._remoteAddress
	c.connMu.RUnlock()
	return addr
}

// connectSd will try to establish service discovery object to struct
func (c *Client) connectSd() error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	// CL-F4: snapshot config — guard against missing config to avoid nil deref when connectSd is called directly.
	cfg := c.getConfig()
	if cfg == nil {
		return fmt.Errorf("Config Data Not Loaded")
	}

	// Protect _sd reads/writes with connMu to prevent races with Close() and discovery functions
	c.connMu.Lock()
	oldSd := c._sd
	c._sd = nil
	c.connMu.Unlock()
	if oldSd != nil {
		oldSd.Disconnect()
	}

	// skip CloudMap wiring when using direct discovery to avoid unnecessary AWS dependency
	if strings.EqualFold(cfg.Target.ServiceDiscoveryType, "direct") {
		return nil
	}

	if util.LenTrim(cfg.Target.NamespaceName) > 0 && util.LenTrim(cfg.Target.ServiceName) > 0 && util.LenTrim(cfg.Target.Region) > 0 {
		cm := &cloudmap.CloudMap{
			AwsRegion: awsregion.GetAwsRegion(cfg.Target.Region),
		}
		if err := cm.Connect(); err != nil {
			return fmt.Errorf("Connect SD Failed: %w", err)
		}
		c.connMu.Lock()
		c._sd = cm
		c.connMu.Unlock()
	}

	return nil
}

// discoverEndpoints uses srv, a, api, or direct to query endpoints
func (c *Client) discoverEndpoints(forceRefresh bool) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	// CL-F4: snapshot config once.
	cfg := c.getConfig()
	if cfg == nil {
		return fmt.Errorf("Config Data Not Loaded")
	}

	cacheExpSeconds := cfg.Target.SdEndpointCacheExpires
	if cacheExpSeconds == 0 {
		cacheExpSeconds = defaultCacheExpireSeconds
	}

	cacheExpires := time.Now().Add(time.Duration(cacheExpSeconds) * time.Second)

	var err error
	switch cfg.Target.ServiceDiscoveryType {
	case "direct":
		err = c.setDirectConnectEndpoint(cacheExpires, cfg.Target.DirectConnectIpPort)
	case "srv":
		fallthrough
	case "a":
		err = c.setDnsDiscoveredIpPorts(cacheExpires, cfg.Target.ServiceDiscoveryType == "srv", cfg.Target.ServiceName,
			cfg.Target.NamespaceName, cfg.Target.InstancePort, forceRefresh)
	case "api":
		err = c.setApiDiscoveredIpPorts(cacheExpires, cfg.Target.ServiceName, cfg.Target.NamespaceName, cfg.Target.InstanceVersion,
			int64(cfg.Target.SdInstanceMaxResult), cfg.Target.SdTimeout, forceRefresh)
	default:
		err = fmt.Errorf("unexpected service discovery type: %s", cfg.Target.ServiceDiscoveryType)
	}

	if err != nil {
		c.setEndpoints(nil) // drop stale endpoints on discovery failure
		return err
	}
	return nil
}

func (c *Client) setDirectConnectEndpoint(cacheExpires time.Time, directIpPort string) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	// normalize input to avoid surprises with trailing spaces
	directIpPort = strings.TrimSpace(directIpPort)
	if len(directIpPort) == 0 {
		return fmt.Errorf("Direct Connect target must include host and port (got empty)")
	}

	host, portStr, err := net.SplitHostPort(directIpPort)
	if err != nil {
		return fmt.Errorf("Direct Connect target must include host and port (got %q): %w", directIpPort, err)
	}

	host = strings.TrimSpace(host)
	if util.LenTrim(host) == 0 { // reject empty host early
		return fmt.Errorf("Direct Connect target host is empty (got %q)", directIpPort)
	}

	p, convErr := strconv.Atoi(strings.TrimSpace(portStr))
	if convErr != nil {
		return fmt.Errorf("Direct Connect port invalid (%q): %w", portStr, convErr)
	}
	if p <= 0 || p > 65535 {
		return fmt.Errorf("Direct Connect port out of range (1-65535): %d", p)
	}

	c.setEndpoints([]*serviceEndpoint{
		{
			SdType:      "direct",
			Host:        host,
			Port:        uint(p),
			CacheExpire: cacheExpires,
		},
	})

	return nil
}

func (c *Client) setDnsDiscoveredIpPorts(cacheExpires time.Time, srv bool, serviceName string, namespaceName string, instancePort uint, forceRefresh bool) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("Service Name Not Defined in Config (SRV / A SD)")
	}

	if util.LenTrim(namespaceName) == 0 {
		return fmt.Errorf("Namespace Name Not Defined in Config (SRV / A SD)")
	}

	if !srv && instancePort == 0 {
		return fmt.Errorf("Instance Port Required in Config When Service Discovery Type is DNS Record Type A")
	}

	serviceName = strings.ToLower(serviceName)
	namespaceName = strings.ToLower(namespaceName)

	//
	// check for existing cache
	//
	found := pruneExpiredEndpoints(cacheGetLiveServiceEndpoints(serviceName+"."+namespaceName, "", forceRefresh))
	if len(found) > 0 {
		c.setEndpoints(found)
		if z != nil {
			z.Printf("Using DNS Discovered Cache Hosts: (Service) %s.%s", serviceName, namespaceName)
		} else {
			log.Printf("Using DNS Discovered Cache Hosts: (Service) %s.%s", serviceName, namespaceName)
		}
		for _, v := range c.endpointsSnapshot() {
			printf("%s", "   - "+v.Host+":"+util.UintToStr(v.Port)+", Cache Expires: "+util.FormatDateTime(v.CacheExpire))
		}
		return nil
	}

	//
	// acquire dns ip port from service discovery
	//
	log.Printf("Start DiscoverDnsIps %s.%s SRV=%v", serviceName, namespaceName, srv)
	ipList, err := registry.DiscoverDnsIps(serviceName+"."+namespaceName, srv)
	if err != nil {
		return fmt.Errorf("service discovery by DNS failed: %w", err)
	}

	sdType := "a"
	if srv {
		sdType = "srv"
	}

	seen := make(map[string]struct{})
	endpoints := make([]*serviceEndpoint, 0, len(ipList))
	for _, v := range ipList {
		var host string
		var port uint

		if srv {
			// srv
			h, pStr, splitErr := net.SplitHostPort(v)
			if splitErr != nil {
				// Issue #14: Warn and skip invalid entry instead of failing entire operation
				if z != nil {
					z.Warnf("Skipping invalid SRV entry %q: %v", v, splitErr)
				} else {
					log.Printf("WARNING: Skipping invalid SRV entry %q: %v", v, splitErr)
				}
				continue
			}
			pInt, convErr := strconv.Atoi(pStr)
			if convErr != nil || pInt <= 0 || pInt > 65535 {
				// Issue #14: Warn and skip invalid port instead of failing entire operation
				if z != nil {
					z.Warnf("Skipping invalid SRV port %q from %q: %v", pStr, v, convErr)
				} else {
					log.Printf("WARNING: Skipping invalid SRV port %q from %q: %v", pStr, v, convErr)
				}
				continue
			}
			host = strings.TrimSuffix(strings.ToLower(h), ".")
			port = uint(pInt)
		} else {
			// a
			host = v
			// Validate that A records resolve to IPs; reject CNAME/malformed answers.
			if net.ParseIP(host) == nil {
				// Issue #14: Warn and skip non-IP host instead of failing entire operation
				if z != nil {
					z.Warnf("Skipping non-IP A record entry: %q", host)
				} else {
					log.Printf("WARNING: Skipping non-IP A record entry: %q", host)
				}
				continue
			}
			if instancePort == 0 || instancePort > 65535 {
				// This is a config error, not a per-entry issue, so still fail
				return fmt.Errorf("Configured Instance Port Not Valid: %d", instancePort)
			}
			port = instancePort
		}

		key := fmt.Sprintf("%s:%d", host, port)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		endpoints = append(endpoints, &serviceEndpoint{
			SdType:      sdType,
			Host:        host,
			Port:        port,
			CacheExpire: cacheExpires,
		})
	}

	live := pruneExpiredEndpoints(endpoints)
	if len(live) == 0 { // fail fast on empty discovery result
		return fmt.Errorf("Service Discovery By DNS returned no endpoints for %s.%s (srv=%v)", serviceName, namespaceName, srv)
	}

	c.setEndpoints(live)
	cacheAddServiceEndpoints(serviceName+"."+namespaceName, live)

	return nil
}

func (c *Client) setApiDiscoveredIpPorts(cacheExpires time.Time, serviceName string, namespaceName string, version string, maxCount int64, timeoutSeconds uint, forceRefresh bool) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	// A3-F4: _sd is read under connMu.RLock and used after release.
	// This is lifecycle-safe: _sd is set once during Dial (serialized
	// by _lifecycleMu) and cleared only during Close (also serialized).
	// The snapshot cannot become stale during normal operation. The
	// connMu guard here prevents a data race with Close's teardown.
	c.connMu.RLock()
	sd := c._sd
	c.connMu.RUnlock()

	if sd == nil {
		return fmt.Errorf("Service Discovery Client Not Connected")
	}

	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("Service Name Not Defined in Config (API SD)")
	}

	if util.LenTrim(namespaceName) == 0 {
		return fmt.Errorf("Namespace Name Not Defined in Config (API SD)")
	}

	serviceName = strings.ToLower(serviceName)
	namespaceName = strings.ToLower(namespaceName)

	//
	// check for existing cache
	//
	found := pruneExpiredEndpoints(cacheGetLiveServiceEndpoints(serviceName+"."+namespaceName, version, forceRefresh))
	if len(found) > 0 {
		c.setEndpoints(found)
		printf("%s", "Using API Discovered Cache Hosts: (Service) "+serviceName+"."+namespaceName)
		for _, v := range c.endpointsSnapshot() {
			printf("%s", "   - "+v.Host+":"+util.UintToStr(v.Port)+", Cache Expires: "+util.FormatDateTime(v.CacheExpire))
		}
		return nil
	}

	//
	// acquire api ip port from service discovery
	//
	if maxCount <= 0 {
		maxCount = 100
	}

	var timeoutDuration []time.Duration
	if timeoutSeconds > 0 {
		timeoutDuration = append(timeoutDuration, time.Duration(timeoutSeconds)*time.Second)
	}

	customAttr := map[string]string{}
	if util.LenTrim(version) > 0 {
		customAttr["INSTANCE_VERSION"] = version
	} else {
		customAttr = nil
	}

	log.Printf("Start DiscoverInstances %s.%s attr=%v count=%d", serviceName, namespaceName, customAttr, maxCount)
	instanceList, err := registry.DiscoverInstances(sd, serviceName, namespaceName, true, customAttr, &maxCount, timeoutDuration...)
	if err != nil {
		return fmt.Errorf("service discovery by API failed: %w", err)
	}

	seen := make(map[string]struct{})
	endpoints := make([]*serviceEndpoint, 0, len(instanceList))
	for _, v := range instanceList {
		// validate IP and port
		if util.LenTrim(v.InstanceIP) == 0 || net.ParseIP(v.InstanceIP) == nil {
			printf("Skipping API SD instance with invalid IP: %q", v.InstanceIP)
			continue
		}
		if v.InstancePort == 0 || v.InstancePort > 65535 {
			printf("Skipping API SD instance with invalid port: %d", v.InstancePort)
			continue
		}

		key := fmt.Sprintf("%s:%d", v.InstanceIP, v.InstancePort)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		endpoints = append(endpoints, &serviceEndpoint{
			SdType:      "api",
			Host:        v.InstanceIP,
			Port:        v.InstancePort,
			InstanceId:  v.InstanceId,
			ServiceId:   v.ServiceId,
			Version:     v.InstanceVersion,
			CacheExpire: cacheExpires,
		})
	}

	live := pruneExpiredEndpoints(endpoints)
	if len(live) == 0 { // fail fast on empty discovery result
		return fmt.Errorf("Service Discovery By API returned no endpoints for %s.%s (version=%s)", serviceName, namespaceName, version)
	}

	c.setEndpoints(live)
	cacheAddServiceEndpoints(serviceName+"."+namespaceName, live)

	return nil
}

func (c *Client) unaryCircuitBreakerHandler(ctx context.Context, method string, req interface{}, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	if c.closed.Load() { // guard use-after-close
		return status.Error(codes.Unavailable, "client is closed")
	}

	// CL-F4: snapshot _config into a local to eliminate TOCTOU between
	// the nil check and the Grpc.* field dereferences below. A direct
	// two-load (check then deref) against the shared atomic.Pointer
	// would nil-deref panic if a Dial error path stores nil between
	// the two reads. Every read in this function must use cfg, not
	// c._config, and never re-load mid-operation.
	cfg := c.getConfig()
	if cfg == nil || !cfg.Grpc.CircuitBreakerEnabled {
		return invoker(ctx, method, req, reply, cc, opts...)
	}

	// SP-008 P3-CONN-4 (2026-04-15): previously captured `logger := c.ZLog()`
	// into three per-call closures (logPrintf/logErrorf/logWarnf), costing
	// ~288 B + 3 allocs per RPC. Replaced with direct method calls on *Client
	// so no closure allocations occur on the CB fast-path. c.logPrintf etc.
	// use a literal-string fast-path so `%` in messages can't be misread as
	// format specifiers.

	c.logLine("In - Unary Circuit Breaker Handler: " + method)

	cb := c.getCircuitBreaker(method)

	if cb == nil {
		c.logLine("... Creating Circuit Breaker for: " + method)

		z := &data.ZapLog{
			DisableLogger:   false,
			OutputToConsole: false,
			AppName:         c.AppName,
		}
		if err := z.Init(); err != nil {
			log.Println("!!! ZapLog Init Failed: " + err.Error() + " !!!")
		}

		var e error
		if cb, e = plugins.NewHystrixGoPlugin(method,
			int(cfg.Grpc.CircuitBreakerTimeout),
			int(cfg.Grpc.CircuitBreakerMaxConcurrentRequests),
			int(cfg.Grpc.CircuitBreakerRequestVolumeThreshold),
			int(cfg.Grpc.CircuitBreakerSleepWindow),
			int(cfg.Grpc.CircuitBreakerErrorPercentThreshold),
			z); e != nil {
			c.logErr("!!! Create Circuit Breaker for: " + method + " Failed !!!")
			c.logErr("Will Skip Circuit Breaker and Continue Execution: " + e.Error())

			return invoker(ctx, method, req, reply, cc, opts...)
		}

		c.setCircuitBreaker(method, cb)

		c.logLine("... Circuit Breaker Created for: " + method)
	} else {
		c.logLine("... Using Cached Circuit Breaker Command: " + method)
	}

	_, gerr := cb.Exec(true, func(dataIn interface{}, ctx1 ...context.Context) (dataOut interface{}, err error) {
		c.logLine("Run Circuit Breaker Action for: " + method + "...")

		err = invoker(ctx, method, req, reply, cc, opts...)

		if err != nil {
			c.logErr("!!! Circuit Breaker Action for " + method + " Failed: " + err.Error() + " !!!")
		} else {
			c.logLine("... Circuit Breaker Action for " + method + " Invoked")
		}
		return nil, err

	}, func(dataIn interface{}, errIn error, ctx1 ...context.Context) (dataOut interface{}, err error) {
		c.logWarn("Circuit Breaker Action for " + method + " Fallback...")
		errMsg := "nil"
		if errIn != nil {
			errMsg = errIn.Error()
		}
		c.logWarn("... Error = " + errMsg)

		return nil, errIn
	}, nil)

	return gerr
}

func (c *Client) streamCircuitBreakerHandler(
	ctx context.Context,
	desc *grpc.StreamDesc,
	cc *grpc.ClientConn,
	method string,
	streamer grpc.Streamer,
	opts ...grpc.CallOption,
) (grpc.ClientStream, error) {

	if c == nil {
		return nil, fmt.Errorf("Client Object Nil")
	}

	if c.closed.Load() { // guard use-after-close
		return nil, status.Error(codes.Unavailable, "client is closed")
	}

	// CL-F4: snapshot _config into a local to eliminate TOCTOU. See the
	// matching comment in unaryCircuitBreakerHandler for rationale.
	cfg := c.getConfig()
	if cfg == nil || !cfg.Grpc.CircuitBreakerEnabled {
		return streamer(ctx, desc, cc, method, opts...)
	}

	// SP-008 P3-CONN-4 (2026-04-15): see matching comment in
	// unaryCircuitBreakerHandler — closures replaced with *Client methods
	// to eliminate per-stream allocations (~600-900 B + 8-12 allocs).

	c.logLine("In - Stream Circuit Breaker Handler: " + method)

	cb := c.getCircuitBreaker(method)

	if cb == nil {
		c.logLine("... Creating Circuit Breaker for: " + method)

		z := &data.ZapLog{
			DisableLogger:   false,
			OutputToConsole: false,
			AppName:         c.AppName,
		}
		if err := z.Init(); err != nil {
			log.Println("!!! ZapLog Init Failed: " + err.Error() + " !!!")
		}

		var e error
		cb, e = plugins.NewHystrixGoPlugin(method,
			int(cfg.Grpc.CircuitBreakerTimeout),
			int(cfg.Grpc.CircuitBreakerMaxConcurrentRequests),
			int(cfg.Grpc.CircuitBreakerRequestVolumeThreshold),
			int(cfg.Grpc.CircuitBreakerSleepWindow),
			int(cfg.Grpc.CircuitBreakerErrorPercentThreshold),
			z)

		if e != nil {
			c.logErr("!!! Create Circuit Breaker for: " + method + " Failed !!!")
			c.logErr("Will Skip Circuit Breaker and Continue Execution: " + e.Error())
			return streamer(ctx, desc, cc, method, opts...)
		}

		c.setCircuitBreaker(method, cb)

		c.logLine("... Circuit Breaker Created for: " + method)
	} else {
		c.logLine("... Using Cached Circuit Breaker Command: " + method)
	}

	gres, gerr := cb.Exec(true, func(dataIn interface{}, ctx1 ...context.Context) (dataOut interface{}, err error) {
		c.logLine("Run Circuit Breaker Action for: " + method + "...")

		dataOut, err = streamer(ctx, desc, cc, method, opts...)

		if err != nil {
			c.logErr("!!! Circuit Breaker Action for " + method + " Failed: " + err.Error() + " !!!")
		} else {
			c.logLine("... Circuit Breaker Action for " + method + " Invoked")
		}
		return dataOut, err
	}, func(dataIn interface{}, errIn error, ctx1 ...context.Context) (dataOut interface{}, err error) {
		c.logWarn("Circuit Breaker Action for " + method + " Fallback...")
		errMsg := "nil"
		if errIn != nil {
			errMsg = errIn.Error()
		}
		c.logWarn("... Error = " + errMsg)

		return nil, errIn
	}, nil)

	if gres != nil {
		if cs, ok := gres.(grpc.ClientStream); ok {
			return cs, gerr
		}
		return nil, fmt.Errorf("Assert grpc.ClientStream Failed")
	}

	// avoid returning (nil, nil) which causes caller panic
	if gerr == nil {
		return nil, fmt.Errorf("Circuit breaker returned nil stream without error for method %s", method)
	}
	return nil, gerr
}

// P1-CONN-CL-A (2026-04-15): unaryXRayTracerHandler was deleted —
// superseded by tracer.TracerUnaryClientInterceptor and no longer
// referenced. streamXRayTracerHandler is still in use and remains.
func (c *Client) streamXRayTracerHandler(
	ctx context.Context,
	desc *grpc.StreamDesc,
	cc *grpc.ClientConn,
	method string,
	streamer grpc.Streamer,
	opts ...grpc.CallOption,
) (cs grpc.ClientStream, err error) {

	if c == nil {
		return cs, fmt.Errorf("Client Object Nil")
	}

	if xray.XRayServiceOn() {
		parentSegID := ""
		parentTraceID := ""

		streamType := "StreamRPC"
		if desc.ClientStreams {
			streamType = "Client" + streamType
		} else if desc.ServerStreams {
			streamType = "Server" + streamType
		}

		if md, ok := metadata.FromOutgoingContext(ctx); ok {
			if v, ok2 := md["x-amzn-seg-id"]; ok2 && len(v) > 0 {
				parentSegID = v[0]
			}
			if v, ok2 := md["x-amzn-tr-id"]; ok2 && len(v) > 0 {
				parentTraceID = v[0]
			}
		}

		var seg *xray.XSegment
		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
			seg = xray.NewSegment("GrpcClient-"+streamType+"-"+method, &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID:   parentTraceID,
			})
		} else {
			seg = xray.NewSegment("GrpcClient-" + streamType + "-" + method)
		}

		if seg == nil || seg.Seg == nil { // guard against nil segment to prevent panic
			return streamer(ctx, desc, cc, method, opts...)
		}

		defer seg.Close()
		defer func() {
			if err != nil {
				xray.LogXrayAddFailure("Client", seg.SafeAddError(err))
			}
		}()

		md, ok := metadata.FromOutgoingContext(ctx) // preserve existing outgoing metadata
		if !ok {
			md = metadata.New(nil) // initialize if absent
		} else {
			md = md.Copy() // avoid mutating shared maps
		}
		md.Set("x-amzn-seg-id", seg.Seg.ID) // keep existing keys while adding tracing
		md.Set("x-amzn-tr-id", seg.Seg.TraceID)

		// attach tracing headers to outgoing context for the stream
		ctx = metadata.NewOutgoingContext(ctx, md)

		cs, err = streamer(ctx, desc, cc, method, opts...)
		return cs, err
	} else {
		return streamer(ctx, desc, cc, method, opts...)
	}
}

// =====================================================================================================================
// HTTP WEB SERVER
// =====================================================================================================================

// WebServerConfig info,
// note: WebServerLocalAddress = read only getter
//
//	note: WebServerRoutes = map[string]*ginw.RouteDefinition{
//			"base": {
//				Routes: []*ginw.Route{
//					{
//						Method: ginhttpmethod.GET,
//						RelativePath: "/",
//						Handler: func(c *gin.Context, bindingInputPtr interface{}) {
//							c.String(200, "Connector Client Http Host Up")
//						},
//					},
//				},
//			},
//		}
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

func (c *Client) startWebServer(serveErr chan<- error) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	if c.WebServerConfig == nil {
		return fmt.Errorf("Start Web Server Failed: Web Server Config Not Setup")
	}

	if util.LenTrim(c.WebServerConfig.AppName) == 0 {
		return fmt.Errorf("Start Web Server Failed: Web Server Config App Name Not Set")
	}

	if util.LenTrim(c.WebServerConfig.ConfigFileName) == 0 {
		return fmt.Errorf("Start Web Server Failed: Web Server Config Custom File Name Not Set")
	}

	if c.WebServerConfig.WebServerRoutes == nil {
		return fmt.Errorf("Start Web Server Failed: Web Server Routes Not Defined (Map Nil)")
	}

	if len(c.WebServerConfig.WebServerRoutes) == 0 {
		return fmt.Errorf("Start Web Server Failed: Web Server Routes Not Set (Count Zero)")
	}

	// require caller to provide a channel so startup errors are observable
	if serveErr == nil {
		serveErr = make(chan error, 1)
	}

	c.webServerMu.Lock() // serialize start
	defer c.webServerMu.Unlock()

	// Prevent double-start on the same Client instance.
	if c.webServer != nil {
		return fmt.Errorf("Start Web Server Failed: server already running")
	}

	// always buffer the channel we write to, even if caller passed unbuffered
	internalServeErr := make(chan error, 1)

	server, err := ws.NewWebServer(c.WebServerConfig.AppName, c.WebServerConfig.ConfigFileName, c.WebServerConfig.CustomConfigPath)
	if err != nil {
		return fmt.Errorf("Start Web Server Failed: %s", err)
	}

	/* EXAMPLE
	server.Routes = map[string]*ginw.RouteDefinition{
		"base": {
			Routes: []*ginw.Route{
				{
					Method: ginhttpmethod.GET,
					RelativePath: "/",
					Handler: func(c *gin.Context, bindingInputPtr interface{}) {
						c.String(200, "Connector Client Http Host Up")
					},
				},
			},
		},
	}
	*/
	server.Routes = c.WebServerConfig.WebServerRoutes

	// set web server local address before serve action
	httpVerb := ""

	if server.UseTls() {
		httpVerb = "https"
	} else {
		httpVerb = "http"
	}

	c.WebServerConfig.WebServerLocalAddress = fmt.Sprintf("%s://%s:%d", httpVerb, server.GetHostAddress(), server.Port())

	// FIX #33: Store the original user-provided cleanup on first call to prevent
	// accumulating a closure chain across reconnects.
	if c._origWebCleanUp == nil && c.WebServerConfig.CleanUp != nil {
		c._origWebCleanUp = c.WebServerConfig.CleanUp
	}
	origCleanup := c._origWebCleanUp
	c.WebServerConfig.CleanUp = func() {
		if origCleanup != nil {
			origCleanup()
		}
		server.RemoveDNSRecordset()
	}
	log.Println("Web Server Host Starting On: " + c.WebServerConfig.WebServerLocalAddress)

	// keep reference so Close() can stop it
	c.webServer = server
	c.webServerStop = make(chan struct{})

	// serve asynchronously so Dial can continue; capture error for logging
	// FIX: Capture channel in local var so deferred close is not affected
	// if shutdownWebServerLocked sets c.webServerStop = nil on timeout.
	// C3-007: migrated from raw go+inline recover to safeGo for uniformity
	// with CL-F5 invariant (line 803). The inner defer still needs to own
	// panic-to-error surfacing (dial-path must see an error on panic), so
	// the local recover is retained AHEAD of safeGo's outer recover. The
	// outer safeGo recover acts as a belt-and-suspenders backstop.
	stopCh := c.webServerStop
	safego.Go("start-webserver-serve", func() {
		defer close(stopCh) // signal completion/shutdown
		defer func() {
			if r := recover(); r != nil {
				if z := c.ZLog(); z != nil {
					z.Errorf("Start Web Server panic: %v", r)
				} else {
					log.Printf("Start Web Server panic: %v", r)
				}
				// surface panic to dial path so startup does not silently succeed
				internalServeErr <- fmt.Errorf("start web server panic: %v", r)
			}
		}()

		if err := server.Serve(); err != nil {
			// treat graceful shutdown as success, not an error
			if errors.Is(err, http.ErrServerClosed) {
				internalServeErr <- nil
				return
			}

			if z := c.ZLog(); z != nil {
				z.Errorf("Start Web Server Failed: (Serve Error) %s", err)
			} else {
				log.Printf("Start Web Server Failed: (Serve Error) %s", err)
			}
			// non-blocking send protects against accidental nil/closed channel.
			internalServeErr <- err
			return
		}

		internalServeErr <- nil
	})

	// C3-006: guarantee the caller receives exactly one result, even with
	// unbuffered channels. Wrapped in safeGo so a concurrent close of
	// serveErr (triggers "send on closed channel") is recovered instead
	// of crashing the process. A nil serveErr will still block-leak this
	// goroutine, but that is a caller-contract bug, not our concern here.
	internalErrCh := internalServeErr
	outCh := serveErr
	safego.Go("start-webserver-errch-forwarder", func() {
		outCh <- <-internalErrCh
	})

	return nil
}
