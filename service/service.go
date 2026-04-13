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
	"strconv"
	"strings"
	"sync"
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

// Service represents a gRPC server's service definition and entry point
type Service struct {
	// service properties
	AppName          string
	ConfigFileName   string
	CustomConfigPath string

	// web server config
	WebServerConfig *WebServerConfig

	RegisterServiceHandlers func(grpcServer *grpc.Server)

	// setup optional health check handlers
	DefaultHealthCheckHandler  func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus
	ServiceHealthCheckHandlers map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus

	// setup optional rate limit server interceptor
	RateLimit ratelimiter.RateLimiterIFace

	// one or more unary server interceptors for handling wrapping actions
	UnaryServerInterceptors []grpc.UnaryServerInterceptor

	// one or more stream server interceptors for handling wrapping actions
	StreamServerInterceptors []grpc.StreamServerInterceptor

	// typically wrapper action to handle monitoring
	StatsHandler stats.Handler

	// handler for unknown requests rather than sending back an error
	UnknownStreamHandler grpc.StreamHandler

	// handler to invoke before gRPC server is to start
	BeforeServerStart func(svc *Service)

	// handler to invoke after gRPC server started
	AfterServerStart func(svc *Service)

	// handler to invoke before gRPC server is to shutdown
	BeforeServerShutdown func(svc *Service)

	// handler to invoke after gRPC server has shutdown
	AfterServerShutdown func(svc *Service)

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
}

// create service
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
		return fmt.Errorf("Read Config Failed: %s", err.Error())
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
		s.RegisterServiceHandlers(s._grpcServer)

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
					discoverySnsTopic, _ = util.ExtractAlphaNumericUnderscoreDash(util.Replace(discoverySnsTopic, ".", "-"))

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
					discoveryQueueName, _ = util.ExtractAlphaNumericUnderscoreDash(util.Replace(discoveryQueueName, ".", "-"))

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
					loggerQueueName, _ = util.ExtractAlphaNumericUnderscoreDash(util.Replace(loggerQueueName, ".", "-"))

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
			return fmt.Errorf("Connect SD Failed: %s", err.Error())
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

// startServer will start and serve grpc services, it will run in goroutine until terminated.
// FIX #8: Replaced busy-wait (default + time.Sleep(10ms)) with blocking on quit channel
// after server startup is complete.
func (s *Service) startServer(lis net.Listener, quit chan bool, quitDone chan struct{}) (err error) {
	seg := xray.NewSegmentNullable("GrpcService-StartServer")
	if seg != nil {
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
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
	go func() {
		if s.BeforeServerStart != nil {
			log.Println("Before gRPC Server Starts Begin...")
			s.BeforeServerStart(s)
			log.Println("... Before gRPC Server Starts End")
		}

		log.Println("Initiating gRPC Server Startup...")

		go func() {
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
				log.Printf("Serve gRPC Service %s on %s Failed: (Server Halt) %s", appName, s.LocalAddress(), serveErr.Error())
				if p, findErr := os.FindProcess(os.Getpid()); findErr == nil {
					_ = p.Signal(syscall.SIGTERM)
				}
			} else {
				log.Println("... gRPC Server Quit Command Received")
			}
		}()

		log.Println("... gRPC Server Startup Initiated")

		if s.WebServerConfig != nil {
			if util.LenTrim(s.WebServerConfig.ConfigFileName) > 0 {
				log.Println("Starting Http Web Server...")
				startWebServerFail := make(chan bool, 1)

				go func() {
					if webServerErr := s.startWebServer(); webServerErr != nil {
						log.Printf("!!! Serve Http Web Server %s Failed: %s !!!\n", s.WebServerConfig.AppName, webServerErr)
						startWebServerFail <- true
					} else {
						log.Println("... Http Web Server Quit Command Received")
					}
				}()

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
		go func() {
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
								log.Println("+++ Service Discovery Push Notification: Publish Host Info OK +++")
								s.publishToSNS(tArn, "Discovery Push Notification", s.getHostDiscoveryMessage(true), nil)
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
		}()

		// trigger after server start event
		if s.AfterServerStart != nil {
			s.AfterServerStart(s)
		}
	}()

	// Quit handler goroutine: performs full graceful cleanup on shutdown signal,
	// matching the cleanup done by GracefulStop()/ImmediateStop().
	// Closes quitDone when all cleanup is complete so Serve() can wait.
	go func() {
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
		if s.WebServerConfig != nil && s.WebServerConfig.CleanUp != nil {
			s.WebServerConfig.CleanUp()
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
		if gs != nil {
			gs.Stop()
		}
		_ = lis.Close()
	}()

	return nil
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
		return fmt.Errorf("Register Instance Failed: %s", err.Error())
	}

	return nil
}

// awaitOsSigExit handles os exit event for clean up
func (s *Service) awaitOsSigExit() {
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)

	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)

	go func() {
		sig := <-sigs
		log.Println("OS Sig Exit Command: ", sig)
		done <- true
	}()

	log.Println("=== Press 'Ctrl + C' to Shutdown ===")
	s._mu.RLock()
	hft := s._config.SvcCreateData.HealthFailThreshold
	s._mu.RUnlock()
	log.Printf("Please Wait for Instance Service Discovery To Complete... (This may take %v Seconds)", hft*45)

	<-done

	// FIX #29: Stop signal delivery so the channel does not leak
	signal.Stop(sigs)

	log.Println("*** Shutdown Invoked ***")
}

// deleteServiceHealthReportFromDataStore will remove the health report service record from data store based on instanceId
func (s *Service) deleteServiceHealthReportFromDataStore(instanceId string) (err error) {
	seg := xray.NewSegmentNullable("GrpcService-DeleteServiceHealthReportFromDataStore")
	if seg != nil {
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
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
				_ = seg.Seg.AddError(err)
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
			_ = seg.Seg.AddMetadata("Skipped-Reason", msg)
		}

		log.Println(msg)
		return true
	}

	if util.LenTrim(cfg.Instance.Id) == 0 {
		msg := "Set Service Health Report Update To Data Store Skipped: " + "InstanceID Not Defined in Config"

		if seg != nil {
			_ = seg.Seg.AddMetadata("Skipped-Reason", msg)
		}

		log.Println(msg)
		return true
	}

	localAddr := s.LocalAddress()
	if util.LenTrim(localAddr) == 0 {
		msg := "Set Service Health Report Update To Data Store Skipped: " + "Service Host Info Not Ready"

		if seg != nil {
			_ = seg.Seg.AddMetadata("Skipped-Reason", msg)
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

// Serve will setup grpc service and start serving
func (s *Service) Serve() error {
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

	quit := make(chan bool, 1)     // buffered to prevent deadlock if handler goroutine exits early
	quitDone := make(chan struct{}) // closed by quit handler when all cleanup is complete

	if err = s.startServer(lis, quit, quitDone); err != nil {
		_ = lis.Close()
		return err
	}

	// FIX: If registerSd fails after startServer, the server goroutines are
	// already running. Signal quit to trigger cleanup (stop gRPC, close listener)
	// instead of leaking goroutines and the listener.
	if err = s.registerSd(ip, port); err != nil {
		quit <- true
		return err
	}

	s.awaitOsSigExit()

	if s.BeforeServerShutdown != nil {
		log.Println("Before gRPC Server Shutdown Begin...")
		s.BeforeServerShutdown(s)
		log.Println("... Before gRPC Server Shutdown End")
	}

	quit <- true
	<-quitDone // wait for quit handler to complete full cleanup before proceeding

	if s.AfterServerShutdown != nil {
		log.Println("After gRPC Server Shutdown Begin...")
		s.AfterServerShutdown(s)
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
func (s *Service) deregisterInstance() error {
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
			return fmt.Errorf("De-Register Instance Fail: %s", err.Error())
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

// GracefulStop allows existing actions be completed before shutting down gRPC server
func (s *Service) GracefulStop() {
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

	if s.WebServerConfig != nil && s.WebServerConfig.CleanUp != nil {
		s.WebServerConfig.CleanUp()
	}

	s.setLocalAddress("")

	log.Println("Stopping gRPC Server (Graceful)")

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

// ImmediateStop will forcefully shutdown gRPC server regardless of pending actions being processed
func (s *Service) ImmediateStop() {
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

	if s.WebServerConfig != nil && s.WebServerConfig.CleanUp != nil {
		s.WebServerConfig.CleanUp()
	}

	s.setLocalAddress("")

	log.Println("Stopping gRPC Server (Immediate)")

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
