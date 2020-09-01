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

type config struct {
	AppName string					`mapstructure:"-"`
	ConfigFileName string			`mapstructure:"-"`
	CustomConfigPath string			`mapstructure:"-"`

	_v *data.ViperConf				`mapstructure:"-"`

	Target targetData				`mapstructure:"target"`
	Namespace namespaceData			`mapstructure:"namespace"`
	Service serviceData				`mapstructure:"service"`
	Queues queuesData				`mapstructure:"queues"`
	Topics topicsData				`mapstructure:"topics"`
	SvcCreateData serviceAutoCreate	`mapstructure:"service_auto_create"`
	Instance instanceData			`mapstructure:"instance"`
	Grpc grpcData					`mapstructure:"grpc"`
}

type targetData struct {
	AppName string					`mapstructure:"app_name"`
	Region string					`mapstructure:"region"`
}

type namespaceData struct {
	Id string						`mapstructure:"ns_id"`
	Name string						`mapstructure:"ns_name"`
}

type serviceData struct {
	Id string						`mapstructure:"sv_id"`
	Name string						`mapstructure:"sv_name"`
	DiscoveryUseSqsSns bool			`mapstructure:"sv_discovery_use_sqs_sns"`
	MonitorUseSqsSns bool			`mapstrucutre:"sv_monitor_use_sqs_sns"`
	TracerUseSqsSns bool			`mapstructure:"sv_tracer_use_sqs_sns"`
	LoggerUseSqsSns bool			`mapstructure:"sv_logger_use_sqs_sns"`
}

type queuesData struct {
	SqsDiscoveryQueueNamePrefix string			`mapstructure:"sqs_discovery_queue_name_prefix"`
	SqsDiscoveryMessageRetentionSeconds uint	`mapstructure:"sqs_discovery_message_retention_seconds"`
	SqsDiscoveryQueueUrl string					`mapstructure:"sqs_discovery_queue_url"`
	SqsDiscoveryQueueArn string					`mapstructure:"sqs_discovery_queue_arn"`
	SqsMonitorQueueNamePrefix string			`mapstructure:"sqs_monitor_queue_name_prefix"`
	SqsMonitorMessageRetentionSeconds uint		`mapstructure:"sqs_monitor_message_retention_seconds"`
	SqsMonitorQueueUrl string					`mapstructure:"sqs_monitor_queue_url"`
	SqsMonitorQueueArn string					`mapstructure:"sqs_monitor_queue_arn"`
	SqsTracerQueueNamePrefix string				`mapstructure:"sqs_tracer_queue_name_prefix"`
	SqsTracerMessageRetentionSeconds uint		`mapstructure:"sqs_tracer_message_retention_seconds"`
	SqsTracerQueueUrl string					`mapstructure:"sqs_tracer_queue_url"`
	SqsTracerQueueArn string					`mapstructure:"sqs_tracer_queue_arn"`
	SqsLoggerQueueNamePrefix string				`mapstructure:"sqs_logger_queue_name_prefix"`
	SqsLoggerMessageRetentionSeconds uint		`mapstructure:"sqs_logger_message_retention_seconds"`
	SqsLoggerQueueUrl string					`mapstructure:"sqs_logger_queue_url"`
	SqsLoggerQueueArn string					`mapstructure:"sqs_logger_queue_arn"`
}

type topicsData struct {
	SnsDiscoveryTopicNamePrefix string			`mapstructure:"sns_discovery_topic_name_prefix"`
	SnsDiscoveryTopicArn string					`mapstructure:"sns_discovery_topic_arn"`
	SnsDiscoverySubscriptionArn string			`mapstructure:"sns_discovery_subscription_arn"`
	SnsMonitorTopicNamePrefix string			`mapstructure:"sns_monitor_topic_name_prefix"`
	SnsMonitorTopicArn string					`mapstructure:"sns_monitor_topic_arn"`
	SnsMonitorSubscriptionArn string			`mapstructure:"sns_monitor_subscription_arn"`
	SnsTracerTopicNamePrefix string				`mapstructure:"sns_tracer_topic_name_prefix"`
	SnsTracerTopicArn string					`mapstructure:"sns_tracer_topic_arn"`
	SnsTracerSubscriptionArn string				`mapstructure:"sns_tracer_subscription_arn"`
	SnsLoggerTopicNamePrefix string				`mapstructure:"sns_logger_topic_name_prefix"`
	SnsLoggerTopicArn string					`mapstructure:"sns_logger_topic_arn"`
	SnsLoggerSubscriptionArn string				`mapstructure:"sns_logger_subscription_arn"`
}

type serviceAutoCreate struct {
	DnsTTL uint						`mapstructure:"sac_dns_ttl"`
	DnsType string					`mapstructure:"sac_dns_type"`
	DnsRouting string				`mapstructure:"sac_dns_routing"`
	HealthCustom bool				`mapstructure:"sac_health_custom"`
	HealthFailThreshold uint		`mapstructure:"sac_health_failthreshold"`
	HealthPubDnsType string			`mapstructure:"sac_health_pubdns_type"`
	HealthPubDnsPath string			`mapstructure:"sac_health_pubdns_path"`
}

type instanceData struct {
	Port uint						`mapstructure:"instance_port"`
	Version string					`mapstructure:"instance_version"`
	Prefix string					`mapstructure:"instance_prefix"`
	InitialUnhealthy bool			`mapstructure:"initial_unhealthy"`
	Id string						`mapstructure:"instance_id"`
	SdTimeout uint					`mapstructure:"sd_timeout"`
	InternalHealthFrequency uint	`mapstructure:"internal_health_frequency"`
	AutoDeregisterPrior bool		`mapstructure:"auto_deregister_prior"`
}

type grpcData struct {
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
}

func (c *config) SetTargetAppName(s string) {
	if c._v != nil {
		c._v.Set("target.app_name", s)
		c.Target.AppName = s
	}
}

func (c *config) SetTargetRegion(s string) {
	if c._v != nil {
		c._v.Set("target.region", s)
		c.Target.Region = s
	}
}

func (c *config) SetNamespaceId(s string) {
	if c._v != nil {
		c._v.Set("namespace.ns_id", s)
		c.Namespace.Id = s
	}
}

func (c *config) SetNamespaceName(s string) {
	if c._v != nil {
		c._v.Set("namespace.ns_name", s)
		c.Namespace.Name = s
	}
}

func (c *config) SetServiceId(s string) {
	if c._v != nil {
		c._v.Set("service.sv_id", s)
		c.Service.Id = s
	}
}

func (c *config) SetServiceName(s string) {
	if c._v != nil {
		c._v.Set("service.sv_name", s)
		c.Service.Name = s
	}
}

func (c *config) SetDiscoveryUseSqsSns(b bool) {
	if c._v != nil {
		c._v.Set("service.sv_discovery_use_sqs_sns", b)
		c.Service.DiscoveryUseSqsSns = b
	}
}

func (c *config) SetMonitorUseSqsSns(b bool) {
	if c._v != nil {
		c._v.Set("service.sv_monitor_use_sqs_sns", b)
		c.Service.MonitorUseSqsSns = b
	}
}

func (c *config) SetTracerUseSqsSns(b bool) {
	if c._v != nil {
		c._v.Set("service.sv_tracer_use_sqs_sns", b)
		c.Service.TracerUseSqsSns = b
	}
}

func (c *config) SetLoggerUseSqsSns(b bool) {
	if c._v != nil {
		c._v.Set("service.sv_logger_use_sqs_sns", b)
		c.Service.LoggerUseSqsSns = b
	}
}

func (c *config) SetSqsDiscoveryQueueNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_discovery_queue_name_prefix", s)
		c.Queues.SqsDiscoveryQueueNamePrefix = s
	}
}

func (c *config) SetSqsDiscoveryMessageRetensionSeconds(i uint) {
	if c._v != nil {
		c._v.Set("queues.sqs_discovery_message_retension_seconds", i)
		c.Queues.SqsDiscoveryMessageRetentionSeconds = i
	}
}

func (c *config) SetSqsDiscoveryQueueUrl(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_discovery_queue_url", s)
		c.Queues.SqsDiscoveryQueueUrl = s
	}
}

func (c *config) SetSqsDiscoveryQueueArn(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_discovery_queue_arn", s)
		c.Queues.SqsDiscoveryQueueArn = s
	}
}

func (c *config) SetSqsMonitorQueueNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_monitor_queue_name_prefix", s)
		c.Queues.SqsMonitorQueueNamePrefix = s
	}
}

func (c *config) SetSqsMonitorMessageRetentionSeconds(i uint) {
	if c._v != nil {
		c._v.Set("queues.sqs_monitor_message_retention_seconds", i)
		c.Queues.SqsMonitorMessageRetentionSeconds = i
	}
}

func (c *config) SetSqsMonitorQueueUrl(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_monitor_queue_url", s)
		c.Queues.SqsMonitorQueueUrl = s
	}
}

func (c *config) SetSqsMonitorQueueArn(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_monitor_queue_arn", s)
		c.Queues.SqsMonitorQueueArn = s
	}
}

func (c *config) SetSqsTracerQueueNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_tracer_queue_name_prefix", s)
		c.Queues.SqsTracerQueueNamePrefix = s
	}
}

func (c *config) SetSqsTracerMessageRetentionSeconds(i uint) {
	if c._v != nil {
		c._v.Set("queues.sqs_tracer_message_retention_seconds", i)
		c.Queues.SqsTracerMessageRetentionSeconds = i
	}
}

func (c *config) SetSqsTracerQueueUrl(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_tracer_queue_url", s)
		c.Queues.SqsTracerQueueUrl = s
	}
}

func (c *config) SetSqsTracerQueueArn(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_tracer_queue_arn", s)
		c.Queues.SqsTracerQueueArn = s
	}
}

func (c *config) SetSqsLoggerQueueNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_logger_queue_name_prefix", s)
		c.Queues.SqsLoggerQueueNamePrefix = s
	}
}

func (c *config) SetSqsLoggerMessageRetentionRecords(i uint) {
	if c._v != nil {
		c._v.Set("queues.sqs_logger_message_retention_seconds", i)
		c.Queues.SqsLoggerMessageRetentionSeconds = i
	}
}

func (c *config) SetSqsLoggerQueueUrl(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_logger_queue_url", s)
		c.Queues.SqsLoggerQueueUrl = s
	}
}

func (c *config) SetSqsLoggerQueueArn(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_logger_queue_arn", s)
		c.Queues.SqsLoggerQueueArn = s
	}
}

func (c *config) SetSnsDiscoveryTopicNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_discovery_topic_name_prefix", s)
		c.Topics.SnsDiscoveryTopicNamePrefix = s
	}
}

func (c *config) SetSnsDiscoveryTopicArn(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_discovery_topic_arn", s)
		c.Topics.SnsDiscoveryTopicArn = s
	}
}

func (c *config) SetSnsDiscoverySubscriptionArn(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_discovery_subscription_arn", s)
		c.Topics.SnsDiscoverySubscriptionArn = s
	}
}

func (c *config) SetSnsMonitorTopicNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_monitor_topic_name_prefix", s)
		c.Topics.SnsMonitorTopicNamePrefix = s
	}
}

func (c *config) SetSnsMonitorTopicArn(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_monitor_topic_arn", s)
		c.Topics.SnsMonitorTopicArn = s
	}
}

func (c *config) SetSnsMonitorSubscriptionArn(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_monitor_subscription_arn", s)
		c.Topics.SnsMonitorSubscriptionArn = s
	}
}

func (c *config) SetSnsTracerTopicNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_tracer_topic_name_prefix", s)
		c.Topics.SnsTracerTopicNamePrefix = s
	}
}

func (c *config) SetSnsTracerTopicArn(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_tracer_topic_arn", s)
		c.Topics.SnsTracerTopicArn = s
	}
}

func (c *config) SetSnsTracerSubscriptionArn(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_tracer_subscription_arn", s)
		c.Topics.SnsTracerSubscriptionArn = s
	}
}

func (c *config) SetSnsLoggerTopicNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_logger_topic_name_prefix", s)
		c.Topics.SnsLoggerTopicNamePrefix = s
	}
}

func (c *config) SetSnsLoggerTopicArn(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_logger_topic_arn", s)
		c.Topics.SnsLoggerTopicArn = s
	}
}

func (c *config) SetSnsLoggerSubscriptionArn(s string) {
	if c._v != nil {
		c._v.Set("topics.sns_logger_subscription_arn", s)
		c.Topics.SnsLoggerSubscriptionArn = s
	}
}

func (c *config) SetSvcCreateDnsTTL(i uint) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_dns_ttl", i)
		c.SvcCreateData.DnsTTL = i
	}
}

func (c *config) SetSvcCreateDnsType(s string) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_dns_type", s)
		c.SvcCreateData.DnsType = s
	}
}

func (c *config) SetSvcCreateDnsRouting(s string) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_dns_routing", s)
		c.SvcCreateData.DnsRouting = s
	}
}

func (c *config) SetSvcCreateHealthCustom(b bool) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_health_custom", b)
		c.SvcCreateData.HealthCustom = b
	}
}

func (c *config) SetSvcCreateHealthFailthreshold(i uint) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_health_failthreshold", i)
		c.SvcCreateData.HealthFailThreshold = i
	}
}

func (c *config) SetSvcCreateHealthPubDnsType(s string) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_health_pubdns_type", s)
		c.SvcCreateData.HealthPubDnsType = s
	}
}

func (c *config) SetSvcCreateHealthPubDnsPath(s string) {
	if c._v != nil {
		c._v.Set("service_auto_create.sac_health_pubdns_path", s)
		c.SvcCreateData.HealthPubDnsPath = s
	}
}

func (c *config) SetInstancePort(i uint) {
	if c._v != nil {
		c._v.Set("instance.instance_port", i)
		c.Instance.Port = i
	}
}

func (c *config) SetInstanceVersion(s string) {
	if c._v != nil {
		c._v.Set("instance.instance_version", s)
		c.Instance.Version = s
	}
}

func (c *config) SetInstancePrefix(s string) {
	if c._v != nil {
		c._v.Set("instance.instance_prefix", s)
		c.Instance.Prefix = s
	}
}

func (c *config) SetInitialUnhealthy(b bool) {
	if c._v != nil {
		c._v.Set("instance.initial_unhealthy", b)
		c.Instance.InitialUnhealthy = b
	}
}

func (c *config) SetInstanceId(s string) {
	if c._v != nil {
		c._v.Set("instance.instance_id", s)
		c.Instance.Id = s
	}
}

func (c *config) SetSdTimeout(i uint) {
	if c._v != nil {
		c._v.Set("instance.sd_timeout", i)
		c.Instance.SdTimeout = i
	}
}

func (c *config) SetInternalHealthFrequency(i uint) {
	if c._v != nil {
		c._v.Set("instance.internal_health_frequency", i)
		c.Instance.InternalHealthFrequency = i
	}
}

func (c *config) SetAutoDeregisterPrior(b bool) {
	if c._v != nil {
		c._v.Set("instance.auto_deregister_prior", b)
		c.Instance.AutoDeregisterPrior = b
	}
}

func (c *config) SetGrpcConnectTimeout(i uint) {
	if c._v != nil {
		c._v.Set("grpc.connection_timeout", i)
		c.Grpc.ConnectionTimeout = i
	}
}

func (c *config) SetServerCertFile(s string) {
	if c._v != nil {
		c._v.Set("grpc.server_cert_file", s)
		c.Grpc.ServerCertFile = s
	}
}

func (c *config) SetServerKeyFile(s string) {
	if c._v != nil {
		c._v.Set("grpc.server_key_file", s)
		c.Grpc.ServerKeyFile = s
	}
}

func (c *config) SetClientCACertFiles(s string) {
	if c._v != nil {
		c._v.Set("grpc.client_ca_cert_files", s)
		c.Grpc.ClientCACertFiles = s
	}
}

func (c *config) SetKeepAliveMinWait(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_min_wait", i)
		c.Grpc.KeepAliveMinWait = i
	}
}

func (c *config) SetKeepAlivePermitWithoutStream(b bool) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_permit_without_stream", b)
		c.Grpc.KeepAlivePermitWithoutStream = b
	}
}

func (c *config) SetKeepAliveMaxConnIdle(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_max_conn_idle", i)
		c.Grpc.KeepAliveMaxConnIdle = i
	}
}

func (c *config) SetKeepAliveMaxConnAge(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_max_conn_age", i)
		c.Grpc.KeepAliveMaxConnAge = i
	}
}

func (c *config) SetKeepAliveMaxConnAgeGrace(i uint) {
	if c._v != nil {
		c._v.Set("grpc.keepalive_max_conn_age_grace", i)
		c.Grpc.KeepAliveMaxConnAgeGrace = i
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

func (c *config) SetMaxReceiveMessageSize(i uint) {
	if c._v != nil {
		c._v.Set("grpc.max_recv_msg_size", i)
		c.Grpc.MaxReceiveMessageSize = i
	}
}

func (c *config) SetMaxSendMessageSize(i uint) {
	if c._v	!= nil {
		c._v.Set("grpc.max_send_msg_size", i)
		c.Grpc.MaxSendMessageSize = i
	}
}

func (c *config) SetMaxConcurrentStreams(i uint) {
	if c._v != nil {
		c._v.Set("grpc.max_concurrent_streams", i)
		c.Grpc.MaxConcurrentStreams = i
	}
}

func (c *config) SetNumStreamWorkers(i uint) {
	if c._v != nil {
		c._v.Set("grpc.num_stream_workers", i)
		c.Grpc.NumStreamWorkers = i
	}
}

func (c *config) SetRateLimitPerSecond(i uint) {
	if c._v != nil {
		c._v.Set("grpc.rate_limit_per_second", i)
		c.Grpc.RateLimitPerSecond = i
	}
}

// Read will load config settings from disk
func (c *config) Read() error {
	c._v = nil
	c.Target = targetData{}
	c.Namespace = namespaceData{}
	c.Service = serviceData{}
	c.Queues = queuesData{}
	c.Topics = topicsData{}
	c.SvcCreateData = serviceAutoCreate{}
	c.Instance = instanceData{}
	c.Grpc = grpcData{}

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
		"target.app_name", "connector.service").Default(									// required, app being created, be service specific
		"target.region", "us-east-1").Default(											// must be valid aws regions supported
		"namespace.ns_id", "").Default(													// from aws cloud map namespace - must be pre-created first
		"namespace.ns_name", "").Default(													// from aws cloud map namespace - must be pre-created first
		"service.sv_id", "").Default(														// from aws cloud map or leave blank for auto creation
		"service.sv_name", "").Default(													// from aws cloud map or leave blank for auto creation
		"service.sv_discovery_use_sqs_sns", false).Default(								// indicate if this service will use sqs and sns for service discovery, default = false
		"service.sv_monitor_use_sqs_sns", false).Default(									// indicate if this service will use sqs and sns for service monitor, default = false
		"service.sv_tracer_use_sqs_sns", false).Default(									// indicate if this service will use sqs and sns for service tracer, default = false
		"service.sv_logger_use_sqs_sns", false).Default(									// indicate if this service will use sqs and sns for service logger, default = false
		"queues.sqs_discovery_queue_name_prefix", "service-discovery-data-").Default(		// sqs queue name prefix used for service discovery data queuing, if name is not provided, default = service-discovery-data-
		"queues.sqs_discovery_message_retention_seconds", 300).Default(					// sqs service discovery queue's messages retention seconds, default = 300 seconds (5 Minutes)
		"queues.sqs_discovery_queue_url", "").Default(									// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service discovery data queue used by this service (auto set by service upon creation)
		"queues.sqs_discovery_queue_arn", "").Default(									// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service discovery data queue used by this service (auto set by service upon creation)
		"queues.sqs_monitor_queue_name_prefix", "service-monitor-data-").Default(			// sqs queue name prefix used for service monitoring data queuing, if name is not provided, default = service-monitor-data-
		"queues.sqs_monitor_message_retention_seconds", 14400).Default(					// sqs service monitor queue's messages retention seconds, default = 14,400 seconds (4 Hours)
		"queues.sqs_monitor_queue_url", "").Default(										// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service monitor data queue used by this service (auto set by service upon creation)
		"queues.sqs_monitor_queue_arn", "").Default(										// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service monitor data queue used by this service (auto set by service upon creation)
		"queues.sqs_tracer_queue_name_prefix", "service-tracer-data-").Default(			// sqs queue name prefix used for service tracing data queuing, if name is not provided, default = service-tracer-data-
		"queues.sqs_tracer_message_retention_seconds", 14400).Default(					// sqs service tracer queue's messages retention seconds, default = 14,400 seconds (4 Hours)
		"queues.sqs_tracer_queue_url", "").Default(										// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service tracer data queue used by this service (auto set by service upon creation)
		"queues.sqs_tracer_queue_arn", "").Default(										// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service tracer data queue used by this service (auto set by service upon creation)
		"queues.sqs_logger_queue_name_prefix", "service-logger-data-").Default(			// sqs queue name prefix used for service logging data queuing, if name is not provided, default = service-logger-data-
		"queues.sqs_logger_message_retention_seconds", 14400).Default(					// sqs service logger queue's messages retention seconds, default = 14,400 seconds (4 Hours)
		"queues.sqs_logger_queue_url", "").Default(										// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service logger data queue used by this service (auto set by service upon creation)
		"queues.sqs_logger_queue_arn", "").Default(										// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service logger data queue used by this service (auto set by service upon creation)
		"topics.sns_discovery_topic_name_prefix", "service-discovery-notify-").Default(	// sns topic name prefix used for discovery data notification, if name is not provided, default = service-discovery-notify-
		"topics.sns_discovery_topic_arn", "").Default(									// sns topic's topicArn as generated by aws sns for the corresponding service discovery topic used by this service (auto set by service upon creation)
		"topics.sns_discovery_subscription_arn", "").Default(								// sns topic subscription arn as generated by aws during subscribe event
		"topics.sns_monitor_topic_name_prefix", "service-monitor-notify-").Default(		// sns topic name prefix used for monitor data notification, if name is not provided, default = service-monitor-notify-
		"topics.sns_monitor_topic_arn", "").Default(										// sns topic's topicArn as generated by aws sns for the corresponding service monitor topic used by this service (auto set by service upon creation)
		"topics.sns_monitor_subscription_arn", "").Default(								// sns topic subscription arn as generated by aws during subscribe event
		"topics.sns_tracer_topic_name_prefix", "service-tracer-notify-").Default(			// sns topic name prefix used for tracer data notification, if name is not provided, default = service-tracer-notify-
		"topics.sns_tracer_topic_arn", "").Default( 										// sns topic's topicArn as generated by aws sns for the corresponding service tracer topic used by this service (auto set by service upon creation)
		"topics.sns_tracer_subscription_arn", "").Default(							 	// sns topic subscription arn as generated by aws during subscribe event
		"topics.sns_logger_topic_name_prefix", "service-logger-notify-").Default( 		// sns topic name prefix used for logger data notification, if name is not provided, default = service-logger-notify-
		"topics.sns_logger_topic_arn", "").Default( 										// sns topic's topicArn as generated by aws sns for the corresponding service logger topic used by this service (auto set by service upon creation)
		"topics.sns_logger_subscription_arn", "").Default( 								// sns topic subscription arn as generated by aws during subscribe event
		"service_auto_create.sac_dns_ttl", 90).Default(									// value to use for auto service creation, in seconds
		"service_auto_create.sac_dns_type", "srv").Default(								// value to use for auto service creation, srv or a
		"service_auto_create.sac_dns_routing", "multivalue").Default(						// value to use for auto service creation, multivalue or weighted
		"service_auto_create.sac_health_custom", true).Default(							// value to use for auto service creation, true or false
		"service_auto_create.sac_health_failthreshold", 1).Default(						// value to use for auto service creation, uint
		"service_auto_create.sac_health_pubdns_type", "").Default(						// value to use for auto service creation, http, https, or tcp
		"service_auto_create.sac_health_pubdns_path", "").Default(						// value to use for auto service creation, http or https health check resource path
		"instance.instance_port", 0).Default(												// instance launch tcp port, leave 0 as dynamic
		"instance.instance_version", "v1.0.0").Default(									// instance classification, vx.x.x style
		"instance.instance_prefix", "ms-").Default(										// instance id creation prefix, leave blank if no prefix
		"instance.initial_unhealthy", false).Default(										// instance launch initial health state when registered, true or false
		"instance.instance_id", "").Default(												// instance id currently launched
		"instance.sd_timeout", 5).Default(												// service discovery actions timeout seconds  (for cloudmap register, health update, deregister)
		"instance.internal_health_frequency", 5).Default(									// instance internal grpc health check frequency in seconds
		"instance.auto_deregister_prior", true).Default(									// automatically deregister prior service discovery registration if exists during launch, default = true
		"grpc.connection_timeout", 15).Default(											// grpc connection attempt time out in seconds, 0 for default of 120 seconds
		"grpc.server_cert_file", "").Default(												// grpc tls setup, path to cert pem file
		"grpc.server_key_file", "").Default(												// grpc tls setup, path to key pem file
		"grpc.client_ca_cert_files", "").Default(											// for mTLS setup, one or more client CA cert path to pem file, multiple files separated by comma
		"grpc.keepalive_min_wait", 0).Default(											// grpc keep-alive enforcement policy, minimum seconds before client may send keepalive, 0 for default 300 seconds
		"grpc.keepalive_permit_without_stream", false).Default(							// grpc keep-alive enforcement policy, allow client to keepalive if no stream, false is default
		"grpc.keepalive_max_conn_idle", 0).Default(										// grpc keep-alive option, max seconds before idle connect is closed, 0 for default of infinity
		"grpc.keepalive_max_conn_age", 0).Default(										// grpc keep-alive option, max seconds a connection may exist before closed, 0 for default of infinity
		"grpc.keepalive_max_conn_age_grace", 0).Default(									// grpc keep-alive option, max seconds added to max_conn_age to forcefully close, 0 for default of infinity
		"grpc.keepalive_inactive_ping_time_trigger", 0).Default(							// grpc keep-alive option, max seconds of no activity before server pings client, 0 for default of 2 hours
		"grpc.keepalive_inactive_ping_timeout", 0).Default( 								// grpc keep-alive option, max seconds of timeout during server to client ping, where no repsonse closes connection, 0 for default of 20 seconds
		"grpc.read_buffer_size", 0).Default(												// 0 for default 32 kb = 1024 * 32
		"grpc.write_buffer_size", 0).Default(												// 0 for default 32 kb = 1024 * 32
		"grpc.max_recv_msg_size", 0).Default(												// 0 for default 4 mb = 1024 * 1024 * 4, maximum bytes allowed to receive from client
		"grpc.max_send_msg_size", 0).Default(												// 0 for default maxInt32, maximum bytes allowed to send to client
		"grpc.max_concurrent_streams", 0).Default(										// defines maximum concurrent streams server will handle, 0 for http2 transport default value of 250
		"grpc.num_stream_workers", 0).Default(											// defines max of stream workers rather than new goroutine per stream, 0 for default of new per routine, if > 0, match to cpu core count for most performant
		"grpc.rate_limit_per_second", 0)													// indicates rate limit per second, 0 disables rate limit

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
