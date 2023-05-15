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
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/dynamodb"
	"github.com/aldelo/connector/notifiergateway/config"
	"github.com/aws/aws-sdk-go/aws"
	ddb "github.com/aws/aws-sdk-go/service/dynamodb"
	"time"
)

var DynamoDBActionRetryAttempts uint
var ServiceDiscoveryTimeoutSeconds uint
var GatewayKey string
var HealthReportCleanUpFrequencySeconds uint
var HealthReportRecordStaleMinutes uint
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

		DynamoDBActionRetryAttempts = _ddbActionRetries
		HealthReportCleanUpFrequencySeconds = cfg.NotifierGatewayData.HealthReportCleanUpFrequencySeconds
		HealthReportRecordStaleMinutes = cfg.NotifierGatewayData.HealthReportRecordStaleMinutes
		ServiceDiscoveryTimeoutSeconds = cfg.NotifierGatewayData.ServiceDiscoveryTimeoutSeconds

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

func DeleteInstanceHealthFromDataStore(instanceKeys ...*dynamodb.DynamoDBTableKeys) (deleteFailKeys []*dynamodb.DynamoDBTableKeys, err error) {
	if _ddbStore == nil {
		return []*dynamodb.DynamoDBTableKeys{}, fmt.Errorf("Delete Instance Health From Data Store Failed: DynamoDB Connection Not Established")
	}

	if len(instanceKeys) == 0 {
		return []*dynamodb.DynamoDBTableKeys{}, fmt.Errorf("Delete Instance Health From Data Store Failed: %s", "InstanceKeys To Delete is Required")
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
		return []*dynamodb.DynamoDBTableKeys{}, nil
	}
}

func ListInactiveInstancesFromDataStore() (inactiveInstances []*healthStatus, err error) {
	if _ddbStore == nil {
		return []*healthStatus{}, fmt.Errorf("List Inactive Instances From Data Store Failed: %s", err)
	}

	pk := fmt.Sprintf("%s#%s#service#discovery#host#health", "corems", "all")

	staleMinutes := HealthReportRecordStaleMinutes

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
