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
	"github.com/aldelo/connector/adapters/circuitbreaker/plugins"
	"github.com/aldelo/connector/adapters/health"
	"github.com/aldelo/connector/adapters/loadbalancer"
	"github.com/aldelo/connector/adapters/queue"
	"github.com/aldelo/connector/adapters/registry"
	"github.com/aldelo/connector/adapters/registry/sdoperationstatus"
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
	defaultCacheExpireSeconds         = 300
	defaultDialTimeoutSeconds         = 5
	defaultHealthCheckInterval        = 250 * time.Millisecond
	defaultWebServerCheckInterval     = 500 * time.Millisecond
	defaultNotifierReconnectDelay     = 5 * time.Second
	maxTCPPort                        = 65535
	defaultMaxDiscoveryResults        = 100
	connectionCloseDelay              = 100 * time.Millisecond
	webServerShutdownTimeout          = 5 * time.Second
	webServerStopChannelTimeout       = 6 * time.Second
	deregisterInstanceCheckInterval   = 250 * time.Millisecond
	deregisterInstanceMaxRetries      = 20
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
		_cache.DisableLogging = disable
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

	// refuse to bind a new connection when the client is already closed.
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
			if c._z != nil {
				c._z.Errorf("Init health client failed: %v", err)
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
		go func(conn *grpc.ClientConn) {
			time.Sleep(connectionCloseDelay) // Allow in-flight RPCs to complete
			_ = conn.Close()
		}(old)
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

// Client represents a gRPC client's connection and entry point,
// also provides optional gin based web server upon dial
//
// note:
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

	// read or persist client config settings
	_config *config

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

	// guard to avoid spawning overlapping notifier reconnect loops
	notifierReconnectActive atomic.Bool
	notifierStartMu         sync.Mutex

	zOnce sync.Once

	// guard notifier client access to avoid races across callbacks/reconnect/close
	notifierMu sync.Mutex

	webServer     *ws.WebServer // track started web server for shutdown
	webServerStop chan struct{} // signal channel to stop web server
	webServerMu   sync.Mutex
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

// helper to (re)wire notifier handlers every time before dial
func (c *Client) configureNotifierHandlers(nc *NotifierClient) {
	if c == nil || nc == nil || c._config == nil {
		return
	}

	nc.ServiceHostOnlineHandler = func(host string, port uint) {
		if c == nil || c.closed.Load() || c._config == nil {
			return
		}
		cacheExpSeconds := c._config.Target.SdEndpointCacheExpires
		if cacheExpSeconds == 0 {
			cacheExpSeconds = defaultCacheExpireSeconds
		}

		svcKey := strings.ToLower(c._config.Target.ServiceName + "." + c._config.Target.NamespaceName)
		cacheAddServiceEndpoints(svcKey, []*serviceEndpoint{
			{
				SdType:      c._config.Target.ServiceDiscoveryType,
				Host:        host,
				Port:        port,
				InstanceId:  "",
				ServiceId:   "",
				Version:     c._config.Target.InstanceVersion,
				CacheExpire: time.Now().Add(time.Duration(cacheExpSeconds) * time.Second),
			},
		})
		c.setEndpoints(cacheGetLiveServiceEndpoints(svcKey, c._config.Target.InstanceVersion, true))
		_ = c.UpdateLoadBalanceResolver()
	}

	nc.ServiceHostOfflineHandler = func(host string, port uint) {
		if c == nil || c.closed.Load() || c._config == nil {
			return
		}
		svcKey := strings.ToLower(c._config.Target.ServiceName + "." + c._config.Target.NamespaceName)
		cachePurgeServiceEndpointByHostAndPort(svcKey, host, port)
		c.setEndpoints(cacheGetLiveServiceEndpoints(svcKey, c._config.Target.InstanceVersion, true))
		_ = c.UpdateLoadBalanceResolver()
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
		go func() {
			defer c.notifierReconnectActive.Store(false)

			ticker := time.NewTicker(defaultNotifierReconnectDelay)
			defer ticker.Stop()

			for {
				// Check if closed before waiting
				if c.closed.Load() {
					return
				}

				// Simple select statement with ticker and immediate closed check
				select {
				case <-ticker.C:
					// Continue to reconnection attempt
				}

				// Check if closed after ticker
				if c.closed.Load() {
					return
				}

				if current := c.getNotifierClient(); current != nil {
					current.Close() // close leaked notifier connection before dropping it
					c.setNotifierClient(nil)
				}
				if c.closed.Load() {
					return
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
		}()
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

// NewClient creates grpc client
func NewClient(appName string, configFileName string, customConfigPath string) *Client {
	return &Client{
		AppName:          appName,
		ConfigFileName:   configFileName,
		CustomConfigPath: customConfigPath,
	}
}

// readConfig will read in config data
func (c *Client) readConfig() error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	c._config = &config{
		AppName:          c.AppName,
		ConfigFileName:   c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,
	}

	if err := c._config.Read(); err != nil {
		c._config = nil
		return fmt.Errorf("Read Config Failed: %s", err.Error())
	}

	if c._config.Target.InstancePort > 65535 {
		c._config = nil
		return fmt.Errorf("Configured Instance Port Not Valid: %s", "Tcp Port Max is 65535")
	}

	// setup logger
	c.zMu.Lock()
	c._z = &data.ZapLog{
		DisableLogger:   !c._config.Target.ZapLogEnabled,
		OutputToConsole: c._config.Target.ZapLogOutputConsole,
		AppName:         c._config.AppName,
	}
	if e := c._z.Init(); e != nil {
		c.zMu.Unlock()
		return fmt.Errorf("Init ZapLog Failed: %s", e.Error())
	}
	c.zMu.Unlock()

	cacheDisableLogging(!c._config.Target.ZapLogEnabled)

	return nil
}

// buildDialOptions returns slice of dial options built from client struct fields
func (c *Client) buildDialOptions(loadBalancerPolicy string) (opts []grpc.DialOption, err error) {
	if c == nil {
		return []grpc.DialOption{}, fmt.Errorf("Client Object Nil")
	}

	// Issue #8: Capture config reference atomically at the start to avoid nil dereference
	cfg := c._config
	if cfg == nil {
		return []grpc.DialOption{}, fmt.Errorf("Config Data Not Loaded")
	}

	logger := c.ZLog() // ensure logger is initialized
	logPrintf := func(msg string) {
		if logger != nil {
			logger.Printf(msg)
		} else {
			log.Printf(msg)
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
		_ = xray.Init("127.0.0.1:2000", "1.2.0")
		xray.SetXRayServiceOn()
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
	if cfg.Grpc.DialBlockingMode {
		opts = append(opts, grpc.WithBlock())
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
		//c.UnaryClientInterceptors = append(c.UnaryClientInterceptors, c.unaryXRayTracerHandler)
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
	opts = append(opts, grpc.FailOnNonTempDialError(true))

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
		_ = stopper.Shutdown(ctx)
	}
	cancel()

	// invoke cleanup hook if requested (e.g., Route53 DNS cleanup)
	if callCleanup && c.WebServerConfig != nil && c.WebServerConfig.CleanUp != nil {
		c.WebServerConfig.CleanUp()
	}

	// wait for server goroutine to signal completion or timeout
	if c.webServerStop != nil {
		select {
		case <-c.webServerStop:
		case <-time.After(timeout + time.Second):
		}
		c.webServerStop = nil
	}
	c.webServer = nil
}

// ZLog access internal zap logger
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

		if c._config != nil {
			appName = c._config.AppName
			disableLogger = !c._config.Target.ZapLogEnabled
			outputConsole = c._config.Target.ZapLogOutputConsole
		}

		z := &data.ZapLog{
			DisableLogger:   disableLogger,
			OutputToConsole: outputConsole,
			AppName:         appName,
		}
		_ = z.Init()
		c._z = z
	})

	c.zMu.Lock()
	z := c._z
	c.zMu.Unlock()
	return z
}

// Issue #17: Consolidate logging patterns - create helper methods for consistent logging
// logPrintf logs an info message using the configured logger or standard log
func (c *Client) logPrintf(msg string, args ...interface{}) {
	if z := c.ZLog(); z != nil {
		z.Printf(msg, args...)
	} else {
		log.Printf(msg, args...)
	}
}

// logErrorf logs an error message using the configured logger or standard log
func (c *Client) logErrorf(msg string, args ...interface{}) {
	if z := c.ZLog(); z != nil {
		z.Errorf(msg, args...)
	} else {
		log.Printf("ERROR: "+msg, args...)
	}
}

// logWarnf logs a warning message using the configured logger or standard log
func (c *Client) logWarnf(msg string, args ...interface{}) {
	if z := c.ZLog(); z != nil {
		z.Warnf(msg, args...)
	} else {
		log.Printf("WARN: "+msg, args...)
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

	if c._config != nil {
		if c._config.Grpc.DialMinConnectTimeout > 0 {
			return c._config.Grpc.DialMinConnectTimeout
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

	if c._config == nil {
		return false
	}

	if util.LenTrim(c._config.Target.AppName) == 0 {
		return false
	}

	if util.LenTrim(c._config.Target.ServiceDiscoveryType) == 0 {
		return false
	}

	if util.LenTrim(c._config.Target.ServiceName) == 0 {
		return false
	}

	if util.LenTrim(c._config.Target.NamespaceName) == 0 {
		return false
	}

	if util.LenTrim(c._config.Target.Region) == 0 {
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

	if c._config == nil {
		return false
	}

	if util.LenTrim(c._config.Topics.SnsDiscoveryTopicArn) == 0 {
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

	if c._config == nil {
		return ""
	}

	return c._config.Topics.SnsDiscoveryTopicArn
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

// Dial will dial grpc service and establish client connection
func (c *Client) Dial(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

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
		if cleanupSqs && c._sqs != nil {
			c._sqs.Disconnect()
			c._sqs = nil
		}
		if cleanupSd && c._sd != nil {
			c._sd.Disconnect()
			c._sd = nil
		}
	}()

	// Issue #6: Reset both closed and closing flags at the start of Dial to allow re-dial
	c.closed.Store(false)
	c.closing.Store(false)
	c.setConnection(nil, "")

	// read client config data in
	if c._config == nil {
		if err := c.readConfig(); err != nil {
			return err
		}
	}

	if !c.ConfiguredForClientDial() {
		c._config = nil
		return fmt.Errorf(c.ConfigFileName + " Not Yet Configured for gRPC Client Dial, Please Check Config File")
	}

	// if rest target ca cert files defined, load self-signed ca certs so that this service may use those host resources
	if util.LenTrim(c._config.Target.RestTargetCACertFiles) > 0 {
		if err := rest.AppendServerCAPemFiles(strings.Split(c._config.Target.RestTargetCACertFiles, ",")...); err != nil {
			c._z.Errorf("!!! Load Rest Target Self-Signed CA Cert Files '" + c._config.Target.RestTargetCACertFiles + "' Failed: " + err.Error() + " !!!")
		}
	}

	c._z.Printf("Client " + c._config.AppName + " Starting to Connect with " + c._config.Target.ServiceName + "." + c._config.Target.NamespaceName + "...")

	// setup sqs and sns if configured
	if util.LenTrim(c._config.Queues.SqsLoggerQueueUrl) > 0 {
		var e error
		if c._sqs, e = queue.NewQueueAdapter(awsregion.GetAwsRegion(c._config.Target.Region), nil); e != nil {
			c._z.Errorf("Get SQS Queue Adapter Failed: %s", e.Error())
			c._sqs = nil
		}
		cleanupSqs = c._sqs != nil
	} else {
		c._sqs = nil
	}

	// circuit breakers prep
	c._circuitBreakers = map[string]circuitbreaker.CircuitBreakerIFace{}

	// connect sd
	if err := c.connectSd(); err != nil {
		return err
	}
	cleanupSd = c._sd != nil

	// discover service endpoints
	if err := c.discoverEndpoints(true); err != nil {
		return err
	}
	eps := c.endpointsSnapshot() // protect _endpoints read
	if len(eps) == 0 {
		return fmt.Errorf("No Service Endpoints Discovered for " + c._config.Target.ServiceName + "." + c._config.Target.NamespaceName)
	}

	c._z.Printf("... Service Discovery for " + c._config.Target.ServiceName + "." + c._config.Target.NamespaceName + " Found " + strconv.Itoa(len(eps)) + " Endpoints:")

	// get endpoint addresses
	endpointAddrs := make([]string, 0, len(eps))
	for i, ep := range eps {
		endpointAddrs = append(endpointAddrs, fmt.Sprintf("%s:%s", ep.Host, util.UintToStr(ep.Port)))

		info := strconv.Itoa(i+1) + ") "
		info += ep.SdType + "=" + ep.Host + ":" + util.UintToStr(ep.Port) + ", "
		info += "Version=" + ep.Version + ", "
		info += "CacheExpires=" + util.FormatDateTime(ep.CacheExpire)

		c._z.Printf("       - " + info)
	}

	// setup resolver and setup load balancer
	var target string
	var loadBalancerPolicy string

	if c._config.Target.ServiceDiscoveryType != "direct" {
		var err error

		// very important: client load balancer scheme name must be alpha and lower cased
		//                 if scheme name is not valid, error will occur: transport error, tcp port unknown
		schemeName, _ := util.ExtractAlpha(c._config.AppName)
		schemeName = strings.ToLower("clb" + schemeName)

		target, loadBalancerPolicy, err = loadbalancer.WithRoundRobin(schemeName, fmt.Sprintf("%s.%s", c._config.Target.ServiceName, c._config.Target.NamespaceName), endpointAddrs)

		if err != nil {
			return fmt.Errorf("Build Client Load Balancer Failed: " + err.Error())
		}
	} else {
		target = fmt.Sprintf("%s:///%s", "passthrough", endpointAddrs[0])
		loadBalancerPolicy = ""
	}

	// build dial options
	if opts, err := c.buildDialOptions(loadBalancerPolicy); err != nil {
		return fmt.Errorf("Build gRPC Client Dial Options Failed: " + err.Error())
	} else {
		if c.BeforeClientDial != nil {
			c._z.Printf("Before gRPC Client Dial Begin...")

			c.BeforeClientDial(c)

			c._z.Printf("... Before gRPC Client Dial End")
		}

		defer func() {
			if c.AfterClientDial != nil {
				c._z.Printf("After gRPC Client Dial Begin...")

				c.AfterClientDial(c)

				c._z.Printf("... After gRPC Client Dial End")
			}
		}()

		c._z.Printf("Dialing gRPC Service @ " + target + "...")

		dialSec := c._config.Grpc.DialMinConnectTimeout
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
			c._z.Errorf("Dial Failed: (If TLS/mTLS, Check Certificate SAN) %s", err.Error())
			e := fmt.Errorf("gRPC Client Dial Service Endpoint %s Failed: (If TLS/mTLS, Check Certificate SAN) %s", target, err.Error())
			if seg != nil {
				_ = seg.Seg.AddError(e)
			}
			return e
		}

		// dial grpc service endpoint success
		c._z.Printf("Dial Successful")

		c.setConnection(conn, target)
		cleanupConn = true

		c._z.Printf("Remote Address = " + target)

		c._z.Printf("... gRPC Service @ " + target + " [" + c.RemoteAddress() + "] Connected")

		if c.WaitForServerReady {
			healthCtx, healthCancel := context.WithTimeout(ctx, time.Duration(dialSec)*time.Second)
			if e := c.waitForEndpointReady(healthCtx, time.Duration(dialSec)*time.Second); e != nil {
				// health probe failed
				if closedConn := c.clearConnection(); closedConn != nil {
					_ = closedConn.Close()
				}
				cleanupConn = false
				if seg != nil {
					_ = seg.Seg.AddError(fmt.Errorf("gRPC Service Server Not Ready: " + e.Error()))
				}
				healthCancel()
				return fmt.Errorf("gRPC Service Server Not Ready: " + e.Error())
			}
			healthCancel()
		}

		// dial successful, now start web server for notification callbacks (webhook)
		if c.WebServerConfig != nil && util.LenTrim(c.WebServerConfig.ConfigFileName) > 0 {
			c._z.Printf("Starting Http Web Server...")
			serveErrCh := make(chan error, 1)

			// start server and capture immediate config/start errors
			if err := c.startWebServer(serveErrCh); err != nil {
				if seg != nil {
					_ = seg.Seg.AddError(fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, err))
				}
				return fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, err)
			}
			cleanupWeb = true

			// wait for readiness first; propagate deterministic readiness failures
			if e := c.waitForWebServerReady(ctx, time.Duration(c._config.Target.SdTimeout)*time.Second); e != nil {
				c._z.Errorf("!!! Http Web Server %s Failed: %s !!!", c.WebServerConfig.AppName, e)
				if seg != nil {
					_ = seg.Seg.AddError(fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, e))
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
						_ = seg.Seg.AddError(fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, webErr))
					}
					return fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, webErr)
				}
			default:
			}

			// keep observing the server goroutine so late startup failures are not silent
			if !consumed {
				go func() {
					if webErr := <-serveErrCh; webErr != nil {
						if z := c.ZLog(); z != nil {
							z.Errorf("Http Web Server %s exited: %v", c.WebServerConfig.AppName, webErr)
						} else {
							log.Printf("Http Web Server %s exited: %v", c.WebServerConfig.AppName, webErr)
						}
					}
				}()
			}

			c._z.Printf("... Http Web Server Started: %s", c.WebServerConfig.WebServerLocalAddress)
		}

		//
		// dial completed
		//
		cleanupConn = false // success, keep connection open
		cleanupWeb = false
		cleanupSd = false
		cleanupSqs = false
		return nil
	}
}

func muxDialContext(ctx context.Context, target string, opts ...grpc.DialOption) (conn *grpc.ClientConn, err error) {
	_mux.Lock()
	defer _mux.Unlock()

	return grpc.DialContext(ctx, target, opts...)
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

	if c._config == nil {
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
			c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
		return 0, fmt.Errorf("GetLiveEndpointsCount requires current client connection already established first")
	}

	if c._config.Target.ServiceDiscoveryType == "direct" {
		count := len(c.endpointsSnapshot())
		printf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Aborted: Service Discovery Type is Direct = %d",
			c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName, count)
		return count, nil
	}

	printf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Started...", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)

	forceRefresh := len(c.endpointsSnapshot()) == 0 || updateEndpointsToLoadBalanceResolver

	if e := c.discoverEndpoints(forceRefresh); e != nil {
		s := fmt.Sprintf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Failed: (Discover Endpoints From Cloudmap) %s",
			c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName, e.Error())
		errorf(s)
		return 0, fmt.Errorf(s)
	}

	eps := c.endpointsSnapshot()
	if len(eps) == 0 {
		s := fmt.Sprintf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Failed: (Discover Endpoints From Cloudmap) No Live Endpoints",
			c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
		errorf(s)
		return 0, fmt.Errorf(s)
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

			printf("       - " + info)
		}

		if len(endpointAddrs) == 0 {
			s := fmt.Sprintf("GetLiveEndpointsCount-UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Aborted: Endpoint Addresses Required",
				c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
			errorf(s)
			return 0, fmt.Errorf(s)
		}

		// update load balance resolver with new endpoint addresses
		serviceName := fmt.Sprintf("%s.%s", c._config.Target.ServiceName, c._config.Target.NamespaceName)
		schemeName, _ := util.ExtractAlpha(c._config.AppName)
		schemeName = strings.ToLower("clb" + schemeName)

		if e := res.UpdateManualResolver(schemeName, serviceName, endpointAddrs); e != nil {
			errorf("GetLiveEndpointsCount-UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Failed: %s",
				c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName, e.Error())
			return 0, e
		}

		printf("GetLiveEndpointsCount-UpdateLoadBalanceResolver for Client %s with Service '%s.%s' OK",
			c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
	}

	printf("GetLiveEndpointsCount for Client %s with Service '%s.%s' OK",
		c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
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

	if c._config == nil {
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
			c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
		return fmt.Errorf("UpdateLoadBalanceResolver Requires Current Client Connection Already Established First")
	}

	if c._config.Target.ServiceDiscoveryType == "direct" {
		printf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Aborted: Service Discovery Type is Direct",
			c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
		return nil
	}

	printf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Started...",
		c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)

	eps := c.endpointsSnapshot()
	if len(eps) == 0 {
		if e := c.discoverEndpoints(false); e != nil {
			s := fmt.Sprintf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Failed: (Discover Endpoints From Cloudmap) %s",
				c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName, e.Error())
			errorf(s)
			return fmt.Errorf(s)
		}
		// refresh snapshot after discovery to use newly populated endpoints
		eps = c.endpointsSnapshot()
	}

	if len(eps) == 0 {
		s := fmt.Sprintf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Aborted: Endpoint Addresses Required",
			c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
		errorf(s)
		return fmt.Errorf(s)
	}

	// get endpoint addresses
	endpointAddrs := []string{}
	for i, ep := range eps {
		endpointAddrs = append(endpointAddrs, fmt.Sprintf("%s:%s", ep.Host, util.UintToStr(ep.Port)))

		info := strconv.Itoa(i+1) + ") "
		info += ep.SdType + "=" + ep.Host + ":" + util.UintToStr(ep.Port) + ", "
		info += "Version=" + ep.Version + ", "
		info += "CacheExpires=" + util.FormatDateTime(ep.CacheExpire)

		printf("       - " + info)
	}

	// update load balance resolver with new endpoint addresses
	serviceName := fmt.Sprintf("%s.%s", c._config.Target.ServiceName, c._config.Target.NamespaceName)

	schemeName, _ := util.ExtractAlpha(c._config.AppName)
	schemeName = strings.ToLower("clb" + schemeName)

	if e := res.UpdateManualResolver(schemeName, serviceName, endpointAddrs); e != nil {
		errorf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Failed: %s",
			c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName, e.Error())
		return e
	}

	printf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' OK",
		c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
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

	if c._config == nil { // guard against nil config (prevents panic if called before Dial)
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
			nc.Unsubscribe()
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
			_ = resp.Body.Close()
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
	serviceName := ""
	if c._config != nil && util.LenTrim(c._config.Target.ServiceName) > 0 {
		serviceName = c._config.Target.ServiceName
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
			// honor ctx/close while backing off to avoid hanging shutdown.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(interval):
			case <-func() <-chan struct{} {
				if !c.closed.Load() {
					return nil
				}
				ch := make(chan struct{})
				close(ch)
				return ch
			}():
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

		select { // honor ctx/closed during sleep
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		case <-func() <-chan struct{} {
			if !c.closed.Load() {
				return nil // no signal, keep waiting
			}
			ch := make(chan struct{})
			close(ch)
			return ch
		}():
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
		if c._z != nil {
			c._z.Errorf("Init health client failed: %v", err)
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
		if conn != nil {
			c.setupHealthManualChecker()
			c.connMu.RLock()
			hc = c._healthManualChecker
			c.connMu.RUnlock()
			if hc == nil {
				return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Health Probe Failed: (Auto Instantiate) %s", "Health Manual Checker is Nil")
			}
		} else {
			return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Health Probe Failed: %s", "Health Manual Checker is Nil")
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

// Close will close grpc client connection
func (c *Client) Close() {
	if c == nil {
		log.Println("Close(): Client Object Nil")
		return
	}

	// make Close idempotent to avoid double teardown in concurrent/duplicate calls
	if !c.closing.CompareAndSwap(false, true) {
		return
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

	if c.BeforeClientClose != nil {
		printf("Before gRPC Client Close Begin...")
		c.BeforeClientClose(c)
		printf("... Before gRPC Client Close End")
	}

	// now mark closed; this prevents use-after-close while keeping hook observable.
	if !c.closed.CompareAndSwap(false, true) {
		// already closed by another goroutine after the hook finished
		return
	}

	defer func() {
		if c.AfterClientClose != nil {
			printf("After gRPC Client Close Begin...")
			c.AfterClientClose(c)
			printf("... After gRPC Client Close End")
		}
	}()

	// clean up web server route53 dns if applicable
	if c.WebServerConfig != nil && c.WebServerConfig.CleanUp != nil {
		c.WebServerConfig.CleanUp()
	}

	c.webServerMu.Lock()
	// Issue #18: Use shared shutdown helper
	c.shutdownWebServerLocked(webServerShutdownTimeout, false) // CleanUp already called above
	c.webServerMu.Unlock()

	// clean up notifier client connection
	if nc := c.getNotifierClient(); nc != nil { // use guarded getter to avoid races
		if nc.NotifierClientAlertServicesStarted() {
			if err := nc.Unsubscribe(); err != nil {
				errorf("!!! Notifier Client Alert Services Unsubscribe Failed: " + err.Error() + " !!!")
			}
		}

		nc.Close()
		c.setNotifierClient(nil) // synchronized setter
	}
	c.notifierReconnectActive.Store(false)

	if c._sqs != nil {
		c._sqs.Disconnect()
		c._sqs = nil
	}

	if c._sd != nil {
		c._sd.Disconnect()
		c._sd = nil
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

	// guard against missing config to avoid nil deref when connectSd is called directly.
	if c._config == nil {
		return fmt.Errorf("Config Data Not Loaded")
	}

	if c._sd != nil { // ensure previous CloudMap client is disconnected before reconfiguring
		c._sd.Disconnect()
		c._sd = nil
	}

	// skip CloudMap wiring when using direct discovery to avoid unnecessary AWS dependency
	if strings.EqualFold(c._config.Target.ServiceDiscoveryType, "direct") {
		return nil
	}

	if util.LenTrim(c._config.Target.NamespaceName) > 0 && util.LenTrim(c._config.Target.ServiceName) > 0 && util.LenTrim(c._config.Target.Region) > 0 {
		cm := &cloudmap.CloudMap{
			AwsRegion: awsregion.GetAwsRegion(c._config.Target.Region),
		}
		if err := cm.Connect(); err != nil {
			return fmt.Errorf("Connect SD Failed: %s", err.Error())
		}
		c._sd = cm
	} else {
		c._sd = nil
	}

	return nil
}

// discoverEndpoints uses srv, a, api, or direct to query endpoints
func (c *Client) discoverEndpoints(forceRefresh bool) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	if c._config == nil {
		return fmt.Errorf("Config Data Not Loaded")
	}

	cacheExpSeconds := c._config.Target.SdEndpointCacheExpires
	if cacheExpSeconds == 0 {
		cacheExpSeconds = defaultCacheExpireSeconds
	}

	cacheExpires := time.Now().Add(time.Duration(cacheExpSeconds) * time.Second)

	var err error
	switch c._config.Target.ServiceDiscoveryType {
	case "direct":
		err = c.setDirectConnectEndpoint(cacheExpires, c._config.Target.DirectConnectIpPort)
	case "srv":
		fallthrough
	case "a":
		err = c.setDnsDiscoveredIpPorts(cacheExpires, c._config.Target.ServiceDiscoveryType == "srv", c._config.Target.ServiceName,
			c._config.Target.NamespaceName, c._config.Target.InstancePort, forceRefresh)
	case "api":
		err = c.setApiDiscoveredIpPorts(cacheExpires, c._config.Target.ServiceName, c._config.Target.NamespaceName, c._config.Target.InstanceVersion,
			int64(c._config.Target.SdInstanceMaxResult), c._config.Target.SdTimeout, forceRefresh)
	default:
		err = fmt.Errorf("Unexpected Service Discovery Type: " + c._config.Target.ServiceDiscoveryType)
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
		return fmt.Errorf("Direct Connect target must include host and port (got %q): %v", directIpPort, err)
	}

	host = strings.TrimSpace(host)
	if util.LenTrim(host) == 0 { // reject empty host early
		return fmt.Errorf("Direct Connect target host is empty (got %q)", directIpPort)
	}

	p, convErr := strconv.Atoi(strings.TrimSpace(portStr))
	if convErr != nil {
		return fmt.Errorf("Direct Connect port invalid (%q): %v", portStr, convErr)
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
			printf("   - " + v.Host + ":" + util.UintToStr(v.Port) + ", Cache Expires: " + util.FormatDateTime(v.CacheExpire))
		}
		return nil
	}

	//
	// acquire dns ip port from service discovery
	//
	log.Printf("Start DiscoverDnsIps %s.%s SRV=%v", serviceName, namespaceName, srv)
	ipList, err := registry.DiscoverDnsIps(serviceName+"."+namespaceName, srv)
	if err != nil {
		return fmt.Errorf("Service Discovery By DNS Failed: " + err.Error())
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

	if c._sd == nil {
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
		printf("Using API Discovered Cache Hosts: (Service) " + serviceName + "." + namespaceName)
		for _, v := range c.endpointsSnapshot() {
			printf("   - " + v.Host + ":" + util.UintToStr(v.Port) + ", Cache Expires: " + util.FormatDateTime(v.CacheExpire))
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
	instanceList, err := registry.DiscoverInstances(c._sd, serviceName, namespaceName, true, customAttr, &maxCount, timeoutDuration...)
	if err != nil {
		return fmt.Errorf("Service Discovery By API Failed: " + err.Error())
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

// findUnhealthyInstances will call cloud map sd to discover unhealthy instances, a slice of unhealthy instances is returned
func (c *Client) findUnhealthyEndpoints(serviceName string, namespaceName string, version string, maxCount int64, timeoutSeconds uint) (unhealthyList []*serviceEndpoint, err error) {
	if c == nil {
		return []*serviceEndpoint{}, fmt.Errorf("Client Object Nil")
	}

	if c._sd == nil {
		return []*serviceEndpoint{}, fmt.Errorf("Service Discovery Client Not Connected")
	}

	if util.LenTrim(serviceName) == 0 {
		return []*serviceEndpoint{}, fmt.Errorf("Service Name Not Defined in Config (API SD)")
	}

	if util.LenTrim(namespaceName) == 0 {
		return []*serviceEndpoint{}, fmt.Errorf("Namespace Name Not Defined in Config (API SD)")
	}

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

	instanceList, err := registry.DiscoverInstances(c._sd, serviceName, namespaceName, false, customAttr, &maxCount, timeoutDuration...)
	if err != nil {
		return []*serviceEndpoint{}, fmt.Errorf("Service Discovery By API Failed: " + err.Error())
	}

	for _, v := range instanceList {
		// validate IP and port
		if util.LenTrim(v.InstanceIP) == 0 || net.ParseIP(v.InstanceIP) == nil {
			continue
		}
		if v.InstancePort == 0 || v.InstancePort > 65535 {
			continue
		}

		unhealthyList = append(unhealthyList, &serviceEndpoint{
			SdType:      "api",
			Host:        v.InstanceIP,
			Port:        v.InstancePort,
			InstanceId:  v.InstanceId,
			ServiceId:   v.ServiceId,
			Version:     v.InstanceVersion,
			CacheExpire: time.Time{},
		})
	}

	return unhealthyList, nil
}

// updateHealth will update instance health
func (c *Client) updateHealth(p *serviceEndpoint, healthy bool) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	if c._sd != nil && c._config != nil && p != nil && p.SdType == "api" && util.LenTrim(p.ServiceId) > 0 && util.LenTrim(p.InstanceId) > 0 {
		return registry.UpdateHealthStatus(c._sd, p.InstanceId, p.ServiceId, healthy)
	} else {
		return nil
	}
}

// deregisterInstance will remove instance from cloudmap and route 53
func (c *Client) deregisterInstance(p *serviceEndpoint) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	logprintf := func(msg string, args ...interface{}) {
		if z := c.ZLog(); z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}
	errorf := func(msg string, args ...interface{}) {
		if z := c.ZLog(); z != nil {
			z.Errorf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	if c._sd != nil && c._config != nil && p != nil && p.SdType == "api" && util.LenTrim(p.ServiceId) > 0 && util.LenTrim(p.InstanceId) > 0 {
		logprintf("De-Register Instance '%s:%s-%s' Begin...", p.Host, util.UintToStr(p.Port), p.InstanceId)

		var timeoutDuration []time.Duration
		if c._config.Target.SdTimeout > 0 {
			timeoutDuration = append(timeoutDuration, time.Duration(c._config.Target.SdTimeout)*time.Second)
		}

		operationId, err := registry.DeregisterInstance(c._sd, p.InstanceId, p.ServiceId, timeoutDuration...)
		if err != nil {
			errorf("... De-Register Instance '%s:%s-%s' Failed: %s", p.Host, util.UintToStr(p.Port), p.InstanceId, err.Error())
			return fmt.Errorf("De-Register Instance '%s:%s-%s'Fail: %s", p.Host, util.UintToStr(p.Port), p.InstanceId, err.Error())
		}

		tryCount := 0
		time.Sleep(deregisterInstanceCheckInterval)

		for {
			status, e := registry.GetOperationStatus(c._sd, operationId, timeoutDuration...)
			if e != nil {
				errorf("... De-Register Instance '%s:%s-%s' Failed: %s", p.Host, util.UintToStr(p.Port), p.InstanceId, e.Error())
				return fmt.Errorf("De-Register Instance '%s:%s-%s'Fail: %s", p.Host, util.UintToStr(p.Port), p.InstanceId, e.Error())
			}

			if status == sdoperationstatus.Success {
				logprintf("... De-Register Instance '%s:%s-%s' OK", p.Host, util.UintToStr(p.Port), p.InstanceId)
				return nil
			}

			if tryCount < deregisterInstanceMaxRetries {
				tryCount++
				logprintf("... Checking De-Register Instance '%s:%s-%s' Completion Status, Attempt %d (100ms)", p.Host, util.UintToStr(p.Port), p.InstanceId, tryCount)
				time.Sleep(deregisterInstanceCheckInterval)
				continue
			}

			errorf("... De-Register Instance '%s:%s-%s' Failed: Operation Timeout After 5 Seconds", p.Host, util.UintToStr(p.Port), p.InstanceId)
			return fmt.Errorf("De-Register Instance '%s:%s-%s'Fail When Operation Timed Out After 5 Seconds", p.Host, util.UintToStr(p.Port), p.InstanceId)
		}
	}

	return nil
}

func (c *Client) unaryCircuitBreakerHandler(ctx context.Context, method string, req interface{}, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	if c.closed.Load() { // guard use-after-close
		return status.Error(codes.Unavailable, "client is closed")
	}

	// guard against nil _config
	if c._config == nil || !c._config.Grpc.CircuitBreakerEnabled {
		return invoker(ctx, method, req, reply, cc, opts...)
	}

	logger := c.ZLog()
	logPrintf := func(msg string) {
		if logger != nil {
			logger.Printf(msg)
		} else {
			log.Printf(msg)
		}
	}
	logErrorf := func(msg string) {
		if logger != nil {
			logger.Errorf(msg)
		} else {
			log.Printf(msg)
		}
	}
	logWarnf := func(msg string) {
		if logger != nil {
			logger.Warnf(msg)
		} else {
			log.Printf(msg)
		}
	}

	logPrintf("In - Unary Circuit Breaker Handler: " + method)

	c.cbMu.RLock()
	cb := c._circuitBreakers[method]
	c.cbMu.RUnlock()

	if cb == nil {
		logPrintf("... Creating Circuit Breaker for: " + method)

		z := &data.ZapLog{
			DisableLogger:   false,
			OutputToConsole: false,
			AppName:         c.AppName,
		}
		_ = z.Init()

		var e error
		if cb, e = plugins.NewHystrixGoPlugin(method,
			int(c._config.Grpc.CircuitBreakerTimeout),
			int(c._config.Grpc.CircuitBreakerMaxConcurrentRequests),
			int(c._config.Grpc.CircuitBreakerRequestVolumeThreshold),
			int(c._config.Grpc.CircuitBreakerSleepWindow),
			int(c._config.Grpc.CircuitBreakerErrorPercentThreshold),
			z); e != nil {
			c._z.Errorf("!!! Create Circuit Breaker for: " + method + " Failed !!!")
			c._z.Errorf("Will Skip Circuit Breaker and Continue Execution: " + e.Error())

			return invoker(ctx, method, req, reply, cc, opts...)
		}

		c.cbMu.Lock()
		if c._circuitBreakers == nil {
			c._circuitBreakers = map[string]circuitbreaker.CircuitBreakerIFace{}
		}
		c._circuitBreakers[method] = cb
		c.cbMu.Unlock()

		logPrintf("... Circuit Breaker Created for: " + method)
	} else {
		logPrintf("... Using Cached Circuit Breaker Command: " + method)
	}

	_, gerr := cb.Exec(true, func(dataIn interface{}, ctx1 ...context.Context) (dataOut interface{}, err error) {
		logPrintf("Run Circuit Breaker Action for: " + method + "...")

		err = invoker(ctx, method, req, reply, cc, opts...)

		if err != nil {
			logErrorf("!!! Circuit Breaker Action for " + method + " Failed: " + err.Error() + " !!!")
		} else {
			logPrintf("... Circuit Breaker Action for " + method + " Invoked")
		}
		return nil, err

	}, func(dataIn interface{}, errIn error, ctx1 ...context.Context) (dataOut interface{}, err error) {
		logWarnf("Circuit Breaker Action for " + method + " Fallback...")
		logWarnf("... Error = " + errIn.Error())

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

	// guard against nil _config to avoid panic
	if c._config == nil || !c._config.Grpc.CircuitBreakerEnabled {
		return streamer(ctx, desc, cc, method, opts...)
	}

	logger := c.ZLog()
	logPrintf := func(msg string) {
		if logger != nil {
			logger.Printf(msg)
		} else {
			log.Printf(msg)
		}
	}
	logErrorf := func(msg string) {
		if logger != nil {
			logger.Errorf(msg)
		} else {
			log.Printf(msg)
		}
	}
	logWarnf := func(msg string) {
		if logger != nil {
			logger.Warnf(msg)
		} else {
			log.Printf(msg)
		}
	}

	logPrintf("In - Stream Circuit Breaker Handler: " + method)

	c.cbMu.RLock()
	cb := c._circuitBreakers[method]
	c.cbMu.RUnlock()

	if cb == nil {
		logPrintf("... Creating Circuit Breaker for: " + method)

		z := &data.ZapLog{
			DisableLogger:   false,
			OutputToConsole: false,
			AppName:         c.AppName,
		}
		_ = z.Init()

		var e error
		cb, e = plugins.NewHystrixGoPlugin(method,
			int(c._config.Grpc.CircuitBreakerTimeout),
			int(c._config.Grpc.CircuitBreakerMaxConcurrentRequests),
			int(c._config.Grpc.CircuitBreakerRequestVolumeThreshold),
			int(c._config.Grpc.CircuitBreakerSleepWindow),
			int(c._config.Grpc.CircuitBreakerErrorPercentThreshold),
			z)

		if e != nil {
			logErrorf("!!! Create Circuit Breaker for: " + method + " Failed !!!")
			logErrorf("Will Skip Circuit Breaker and Continue Execution: " + e.Error())
			return streamer(ctx, desc, cc, method, opts...)
		}

		c.cbMu.Lock()
		if c._circuitBreakers == nil { // defensive init
			c._circuitBreakers = map[string]circuitbreaker.CircuitBreakerIFace{}
		}
		c._circuitBreakers[method] = cb
		c.cbMu.Unlock()

		logPrintf("... Circuit Breaker Created for: " + method)
	} else {
		logPrintf("... Using Cached Circuit Breaker Command: " + method)
	}

	gres, gerr := cb.Exec(true, func(dataIn interface{}, ctx1 ...context.Context) (dataOut interface{}, err error) {
		logPrintf("Run Circuit Breaker Action for: " + method + "...")

		dataOut, err = streamer(ctx, desc, cc, method, opts...)

		if err != nil {
			logErrorf("!!! Circuit Breaker Action for " + method + " Failed: " + err.Error() + " !!!")
		} else {
			logPrintf("... Circuit Breaker Action for " + method + " Invoked")
		}
		return dataOut, err
	}, func(dataIn interface{}, errIn error, ctx1 ...context.Context) (dataOut interface{}, err error) {
		logWarnf("Circuit Breaker Action for " + method + " Fallback...")
		logWarnf("... Error = " + errIn.Error())

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

func (c *Client) unaryXRayTracerHandler(ctx context.Context, method string, req interface{}, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) (err error) {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	if xray.XRayServiceOn() {
		parentSegID := ""
		parentTraceID := ""

		// use outgoing metadata on the client side
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
			seg = xray.NewSegment("GrpcClient-UnaryRPC-"+method, &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID:   parentTraceID,
			})
		} else {
			seg = xray.NewSegment("GrpcClient-UnaryRPC-" + method)
		}

		if seg == nil || seg.Seg == nil { // guard against nil segment to prevent panic
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
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

		// attach tracing headers to outgoing context
		ctx = metadata.NewOutgoingContext(ctx, md)

		err = invoker(ctx, method, req, reply, cc, opts...)
		return err
	}

	return invoker(ctx, method, req, reply, cc, opts...)
}

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
				_ = seg.Seg.AddError(err)
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

	// preserve caller-provided cleanup and chain with DNS cleanup.
	prevCleanup := c.WebServerConfig.CleanUp
	c.WebServerConfig.CleanUp = func() {
		if prevCleanup != nil {
			prevCleanup()
		}
		server.RemoveDNSRecordset()
	}
	log.Println("Web Server Host Starting On: " + c.WebServerConfig.WebServerLocalAddress)

	// keep reference so Close() can stop it
	c.webServer = server
	c.webServerStop = make(chan struct{})

	// serve asynchronously so Dial can continue; capture error for logging
	go func() {
		defer close(c.webServerStop) // signal completion/shutdown
		defer func() {
			if r := recover(); r != nil {
				server.RemoveDNSRecordset()
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

			server.RemoveDNSRecordset()
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
	}()

	// guarantee the caller receives exactly one result, even with unbuffered channels.
	go func(errCh <-chan error, out chan<- error) {
		out <- <-errCh
	}(internalServeErr, serveErr)

	return nil
}
