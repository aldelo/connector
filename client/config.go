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
	"fmt"
	util "github.com/aldelo/common"
	data "github.com/aldelo/common/wrapper/viper"
)

type Config struct {
	AppName string					`mapstructure:"-"`
	ConfigFileName string			`mapstructure:"-"`
	CustomConfigPath string			`mapstructure:"-"`

	_v *data.ViperConf				`mapstructure:"-"`

	Target TargetData				`mapstructure:"target"`
	Grpc GrpcData					`mapstructure:"grpc"`
}

type TargetData struct {
	AppName string					`mapstructure:"app_name"`
	ServiceDiscoveryType string		`mapstructure:"service_discovery_type"`
	DirectConnectIpPort string		`mapstructure:"direct_connect_ip_port"`
	ServiceName string				`mapstructure:"service_name"`
	NamespaceName string			`mapstructure:"namespace_name"`
	Region string					`mapstructure:"region"`
	InstanceVersion string			`mapstructure:"instance_version"`
	InstancePort uint				`mapstructure:"instance_port"`
	SdTimeout uint					`mapstructure:"sd_timeout"`
	SdEndpointCacheExpires uint		`mapstructure:"sd_endpoint_cache_expires"`
	SdEndpointProbeFrequency uint	`mapstructure:"sd_endpoint_probe_frequency"`
	SdInstanceMaxResult uint		`mapstructure:"sd_instance_max_result"`
}

type GrpcData struct {
	X509CaCertFile string						`mapstructure:"x509_ca_cert_file"`
	UserAgent string							`mapstructure:"user_agent"`
	UseLoadBalancer bool						`mapstructure:"use_load_balancer"`
	UseHealthCheck bool							`mapstructure:"use_health_check"`
	DialMinConnectTimeout uint					`mapstructure:"dial_min_connect_timeout"`
	KeepAliveInactivePingTimeTrigger uint		`mapstructure:"keepalive_inactive_ping_time_trigger"`
	KeepAliveInactivePingTimeout uint			`mapstructure:"keepalive_inactive_ping_timeout"`
	KeepAlivePermitWithoutStream bool			`mapstructure:"keepalive_permit_without_stream"`
	ReadBufferSize uint							`mapstructure:"read_buffer_size"`
	WriteBufferSize uint						`mapstructure:"write_buffer_size"`
	CircuitBreakerEnabled bool					`mapstructure:"circuit_breaker_enabled"`
	CircuitBreakerTimeout uint					`mapstructure:"circuit_breaker_timeout"`
	CircuitBreakerMaxConcurrentRequests uint	`mapstructure:"circuit_breaker_max_concurrent_requests"`
	CircuitBreakerRequestVolumeThreshold uint	`mapstructure:"circuit_breaker_request_volume_threshold"`
	CircuitBreakerSleepWindow uint				`mapstructure:"circuit_breaker_sleep_window"`
	CircuitBreakerErrorPercentThreshold uint	`mapstructure:"circuit_breaker_error_percent_threshold"`
	CircuitBreakerLoggerEnabled bool			`mapstructure:"circuit_breaker_logger_enabled"`
	UseSQS bool									`mapstructure:"use_sqs"`
	UseSNS bool									`mapstructure:"use_sns"`
}

func (c *Config) SetTargetAppName(s string) {
	if c._v != nil {
		c._v.Set("target.app_name", s)
		c.Target.AppName = s
	}
}

func (c *Config) SetServiceDiscoveryType(s string) {
	if c._v != nil {
		c._v.Set("target.service_discovery_type", s)
		c.Target.ServiceDiscoveryType = s
	}
}

func (c *Config) SetDirectConnectIpPort(s string) {
	if c._v != nil {
		c._v.Set("target.direct_connect_ip_port", s)
		c.Target.DirectConnectIpPort = s
	}
}

func (c *Config) SetServiceName(s string) {
	if c._v != nil {
		c._v.Set("target.service_name", s)
		c.Target.ServiceName = s
	}
}

func (c *Config) SetNamespaceName(s string) {
	if c._v != nil {
		c._v.Set("target.namespace_name", s)
		c.Target.NamespaceName = s
	}
}

func (c *Config) SetTargetRegion(s string) {
	if c._v != nil {
		c._v.Set("target.region", s)
		c.Target.Region = s
	}
}

func (c *Config) SetInstanceVersion(s string) {
	if c._v != nil {
		c._v.Set("target.instance_version", s)
		c.Target.InstanceVersion = s
	}
}

func (c *Config) SetInstancePort(i uint) {
	if c._v != nil {
		c._v.Set("target.instance_port", i)
		c.Target.InstancePort = i
	}
}

func (c *Config) SetSdTimeout(i uint) {
	if c._v != nil {
		c._v.Set("target.sd_timeout", i)
		c.Target.SdTimeout = i
	}
}

func (c *Config) SetSdEndpointCacheExpires(i uint) {
	if c._v != nil {
		c._v.Set("target.sd_endpoint_cache_expires", i)
		c.Target.SdEndpointCacheExpires = i
	}
}

func (c *Config) SetSdEndpointProbeFrequency(i uint) {
	if c._v != nil {
		c._v.Set("target.sd_endpoint_probe_frequency", i)
		c.Target.SdEndpointProbeFrequency = i
	}
}

func (c *Config) SetSdInstanceMaxResult(i uint) {
	if c._v != nil {
		c._v.Set("target.sd_instance_max_result", i)
		c.Target.SdInstanceMaxResult = i
	}
}

func (c *Config) SetX509CaCertFile(s string) {
	if c._v != nil {
		c._v.Set("grpc.x509_ca_cert_file", s)
		c.Grpc.X509CaCertFile = s
	}
}

func (c *Config) SetUserAgent(s string) {
	if c._v != nil {
		c._v.Set("grpc.user_agent", s)
		c.Grpc.UserAgent = s
	}
}

func (c *Config) SetUseLoadBalancer(b bool) {
	if c._v != nil {
		c._v.Set("grpc.use_load_balancer", b)
		c.Grpc.UseLoadBalancer = b
	}
}

func (c *Config) SetUseHealthCheck(b bool) {
	if c._v != nil {
		c._v.Set("grpc.use_health_check", b)
		c.Grpc.UseHealthCheck = b
	}
}

func (c *Config) SetDialMinConnectTimeout(i uint) {
	if c._v != nil {
		c._v.Set("grpc.dial_min_connect_timeout", i)
		c.Grpc.DialMinConnectTimeout = i
	}
}

func (c *Config) SetKeepAliveInactivePingTimeTrigger(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_inactive_ping_time_trigger", i)
		c.Grpc.KeepAliveInactivePingTimeTrigger = i
	}
}

func (c *Config) SetKeepAliveInactivePingTimeout(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_inactive_ping_timeout", i)
		c.Grpc.KeepAliveInactivePingTimeout = i
	}
}

func (c *Config) SetKeepAlivePermitWithoutStream(b bool) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_permit_without_stream", b)
		c.Grpc.KeepAlivePermitWithoutStream = b
	}
}

func (c *Config) SetReadBufferSize(i uint) {
	if c._v != nil {
		c._v.Set("grpc.read_buffer_size", i)
		c.Grpc.ReadBufferSize = i
	}
}

func (c *Config) SetWriteBufferSize(i uint) {
	if c._v != nil {
		c._v.Set("grpc.write_buffer_size", i)
		c.Grpc.WriteBufferSize = i
	}
}

func (c *Config) SetCircuitBreakerEnabled(b bool) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_enabled", b)
		c.Grpc.CircuitBreakerEnabled = b
	}
}

func (c *Config) SetCircuitBreakerTimeout(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_timeout", i)
		c.Grpc.CircuitBreakerTimeout = i
	}
}

func (c *Config) SetCircuitBreakerMaxConcurrentRequests(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_max_concurrent_requests", i)
		c.Grpc.CircuitBreakerMaxConcurrentRequests = i
	}
}

func (c *Config) SetCircuitBreakerRequestVolumeThreshold(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_request_volume_threshold", i)
		c.Grpc.CircuitBreakerRequestVolumeThreshold = i
	}
}

func (c *Config) SetCircuitBreakerSleepWindow(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_sleep_window", i)
		c.Grpc.CircuitBreakerSleepWindow = i
	}
}

func (c *Config) SetCircuitBreakerErrorPercentThreshold(i uint) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_error_percent_threshold", i)
		c.Grpc.CircuitBreakerErrorPercentThreshold = i
	}
}

func (c *Config) SetCircuitBreakerLoggerEnabled(b bool) {
	if c._v != nil {
		c._v.Set("grpc.circuit_breaker_logger_enabled", b)
		c.Grpc.CircuitBreakerLoggerEnabled = b
	}
}

func(c *Config) SetUseSQS(b bool) {
	if c._v != nil {
		c._v.Set("grpc.use_sqs", b)
		c.Grpc.UseSQS = b
	}
}

func (c *Config) SetUseSNS(b bool) {
	if c._v	!= nil {
		c._v.Set("grpc.use_sns", b)
		c.Grpc.UseSNS = b
	}
}

// Read will load config settings from disk
func (c *Config) Read() error {
	c._v = nil
	c.Target = TargetData{}
	c.Grpc = GrpcData{}

	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	if util.LenTrim(c.ConfigFileName) == 0 {
		c.ConfigFileName = "client"
	}

	c._v = &data.ViperConf{
		AppName: c.AppName,
		ConfigName: c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML: true,
		UseAutomaticEnvVar: false,
	}

	c._v.Default(
		"target.app_name", "connector.client").Default(					// required, this client app's name
		"target.service_discovery_type", "srv").Default(					// required, defines target service discovery mode: srv, a, api, direct
		"target.direct_connect_ip_port", "").Default(						// for direct: ip:port format of direct service endpoint (for testing use only)
		"target.service_name", "").Default(								// for srv, api: service name as registered on cloud map
		"target.namespace_name", "").Default(								// for srv, api: namespace name as registered on cloud map
		"target.region", "us-east-1").Default(							// for api: must be valid aws regions supported
		"target.instance_version", "").Default(							// for api: target instance filter to given version only
		"target.instance_port", 0).Default(								// for sd = a: specific port being used on service endpoint
		"target.sd_timeout", 5).Default(									// timeout seconds for service discovery actions
		"target.sd_endpoint_cache_expires", 300).Default(					// service discovery endpoints cache expires seconds, 0 for default of 300 seconds
		"target.sd_endpoint_probe_frequency", 30).Default(				// service discovery endpoints cache health probe frequency seconds, 0 for default of 30 seconds
		"target.sd_instance_max_result", 100).Default(					// service discovery api returns maximum instances count from discovery call, 0 for default of 100
		"grpc.x509_ca_cert_file", "").Default(							// grpc tls setup, path to ca cert pem file
		"grpc.user_agent", "").Default(									// define user agent string for all RPCs
		"grpc.use_load_balancer", true).Default(							// indicates round robin load balancer is to be used, default is true
		"grpc.use_health_check", true).Default(							// indicates health check for server serving status is enabled, default is true
		"grpc.dial_min_connect_timeout", 5).Default(						// indicates the minimum connect timeout seconds for the dial action, default is 5 seconds
		"grpc.keepalive_inactive_ping_time_trigger", 0).Default(			// max seconds of no activity before client pings server, 0 for default of 30 seconds
		"grpc.keepalive_inactive_ping_timeout", 0).Default( 				// max seconds of timeout during client to server ping, where no response closes connection, 0 for default of 5 seconds
		"grpc.keepalive_permit_without_stream", false).Default(			// allow client to keepalive if no stream, false is default
		"grpc.read_buffer_size", 0).Default(								// 0 for default 32 kb = 1024 * 32
		"grpc.write_buffer_size", 0).Default(								// 0 for default 32 kb = 1024 * 32
		"grpc.circuit_breaker_enabled", false).Default(					// indicates if circuit breaker is enabled, default is false
		"grpc.circuit_breaker_timeout", 1000).Default(					// how long to wait for command to complete, in milliseconds, default = 1000
		"grpc.circuit_breaker_max_concurrent_requests", 10).Default(		// how many commands of the same type can run at the same time, default = 10
		"grpc.circuit_breaker_request_volume_threshold", 20).Default(		// minimum number of requests needed before a circuit can be tripped due to health, default = 20
		"grpc.circuit_breaker_sleep_window", 5000).Default(				// how long to wait after a circuit opens before testing for recovery, in milliseconds, default = 5000
		"grpc.circuit_breaker_error_percent_threshold", 50).Default(		// causes circuits to open once the rolling measure of errors exceeds this percent of requests, default = 50
		"grpc.circuit_breaker_logger_enabled", true).Default(				// indicates the logger that will be used to log circuit breaker action
		"grpc.use_sqs", true).Default(									// indicates if sqs is used if applicable, default is true
		"grpc.use_sns", true)												// indicates if sns is used if applicable, default is true


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
func (c *Config) Save() error {
	if c._v != nil {
		return c._v.Save()
	} else {
		return nil
	}
}
