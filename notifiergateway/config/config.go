package config

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
	"github.com/aldelo/common/wrapper/aws/awsregion"
	data "github.com/aldelo/common/wrapper/viper"
)

type Config struct {
	AppName          string `mapstructure:"-"`
	ConfigFileName   string `mapstructure:"-"`
	CustomConfigPath string `mapstructure:"-"`

	_v *data.ViperConf `mapstructure:"-"`

	NotifierGatewayData *NotifierGatewayData `mapstructure:"notifier_gateway"`
}

// FIX #7: Exported so other packages can declare variables of this type
// and access the struct fields directly.
type NotifierGatewayData struct {
	DynamoDBAwsRegion                   string        `mapstructure:"dynamodb_aws_region"`
	DynamoDBUseDax                      bool          `mapstructure:"dynamodb_use_dax"`
	DynamoDBDaxUrl                      string        `mapstructure:"dynamodb_dax_url"`
	DynamoDBTable                       string        `mapstructure:"dynamodb_table"`
	DynamoDBTimeoutSeconds              uint          `mapstructure:"dynamodb_timeout_seconds"`
	DynamoDBActionRetries               uint          `mapstructure:"dynamodb_action_retries"`
	GatewayKey                          string        `mapstructure:"gateway_key"`
	ServiceDiscoveryTimeoutSeconds      uint          `mapstructure:"service_discovery_timeout_seconds"`
	HealthReportCleanUpFrequencySeconds uint          `mapstructure:"health_report_cleanup_frequency_seconds"`
	HealthReportRecordStaleMinutes      uint          `mapstructure:"health_report_record_stale_minutes"`
	HashKeys                            []HashKeyData `mapstructure:"hash_keys"`
	// RequireSNSSignature enforces AWS SNS signature verification on all
	// inbound SubscriptionConfirmation / Notification callbacks. Secure by
	// default — operators must explicitly set this to false to bypass.
	RequireSNSSignature bool `mapstructure:"require_sns_signature"`
	// EndpointRetryDelayMs is the delay in milliseconds between DDB endpoint
	// lookup retries during SNS subscription confirmation. Default: 250.
	EndpointRetryDelayMs uint `mapstructure:"endpoint_retry_delay_ms"`
	// EndpointRetryMaxAttempts is the maximum number of DDB endpoint lookup
	// attempts during SNS subscription confirmation. Default: 3.
	EndpointRetryMaxAttempts uint `mapstructure:"endpoint_retry_max_attempts"`
}

// FIX #7: Exported so other packages can construct HashKeyData values
// to pass to SetHashKeys.
type HashKeyData struct {
	HashKeyName   string `mapstructure:"hash_key_name"`
	HashKeySecret string `mapstructure:"hash_key_secret"`
}

// ensureGatewayData initializes NotifierGatewayData if nil.
// FIX #1: All setters call this to prevent nil-pointer panics when called before Read().
func (c *Config) ensureGatewayData() {
	if c.NotifierGatewayData == nil {
		c.NotifierGatewayData = &NotifierGatewayData{}
	}
}

func (c *Config) SetDynamoDBAwsRegion(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("AWS region cannot be empty")
	}

	// Validate it's a known AWS region
	if awsregion.GetAwsRegion(s) == awsregion.UNKNOWN {
		return fmt.Errorf("invalid AWS region provided")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.dynamodb_aws_region", s)
	c.NotifierGatewayData.DynamoDBAwsRegion = s
	return nil
}

func (c *Config) SetDynamoDBUseDax(b bool) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	// FIX #5: If disabling DAX, just set the flag.
	// If enabling DAX, validate that DaxUrl is already configured.
	if b {
		c.ensureGatewayData()
		if util.LenTrim(c.NotifierGatewayData.DynamoDBDaxUrl) == 0 {
			return fmt.Errorf("cannot enable DAX without a configured DynamoDB DAX URL; call SetDynamoDBDaxUrl first")
		}
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.dynamodb_use_dax", b)
	c.NotifierGatewayData.DynamoDBUseDax = b
	return nil
}

func (c *Config) SetDynamoDBDaxUrl(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	s = strings.TrimSpace(s)

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.dynamodb_dax_url", s)
	c.NotifierGatewayData.DynamoDBDaxUrl = s
	return nil
}

func (c *Config) SetDynamoDBTable(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("DynamoDB table name cannot be empty")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.dynamodb_table", s)
	c.NotifierGatewayData.DynamoDBTable = s
	return nil
}

func (c *Config) SetDynamoDBTimeoutSeconds(i uint) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	if i == 0 {
		return fmt.Errorf("DynamoDB timeout must be greater than 0")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.dynamodb_timeout_seconds", i)
	c.NotifierGatewayData.DynamoDBTimeoutSeconds = i
	return nil
}

func (c *Config) SetDynamoDBActionRetries(i uint) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.dynamodb_action_retries", i)
	c.NotifierGatewayData.DynamoDBActionRetries = i
	return nil
}

func (c *Config) SetGatewayKey(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	s = strings.TrimSpace(s)

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.gateway_key", s)
	c.NotifierGatewayData.GatewayKey = s
	return nil
}

func (c *Config) SetServiceDiscoveryTimeoutSeconds(i uint) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	if i == 0 {
		return fmt.Errorf("service discovery timeout must be greater than 0")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.service_discovery_timeout_seconds", i)
	c.NotifierGatewayData.ServiceDiscoveryTimeoutSeconds = i
	return nil
}

func (c *Config) SetHealthReportCleanUpFrequencySeconds(i uint) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.health_report_cleanup_frequency_seconds", i)
	c.NotifierGatewayData.HealthReportCleanUpFrequencySeconds = i
	return nil
}

func (c *Config) SetHealthReportRecordStaleMinutes(i uint) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.health_report_record_stale_minutes", i)
	c.NotifierGatewayData.HealthReportRecordStaleMinutes = i
	return nil
}

// SetRequireSNSSignature toggles the enforcement of AWS SNS signature
// verification on inbound SNS callbacks. Default is true (secure by default).
// Operators must explicitly call SetRequireSNSSignature(false) to bypass
// verification — typically only for local development or simulator testing.
func (c *Config) SetRequireSNSSignature(b bool) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.require_sns_signature", b)
	c.NotifierGatewayData.RequireSNSSignature = b
	return nil
}

// SetEndpointRetryDelayMs sets the delay in milliseconds between DDB
// endpoint lookup retries during SNS subscription confirmation.
func (c *Config) SetEndpointRetryDelayMs(i uint) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.endpoint_retry_delay_ms", i)
	c.NotifierGatewayData.EndpointRetryDelayMs = i
	return nil
}

// SetEndpointRetryMaxAttempts sets the maximum number of DDB endpoint
// lookup attempts during SNS subscription confirmation.
func (c *Config) SetEndpointRetryMaxAttempts(i uint) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.endpoint_retry_max_attempts", i)
	c.NotifierGatewayData.EndpointRetryMaxAttempts = i
	return nil
}

// FIX #4: Trim key names and secrets before storing so that lookups
// using trimmed names (e.g. model.GetHashKey("mykey")) match correctly.
func (c *Config) SetHashKeys(hk ...HashKeyData) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	// Validate and normalize hash keys
	normalized := make([]HashKeyData, 0, len(hk))
	for _, key := range hk {
		name := strings.TrimSpace(key.HashKeyName)
		secret := strings.TrimSpace(key.HashKeySecret)

		if name == "" {
			return fmt.Errorf("hash key name cannot be empty")
		}
		if secret == "" {
			return fmt.Errorf("hash key secret cannot be empty for key: %s", name)
		}

		normalized = append(normalized, HashKeyData{
			HashKeyName:   name,
			HashKeySecret: secret,
		})
	}

	c.ensureGatewayData()
	c._v.Set("notifier_gateway.hash_keys", normalized)
	c.NotifierGatewayData.HashKeys = normalized
	return nil
}

// Read will load config settings from disk.
// FIX #2: Builds into local variables first and only overwrites c._v and
// c.NotifierGatewayData on success, so a failed Read() doesn't destroy
// previously valid state.
// FIX #3: Validates required fields (DynamoDBAwsRegion, DynamoDBTable)
// after unmarshal so callers get a clear error instead of a confusing
// downstream AWS SDK failure.
func (c *Config) Read() error {
	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	configFileName := c.ConfigFileName
	if util.LenTrim(configFileName) == 0 {
		configFileName = "gateway-config"
	}

	v := &data.ViperConf{
		AppName:          c.AppName,
		ConfigName:       configFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML:            true,
		UseAutomaticEnvVar: false,
	}

	v.Default("notifier_gateway.dynamodb_aws_region", "").Default(
		"notifier_gateway.dynamodb_use_dax", false).Default(
		"notifier_gateway.dynamodb_dax_url", "").Default(
		"notifier_gateway.dynamodb_table", "").Default(
		"notifier_gateway.dynamodb_timeout_seconds", 5).Default(
		"notifier_gateway.dynamodb_action_retries", 3).Default(
		"notifier_gateway.gateway_key", "").Default(
		"notifier_gateway.service_discovery_timeout_seconds", 5)

	v.Default("notifier_gateway.health_report_cleanup_frequency_seconds", 120)
	v.Default("notifier_gateway.health_report_record_stale_minutes", 5)
	v.Default("notifier_gateway.hash_keys", []HashKeyData{})
	// Secure by default — SNS signature verification is enforced unless
	// operators explicitly disable it via config.
	v.Default("notifier_gateway.require_sns_signature", true)
	// BL-1: configurable DDB endpoint lookup retry parameters for SNS
	// subscription confirmation. Defaults match prior hardcoded behavior.
	v.Default("notifier_gateway.endpoint_retry_delay_ms", 250)
	v.Default("notifier_gateway.endpoint_retry_max_attempts", 3)

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

	// Unmarshal into a temporary Config to validate before committing
	tempCfg := &Config{}
	if err := v.Unmarshal(tempCfg); err != nil {
		return err
	}

	// FIX #3: Validate required fields after unmarshal
	if tempCfg.NotifierGatewayData == nil {
		return fmt.Errorf("notifier_gateway config section is missing")
	}

	if util.LenTrim(tempCfg.NotifierGatewayData.DynamoDBAwsRegion) == 0 {
		return fmt.Errorf("notifier_gateway.dynamodb_aws_region is required")
	}

	if awsregion.GetAwsRegion(tempCfg.NotifierGatewayData.DynamoDBAwsRegion) == awsregion.UNKNOWN {
		return fmt.Errorf("notifier_gateway.dynamodb_aws_region '%s' is not a valid AWS region", tempCfg.NotifierGatewayData.DynamoDBAwsRegion)
	}

	if util.LenTrim(tempCfg.NotifierGatewayData.DynamoDBTable) == 0 {
		return fmt.Errorf("notifier_gateway.dynamodb_table is required")
	}

	if tempCfg.NotifierGatewayData.DynamoDBUseDax && util.LenTrim(tempCfg.NotifierGatewayData.DynamoDBDaxUrl) == 0 {
		return fmt.Errorf("notifier_gateway.dynamodb_dax_url is required when dynamodb_use_dax is true")
	}

	// FIX #2: All validation passed — now commit to receiver
	c._v = v
	c.ConfigFileName = configFileName
	c.NotifierGatewayData = tempCfg.NotifierGatewayData
	return nil
}

// Save persists config settings to disk.
// FIX #6: Returns an error when _v is nil instead of silently succeeding.
func (c *Config) Save() error {
	if strings.ToLower(os.Getenv("CONFIG_READ_ONLY")) == "true" {
		return nil
	}
	if c._v == nil {
		return fmt.Errorf("cannot save: viper config not initialized (call Read first)")
	}
	return c._v.Save()
}
