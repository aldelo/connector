package impl

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/dynamodb"
)

const (
	pkPrefix  = "corems"
	pkService = "notifier-server"
	pkPattern = "%s#%s#service#discovery#host#target"
	skPattern = "ServerKey^%s"
)

type serverRoute struct {
	PK            string `json:"pk" dynamodbav:"PK"`
	SK            string `json:"sk" dynamodbav:"SK"`
	ServerKey     string `json:"serverkey" dynamodbav:"ServerKey"`
	HostInfo      string `json:"hostinfo" dynamodbav:"HostInfo"`
	LastTimestamp int64  `json:"lasttimestamp" dynamodbav:"LastTimestamp"`
}

// ConnectDataStore establishes the DynamoDB connection for the notifier server.
// FIX #1: Builds into a local variable first so n._ddbStore is never non-nil but unconnected.
// Other methods check n._ddbStore == nil as their guard — assigning before Connect()
// would let them pass the guard and use a broken connection.
func (n *NotifierImpl) ConnectDataStore() error {
	if n == nil {
		return fmt.Errorf("NotifierImpl receiver is nil")
	}

	if n.ConfigData == nil || n.ConfigData.NotifierServerData == nil {
		return fmt.Errorf("Config data is not initialized")
	}

	store := &dynamodb.DynamoDB{
		AwsRegion:   awsregion.GetAwsRegion(n.ConfigData.NotifierServerData.DynamoDBAwsRegion),
		SkipDax:     !n.ConfigData.NotifierServerData.DynamoDBUseDax,
		DaxEndpoint: n.ConfigData.NotifierServerData.DynamoDBDaxUrl,
		TableName:   n.ConfigData.NotifierServerData.DynamoDBTable,
		PKName:      "PK",
		SKName:      "SK",
	}

	if err := store.Connect(); err != nil {
		return err
	}

	if n.ConfigData.NotifierServerData.DynamoDBUseDax {
		if err := store.EnableDax(); err != nil {
			return err
		}
	}

	// Connection fully established — now publish to receiver
	n._ddbStore = store
	return nil
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

// FIX #2: All data store methods now guard against nil ConfigData/NotifierServerData
// to prevent nil-pointer panics if called before ReadConfig.
// FIX #3: Timeout construction now consistently uses n._ddbStore.TimeOutDuration()
// across all methods instead of mixing with util.DurationPtr.

func (n *NotifierImpl) saveServerRouteToDataStore(serverKey string, serverUrl string) error {
	if n._ddbStore == nil {
		return fmt.Errorf("Persist Server Routing to Data Store Failed: DynamoDB Connection Not Established")
	}

	if n.ConfigData == nil || n.ConfigData.NotifierServerData == nil {
		return fmt.Errorf("Persist Server Routing to Data Store Failed: Config Data Not Initialized")
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
		PK:            pk,
		SK:            sk,
		ServerKey:     serverKey,
		HostInfo:      serverUrl,
		LastTimestamp: time.Now().Unix(),
	}

	if e := n._ddbStore.PutItemWithRetry(
		n.ConfigData.NotifierServerData.DynamoDBActionRetries,
		hostInfo,
		n._ddbStore.TimeOutDuration(n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds),
	); e != nil {
		return fmt.Errorf("Persist Server Routing to Data Store Failed: %s", e)
	}

	return nil
}

func (n *NotifierImpl) getServerRouteFromDataStore(serverKey string) (serverUrl string, err error) {
	if n._ddbStore == nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if n.ConfigData == nil || n.ConfigData.NotifierServerData == nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: Config Data Not Initialized")
	}

	if util.LenTrim(serverKey) == 0 {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: Server Key is Required")
	}

	pk := fmt.Sprintf(pkPattern, pkPrefix, pkService)
	sk := fmt.Sprintf(skPattern, serverKey)

	routeInfo := &serverRoute{}

	// FIX #3: Use n._ddbStore.TimeOutDuration() consistently (was using util.DurationPtr)
	if e := n._ddbStore.GetItemWithRetry(
		n.ConfigData.NotifierServerData.DynamoDBActionRetries,
		routeInfo,
		pk,
		sk,
		n._ddbStore.TimeOutDuration(n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds),
		nil,
	); e != nil {
		return "", fmt.Errorf("Get Server Routing From Data Store Failed: %s", e)
	}

	return routeInfo.HostInfo, nil
}

func (n *NotifierImpl) deleteServerRouteFromDataStore(serverKey string) error {
	if n._ddbStore == nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: DynamoDB Connection Not Established")
	}

	if n.ConfigData == nil || n.ConfigData.NotifierServerData == nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: Config Data Not Initialized")
	}

	if util.LenTrim(serverKey) == 0 {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: Server Key is Required")
	}

	pk := fmt.Sprintf(pkPattern, pkPrefix, pkService)
	sk := fmt.Sprintf(skPattern, serverKey)

	// FIX #3: Use n._ddbStore.TimeOutDuration() consistently (was using util.DurationPtr)
	if err := n._ddbStore.DeleteItemWithRetry(
		n.ConfigData.NotifierServerData.DynamoDBActionRetries,
		pk,
		sk,
		n._ddbStore.TimeOutDuration(n.ConfigData.NotifierServerData.DynamoDBTimeoutSeconds),
	); err != nil {
		return fmt.Errorf("Delete Server Routing From Data Store Failed: %s", err)
	}

	return nil
}
