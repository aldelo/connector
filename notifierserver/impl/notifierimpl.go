package impl

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
	"context"
	"fmt"
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/dynamodb"
	"github.com/aldelo/common/wrapper/sns"
	"github.com/aldelo/common/wrapper/sns/snsprotocol"
	"github.com/aldelo/connector/adapters/notification"
	"github.com/aldelo/connector/notifierserver/config"
	pb "github.com/aldelo/connector/notifierserver/proto"
	"log"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------------------------------------------------
// define clientEndpoint and clientEndpointMap
// ---------------------------------------------------------------------------------------------------------------------
type clientEndpoint struct {
	ClientId      string                    // required, typically custom created guid or other unique id value, used during unsubscribe
	TopicArn      string                    // required
	DataToSend    chan *pb.NotificationData // this field will be initialized by clientEndpointMap.Add (No need to init outside)
	ClientContext context.Context           // this field should be setup by code outside of clientEndpointMap
}

type clientEndpointMap struct {
	Clients map[string][]*clientEndpoint
	_mux    sync.Mutex
}

func (m *clientEndpointMap) Add(ep *clientEndpoint) bool {
	if ep == nil {
		return false
	}

	if util.LenTrim(ep.ClientId) == 0 {
		return false
	}

	if util.LenTrim(ep.TopicArn) == 0 {
		return false
	}

	if m.Clients == nil {
		m.Clients = make(map[string][]*clientEndpoint)
	}

	ep.DataToSend = make(chan *pb.NotificationData, 1)

	epList := m.Clients[ep.TopicArn]
	epList = append(epList, ep)

	m.Clients[ep.TopicArn] = epList

	return true
}

func (m *clientEndpointMap) RemoveByClientId(clientId string) bool {
	if util.LenTrim(clientId) == 0 {
		return false
	}

	if m.Clients == nil {
		return false
	}

	if len(m.Clients) == 0 {
		return false
	}

	// find the topicArn of clientId
	var epListToRemove []*clientEndpoint

	epIndexToRemove := -1
	topicArnToRemove := ""
	breakFromLoop := false

	for topicArn, epList := range m.Clients {
		for epIndex, ep := range epList {
			if ep != nil && strings.ToUpper(ep.ClientId) == strings.ToUpper(clientId) {
				// found match
				epListToRemove = epList
				epIndexToRemove = epIndex
				topicArnToRemove = topicArn
				breakFromLoop = true
				break
			}
		}

		if breakFromLoop {
			break
		}
	}

	// perform remove action
	if len(epListToRemove) > 0 && util.LenTrim(topicArnToRemove) > 0 && epIndexToRemove >= 0 {
		// continue to remove
		if resultSlice := util.SliceDeleteElement(epListToRemove, epIndexToRemove); resultSlice != nil {
			if l, ok := resultSlice.([]*clientEndpoint); ok {
				if len(l) == 0 {
					// slice all removed for given topic arn
					delete(m.Clients, topicArnToRemove)
				} else {
					// still remaining slice for given topic arn
					m.Clients[topicArnToRemove] = l
				}

				return true
			} else {
				return false
			}
		} else {
			return false
		}
	} else {
		// not removed
		return false
	}
}

func (m *clientEndpointMap) RemoveByTopicArn(topicArn string) bool {
	if util.LenTrim(topicArn) == 0 {
		return false
	}

	if m.Clients == nil {
		return false
	}

	delete(m.Clients, topicArn)

	return true
}

func (m *clientEndpointMap) RemoveAll() {
	m.Clients = make(map[string][]*clientEndpoint)
}

func (m *clientEndpointMap) GetByClientId(clientId string) *clientEndpoint {
	if util.LenTrim(clientId) == 0 {
		return nil
	}

	if m.Clients == nil {
		return nil
	}

	for _, epList := range m.Clients {
		for _, ep := range epList {
			if ep != nil && strings.ToUpper(ep.ClientId) == strings.ToUpper(clientId) {
				// found match
				return ep
			}
		}
	}

	return nil
}

func (m *clientEndpointMap) GetByTopicArn(topicArn string) []*clientEndpoint {
	if util.LenTrim(topicArn) == 0 {
		return []*clientEndpoint{}
	}

	if m.Clients == nil {
		return []*clientEndpoint{}
	}

	for t, epList := range m.Clients {
		if strings.ToUpper(t) == strings.ToUpper(topicArn) {
			return epList
		}
	}

	return []*clientEndpoint{}
}

func (m *clientEndpointMap) SetDataToSendByClientId(clientId string, dataToSend *pb.NotificationData) bool {
	if util.LenTrim(clientId) == 0 {
		return false
	}

	if dataToSend == nil {
		return false
	}

	if m.Clients == nil {
		return false
	}

	if ep := m.GetByClientId(clientId); ep != nil {
		ep.DataToSend <- dataToSend
		return true
	} else {
		return false
	}
}

func (m *clientEndpointMap) SetDataToSendByTopicArn(topicArn string, dataToSend *pb.NotificationData) bool {
	log.Println("SetDataToSendByTopicArn - Entry")

	if util.LenTrim(topicArn) == 0 {
		log.Println("Trace: SetDataToSendByTopicArn - topicArn Len 0")
		return false
	}

	if dataToSend == nil {
		log.Println("Trace: SetDataToSendByTopicArn - dataToSend Nil")
		return false
	}

	if m.Clients == nil {
		log.Println("Trace: SetDataToSendByTopicArn - Clients Nil")
		return false
	}

	if epList := m.GetByTopicArn(topicArn); len(epList) > 0 {
		log.Println("Trace: SetDataToSendByTopicArn - GetTopicArn Has epList")

		for _, ep := range epList {
			if ep != nil {
				log.Println("Trace: SetDataSendByTopicArn (Loop) - Before <-dataToSend Assign to ep.DataToSend")
				select {
				case ep.DataToSend <- dataToSend:
					log.Println("Trace: SetDataSendByTopicArn (Loop) - After <-dataToSend Assign to ep.DataToSend")
				case <-time.After(2 * time.Second):
					log.Println("Trace: SetDataSendByTopicArn (Loop) - timeout(2s) on <-dataToSend Assign to ep.DataToSend")
					continue
				}
			} else {
				log.Println("Trace: SetDataSendByTopicArn (Loop) - ep Nil")
			}
		}

		return true
	} else {
		log.Println("Trace: SetDataToSendByTopicArn - GetTopicArn epList Count 0")
		return false
	}
}

func (m *clientEndpointMap) ClientsCount() int {
	if m.Clients == nil {
		return 0
	}

	count := 0

	for _, epList := range m.Clients {
		if epList != nil {
			count += len(epList)
		}
	}

	return count
}

func (m *clientEndpointMap) TopicsCount() int {
	if m.Clients == nil {
		return 0
	}

	return len(m.Clients)
}

func (m *clientEndpointMap) ClientsByTopicCount(topicArn string) int {
	if m.Clients == nil {
		return 0
	}

	if m.Clients == nil {
		return 0
	}

	for t, epList := range m.Clients {
		if strings.ToUpper(t) == strings.ToUpper(topicArn) && epList != nil {
			return len(epList)
		}
	}

	return 0
}

// ---------------------------------------------------------------------------------------------------------------------
// define NotifierImpl
// ---------------------------------------------------------------------------------------------------------------------

type NotifierImpl struct {
	pb.UnimplementedNotifierServiceServer

	ConfigData                *config.Config
	WebServerLocalAddressFunc func() string

	_clients *clientEndpointMap

	_ddbStore *dynamodb.DynamoDB
	_sns      *sns.SNS

	_mux sync.Mutex
}

// ReadConfig will read in config data
func (n *NotifierImpl) ReadConfig(appName string, configFileName string, customConfigPath string) error {
	n.ConfigData = &config.Config{
		AppName:          appName,
		ConfigFileName:   configFileName,
		CustomConfigPath: customConfigPath,
	}

	if err := n.ConfigData.Read(); err != nil {
		return fmt.Errorf("Read Notifier Config Failed: %s", err.Error())
	}

	if strings.ToLower(util.Left(n.ConfigData.NotifierServerData.GatewayUrl, 7)) != "http://" && strings.ToLower(util.Left(n.ConfigData.NotifierServerData.GatewayUrl, 8)) != "https://" {
		return fmt.Errorf("Notifier Gateway URL is Required, Must Begin with https:// or http://")
	}

	if util.LenTrim(n.ConfigData.NotifierServerData.DynamoDBAwsRegion) == 0 {
		return fmt.Errorf("Notifier's DynamoDB AWS Region is Required")
	}

	if util.LenTrim(n.ConfigData.NotifierServerData.DynamoDBTable) == 0 {
		return fmt.Errorf("Notifier's DynamoDB Table Name is Required")
	}

	if n.ConfigData.NotifierServerData.DynamoDBUseDax {
		if util.LenTrim(n.ConfigData.NotifierServerData.DynamoDBDaxUrl) == 0 {
			return fmt.Errorf("Notifier's DynamoDB Dax URL is Required When Use Dax is True")
		}
	}

	if util.LenTrim(n.ConfigData.NotifierServerData.SnsAwsRegion) == 0 {
		return fmt.Errorf("Notifier's SNS AWS Region is Required")
	}

	return nil
}

func (n *NotifierImpl) ConnectSNS(awsRegionStr awsregion.AWSRegion) (err error) {
	if n._sns, err = notification.NewNotificationAdapter(awsRegionStr, nil); err != nil {
		return err
	} else {
		return nil
	}
}

// UnsubscribeAllPriorSNSTopics will attempt to clean up all prior SNS topic subscriptions based in the config
func (n *NotifierImpl) UnsubscribeAllPriorSNSTopics(topicArnLimit ...string) {
	log.Println("=== Notifier Server Unsubscribe All Prior SNS Topics Invoked ===")
	defer func() {
		if util.LenTrim(n.ConfigData.NotifierServerData.ServerKey) > 0 {
			if e := n.deleteServerRouteFromDataStore(n.ConfigData.NotifierServerData.ServerKey); e != nil {
				log.Println("!!! DeleteServerRouteFromDataStore with Key '" + n.ConfigData.NotifierServerData.ServerKey + "' Failed: " + e.Error())
			}
		}
	}()

	if len(n.ConfigData.SubscriptionsData) == 0 {
		return
	}

	if n._sns == nil {
		return
	}

	topicArnToUnsubscribe := ""

	if len(topicArnLimit) > 0 {
		topicArnToUnsubscribe = topicArnLimit[0]
	}

	timeout := 5 * time.Second

	if util.LenTrim(topicArnToUnsubscribe) == 0 {
		// unsubscribe all sns topics tracked in config
		for _, v := range n.ConfigData.SubscriptionsData {
			if util.LenTrim(v.SubscriptionArn) > 0 {
				_ = notification.Unsubscribe(n._sns, v.SubscriptionArn, timeout)
			}
		}

		n.ConfigData.RemoveSubscriptionData("")
	} else {
		// unsubscribe specific sns topic
		for _, v := range n.ConfigData.SubscriptionsData {
			if util.LenTrim(v.SubscriptionArn) > 0 && strings.ToUpper(v.TopicArn) == strings.ToUpper(topicArnToUnsubscribe) {
				_ = notification.Unsubscribe(n._sns, v.SubscriptionArn, timeout)
			}
		}

		n.ConfigData.RemoveSubscriptionData(topicArnToUnsubscribe)
	}

	_ = n.ConfigData.Save()

	log.Println("=== Notifier Server Unsubscribe All Prior SNS Topics: OK ===")
}

// subscribeToSNSTopic is an internal helper to subscribe given subscriber topic, and subscribe to sns topic is needed,
// return nil indicate operation completed, whether performed or not need to be performed,
// return error indicates action failed
func (n *NotifierImpl) subscribeToSNSTopic(s *pb.NotificationSubscriber) error {
	if n.ConfigData == nil {
		return fmt.Errorf("Subscribe To SNS Topic Requires Config Object")
	}

	if s == nil {
		return fmt.Errorf("Subscribe To SNS Topic Requires Notification Subscriber Object")
	}

	if util.LenTrim(s.Topic) == 0 {
		return fmt.Errorf("Subscribe Requires SNS Topic ARN")
	}

	if n._sns == nil {
		return fmt.Errorf("Subscribe Requires SNS Connection")
	}

	if n.WebServerLocalAddressFunc == nil {
		return fmt.Errorf("Subscribe Requires Web Server Local Address Func Assigned")
	}

	subsArn := n.ConfigData.GetSubscriptionArn(s.Topic)

	if util.LenTrim(subsArn) == 0 || strings.ToUpper(subsArn) == "PENDING CONFIRMATION" {
		log.Println("+++ Subscribing To SNS TopicArn '" + s.Topic + "' +++")

		// no subscription found for given topic
		// will perform sns subscribe topic
		var snsp snsprotocol.SNSProtocol

		if util.IsHttpsEndpoint(n.ConfigData.NotifierServerData.GatewayUrl) {
			snsp = snsprotocol.Https
		} else {
			snsp = snsprotocol.Http
		}

		timeout := time.Duration(5) * time.Second

		if subsArn, err := notification.Subscribe(n._sns, s.Topic, snsp, fmt.Sprintf("%s/%s", n.ConfigData.NotifierServerData.GatewayUrl, n.ConfigData.NotifierServerData.ServerKey), timeout); err != nil {
			// error
			log.Println("!!! Subscribe TopicArn '" + s.Topic + "' with Callback To '" + fmt.Sprintf("%s/%s", n.ConfigData.NotifierServerData.GatewayUrl, n.ConfigData.NotifierServerData.ServerKey) + "' Failed: " + err.Error() + " !!!")
			return fmt.Errorf("Subscribe to SNS Topic '%s' Failed: (SNS Subscribe Action) %s", s.Topic, err)
		} else {
			// no error
			// update config to store new subscription info
			n.ConfigData.SetSubscriptionData(s.Topic, subsArn) // subsArn is Pending Confirmation, will be updated later via callback

			log.Println("+++ Subscribe TopicArn '" + s.Topic + "' with Callback To '" + fmt.Sprintf("%s/%s", n.ConfigData.NotifierServerData.GatewayUrl, n.ConfigData.NotifierServerData.ServerKey) + "' Success: SubscribeArn '" + subsArn + "' +++")

			if err := n.ConfigData.Save(); err != nil {
				// attempt clean up upon config save failure
				log.Println("!!! Subscribed TopicArn '" + s.Topic + "' with Callback To '" + fmt.Sprintf("%s/%s", n.ConfigData.NotifierServerData.GatewayUrl, n.ConfigData.NotifierServerData.ServerKey) + "' Failed to Save to Config: " + err.Error() + " !!!")
				_ = notification.Unsubscribe(n._sns, subsArn, timeout)
				return fmt.Errorf("Subscribe to SNS Topic '%s' Failed: (Persist to Config Error) %s", s.Topic, err)
			}

			// upon sns subscribe success,
			// persist host lookup value to dynamodb for notifierGateway to lookup and route upon sns callback
			if err := n.saveServerRouteToDataStore(n.ConfigData.NotifierServerData.ServerKey, n.WebServerLocalAddressFunc()); err != nil {
				// save to dynamodb error
				log.Println("!!! Subscribed TopicArn '" + s.Topic + "' with Callback To '" + fmt.Sprintf("%s/%s", n.ConfigData.NotifierServerData.GatewayUrl, n.ConfigData.NotifierServerData.ServerKey) + "' Failed to Save to DynamoDB: " + err.Error() + " !!!")

				// attempt clean up sns subscription
				_ = notification.Unsubscribe(n._sns, subsArn, timeout)

				// attempt clean up config value
				n.ConfigData.RemoveSubscriptionData(s.Topic)
				_ = n.ConfigData.Save()

				return fmt.Errorf("Subscribe to SNS Topic '%s' Failed: (Persist to Data Store Failed) %s", s.Topic, err)
			}

			log.Println("$$$ Subscribe TopicArn '" + s.Topic + "' with Callback To '" + fmt.Sprintf("%s/%s", n.ConfigData.NotifierServerData.GatewayUrl, n.ConfigData.NotifierServerData.ServerKey) + "' Persisted to Config and DynamoDB $$$")
		}
	} else {
		log.Println("~~~ SNS TopicArn '" + s.Topic + "' Already Has SubscriptionArn '" + subsArn + "', Subscribe Action Skipped ~~~")
	}

	// subscribe topic to sns ok, or not needed as already subscribed
	return nil
}

// Subscribe handles rpc implementation to subscribe a client to a topicArn, and process ongoing server stream notification to client
func (n *NotifierImpl) Subscribe(s *pb.NotificationSubscriber, serverStream pb.NotifierService_SubscribeServer) error {
	//
	// perform sns topic subscription for the given subscriber, if sns topic not yet subscribed for this host
	// (sns topic subscription is notifier server host specific, rather than each subscriber client)
	//
	log.Println("+++ Notifier Server RPC Subscribe Invoked +++")

	if serverStream == nil {
		return fmt.Errorf("RPC Subscribe Requires serverStream")
	}

	if err := n.subscribeToSNSTopic(s); err != nil {
		return err
	}

	if n._clients == nil {
		n._mux.Lock()
		n._clients = new(clientEndpointMap)
		n._mux.Unlock()
	}

	log.Println("=== Notifier Server RPC Server TopicArn '" + s.Topic + "' To Client ID '" + s.Id + "' Stream Send Loop Begin ===")

	ep := &clientEndpoint{
		ClientId:      s.Id,
		TopicArn:      s.Topic,
		ClientContext: serverStream.Context(),
	}

	n._clients._mux.Lock()
	n._clients.Add(ep)
	n._clients._mux.Unlock()

	//
	// start loop for pushing received data to client via server stream
	//
	endLoop := false

	for {
		select {
		case <-ep.ClientContext.Done():
			log.Println("--- RPC Subscribe Stream Send Loop for TopicArn '" + ep.TopicArn + "' with Client ID '" + ep.ClientId + "' Ended: Server Stream Context Done ---")
			endLoop = true

		case data := <-ep.DataToSend:
			log.Println("$$$ RPC Subscribe Stream Send Loop for TopicArn '" + ep.TopicArn + "' to Client ID '" + ep.ClientId + "' Received Data to Send $$$")

			if data != nil {
				if err := serverStream.Send(data); err != nil {
					log.Println("!!! RPC Subscribe Stream Send for TopicArn '" + ep.TopicArn + "' to Client ID '" + ep.ClientId + "' Fail: (Loop Will End) " + err.Error() + " !!!")
					endLoop = true
					break
				} else {
					log.Println("$$$ RPC Subscribe Stream Send Loop for TopicArn '" + ep.TopicArn + "' to Client ID '" + ep.ClientId + "' Sent Data Successful $$$")
				}
			} else {
				log.Println("~~~ RPC Subscribe Stream Send for TopicArn '" + ep.TopicArn + "' to Client ID '" + ep.ClientId + "' Received Data was Nil ~~~")
			}

		default:
			// continue looping
			time.Sleep(100 * time.Millisecond)
		}

		if endLoop {
			break
		}
	}

	// at this point, loop ended, we will clean up the client endpoint as the end of the service cycle
	log.Println("### Notifier Server RPC Subscribe Send Loop Ending, Cleaning Up Client ID '" + ep.ClientId + "' ###")
	func() {
		n._clients._mux.Lock()
		defer n._clients._mux.Unlock()
		n._clients.RemoveByClientId(ep.ClientId)
	}()
	log.Println("### Notifier Server RPC Subscriber Send Loop Ended ###")

	return nil
}

// Unsubscribe handles rpc implementation to unsubscribe a client to a topicArn
func (n *NotifierImpl) Unsubscribe(c context.Context, s *pb.NotificationSubscriber) (r *pb.NotificationDone, err error) {
	log.Println("+++ Notifier Server RPC Unsubscribe Invoked +++")

	if n._clients == nil {
		return &pb.NotificationDone{}, fmt.Errorf("RPC Unsubscribe Skipped, No Client Endpoints Found")
	}

	if util.LenTrim(s.Topic) == 0 {
		return &pb.NotificationDone{}, fmt.Errorf("RPC Unsubscribe Requires SNS Topic ARN")
	}

	if n._sns == nil {
		return &pb.NotificationDone{}, fmt.Errorf("RPC Unsubscribe Requires SNS Connection")
	}

	// find client endpoint for the given subscriber to unsubscribe
	n._clients._mux.Lock()
	defer n._clients._mux.Unlock()

	if ep := n._clients.GetByClientId(s.Id); ep != nil {
		// stop the client endpoint loop service
		_, cancel := context.WithCancel(ep.ClientContext)
		cancel()

		// remove client endpoint from clients map
		if n._clients.RemoveByClientId(s.Id) {
			log.Println("--- Notifier Server RPC Unsubscribe Remove Client ID '" + s.Id + "' From Client Endpoints Map: OK ---")
		} else {
			log.Println("!!! Notifier Server RPC Unsubscribe Remove Client ID '" + s.Id + "' From Client Endpoints Map: Failed ---")
		}

		if balCount := n._clients.ClientsByTopicCount(s.Topic); balCount > 0 {
			// has active clients in topic
			log.Println("### Notifier Server RPC Unsubscribe Completed, " + util.Itoa(balCount) + " Client Endpoints Remain for TopicArn '" + s.Topic + "' ###")
			return &pb.NotificationDone{}, nil
		} else {
			// no more active clients in topic
			removeTopic := false

			for _, v := range n.ConfigData.SubscriptionsData {
				if v != nil && strings.ToLower(v.TopicArn) == strings.ToLower(s.Topic) && util.LenTrim(v.SubscriptionArn) > 0 {
					if err := notification.Unsubscribe(n._sns, v.SubscriptionArn, 5*time.Second); err != nil {
						log.Printf("!!! RPC Unsubscribe SNS Topic '%s' Failed: %s !!!\n", s.Topic, err)
					} else {
						log.Printf("$$$ RPC Unsubscribe SNS Topic '%s' OK: (Zero Clients) $$$\n", s.Topic)
						removeTopic = true
					}
					break
				}
			}

			if removeTopic {
				n.ConfigData.RemoveSubscriptionData(s.Topic)

				if err := n.ConfigData.Save(); err != nil {
					log.Println("!!! RPC Unsubscribe Persist Config Subscription Removal Failed: " + err.Error() + " !!!")
				}
			}

			if e := n.deleteServerRouteFromDataStore(n.ConfigData.NotifierServerData.ServerKey); e != nil {
				log.Println("!!! DeleteServerRouteFromDataStore for Key '" + n.ConfigData.NotifierServerData.ServerKey + "' Failed: " + e.Error() + " !!!")
			}

			log.Println("### Notifier Server RPC Unsubscribe Completed, All Client Endpoints Removed for TopicArn '" + s.Topic + "' ###")
			return &pb.NotificationDone{}, nil
		}
	} else {
		// no client endpoint found
		log.Println("### Notifier Server RPC Unsubscribe Ended, No Client Endpoints On Host Match Client ID ###")
		return &pb.NotificationDone{}, fmt.Errorf("RPC Unsubscribe Skipped, No Client Endpoints On Host Match Client ID")
	}
}

// Broadcast handles rpc implementation to broadcast notification data to all clients under a specific topicArn
func (n *NotifierImpl) Broadcast(c context.Context, d *pb.NotificationData) (r *pb.NotificationDone, err error) {
	log.Println("+++ Notifier Server RPC Broadcast Invoked +++")

	if n._clients == nil {
		log.Println("!!! Notifier Server RPC Broadcast Requires Client Endpoints !!!")
		return &pb.NotificationDone{}, fmt.Errorf("RPC Broadcast Requires Client Endpoints")
	}

	n._clients._mux.Lock()
	defer n._clients._mux.Unlock()

	if epList := n._clients.GetByTopicArn(d.Topic); len(epList) > 0 {
		if n._clients.SetDataToSendByTopicArn(d.Topic, d) {
			// set data to topic clients successful
			log.Println("### Notifier Server RPC Broadcast Set Notification Data To Client Endpoints By TopicArn '" + d.Topic + "': OK ###")
			return &pb.NotificationDone{}, nil
		} else {
			// set data to topic clients failed
			log.Println("!!! Notifier Server RPC Broadcast Set Notification Data To Client Endpoints By TopicArn '" + d.Topic + "': Failed !!!")
			return &pb.NotificationDone{}, fmt.Errorf("RPC broadcast set notification data to client endpoints by TopicArn '%s' failed", d.Topic)
		}
	} else {
		// no client endpoints by topicArn
		log.Println("### Notifier Server RPC Broadcast Found Zero Client Endpoints By TopicArn '" + d.Topic + "' ###")
		return &pb.NotificationDone{}, fmt.Errorf("RPC broadcast found zero client endpoints by TopicArn '%s'", d.Topic)
	}
}

// UpdateSubscriptionArnToTopic persists given subscriptionArn to the matching TopicArn in config data
func (n *NotifierImpl) UpdateSubscriptionArnToTopic(topicArn string, subscriptionArn string) error {
	if n.ConfigData == nil {
		return fmt.Errorf("SNS Update SubscriptionArn for Topic Failed: Config Data Nil")
	}

	if util.LenTrim(topicArn) == 0 {
		return fmt.Errorf("SNS Update SubscriptionArn Requires TopicArn")
	}

	if util.LenTrim(subscriptionArn) == 0 {
		return fmt.Errorf("SNS Update SubscriptionArn Requires SubscriptionArn")
	}

	n.ConfigData.SetSubscriptionData(topicArn, subscriptionArn)

	if err := n.ConfigData.Save(); err != nil {
		return fmt.Errorf("SNS update subscriptionArn persist to config failed: %w", err)
	} else {
		return nil
	}
}
