package model

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
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/dynamodb"
	"github.com/aldelo/connector/notifiergateway/config"
	"log"
	"time"
)

var DynamoDBActionRetryAttempts uint
var GatewayKey string

type serverRoute struct {
	PK string				`json:"pk" dynamodbav:"PK"`
	SK string				`json:"sk" dynamodbav:"SK"`
	ServerHost string		`json:"host" dynamodbav:"Host"`
}

var _ddbStore *dynamodb.DynamoDB
var _appName string
var _ddbTimeoutSeconds uint

func ConnectDataStore(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("Config Object is Required")
	}

	_ddbStore = &dynamodb.DynamoDB{
		AwsRegion: awsregion.GetAwsRegion(cfg.NotifierGatewayData.DynamoDBAwsRegion),
		SkipDax: !cfg.NotifierGatewayData.DynamoDBUseDax,
		DaxEndpoint: cfg.NotifierGatewayData.DynamoDBDaxUrl,
		TableName: cfg.NotifierGatewayData.DynamoDBTable,
		PKName: "PK",
		SKName: "SK",
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
		return nil
	}
}

// maxRetries = 0 disables retry, max is 10
func GetServerRouteFromDataStore(serverKey string, maxRetries uint) (serverUrl string, err error) {
	if _ddbStore == nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: Server Key is Required")
	}

	if maxRetries > 10 {
		maxRetries = 10
	}

	pk := fmt.Sprintf("%s#notifier#gateway#route#config^serverkey#%s", _appName, serverKey)
	sk := "route"
	timeOut := uint(5)
	if _ddbTimeoutSeconds > 0 {
		timeOut = _ddbTimeoutSeconds
		if timeOut > 15 {
			timeOut = 15
		}
	}

	routeInfo := new(serverRoute)

	if e := _ddbStore.GetItem(routeInfo, pk, sk, util.DurationPtr(time.Duration(timeOut)*time.Second), nil); e != nil {
		// error
		if maxRetries > 0 {
			if e.AllowRetry {
				if e.RetryNeedsBackOff {
					time.Sleep(500*time.Millisecond)
				} else {
					time.Sleep(100*time.Millisecond)
				}

				log.Printf("Get Server Routing From Data Store Failed: (Retry) %s\n", e)

				return GetServerRouteFromDataStore(serverKey, maxRetries-1)
			} else {
				if e.SuppressError {
					return "", nil
				} else {
					return "", fmt.Errorf("Get Server Routing From Data Store Failed: (GetItem - Retry) %s", e)
				}
			}
		} else {
			if e.SuppressError {
				return "", nil
			} else {
				return "", fmt.Errorf("Get Server Routing From Data Store Failed: (GetItem - Retries Exhausted) %s", e)
			}
		}
	} else {
		// no error
		return routeInfo.ServerHost, nil
	}
}

// maxRetries = 0 disables retry, max is 10
func DeleteServerRouteFromDataStore(serverKey string, maxRetries uint) error {
	if _ddbStore == nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: Server Key is Required")
	}

	if maxRetries > 10 {
		maxRetries = 10
	}

	pk := fmt.Sprintf("%s#notifier#gateway#route#config^serverkey#%s", _appName, serverKey)
	sk := "route"
	timeOut := uint(5)
	if _ddbTimeoutSeconds > 0 {
		timeOut = _ddbTimeoutSeconds
		if timeOut > 15 {
			timeOut = 15
		}
	}

	if err := _ddbStore.DeleteItem(pk, sk, util.DurationPtr(time.Duration(timeOut)*time.Second)); err != nil {
		// error
		if maxRetries > 0 {
			if err.AllowRetry {
				if err.RetryNeedsBackOff {
					time.Sleep(500*time.Millisecond)
				} else {
					time.Sleep(100*time.Millisecond)
				}

				log.Printf("Delete Server Routing From Data Store Failed: (Retry) %s\n", err)

				return DeleteServerRouteFromDataStore(serverKey, maxRetries-1)
			} else {
				if err.SuppressError {
					return nil
				} else {
					return fmt.Errorf("Delete Server Routing From Data Store Failed: (DeleteItem - Retry) %s", err)
				}
			}
		} else {
			if err.SuppressError {
				return nil
			} else {
				return fmt.Errorf("Delete Server Routing From Data Store Failed: (DeleteItem - Retries Exhausted) %s", err)
			}
		}
	} else {
		// no error
		return nil
	}
}
