package client

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
	"fmt"
	util "github.com/aldelo/common"
	data "github.com/aldelo/common/wrapper/viper"
)

type config struct {
	AppName          string `mapstructure:"-"`
	ConfigFileName   string `mapstructure:"-"`
	CustomConfigPath string `mapstructure:"-"`

	_v *data.ViperConf `mapstructure:"-"`

	Target targetData `mapstructure:"target"`
	Queues queuesData `mapstructure:"queues"`
	Topics topicsData `mapstructure:"topics"`
	Grpc   grpcData   `mapstructure:"grpc"`
}

type targetData struct {
	AppName                string `mapstructure:"app_name"`
	ServiceDiscoveryType   string `mapstructure:"service_discovery_type"`
	DirectConnectIpPort    string `mapstructure:"direct_connect_ip_port"`
	ServiceName            string `mapstructure:"service_name"`
	NamespaceName          string `mapstructure:"namespace_name"`
	Region                 string `mapstructure:"region"`
	InstanceVersion        string `mapstructure:"instance_version"`
	InstancePort           uint   `mapstructure:"instance_port"`
	SdTimeout              uint   `mapstructure:"sd_timeout"`
	SdEndpointCacheExpires uint   `mapstructure:"sd_endpoint_cache_expires"`
	SdInstanceMaxResult    uint   `mapstructure:"sd_instance_max_result"`
	TraceUseXRay           bool   `mapstructure:"trace_use_xray"`
	ZapLogEnabled          bool   `mapstructure:"zaplog_enabled"`
	ZapLogOutputConsole    bool   `mapstructure:"zaplog_output_console"`
	RestTargetCACertFiles  string `mapstructure:"rest_target_ca_cert_files"`
}

type queuesData struct {
	SqsLoggerQueueUrl string `mapstructure:"sqs_logger_queue_url"`
}

type topicsData struct {
	SnsDiscoveryTopicArn string `mapstructure:"sns_discovery_topic_arn"`
}

type grpcData struct {
	DialBlockingMode                     bool   `mapstructure:"dial_blocking_mode"`
	ServerCACertFiles                    string `mapstructure:"server_ca_cert_files"`
	ClientCertFile                       string `mapstructure:"client_cert_file"`
	ClientKeyFile                        string `mapstructure:"client_key_file"`
	UserAgent                            string `mapstructure:"user_agent"`
	UseLoadBalancer                      bool   `mapstructure:"use_load_balancer"`
	UseHealthCheck                       bool   `mapstructure:"use_health_check"`
	DialMinConnectTimeout                uint   `mapstructure:"dial_min_connect_timeout"`
	KeepAliveInactivePingTimeTrigger     uint   `mapstructure:"keepalive_inactive_ping_time_trigger"`
	KeepAliveInactivePingTimeout         uint   `mapstructure:"keepalive_inactive_ping_timeout"`
	KeepAlivePermitWithoutStream         bool   `mapstructure:"keepalive_permit_without_stream"`
	ReadBufferSize                       uint   `mapstructure:"read_buffer_size"`
	WriteBufferSize                      uint   `mapstructure:"write_buffer_size"`
	CircuitBreakerEnabled                bool   `mapstructure:"circuit_breaker_enabled"`
	CircuitBreakerTimeout                uint   `mapstructure:"circuit_breaker_timeout"`
	CircuitBreakerMaxConcurrentRequests  uint   `mapstructure:"circuit_breaker_max_concurrent_requests"`
	CircuitBreakerRequestVolumeThreshold uint   `mapstructure:"circuit_breaker_request_volume_threshold"`
	CircuitBreakerSleepWindow            uint   `mapstructure:"circuit_breaker_sleep_window"`
	CircuitBreakerErrorPercentThreshold  uint   `mapstructure:"circuit_breaker_error_percent_threshold"`
	CircuitBreakerLoggerEnabled          bool   `mapstructure:"circuit_breaker_logger_enabled"`
}

func (c *config) SetTargetAppName(s string) {
	if c._v != nil {
		c._v.Set("target.app_name", s)
		c.Target.AppName = s
	}
}

func (c *config) SetServiceDiscoveryType(s string) {
	if c._v != nil {
		c._v.Set("target.service_discovery_type", s)
		c.Target.ServiceDiscoveryType = s
	}
}

func (c *config) SetDirectConnectIpPort(s string) {
	if c._v != nil {
		c._v.Set("target.direct_connect_ip_port", s)
		c.Target.DirectConnectIpPort = s
	}
}

func (c *config) SetServiceName(s string) {
	if c._v != nil {
		c._v.Set("target.service_name", s)
		c.Target.ServiceName = s
	}
}

func (c *config) SetNamespaceName(s string) {
	if c._v != nil {
		c._v.Set("target.namespace_name", s)
		c.Target.NamespaceName = s
	}
}

func (c *config) SetTargetRegion(s string) {
	if c._v != nil {
		c._v.Set("target.region", s)
		c.Target.Region = s
	}
}

func (c *config) SetInstanceVersion(s string) {
	if c._v != nil {
		c._v.Set("target.instance_version", s)
		c.Target.InstanceVersion = s
	}
}

func (c *config) SetInstancePort(i uint) {
	if c._v != nil {
		c._v.Set("target.instance_port", i)
		c.Target.InstancePort = i
	}
}

func (c *config) SetSdTimeout(i uint) {
	if c._v != nil {
		c._v.Set("target.sd_timeout", i)
		c.Target.SdTimeout = i
	}
}

func (c *config) SetSdEndpointCacheExpires(i uint) {
	if c._v != nil {
		c._v.Set("target.sd_endpoint_cache_expires", i)
		c.Target.SdEndpointCacheExpires = i
	}
}

func (c *config) SetSdInstanceMaxResult(i uint) {
	if c._v != nil {
		c._v.Set("target.sd_instance_max_result", i)
		c.Target.SdInstanceMaxResult = i
	}
}

func (c *config) SetTraceUseXRay(b bool) {
	if c._v != nil {
		c._v.Set("target.trace_use_xray", b)
		c.Target.TraceUseXRay = b
	}
}

func (c *config) SetZapLogEnabled(b bool) {
	if c._v != nil {
		c._v.Set("target.zaplog_enabled", b)
		c.Target.ZapLogEnabled = b
	}
}

func (c *config) SetZapLogOutputConsole(b bool) {
	if c._v != nil {
		c._v.Set("target.zaplog_output_console", b)
		c.Target.ZapLogOutputConsole = b
	}
}

func (c *config) SetRestTargetCACertFiles(s string) {
	if c._v != nil {
		c._v.Set("target.rest_target_ca_cert_files", s)
		c.Target.RestTargetCACertFiles = s
	}
}

func (c *config) SetSqsLoggerQueueUrl(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_logger_queue_url", s)
		c.Queues.SqsLoggerQueueUrl = s
	}
}

func (c *config) SetSnsDiscoveryTopicArn(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_discovery_topic_arn", s)
		c.Topics.SnsDiscoveryTopicArn = s
	}
}

func (c *config) SetDialBlockingMode(b bool) {
	if c._v != nil {
		c._v.Set("grpc.dial_blocking_mode", b)
		c.Grpc.DialBlockingMode = b
	}
}

func (c *config) SetServerCACertFiles(s string) {
	if c._v != nil {
		c._v.Set("grpc.server_ca_cert_files", s)
		c.Grpc.ServerCACertFiles = s
	}
}

func (c *config) SetClientCertFile(s string) {
	if c._v != nil {
		c._v.Set("grpc.client_cert_file", s)
		c.Grpc.ClientCertFile = s
	}
}

func (c *config) SetClientKeyFile(s string) {
	if c._v != nil {
		c._v.Set("grpc.client_key_file", s)
		c.Grpc.ClientKeyFile = s
	}
}

func (c *config) SetUserAgent(s string) {
	if c._v != nil {
		c._v.Set("grpc.user_agent", s)
		c.Grpc.UserAgent = s
	}
}

func (c *config) SetUseLoadBalancer(b bool) {
	if c._v != nil {
		c._v.Set("grpc.use_load_balancer", b)
		c.Grpc.UseLoadBalancer = b
	}
}

func (c *config) SetUseHealthCheck(b bool) {
	if c._v != nil {
		c._v.Set("grpc.use_health_check", b)
		c.Grpc.UseHealthCheck = b
	}
}

func (c *config) SetDialMinConnectTimeout(i uint) {
	if c._v != nil {
		c._v.Set("grpc.dial_min_connect_timeout", i)
		c.Grpc.DialMinConnectTimeout = i
	}
}

func (c *config) SetKeepAliveInactivePingTimeTrigger(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_inactive_ping_time_trigger", i)
		c.Grpc.KeepAliveInactivePingTimeTrigger = i
	}
}

func (c *config) SetKeepAliveInactivePingTimeout(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_inactive_ping_timeout", i)
		c.Grpc.KeepAliveInactivePingTimeout = i
	}
}

func (c *config) SetKeepAlivePermitWithoutStream(b bool) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_permit_without_stream", b)
		c.Grpc.KeepAlivePermitWithoutStream = b
	}
}

func (c *config) SetReadBufferSize(i uint) {
	if c._v != nil {
		c._v.Set("grpc.read_buffer_size", i)
		c.Grpc.ReadBufferSize = i
	}
}

func (c *config) SetWriteBufferSize(i uint) {
	if c._v != nil {
		c._v.Set("grpc.write_buffer_size", i)
		c.Grpc.WriteBufferSize = i
	}
}

func (c *config) SetCircuitBreakerEnabled(b bool) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_enabled", b)
		c.Grpc.CircuitBreakerEnabled = b
	}
}

func (c *config) SetCircuitBreakerTimeout(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_timeout", i)
		c.Grpc.CircuitBreakerTimeout = i
	}
}

func (c *config) SetCircuitBreakerMaxConcurrentRequests(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_max_concurrent_requests", i)
		c.Grpc.CircuitBreakerMaxConcurrentRequests = i
	}
}

func (c *config) SetCircuitBreakerRequestVolumeThreshold(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_request_volume_threshold", i)
		c.Grpc.CircuitBreakerRequestVolumeThreshold = i
	}
}

func (c *config) SetCircuitBreakerSleepWindow(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_sleep_window", i)
		c.Grpc.CircuitBreakerSleepWindow = i
	}
}

func (c *config) SetCircuitBreakerErrorPercentThreshold(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_error_percent_threshold", i)
		c.Grpc.CircuitBreakerErrorPercentThreshold = i
	}
}

func (c *config) SetCircuitBreakerLoggerEnabled(b bool) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_logger_enabled", b)
		c.Grpc.CircuitBreakerLoggerEnabled = b
	}
}

// Read will load config settings from disk
func (c *config) Read() error {
	c._v = nil
	c.Target = targetData{}
	c.Queues = queuesData{}
	c.Topics = topicsData{}
	c.Grpc = grpcData{}

	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	if util.LenTrim(c.ConfigFileName) == 0 {
		c.ConfigFileName = "client"
	}

	c._v = &data.ViperConf{
		AppName:          c.AppName,
		ConfigName:       c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML:            true,
		UseAutomaticEnvVar: false,
	}

	c._v.Default(
		"target.app_name", "connector.client").Default( // required, this client app's name
		"target.service_discovery_type", "srv").Default( // required, defines target service discovery mode: srv, a, api, direct
		"target.direct_connect_ip_port", "").Default( // for direct: ip:port format of direct service endpoint (for testing use only)
		"target.service_name", "").Default( // for srv, api: service name as registered on cloud map
		"target.namespace_name", "").Default( // for srv, api: namespace name as registered on cloud map
		"target.region", "us-east-1").Default( // for api: must be valid aws regions supported
		"target.instance_version", "").Default( // for api: target instance filter to given version only
		"target.instance_port", 0).Default( // for sd = a: specific port being used on service endpoint
		"target.sd_timeout", 5).Default( // timeout seconds for service discovery actions
		"target.sd_endpoint_cache_expires", 300).Default( // service discovery endpoints cache expires seconds, 0 for default of 300 seconds
		"target.sd_instance_max_result", 100).Default( // service discovery api returns maximum instances count from discovery call, 0 for default of 100
		"target.trace_use_xray", false).Default( // indicates if this client will use aws xray tracing, default is false
		"target.zaplog_enabled", false).Default( // indicates if app logs will use zap log, default = false, meaning no log written
		"target.zaplog_output_console", true).Default( // indicates if zap log outputs to console (screen) or to file, true = console, false = file; default = true
		"target.rest_target_ca_cert_files", "").Default( // optional, self-signed ca certs file path, separated by comma if multiple ca pems,
		// 			 used by rest get/post/put/delete against target server hosts that use self-signed certs for tls,
		//			 to avoid bad certificate error during tls handshake
		"queues.sqs_logger_queue_url", "").Default( // the queue url pre-created on aws sqs for the target logger service this config file targets
		"topics.sns_discovery_topic_arn", "").Default( // the topic arn pre-created on aws sns for the target discovery service this config file targets
		"grpc.dial_blocking_mode", true).Default( // indicate if grpc dial is blocking mode or not, default is true
		"grpc.server_ca_cert_files", "").Default( // for server TLS or mTLS setup, one or more server CA cert path to pem file, multiple files separated by comma
		"grpc.client_cert_file", "").Default( // for mTLS setup, the client certificate pem file path
		"grpc.client_key_file", "").Default( // for mTLS setup, the client certificate key file path
		"grpc.user_agent", "").Default( // define user agent string for all RPCs
		"grpc.use_load_balancer", true).Default( // indicates round robin load balancer is to be used, default is true
		"grpc.use_health_check", true).Default( // indicates health check for server serving status is enabled, default is true
		"grpc.dial_min_connect_timeout", 5).Default( // indicates the minimum connect timeout seconds for the dial action, default is 5 seconds
		"grpc.keepalive_inactive_ping_time_trigger", 0).Default( // max seconds of no activity before client pings server, 0 for default of 30 seconds
		"grpc.keepalive_inactive_ping_timeout", 0).Default( // max seconds of timeout during client to server ping, where no response closes connection, 0 for default of 5 seconds
		"grpc.keepalive_permit_without_stream", false).Default( // allow client to keepalive if no stream, false is default
		"grpc.read_buffer_size", 0).Default( // 0 for default 32 kb = 1024 * 32
		"grpc.write_buffer_size", 0).Default( // 0 for default 32 kb = 1024 * 32
		"grpc.circuit_breaker_enabled", false).Default( // indicates if circuit breaker is enabled, default is false
		"grpc.circuit_breaker_timeout", 1000).Default( // how long to wait for command to complete, in milliseconds, default = 1000
		"grpc.circuit_breaker_max_concurrent_requests", 10).Default( // how many commands of the same type can run at the same time, default = 10
		"grpc.circuit_breaker_request_volume_threshold", 20).Default( // minimum number of requests needed before a circuit can be tripped due to health, default = 20
		"grpc.circuit_breaker_sleep_window", 5000).Default( // how long to wait after a circuit opens before testing for recovery, in milliseconds, default = 5000
		"grpc.circuit_breaker_error_percent_threshold", 50).Default( // causes circuits to open once the rolling measure of errors exceeds this percent of requests, default = 50
		"grpc.circuit_breaker_logger_enabled", true) // indicates the logger that will be used to log circuit breaker action

	if ok, err := c._v.Init(); err != nil {
		return err
	} else {
		if !ok {
			if e := c._v.Save(); e != nil {
				return fmt.Errorf("Create Config File Failed: " + e.Error())
			}
		} else {
			c._v.WatchConfig()
		}
	}

	if err := c._v.Unmarshal(c); err != nil {
		return err
	}

	return nil
}

// Save persists config settings to disk
func (c *config) Save() error {
	if c._v != nil {
		return c._v.Save()
	} else {
		return nil
	}
}
