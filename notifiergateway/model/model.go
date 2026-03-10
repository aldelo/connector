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
	result := make(map[string]string, len(hashKeys))
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

// GetHashKeysCount returns the number of hash keys configured
func GetHashKeysCount() int {
	configMu.RLock()
	defer configMu.RUnlock()
	return len(hashKeys)
}

// GetHashKey returns the hash secret for a given key name, and whether it was found
func GetHashKey(name string) (secret string, found bool) {
	configMu.RLock()
	defer configMu.RUnlock()
	secret, found = hashKeys[name]
	return
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

// HealthStatus represents a service instance health record in DynamoDB
type HealthStatus struct {
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

// DynamoDB key pattern constants
const (
	pkPrefix       = "corems"
	pkService      = "notifier-server"
	pkHealthPrefix = "all"

	pkPattern       = "%s#%s#service#discovery#host#target" // → "corems#notifier-server#service#discovery#host#target"
	skPattern       = "ServerKey^%s"                        // → "ServerKey^{serverKey}"
	pkHealthPattern = "%s#%s#service#discovery#host#health" // → "corems#all#service#discovery#host#health"
	skHealthPattern = "InstanceID^%s"                       // → "InstanceID^{instanceId}"
)

// BuildHealthPK returns the fully-formed DynamoDB partition key for health status records.
// Exported so that gateway code uses the same key construction as model internals.
func BuildHealthPK() string {
	return fmt.Sprintf(pkHealthPattern, pkPrefix, pkHealthPrefix)
}

// BuildHealthSK returns the fully-formed DynamoDB sort key for a given instanceId.
// Exported so that gateway code uses the same key construction as model internals.
func BuildHealthSK(instanceId string) string {
	return fmt.Sprintf(skHealthPattern, instanceId)
}

var _ddbStore *dynamodb.DynamoDB
var _appName string
var _ddbTimeoutSeconds uint
var _ddbActionRetries uint

func ConnectDataStore(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("Config Object is Required")
	}

	// Build into a local variable first so _ddbStore is never non-nil but unconnected.
	// Other functions check _ddbStore == nil as their guard, so assigning before Connect()
	// would let them pass the guard and use a broken connection.
	store := &dynamodb.DynamoDB{
		AwsRegion:   awsregion.GetAwsRegion(cfg.NotifierGatewayData.DynamoDBAwsRegion),
		SkipDax:     !cfg.NotifierGatewayData.DynamoDBUseDax,
		DaxEndpoint: cfg.NotifierGatewayData.DynamoDBDaxUrl,
		TableName:   cfg.NotifierGatewayData.DynamoDBTable,
		PKName:      "PK",
		SKName:      "SK",
	}

	if err := store.Connect(); err != nil {
		return err
	}

	if cfg.NotifierGatewayData.DynamoDBUseDax {
		if err := store.EnableDax(); err != nil {
			return err
		}
	}

	// Connection fully established — now publish to package-level state
	_ddbStore = store
	_appName = cfg.AppName
	_ddbTimeoutSeconds = cfg.NotifierGatewayData.DynamoDBTimeoutSeconds
	_ddbActionRetries = cfg.NotifierGatewayData.DynamoDBActionRetries

	// Set all config values and deprecated variables atomically under a single lock
	configMu.Lock()
	dynamoDBActionRetryAttempts = cfg.NotifierGatewayData.DynamoDBActionRetries
	healthReportCleanUpFrequencySeconds = cfg.NotifierGatewayData.HealthReportCleanUpFrequencySeconds
	healthReportRecordStaleMinutes = cfg.NotifierGatewayData.HealthReportRecordStaleMinutes
	serviceDiscoveryTimeoutSeconds = cfg.NotifierGatewayData.ServiceDiscoveryTimeoutSeconds

	// Update deprecated variables for backward compatibility
	DynamoDBActionRetryAttempts = dynamoDBActionRetryAttempts
	HealthReportCleanUpFrequencySeconds = healthReportCleanUpFrequencySeconds
	HealthReportRecordStaleMinutes = healthReportRecordStaleMinutes
	ServiceDiscoveryTimeoutSeconds = serviceDiscoveryTimeoutSeconds
	configMu.Unlock()

	return nil
}

func GetServerRouteFromDataStore(serverKey string) (serverUrl string, err error) {
	if _ddbStore == nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: Server Key is Required")
	}

	pk := fmt.Sprintf(pkPattern, pkPrefix, pkService)
	sk := fmt.Sprintf(skPattern, serverKey)

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

	pk := fmt.Sprintf(pkPattern, pkPrefix, pkService)
	sk := fmt.Sprintf(skPattern, serverKey)

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

	pk := fmt.Sprintf(pkHealthPattern, pkPrefix, pkHealthPrefix)
	sk := fmt.Sprintf(skHealthPattern, instanceId)

	statusInfo := new(HealthStatus)

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

	pk := fmt.Sprintf(pkHealthPattern, pkPrefix, pkHealthPrefix)
	sk := fmt.Sprintf(skHealthPattern, instanceId)
	timeNowUTC := time.Now().UTC()

	statusInfo := &HealthStatus{
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
		return []*dynamodb.DynamoDBTableKeyValue{}, fmt.Errorf("Delete Instance Health From Data Store Failed: InstanceKeys To Delete is Required")
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

func ListInactiveInstancesFromDataStore() (inactiveInstances []*HealthStatus, err error) {
	// FIX: Original code used named return 'err' (which is nil) in the error message:
	//   fmt.Errorf("...Failed: %s", err) — produced "...Failed: %!s(<nil>)"
	if _ddbStore == nil {
		return []*HealthStatus{}, fmt.Errorf("List Inactive Instances From Data Store Failed: DynamoDB Connection Not Established")
	}

	pk := fmt.Sprintf(pkHealthPattern, pkPrefix, pkHealthPrefix)

	staleMinutes := GetHealthReportRecordStaleMinutes()

	if staleMinutes == 0 {
		staleMinutes = 5
	} else if staleMinutes < 3 {
		staleMinutes = 3
	} else if staleMinutes > 15 {
		staleMinutes = 15
	}

	ts := util.Int64ToString(time.Now().UTC().Add(time.Duration(staleMinutes) * time.Minute * -1).Unix())

	if itemsList, e := _ddbStore.QueryPagedItemsWithRetry(_ddbActionRetries, &[]*HealthStatus{}, &[]*HealthStatus{},
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
		return []*HealthStatus{}, fmt.Errorf("List Inactive Instances From Data Store Failed: %s", e)
	} else {
		// success
		ok := false

		if inactiveInstances, ok = itemsList.([]*HealthStatus); !ok {
			return []*HealthStatus{}, nil
		} else {
			return inactiveInstances, nil
		}
	}
}
