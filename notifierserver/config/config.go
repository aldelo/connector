package config

/*
 * Copyright 2020-2021 Aldelo, LP
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
	"strings"
)

type Config struct {
	AppName string								`mapstructure:"-"`
	ConfigFileName string						`mapstructure:"-"`
	CustomConfigPath string						`mapstructure:"-"`

	_v *data.ViperConf							`mapstructure:"-"`

	NotifierServerData *notifierServerData		`mapstructure:"notifier_server"`
	SubscriptionsData []*subscriptionsData 		`mapstructure:"subscriptions"`
}

type notifierServerData struct {
	GatewayUrl string							`mapstructure:"gateway_url"`
	ServerKey string							`mapstructure:"server_key"`
	DynamoDBAwsRegion string					`mapstructure:"dynamodb_aws_region"`
	DynamoDBUseDax bool							`mapstructure:"dynamodb_use_dax"`
	DynamoDBDaxUrl string						`mapstructure:"dynamodb_dax_url"`
	DynamoDBTable string						`mapstructure:"dynamodb_table"`
	DynamoDBTimeoutSeconds uint					`mapstructure:"dynamodb_timeout_seconds"`
	DynamoDBActionRetries uint					`mapstructure:"dynamodb_action_retries"`
	SnsAwsRegion string							`mapstructure:"sns_aws_region"`
}

type subscriptionsData struct {
	TopicArn string								`mapstructure:"topic_arn"`
	SubscriptionArn string						`mapstructure:"subscription_arn"`
}

func (c *Config) GetSubscriptionArn(topicArn string) string {
	for _, v := range c.SubscriptionsData{
		if strings.ToLower(v.TopicArn) == strings.ToLower(topicArn) {
			// match
			return v.SubscriptionArn
		}
	}

	// no match
	return ""
}

func (c *Config) SetSubscriptionData(topicArn string, subscriptionArn string) {
	if c._v != nil {
		if util.LenTrim(topicArn) == 0 {
			return
		}

		found := false

		for _, p := range c.SubscriptionsData{
			if strings.ToLower(p.TopicArn) == strings.ToLower(topicArn) {
				// match
				p.SubscriptionArn = subscriptionArn
				found = true
				break
			}
		}

		if !found {
			c.SubscriptionsData = append(c.SubscriptionsData, &subscriptionsData{
				TopicArn: topicArn,
				SubscriptionArn: subscriptionArn,
			})
		}

		c._v.Set("subscriptions", c.SubscriptionsData)
	}
}

// topicArn = blank to remove all subscription data from config
func (c *Config) RemoveSubscriptionData(topicArn string) {
	if c._v != nil {
		if util.LenTrim(topicArn) == 0 {
			// remove all
			c.SubscriptionsData = []*subscriptionsData{}
			c._v.Set("subscriptions", []*subscriptionsData{})
		} else {
			// remove specific topic arn
			var temp []*subscriptionsData
			splitIndex := -1

			for i, p := range c.SubscriptionsData{
				if strings.ToLower(p.TopicArn) == strings.ToLower(topicArn) {
					// match
					splitIndex = i
					break
				}
			}

			if splitIndex >= 0 {
				tempIFace := util.SliceDeleteElement(c.SubscriptionsData, splitIndex)

				if tempIFace != nil {
					ok := false
					if temp, ok = tempIFace.([]*subscriptionsData); ok {
						c.SubscriptionsData = temp
						c._v.Set("subscriptions", temp)
					}
				}
			}
		}
	}
}

func (c *Config) SetNotifierGatewayUrl(s string) {
	if c._v != nil {
		c._v.Set("notifier_server.gateway_url", s)
		c.NotifierServerData.GatewayUrl = s
	}
}

func (c *Config) SetServerKey(s string) {
	if c._v != nil {
		c._v.Set("notifier_server.server_key", s)
		c.NotifierServerData.ServerKey = s
	}
}

func (c *Config) SetDynamoDBAwsRegion(s string) {
	if c._v != nil {
		c._v.Set("notifier_server.dynamodb_aws_region", s)
		c.NotifierServerData.DynamoDBAwsRegion = s
	}
}

func (c *Config) SetDynamoDBUseDax(b bool) {
	if c._v != nil {
		c._v.Set("notifier_server.dynamodb_use_dax", b)
		c.NotifierServerData.DynamoDBUseDax = b
	}
}

func (c *Config) SetDynamoDBDaxUrl(s string) {
	if c._v != nil {
		c._v.Set("notifier_server.dynamodb_dax_url", s)
		c.NotifierServerData.DynamoDBDaxUrl = s
	}
}

func (c *Config) SetDynamoDBTable(s string) {
	if c._v != nil {
		c._v.Set("notifier_server.dynamodb_table", s)
		c.NotifierServerData.DynamoDBTable = s
	}
}

func (c *Config) SetDynamoDBTimeoutSeconds(i uint) {
	if c._v != nil {
		c._v.Set("notifier_server.dynamodb_timeout_seconds", i)
		c.NotifierServerData.DynamoDBTimeoutSeconds = i
	}
}

func (c *Config) SetDynamoDBActionRetries(i uint) {
	if c._v != nil {
		c._v.Set("notifier_server.dynamodb_action_retries", i)
		c.NotifierServerData.DynamoDBActionRetries = i
	}
}

func (c *Config) SetSnsAwsRegion(s string) {
	if c._v != nil {
		c._v.Set("notifier_server.sns_aws_region", s)
		c.NotifierServerData.SnsAwsRegion = s
	}
}

// Read will load config settings from disk
func (c *Config) Read() error {
	c._v = nil
	c.NotifierServerData = &notifierServerData{}
	c.SubscriptionsData = []*subscriptionsData{}

	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	if util.LenTrim(c.ConfigFileName) == 0 {
		c.ConfigFileName = "notifier-config"
	}

	c._v = &data.ViperConf{
		AppName: c.AppName,
		ConfigName: c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML: true,
		UseAutomaticEnvVar: false,
	}

	c._v.Default("notifier_server.gateway_url", "").Default(			// required, notifier gateway url to be called back by sns, must begin with http or https
	"notifier_server.server_key", "").Default(						// auto-created, notifier server key auto created upon launch
	"notifier_server.dynamodb_aws_region", "").Default(				// required, valid aws region such as us-east-1
	"notifier_server.dynamodb_use_dax", false).Default(				// optional, true = uses dax
	"notifier_server.dynamodb_dax_url", "").Default(					// conditional, required if use dax = true
	"notifier_server.dynamodb_table", "").Default(					// required, dynamodb table name
	"notifier_server.dynamodb_timeout_seconds", 5).Default(			// optional, dynamodb action timeout seconds
	"notifier_server.dynamodb_action_retries", 3).Default(			// optional, dynamodb action retries count
	"notifier_server.sns_aws_region", "")								// required, valid aws region such as us-east-1

	c._v.Default("subscriptions", []*subscriptionsData{}) 					// required, slice of subscriptions (topic_arn, subscription_arn)

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
