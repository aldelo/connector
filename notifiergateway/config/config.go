package config

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
	AppName string								`mapstructure:"-"`
	ConfigFileName string						`mapstructure:"-"`
	CustomConfigPath string						`mapstructure:"-"`

	_v *data.ViperConf							`mapstructure:"-"`

	NotifierGatewayData *notifierGatewayData	`mapstructure:"notifier_gateway"`
}

type notifierGatewayData struct {
	DynamoDBAwsRegion string					`mapstructure:"dynamodb_aws_region"`
	DynamoDBUseDax bool							`mapstructure:"dynamodb_use_dax"`
	DynamoDBDaxUrl string						`mapstructure:"dynamodb_dax_url"`
	DynamoDBTable string						`mapstructure:"dynamodb_table"`
	DynamoDBTimeoutSeconds uint					`mapstructure:"dynamodb_timeout_seconds"`
	DynamoDBActionRetries uint					`mapstructure:"dynamodb_action_retries"`
	GatewayKey string							`mapstructure:"gateway_key"`
}

func (c *Config) SetDynamoDBAwsRegion(s string) {
	if c._v != nil {
		c._v.Set("notifier_gateway.dynamodb_aws_region", s)
		c.NotifierGatewayData.DynamoDBAwsRegion = s
	}
}

func (c *Config) SetDynamoDBUseDax(b bool) {
	if c._v != nil {
		c._v.Set("notifier_gateway.dynamodb_use_dax", b)
		c.NotifierGatewayData.DynamoDBUseDax = b
	}
}

func (c *Config) SetDynamoDBDaxUrl(s string) {
	if c._v != nil {
		c._v.Set("notifier_gateway.dynamodb_dax_url", s)
		c.NotifierGatewayData.DynamoDBDaxUrl = s
	}
}

func (c *Config) SetDynamoDBTable(s string) {
	if c._v != nil {
		c._v.Set("notifier_gateway.dynamodb_table", s)
		c.NotifierGatewayData.DynamoDBTable = s
	}
}

func (c *Config) SetDynamoDBTimeoutSeconds(i uint) {
	if c._v != nil {
		c._v.Set("notifier_gateway.dynamodb_timeout_seconds", i)
		c.NotifierGatewayData.DynamoDBTimeoutSeconds = i
	}
}

func (c *Config) SetDynamoDBActionRetries(i uint) {
	if c._v != nil {
		c._v.Set("notifier_gateway.dynamodb_action_retries", i)
		c.NotifierGatewayData.DynamoDBActionRetries = i
	}
}

func (c *Config) SetGatewayKey(s string) {
	if c._v != nil {
		c._v.Set("notifier_gateway.gateway_key", s)
		c.NotifierGatewayData.GatewayKey = s
	}
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
		AppName: c.AppName,
		ConfigName: c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML: true,
		UseAutomaticEnvVar: false,
	}

	c._v.Default("notifier_gateway.dynamodb_aws_region", "").Default(		// required, valid aws region such as us-east-1
		"notifier_gateway.dynamodb_use_dax", false).Default(				// optional, true = uses dax
		"notifier_gateway.dynamodb_dax_url", "").Default(					// conditional, required if use dax = true
		"notifier_gateway.dynamodb_table", "").Default(					// required, dynamodb table name
		"notifier_gateway.dynamodb_timeout_seconds", 5).Default(			// optional, dynamodb action timeout seconds
		"notifier_gateway.dynamodb_action_retries", 3).Default(
		"notifier_gateway.gateway_key", "")							// optional, gateway key is used to validate inbound request when such request is to be validated

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
