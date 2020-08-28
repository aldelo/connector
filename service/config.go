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
	Namespace NamespaceData			`mapstructure:"namespace"`
	Service ServiceData				`mapstructure:"service"`
	SvcCreateData ServiceAutoCreate	`mapstructure:"service_auto_create"`
	Instance InstanceData			`mapstructure:"instance"`
	Grpc GrpcData					`mapstructure:"grpc"`
}

type TargetData struct {
	AppName string					`mapstructure:"app_name"`
	Region string					`mapstructure:"region"`
}

type NamespaceData struct {
	Id string						`mapstructure:"ns_id"`
	Name string						`mapstructure:"ns_name"`
}

type ServiceData struct {
	Id string						`mapstructure:"sv_id"`
	Name string						`mapstructure:"sv_name"`
}

type ServiceAutoCreate struct {
	DnsTTL uint						`mapstructure:"sac_dns_ttl"`
	DnsType string					`mapstructure:"sac_dns_type"`
	DnsRouting string				`mapstructure:"sac_dns_routing"`
	HealthCustom bool				`mapstructure:"sac_health_custom"`
	HealthFailThreshold uint		`mapstructure:"sac_health_failthreshold"`
	HealthPubDnsType string			`mapstructure:"sac_health_pubdns_type"`
	HealthPubDnsPath string			`mapstructure:"sac_health_pubdns_path"`
}

type InstanceData struct {
	Port uint						`mapstructure:"instance_port"`
	Version string					`mapstructure:"instance_version"`
	Prefix string					`mapstructure:"instance_prefix"`
	InitialUnhealthy bool			`mapstructure:"initial_unhealthy"`
	Id string						`mapstructure:"instance_id"`
	SdTimeout uint					`mapstructure:"sd_timeout"`
	InternalHealthFrequency uint	`mapstructure:"internal_health_frequency"`
	AutoDeregisterPrior bool		`mapstructure:"auto_deregister_prior"`
}

type GrpcData struct {
	ConnectionTimeout uint 					`mapstructure:"connection_timeout"`
	ServerCertFile string					`mapstructure:"server_cert_file"`
	ServerKeyFile string					`mapstructure:"server_key_file"`
	ClientCACertFiles string				`mapstructure:"client_ca_cert_files"`
	KeepAliveMinWait uint					`mapstructure:"keepalive_min_wait"`
	KeepAlivePermitWithoutStream bool		`mapstructure:"keepalive_permit_without_stream"`
	KeepAliveMaxConnIdle uint				`mapstructure:"keepalive_max_conn_idle"`
	KeepAliveMaxConnAge uint				`mapstructure:"keepalive_max_conn_age"`
	KeepAliveMaxConnAgeGrace uint			`mapstructure:"keepalive_max_conn_age_grace"`
	KeepAliveInactivePingTimeTrigger uint	`mapstructure:"keepalive_inactive_ping_time_trigger"`
	KeepAliveInactivePingTimeout uint		`mapstructure:"keepalive_inactive_ping_timeout"`
	ReadBufferSize uint						`mapstructure:"read_buffer_size"`
	WriteBufferSize uint					`mapstructure:"write_buffer_size"`
	MaxReceiveMessageSize uint				`mapstructure:"max_recv_msg_size"`
	MaxSendMessageSize uint					`mapstructure:"max_send_msg_size"`
	MaxConcurrentStreams uint				`mapstructure:"max_concurrent_streams"`
	NumStreamWorkers uint					`mapstructure:"num_stream_workers"`
	RateLimitPerSecond uint 				`mapstructure:"rate_limit_per_second"`
	UseSQS bool								`mapstructure:"use_sqs"`
	UseSNS bool								`mapstructure:"use_sns"`
}

func (c *Config) SetTargetAppName(s string) {
	if c._v != nil {
		c._v.Set("target.app_name", s)
		c.Target.AppName = s
	}
}

func (c *Config) SetTargetRegion(s string) {
	if c._v != nil {
		c._v.Set("target.region", s)
		c.Target.Region = s
	}
}

func (c *Config) SetNamespaceId(s string) {
	if c._v != nil {
		c._v.Set("namespace.ns_id", s)
		c.Namespace.Id = s
	}
}

func (c *Config) SetNamespaceName(s string) {
	if c._v != nil {
		c._v.Set("namespace.ns_name", s)
		c.Namespace.Name = s
	}
}

func (c *Config) SetServiceId(s string) {
	if c._v != nil {
		c._v.Set("service.sv_id", s)
		c.Service.Id = s
	}
}

func (c *Config) SetServiceName(s string) {
	if c._v != nil {
		c._v.Set("service.sv_name", s)
		c.Service.Name = s
	}
}

func (c *Config) SetSvcCreateDnsTTL(i uint) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_dns_ttl", i)
		c.SvcCreateData.DnsTTL = i
	}
}

func (c *Config) SetSvcCreateDnsType(s string) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_dns_type", s)
		c.SvcCreateData.DnsType = s
	}
}

func (c *Config) SetSvcCreateDnsRouting(s string) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_dns_routing", s)
		c.SvcCreateData.DnsRouting = s
	}
}

func (c *Config) SetSvcCreateHealthCustom(b bool) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_health_custom", b)
		c.SvcCreateData.HealthCustom = b
	}
}

func (c *Config) SetSvcCreateHealthFailthreshold(i uint) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_health_failthreshold", i)
		c.SvcCreateData.HealthFailThreshold = i
	}
}

func (c *Config) SetSvcCreateHealthPubDnsType(s string) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_health_pubdns_type", s)
		c.SvcCreateData.HealthPubDnsType = s
	}
}

func (c *Config) SetSvcCreateHealthPubDnsPath(s string) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_health_pubdns_path", s)
		c.SvcCreateData.HealthPubDnsPath = s
	}
}

func (c *Config) SetInstancePort(i uint) {
	if c._v != nil {
		c._v.Set("instance.instance_port", i)
		c.Instance.Port = i
	}
}

func (c *Config) SetInstanceVersion(s string) {
	if c._v != nil {
		c._v.Set("instance.instance_version", s)
		c.Instance.Version = s
	}
}

func (c *Config) SetInstancePrefix(s string) {
	if c._v != nil {
		c._v.Set("instance.instance_prefix", s)
		c.Instance.Prefix = s
	}
}

func (c *Config) SetInitialUnhealthy(b bool) {
	if c._v != nil {
		c._v.Set("instance.initial_unhealthy", b)
		c.Instance.InitialUnhealthy = b
	}
}

func (c *Config) SetInstanceId(s string) {
	if c._v != nil {
		c._v.Set("instance.instance_id", s)
		c.Instance.Id = s
	}
}

func (c *Config) SetSdTimeout(i uint) {
	if c._v != nil {
		c._v.Set("instance.sd_timeout", i)
		c.Instance.SdTimeout = i
	}
}

func (c *Config) SetInternalHealthFrequency(i uint) {
	if c._v != nil {
		c._v.Set("instance.internal_health_frequency", i)
		c.Instance.InternalHealthFrequency = i
	}
}

func (c *Config) SetAutoDeregisterPrior(b bool) {
	if c._v != nil {
		c._v.Set("instance.auto_deregister_prior", b)
		c.Instance.AutoDeregisterPrior = b
	}
}

func (c *Config) SetGrpcConnectTimeout(i uint) {
	if c._v != nil {
		c._v.Set("grpc.connection_timeout", i)
		c.Grpc.ConnectionTimeout = i
	}
}

func (c *Config) SetServerCertFile(s string) {
	if c._v != nil {
		c._v.Set("grpc.server_cert_file", s)
		c.Grpc.ServerCertFile = s
	}
}

func (c *Config) SetServerKeyFile(s string) {
	if c._v != nil {
		c._v.Set("grpc.server_key_file", s)
		c.Grpc.ServerKeyFile = s
	}
}

func (c *Config) SetClientCACertFiles(s string) {
	if c._v != nil {
		c._v.Set("grpc.client_ca_cert_files", s)
		c.Grpc.ClientCACertFiles = s
	}
}

func (c *Config) SetKeepAliveMinWait(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_min_wait", i)
		c.Grpc.KeepAliveMinWait = i
	}
}

func (c *Config) SetKeepAlivePermitWithoutStream(b bool) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_permit_without_stream", b)
		c.Grpc.KeepAlivePermitWithoutStream = b
	}
}

func (c *Config) SetKeepAliveMaxConnIdle(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_max_conn_idle", i)
		c.Grpc.KeepAliveMaxConnIdle = i
	}
}

func (c *Config) SetKeepAliveMaxConnAge(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_max_conn_age", i)
		c.Grpc.KeepAliveMaxConnAge = i
	}
}

func (c *Config) SetKeepAliveMaxConnAgeGrace(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_max_conn_age_grace", i)
		c.Grpc.KeepAliveMaxConnAgeGrace = i
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

func (c *Config) SetMaxReceiveMessageSize(i uint) {
	if c._v != nil {
		c._v.Set("grpc.max_recv_msg_size", i)
		c.Grpc.MaxReceiveMessageSize = i
	}
}

func (c *Config) SetMaxSendMessageSize(i uint) {
	if c._v	!= nil {
		c._v.Set("grpc.max_send_msg_size", i)
		c.Grpc.MaxSendMessageSize = i
	}
}

func (c *Config) SetMaxConcurrentStreams(i uint) {
	if c._v != nil {
		c._v.Set("grpc.max_concurrent_streams", i)
		c.Grpc.MaxConcurrentStreams = i
	}
}

func (c *Config) SetNumStreamWorkers(i uint) {
	if c._v != nil {
		c._v.Set("grpc.num_stream_workers", i)
		c.Grpc.NumStreamWorkers = i
	}
}

func (c *Config) SetRateLimitPerSecond(i uint) {
	if c._v != nil {
		c._v.Set("grpc.rate_limit_per_second", i)
		c.Grpc.RateLimitPerSecond = i
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
	c.Namespace = NamespaceData{}
	c.Service = ServiceData{}
	c.SvcCreateData = ServiceAutoCreate{}
	c.Instance = InstanceData{}
	c.Grpc = GrpcData{}

	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	if util.LenTrim(c.ConfigFileName) == 0 {
		c.ConfigFileName = "service"
	}

	c._v = &data.ViperConf{
		AppName: c.AppName,
		ConfigName: c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML: true,
		UseAutomaticEnvVar: false,
	}

	c._v.Default(
		"target.app_name", "connector.service").Default(					// required, app being created, be service specific
		"target.region", "us-east-1").Default(							// must be valid aws regions supported
		"namespace.ns_id", "").Default(									// from aws cloud map namespace - must be pre-created first
		"namespace.ns_name", "").Default(									// from aws cloud map namespace - must be pre-created first
		"service.sv_id", "").Default(										// from aws cloud map or leave blank for auto creation
		"service.sv_name", "").Default(									// from aws cloud map or leave blank for auto creation
		"service_auto_create.sac_dns_ttl", 90).Default(					// value to use for auto service creation, in seconds
		"service_auto_create.sac_dns_type", "srv").Default(				// value to use for auto service creation, srv or a
		"service_auto_create.sac_dns_routing", "multivalue").Default(		// value to use for auto service creation, multivalue or weighted
		"service_auto_create.sac_health_custom", true).Default(			// value to use for auto service creation, true or false
		"service_auto_create.sac_health_failthreshold", 1).Default(		// value to use for auto service creation, uint
		"service_auto_create.sac_health_pubdns_type", "").Default(		// value to use for auto service creation, http, https, or tcp
		"service_auto_create.sac_health_pubdns_path", "").Default(		// value to use for auto service creation, http or https health check resource path
		"instance.instance_port", 0).Default(								// instance launch tcp port, leave 0 as dynamic
		"instance.instance_version", "v1.0.0").Default(					// instance classification, vx.x.x style
		"instance.instance_prefix", "ms-").Default(						// instance id creation prefix, leave blank if no prefix
		"instance.initial_unhealthy", false).Default(						// instance launch initial health state when registered, true or false
		"instance.instance_id", "").Default(								// instance id currently launched
		"instance.sd_timeout", 5).Default(								// service discovery actions timeout seconds  (for cloudmap register, health update, deregister)
		"instance.internal_health_frequency", 5).Default(					// instance internal grpc health check frequency in seconds
		"instance.auto_deregister_prior", true).Default(					// automatically deregister prior service discovery registration if exists during launch, default = true
		"grpc.connection_timeout", 15).Default(							// grpc connection attempt time out in seconds, 0 for default of 120 seconds
		"grpc.server_cert_file", "").Default(								// grpc tls setup, path to cert pem file
		"grpc.server_key_file", "").Default(								// grpc tls setup, path to key pem file
		"grpc.client_ca_cert_files", "").Default(							// for mTLS setup, one or more client CA cert path to pem file, multiple files separated by comma
		"grpc.keepalive_min_wait", 0).Default(							// grpc keep-alive enforcement policy, minimum seconds before client may send keepalive, 0 for default 300 seconds
		"grpc.keepalive_permit_without_stream", false).Default(			// grpc keep-alive enforcement policy, allow client to keepalive if no stream, false is default
		"grpc.keepalive_max_conn_idle", 0).Default(						// grpc keep-alive option, max seconds before idle connect is closed, 0 for default of infinity
		"grpc.keepalive_max_conn_age", 0).Default(						// grpc keep-alive option, max seconds a connection may exist before closed, 0 for default of infinity
		"grpc.keepalive_max_conn_age_grace", 0).Default(					// grpc keep-alive option, max seconds added to max_conn_age to forcefully close, 0 for default of infinity
		"grpc.keepalive_inactive_ping_time_trigger", 0).Default(			// grpc keep-alive option, max seconds of no activity before server pings client, 0 for default of 2 hours
		"grpc.keepalive_inactive_ping_timeout", 0).Default( 				// grpc keep-alive option, max seconds of timeout during server to client ping, where no repsonse closes connection, 0 for default of 20 seconds
		"grpc.read_buffer_size", 0).Default(								// 0 for default 32 kb = 1024 * 32
		"grpc.write_buffer_size", 0).Default(								// 0 for default 32 kb = 1024 * 32
		"grpc.max_recv_msg_size", 0).Default(								// 0 for default 4 mb = 1024 * 1024 * 4, maximum bytes allowed to receive from client
		"grpc.max_send_msg_size", 0).Default(								// 0 for default maxInt32, maximum bytes allowed to send to client
		"grpc.max_concurrent_streams", 0).Default(						// defines maximum concurrent streams server will handle, 0 for http2 transport default value of 250
		"grpc.num_stream_workers", 0).Default(							// defines max of stream workers rather than new goroutine per stream, 0 for default of new per routine, if > 0, match to cpu core count for most performant
		"grpc.rate_limit_per_second", 0).Default(							// indicates rate limit per second, 0 disables rate limit
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
