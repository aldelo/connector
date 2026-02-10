package impl

import (
	"fmt"
	"net/url"
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/dynamodb"
	"strings"
	"time"
)

const (
	pkPrefix       = "corems"
	pkService      = "notifier-server"
	pkPattern      = "%s#%s#service#discovery#host#target"
	skPattern      = "ServerKey^%s"
)

type serverRoute struct {
	PK string				`json:"pk" dynamodbav:"PK"`
	SK string				`json:"sk" dynamodbav:"SK"`
	ServerKey string		`json:"serverkey" dynamodbav:"ServerKey"`
	HostInfo string			`json:"hostinfo" dynamodbav:"HostInfo"`
	LastTimestamp int64		`json:"lasttimestamp" dynamodbav:"LastTimestamp"`
}

func (n *NotifierImpl) ConnectDataStore() error {
	if n == nil {
		return fmt.Errorf("NotifierImpl receiver is nil")
	}

	if n.ConfigData == nil || n.ConfigData.NotifierServerData == nil {
		return fmt.Errorf("Config data is not initialized")
	}

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

func validateServerURL(serverUrl string) error {
	serverUrl = strings.TrimSpace(serverUrl)
	if serverUrl == "" {
		return fmt.Errorf("server URL is required")
	}

	u, err := url.Parse(serverUrl)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must use http or https scheme, got: %s", u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("URL must have a host")
	}

	return nil
}

func (n *NotifierImpl) saveServerRouteToDataStore(serverKey string, serverUrl string) (err error) {
	if n._ddbStore == nil {
		return fmt.Errorf("Persist Server Routing to Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return fmt.Errorf("Notifier Server Key is Required")
	}

	if util.LenTrim(serverUrl) == 0 {
		return fmt.Errorf("Notifier Server Callback URL Endpoint is Required")
	}

	if err := validateServerURL(serverUrl); err != nil {
		return fmt.Errorf("Notifier Server Callback URL Endpoint: %w", err)
	}

	pk := fmt.Sprintf(pkPattern, pkPrefix, pkService)
	sk := fmt.Sprintf(skPattern, serverKey)

	hostInfo := &serverRoute{
		PK: pk,
		SK: sk,
		ServerKey: serverKey,
		HostInfo: serverUrl,
		LastTimestamp: time.Now().Unix(),
	}

	if e := n._ddbStore.PutItemWithRetry(n.ConfigData.NotifierServerData.DynamoDBActionRetries,
										 hostInfo,
										 n._ddbStore.TimeOutDuration(n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds)); e != nil {
		// error
		return fmt.Errorf("Persist Server Routing to Data Store Failed: %s", e)
	} else {
		// success
		return nil
	}
}

func (n *NotifierImpl) getServerRouteFromDataStore(serverKey string) (serverUrl string, err error) {
	if n._ddbStore == nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: Server Key is Required")
	}

	pk := fmt.Sprintf(pkPattern, pkPrefix, pkService)
	sk := fmt.Sprintf(skPattern, serverKey)

	routeInfo := &serverRoute{}

	if e := n._ddbStore.GetItemWithRetry(n.ConfigData.NotifierServerData.DynamoDBActionRetries, routeInfo, pk, sk, util.DurationPtr(time.Duration(n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds)*time.Second), nil); e != nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: %s", e)
	} else {
		return routeInfo.HostInfo, nil
	}
}

func (n *NotifierImpl) deleteServerRouteFromDataStore(serverKey string) error {
	if n._ddbStore == nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if util.LenTrim(serverKey) == 0 {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: Server Key is Required")
	}

	pk := fmt.Sprintf(pkPattern, pkPrefix, pkService)
	sk := fmt.Sprintf(skPattern, serverKey)

	if err := n._ddbStore.DeleteItemWithRetry(n.ConfigData.NotifierServerData.DynamoDBActionRetries, pk, sk, util.DurationPtr(time.Duration(n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds)*time.Second)); err != nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: %s", err)
	} else {
		return nil
	}
}
