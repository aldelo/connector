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
	"fmt"
	"os"
	"strings"

	util "github.com/aldelo/common"
	data "github.com/aldelo/common/wrapper/viper"
)

type config struct {
	AppName          string `mapstructure:"-"`
	ConfigFileName   string `mapstructure:"-"`
	CustomConfigPath string `mapstructure:"-"`

	_v *data.ViperConf `mapstructure:"-"`

	Target        targetData        `mapstructure:"target"`
	Namespace     namespaceData     `mapstructure:"namespace"`
	Service       serviceData       `mapstructure:"service"`
	Queues        queuesData        `mapstructure:"queues"`
	Topics        topicsData        `mapstructure:"topics"`
	SvcCreateData serviceAutoCreate `mapstructure:"service_auto_create"`
	Instance      instanceData      `mapstructure:"instance"`
	Grpc          grpcData          `mapstructure:"grpc"`
}

type targetData struct {
	AppName string `mapstructure:"app_name"`
	Region  string `mapstructure:"region"`
}

type namespaceData struct {
	Id   string `mapstructure:"ns_id"`
	Name string `mapstructure:"ns_name"`
}

type serviceData struct {
	Id                    string `mapstructure:"sv_id"`
	Name                  string `mapstructure:"sv_name"`
	DiscoveryUseSqsSns    bool   `mapstructure:"sv_discovery_use_sqs_sns"`
	TracerUseXRay         bool   `mapstructure:"sv_tracer_use_xray"`
	LoggerUseSqs          bool   `mapstructure:"sv_logger_use_sqs"`
	RestTargetCACertFiles string `mapstructure:"rest_target_ca_cert_files"`
}

type queuesData struct {
	SqsDiscoveryQueueNamePrefix         string `mapstructure:"sqs_discovery_queue_name_prefix"`
	SqsDiscoveryMessageRetentionSeconds uint   `mapstructure:"sqs_discovery_message_retention_seconds"`
	SqsDiscoveryQueueUrl                string `mapstructure:"sqs_discovery_queue_url"`
	SqsDiscoveryQueueArn                string `mapstructure:"sqs_discovery_queue_arn"`
	SqsLoggerQueueNamePrefix            string `mapstructure:"sqs_logger_queue_name_prefix"`
	SqsLoggerMessageRetentionSeconds    uint   `mapstructure:"sqs_logger_message_retention_seconds"`
	SqsLoggerQueueUrl                   string `mapstructure:"sqs_logger_queue_url"`
	SqsLoggerQueueArn                   string `mapstructure:"sqs_logger_queue_arn"`
}

type topicsData struct {
	SnsDiscoveryTopicNamePrefix string `mapstructure:"sns_discovery_topic_name_prefix"`
	SnsDiscoveryTopicArn        string `mapstructure:"sns_discovery_topic_arn"`
	SnsDiscoverySubscriptionArn string `mapstructure:"sns_discovery_subscription_arn"`
}

type serviceAutoCreate struct {
	DnsTTL              uint   `mapstructure:"sac_dns_ttl"`
	DnsType             string `mapstructure:"sac_dns_type"`
	DnsRouting          string `mapstructure:"sac_dns_routing"`
	HealthCustom        bool   `mapstructure:"sac_health_custom"`
	HealthFailThreshold uint   `mapstructure:"sac_health_failthreshold"`
	HealthPubDnsType    string `mapstructure:"sac_health_pubdns_type"`
	HealthPubDnsPath    string `mapstructure:"sac_health_pubdns_path"`
}

type instanceData struct {
	FavorPublicIP                      bool   `mapstructure:"instance_favor_public_ip"`
	PublicIPGateway                    string `mapstructure:"public_ip_discovery_gateway"`
	PublicIPGatewayKey                 string `mapstructure:"public_ip_gateway_key"`
	Port                               uint   `mapstructure:"instance_port"`
	Version                            string `mapstructure:"instance_version"`
	Prefix                             string `mapstructure:"instance_prefix"`
	InitialUnhealthy                   bool   `mapstructure:"initial_unhealthy"`
	Id                                 string `mapstructure:"instance_id"`
	SdTimeout                          uint   `mapstructure:"sd_timeout"`
	InternalHealthFrequency            uint   `mapstructure:"internal_health_frequency"`
	AutoDeregisterPrior                bool   `mapstructure:"auto_deregister_prior"`
	HealthReportServiceUrl             string `mapstructure:"health_report_service_url"`
	HealthReportUpdateFrequencySeconds uint   `mapstructure:"health_report_update_frequency_seconds"`
	HashKeyName                        string `mapstructure:"hash_key_name"`
	HashKeySecret                      string `mapstructure:"hash_key_secret"`
}

type grpcData struct {
	ConnectionTimeout                uint   `mapstructure:"connection_timeout"`
	ServerCertFile                   string `mapstructure:"server_cert_file"`
	ServerKeyFile                    string `mapstructure:"server_key_file"`
	ClientCACertFiles                string `mapstructure:"client_ca_cert_files"`
	KeepAliveMinWait                 uint   `mapstructure:"keepalive_min_wait"`
	KeepAlivePermitWithoutStream     bool   `mapstructure:"keepalive_permit_without_stream"`
	KeepAliveMaxConnIdle             uint   `mapstructure:"keepalive_max_conn_idle"`
	KeepAliveMaxConnAge              uint   `mapstructure:"keepalive_max_conn_age"`
	KeepAliveMaxConnAgeGrace         uint   `mapstructure:"keepalive_max_conn_age_grace"`
	KeepAliveInactivePingTimeTrigger uint   `mapstructure:"keepalive_inactive_ping_time_trigger"`
	KeepAliveInactivePingTimeout     uint   `mapstructure:"keepalive_inactive_ping_timeout"`
	ReadBufferSize                   uint   `mapstructure:"read_buffer_size"`
	WriteBufferSize                  uint   `mapstructure:"write_buffer_size"`
	MaxReceiveMessageSize            uint   `mapstructure:"max_recv_msg_size"`
	MaxSendMessageSize               uint   `mapstructure:"max_send_msg_size"`
	MaxConcurrentStreams             uint   `mapstructure:"max_concurrent_streams"`
	NumStreamWorkers                 uint   `mapstructure:"num_stream_workers"`
	RateLimitPerSecond               uint   `mapstructure:"rate_limit_per_second"`
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

func (c *config) SetTracerUseXRay(b bool) {
	if c._v != nil {
		c._v.Set("service.sv_tracer_use_xray", b)
		c.Service.TracerUseXRay = b
	}
}

func (c *config) SetLoggerUseSqs(b bool) {
	if c._v != nil {
		c._v.Set("service.sv_logger_use_sqs", b)
		c.Service.LoggerUseSqs = b
	}
}

func (c *config) SetRestTargetCACertFiles(s string) {
	if c._v != nil {
		c._v.Set("service.rest_target_ca_cert_files", s)
		c.Service.RestTargetCACertFiles = s
	}
}

func (c *config) SetSqsDiscoveryQueueNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_discovery_queue_name_prefix", s)
		c.Queues.SqsDiscoveryQueueNamePrefix = s
	}
}

// FIX #1: Viper key was "sqs_discovery_message_retension_seconds" (typo: "retension")
// but the mapstructure tag is "sqs_discovery_message_retention_seconds" (correct: "retention").
// Unmarshal reads via the mapstructure tag, so the setter wrote to a different key than
// Unmarshal reads from. Values set via this setter were silently lost on next Read().
func (c *config) SetSqsDiscoveryMessageRetentionSeconds(i uint) {
	if c._v != nil {
		c._v.Set("queues.sqs_discovery_message_retention_seconds", i)
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

func (c *config) SetSqsLoggerQueueNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("queues.sqs_logger_queue_name_prefix", s)
		c.Queues.SqsLoggerQueueNamePrefix = s
	}
}

func (c *config) SetSqsLoggerMessageRetentionSeconds(i uint) {
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

func (c *config) SetInstanceFavorPublicIP(b bool) {
	if c._v != nil {
		c._v.Set("instance.instance_favor_public_ip", b)
		c.Instance.FavorPublicIP = b
	}
}

func (c *config) SetPublicIPDiscoveryGateway(s string) {
	if c._v != nil {
		c._v.Set("instance.public_ip_discovery_gateway", s)
		c.Instance.PublicIPGateway = s
	}
}

func (c *config) SetPublicIPGatewayKey(s string) {
	if c._v != nil {
		c._v.Set("instance.public_ip_gateway_key", s)
		c.Instance.PublicIPGatewayKey = s
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

func (c *config) SetHealthReportServiceUrl(s string) {
	if c._v != nil {
		c._v.Set("instance.health_report_service_url", s)
		c.Instance.HealthReportServiceUrl = s
	}
}

func (c *config) SetHealthReportUpdateFrequencySeconds(i uint) {
	if c._v != nil {
		c._v.Set("instance.health_report_update_frequency_seconds", i)
		c.Instance.HealthReportUpdateFrequencySeconds = i
	}
}

func (c *config) SetHashKeyName(s string) {
	if c._v != nil {
		c._v.Set("instance.hash_key_name", s)
		c.Instance.HashKeyName = s
	}
}

func (c *config) SetHashKeySecret(s string) {
	if c._v != nil {
		c._v.Set("instance.hash_key_secret", s)
		c.Instance.HashKeySecret = s
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
	if c._v != nil {
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

// Read will load config settings from disk.
// FIX #2: Builds into local variables first and only overwrites c._v and data fields
// on success, so a failed Read() doesn't destroy previously valid state.
func (c *config) Read() error {
	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	configFileName := c.ConfigFileName
	if util.LenTrim(configFileName) == 0 {
		configFileName = "service"
	}

	v := &data.ViperConf{
		AppName:          c.AppName,
		ConfigName:       configFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML:            true,
		UseAutomaticEnvVar: false,
	}

	v.Default(
		"target.app_name", "connector.service").Default(
		"target.region", "us-east-1").Default(
		"namespace.ns_id", "").Default(
		"namespace.ns_name", "").Default(
		"service.sv_id", "").Default(
		"service.sv_name", "").Default(
		"service.sv_discovery_use_sqs_sns", false).Default(
		"service.sv_tracer_use_xray", false).Default(
		"service.sv_logger_use_sqs", false).Default(
		"service.rest_target_ca_cert_files", "").Default(
		"queues.sqs_discovery_queue_name_prefix", "service-discovery-data-").Default(
		"queues.sqs_discovery_message_retention_seconds", 300).Default(
		"queues.sqs_discovery_queue_url", "").Default(
		"queues.sqs_discovery_queue_arn", "").Default(
		"queues.sqs_logger_queue_name_prefix", "service-logger-data-").Default(
		"queues.sqs_logger_message_retention_seconds", 14400).Default(
		"queues.sqs_logger_queue_url", "").Default(
		"queues.sqs_logger_queue_arn", "").Default(
		"topics.sns_discovery_topic_name_prefix", "service-discovery-notify-").Default(
		"topics.sns_discovery_topic_arn", "").Default(
		"topics.sns_discovery_subscription_arn", "").Default(
		"service_auto_create.sac_dns_ttl", 90).Default(
		"service_auto_create.sac_dns_type", "srv").Default(
		"service_auto_create.sac_dns_routing", "multivalue").Default(
		"service_auto_create.sac_health_custom", true).Default(
		"service_auto_create.sac_health_failthreshold", 1).Default(
		"service_auto_create.sac_health_pubdns_type", "").Default(
		"service_auto_create.sac_health_pubdns_path", "").Default(
		// FIX #3: Was "false" (string) — bool field requires false (unquoted bool).
		// Viper may coerce it, but it's inconsistent with all other bool defaults
		// and risks type mismatch in strict unmarshal modes.
		"instance.instance_favor_public_ip", false).Default(
		"instance.public_ip_discovery_gateway", "").Default(
		"instance.public_ip_gateway_key", "").Default(
		"instance.instance_port", 0).Default(
		"instance.instance_version", "v1.0.0").Default(
		"instance.instance_prefix", "ms-").Default(
		"instance.initial_unhealthy", false).Default(
		"instance.instance_id", "").Default(
		"instance.sd_timeout", 5).Default(
		"instance.internal_health_frequency", 5).Default(
		"instance.auto_deregister_prior", true).Default(
		"instance.health_report_service_url", "").Default(
		"instance.health_report_update_frequency_seconds", 120).Default(
		"instance.hash_key_name", "").Default(
		"instance.hash_key_secret", "").Default(
		"grpc.connection_timeout", 15).Default(
		"grpc.server_cert_file", "").Default(
		"grpc.server_key_file", "").Default(
		"grpc.client_ca_cert_files", "").Default(
		"grpc.keepalive_min_wait", 0).Default(
		"grpc.keepalive_permit_without_stream", false).Default(
		"grpc.keepalive_max_conn_idle", 0).Default(
		"grpc.keepalive_max_conn_age", 0).Default(
		"grpc.keepalive_max_conn_age_grace", 0).Default(
		"grpc.keepalive_inactive_ping_time_trigger", 0).Default(
		"grpc.keepalive_inactive_ping_timeout", 0).Default(
		"grpc.read_buffer_size", 0).Default(
		"grpc.write_buffer_size", 0).Default(
		"grpc.max_recv_msg_size", 0).Default(
		"grpc.max_send_msg_size", 0).Default(
		"grpc.max_concurrent_streams", 0).Default(
		"grpc.num_stream_workers", 0).Default(
		"grpc.rate_limit_per_second", 0)

	if ok, err := v.Init(); err != nil {
		return err
	} else {
		if !ok {
			if e := v.Save(); e != nil {
				return fmt.Errorf("create config file failed: %w", e)
			}
		} else {
			v.WatchConfig()
		}
	}

	// Unmarshal into a temporary config to validate before committing
	tempCfg := &config{}
	if err := v.Unmarshal(tempCfg); err != nil {
		return err
	}

	// All succeeded — commit to receiver
	c._v = v
	c.ConfigFileName = configFileName
	c.Target = tempCfg.Target
	c.Namespace = tempCfg.Namespace
	c.Service = tempCfg.Service
	c.Queues = tempCfg.Queues
	c.Topics = tempCfg.Topics
	c.SvcCreateData = tempCfg.SvcCreateData
	c.Instance = tempCfg.Instance
	c.Grpc = tempCfg.Grpc

	return nil
}

// Save persists config settings to disk.
// FIX #4: Returns an error when _v is nil instead of silently succeeding.
func (c *config) Save() error {
	if strings.ToLower(os.Getenv("CONFIG_READ_ONLY")) == "true" {
		return nil
	}
	if c._v == nil {
		return fmt.Errorf("cannot save: viper config not initialized (call Read first)")
	}
	return c._v.Save()
}
