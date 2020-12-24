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
	"github.com/aldelo/common/rest"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/cloudmap"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/sns"
	"github.com/aldelo/common/wrapper/sns/snsprotocol"
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
	"github.com/aldelo/connector/notifierclient"
	ws "github.com/aldelo/connector/webserver"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/stats"
	"log"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

//
// client side cache
//
var _cache *Cache

func init() {
	_cache = new(Cache)
}

// Client represents a gRPC client's connection and entry point
//
// note:
//		1) Using Compressor with RPC
//			a) import "google.golang.org/grpc/encoding/gzip"
//			b) in RPC Call, pass grpc.UseCompressor(gzip.Name)) in the third parameter
//					example: RPCCall(ctx, &pb.Request{...}, grpc.UseCompressor(gzip.Name))
//
//		2) Notifier Client yaml
//			a) xyz-notifier-client.yaml
//					where xyz is the target gRPC service endpoint name
type Client struct {
	// client properties
	AppName string
	ConfigFileName string
	CustomConfigPath string

	// web server config
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

	// service discovery object cached
	_sd *cloudmap.CloudMap
	_sqs *sqs.SQS
	_sns *sns.SNS

	// define circuit breaker commands
	_circuitBreakers map[string]circuitbreaker.CircuitBreakerIFace

	// discovered endpoints for client load balancer use
	_endpoints []*serviceEndpoint

	// instantiated internal objects
	_conn *grpc.ClientConn
	_remoteAddress string

	// upon dial completion successfully,
	// auto instantiate a manual help checker
	_healthManualChecker *health.HealthClient

	// upon dial completion successfully,
	// auto instantiate a notifier client connection to the notifier server,
	// for auto service discovery callback notifications
	_notifierClient *notifierclient.NotifierClient

	// *** Setup by Dial Action ***
	// helper for creating metadata context,
	// and evaluate metadata header or trailer value when received from rpc
	MetadataHelper *metadata.MetaClient
}

// serviceEndpoint represents a specific service endpoint connection target
type serviceEndpoint struct {
	SdType string	// srv, api, direct

	Host string
	Port uint

	InstanceId string
	ServiceId string
	Version string

	CacheExpire time.Time
}

// create client
func NewClient(appName string, configFileName string, customConfigPath string) *Client {
	return &Client{
		AppName: appName,
		ConfigFileName: configFileName,
		CustomConfigPath: customConfigPath,
	}
}

// readConfig will read in config data
func (c *Client) readConfig() error {
	c._config = &config{
		AppName: c.AppName,
		ConfigFileName: c.ConfigFileName,
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

// PreloadConfigData will load the config data before Dial()
func (c *Client) PreloadConfigData() error {
	if err := c.readConfig(); err != nil {
		return err
	} else {
		return nil
	}
}

// ConfiguredDialMinConnectTimeoutSeconds gets the timeout seconds from config yaml
func (c *Client) ConfiguredDialMinConnectTimeoutSeconds() uint {
	if c._config != nil {
		if c._config.Grpc.DialMinConnectTimeout > 0 {
			return c._config.Grpc.DialMinConnectTimeout
		}
	}

	return 5
}

// ConfiguredForClientDial checks if the config yaml is ready for client dial operation
func (c *Client) ConfiguredForClientDial() bool {
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
	if c._config == nil {
		return ""
	}

	return c._config.Topics.SnsDiscoveryTopicArn
}

// Dial will dial grpc service and establish client connection
func (c *Client) Dial(ctx context.Context) error {
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

	log.Println("Client " + c._config.AppName + " Starting to Connect with " + c._config.Target.ServiceName + "." + c._config.Target.NamespaceName + "...")

	// setup sqs and sns if configured
	if util.LenTrim(c._config.Queues.SqsDiscoveryQueueUrl) > 0 || util.LenTrim(c._config.Queues.SqsLoggerQueueUrl) > 0 || util.LenTrim(c._config.Queues.SqsTracerQueueUrl) > 0 || util.LenTrim(c._config.Queues.SqsMonitorQueueUrl) > 0 {
		var e error
		if c._sqs, e = queue.NewQueueAdapter(awsregion.GetAwsRegion(c._config.Target.Region), nil); e != nil {
			log.Println("Get SQS Queue Adapter Failed: " + e.Error())
			c._sqs = nil
		}
	} else {
		c._sqs = nil
	}

	if util.LenTrim(c._config.Topics.SnsDiscoveryTopicArn) > 0 || util.LenTrim(c._config.Topics.SnsLoggerTopicArn) > 0 || util.LenTrim(c._config.Topics.SnsTracerTopicArn) > 0 || util.LenTrim(c._config.Topics.SnsMonitorTopicArn) > 0 {
		var e error
		if c._sns, e = notification.NewNotificationAdapter(awsregion.GetAwsRegion(c._config.Target.Region), nil); e != nil {
			log.Println("Get SNS Notification Adapter Failed: " + e.Error())
			c._sns = nil
		}
	} else {
		c._sns = nil
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
		info += "CacheExpires=" + util.FormatDateTime(ep.CacheExpire)

		log.Println("       - " + info)
	}

	// setup resolver and setup load balancer
	var target string
	var loadBalancerPolicy string

	if c._config.Target.ServiceDiscoveryType != "direct" {
		var err error

		target, loadBalancerPolicy, err = loadbalancer.WithRoundRobin("clb", fmt.Sprintf("%s.%s", c._config.Target.ServiceName, c._config.Target.NamespaceName), endpointAddrs)

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

			if c.WaitForServerReady {
				if e := c.waitForEndpointReady(time.Duration(dialSec)*time.Second); e != nil {
					// health probe failed
					_ = c._conn.Close()
					return fmt.Errorf("gRPC Service Server Not Ready: " + e.Error())
				}
			}

			// dial successful, now start web server for notification callbacks (webhook)
			if c.WebServerConfig != nil {
				log.Println("Starting Http Web Server...")
				startWebServerFail := make(chan bool)

				go func() {
					//
					// start http web server
					//
					if err := c.startWebServer(); err != nil {
						log.Printf("!!! Serve Http Web Server %s Failed: %s !!!\n", c.WebServerConfig.AppName, err)
						startWebServerFail <- true
					} else {
						log.Println("... Http Web Server Quit Command Received")
					}
				}()

				// give slight time delay to allow time slice for non blocking code to complete in goroutine above
				time.Sleep(150*time.Millisecond)

				select {
				case <- startWebServerFail:
					log.Println("... Http Web Server Fail to Start")
				default:
					// wait short time to check if web server was started up successfully
					if e := c.waitForWebServerReady(time.Duration(c._config.Target.SdTimeout)*time.Second); e != nil {
						// web server error
						log.Printf("!!! Http Web Server %s Failed: %s !!!\n", c.WebServerConfig.AppName, e)
					} else {
						// web server ok
						log.Printf("... Http Web Server Started: %s\n", c.WebServerConfig.WebServerLocalAddress)

						// if sns topic arn is set for discovery service, subscribe to the topic (if not yet subscribed)
						c.subscribeToSNS("Discovery", c._config.Topics.SnsDiscoveryTopicArn, c._config.Topics.SnsDiscoverySubscriptionArn, c._config.SetSnsDiscoverySubscriptionArn)
					}
				}
			}

			// finally, run notifier client to subscribe for notification callbacks
			// the notifier client uses the same client config yaml, but a copy of it to keep the scope separated
			// within the notifier client config yaml, named xyz-notifier-client.yaml, where xyz is the endpoint service name,
			// the discovery topicArn as pre-created on aws is stored within, this enables callback for this specific topicArn from notifier server,
			// note that the service discovery of notifier client is to the notifier server cluster
			if util.FileExists(path.Join(c.CustomConfigPath, c.ConfigFileName + "-notifier-client.yaml")) {
				c._notifierClient = notifierclient.NewNotifierClient(c.AppName+"-Notifier-Client", c.ConfigFileName+"-notifier-client", c.CustomConfigPath)

				if c._notifierClient.ConfiguredForNotifierClientDial() {
					/*
					use default logging for the commented out handlers

					c._notifierClient.BeforeClientDialHandler
					c._notifierClient.AfterClientDialHandler
					c._notifierClient.BeforeClientCloseHandler
					c._notifierClient.AfterClientCloseHandler

					c._notifierClient.UnaryClientInterceptorHandlers
					c._notifierClient.StreamClientInterceptorHandlers

					c._notifierClient.ServiceAlertStartedHandler
					c._notifierClient.ServiceAlertStoppedHandler

					c._notifierClient.ServiceAlertSkippedHandler

					c._notifierClient.ServiceHostOnlineHandler
					c._notifierClient.ServiceHostOfflineHandler
					*/

					// dial notifier client to notifier server endpoint and begin service operations
					if err := c._notifierClient.Dial(); err != nil {
						log.Println("!!! Notifier Client Service Dial Failed: " + err.Error() + " !!!")
					} else {
						if err := c._notifierClient.Subscribe(c._notifierClient.ConfiguredSNSDiscoveryTopicArn()); err != nil {
							log.Println("!!! Notifier Client Service Subscribe Failed: " + err.Error() + " !!!")
							c._notifierClient.Close() // close to clean up
						} else {
							// subscribe successful, notifier client alert services started
							log.Println("+++ Notifier Client Service Started +++")
						}
					}
				} else {
					log.Println("--- Notifier Client Service Skipped, Not Yet Configured for Dial ---")
				}
			} else {
				log.Println("--- Notifier Client Service Skipped, No xyz-notifier-client.yaml Defined ---")
			}

			//
			// dial completed
			//
			return nil
		}
	}
}

// note: SNS http callback requires public http endpoint, for service discovery, use NotifierClient instead (already configured within Dial)
func (c *Client) subscribeToSNS(actionName string, topicArn string, topicSubArn string, setConfigSnsSubArnFunc func(string)) {
	if c._sns != nil && util.LenTrim(topicArn) > 0 && util.LenTrim(topicSubArn) == 0 {
		if util.LenTrim(c.WebServerConfig.WebServerLocalAddress) == 0 {
			log.Println("!!! " + actionName + " SNS Topic '" + topicArn + "' Subscribe Failed: Web Server Host Local Address is Empty !!!")
			return
		}

		if setConfigSnsSubArnFunc == nil {
			log.Println("!!! " + actionName + " SNS Topic '" + topicArn + "' Subscribe Failed: setConfigSnsSubArnFunc Parameter Required")
			return
		}

		p := snsprotocol.Http

		if util.Left(strings.ToLower(c.WebServerConfig.WebServerLocalAddress), 5) == "https" {
			p = snsprotocol.Https
		}

		if subArn, e := notification.Subscribe(c._sns, topicArn, p, c.WebServerConfig.WebServerLocalAddress, time.Duration(c._config.Target.SdTimeout)*time.Second); e != nil {
			log.Println("!!! " + actionName + " SNS Topic '" + topicArn + "' Subscribe Failed: " + e.Error() + " !!!")
		} else {
			setConfigSnsSubArnFunc(subArn)

			if e := c._config.Save(); e != nil {
				setConfigSnsSubArnFunc("")
				log.Println("!!! " + actionName + " SNS Topic '" + topicArn + "' Subscribe Failed: Persist Config Error, " + e.Error() + " !!!")
			} else {
				log.Println("... " + actionName + " SNS Topic '" + topicArn + "' Subscribe OK: " + subArn)
			}
		}
	} else if util.LenTrim(topicSubArn) > 0 {
		log.Println("--- " + actionName + " SNS Topic '" + topicArn + "' Already Subscribed: " + topicSubArn + " ---")
	}
}

// note: SNS http callback requires public http endpoint, for service discovery, use NotifierClient instead (already configured within Dial)
func (c *Client) unsubscribeFromSNS() {
	doSave := false

	if c._sns != nil {
		if util.LenTrim(c._config.Topics.SnsDiscoverySubscriptionArn) > 0 {
			if e := notification.Unsubscribe(c._sns, c._config.Topics.SnsDiscoverySubscriptionArn, time.Duration(c._config.Target.SdTimeout)*time.Second); e != nil {
				log.Println("!!! Discovery SNS Topic '" + c._config.Topics.SnsDiscoveryTopicArn + "' Unsubscribe Failed: " + e.Error() + " !!!")
			} else {
				log.Println("... Unsubscribe Discovery SNS Topic '" + c._config.Topics.SnsDiscoveryTopicArn + "' OK")
				c._config.SetSnsDiscoverySubscriptionArn("")
				doSave = true
			}
		} else {
			log.Println("--- No Discovery SNS Topic to Unsubscribe ---")
		}

		if util.LenTrim(c._config.Topics.SnsLoggerSubscriptionArn) > 0 {
			if e := notification.Unsubscribe(c._sns, c._config.Topics.SnsLoggerSubscriptionArn, time.Duration(c._config.Target.SdTimeout)*time.Second); e != nil {
				log.Println("!!! Logger SNS Topic '" + c._config.Topics.SnsLoggerTopicArn + "' Unsubscribe Failed: " + e.Error() + " !!!")
			} else {
				log.Println("... Unsubscribe Logger SNS Topic '" + c._config.Topics.SnsLoggerTopicArn + "' OK")
				c._config.SetSnsLoggerSubscriptionArn("")
				doSave = true
			}
		} else {
			log.Println("--- No Logger SNS Topic to Unsubscribe ---")
		}

		if util.LenTrim(c._config.Topics.SnsTracerSubscriptionArn) > 0 {
			if e := notification.Unsubscribe(c._sns, c._config.Topics.SnsTracerSubscriptionArn, time.Duration(c._config.Target.SdTimeout)*time.Second); e != nil {
				log.Println("!!! Tracer SNS Topic '" + c._config.Topics.SnsTracerTopicArn + "' Unsubscribe Failed: " + e.Error() + " !!!")
			} else {
				log.Println("... Unsubscribe Tracer SNS Topic '" + c._config.Topics.SnsTracerTopicArn + "' OK")
				c._config.SetSnsTracerSubscriptionArn("")
				doSave = true
			}
		} else {
			log.Println("--- No Tracer SNS Topic to Unsubscribe ---")
		}

		if util.LenTrim(c._config.Topics.SnsMonitorSubscriptionArn) > 0 {
			if e := notification.Unsubscribe(c._sns, c._config.Topics.SnsMonitorSubscriptionArn, time.Duration(c._config.Target.SdTimeout)*time.Second); e != nil {
				log.Println("!!! Monitor SNS Topic '" + c._config.Topics.SnsMonitorTopicArn + "' Unsubscribe Failed: " + e.Error() + " !!!")
			} else {
				log.Println("... Unsubscribe Monitor SNS Topic '" + c._config.Topics.SnsMonitorTopicArn + "' OK")
				c._config.SetSnsMonitorSubscriptionArn("")
				doSave = true
			}
		} else {
			log.Println("--- No Monitor SNS Topic to Unsubscribe ---")
		}

		if doSave {
			if e := c._config.Save(); e != nil {
				log.Println("!!! Persist Unsubscribed Status To Config Failed: " + e.Error() + " !!!")
			}
		}
	}
}

// waitForWebServerReady is called after web server is expected to start,
// this function will wait a short time for web server startup success or timeout
func (c *Client) waitForWebServerReady(timeoutDuration ...time.Duration) error {
	if util.LenTrim(c.WebServerConfig.WebServerLocalAddress) == 0 {
		return fmt.Errorf("Web Server Host Address is Empty")
	}

	var timeout time.Duration

	if len(timeoutDuration) > 0 {
		timeout = timeoutDuration[0]
	} else {
		timeout = 5*time.Second
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
				log.Println("Web Server Health Check Failed: " + e.Error())
				wg.Done()
				chanErrorInfo <- "Web Server Health Check Failed: " + e.Error()
				return
			} else {
				if status == 200 {
					log.Println("Web Server Health OK")
					wg.Done()
					chanErrorInfo <- "OK"
					return
				} else {
					log.Println("Web Server Not Ready!")
				}
			}

			time.Sleep(2500*time.Millisecond)

			if time.Now().After(expireDateTime) {
				log.Println("Web Server Health Check Timeout")
				wg.Done()
				chanErrorInfo <- "Web Server Health Check Failed: Timeout"
				return
			}
		}
	}()

	wg.Wait()

	log.Println("Web Server Heath Check Finalized...")

	errInfo := <-chanErrorInfo

	if errInfo == "OK" {
		// success - web server health check = ok
		return nil
	} else {
		// failure
		return fmt.Errorf(errInfo)
	}
}

// waitForEndpointReady is called after Dial to check if target service is ready as reported by health probe
func (c *Client) waitForEndpointReady(timeoutDuration ...time.Duration) error {
	var timeout time.Duration

	if len(timeoutDuration) > 0 {
		timeout = timeoutDuration[0]
	} else {
		timeout = 5*time.Second
	}

	//
	// check if service is ready
	// wait for target service to respond with serving status before moving forward
	//
	wg := sync.WaitGroup{}
	wg.Add(1)

	chanErrorInfo := make(chan string)

	go func() {
		for {
			if status, e := c.HealthProbe("", timeout); e != nil {
				log.Println("Health Status Check Failed: " + e.Error())
				wg.Done()
				chanErrorInfo <- "Health Status Check Failed: " + e.Error()
				return
			} else {
				if status == grpc_health_v1.HealthCheckResponse_SERVING {
					log.Println("Serving Status Detected")
					wg.Done()
					chanErrorInfo <- "OK"
					return
				} else {
					log.Println("Not Serving!")
				}
			}

			time.Sleep(2500*time.Millisecond)
		}
	}()

	wg.Wait()

	log.Println("Heath Status Check Finalized...")

	errInfo := <-chanErrorInfo

	if errInfo == "OK" {
		// success - server service health = serving
		return nil
	} else {
		// failure
		return fmt.Errorf(errInfo)
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

	c.unsubscribeFromSNS()

	// clean up notifier client connection
	if c._notifierClient != nil {
		if c._notifierClient.NotifierClientAlertServicesStarted() {
			if err := c._notifierClient.Unsubscribe(); err != nil {
				log.Println("!!! Notifier Client Alert Services Unsubscribe Failed: " + err.Error() + " !!!")
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

// ClientConnection returns the currently loaded grpc client connection
func (c *Client) ClientConnection() grpc.ClientConnInterface{
	return c._conn
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

	c._endpoints = []*serviceEndpoint{}

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

	c._endpoints = append(c._endpoints, &serviceEndpoint{
		SdType:  "direct",
		Host: ip,
		Port: port,
		CacheExpire: cacheExpires,
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

	//
	// check for existing cache
	//
	found := _cache.GetLiveServiceEndpoints(serviceName + "." + namespaceName, "")

	if len(found) > 0 {
		c._endpoints = found
		log.Println("Using DNS Discovered Cache Hosts: (Service) " + serviceName + "." + namespaceName)
		for _, v := range c._endpoints {
			log.Println("   - " + v.Host + ":" + util.UintToStr(v.Port) + ", Cache Expires: " + util.FormatDateTime(v.CacheExpire))
		}
		return nil
	}

	//
	// acquire dns ip port from service discovery
	//
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

			c._endpoints = append(c._endpoints, &serviceEndpoint{
				SdType:  sdType,
				Host: ip,
				Port: port,
				CacheExpire: cacheExpires,
			})
		}

		_cache.AddServiceEndpoints(serviceName + "." + namespaceName, c._endpoints)

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

	//
	// check for existing cache
	//
	found := _cache.GetLiveServiceEndpoints(serviceName + "." + namespaceName, version)

	if len(found) > 0 {
		c._endpoints = found
		log.Println("Using API Discovered Cache Hosts: (Service) " + serviceName + "." + namespaceName)
		for _, v := range c._endpoints {
			log.Println("   - " + v.Host + ":" + util.UintToStr(v.Port) + ", Cache Expires: " + util.FormatDateTime(v.CacheExpire))
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
			c._endpoints = append(c._endpoints, &serviceEndpoint{
				SdType:  "api",
				Host: v.InstanceIP,
				Port: v.InstancePort,
				InstanceId: v.InstanceId,
				ServiceId: v.ServiceId,
				Version: v.InstanceVersion,
				CacheExpire: cacheExpires,
			})
		}

		_cache.AddServiceEndpoints(serviceName + "." + namespaceName, c._endpoints)

		return nil
	}
}

// findUnhealthyInstances will call cloud map sd to discover unhealthy instances, a slice of unhealthy instances is returned
func (c *Client) findUnhealthyEndpoints(serviceName string, namespaceName string, version string, maxCount int64, timeoutSeconds uint) (unhealthyList []*serviceEndpoint, err error) {
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
		timeoutDuration = append(timeoutDuration, time.Duration(timeoutSeconds) * time.Second)
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
				SdType:  "api",
				Host: v.InstanceIP,
				Port: v.InstancePort,
				InstanceId: v.InstanceId,
				ServiceId: v.ServiceId,
				Version: v.InstanceVersion,
				CacheExpire: time.Time{},
			})
		}

		return unhealthyList, nil
	}
}

// updateHealth will update instance health
func (c *Client) updateHealth(p *serviceEndpoint, healthy bool) error {
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
func (c *Client) deregisterInstance(p *serviceEndpoint) error {
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

// =====================================================================================================================
// HTTP WEB SERVER
// =====================================================================================================================

// note: WebServerLocalAddress = read only getter
//
// note: WebServerRoutes = map[string]*ginw.RouteDefinition{
//		"base": {
//			Routes: []*ginw.Route{
//				{
//					Method: ginhttpmethod.GET,
//					RelativePath: "/",
//					Handler: func(c *gin.Context, bindingInputPtr interface{}) {
//						c.String(200, "Connector Client Http Host Up")
//					},
//				},
//			},
//		},
//	}
type WebServerConfig struct {
	AppName string
	ConfigFileName string
	CustomConfigPath string

	// define web server router info
	WebServerRoutes map[string]*ginw.RouteDefinition

	// getter only
	WebServerLocalAddress string
}

func (c *Client) startWebServer() error {
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

	c.WebServerConfig.WebServerLocalAddress = fmt.Sprintf("%s://%s:%d", httpVerb, util.GetLocalIP(), server.Port())

	// serve web server
	if err := server.Serve(); err != nil {
		return fmt.Errorf("Start Web Server Failed: (Serve Error) %s", err)
	}

	return nil
}
