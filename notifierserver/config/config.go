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
	"sync"

	util "github.com/aldelo/common"
	data "github.com/aldelo/common/wrapper/viper"
)

type Config struct {
	AppName          string `mapstructure:"-"`
	ConfigFileName   string `mapstructure:"-"`
	CustomConfigPath string `mapstructure:"-"`

	_v  *data.ViperConf `mapstructure:"-"`
	_mu sync.RWMutex    `mapstructure:"-"`

	// FIX #1: Exported types so other packages can declare variables of these types
	NotifierServerData *NotifierServerData  `mapstructure:"notifier_server"`
	SubscriptionsData  []*SubscriptionsData `mapstructure:"subscriptions"`
}

// FIX #1: Exported so other packages can reference this type directly
type NotifierServerData struct {
	GatewayUrl             string `mapstructure:"gateway_url"`
	ServerKey              string `mapstructure:"server_key"`
	DynamoDBAwsRegion      string `mapstructure:"dynamodb_aws_region"`
	DynamoDBUseDax         bool   `mapstructure:"dynamodb_use_dax"`
	DynamoDBDaxUrl         string `mapstructure:"dynamodb_dax_url"`
	DynamoDBTable          string `mapstructure:"dynamodb_table"`
	DynamoDBTimeoutSeconds uint   `mapstructure:"dynamodb_timeout_seconds"`
	DynamoDBActionRetries  uint   `mapstructure:"dynamodb_action_retries"`
	SnsAwsRegion           string `mapstructure:"sns_aws_region"`
}

// FIX #1: Exported so other packages can reference this type directly
type SubscriptionsData struct {
	TopicArn        string `mapstructure:"topic_arn"`
	SubscriptionArn string `mapstructure:"subscription_arn"`
}

// ensureServerData initializes NotifierServerData if nil.
// FIX #2: Prevents nil-pointer panics when setters are called before Read().
func (c *Config) ensureServerData() {
	if c.NotifierServerData == nil {
		c.NotifierServerData = &NotifierServerData{}
	}
}

// FIX #3: GetSubscriptionArn — added nil receiver check and nil element check
// to prevent panic on bare Config{} or if SubscriptionsData contains nil entries.
// GetAllSubscriptionArns returns a snapshot of all subscription data under the lock.
func (c *Config) GetAllSubscriptionArns() []SubscriptionsData {
	if c == nil {
		return nil
	}

	c._mu.RLock()
	defer c._mu.RUnlock()

	result := make([]SubscriptionsData, 0, len(c.SubscriptionsData))
	for _, v := range c.SubscriptionsData {
		if v != nil {
			result = append(result, *v)
		}
	}
	return result
}

func (c *Config) GetSubscriptionArn(topicArn string) string {
	if c == nil {
		return ""
	}

	c._mu.RLock()
	defer c._mu.RUnlock()

	for _, v := range c.SubscriptionsData {
		if v != nil && strings.EqualFold(v.TopicArn, topicArn) {
			return v.SubscriptionArn
		}
	}

	return ""
}

// FIX #4: SetSubscriptionData — added nil receiver check, nil element check in loop,
// and ensureServerData guard. Uses strings.EqualFold for consistency.
func (c *Config) SetSubscriptionData(topicArn string, subscriptionArn string) {
	if c == nil || c._v == nil {
		return
	}

	if util.LenTrim(topicArn) == 0 {
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	found := false

	for _, p := range c.SubscriptionsData {
		if p != nil && strings.EqualFold(p.TopicArn, topicArn) {
			p.SubscriptionArn = subscriptionArn
			found = true
			break
		}
	}

	if !found {
		c.SubscriptionsData = append(c.SubscriptionsData, &SubscriptionsData{
			TopicArn:        topicArn,
			SubscriptionArn: subscriptionArn,
		})
	}

	c._v.Set("subscriptions", c.SubscriptionsData)
}

// RemoveSubscriptionData removes subscription data from config.
// hint: topicArn = blank to remove all subscription data from config.
// FIX #5: Added nil receiver check, nil element check in loop,
// and uses strings.EqualFold for consistency.
func (c *Config) RemoveSubscriptionData(topicArn string) {
	if c == nil || c._v == nil {
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	if util.LenTrim(topicArn) == 0 {
		// remove all
		c.SubscriptionsData = []*SubscriptionsData{}
		c._v.Set("subscriptions", []*SubscriptionsData{})
	} else {
		// remove specific topic arn
		splitIndex := -1

		for i, p := range c.SubscriptionsData {
			if p != nil && strings.EqualFold(p.TopicArn, topicArn) {
				splitIndex = i
				break
			}
		}

		if splitIndex >= 0 {
			tempIFace := util.SliceDeleteElement(c.SubscriptionsData, splitIndex)

			if tempIFace != nil {
				if temp, ok := tempIFace.([]*SubscriptionsData); ok {
					c.SubscriptionsData = temp
					c._v.Set("subscriptions", temp)
				}
			}
		}
	}
}

// FIX #2: All setters now call ensureServerData() to prevent nil-pointer panics
// when called before Read().
// FIX #6: All setters now return error instead of silently doing nothing,
// so callers know when the operation was skipped.

func (c *Config) SetNotifierGatewayUrl(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureServerData()
	c._v.Set("notifier_server.gateway_url", s)
	c.NotifierServerData.GatewayUrl = s
	return nil
}

func (c *Config) SetServerKey(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureServerData()
	c._v.Set("notifier_server.server_key", s)
	c.NotifierServerData.ServerKey = s
	return nil
}

func (c *Config) SetDynamoDBAwsRegion(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureServerData()
	c._v.Set("notifier_server.dynamodb_aws_region", s)
	c.NotifierServerData.DynamoDBAwsRegion = s
	return nil
}

func (c *Config) SetDynamoDBUseDax(b bool) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureServerData()
	c._v.Set("notifier_server.dynamodb_use_dax", b)
	c.NotifierServerData.DynamoDBUseDax = b
	return nil
}

func (c *Config) SetDynamoDBDaxUrl(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureServerData()
	c._v.Set("notifier_server.dynamodb_dax_url", s)
	c.NotifierServerData.DynamoDBDaxUrl = s
	return nil
}

func (c *Config) SetDynamoDBTable(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureServerData()
	c._v.Set("notifier_server.dynamodb_table", s)
	c.NotifierServerData.DynamoDBTable = s
	return nil
}

func (c *Config) SetDynamoDBTimeoutSeconds(i uint) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureServerData()
	c._v.Set("notifier_server.dynamodb_timeout_seconds", i)
	c.NotifierServerData.DynamoDBTimeoutSeconds = i
	return nil
}

func (c *Config) SetDynamoDBActionRetries(i uint) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureServerData()
	c._v.Set("notifier_server.dynamodb_action_retries", i)
	c.NotifierServerData.DynamoDBActionRetries = i
	return nil
}

func (c *Config) SetSnsAwsRegion(s string) error {
	if c == nil {
		return fmt.Errorf("config receiver is nil")
	}
	if c._v == nil {
		return fmt.Errorf("viper config not initialized")
	}

	c.ensureServerData()
	c._v.Set("notifier_server.sns_aws_region", s)
	c.NotifierServerData.SnsAwsRegion = s
	return nil
}

// Read will load config settings from disk.
// FIX #7: Builds into local variables first and only overwrites c._v,
// c.NotifierServerData, and c.SubscriptionsData on success, so a failed
// Read() doesn't destroy previously valid state.
func (c *Config) Read() error {
	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	configFileName := c.ConfigFileName
	if util.LenTrim(configFileName) == 0 {
		configFileName = "notifier-config"
	}

	v := &data.ViperConf{
		AppName:          c.AppName,
		ConfigName:       configFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML:            true,
		UseAutomaticEnvVar: false,
	}

	v.Default("notifier_server.gateway_url", "").Default(
		"notifier_server.server_key", "").Default(
		"notifier_server.dynamodb_aws_region", "").Default(
		"notifier_server.dynamodb_use_dax", false).Default(
		"notifier_server.dynamodb_dax_url", "").Default(
		"notifier_server.dynamodb_table", "").Default(
		"notifier_server.dynamodb_timeout_seconds", 5).Default(
		"notifier_server.dynamodb_action_retries", 3).Default(
		"notifier_server.sns_aws_region", "")

	v.Default("subscriptions", []*SubscriptionsData{})

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

	// All succeeded — commit to receiver
	c._v = v
	c.ConfigFileName = configFileName
	c.NotifierServerData = tempCfg.NotifierServerData
	c.SubscriptionsData = tempCfg.SubscriptionsData

	// Ensure NotifierServerData is never nil after Read
	c.ensureServerData()

	return nil
}

// Save persists config settings to disk.
// FIX #8: Returns an error when _v is nil instead of silently succeeding.
func (c *Config) Save() error {
	if strings.ToLower(os.Getenv("CONFIG_READ_ONLY")) == "true" {
		return nil
	}
	if c._v == nil {
		return fmt.Errorf("cannot save: viper config not initialized (call Read first)")
	}
	return c._v.Save()
}
