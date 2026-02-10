package impl

import (
	"fmt"
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/dynamodb"
	"strings"
	"time"
)

type serverRoute struct {
	PK string				`json:"pk" dynamodbav:"PK"`
	SK string				`json:"sk" dynamodbav:"SK"`
	ServerKey string		`json:"serverkey" dynamodbav:"ServerKey"`
	HostInfo string			`json:"hostinfo" dynamodbav:"HostInfo"`
	LastTimestamp int64		`json:"lasttimestamp" dynamodbav:"LastTimestamp"`
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

	if strings.ToLower(util.Left(serverUrl, 7)) != "http://" && strings.ToLower(util.Left(serverUrl, 8)) != "https://" {
		return fmt.Errorf("Notifier Server Callback URL Endpoint Must Begin with https:// or http://")
	}

	pk := fmt.Sprintf("%s#%s#service#discovery#host#target", "corems", "notifier-server")
	sk := fmt.Sprintf("ServerKey^%s", serverKey)

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

	pk := fmt.Sprintf("%s#%s#service#discovery#host#target", "corems", "notifier-server")
	sk := fmt.Sprintf("ServerKey^%s", serverKey)

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

	pk := fmt.Sprintf("%s#%s#service#discovery#host#target", "corems", "notifier-server")
	sk := fmt.Sprintf("ServerKey^%s", serverKey)

	if err := n._ddbStore.DeleteItemWithRetry(n.ConfigData.NotifierServerData.DynamoDBActionRetries, pk, sk, util.DurationPtr(time.Duration(n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds)*time.Second)); err != nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: %s", err)
	} else {
		return nil
	}
}
