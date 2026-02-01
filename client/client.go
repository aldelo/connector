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
	"fmt"
	"log"
	"net"
	"path"
	"strconv"
	"strings"
	"sync"
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
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"
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
		return nil
	}
	return _cache.GetLiveServiceEndpoints(key, version, force...)
}

func cachePurgeServiceEndpointByHostAndPort(key, host string, port uint) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if _cache != nil {
		_cache.PurgeServiceEndpointByHostAndPort(key, host, port)
	}
}

var _mux sync.Mutex // thread-safety for accessing resolver map in DialContext()

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
	_z *data.ZapLog
}

// safely replace the current endpoints slice
func (c *Client) setEndpoints(eps []*serviceEndpoint) {
	c.endpointsMu.Lock()
	c._endpoints = eps
	c.endpointsMu.Unlock()
}

// snapshot current endpoints (shallow copy) under read lock
func (c *Client) endpointsSnapshot() []*serviceEndpoint {
	c.endpointsMu.RLock()
	defer c.endpointsMu.RUnlock()
	out := make([]*serviceEndpoint, len(c._endpoints))
	copy(out, c._endpoints)
	return out
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
	c._z = &data.ZapLog{
		DisableLogger:   !c._config.Target.ZapLogEnabled,
		OutputToConsole: c._config.Target.ZapLogOutputConsole,
		AppName:         c._config.AppName,
	}
	if e := c._z.Init(); e != nil {
		return fmt.Errorf("Init ZapLog Failed: %s", e.Error())
	}

	cacheDisableLogging(!c._config.Target.ZapLogEnabled)

	return nil
}

// buildDialOptions returns slice of dial options built from client struct fields
func (c *Client) buildDialOptions(loadBalancerPolicy string) (opts []grpc.DialOption, err error) {
	if c == nil {
		return []grpc.DialOption{}, fmt.Errorf("Client Object Nil")
	}

	if c._config == nil {
		return []grpc.DialOption{}, fmt.Errorf("Config Data Not Loaded")
	}

	//
	// config client options
	//

	// set tls credential dial option
	if util.LenTrim(c._config.Grpc.ServerCACertFiles) > 0 {
		tls := new(tlsconfig.TlsConfig)
		if tc, e := tls.GetClientTlsConfig(strings.Split(c._config.Grpc.ServerCACertFiles, ","), c._config.Grpc.ClientCertFile, c._config.Grpc.ClientKeyFile); e != nil {
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
	if c._config.Target.TraceUseXRay {
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
	if util.LenTrim(c._config.Grpc.UserAgent) > 0 {
		opts = append(opts, grpc.WithUserAgent(c._config.Grpc.UserAgent))
	}

	// set with block option,
	// with block will halt code execution until after dial completes
	if c._config.Grpc.DialBlockingMode {
		opts = append(opts, grpc.WithBlock())
	}

	// set default server config for load balancer and/or health check
	defSvrConf := ""

	if c._config.Grpc.UseLoadBalancer && util.LenTrim(loadBalancerPolicy) > 0 {
		defSvrConf = loadBalancerPolicy
	}

	if c._config.Grpc.UseHealthCheck {
		if util.LenTrim(defSvrConf) > 0 {
			defSvrConf += ", "
		}

		defSvrConf += `"healthCheckConfig":{"serviceName":""}`
	}

	if util.LenTrim(defSvrConf) > 0 {
		opts = append(opts, grpc.WithDefaultServiceConfig(fmt.Sprintf(`{%s}`, defSvrConf)))
	}

	// set connect timeout value
	if c._config.Grpc.DialMinConnectTimeout > 0 {
		opts = append(opts, grpc.WithConnectParams(grpc.ConnectParams{
			Backoff:           backoff.DefaultConfig,
			MinConnectTimeout: time.Duration(c._config.Grpc.DialMinConnectTimeout) * time.Second,
		}))
	}

	// set keep alive dial options
	ka := keepalive.ClientParameters{
		PermitWithoutStream: c._config.Grpc.KeepAlivePermitWithoutStream,
	}

	if c._config.Grpc.KeepAliveInactivePingTimeTrigger > 0 {
		ka.Time = time.Duration(c._config.Grpc.KeepAliveInactivePingTimeTrigger) * time.Second
	}

	if c._config.Grpc.KeepAliveInactivePingTimeout > 0 {
		ka.Timeout = time.Duration(c._config.Grpc.KeepAliveInactivePingTimeout) * time.Second
	}

	opts = append(opts, grpc.WithKeepaliveParams(ka))

	// set read buffer dial option, 32 kb default, 32 * 1024
	if c._config.Grpc.ReadBufferSize > 0 {
		opts = append(opts, grpc.WithReadBufferSize(int(c._config.Grpc.ReadBufferSize)))
	}

	// set write buffer dial option, 32 kb default, 32 * 1024
	if c._config.Grpc.WriteBufferSize > 0 {
		opts = append(opts, grpc.WithWriteBufferSize(int(c._config.Grpc.WriteBufferSize)))
	}

	// turn off retry when retry default is enabled in the future framework versions
	opts = append(opts, grpc.WithDisableRetry())

	// add unary client interceptors
	if c._config.Grpc.CircuitBreakerEnabled {
		c._z.Printf("Setup Unary Circuit Breaker Interceptor")
		c.UnaryClientInterceptors = append(c.UnaryClientInterceptors, c.unaryCircuitBreakerHandler)
	}

	if xray.XRayServiceOn() {
		c._z.Printf("Setup Unary XRay Tracer Interceptor")
		//c.UnaryClientInterceptors = append(c.UnaryClientInterceptors, c.unaryXRayTracerHandler)
		c.UnaryClientInterceptors = append(c.UnaryClientInterceptors, tracer.TracerUnaryClientInterceptor(c._config.Target.AppName+"-Client"))
	}

	count := len(c.UnaryClientInterceptors)

	if count == 1 {
		opts = append(opts, grpc.WithUnaryInterceptor(c.UnaryClientInterceptors[0]))
	} else if count > 1 {
		opts = append(opts, grpc.WithChainUnaryInterceptor(c.UnaryClientInterceptors...))
	}

	// add stream client interceptors
	if c._config.Grpc.CircuitBreakerEnabled {
		c._z.Printf("Setup Stream Circuit Breaker Interceptor")
		c.StreamClientInterceptors = append(c.StreamClientInterceptors, c.streamCircuitBreakerHandler)
	}

	if xray.XRayServiceOn() {
		c._z.Printf("Setup Stream XRay Tracer Interceptor")
		c.StreamClientInterceptors = append(c.StreamClientInterceptors, c.streamXRayTracerHandler)
	}

	count = len(c.StreamClientInterceptors)

	if count == 1 {
		opts = append(opts, grpc.WithStreamInterceptor(c.StreamClientInterceptors[0]))
	} else if count > 1 {
		opts = append(opts, grpc.WithChainStreamInterceptor(c.StreamClientInterceptors...))
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

// ZLog access internal zap logger
func (c *Client) ZLog() *data.ZapLog {
	if c == nil {
		log.Println("ZLog(): Client Object Nil")
		return nil
	}

	if c._z != nil {
		return c._z
	} else {
		appName := "Default-BeforeConfigLoad"
		disableLogger := true
		outputConsole := true

		if c._config != nil {
			appName = c._config.AppName
			disableLogger = !c._config.Target.ZapLogEnabled
			outputConsole = c._config.Target.ZapLogOutputConsole
		}

		c._z = &data.ZapLog{
			DisableLogger:   disableLogger,
			OutputToConsole: outputConsole,
			AppName:         appName,
		}
		_ = c._z.Init()

		return c._z
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

	eps := c.endpointsSnapshot() // protect _endpoints read
	if c._conn != nil && len(eps) > 0 && (c._conn.GetState() == connectivity.Ready || c._conn.GetState() == connectivity.Idle) {
		return true
	}

	return false
}

// Dial will dial grpc service and establish client connection
func (c *Client) Dial(ctx context.Context) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	c._remoteAddress = ""

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
	} else {
		c._sqs = nil
	}

	// circuit breakers prep
	c._circuitBreakers = map[string]circuitbreaker.CircuitBreakerIFace{}

	// connect sd
	if err := c.connectSd(); err != nil {
		return err
	}

	// discover service endpoints
	if err := c.discoverEndpoints(true); err != nil {
		return err
	} else if len(c._endpoints) == 0 {
		return fmt.Errorf("No Service Endpoints Discovered for " + c._config.Target.ServiceName + "." + c._config.Target.NamespaceName)
	}

	c._z.Printf("... Service Discovery for " + c._config.Target.ServiceName + "." + c._config.Target.NamespaceName + " Found " + strconv.Itoa(len(c._endpoints)) + " Endpoints:")

	// get endpoint addresses
	endpointAddrs := []string{}

	for i, ep := range c._endpoints {
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

		if c._conn, err = muxDialContext(ctxWithTimeout, target, opts...); err != nil {
			c._z.Errorf("Dial Failed: (If TLS/mTLS, Check Certificate SAN) %s", err.Error())
			e := fmt.Errorf("gRPC Client Dial Service Endpoint %s Failed: (If TLS/mTLS, Check Certificate SAN) %s", target, err.Error())
			if seg != nil {
				_ = seg.Seg.AddError(e)
			}
			return e
		} else {
			// dial grpc service endpoint success
			c._z.Printf("Dial Successful")

			c._remoteAddress = target

			c._z.Printf("Remote Address = " + target)

			c.setupHealthManualChecker()

			c._z.Printf("... gRPC Service @ " + target + " [" + c._remoteAddress + "] Connected")

			if c.WaitForServerReady {
				if e := c.waitForEndpointReady(time.Duration(dialSec) * time.Second); e != nil {
					// health probe failed
					_ = c._conn.Close()
					if seg != nil {
						_ = seg.Seg.AddError(fmt.Errorf("gRPC Service Server Not Ready: " + e.Error()))
					}
					return fmt.Errorf("gRPC Service Server Not Ready: " + e.Error())
				}
			}

			// dial successful, now start web server for notification callbacks (webhook)
			if c.WebServerConfig != nil && util.LenTrim(c.WebServerConfig.ConfigFileName) > 0 {
				c._z.Printf("Starting Http Web Server...")
				startWebServerFail := make(chan bool)

				go func() {
					//
					// start http web server
					//
					if err := c.startWebServer(); err != nil {
						c._z.Errorf("Serve Http Web Server %s Failed: %s", c.WebServerConfig.AppName, err)
						startWebServerFail <- true
					} else {
						c._z.Printf("... Http Web Server Quit Command Received")
					}
				}()

				// give slight time delay to allow time slice for non blocking code to complete in goroutine above
				time.Sleep(150 * time.Millisecond)

				select {
				case <-startWebServerFail:
					c._z.Errorf("... Http Web Server Fail to Start")
				default:
					// wait short time to check if web server was started up successfully
					if e := c.waitForWebServerReady(time.Duration(c._config.Target.SdTimeout) * time.Second); e != nil {
						// web server error
						c._z.Errorf("!!! Http Web Server %s Failed: %s !!!", c.WebServerConfig.AppName, e)
						if seg != nil {
							_ = seg.Seg.AddError(fmt.Errorf("Http Web Server %s Failed: %s", c.WebServerConfig.AppName, e))
						}
					} else {
						// web server ok
						c._z.Printf("... Http Web Server Started: %s", c.WebServerConfig.WebServerLocalAddress)
					}
				}
			}

			//
			// dial completed
			//
			return nil
		}
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

	if c._conn == nil {
		errorf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Requires Current Client Connection Already Established First", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
		return 0, fmt.Errorf("GetLiveEndpointsCount requires current client connection already established first")
	}

	if c._config.Target.ServiceDiscoveryType == "direct" {
		printf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Aborted: Service Discovery Type is Direct", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
		return 0, nil
	}

	printf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Started...", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)

	forceRefresh := len(c.endpointsSnapshot()) == 0 || updateEndpointsToLoadBalanceResolver

	if e := c.discoverEndpoints(forceRefresh); e != nil {
		s := fmt.Sprintf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Failed: (Discover Endpoints From Cloudmap) %s", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName, e.Error())
		errorf(s)
		return 0, fmt.Errorf(s)
	}

	eps := c.endpointsSnapshot()
	if len(eps) == 0 {
		s := fmt.Sprintf("GetLiveEndpointsCount for Client %s with Service '%s.%s' Failed: (Discover Endpoints From Cloudmap) No Live Endpoints", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
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

			c._z.Printf("       - " + info)
		}

		if len(endpointAddrs) == 0 {
			s := fmt.Sprintf("GetLiveEndpointsCount-UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Aborted: Endpoint Addresses Required", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
			errorf(s)
			return 0, fmt.Errorf(s)
		}

		// update load balance resolver with new endpoint addresses
		serviceName := fmt.Sprintf("%s.%s", c._config.Target.ServiceName, c._config.Target.NamespaceName)
		schemeName, _ := util.ExtractAlpha(c._config.AppName)
		schemeName = "clb" + schemeName

		if e := res.UpdateManualResolver(schemeName, serviceName, endpointAddrs); e != nil {
			errorf("GetLiveEndpointsCount-UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Failed: %s", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName, e.Error())
			return 0, e
		}

		printf("GetLiveEndpointsCount-UpdateLoadBalanceResolver for Client %s with Service '%s.%s' OK", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
	}

	printf("GetLiveEndpointsCount for Client %s with Service '%s.%s' OK", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
	return len(eps), nil
}

// UpdateLoadBalanceResolves updates client load balancer resolver state with new endpoint addresses
func (c *Client) UpdateLoadBalanceResolver() error {
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

	if c._conn == nil {
		errorf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Requires Current Client Connection Already Established First", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
		return fmt.Errorf("UpdateLoadBalanceResolver Requires Current Client Connection Already Established First")
	}

	if c._config.Target.ServiceDiscoveryType == "direct" {
		printf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Aborted: Service Discovery Type is Direct", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
		return nil
	}

	printf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Started...", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)

	eps := c.endpointsSnapshot()
	if len(eps) == 0 {
		if e := c.discoverEndpoints(false); e != nil {
			s := fmt.Sprintf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Failed: (Discover Endpoints From Cloudmap) %s", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName, e.Error())
			errorf(s)
			return fmt.Errorf(s)
		}
		// refresh snapshot after discovery to use newly populated endpoints
		eps = c.endpointsSnapshot()
	}

	if len(eps) == 0 {
		s := fmt.Sprintf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Aborted: Endpoint Addresses Required", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
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

		c._z.Printf("       - " + info)
	}

	// update load balance resolver with new endpoint addresses
	serviceName := fmt.Sprintf("%s.%s", c._config.Target.ServiceName, c._config.Target.NamespaceName)

	schemeName, _ := util.ExtractAlpha(c._config.AppName)
	schemeName = "clb" + schemeName

	if e := res.UpdateManualResolver(schemeName, serviceName, endpointAddrs); e != nil {
		errorf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' Failed: %s", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName, e.Error())
		return e
	}

	printf("UpdateLoadBalanceResolver for Client %s with Service '%s.%s' OK", c._config.AppName, c._config.Target.ServiceName, c._config.Target.NamespaceName)
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

	// finally, run notifier client to subscribe for notification callbacks
	// the notifier client uses the same client config yaml, but a copy of it to keep the scope separated
	// within the notifier client config yaml, named xyz-notifier-client.yaml, where xyz is the endpoint service name,
	// the discovery topicArn as pre-created on aws is stored within, this enables callback for this specific topicArn from notifier server,
	// note that the service discovery of notifier client is to the notifier server cluster
	doConnection := c._notifierClient != nil

	if doConnection || util.FileExists(path.Join(c.CustomConfigPath, c.ConfigFileName+"-notifier-client.yaml")) {
		if !doConnection {
			c._notifierClient = NewNotifierClient(c.AppName+"-Notifier-Client", c.ConfigFileName+"-notifier-client", c.CustomConfigPath)
		}

		if doConnection || c._notifierClient.ConfiguredForNotifierClientDial() {
			/*
				use default logging for the commented out handlers

				c._notifierClient.BeforeClientDialHandler
				c._notifierClient.AfterClientDialHandler
				c._notifierClient.BeforeClientCloseHandler
				c._notifierClient.AfterClientCloseHandler

				c._notifierClient.UnaryClientInterceptorHandlers
				c._notifierClient.StreamClientInterceptorHandlers

				c._notifierClient.ServiceAlertStartedHandler

				c._notifierClient.ServiceAlertSkippedHandler
			*/

			if !doConnection {
				c._notifierClient.ServiceHostOnlineHandler = func(host string, port uint) {
					if c._config != nil {
						if util.LenTrim(c._config.Target.ServiceName) > 0 && util.LenTrim(c._config.Target.NamespaceName) > 0 {
							cacheExpSeconds := c._config.Target.SdEndpointCacheExpires
							if cacheExpSeconds == 0 {
								cacheExpSeconds = 300
							}

							_cache.AddServiceEndpoints(strings.ToLower(c._config.Target.ServiceName+"."+c._config.Target.NamespaceName), []*serviceEndpoint{
								{
									SdType:      c._config.Target.ServiceDiscoveryType,
									Host:        host,
									Port:        port,
									InstanceId:  "", // not used
									ServiceId:   "", // not used
									Version:     c._config.Target.InstanceVersion,
									CacheExpire: time.Now().Add(time.Duration(cacheExpSeconds) * time.Second),
								},
							})

							c.setEndpoints(cacheGetLiveServiceEndpoints(strings.ToLower(c._config.Target.ServiceName+"."+c._config.Target.NamespaceName), c._config.Target.InstanceVersion, true))

							if e := c.UpdateLoadBalanceResolver(); e != nil {
								if z != nil {
									z.Errorf(e.Error())
								} else {
									log.Printf(e.Error())
								}
							}
						}
					}
				}

				c._notifierClient.ServiceHostOfflineHandler = func(host string, port uint) {
					if c._config != nil {
						if util.LenTrim(c._config.Target.ServiceName) > 0 && util.LenTrim(c._config.Target.NamespaceName) > 0 {
							cachePurgeServiceEndpointByHostAndPort(strings.ToLower(c._config.Target.ServiceName+"."+c._config.Target.NamespaceName), host, port)
						}

						c.setEndpoints(cacheGetLiveServiceEndpoints(strings.ToLower(c._config.Target.ServiceName+"."+c._config.Target.NamespaceName), c._config.Target.InstanceVersion, true)) // CHANGED

						if e := c.UpdateLoadBalanceResolver(); e != nil {
							if z != nil {
								z.Errorf(e.Error())
							} else {
								log.Printf(e.Error())
							}
						}
					}
				}

				c._notifierClient.ServiceAlertStoppedHandler = func(reason string) {
					if strings.Contains(strings.ToLower(reason), "transport is closing") {
						if c._notifierClient != nil && c._z != nil {
							if z != nil {
								z.Warnf("!!! Notifier Client Service Disconnected - Re-Attempting Connection in 5 Seconds...!!!")
							} else {
								log.Printf("!!! Notifier Client Service Disconnected - Re-Attempting Connection in 5 Seconds...!!!")
							}

							go func() { // reconnect asynchronously to avoid blocking callback
								for {
									time.Sleep(5 * time.Second)
									if c._notifierClient != nil {
										c._notifierClient.Close()
									}
									if e := c.DoNotifierAlertService(); e != nil {
										if z != nil {
											z.Errorf("... Reconnect Notifier Server Failed: %s (Will Retry in 5 Seconds)", e.Error())
										} else {
											log.Printf("... Reconnect Notifier Server Failed: %s (Will Retry in 5 Seconds)", e.Error())
										}
										continue
									}
									return
								}
							}()
						}
					} else if z != nil {
						z.Printf("--- Notifier Client Service Disconnected Normally: %s ---", reason)
					} else {
						log.Printf("--- Notifier Client Service Disconnected Normally: %s ---", reason)
					}
				}
			}

			if c._notifierClient == nil {
				return nil
			}

			// dial notifier client to notifier server endpoint and begin service operations
			if err = c._notifierClient.Dial(); err != nil {
				if c._notifierClient != nil {
					errorf("!!! Notifier Client Service Dial Failed: %s !!!", err.Error())
					c._notifierClient.Close()
				}
				return err
			} else {
				if err = c._notifierClient.Subscribe(c._notifierClient.ConfiguredSNSDiscoveryTopicArn()); err != nil {
					if c._notifierClient != nil {
						errorf("!!! Notifier Client Service Subscribe Failed: %s !!!", err.Error())
						c._notifierClient.Close()
					}
					return err
				} else {
					printf("~~~ Notifier Client Service Started ~~~")
				}
			}
		} else {
			printf("### Notifier Client Service Skipped, Not Yet Configured for Dial ###")
		}
	}

	return nil
}

// waitForWebServerReady is called after web server is expected to start,
// this function will wait a short time for web server startup success or timeout
func (c *Client) waitForWebServerReady(timeoutDuration ...time.Duration) error {
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
	errorf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Errorf(msg, args...)
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

	var timeout time.Duration
	if len(timeoutDuration) > 0 {
		timeout = timeoutDuration[0]
	} else {
		timeout = 5 * time.Second
	}

	expireDateTime := time.Now().Add(timeout)

	//
	// check if web server is ready and healthy via /health check
	//
	wg := sync.WaitGroup{}
	wg.Add(1)

	chanErrorInfo := make(chan string)
	healthUrl := c.WebServerConfig.WebServerLocalAddress + "/health"

	go func() {
		for {
			if status, _, e := rest.GET(healthUrl, nil); e != nil {
				errorf("Web Server Health Check Failed: %s", e.Error())
				wg.Done()
				chanErrorInfo <- "Web Server Health Check Failed: " + e.Error()
				return
			} else {
				if status == 200 {
					printf("Web Server Health OK")
					wg.Done()
					chanErrorInfo <- "OK"
					return
				} else {
					warnf("Web Server Not Ready!")
				}
			}

			time.Sleep(2500 * time.Millisecond)

			if time.Now().After(expireDateTime) {
				warnf("Web Server Health Check Timeout")
				wg.Done()
				chanErrorInfo <- "Web Server Health Check Failed: Timeout"
				return
			}
		}
	}()

	wg.Wait()

	printf("Web Server Heath Check Finalized...")

	errInfo := <-chanErrorInfo

	if errInfo == "OK" {
		// success - web server health check = ok
		return nil
	}

	// failure
	return fmt.Errorf(errInfo)
}

// waitForEndpointReady is called after Dial to check if target service is ready as reported by health probe
func (c *Client) waitForEndpointReady(timeoutDuration ...time.Duration) error {
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
	errorf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Errorf(msg, args...)
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

	var timeout time.Duration
	if len(timeoutDuration) > 0 {
		timeout = timeoutDuration[0]
	} else {
		timeout = 5 * time.Second
	}

	expireDateTime := time.Now().Add(timeout)

	// per-attempt timeout (cap at remaining time, min 1s)
	perAttempt := 2 * time.Second

	//
	// check if service is ready
	// wait for target service to respond with serving status before moving forward
	//
	wg := sync.WaitGroup{}
	wg.Add(1)

	chanErrorInfo := make(chan string)

	go func() {
		defer wg.Done()

		for {
			remaining := time.Until(expireDateTime)
			if remaining <= 0 {
				warnf("Health Status Check Timeout")
				chanErrorInfo <- "Health Status Check Failed: Timeout"
				return
			}
			attemptTimeout := perAttempt
			if remaining < attemptTimeout {
				attemptTimeout = remaining
			}

			if status, e := c.HealthProbe("", attemptTimeout); e != nil {
				errorf("Health Status Check Failed: %s", e.Error())
				chanErrorInfo <- "Health Status Check Failed: " + e.Error()
				return
			} else {
				if status == grpc_health_v1.HealthCheckResponse_SERVING {
					printf("Serving Status Detected")
					chanErrorInfo <- "OK"
					return
				} else {
					warnf("Not Serving!")
				}
			}

			time.Sleep(2500 * time.Millisecond)

			if time.Now().After(expireDateTime) {
				warnf("Health Status Check Timeout")
				chanErrorInfo <- "Health Status Check Failed: Timeout"
				return
			}
		}
	}()

	wg.Wait()

	printf("Heath Status Check Finalized...")

	errInfo := <-chanErrorInfo

	if errInfo == "OK" {
		// success - server service health = serving
		return nil
	}

	// failure
	return fmt.Errorf(errInfo)
}

// setupHealthManualChecker sets up the HealthChecker for manual use by HealthProbe method
func (c *Client) setupHealthManualChecker() {
	if c == nil {
		log.Println("setupHealthManualChecker(): Client Object Nil")
		return
	}

	if c._conn == nil {
		return
	}

	c._healthManualChecker, _ = health.NewHealthClient(c._conn)
}

// HealthProbe manually checks service serving health status
func (c *Client) HealthProbe(serviceName string, timeoutDuration ...time.Duration) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
	if c == nil {
		return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Client Object Nil")
	}

	z := c.ZLog()
	printf := func(msg string, args ...interface{}) {
		if z != nil {
			z.Printf(msg, args...)
		} else {
			log.Printf(msg, args...)
		}
	}

	if c._healthManualChecker == nil {
		if c._conn != nil {
			c.setupHealthManualChecker()

			if c._healthManualChecker == nil {
				return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Health Probe Failed: (Auto Instantiate) %s", "Health Manual Checker is Nil")
			}
		} else {
			return grpc_health_v1.HealthCheckResponse_NOT_SERVING, fmt.Errorf("Health Probe Failed: %s", "Health Manual Checker is Nil")
		}
	}

	printf("Health Probe - Manual Check Begin...")
	defer printf("... Health Probe - Manual Check End")

	return c._healthManualChecker.Check(serviceName, timeoutDuration...)
}

// GetState returns the current grpc client connection's state
func (c *Client) GetState() connectivity.State {
	if c == nil {
		log.Println("GetState(): Client Object Nil")
		return connectivity.Shutdown
	}

	if c._conn != nil {
		return c._conn.GetState()
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

	// clean up notifier client connection
	if c._notifierClient != nil {
		if c._notifierClient.NotifierClientAlertServicesStarted() {
			if err := c._notifierClient.Unsubscribe(); err != nil {
				errorf("!!! Notifier Client Alert Services Unsubscribe Failed: " + err.Error() + " !!!")
			}
		}

		c._notifierClient.Close()
		c._notifierClient = nil
	}

	// clean up client connection objects
	c._remoteAddress = ""

	if c._sqs != nil {
		c._sqs.Disconnect()
	}

	if c._sd != nil {
		c._sd.Disconnect()
	}

	if c._conn != nil {
		_ = c._conn.Close()
	}
}

// ClientConnection returns the currently loaded grpc client connection
func (c *Client) ClientConnection() grpc.ClientConnInterface {
	if c == nil {
		log.Println("ClientConnection(): Client Object Nil")
		return nil
	}

	return c._conn
}

// RemoteAddress gets the remote endpoint address currently connected to
func (c *Client) RemoteAddress() string {
	if c == nil {
		log.Println("RemoteAddress(): Client Object Nil")
		return ""
	}

	return c._remoteAddress
}

// connectSd will try to establish service discovery object to struct
func (c *Client) connectSd() error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	// skip CloudMap wiring when using direct discovery to avoid unnecessary AWS dependency
	if strings.EqualFold(c._config.Target.ServiceDiscoveryType, "direct") {
		c._sd = nil
		return nil
	}

	if util.LenTrim(c._config.Target.NamespaceName) > 0 && util.LenTrim(c._config.Target.ServiceName) > 0 && util.LenTrim(c._config.Target.Region) > 0 {
		c._sd = &cloudmap.CloudMap{
			AwsRegion: awsregion.GetAwsRegion(c._config.Target.Region),
		}

		if err := c._sd.Connect(); err != nil {
			return fmt.Errorf("Connect SD Failed: %s", err.Error())
		}
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
		cacheExpSeconds = 300
	}

	cacheExpires := time.Now().Add(time.Duration(cacheExpSeconds) * time.Second)

	switch c._config.Target.ServiceDiscoveryType {
	case "direct":
		return c.setDirectConnectEndpoint(cacheExpires, c._config.Target.DirectConnectIpPort)
	case "srv":
		fallthrough
	case "a":
		return c.setDnsDiscoveredIpPorts(cacheExpires, c._config.Target.ServiceDiscoveryType == "srv", c._config.Target.ServiceName,
			c._config.Target.NamespaceName, c._config.Target.InstancePort, forceRefresh)
	case "api":
		return c.setApiDiscoveredIpPorts(cacheExpires, c._config.Target.ServiceName, c._config.Target.NamespaceName, c._config.Target.InstanceVersion,
			int64(c._config.Target.SdInstanceMaxResult), c._config.Target.SdTimeout, forceRefresh)
	default:
		return fmt.Errorf("Unexpected Service Discovery Type: " + c._config.Target.ServiceDiscoveryType)
	}
}

func (c *Client) setDirectConnectEndpoint(cacheExpires time.Time, directIpPort string) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	host, portStr, err := net.SplitHostPort(directIpPort)
	if err != nil {
		return fmt.Errorf("Direct Connect target must include host and port (got %q): %v", directIpPort, err)
	}

	p, convErr := strconv.Atoi(portStr)
	if convErr != nil || p <= 0 || p > 65535 {
		return fmt.Errorf("Direct Connect port invalid (got %q): %v", portStr, convErr)
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
	found := cacheGetLiveServiceEndpoints(serviceName+"."+namespaceName, "", forceRefresh)
	if len(found) > 0 {
		c.setEndpoints(found)
		c._z.Printf("Using DNS Discovered Cache Hosts: (Service) " + serviceName + "." + namespaceName)
		for _, v := range c.endpointsSnapshot() {
			c._z.Printf("   - " + v.Host + ":" + util.UintToStr(v.Port) + ", Cache Expires: " + util.FormatDateTime(v.CacheExpire))
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
		ip := ""
		port := uint(0)

		if srv {
			// srv
			av := strings.Split(v, ":")
			if len(av) == 2 {
				ip = av[0]
				port = util.StrToUint(av[1])
			}

			if util.LenTrim(ip) == 0 || port == 0 || port > 65535 {
				return fmt.Errorf("SRV Host or Port From Service Discovery Not Valid: " + v)
			}
		} else {
			// a
			ip = v
			if instancePort == 0 || instancePort > 65535 {
				return fmt.Errorf("Configured Instance Port Not Valid: %d", instancePort)
			}
			port = instancePort
		}

		key := fmt.Sprintf("%s:%d", ip, port)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		endpoints = append(endpoints, &serviceEndpoint{
			SdType:      sdType,
			Host:        ip,
			Port:        port,
			CacheExpire: cacheExpires,
		})
	}

	c.setEndpoints(endpoints)
	cacheAddServiceEndpoints(serviceName+"."+namespaceName, c._endpoints)

	return nil
}

func (c *Client) setApiDiscoveredIpPorts(cacheExpires time.Time, serviceName string, namespaceName string, version string, maxCount int64, timeoutSeconds uint, forceRefresh bool) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
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
	found := cacheGetLiveServiceEndpoints(serviceName+"."+namespaceName, version, forceRefresh)
	if len(found) > 0 {
		c.setEndpoints(found)
		c._z.Printf("Using API Discovered Cache Hosts: (Service) " + serviceName + "." + namespaceName)
		for _, v := range c.endpointsSnapshot() {
			c._z.Printf("   - " + v.Host + ":" + util.UintToStr(v.Port) + ", Cache Expires: " + util.FormatDateTime(v.CacheExpire))
		}
		return nil
	}

	//
	// acquire api ip port from service discovery
	//
	if maxCount == 0 {
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
		if v.InstancePort == 0 || v.InstancePort > 65535 {
			return fmt.Errorf("Service Discovery Returned Invalid Port for %s:%d", v.InstanceIP, v.InstancePort)
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

	c.setEndpoints(endpoints)
	cacheAddServiceEndpoints(serviceName+"."+namespaceName, c._endpoints)

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

	if maxCount == 0 {
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

	if instanceList, err := registry.DiscoverInstances(c._sd, serviceName, namespaceName, false, customAttr, &maxCount, timeoutDuration...); err != nil {
		return []*serviceEndpoint{}, fmt.Errorf("Service Discovery By API Failed: " + err.Error())
	} else {
		for _, v := range instanceList {
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

	if c._sd != nil && c._config != nil && p != nil && p.SdType == "api" && util.LenTrim(p.ServiceId) > 0 && util.LenTrim(p.InstanceId) > 0 {
		c._z.Printf("De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Begin...")

		var timeoutDuration []time.Duration

		if c._config.Target.SdTimeout > 0 {
			timeoutDuration = append(timeoutDuration, time.Duration(c._config.Target.SdTimeout)*time.Second)
		}

		if operationId, err := registry.DeregisterInstance(c._sd, p.InstanceId, p.ServiceId, timeoutDuration...); err != nil {
			c._z.Errorf("... De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Failed: " + err.Error())
			return fmt.Errorf("De-Register Instance '"+p.Host+":"+util.UintToStr(p.Port)+"-"+p.InstanceId+"'Fail: %s", err.Error())
		} else {
			tryCount := 0

			time.Sleep(250 * time.Millisecond)

			for {
				if status, e := registry.GetOperationStatus(c._sd, operationId, timeoutDuration...); e != nil {
					c._z.Errorf("... De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Failed: " + e.Error())
					return fmt.Errorf("De-Register Instance '"+p.Host+":"+util.UintToStr(p.Port)+"-"+p.InstanceId+"'Fail: %s", e.Error())
				} else {
					if status == sdoperationstatus.Success {
						c._z.Printf("... De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' OK")
					} else {
						// wait 250 ms then retry, up until 20 counts of 250 ms (5 seconds)
						if tryCount < 20 {
							tryCount++
							c._z.Printf("... Checking De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Completion Status, Attempt " + strconv.Itoa(tryCount) + " (100ms)")
							time.Sleep(250 * time.Millisecond)
						} else {
							c._z.Errorf("... De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Failed: Operation Timeout After 5 Seconds")
							return fmt.Errorf("De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "'Fail When Operation Timed Out After 5 Seconds")
						}
					}
				}
			}
		}
	} else {
		return nil
	}
}

func (c *Client) unaryCircuitBreakerHandler(ctx context.Context, method string, req interface{}, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if c == nil {
		return fmt.Errorf("Client Object Nil")
	}

	if c._config.Grpc.CircuitBreakerEnabled {
		c._z.Printf("In - Unary Circuit Breaker Handler: " + method)

		c.cbMu.RLock()
		cb := c._circuitBreakers[method]
		c.cbMu.RUnlock()

		if cb == nil {
			c._z.Printf("... Creating Circuit Breaker for: " + method)

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

			c._z.Printf("... Circuit Breaker Created for: " + method)
		} else {
			c._z.Printf("... Using Cached Circuit Breaker Command: " + method)
		}

		_, gerr := cb.Exec(true, func(dataIn interface{}, ctx1 ...context.Context) (dataOut interface{}, err error) {
			c._z.Printf("Run Circuit Breaker Action for: " + method + "...")

			err = invoker(ctx, method, req, reply, cc, opts...)

			if err != nil {
				c._z.Errorf("!!! Circuit Breaker Action for " + method + " Failed: " + err.Error() + " !!!")
			} else {
				c._z.Printf("... Circuit Breaker Action for " + method + " Invoked")
			}
			return nil, err

		}, func(dataIn interface{}, errIn error, ctx1 ...context.Context) (dataOut interface{}, err error) {
			c._z.Warnf("Circuit Breaker Action for " + method + " Fallback...")
			c._z.Warnf("... Error = " + errIn.Error())

			return nil, errIn
		}, nil)

		return gerr
	}

	return invoker(ctx, method, req, reply, cc, opts...)
}

func (c *Client) streamCircuitBreakerHandler(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	if c == nil {
		return nil, fmt.Errorf("Client Object Nil")
	}

	if c._config.Grpc.CircuitBreakerEnabled {
		c._z.Printf("In - Stream Circuit Breaker Handler: " + method)

		c.cbMu.RLock()
		cb := c._circuitBreakers[method]
		c.cbMu.RUnlock()

		if cb == nil {
			c._z.Printf("... Creating Circuit Breaker for: " + method)

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
				return streamer(ctx, desc, cc, method, opts...)
			}

			c.cbMu.Lock()
			if c._circuitBreakers == nil { // defensive init
				c._circuitBreakers = map[string]circuitbreaker.CircuitBreakerIFace{}
			}
			c._circuitBreakers[method] = cb
			c.cbMu.Unlock()

			c._z.Printf("... Circuit Breaker Created for: " + method)
		} else {
			c._z.Printf("... Using Cached Circuit Breaker Command: " + method)
		}

		gres, gerr := cb.Exec(true, func(dataIn interface{}, ctx1 ...context.Context) (dataOut interface{}, err error) {
			c._z.Printf("Run Circuit Breaker Action for: " + method + "...")

			dataOut, err = streamer(ctx, desc, cc, method, opts...)

			if err != nil {
				c._z.Errorf("!!! Circuit Breaker Action for " + method + " Failed: " + err.Error() + " !!!")
			} else {
				c._z.Printf("... Circuit Breaker Action for " + method + " Invoked")
			}
			return dataOut, err

		}, func(dataIn interface{}, errIn error, ctx1 ...context.Context) (dataOut interface{}, err error) {
			c._z.Warnf("Circuit Breaker Action for " + method + " Fallback...")
			c._z.Warnf("... Error = " + errIn.Error())

			return nil, errIn
		}, nil)

		if gres != nil {
			if cs, ok := gres.(grpc.ClientStream); ok {
				return cs, gerr
			}
			return nil, fmt.Errorf("Assert grpc.ClientStream Failed")
		}
		return nil, gerr
	}

	return streamer(ctx, desc, cc, method, opts...)
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
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
			}
		}()

		md := metadata.New(nil)
		md.Set("x-amzn-seg-id", seg.Seg.ID)
		md.Set("x-amzn-tr-id", seg.Seg.TraceID)

		// attach tracing headers to outgoing context
		ctx = metadata.NewOutgoingContext(ctx, md)

		err = invoker(ctx, method, req, reply, cc, opts...)
		return err
	}

	return invoker(ctx, method, req, reply, cc, opts...)
}

func (c *Client) streamXRayTracerHandler(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (cs grpc.ClientStream, err error) {
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
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
			}
		}()

		md := metadata.New(nil)
		md.Set("x-amzn-seg-id", seg.Seg.ID)
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

func (c *Client) startWebServer() error {
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

	server := ws.NewWebServer(c.WebServerConfig.AppName, c.WebServerConfig.ConfigFileName, c.WebServerConfig.CustomConfigPath)

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
	c.WebServerConfig.CleanUp = func() {
		server.RemoveDNSRecordset()
	}
	log.Println("Web Server Host Starting On: " + c.WebServerConfig.WebServerLocalAddress)

	// serve web server
	if err := server.Serve(); err != nil {
		server.RemoveDNSRecordset()
		return fmt.Errorf("Start Web Server Failed: (Serve Error) %s", err)
	}

	return nil
}
