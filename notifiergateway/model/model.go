package model

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
	"sync"
	"time"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/dynamodb"
	"github.com/aldelo/connector/notifiergateway/config"
	"github.com/aws/aws-sdk-go/aws"
	ddb "github.com/aws/aws-sdk-go/service/dynamodb"
)

var (
	configMu sync.RWMutex

	dynamoDBActionRetryAttempts         uint
	serviceDiscoveryTimeoutSeconds      uint
	gatewayKey                          string
	healthReportCleanUpFrequencySeconds uint
	healthReportRecordStaleMinutes      uint
	hashKeys                            map[string]string
)

// GetDynamoDBActionRetryAttempts returns the configured retry attempts value
func GetDynamoDBActionRetryAttempts() uint {
	configMu.RLock()
	defer configMu.RUnlock()
	return dynamoDBActionRetryAttempts
}

// SetDynamoDBActionRetryAttempts sets the retry attempts value
func SetDynamoDBActionRetryAttempts(v uint) {
	configMu.Lock()
	defer configMu.Unlock()
	dynamoDBActionRetryAttempts = v
}

// GetServiceDiscoveryTimeoutSeconds returns the configured timeout value
func GetServiceDiscoveryTimeoutSeconds() uint {
	configMu.RLock()
	defer configMu.RUnlock()
	return serviceDiscoveryTimeoutSeconds
}

// SetServiceDiscoveryTimeoutSeconds sets the timeout value
func SetServiceDiscoveryTimeoutSeconds(v uint) {
	configMu.Lock()
	defer configMu.Unlock()
	serviceDiscoveryTimeoutSeconds = v
}

// GetGatewayKey returns the configured gateway key
func GetGatewayKey() string {
	configMu.RLock()
	defer configMu.RUnlock()
	return gatewayKey
}

// SetGatewayKey sets the gateway key
func SetGatewayKey(v string) {
	configMu.Lock()
	defer configMu.Unlock()
	gatewayKey = v
}

// GetHealthReportCleanUpFrequencySeconds returns the configured cleanup frequency
func GetHealthReportCleanUpFrequencySeconds() uint {
	configMu.RLock()
	defer configMu.RUnlock()
	return healthReportCleanUpFrequencySeconds
}

// SetHealthReportCleanUpFrequencySeconds sets the cleanup frequency
func SetHealthReportCleanUpFrequencySeconds(v uint) {
	configMu.Lock()
	defer configMu.Unlock()
	healthReportCleanUpFrequencySeconds = v
}

// GetHealthReportRecordStaleMinutes returns the configured stale minutes value
func GetHealthReportRecordStaleMinutes() uint {
	configMu.RLock()
	defer configMu.RUnlock()
	return healthReportRecordStaleMinutes
}

// SetHealthReportRecordStaleMinutes sets the stale minutes value
func SetHealthReportRecordStaleMinutes(v uint) {
	configMu.Lock()
	defer configMu.Unlock()
	healthReportRecordStaleMinutes = v
}

// GetHashKeys returns a copy of the hash keys map
func GetHashKeys() map[string]string {
	configMu.RLock()
	defer configMu.RUnlock()
	// Return a copy to prevent external modification
	result := make(map[string]string)
	for k, v := range hashKeys {
		result[k] = v
	}
	return result
}

// SetHashKeys sets the hash keys map
func SetHashKeys(v map[string]string) {
	configMu.Lock()
	defer configMu.Unlock()
	hashKeys = v
}

// Deprecated: Use GetDynamoDBActionRetryAttempts instead
var DynamoDBActionRetryAttempts uint

// Deprecated: Use GetServiceDiscoveryTimeoutSeconds instead
var ServiceDiscoveryTimeoutSeconds uint

// Deprecated: Use GetGatewayKey instead
var GatewayKey string

// Deprecated: Use GetHealthReportCleanUpFrequencySeconds instead
var HealthReportCleanUpFrequencySeconds uint

// Deprecated: Use GetHealthReportRecordStaleMinutes instead
var HealthReportRecordStaleMinutes uint

// Deprecated: Use GetHashKeys/SetHashKeys instead
var HashKeys map[string]string

type serverRoute struct {
	PK            string `json:"pk" dynamodbav:"PK"`
	SK            string `json:"sk" dynamodbav:"SK"`
	ServerKey     string `json:"serverkey" dynamodbav:"ServerKey"`
	HostInfo      string `json:"hostinfo" dynamodbav:"HostInfo"`
	LastTimestamp int64  `json:"lasttimestamp" dynamodbav:"LastTimestamp"`
}

type healthStatus struct {
	PK             string `json:"pk" dynamodbav:"PK"`
	SK             string `json:"sk" dynamodbav:"SK"`
	NamespaceId    string `json:"namespaceid" dynamodbav:"NamespaceId"`
	ServiceId      string `json:"serviceid" dynamodbav:"ServiceId"`
	InstanceId     string `json:"instanceid" dynamodbav:"InstanceId"`
	AwsRegion      string `json:"awsregion" dynamodbav:"AWSRegion"`
	ServiceInfo    string `json:"serviceinfo" dynamodbav:"ServiceInfo"`
	HostInfo       string `json:"hostinfo" dynamodbav:"HostInfo"`
	LastTimestamp  int64  `json:"lasttimestamp" dynamodbav:"LastTimestamp"`
	LastUpdatedUTC string `json:"lastupdatedutc" dynamodbav:"LastUpdatedUTC"`
}

var _ddbStore *dynamodb.DynamoDB
var _appName string
var _ddbTimeoutSeconds uint
var _ddbActionRetries uint

func ConnectDataStore(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("Config Object is Required")
	}

	_ddbStore = &dynamodb.DynamoDB{
		AwsRegion:   awsregion.GetAwsRegion(cfg.NotifierGatewayData.DynamoDBAwsRegion),
		SkipDax:     !cfg.NotifierGatewayData.DynamoDBUseDax,
		DaxEndpoint: cfg.NotifierGatewayData.DynamoDBDaxUrl,
		TableName:   cfg.NotifierGatewayData.DynamoDBTable,
		PKName:      "PK",
		SKName:      "SK",
	}

	if err := _ddbStore.Connect(); err != nil {
		return err
	} else {
		if cfg.NotifierGatewayData.DynamoDBUseDax {
			if err = _ddbStore.EnableDax(); err != nil {
				return err
			}
		}

		_appName = cfg.AppName
		_ddbTimeoutSeconds = cfg.NotifierGatewayData.DynamoDBTimeoutSeconds
		_ddbActionRetries = cfg.NotifierGatewayData.DynamoDBActionRetries

		SetDynamoDBActionRetryAttempts(cfg.NotifierGatewayData.DynamoDBActionRetries)
		SetHealthReportCleanUpFrequencySeconds(cfg.NotifierGatewayData.HealthReportCleanUpFrequencySeconds)
		SetHealthReportRecordStaleMinutes(cfg.NotifierGatewayData.HealthReportRecordStaleMinutes)
		SetServiceDiscoveryTimeoutSeconds(cfg.NotifierGatewayData.ServiceDiscoveryTimeoutSeconds)

		// Update deprecated variables for backward compatibility (protected by same mutex)
		configMu.Lock()
		DynamoDBActionRetryAttempts = dynamoDBActionRetryAttempts
		HealthReportCleanUpFrequencySeconds = healthReportCleanUpFrequencySeconds
		HealthReportRecordStaleMinutes = healthReportRecordStaleMinutes
		ServiceDiscoveryTimeoutSeconds = serviceDiscoveryTimeoutSeconds
		configMu.Unlock()

		return nil
	}
}

func GetServerRouteFromDataStore(serverKey string) (serverUrl string, err error) {
	if _ddbStore == nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: Server Key is Required")
	}

	pk := fmt.Sprintf("%s#%s#service#discovery#host#target", "corems", "notifier-server")
	sk := fmt.Sprintf("ServerKey^%s", serverKey)

	routeInfo := new(serverRoute)

	if e := _ddbStore.GetItemWithRetry(_ddbActionRetries, routeInfo, pk, sk, _ddbStore.TimeOutDuration(_ddbTimeoutSeconds), nil); e != nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: %s", e)
	} else {
		return routeInfo.HostInfo, nil
	}
}

func DeleteServerRouteFromDataStore(serverKey string) error {
	if _ddbStore == nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: Server Key is Required")
	}

	pk := fmt.Sprintf("%s#%s#service#discovery#host#target", "corems", "notifier-server")
	sk := fmt.Sprintf("ServerKey^%s", serverKey)

	if err := _ddbStore.DeleteItemWithRetry(_ddbActionRetries, pk, sk, _ddbStore.TimeOutDuration(_ddbTimeoutSeconds)); err != nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: %s", err)
	} else {
		return nil
	}
}

func GetInstanceHealthFromDataStore(instanceId string) (lastHealthy string, err error) {
	if _ddbStore == nil {
		return "", fmt.Errorf("Get Instance Health From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(instanceId) == 0 {
		return "", fmt.Errorf("Get Instance Health From Data Store Failed: InstanceId is Required")
	}

	pk := fmt.Sprintf("%s#%s#service#discovery#host#health", "corems", "all")
	sk := fmt.Sprintf("InstanceID^%s", instanceId)

	statusInfo := new(healthStatus)

	if e := _ddbStore.GetItemWithRetry(_ddbActionRetries, statusInfo, pk, sk, _ddbStore.TimeOutDuration(_ddbTimeoutSeconds), nil); e != nil {
		// error
		return "", fmt.Errorf("Get Health Status From Data Store Failed: %s", e)
	} else {
		// no error
		return util.FormatDateTime(time.Unix(statusInfo.LastTimestamp, 0)), nil
	}
}

func SetInstanceHealthToDataStore(namespaceId string, serviceId string, instanceId string, awsRegion string, serviceInfo string, hostInfo string) (err error) {
	if _ddbStore == nil {
		return fmt.Errorf("Set Instance Health To Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(namespaceId) == 0 {
		return fmt.Errorf("Set Instance Health To Data Store Failed: NamespaceId is Required")
	}

	if util.LenTrim(serviceId) == 0 {
		return fmt.Errorf("Set Instance Health To Data Store Failed: ServiceId is Required")
	}

	if util.LenTrim(instanceId) == 0 {
		return fmt.Errorf("Set Instance Health To Data Store Failed: InstanceId is Required")
	}

	if util.LenTrim(awsRegion) == 0 {
		return fmt.Errorf("Set Instance Health To Data Store Failed: AWSRegion is Required")
	}

	if util.LenTrim(serviceInfo) == 0 {
		return fmt.Errorf("Set Instance Health To Data Store Failed: ServiceInfo is Required")
	}

	if util.LenTrim(hostInfo) == 0 {
		return fmt.Errorf("Set Instance Health To Data Store Failed: HostInfo is Required")
	}

	pk := fmt.Sprintf("%s#%s#service#discovery#host#health", "corems", "all")
	sk := fmt.Sprintf("InstanceID^%s", instanceId)
	timeNowUTC := time.Now().UTC()

	statusInfo := &healthStatus{
		PK:             pk,
		SK:             sk,
		NamespaceId:    namespaceId,
		ServiceId:      serviceId,
		InstanceId:     instanceId,
		AwsRegion:      awsRegion,
		ServiceInfo:    serviceInfo,
		HostInfo:       hostInfo,
		LastTimestamp:  timeNowUTC.Unix(),
		LastUpdatedUTC: util.FormatDateTime(timeNowUTC),
	}

	if e := _ddbStore.PutItemWithRetry(_ddbActionRetries, statusInfo, _ddbStore.TimeOutDuration(_ddbTimeoutSeconds)); e != nil {
		// error
		return fmt.Errorf("Set Instance Health To Data Store Failed: %s", e)
	} else {
		// success
		return nil
	}
}

func DeleteInstanceHealthFromDataStore(instanceKeys ...*dynamodb.DynamoDBTableKeyValue) (deleteFailKeys []*dynamodb.DynamoDBTableKeyValue, err error) {
	if _ddbStore == nil {
		return []*dynamodb.DynamoDBTableKeyValue{}, fmt.Errorf("Delete Instance Health From Data Store Failed: DynamoDB Connection Not Established")
	}

	if len(instanceKeys) == 0 {
		return []*dynamodb.DynamoDBTableKeyValue{}, fmt.Errorf("Delete Instance Health From Data Store Failed: %s", "InstanceKeys To Delete is Required")
	}

	if deleteFailKeys, err = _ddbStore.BatchDeleteItemsWithRetry(_ddbActionRetries, _ddbStore.TimeOutDuration(_ddbTimeoutSeconds), instanceKeys...); err != nil {
		// all or some delete failures
		if len(deleteFailKeys) == len(instanceKeys) {
			// all failed
			return deleteFailKeys, err
		} else {
			// some failed
			return deleteFailKeys, nil
		}
	} else {
		// delete all success
		return []*dynamodb.DynamoDBTableKeyValue{}, nil
	}
}

func ListInactiveInstancesFromDataStore() (inactiveInstances []*healthStatus, err error) {
	if _ddbStore == nil {
		return []*healthStatus{}, fmt.Errorf("List Inactive Instances From Data Store Failed: %s", err)
	}

	pk := fmt.Sprintf("%s#%s#service#discovery#host#health", "corems", "all")

	staleMinutes := GetHealthReportRecordStaleMinutes()

	if staleMinutes == 0 {
		staleMinutes = 5
	} else if staleMinutes < 3 {
		staleMinutes = 3
	} else if staleMinutes > 15 {
		staleMinutes = 15
	}

	ts := util.Int64ToString(time.Now().UTC().Add(time.Duration(staleMinutes) * time.Minute * -1).Unix())

	if itemsList, e := _ddbStore.QueryPagedItemsWithRetry(_ddbActionRetries, &[]*healthStatus{}, &[]*healthStatus{},
		_ddbStore.TimeOutDuration(_ddbTimeoutSeconds),
		"LSI-LastTimestamp",
		"PK=:pk AND LastTimestamp<:lasttimestamp",
		map[string]*ddb.AttributeValue{
			":pk": {
				S: aws.String(pk),
			},
			":lasttimestamp": {
				N: aws.String(ts),
			},
		}, nil); e != nil {
		// error
		return []*healthStatus{}, fmt.Errorf("List Inactive Instances From Data Store Failed: %s", e)
	} else {
		// success
		ok := false

		if inactiveInstances, ok = itemsList.([]*healthStatus); !ok {
			return []*healthStatus{}, nil
		} else {
			return inactiveInstances, nil
		}
	}
}
