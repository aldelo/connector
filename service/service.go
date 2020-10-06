package service

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
	"github.com/aldelo/common/wrapper/cloudmap/sdhealthchecktype"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/sns"
	"github.com/aldelo/common/wrapper/sns/snsprotocol"
	"github.com/aldelo/common/wrapper/sqs"
	"github.com/aldelo/connector/adapters/health"
	"github.com/aldelo/connector/adapters/notification"
	"github.com/aldelo/connector/adapters/queue"
	"github.com/aldelo/connector/adapters/ratelimiter"
	"github.com/aldelo/connector/adapters/ratelimiter/ratelimitplugin"
	"github.com/aldelo/connector/adapters/registry"
	"github.com/aldelo/connector/adapters/registry/sdoperationstatus"
	ws "github.com/aldelo/connector/webserver"
	sns2 "github.com/aws/aws-sdk-go/service/sns"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/tap"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Service represents a gRPC server's service definition and entry point
//
// AppName = (required) name of this service
// ConfigFileName = (required) config file name without .yaml extension
// CustomConfigPath = (optional) if not specified, . is assumed
// RegisterServiceHandlers = (required) func to register grpc service handlers
type Service struct {
	// service properties
	AppName string
	ConfigFileName string
	CustomConfigPath string

	// web server config
	WebServerConfig *WebServerConfig

	// register one or more service handlers
	// example: type AnswerServiceImpl struct {
	//				testpb.UnimplementedAnswerServiceServer
	//			}
	//
	//			RegisterServiceHandlers: func(grpcServer *grpc.Server) {
	//				testpb.RegisterAnswerServiceServer(grpcServer, &AnswerServiceImpl{})
	//			},
	RegisterServiceHandlers func(grpcServer *grpc.Server)

	// setup optional health check handlers
	DefaultHealthCheckHandler func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus
	ServiceHealthCheckHandlers map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus

	// setup optional auth server interceptor
	// TODO:

	// setup optional rate limit server interceptor
	RateLimit ratelimiter.RateLimiterIFace

	// setup optional monitor server interceptor
	// TODO:

	// setup optional trace server interceptor
	// TODO:

	// setup optional logging server interceptor
	// TODO:

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
	_sd *cloudmap.CloudMap
	_sqs *sqs.SQS
	_sns *sns.SNS

	// instantiated internal objects
	_grpcServer *grpc.Server
	_localAddress string

	// grpc serving status and mutex locking
	_serving bool
	_mu sync.RWMutex
}

// create service
func NewService(appName string, configFileName string, customConfigPath string, registerServiceHandlers func(grpcServer *grpc.Server)) *Service {
	return &Service{
		AppName: appName,
		ConfigFileName: configFileName,
		CustomConfigPath: customConfigPath,
		RegisterServiceHandlers: registerServiceHandlers,
	}
}

// readConfig will read in config data
func (s *Service) readConfig() error {
	s._config = &config{
		AppName: s.AppName,
		ConfigFileName: s.ConfigFileName,
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
		//
		// config server options
		//
		var opts []grpc.ServerOption

		if s._config.Grpc.ConnectionTimeout > 0 {
			// default 120 seconds
			opts = append(opts, grpc.ConnectionTimeout(time.Duration(s._config.Grpc.ConnectionTimeout) * time.Second))
		}

		if util.LenTrim(s._config.Grpc.ServerCertFile) > 0 && util.LenTrim(s._config.Grpc.ServerKeyFile) > 0 {
			tls := new(crypto.TlsConfig)
			if tc, e := tls.GetServerTlsConfig(s._config.Grpc.ServerCertFile, s._config.Grpc.ServerKeyFile, strings.Split(s._config.Grpc.ClientCACertFiles, ",")); e != nil {
				log.Fatal("Setup gRPC Server TLS Failed: " + e.Error())
			} else {
				if len(s._config.Grpc.ClientCACertFiles) == 0 {
					log.Println("^^^ Server On TLS ^^^")
				} else {
					log.Println("^^^ Server On mTLS ^^^")
				}

				opts = append(opts, grpc.Creds(credentials.NewTLS(tc)))
			}
		}

		if s._config.Grpc.KeepAliveMinWait > 0 || s._config.Grpc.KeepAlivePermitWithoutStream {
			minTime := 5 * time.Minute // default value per grpc doc

			if s._config.Grpc.KeepAliveMinWait > 0 {
				minTime = time.Duration(s._config.Grpc.KeepAliveMinWait) * time.Second
			}

			opts = append(opts, grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
				MinTime: minTime,
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
		count := len(s.UnaryServerInterceptors)

		if count == 1 {
			opts = append(opts, grpc.UnaryInterceptor(s.UnaryServerInterceptors[0]))
		} else if count > 1 {
			opts = append(opts, grpc.ChainUnaryInterceptor(s.UnaryServerInterceptors...))
		}

		// add stream server interceptors
		count = len(s.StreamServerInterceptors)

		if count == 1 {
			opts = append(opts, grpc.StreamInterceptor(s.StreamServerInterceptors[0]))
		} else if count > 1 {
			opts = append(opts, grpc.ChainStreamInterceptor(s.StreamServerInterceptors...))
		}

		// rate limit control
		if s.RateLimit == nil {
			// auto create rate limiter if needed
			log.Println("Rate Limiter Nil, Checking If Need To Create...")

			if s._config.Grpc.RateLimitPerSecond > 0 {
				log.Println("Creating Default Rate Limiter...")

				// default to hystrixgo
				s.RateLimit = ratelimitplugin.NewRateLimitPlugin(int(s._config.Grpc.RateLimitPerSecond), false)
			} else {
				log.Println("Rate Limiter Config Per Second = ", s._config.Grpc.RateLimitPerSecond)
			}
		}

		if s.RateLimit != nil {
			log.Println("Setup Rate Limiter - In Tap Handle")

			opts = append(opts, grpc.InTapHandle(func(ctx context.Context, info *tap.Info) (context.Context, error){
				log.Println("Rate Limit Take = " + info.FullMethodName + "...")

				t := s.RateLimit.Take()
				log.Println("... Rate Limit Take = " + t.String())

				return ctx, nil
			}))
		}

		// for monitoring use
		if s.StatsHandler != nil {
			opts = append(opts, grpc.StatsHandler(s.StatsHandler))
		}

		// bi-di stream handler for unknown requests (instead of replying unimplemented grpc error)
		if s.UnknownStreamHandler != nil {
			opts = append(opts, grpc.UnknownServiceHandler(s.UnknownStreamHandler))
		}

		//
		// create server with options if any
		//
		s._grpcServer = grpc.NewServer(opts...)
		s.RegisterServiceHandlers(s._grpcServer)

		ip = util.GetLocalIP()
		port = util.StrToUint(util.SplitString(lis.Addr().String(), ":", -1))
		s._localAddress = fmt.Sprintf("%s:%d", ip, port)

		//
		// setup sqs and sns if needed
		//
		if s._config.Service.DiscoveryUseSqsSns || s._config.Service.LoggerUseSqsSns || s._config.Service.MonitorUseSqsSns || s._config.Service.TracerUseSqsSns {
			//
			// establish sqs and sns adapters
			//
			if s._sqs, err = queue.NewQueueAdapter(awsregion.GetAwsRegion(s._config.Target.Region), nil); err != nil {
				return nil, "", 0, fmt.Errorf("Get SQS Queue Adapter Failed: %s", err)
			}

			if s._sns, err = notification.NewNotificationAdapter(awsregion.GetAwsRegion(s._config.Target.Region), nil); err != nil {
				return nil, "", 0, fmt.Errorf("Get SNS Notification Adapter Failed: %s", err)
			}

			//
			// get a list of all sns topics
			//
			var snsTopicArns []string
			needConfigSave := false

			if snsTopicArns, err = notification.ListTopics(s._sns, time.Duration(s._config.Instance.SdTimeout)*time.Second); err != nil {
				return nil, "", 0, fmt.Errorf("Get SNS Topics List Failed: %s", err)
			}

			//
			// configure discovery sqs sns if needed
			//
			if s._config.Service.DiscoveryUseSqsSns {
				// create sns topic name if need be
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

				// create sqs queue name if need be
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

			//
			// configure logger sqs sns if needed
			//
			if s._config.Service.LoggerUseSqsSns {
				// create sns topic name if need be
				if util.LenTrim(s._config.Topics.SnsLoggerTopicArn) == 0 || !util.StringSliceContains(&snsTopicArns, s._config.Topics.SnsLoggerTopicArn) {
					loggerSnsTopic := s._config.Topics.SnsLoggerTopicNamePrefix + s._config.Service.Name + "." + s._config.Namespace.Name
					loggerSnsTopic, _ = util.ExtractAlphaNumericUnderscoreDash(util.Replace(loggerSnsTopic, ".", "-"))

					if topicArn, e := notification.CreateTopic(s._sns, loggerSnsTopic, time.Duration(s._config.Instance.SdTimeout)*time.Second); e != nil {
						return nil, "", 0, fmt.Errorf("Create SNS Topic %s Failed: %s", loggerSnsTopic, e)
					} else {
						snsTopicArns = append(snsTopicArns, topicArn)
						s._config.SetSnsLoggerTopicArn(topicArn)
						needConfigSave = true
					}
				}

				// create sqs queue name if need be
				if util.LenTrim(s._config.Queues.SqsLoggerQueueArn) == 0 || util.LenTrim(s._config.Queues.SqsLoggerQueueUrl) == 0 {
					loggerQueueName := s._config.Queues.SqsLoggerQueueNamePrefix + s._config.Service.Name + "." + s._config.Namespace.Name
					loggerQueueName, _ = util.ExtractAlphaNumericUnderscoreDash(util.Replace(loggerQueueName, ".", "-"))

					if url, arn, e := queue.GetQueue(s._sqs, loggerQueueName, s._config.Queues.SqsLoggerMessageRetentionSeconds, s._config.Topics.SnsLoggerTopicArn, time.Duration(s._config.Instance.SdTimeout)*time.Second); e != nil {
						return nil, "", 0, fmt.Errorf("Create SQS Queue %s Failed: %s", loggerQueueName, e)
					} else {
						s._config.SetSqsLoggerQueueUrl(url)
						s._config.SetSqsLoggerQueueArn(arn)
						needConfigSave = true
					}
				}
			}

			//
			// configure monitor sqs sns if needed
			//
			if s._config.Service.MonitorUseSqsSns {
				// create sns topic name if need be
				if util.LenTrim(s._config.Topics.SnsMonitorTopicArn) == 0 || !util.StringSliceContains(&snsTopicArns, s._config.Topics.SnsMonitorTopicArn) {
					monitorSnsTopic := s._config.Topics.SnsMonitorTopicNamePrefix + s._config.Service.Name + "." + s._config.Namespace.Name
					monitorSnsTopic, _ = util.ExtractAlphaNumericUnderscoreDash(util.Replace(monitorSnsTopic, ".", "-"))

					if topicArn, e := notification.CreateTopic(s._sns, monitorSnsTopic, time.Duration(s._config.Instance.SdTimeout)*time.Second); e != nil {
						return nil, "", 0, fmt.Errorf("Create SNS Topic %s Failed: %s", monitorSnsTopic, e)
					} else {
						snsTopicArns = append(snsTopicArns, topicArn)
						s._config.SetSnsMonitorTopicArn(topicArn)
						needConfigSave = true
					}
				}

				// create sqs queue name if need be
				if util.LenTrim(s._config.Queues.SqsMonitorQueueArn) == 0 || util.LenTrim(s._config.Queues.SqsMonitorQueueUrl) == 0 {
					monitorQueueName := s._config.Queues.SqsMonitorQueueNamePrefix + s._config.Service.Name + "." + s._config.Namespace.Name
					monitorQueueName, _ = util.ExtractAlphaNumericUnderscoreDash(util.Replace(monitorQueueName, ".", "-"))

					if url, arn, e := queue.GetQueue(s._sqs, monitorQueueName, s._config.Queues.SqsMonitorMessageRetentionSeconds, s._config.Topics.SnsMonitorTopicArn, time.Duration(s._config.Instance.SdTimeout)*time.Second); e != nil {
						return nil, "", 0, fmt.Errorf("Create SQS Queue %s Failed: %s", monitorQueueName, e)
					} else {
						s._config.SetSqsMonitorQueueUrl(url)
						s._config.SetSqsMonitorQueueArn(arn)
						needConfigSave = true
					}
				}
			}

			//
			// configure tracer sqs sns if needed
			//
			if s._config.Service.TracerUseSqsSns {
				// create sns topic name if need be
				if util.LenTrim(s._config.Topics.SnsTracerTopicArn) == 0 || !util.StringSliceContains(&snsTopicArns, s._config.Topics.SnsTracerTopicArn) {
					tracerSnsTopic := s._config.Topics.SnsTracerTopicNamePrefix + s._config.Service.Name + "." + s._config.Namespace.Name
					tracerSnsTopic, _ = util.ExtractAlphaNumericUnderscoreDash(util.Replace(tracerSnsTopic, ".", "-"))

					if topicArn, e := notification.CreateTopic(s._sns, tracerSnsTopic, time.Duration(s._config.Instance.SdTimeout)*time.Second); e != nil {
						return nil, "", 0, fmt.Errorf("Create SNS Topic %s Failed: %s", tracerSnsTopic, e)
					} else {
						snsTopicArns = append(snsTopicArns, topicArn)
						s._config.SetSnsTracerTopicArn(topicArn)
						needConfigSave = true
					}
				}

				// create sqs queue name if need be
				if util.LenTrim(s._config.Queues.SqsTracerQueueArn) == 0 || util.LenTrim(s._config.Queues.SqsTracerQueueUrl) == 0 {
					tracerQueueName := s._config.Queues.SqsTracerQueueNamePrefix + s._config.Service.Name + "." + s._config.Namespace.Name
					tracerQueueName, _ = util.ExtractAlphaNumericUnderscoreDash(util.Replace(tracerQueueName, ".", "-"))

					if url, arn, e := queue.GetQueue(s._sqs, tracerQueueName, s._config.Queues.SqsTracerMessageRetentionSeconds, s._config.Topics.SnsTracerTopicArn, time.Duration(s._config.Instance.SdTimeout)*time.Second); e != nil {
						return nil, "", 0, fmt.Errorf("Create SQS Queue %s Failed: %s", tracerQueueName, e)
					} else {
						s._config.SetSqsTracerQueueUrl(url)
						s._config.SetSqsTracerQueueArn(arn)
						needConfigSave = true
					}
				}
			}

			//
			// save config if need be
			//
			if needConfigSave {
				if e := s._config.Save(); e != nil {
					// save config failed
					return nil, "", 0, fmt.Errorf("Save Config for SNS SQS ARNs Failed: %s", e)
				}
			}
		}

		//
		// exit
		//
		err = nil
		return
	}
}

// connectSd will try to establish service discovery object to struct
func (s *Service) connectSd() error {
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
	if s._grpcServer == nil {
		return fmt.Errorf("Health Check Server Can't Start: gRPC Server Not Started")
	}

	grpc_health_v1.RegisterHealthServer(s._grpcServer, health.NewHealthServer(s.DefaultHealthCheckHandler, s.ServiceHealthCheckHandlers))

	return nil
}

// CurrentlyServing indicates if this service health status indicates currently serving mode or not
func (s *Service) CurrentlyServing() bool {
	s._mu.RLock()
	defer s._mu.RUnlock()
	return s._serving
}

// startServer will start and serve grpc services, it will run in goroutine until terminated
func (s *Service) startServer(lis net.Listener, quit chan bool) error {
	if s._grpcServer == nil {
		return fmt.Errorf("gRPC Server Not Setup")
	}

	// set service default serving mode to 'not serving'
	s._mu.Lock()
	s._serving = false
	s._mu.Unlock()

	go func() {
		gRPCServerInvoked := false

		for {
			select {
			case <-quit:
				// quit is invoked
				log.Println("gRPC Server Quit Invoked")

				// on exit, stop serving
				s._mu.Lock()
				s._serving = false
				s._mu.Unlock()

				s._grpcServer.Stop()
				_ = lis.Close()
				return

			default:
				// start gRPC server
				if !gRPCServerInvoked {
					gRPCServerInvoked = true

					if s.BeforeServerStart != nil {
						log.Println("Before gRPC Server Starts Begin...")

						s.BeforeServerStart(s)

						log.Println("... Before gRPC Server Starts End")
					}

					log.Println("Starting gRPC Server...")

					go func() {
						//
						// start health checker services
						//
						log.Println("Starting gRPC Health Server...")

						if err := s.startHealthChecker(); err != nil {
							log.Println("!!! gRPC Health Server Fail To Start: " + err.Error() + " !!!")
						} else {
							log.Println("... gRPC Health Server Started")
						}

						//
						// serve grpc service
						//
						if err := s._grpcServer.Serve(lis); err != nil {
							log.Fatalf("Serve gRPC Service %s on %s Failed: (Server Halt) %s", s._config.AppName, s._localAddress, err.Error())
						} else {
							log.Println("... gRPC Server Quit Command Received")
						}
					}()

					log.Println("... gRPC Server Started")

					if s.WebServerConfig != nil {
						log.Println("Starting Http Web Server...")
						startWebServerFail := make(chan bool)

						go func() {
							//
							// start http web server
							//
							if err := s.startWebServer(); err != nil {
								log.Printf("!!! Serve Http Web Server %s Failed: %s !!!\n", s.WebServerConfig.AppName, err)
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
								log.Printf("... Http Web Server Started: %s\n", s.WebServerConfig.WebServerLocalAddress)
						}
					}

					if s.AfterServerStart != nil {
						log.Println("After gRPC Server Started Begin...")

						// trigger sd initial health update
						go func() {
							waitTime := int(s._config.SvcCreateData.HealthFailThreshold*30+5)

							log.Println(">>> Initial SD Instance Health Check Staged (" +  util.Itoa(waitTime) + " Seconds Warm-Up) >>>")
							time.Sleep(time.Duration(waitTime)*time.Second)
							log.Println("Initial SD Instance Health Check Begin...")

							// on initial health update, set sd instance health status to healthy (true)
							if err := s.updateHealth(true); err != nil {
								log.Println("!!! Update SD Instance Health Failed: " + err.Error() + " !!!")
							} else {
								log.Println("... Update SD Instance Health to 'Healthy' Successful")

								// queue new grpc service host healthy notification
								if s._config.Service.DiscoveryUseSqsSns {
									log.Println("Discovery Push Notification Begin...")

									if s._sqs == nil {
										log.Println("!!! Discovery Push Notification - Requires SQS Initialized !!!")
									} else if s._sns == nil {
										log.Println("!!! Discovery Push Notification - Requires SNS Initialized !!!")
									} else {
										qArn := s._config.Queues.SqsDiscoveryQueueArn
										qUrl := s._config.Queues.SqsDiscoveryQueueUrl
										tArn := s._config.Topics.SnsDiscoveryTopicArn
										tSubId := s._config.Topics.SnsDiscoverySubscriptionArn

										if util.LenTrim(qArn) == 0 {
											log.Println("!!! Discovery Push Notification - Required SQS Queue Not Auto Created (Missing QueueARN) !!!")
										} else if util.LenTrim(qUrl) == 0 {
											log.Println("!!! Discovery Push Notification - Required SQS Queue Not Auto Created (Missing QueueURL) !!!")
										} else if util.LenTrim(tArn) == 0 {
											log.Println("!!! Discovery Push Notification - Required SNS Topic Not Auto Created (Missing TopicARN) !!!")
										} else {
											pubOk := false

											if util.LenTrim(tSubId) == 0 {
												// need to subscribe topic
												if subId, subErr := notification.Subscribe(s._sns, tArn, snsprotocol.Sqs, qArn, time.Duration(s._config.Instance.SdTimeout)*time.Second); subErr != nil {
													log.Println("!!! Discovery Push Notification - Server Queue Topic Subscribe Failed: " + subErr.Error() + " !!!")
												} else {
													s._config.SetSnsDiscoverySubscriptionArn(subId)

													if cErr := s._config.Save(); cErr != nil {
														// save config fail, reverse subscription if possible
														log.Println("!!! Discovery Push Notification - Server Queue Topic Subscription Persist To Config Failed: " + cErr.Error() + " !!!")

														if uErr := notification.Unsubscribe(s._sns, subId, time.Duration(s._config.Instance.SdTimeout)*time.Second); uErr != nil {
															log.Println("!!! Discovery Push Notification - Server Queue Auto Unsubscribe Failed: " + uErr.Error() + " !!!")
														} else {
															log.Println("!!! Discovery Push Notification - Server Queue Auto Unsubscribe Successful !!!")
														}

														log.Println("!!! Discovery Push Notification - Publish Service Host Will Not Be Performed !!!")
													} else {
														pubOk = true
													}
												}
											} else {
												// subscription already exist, use existing subscription
												pubOk = true
											}

											// publish message to sns -> sqs about this host live
											if pubOk {
												s.publishToSNS(tArn, "Discovery Push Notification", s.getHostDiscoveryMessage(true), nil)
											} else {
												log.Println("!!! Discovery Push Notification - Nothing to Publish, Possible Error Encountered !!!")
											}
										}
									}
								} else {
									log.Println("!!! Discovery Push Notification Disabled !!!")
								}

								// set service serving mode to true (serving)
								s._mu.Lock()
								s._serving = true
								s._mu.Unlock()
							}
						}()

						// trigger after server start event
						s.AfterServerStart(s)

						log.Println("... After gRPC Server Started End")
					}
				}
			}

			time.Sleep(10*time.Millisecond)
		}
	}()

	return nil
}

// getHostDiscoveryMessage returns json string formatted with online / offline status indicator along with host address info
func (s *Service) getHostDiscoveryMessage(online bool) string {
	onlineStatus := ""

	if online {
		onlineStatus = "online"
	} else {
		onlineStatus = "offline"
	}

	return fmt.Sprintf(`{"msg_type":"host-discovery", "action":"%s", "host":"%s"}`, onlineStatus, s.LocalAddress())
}

// publishToSNS publishes message to an sns topic, if sns is setup
func (s *Service) publishToSNS(topicArn string, actionName string, message string, attributes map[string]*sns2.MessageAttributeValue) {
	if s._sns == nil {
		return
	}

	if util.LenTrim(topicArn) == 0 {
		return
	}

	if util.LenTrim(message) == 0 {
		return
	}

	if id, err := notification.Publish(s._sns, topicArn, message, attributes, time.Duration(s._config.Instance.SdTimeout)*time.Second); err != nil {
		log.Println("!!! " + actionName + " - Publish Failed: " + err.Error() + " !!!")
	} else {
		log.Println("... " + actionName + " - Publish OK: " + id)
	}
}

// registerSd registers instance to sd
func (s *Service) registerSd(ip string, port uint) error {
	if s._config == nil || s._sd == nil {
		return nil
	}

	if err := s.registerInstance(ip, port, !s._config.Instance.InitialUnhealthy, s._config.Instance.Version); err != nil {
		if util.LenTrim(s._config.Instance.Id) > 0 {
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
	// watch for os sigint or sigterm exit conditions for cleanup
	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)

	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM)

	go func() {
		sig := <-sigs
		log.Println("OS Sig Exit Command: ", sig)
		done <- true
	}()

	log.Println("=== Press 'Ctrl + C' to Shutdown ===")
	<-done

	// blocked until signal
	log.Println("*** Shutdown Invoked ***")
}

// Serve will setup grpc service and start serving
func (s *Service) Serve() error {
	s._localAddress = ""

	// read server config data in
	if err := s.readConfig(); err != nil {
		return err
	}

	// create new grpc server
	lis, ip, port, err := s.setupServer()

	if err != nil {
		return err
	}

	log.Println("Service " + s._config.AppName + " Starting On " + s._localAddress + "...")

	// connect sd
	if err = s.connectSd(); err != nil {
		return err
	}

	// auto create sd service if needed
	if err = s.autoCreateService(); err != nil {
		return err
	}

	// start grpc server
	quit := make(chan bool)

	if err = s.startServer(lis, quit); err != nil {
		return err
	}

	// register instance to sd
	if err = s.registerSd(ip, port); err != nil {
		return err
	}

	// halt until os exit signal
	s.awaitOsSigExit()

	if s.BeforeServerShutdown != nil {
		log.Println("Before gRPC Server Shutdown Begin...")

		s.BeforeServerShutdown(s)

		log.Println("... Before gRPC Server Shutdown End")
	}

	// shut down gRPC server command invoke
	quit <- true

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
				TTL: int64(ttl),
				MultiValue: multivalue,
				SRV: srv,
			}

			if util.LenTrim(s._config.SvcCreateData.DnsRouting) == 0 {
				// no dns conf
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
				Custom: s._config.SvcCreateData.HealthCustom,
				FailureThreshold: int64(failThreshold),
				PubDns_HealthCheck_Type: healthType,
				PubDns_HealthCheck_Path: healthPath,
			}

			var timeoutDuration []time.Duration

			if s._config.Instance.SdTimeout > 0 {
				timeoutDuration = append(timeoutDuration, time.Duration(s._config.Instance.SdTimeout) * time.Second)
			}

			if svcId, err := registry.CreateService(s._sd,
						 					   		name,
											   		s._config.Namespace.Id,
											   		dnsConf,
											   		healthConf,
										   "", timeoutDuration...); err != nil {
				log.Println("Auto Create Service Failed: " + err.Error())
				return err
			} else {
				// service id obtained, update to config
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

// registerInstance will call cloud map to register service instance
func (s *Service) registerInstance(ip string, port uint, healthy bool, version string) error {
	if s._sd != nil && s._config != nil && len(s._config.Service.Id) > 0 {
		var timeoutDuration []time.Duration

		if s._config.Instance.SdTimeout > 0 {
			timeoutDuration = append(timeoutDuration, time.Duration(s._config.Instance.SdTimeout) * time.Second)
		}

		// if prior instance id already exist, deregister prior first (clean up prior in case instance ghosted)
		if s._config.Instance.AutoDeregisterPrior {
			_ = s.deregisterInstance()
		}

		// now register instance
		if instanceId, operationId, err := registry.RegisterInstance(s._sd, s._config.Service.Id, s._config.Instance.Prefix, ip, port, healthy, version, timeoutDuration...); err != nil {
			log.Println("Auto Register Instance Failed: " + err.Error())
			return err
		} else {
			tryCount := 0

			log.Println("Auto Register Instance Initiated... " + instanceId)

			time.Sleep(250*time.Millisecond)

			for {
				if status, e := registry.GetOperationStatus(s._sd, operationId, timeoutDuration...); e != nil {
					log.Println("... Auto Register Instance Failed: " + e.Error())
					return e
				} else {
					if status == sdoperationstatus.Success {
						log.Println("... Auto Register Instance OK: " + instanceId)

						s._config.SetInstanceId(instanceId)

						if e2 := s._config.Save(); e2 != nil {
							log.Println("... Update Config with Registered Instance Failed: " + e2.Error())
							return fmt.Errorf("Register Instance Fail When Save Config Errored: %s", e2.Error())
						} else {
							log.Println("... Update Config with Registered Instance OK")
							return nil
						}
					} else {
						// wait 250 ms then retry, up until 20 counts of 250 ms (5 seconds)
						if tryCount < 20 {
							tryCount++
							log.Println("... Checking Register Instance Completion Status, Attempt " + strconv.Itoa(tryCount) + " (100ms)")
							time.Sleep(250*time.Millisecond)
						} else {
							log.Println("... Auto Register Instance Failed: Operation Timeout After 5 Seconds")
							return fmt.Errorf("Register Instance Fail When Operation Timed Out After 5 Seconds")
						}
					}
				}
			}
		}
	} else {
		return nil
	}
}

// updateHealth will update instance health
func (s *Service) updateHealth(healthy bool) error {
	if s._sd != nil && s._config != nil && util.LenTrim(s._config.Service.Id) > 0 && util.LenTrim(s._config.Instance.Id) > 0 {
		var timeoutDuration []time.Duration

		if s._config.Instance.SdTimeout > 0 {
			timeoutDuration = append(timeoutDuration, time.Duration(s._config.Instance.SdTimeout) * time.Second)
		}

		return registry.UpdateHealthStatus(s._sd, s._config.Instance.Id, s._config.Service.Id, healthy)
	} else {
		return nil
	}
}

// deregisterInstance will remove instance from cloudmap and route 53
func (s *Service) deregisterInstance() error {
	if s._sd != nil && s._config != nil && util.LenTrim(s._config.Service.Id) > 0 && util.LenTrim(s._config.Instance.Id) > 0 {
		log.Println("De-Register Instance Begin...")

		var timeoutDuration []time.Duration

		if s._config.Instance.SdTimeout > 0 {
			timeoutDuration = append(timeoutDuration, time.Duration(s._config.Instance.SdTimeout) * time.Second)
		}

		if operationId, err := registry.DeregisterInstance(s._sd, s._config.Instance.Id, s._config.Service.Id, timeoutDuration...); err != nil {
			log.Println("... De-Register Instance Failed: " + err.Error())
			return fmt.Errorf("De-Register Instance Fail: %s", err.Error())
		} else {
			tryCount := 0

			time.Sleep(250*time.Millisecond)

			for {
				if status, e := registry.GetOperationStatus(s._sd, operationId, timeoutDuration...); e != nil {
					log.Println("... De-Register Instance Failed: " + e.Error())
					return fmt.Errorf("De-Register Instance Fail: %s", e.Error())
				} else {
					if status == sdoperationstatus.Success {
						log.Println("... De-Register Instance OK")

						s._config.SetInstanceId("")

						if e2 := s._config.Save(); e2 != nil {
							log.Println("... Update Config with De-Registered Instance Failed: " + e2.Error())
							return fmt.Errorf("De-Register Instance Fail When Save Config Errored: %s", e2.Error())
						} else {
							log.Println("... Update Config with De-Registered Instance OK")
							return nil
						}
					} else {
						// wait 250 ms then retry, up until 20 counts of 250 ms (5 seconds)
						if tryCount < 20 {
							tryCount++
							log.Println("... Checking De-Register Instance Completion Status, Attempt " + strconv.Itoa(tryCount) + " (100ms)")
							time.Sleep(250*time.Millisecond)
						} else {
							log.Println("... De-Register Instance Failed: Operation Timeout After 5 Seconds")
							return fmt.Errorf("De-register Instance Fail When Operation Timed Out After 5 Seconds")
						}
					}
				}
			}
		}
	} else {
		return nil
	}
}

// unsubscribe all existing sns subscriptions if any
func (s *Service) unsubscribeSNS() {
	if s._sns == nil {
		return
	}

	log.Println("Notification/Queue Services Unsubscribe Begin...")

	doSave := false

	if s._config.Service.DiscoveryUseSqsSns {
		if util.LenTrim(s._config.Topics.SnsDiscoverySubscriptionArn) > 0 {
			if err := notification.Unsubscribe(s._sns, s._config.Topics.SnsDiscoverySubscriptionArn, time.Duration(s._config.Instance.SdTimeout)*time.Second); err != nil {
				log.Println("!!! Unsubscribe Discovery Subscription Failed: " + err.Error() + " !!!")
			} else {
				log.Println("... Unsubscribe Discovery Subscription OK")
				s._config.SetSnsDiscoverySubscriptionArn("")
				doSave = true
			}
		} else {
			log.Println("--- No Discovery Subscription to Unsubscribe ---")
		}
	} else {
		log.Println("--- Discovery Push Notification is Disabled ---")
	}

	if s._config.Service.LoggerUseSqsSns {
		if util.LenTrim(s._config.Topics.SnsLoggerSubscriptionArn) > 0 {
			if err := notification.Unsubscribe(s._sns, s._config.Topics.SnsLoggerSubscriptionArn, time.Duration(s._config.Instance.SdTimeout)*time.Second); err != nil {
				log.Println("!!! Unsubscribe Logger Subscription Failed: " + err.Error() + " !!!")
			} else {
				log.Println("... Unsubscribe Logger Subscription OK")
				s._config.SetSnsLoggerSubscriptionArn("")
				doSave = true
			}
		} else {
			log.Println("--- No Logger Subscription to Unsubscribe ---")
		}
	} else {
		log.Println("--- Logger Push Notification is Disabled ---")
	}

	if s._config.Service.TracerUseSqsSns {
		if util.LenTrim(s._config.Topics.SnsTracerSubscriptionArn) > 0 {
			if err := notification.Unsubscribe(s._sns, s._config.Topics.SnsTracerSubscriptionArn, time.Duration(s._config.Instance.SdTimeout)*time.Second); err != nil {
				log.Println("!!! Unsubscribe Tracer Subscription Failed: " + err.Error() + " !!!")
			} else {
				log.Println("... Unsubscribe Tracer Subscription OK")
				s._config.SetSnsTracerSubscriptionArn("")
				doSave = true
			}
		} else {
			log.Println("--- No Tracer Subscription to Unsubscribe ---")
		}
	} else {
		log.Println("--- Tracer Push Notification is Disabled ---")
	}

	if s._config.Service.MonitorUseSqsSns {
		if util.LenTrim(s._config.Topics.SnsMonitorSubscriptionArn) > 0 {
			if err := notification.Unsubscribe(s._sns, s._config.Topics.SnsMonitorSubscriptionArn, time.Duration(s._config.Instance.SdTimeout)*time.Second); err != nil {
				log.Println("!!! Unsubscribe Monitor Subscription Failed: " + err.Error() + " !!!")
			} else {
				log.Println("... Unsubscribe Monitor Subscription OK")
				s._config.SetSnsMonitorTopicArn("")
				doSave = true
			}
		} else {
			log.Println("--- No Monitor Subscription to Unsubscribe ---")
		}
	} else {
		log.Println("--- Monitor Push Notification is Disabled ---")
	}

	log.Println("... Notification/Queue Services Unsubscribe End")

	if doSave {
		if err := s._config.Save(); err != nil {
			log.Println("!!! Persist Unsubscribed Info To Config Failed: " + err.Error() + " !!!")
		}
	}
}

// GracefulStop allows existing actions be completed before shutting down gRPC server
func (s *Service) GracefulStop() {
	// notify sns of host offline
	s.publishToSNS(s._config.Topics.SnsDiscoveryTopicArn, "Discovery Push Notification", s.getHostDiscoveryMessage(false), nil)

	// perform unsubscribe if any
	s.unsubscribeSNS()

	// start clean up
	s._localAddress = ""

	// de-register instance from cloud map
	log.Println("Stopping gRPC Server (Graceful)")

	if s._sd != nil {
		if err := s.deregisterInstance(); err != nil {
			log.Println("De-Register Instance Failed From GracefulStop: " + err.Error())
		} else {
			log.Println("De-Register Instance OK From GracefulStop")
		}
	}

	if s._sd != nil {
		s._sd.Disconnect()
		s._sd = nil
	}

	if s._sqs != nil {
		s._sqs.Disconnect()
		s._sqs = nil
	}

	if s._sns != nil {
		s._sns.Disconnect()
		s._sns = nil
	}

	if s._grpcServer != nil {
		s._grpcServer.GracefulStop()
		s._grpcServer = nil
	}
}

// ImmediateStop will forcefully shutdown gRPC server regardless of pending actions being processed
func (s *Service) ImmediateStop() {
	// notify sns of host offline
	s.publishToSNS(s._config.Topics.SnsDiscoveryTopicArn, "Discovery Push Notification", s.getHostDiscoveryMessage(false), nil)

	// perform unsubscribe if any
	s.unsubscribeSNS()

	// start clean up
	s._localAddress = ""

	// de-register instance from cloud map
	log.Println("Stopping gRPC Server (Immediate)")

	if s._sd != nil {
		if err := s.deregisterInstance(); err != nil {
			log.Println("De-Register Instance Failed From ImmediateStop: " + err.Error())
		} else {
			log.Println("De-Register Instance OK From ImmediateStop")
		}
	}

	if s._sd != nil {
		s._sd.Disconnect()
		s._sd = nil
	}

	if s._sqs != nil {
		s._sqs.Disconnect()
		s._sqs = nil
	}

	if s._sns != nil {
		s._sns.Disconnect()
		s._sns = nil
	}

	if s._grpcServer != nil {
		s._grpcServer.Stop()
		s._grpcServer = nil
	}
}

// LocalAddress returns the service server's address and port
func (s *Service) LocalAddress() string {
	return s._localAddress
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
//						c.String(200, "Connector Service Http Host Up")
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

	server := ws.NewWebServer(s.WebServerConfig.AppName, s.WebServerConfig.ConfigFileName, s.WebServerConfig.CustomConfigPath)

	/* EXAMPLE
	server.Routes = map[string]*ginw.RouteDefinition{
		"base": {
			Routes: []*ginw.Route{
				{
					Method: ginhttpmethod.GET,
					RelativePath: "/",
					Handler: func(c *gin.Context, bindingInputPtr interface{}) {
						c.String(200, "Connector Service Http Host Up")
					},
				},
			},
		},
	}
	*/
	server.Routes = s.WebServerConfig.WebServerRoutes

	// set web server local address before serve action
	httpVerb := ""

	if server.UseTls() {
		httpVerb = "https"
	} else {
		httpVerb = "http"
	}

	s.WebServerConfig.WebServerLocalAddress = fmt.Sprintf("%s://%s:%d", httpVerb, util.GetLocalIP(), server.Port())

	// serve web server
	if err := server.Serve(); err != nil {
		return fmt.Errorf("Start Web Server Failed: (Serve Error) %s", err)
	}

	return nil
}

