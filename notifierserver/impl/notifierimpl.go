package impl

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
	"context"
	"fmt"
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/dynamodb"
	"github.com/aldelo/common/wrapper/sns"
	"github.com/aldelo/common/wrapper/sns/snsprotocol"
	"github.com/aldelo/connector/notifierserver/config"
	"github.com/aldelo/connector/adapters/notification"
	pb "github.com/aldelo/connector/notifierserver/proto"
	"google.golang.org/grpc/grpclog"
	"log"
	"strings"
	"sync"
	"time"
)

type StreamConnection struct {
	Id string
	Topic string
	Stream pb.Notifier_SubscribeServer
	Active bool
	Err chan error
}

type NotifierImpl struct {
	pb.UnimplementedNotifierServer

	Subscribers map[string][]*StreamConnection
	ConfigData *config.Config

	WebServerLocalAddressFunc func() string

	_ddbStore *dynamodb.DynamoDB
	_sns *sns.SNS

	_mux sync.RWMutex
}

// ReadConfig will read in config data
func (n *NotifierImpl) ReadConfig(appName string, configFileName string, customConfigPath string) error {
	n.ConfigData = &config.Config{
		AppName: appName,
		ConfigFileName: configFileName,
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

// UnsubscribeAllPrior will attempt to clean up all prior subscriptions based in the config
func (n *NotifierImpl) UnsubscribeAllPrior() {
	if len(n.ConfigData.SubscriptionsData) == 0 {
		return
	}

	if n._sns == nil {
		return
	}

	timeout := 5*time.Second

	for _, v := range n.ConfigData.SubscriptionsData{
		if util.LenTrim(v.SubscriptionArn) > 0 {
			_ = notification.Unsubscribe(n._sns, v.SubscriptionArn, timeout)
		}
	}

	n.ConfigData.RemoveSubscriptionData("")
	_ = n.ConfigData.Save()
}

func (n *NotifierImpl) Subscribe(s *pb.NotificationSubscriber, serverStream pb.Notifier_SubscribeServer) error {
	n._mux.Lock()
	defer n._mux.Unlock()

	if n.Subscribers == nil {
		n.Subscribers = make(map[string][]*StreamConnection)
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

	sc := &StreamConnection{
		Id: s.Id,
		Topic: s.Topic,
		Stream: serverStream,
		Active: true,
		Err: make(chan error),
	}

	// upon client subscribe to this host,
	// perform sns subscribe https endpoint (common public https endpoint)
	if util.LenTrim(n.ConfigData.GetSubscriptionArn(s.Topic)) == 0 {
		// no subscription found for given topic
		// will perform sns subscribe topic
		var snsp snsprotocol.SNSProtocol

		if util.IsHttpsEndpoint(n.ConfigData.NotifierServerData.GatewayUrl) {
			snsp = snsprotocol.Https
		} else {
			snsp = snsprotocol.Http
		}

		timeout := time.Duration(5)*time.Second

		if subsArn, err := notification.Subscribe(n._sns, s.Topic, snsp, fmt.Sprintf("%s/%s", n.ConfigData.NotifierServerData.GatewayUrl, n.ConfigData.NotifierServerData.ServerKey), timeout); err != nil {
			// error
			return fmt.Errorf("Subscribe to SNS Topic '%s' Failed: (SNS Subscribe Action) %s", s.Topic, err)
		} else {
			// no error
			// update config to store new subscription info
			n.ConfigData.SetSubscriptionData(s.Topic, subsArn)

			if err := n.ConfigData.Save(); err != nil {
				// attempt clean up
				_ = notification.Unsubscribe(n._sns, subsArn, timeout)
				return fmt.Errorf("Subscribe to SNS Topic '%s' Failed: (Persist to Config Error) %s", s.Topic, err)
			}

			// upon sns subscribe success,
			// persist host lookup value to dynamodb for notifierGateway to lookup and route upon sns callback
			if err := n.saveServerRouteToDataStore(n.ConfigData.NotifierServerData.ServerKey, n.WebServerLocalAddressFunc(), n.ConfigData.NotifierServerData.DynamoDBActionRetries); err != nil {
				// save to dynamodb error

				// attempt clean up sns subscription
				_ = notification.Unsubscribe(n._sns, subsArn, timeout)

				// attempt clean up config value
				n.ConfigData.RemoveSubscriptionData(s.Topic)
				_ = n.ConfigData.Save()

				return fmt.Errorf("Subscribe to SNS Topic '%s' Failed: (Persist to Data Store Failed) %s", s.Topic, err)
			}
		}
	}

	// add server stream to list of subscribers on this host
	scList := n.Subscribers[s.Topic]
	scList = append(scList, sc)
	n.Subscribers[s.Topic] = scList

	return <-sc.Err
}

func (n *NotifierImpl) Unsubscribe(c context.Context, s *pb.NotificationSubscriber) (r *pb.NotificationDone, err error) {
	n._mux.Lock()
	defer n._mux.Unlock()

	if n.Subscribers == nil {
		n.Subscribers = make(map[string][]*StreamConnection)
	}

	if util.LenTrim(s.Topic) == 0 {
		return &pb.NotificationDone{}, fmt.Errorf("Unsubscribe Requires SNS Topic ARN")
	}

	if n._sns == nil {
		return &pb.NotificationDone{}, fmt.Errorf("Unsubscribe Requires SNS Connection")
	}

	subs := n.Subscribers[s.Topic]
	splitIndex := -1

	for i, v := range subs{
		if v != nil {
			if strings.ToLower(v.Id) == strings.ToLower(s.Id) {
				splitIndex = i
				break
			}
		}
	}

	if splitIndex >= 0 {
		subsIFace := util.SliceDeleteElement(subs, splitIndex)

		ok := false
		if subs, ok = subsIFace.([]*StreamConnection); !ok {
			subs = []*StreamConnection{}
		}

		activeCount := 0

		for _, v2 := range subs{
			if v2 != nil && v2.Active {
				activeCount++
			}
		}

		if activeCount > 0 {
			n.Subscribers[s.Topic] = subs
			return &pb.NotificationDone{}, nil
		} else {
			// this topic no more active clients
			// time to unsubscribe from sns and clean up
			delete(n.Subscribers, s.Topic)
			removeTopic := false

			for _, v3 := range n.ConfigData.SubscriptionsData{
				if v3 != nil && strings.ToLower(v3.TopicArn) == strings.ToLower(s.Topic) && util.LenTrim(v3.SubscriptionArn) > 0 {
					if err := notification.Unsubscribe(n._sns, v3.SubscriptionArn, 5*time.Second); err != nil {
						log.Printf("Unsubscribe SNS Topic '%s' Failed: %s\n", s.Topic, err)
					} else {
						log.Printf("Unsubscribe SNS Topic '%s' OK: (Zero Clients)\n", s.Topic)
						removeTopic = true
					}

					break
				}
			}

			if removeTopic {
				n.ConfigData.RemoveSubscriptionData(s.Topic)
				if err := n.ConfigData.Save(); err != nil {
					log.Println("Persist Config Subscription Removal Failed: " + err.Error())
				}
			}

			return &pb.NotificationDone{}, nil
		}
	} else {
		// not found
		return &pb.NotificationDone{}, nil
	}
}

func (n *NotifierImpl) Broadcast(c context.Context, d *pb.NotificationData) (r *pb.NotificationDone, err error) {
	if n.Subscribers == nil {
		return &pb.NotificationDone{}, fmt.Errorf("Broadcast Cancelled, No Subscribers Found On This Server")
	}

	topic := d.Topic

	if util.LenTrim(topic) == 0 {
		return &pb.NotificationDone{}, fmt.Errorf("Broadcast Cancelled, Input Notification Data Missing Topic")
	}

	n._mux.Lock()
	defer n._mux.Unlock()

	scList := n.Subscribers[topic]

	if len(scList) == 0 {
		return &pb.NotificationDone{}, fmt.Errorf("Broadcast Cancelled, No Subscribers Found For Topic '%s'", topic)
	}

	wg := sync.WaitGroup{}
	done := make(chan int)

	log.Println("Begin Broadcasting to " + util.Itoa(len(scList)) + " Subscribers On Topic '" + topic + "'...")

	for _, sc := range scList {
		wg.Add(1)

		go func(data *pb.NotificationData, streamConn *StreamConnection) {
			defer wg.Done()

			if streamConn.Active {
				if e := streamConn.Stream.Send(data); e != nil {
					grpclog.Errorf("Broadcasting Data with ID '%s' to Subscriber '%s' For Topic '%s' Failed: %s", data.Id, streamConn.Id, streamConn.Topic, e.Error())

					streamConn.Active = false
					streamConn.Err <- e
				} else {
					grpclog.Infof("Broadcasting Data with ID '%s' to Subscriber '%s' For Topic '%s' Completed", data.Id, streamConn.Id, streamConn.Topic)
				}
			} else {
				grpclog.Infof("Broadcasting Data with ID '%s' to Subscriber '%s' For Topic '%s' Skipped (StreamConnection Non-Active)", data.Id, streamConn.Id, streamConn.Topic)
			}
		}(d, sc)
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	<-done

	log.Println("... End Broadcasting to " + util.Itoa(len(scList)) + " Subscribers On Topic '" + topic + "'")

	return &pb.NotificationDone{}, nil
}
