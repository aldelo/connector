package impl

import (
	"fmt"
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/dynamodb"
	"log"
	"strings"
	"time"
)

type serverRoute struct {
	PK string				`json:"pk" dynamodbav:"PK"`
	SK string				`json:"sk" dynamodbav:"SK"`
	ServerHost string		`json:"host" dynamodbav:"Host"`
}

func (n *NotifierImpl) ConnectDataStore() error {
	n._ddbStore = &dynamodb.DynamoDB{
		AwsRegion: awsregion.GetAwsRegion(n.ConfigData.NotifierServerData.DynamoDBAwsRegion),
		SkipDax: !n.ConfigData.NotifierServerData.DynamoDBUseDax,
		DaxEndpoint: n.ConfigData.NotifierServerData.DynamoDBDaxUrl,
		TableName: n.ConfigData.NotifierServerData.DynamoDBTable,
		PKName: "PK",
		SKName: "SK",
	}

	if err := n._ddbStore.Connect(); err != nil {
		return err
	} else {
		if n.ConfigData.NotifierServerData.DynamoDBUseDax {
			if err = n._ddbStore.EnableDax(); err != nil {
				return err
			}
		}

		return nil
	}
}

// maxRetries = 0 disables retry, max is 10
func (n *NotifierImpl) saveServerRouteToDataStore(serverKey string, serverUrl string, maxRetries uint) error {
	if n._ddbStore == nil {
		return fmt.Errorf("Persist Server Routing to Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return fmt.Errorf("Notifier Server Key is Required")
	}

	if util.LenTrim(serverUrl) == 0 {
		return fmt.Errorf("Notifier Server Callback URL Endpoint is Required")
	}

	if strings.ToLower(util.Left(serverUrl, 7)) != "http://" && strings.ToLower(util.Left(serverUrl, 8)) != "https://" {
		return fmt.Errorf("Notifier Server Callback URL Endpoint Must Begin with https:// or http://")
	}

	if maxRetries > 10 {
		maxRetries = 10
	}

	pk := fmt.Sprintf("%s#notifier#gateway#route#config^serverkey#%s", n.ConfigData.AppName, serverKey)
	sk := "route"
	timeOut := uint(5)
	if n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds > 0 {
		timeOut = n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds
		if timeOut > 15 {
			timeOut = 15
		}
	}

	if err := n._ddbStore.PutItem(serverRoute{
		PK: pk,
		SK: sk,
		ServerHost: serverUrl,
	}, util.DurationPtr(time.Duration(timeOut)*time.Second)); err != nil {
		// error
		if maxRetries > 0 {
			if err.AllowRetry {
				if err.RetryNeedsBackOff {
					time.Sleep(500*time.Millisecond)
				} else {
					time.Sleep(100*time.Millisecond)
				}

				log.Printf("Persist Server Routing to Data Store Failed: (Retry) %s\n", err)

				return n.saveServerRouteToDataStore(serverKey, serverUrl, maxRetries-1)
			} else {
				if err.SuppressError {
					return nil
				} else {
					return fmt.Errorf("Persist Server Routing to Data Store Failed: (PutItem - Retry) %s", err)
				}
			}
		} else {
			if err.SuppressError {
				return nil
			} else {
				return fmt.Errorf("Persist Server Routing to Data Store Failed: (PutItem - Retries Exhausted) %s", err)
			}
		}
	} else {
		// no error
		return nil
	}
}

// maxRetries = 0 disables retry, max is 10
func (n *NotifierImpl) getServerRouteFromDataStore(serverKey string, maxRetries uint) (serverUrl string, err error) {
	if n._ddbStore == nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: Server Key is Required")
	}

	if maxRetries > 10 {
		maxRetries = 10
	}

	pk := fmt.Sprintf("%s#notifier#gateway#route#config^serverkey#%s", n.ConfigData.AppName, serverKey)
	sk := "route"
	timeOut := uint(5)
	if n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds > 0 {
		timeOut = n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds
		if timeOut > 15 {
			timeOut = 15
		}
	}

	routeInfo := new(serverRoute)

	if e := n._ddbStore.GetItem(routeInfo, pk, sk, util.DurationPtr(time.Duration(timeOut)*time.Second), nil); e != nil {
		// error
		if maxRetries > 0 {
			if e.AllowRetry {
				if e.RetryNeedsBackOff {
					time.Sleep(500*time.Millisecond)
				} else {
					time.Sleep(100*time.Millisecond)
				}

				log.Printf("Get Server Routing From Data Store Failed: (Retry) %s\n", e)

				return n.getServerRouteFromDataStore(serverKey, maxRetries-1)
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
func (n *NotifierImpl) deleteServerRouteFromDataStore(serverKey string, maxRetries uint) error {
	if n._ddbStore == nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: Server Key is Required")
	}

	if maxRetries > 10 {
		maxRetries = 10
	}

	pk := fmt.Sprintf("%s#notifier#gateway#route#config^serverkey#%s", n.ConfigData.AppName, serverKey)
	sk := "route"
	timeOut := uint(5)
	if n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds > 0 {
		timeOut = n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds
		if timeOut > 15 {
			timeOut = 15
		}
	}

	if err := n._ddbStore.DeleteItem(pk, sk, util.DurationPtr(time.Duration(timeOut)*time.Second)); err != nil {
		// error
		if maxRetries > 0 {
			if err.AllowRetry {
				if err.RetryNeedsBackOff {
					time.Sleep(500*time.Millisecond)
				} else {
					time.Sleep(100*time.Millisecond)
				}

				log.Printf("Delete Server Routing From Data Store Failed: (Retry) %s\n", err)

				return n.deleteServerRouteFromDataStore(serverKey, maxRetries-1)
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
