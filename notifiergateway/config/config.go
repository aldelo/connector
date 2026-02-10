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

	NotifierGatewayData *notifierGatewayData `mapstructure:"notifier_gateway"`
}

type notifierGatewayData struct {
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
	HashKeys                            []hashKeyData `mapstructure:"hash_keys"`
}

type hashKeyData struct {
	HashKeyName   string `mapstructure:"hash_key_name"`
	HashKeySecret string `mapstructure:"hash_key_secret"`
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
	// DAX URL can be empty if not using DAX, but if provided should be validated

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

	// Action retries can be 0, meaning no retries

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
	// Gateway key can be empty if not using authentication

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

	// Frequency can be 0, will use default value

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

	// Stale minutes can be 0, will use default value

	c._v.Set("notifier_gateway.health_report_record_stale_minutes", i)
	c.NotifierGatewayData.HealthReportRecordStaleMinutes = i
	return nil
}

func (c *Config) SetHashKeys(hk ...hashKeyData) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	// Validate hash keys
	for _, key := range hk {
		if strings.TrimSpace(key.HashKeyName) == "" {
			return fmt.Errorf("hash key name cannot be empty")
		}
		if strings.TrimSpace(key.HashKeySecret) == "" {
			return fmt.Errorf("hash key secret cannot be empty for key: %s", key.HashKeyName)
		}
	}

	c._v.Set("notifier_gateway.hash_keys", hk)
	c.NotifierGatewayData.HashKeys = hk
	return nil
}

// Read will load config settings from disk
func (c *Config) Read() error {
	c._v = nil
	c.NotifierGatewayData = &notifierGatewayData{}

	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	if util.LenTrim(c.ConfigFileName) == 0 {
		c.ConfigFileName = "gateway-config"
	}

	c._v = &data.ViperConf{
		AppName:          c.AppName,
		ConfigName:       c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML:            true,
		UseAutomaticEnvVar: false,
	}

	c._v.Default("notifier_gateway.dynamodb_aws_region", "").Default( // required, valid aws region such as us-east-1
		"notifier_gateway.dynamodb_use_dax", false).Default( // optional, true = uses dax
		"notifier_gateway.dynamodb_dax_url", "").Default( // conditional, required if use dax = true
		"notifier_gateway.dynamodb_table", "").Default( // required, dynamodb table name
		"notifier_gateway.dynamodb_timeout_seconds", 5).Default( // optional, dynamodb action timeout seconds
		"notifier_gateway.dynamodb_action_retries", 3).Default( // optional, dynamodb actions retry count
		"notifier_gateway.gateway_key", "").Default( // optional, gateway key is used to validate inbound request when such request is to be validated
		"notifier_gateway.service_discovery_timeout_seconds", 5) // optional, service discovery actions timeout seconds, defaults to 5 seconds

	// optional, health report service record clean up frequency seconds, default is 120, minmimum is 30, maximum is 3600 (1 hour), 0 = 120
	c._v.Default("notifier_gateway.health_report_cleanup_frequency_seconds", 120)

	// optional, health report service record stale minutes, total minutes before record is considered stale and primed for clean up removal
	// minimum = 3 minutes, default = 5 minutes, maximum = 15 minutes, 0 = 5 minutes
	c._v.Default("notifier_gateway.health_report_record_stale_minutes", 5)

	// optional, sets up default hash keys slice
	c._v.Default("notifier_gateway.hash_keys", []hashKeyData{})

	if ok, err := c._v.Init(); err != nil {
		return err
	} else {
		if !ok {
			if e := c._v.Save(); e != nil {
				return fmt.Errorf("Create Config File Failed: %w", e)
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
	if strings.ToLower(os.Getenv("CONFIG_READ_ONLY")) == "true" {
		return nil
	}
	if c._v != nil {
		return c._v.Save()
	} else {
		return nil
	}
}
