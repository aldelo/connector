package client

/*
 * Copyright 2020 Aldelo, LP
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
	util "github.com/aldelo/common"
	"github.com/aldelo/common/crypto"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/cloudmap"
	"github.com/aldelo/common/wrapper/sns"
	"github.com/aldelo/common/wrapper/sqs"
	data "github.com/aldelo/common/wrapper/zap"
	"github.com/aldelo/connector/adapters/circuitbreaker"
	"github.com/aldelo/connector/adapters/circuitbreaker/plugins"
	"github.com/aldelo/connector/adapters/health"
	"github.com/aldelo/connector/adapters/loadbalancer"
	"github.com/aldelo/connector/adapters/metadata"
	"github.com/aldelo/connector/adapters/notification"
	"github.com/aldelo/connector/adapters/queue"
	"github.com/aldelo/connector/adapters/registry"
	"github.com/aldelo/connector/adapters/registry/sdoperationstatus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/stats"
	"log"
	"strconv"
	"strings"
	"time"
)

// Client represents a gRPC client's connection and entry point
//
// note:
//		1) Using Compressor with RPC
//			a) import "google.golang.org/grpc/encoding/gzip"
//			b) in RPC Call, pass grpc.UseCompressor(gzip.Name)) in the third parameter
//					example: RPCCall(ctx, &pb.Request{...}, grpc.UseCompressor(gzip.Name))
type Client struct {
	// client properties
	AppName string
	ConfigFileName string
	CustomConfigPath string

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
	_config *Config

	// service discovery object cached
	_sd *cloudmap.CloudMap
	_sqs *sqs.SQS
	_sns *sns.SNS

	// define circuit breaker commands
	_circuitBreakers map[string]circuitbreaker.CircuitBreakerIFace

	// discovered endpoints for client load balancer use
	_endpoints []*ServiceEndpoint

	// instantiated internal objects
	_conn *grpc.ClientConn
	_remoteAddress string

	// upon dial completion successfully,
	// auto instantiate a manual help checker
	_healthManualChecker *health.HealthClient

	// *** Setup by Dial Action ***
	// helper for creating metadata context,
	// and evaluate metadata header or trailer value when received from rpc
	MetadataHelper *metadata.MetaClient
}

// ServiceEndpoint represents a specific service endpoint connection target
type ServiceEndpoint struct {
	SdType string	// srv, api, direct
	Host string
	Port uint

	Healthy bool
	ServingStatus grpc_health_v1.HealthCheckResponse_ServingStatus

	InstanceId string
	ServiceId string
	Version string

	CacheExpire time.Time
	LastHealthCheck time.Time
}

// create client
func NewClient(appName string, configFileName string) *Client {
	return &Client{
		AppName: appName,
		ConfigFileName: configFileName,
	}
}

// readConfig will read in config data
func (c *Client) readConfig() error {
	c._config = &Config{
		AppName: c.AppName,
		ConfigFileName: c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,
	}

	if err := c._config.Read(); err != nil {
		return fmt.Errorf("Read Config Failed: %s", err.Error())
	}

	if c._config.Target.InstancePort > 65535 {
		return fmt.Errorf("Configured Instance Port Not Valid: %s", "Tcp Port Max is 65535")
	}

	return nil
}

// buildDialOptions returns slice of dial options built from client struct fields
func (c *Client) buildDialOptions(loadBalancerPolicy string) (opts []grpc.DialOption, err error) {
	if c._config == nil {
		return []grpc.DialOption{}, fmt.Errorf("Config Data Not Loaded")
	}

	//
	// config client options
	//

	// set tls credential dial option
	if util.LenTrim(c._config.Grpc.ServerCACertFiles) > 0 {
		tls := new(crypto.TlsConfig)
		if tc, e := tls.GetClientTlsConfig(strings.Split(c._config.Grpc.ServerCACertFiles, ","), c._config.Grpc.ClientCertFile, c._config.Grpc.ClientKeyFile); e != nil {
			return []grpc.DialOption{}, fmt.Errorf("Set Dial Option Client TLS Failed: %s", e.Error())
		} else {
			opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tc)))
		}
	} else {
		// if not tls secured, use inSecure dial option
		opts = append(opts, grpc.WithInsecure())
	}

	/*
	// set per rpc auth via oauth2
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
	opts = append(opts, grpc.WithBlock())

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
			Backoff: backoff.DefaultConfig,
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
		log.Println("Setup Unary Circuit Breaker Interceptor")
		c.UnaryClientInterceptors = append(c.UnaryClientInterceptors, c.unaryCircuitBreakerHandler)
	}

	count := len(c.UnaryClientInterceptors)

	if count == 1 {
		opts = append(opts, grpc.WithUnaryInterceptor(c.UnaryClientInterceptors[0]))
	} else if count > 1 {
		opts = append(opts, grpc.WithChainUnaryInterceptor(c.UnaryClientInterceptors...))
	}

	// add stream client interceptors
	if c._config.Grpc.CircuitBreakerEnabled {
		log.Println("Setup Stream Circuit Breaker Interceptor")
		c.StreamClientInterceptors = append(c.StreamClientInterceptors, c.streamCircuitBreakerHandler)
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

	//
	// complete
	//
	err = nil
	return
}

// Dial will dial grpc service and establish client connection
func (c *Client) Dial(ctx context.Context) error {
	c._remoteAddress = ""

	// read client config data in
	if err := c.readConfig(); err != nil {
		return err
	}

	log.Println("Client " + c._config.AppName + " Starting to Connect with " + c._config.Target.ServiceName + "." + c._config.Target.NamespaceName + "...")

	// setup sqs and sns if configured
	if c._config.Grpc.UseSQS {
		c._sqs, _ = queue.NewQueueAdapter(awsregion.GetAwsRegion(c._config.Target.Region), nil)
	}

	if c._config.Grpc.UseSNS {
		c._sns, _ = notification.NewNotificationAdapter(awsregion.GetAwsRegion(c._config.Target.Region), nil)
	}

	// circuit breakers prep
	c._circuitBreakers = map[string]circuitbreaker.CircuitBreakerIFace{}

	// connect sd
	if err := c.connectSd(); err != nil {
		return err
	}

	// discover service endpoints
	if err := c.discoverEndpoints(); err != nil {
		return err
	} else if len(c._endpoints) == 0 {
		return fmt.Errorf("No Service Endpoints Discovered for " + c._config.Target.ServiceName + "." + c._config.Target.NamespaceName)
	}

	log.Println("... Service Discovery for " + c._config.Target.ServiceName + "." + c._config.Target.NamespaceName + " Found " + strconv.Itoa(len(c._endpoints)) + " Endpoints:")

	// get endpoint addresses
	endpointAddrs := []string{}

	for i, ep := range c._endpoints {
		endpointAddrs = append(endpointAddrs, fmt.Sprintf("%s:%s", ep.Host, util.UintToStr(ep.Port)))

		info := strconv.Itoa(i+1) + ") "
		info += ep.SdType + "=" + ep.Host + ":" + util.UintToStr(ep.Port) + ", "
		info += "Version=" + ep.Version + ", "
		info += "Status=" + ep.ServingStatus.String() + ", "
		info += "LastHealth=" + util.FormatDateTime(ep.LastHealthCheck) + ", "
		info += "CacheExpires=" + util.FormatDateTime(ep.CacheExpire)

		log.Println("       - " + info)
	}

	// setup resolver and setup load balancer
	var target string
	var loadBalancerPolicy string

	if c._config.Target.ServiceDiscoveryType != "direct" {
		var err error

		target, loadBalancerPolicy, err = loadbalancer.WithRoundRobin(fmt.Sprintf("roundrobin.%s.%s", c._config.Target.ServiceName, c._config.Target.NamespaceName), endpointAddrs)

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
			log.Println("Before gRPC Client Dial Begin...")

			c.BeforeClientDial(c)

			log.Println("... Before gRPC Client Dial End")
		}

		defer func() {
			if c.AfterClientDial != nil {
				log.Println("After gRPC Client Dial Begin...")

				c.AfterClientDial(c)

				log.Println("... After gRPC Client Dial End")
			}
		}()

		log.Println("Dialing gRPC Service @ " + target + "...")

		dialSec := c._config.Grpc.DialMinConnectTimeout
		if dialSec == 0 {
			dialSec = 5
		}

		ctxWithTimeout, cancel := context.WithTimeout(ctx, time.Duration(dialSec)*time.Second)
		defer cancel()

		if c._conn, err = grpc.DialContext(ctxWithTimeout, target, opts...); err != nil {
			log.Println("Dial Failed: " + err.Error())
			return fmt.Errorf("gRPC Client Dial Service Endpoint %s Failed: %s", target, err.Error())
		} else {
			// dial grpc service endpoint success
			log.Println("Dial Successful")

			c._remoteAddress = target

			log.Println("Remote Address = " + target)

			c.setupHealthManualChecker()
			c.MetadataHelper = new(metadata.MetaClient)

			log.Println("... gRPC Service @ " + target + " [" + c._remoteAddress + "] Connected")
			return nil
		}
	}
}

// setupHealthManualChecker sets up the HealthChecker for manual use by HealthProbe method
func (c *Client) setupHealthManualChecker() {
	if c._conn == nil {
		return
	}

	c._healthManualChecker, _ = health.NewHealthClient(c._conn)
}

// HealthProbe manually checks service serving health status
func (c *Client) HealthProbe(serviceName string, timeoutDuration ...time.Duration) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
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

	log.Println("Health Probe - Manual Check Begin...")
	defer log.Println("... Health Probe - Manual Check End")

	return c._healthManualChecker.Check(serviceName, timeoutDuration...)
}

// GetState returns the current grpc client connection's state
func (c *Client) GetState() connectivity.State {
	if c._conn != nil {
		return c._conn.GetState()
	} else {
		return connectivity.Shutdown
	}
}

// Close will close grpc client connection
func (c *Client) Close() {
	if c.BeforeClientClose != nil {
		log.Println("Before gRPC Client Close Begin...")

		c.BeforeClientClose(c)

		log.Println("... Before gRPC Client Close End")
	}

	defer func() {
		if c.AfterClientClose != nil {
			log.Println("After gRPC Client Close Begin...")

			c.AfterClientClose(c)

			log.Println("... After gRPC Client Close End")
		}
	}()

	c._remoteAddress = ""

	if c._sqs != nil {
		c._sqs.Disconnect()
	}

	if c._sns != nil {
		c._sns.Disconnect()
	}

	if c._sd != nil {
		c._sd.Disconnect()
	}

	if c._conn != nil {
		_ = c._conn.Close()
	}
}

// RemoteAddress gets the remote endpoint address currently connected to
func (c *Client) RemoteAddress() string {
	return c._remoteAddress
}

// connectSd will try to establish service discovery object to struct
func (c *Client) connectSd() error {
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
func (c *Client) discoverEndpoints() error {
	if c._config == nil {
		return fmt.Errorf("Config Data Not Loaded")
	}

	c._endpoints = []*ServiceEndpoint{}

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
										 c._config.Target.NamespaceName, c._config.Target.InstancePort)
	case "api":
		return c.setApiDiscoveredIpPorts(cacheExpires, c._config.Target.ServiceName, c._config.Target.NamespaceName, c._config.Target.InstanceVersion,
										 int64(c._config.Target.SdInstanceMaxResult), c._config.Target.SdTimeout)
	default:
		return fmt.Errorf("Unexpected Service Discovery Type: " + c._config.Target.ServiceDiscoveryType)
	}
}

func (c *Client) setDirectConnectEndpoint(cacheExpires time.Time, directIpPort string) error {
	v := strings.Split(directIpPort, ":")
	ip := ""
	port := uint(0)

	if len(v) == 2 {
		ip = v[0]
		port = util.StrToUint(v[1])
	} else {
		ip = directIpPort
	}

	if util.LenTrim(ip) == 0 || port == 0 {
		return fmt.Errorf("Direct Connect IP or Port Not Defined in Config")
	}

	c._endpoints = append(c._endpoints, &ServiceEndpoint{
		SdType:  "direct",
		Host: ip,
		Port: port,
		Healthy: true,
		ServingStatus: grpc_health_v1.HealthCheckResponse_SERVING,		// initial discovery assumes serving
		CacheExpire: cacheExpires,
		LastHealthCheck: time.Time{},
	})

	return nil
}

func (c *Client) setDnsDiscoveredIpPorts(cacheExpires time.Time, srv bool, serviceName string, namespaceName string, instancePort uint) error {
	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("Service Name Not Defined in Config (SRV / A SD)")
	}

	if util.LenTrim(namespaceName) == 0 {
		return fmt.Errorf("Namespace Name Not Defined in Config (SRV / A SD)")
	}

	if !srv {
		if instancePort == 0 {
			return fmt.Errorf("Instance Port Required in Config When Service Discovery Type is DNS Record Type A")
		}
	}

	if ipList, err := registry.DiscoverDnsIps(serviceName + "." + namespaceName, srv); err != nil {
		return fmt.Errorf("Service Discovery By DNS Failed: " + err.Error())
	} else {
		sdType := ""

		if srv {
			sdType = "srv"
		} else {
			sdType = "a"
		}

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

				if util.LenTrim(ip) == 0 || port == 0 {
					return fmt.Errorf("SRV Host or Port From Service Discovery Not Valid: " + v)
				}
			} else {
				// a
				ip = v
				port = c._config.Target.InstancePort
			}

			c._endpoints = append(c._endpoints, &ServiceEndpoint{
				SdType:  sdType,
				Host: ip,
				Port: port,
				Healthy: true,
				ServingStatus: grpc_health_v1.HealthCheckResponse_SERVING,		// initial discovery assumes serving
				CacheExpire: cacheExpires,
				LastHealthCheck: time.Time{},
			})
		}

		return nil
	}
}

func (c *Client) setApiDiscoveredIpPorts(cacheExpires time.Time, serviceName string, namespaceName string, version string, maxCount int64, timeoutSeconds uint) error {
	if c._sd == nil {
		return fmt.Errorf("Service Discovery Client Not Connected")
	}

	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("Service Name Not Defined in Config (API SD)")
	}

	if util.LenTrim(namespaceName) == 0 {
		return fmt.Errorf("Namespace Name Not Defined in Config (API SD)")
	}

	if maxCount == 0 {
		maxCount = 100
	}

	var timeoutDuration []time.Duration

	if timeoutSeconds > 0 {
		timeoutDuration = append(timeoutDuration, time.Duration(timeoutSeconds) * time.Second)
	}

	customAttr := map[string]string{}

	if util.LenTrim(version) > 0 {
		customAttr["INSTANCE_VERSION"] = version
	} else {
		customAttr = nil
	}

	if instanceList, err := registry.DiscoverInstances(c._sd, serviceName, namespaceName, true, customAttr, &maxCount, timeoutDuration...); err != nil {
		return fmt.Errorf("Service Discovery By API Failed: " + err.Error())
	} else {
		for _, v := range instanceList {
			c._endpoints = append(c._endpoints, &ServiceEndpoint{
				SdType:  "api",
				Host: v.InstanceIP,
				Port: v.InstancePort,
				Healthy: v.InstanceHealthy,
				ServingStatus: grpc_health_v1.HealthCheckResponse_SERVING,		// initial discovery assumes serving
				InstanceId: v.InstanceId,
				ServiceId: v.ServiceId,
				Version: v.InstanceVersion,
				CacheExpire: cacheExpires,
				LastHealthCheck: time.Time{},
			})
		}

		return nil
	}
}

// findUnhealthyInstances will call cloud map sd to discover unhealthy instances, a slice of unhealthy instances is returned
func (c *Client) findUnhealthyEndpoints(serviceName string, namespaceName string, version string, maxCount int64, timeoutSeconds uint) (unhealthyList []*ServiceEndpoint, err error) {
	if c._sd == nil {
		return []*ServiceEndpoint{}, fmt.Errorf("Service Discovery Client Not Connected")
	}

	if util.LenTrim(serviceName) == 0 {
		return []*ServiceEndpoint{}, fmt.Errorf("Service Name Not Defined in Config (API SD)")
	}

	if util.LenTrim(namespaceName) == 0 {
		return []*ServiceEndpoint{}, fmt.Errorf("Namespace Name Not Defined in Config (API SD)")
	}

	if maxCount == 0 {
		maxCount = 100
	}

	var timeoutDuration []time.Duration

	if timeoutSeconds > 0 {
		timeoutDuration = append(timeoutDuration, time.Duration(timeoutSeconds) * time.Second)
	}

	customAttr := map[string]string{}

	if util.LenTrim(version) > 0 {
		customAttr["INSTANCE_VERSION"] = version
	} else {
		customAttr = nil
	}

	if instanceList, err := registry.DiscoverInstances(c._sd, serviceName, namespaceName, false, customAttr, &maxCount, timeoutDuration...); err != nil {
		return []*ServiceEndpoint{}, fmt.Errorf("Service Discovery By API Failed: " + err.Error())
	} else {
		for _, v := range instanceList {
			unhealthyList = append(unhealthyList, &ServiceEndpoint{
				SdType:  "api",
				Host: v.InstanceIP,
				Port: v.InstancePort,
				Healthy: v.InstanceHealthy,
				ServingStatus: grpc_health_v1.HealthCheckResponse_NOT_SERVING,	// upon discovery serve status is yet unknown, need health probe
				InstanceId: v.InstanceId,
				ServiceId: v.ServiceId,
				Version: v.InstanceVersion,
				CacheExpire: time.Time{},
				LastHealthCheck: time.Time{},
			})
		}

		return unhealthyList, nil
	}
}

// updateHealth will update instance health
func (c *Client) updateHealth(p *ServiceEndpoint, healthy bool) error {
	if c._sd != nil && c._config != nil && p != nil && p.SdType == "api" && util.LenTrim(p.ServiceId) > 0 && util.LenTrim(p.InstanceId) > 0 {
		var timeoutDuration []time.Duration

		if c._config.Target.SdTimeout > 0 {
			timeoutDuration = append(timeoutDuration, time.Duration(c._config.Target.SdTimeout) * time.Second)
		}

		return registry.UpdateHealthStatus(c._sd, p.InstanceId, p.ServiceId, healthy)
	} else {
		return nil
	}
}

// deregisterInstance will remove instance from cloudmap and route 53
func (c *Client) deregisterInstance(p *ServiceEndpoint) error {
	if c._sd != nil && c._config != nil && p != nil && p.SdType == "api" && util.LenTrim(p.ServiceId) > 0 && util.LenTrim(p.InstanceId) > 0 {
		log.Println("De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Begin...")

		var timeoutDuration []time.Duration

		if c._config.Target.SdTimeout > 0 {
			timeoutDuration = append(timeoutDuration, time.Duration(c._config.Target.SdTimeout) * time.Second)
		}

		if operationId, err := registry.DeregisterInstance(c._sd, p.InstanceId, p.ServiceId, timeoutDuration...); err != nil {
			log.Println("... De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Failed: " + err.Error())
			return fmt.Errorf("De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "'Fail: %s", err.Error())
		} else {
			tryCount := 0

			time.Sleep(250*time.Millisecond)

			for {
				if status, e := registry.GetOperationStatus(c._sd, operationId, timeoutDuration...); e != nil {
					log.Println("... De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Failed: " + e.Error())
					return fmt.Errorf("De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "'Fail: %s", e.Error())
				} else {
					if status == sdoperationstatus.Success {
						log.Println("... De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' OK")
					} else {
						// wait 250 ms then retry, up until 20 counts of 250 ms (5 seconds)
						if tryCount < 20 {
							tryCount++
							log.Println("... Checking De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Completion Status, Attempt " + strconv.Itoa(tryCount) + " (100ms)")
							time.Sleep(250*time.Millisecond)
						} else {
							log.Println("... De-Register Instance '" + p.Host + ":" + util.UintToStr(p.Port) + "-" + p.InstanceId + "' Failed: Operation Timeout After 5 Seconds")
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
	if c._config.Grpc.CircuitBreakerEnabled {
		log.Println("In - Unary Circuit Breaker Handler: " + method)

		cb := c._circuitBreakers[method]

		if cb == nil {
			log.Println("... Creating Circuit Breaker for: " + method)

			z := &data.ZapLog{
				DisableLogger: false,
				OutputToConsole: false,
				AppName: c.AppName,
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
				log.Println("!!! Create Circuit Breaker for: " + method + " Failed !!!")
				log.Println("Will Skip Circuit Breaker and Continue Execution: " + e.Error())

				return invoker(ctx, method, req, reply, cc, opts...)
			} else {
				log.Println("... Circuit Breaker Created for: " + method)

				c._circuitBreakers[method] = cb
			}
		} else {
			log.Println("... Using Cached Circuit Breaker Command: " + method)
		}

		_, gerr := cb.Exec(true, func(dataIn interface{}, ctx1 ...context.Context) (dataOut interface{}, err error) {
								log.Println("Run Circuit Breaker Action for: " + method + "...")

								err = invoker(ctx, method, req, reply, cc, opts...)

								if err != nil {
									log.Println("!!! Circuit Breaker Action for " + method + " Failed: " + err.Error() + " !!!")
								} else {
									log.Println("... Circuit Breaker Action for " + method + " Invoked")
								}
								return nil, err

							}, func(dataIn interface{}, errIn error, ctx1 ...context.Context) (dataOut interface{}, err error) {
								log.Println("Circuit Breaker Action for " + method + " Fallback...")
								log.Println("... Error = " + errIn.Error())

								return nil, errIn
							}, nil)

		return gerr
	} else {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func (c *Client) streamCircuitBreakerHandler(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	if c._config.Grpc.CircuitBreakerEnabled {
		log.Println("In - Stream Circuit Breaker Handler: " + method)

		cb := c._circuitBreakers[method]

		if cb == nil {
			log.Println("... Creating Circuit Breaker for: " + method)

			z := &data.ZapLog{
				DisableLogger: false,
				OutputToConsole: false,
				AppName: c.AppName,
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
				log.Println("!!! Create Circuit Breaker for: " + method + " Failed !!!")
				log.Println("Will Skip Circuit Breaker and Continue Execution: " + e.Error())

				return streamer(ctx, desc, cc, method, opts...)
			} else {
				log.Println("... Circuit Breaker Created for: " + method)

				c._circuitBreakers[method] = cb
			}
		} else {
			log.Println("... Using Cached Circuit Breaker Command: " + method)
		}

		gres, gerr := cb.Exec(true, func(dataIn interface{}, ctx1 ...context.Context) (dataOut interface{}, err error) {
			log.Println("Run Circuit Breaker Action for: " + method + "...")

			dataOut, err = streamer(ctx, desc, cc, method, opts...)

			if err != nil {
				log.Println("!!! Circuit Breaker Action for " + method + " Failed: " + err.Error() + " !!!")
			} else {
				log.Println("... Circuit Breaker Action for " + method + " Invoked")
			}
			return dataOut, err

		}, func(dataIn interface{}, errIn error, ctx1 ...context.Context) (dataOut interface{}, err error) {
			log.Println("Circuit Breaker Action for " + method + " Fallback...")
			log.Println("... Error = " + errIn.Error())

			return nil, errIn
		}, nil)

		if gres != nil {
			if cs, ok := gres.(grpc.ClientStream); ok {
				return cs, gerr
			} else {
				return nil, fmt.Errorf("Assert grpc.ClientStream Failed")
			}
		} else {
			return nil, gerr
		}
	} else {
		return streamer(ctx, desc, cc, method, opts...)
	}
}

