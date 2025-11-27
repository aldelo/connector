package notifierserver

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

/*
=== PURPOSE ===
1) notifierServer implements impl.NotifierImpl interface
2) notifierServer is to be used by a cmd gRPC daemon where it acts as an independent service host
3) notifierServer exists because aws SNS http/s callback do not support private ip endpoints,
	therefore, if we need to enable gRPC clients subscribe to an aws SNS topic, with an http callback to a private IP endpoint,
	we need to comprise a set of services so that SNS can callback to a public notifier gateway endpoint (likely behind ELB),
	and then such notifier gateway routes the callback data to the notifier server, where notifier server is connected to the gRPC clients via server stream,
	ultimately allowing the gRPC clients receive the callback that would normally not be possible since it lives within private IP scope
4) notifierServer is a gRPC service host, likely one or more running on EC2, or ECS, or other containerized deployment strategy within the aws vpc region
5) notifierServer can be either public or private, depending on target gRPC client needs, notifierServer configuration always is defined with its upstream gateway url
6) notifierServer exposes gRPC services so that it enables gRPC clients to Subscribe or Unsubscribe intended SNS topic,
	so that when callback occurs, such callback data is routed to the gRPC client
7) notifierServer also has the ability to broadcast data to all its connected gRPC clients, when such broadcast request is received by the notifierServer itself
8) notifierServer uses aws DynamoDB as its backend data store, so that it can store its own server url info to the data store, for Notifier Gateway to discover via Server Key,
	note, Server Key is submitted during SNS Topic subscription, so during callback by SNS, the server key is returned in path,
	so that using such server key, we can find the server url from DynamoDB data store
9) topology of the notification system is:
	a) gRPC Clients, uses gRPC Notifier Client wrapper, to facilitate the notification service participation; gRPC Clients can be private or public IP scope
	b) gRPC Notifier Client wrapper, connects to one of, gRPC Notifier Server, where gRPC Notifier Server endpoints discovered via service discovery;
	c) gRPC Notifier Server, provides SNS Topic Subscribe / Unsubscribe services, to gRPC Notifier Client wrapper; gRPC Notifier Server can be private or public IP scope
	d) gRPC Notifier Server, provides Data Broadcast to connected gRPC Notifier Clients, as received from gRPC Notifier Gateway from time to time;
	e) gRPC Notifier Server, automatically persists its own server url into DynamoDB during its lifecycle, so that Notifier Gateway can easily discover its url based on Server Key;
	f) gRPC Notifier Server, when performing Subscribe to SNS Topic, uses the host defined Notifier Gateway URL;
	g) gRPC Notifier Gateway, a REST API service, hosted under ELB on Public IP scope, acts as the router between SNS HTTP callback and the gRPC Notifier Server network;
	h) SNS Topic HTTP Callback, will always be registered (subscribed) for callback to the gRPC Notifier Gateway;
	i) where the SNS Topic HTTP Callback, will call gRPC Notifier Gateway REST API service, and upon it receiving the call, delegate to the gRPC Notifier Server for downstream push;
	j) note that gRPC Notifier Server exposes a self hosted REST API endpoint for /snsrelay, and a pre-defined snsNotification struct;
	k) so that gRPC Notifier Gateway has a standard method of invoking the routing process;
	l) in essence, gRPC Client to Notifier Server is pure gRPC,
		while Notifier Server also exposes HTTP REST API, so that Notifier Gateway can trigger when needed during callback,
		where SNS Callback is always HTTP invocation to the Notifier Gateway REST API path.
*/

import (
	"context"
	"fmt"
	"log"
	"time"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginbindtype"
	"github.com/aldelo/common/wrapper/gin/ginhttpmethod"
	"github.com/aldelo/connector/notifierserver/impl"
	pb "github.com/aldelo/connector/notifierserver/proto"
	"github.com/aldelo/connector/service"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

var notifierServer *impl.NotifierImpl

type snsNotification struct {
	Type             string `json:"Type"`
	MessageId        string `json:"MessageId"`
	TopicArn         string `json:"TopicArn"`
	Subject          string `json:"Subject"`
	Message          string `json:"Message"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
	UnsubscribeURL   string `json:"UnsubscribeURL"`
}

// NewNotifierServer info,
// NOTE: notifier server grpc config should not favor public ip when deploy to aws, otherwise EC2 security groups cannot be used for inbound security permissions,
//
//	if notifier server is discovered via public ip, then inbound security group must be setup to indicate inbound ec2 ip address rather than its security groups
func NewNotifierServer(appName string, configFileNameGrpcServer string, configFileNameWebServer string, configFileNameNotifier string, customConfigPath string) (*service.Service, error) {
	notifierServer = new(impl.NotifierImpl)

	if err := notifierServer.ReadConfig(appName, configFileNameNotifier, customConfigPath); err != nil {
		return nil, fmt.Errorf("WARNING: Notifier-Config.yaml Not Ready: %s", err)
	} else {
		if util.LenTrim(notifierServer.ConfigData.NotifierServerData.ServerKey) == 0 {
			notifierServer.ConfigData.SetServerKey(util.NewULID())

			if err := notifierServer.ConfigData.Save(); err != nil {
				return nil, fmt.Errorf("ERROR: Persist Notifier Server Key Failed: %s", err)
			}
		}
	}

	if err := notifierServer.ConnectDataStore(); err != nil {
		return nil, fmt.Errorf("WARNING: Notifier's DynamoDB Connection Failed: %s", err)
	}

	if err := notifierServer.ConnectSNS(awsregion.GetAwsRegion(notifierServer.ConfigData.NotifierServerData.SnsAwsRegion)); err != nil {
		return nil, fmt.Errorf("WARNING: Notifier's SNS Connection Failed: %s", err)
	}

	svr := service.NewService(appName, configFileNameGrpcServer, customConfigPath, func(grpcServer *grpc.Server) {
		pb.RegisterNotifierServiceServer(grpcServer, notifierServer)
	})

	svr.WebServerConfig = &service.WebServerConfig{
		AppName:          appName,
		ConfigFileName:   configFileNameWebServer,
		CustomConfigPath: "",
		WebServerRoutes: map[string]*ginw.RouteDefinition{
			"base": {
				Routes: []*ginw.Route{
					{
						RelativePath:    "/snsrelay",
						Method:          ginhttpmethod.POST,
						Binding:         ginbindtype.BindJson,
						BindingInputPtr: &snsNotification{},
						Handler:         snsrelay,
					},
					{
						RelativePath:    "/snsupdate/:topicArn",
						Method:          ginhttpmethod.POST,
						Binding:         ginbindtype.UNKNOWN,
						BindingInputPtr: nil,
						Handler:         snsupdate,
					},
				},
				CorsMiddleware: &cors.Config{},
			},
		},
	}

	notifierServer.WebServerLocalAddressFunc = svr.WebServerConfig.GetWebServerLocalAddress

	// clean up prior sns subscriptions logged in config, upon initial launch
	notifierServer.UnsubscribeAllPriorSNSTopics()

	return svr, nil
}

// UnsubscribeAllTopics will clean up by unsubscribing all subscriptionArns from Topic list in config,
// this is call during notifier service shutdown to clean up
func UnsubscribeAllTopics() {
	if notifierServer != nil {
		log.Println("UnsubscribeAllTopics Invoked")
		notifierServer.UnsubscribeAllPriorSNSTopics()
	} else {
		log.Println("UnsubscribeAllTopics Not Invoked Because notifierServer Object is Nil")
	}
}

func snsrelay(c *gin.Context, bindingInputPtr interface{}) {
	if notifierServer == nil {
		c.String(412, "notifierServer Not Exist")
		return
	}

	n, ok := bindingInputPtr.(*snsNotification)

	if !ok {
		c.String(412, "Assert SNS Notification Json Failed")
	}

	log.Println("Notifier Server SNS Relay Started...")

	if util.LenTrim(n.Message) > 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			log.Println("~~~ Notifier Server Relaying SNS Message to TopicArn: " + n.TopicArn + " ~~~")

			if _, err := notifierServer.Broadcast(ctx, &pb.NotificationData{
				Id:        n.MessageId,
				Topic:     n.TopicArn,
				Message:   n.Message,
				Timestamp: n.Timestamp,
			}); err != nil {
				// broadcast error encountered
				log.Println("!!! Notifier Server Relay SNS Message Failed: " + err.Error() + " !!!")
			} else {
				log.Println("+++ Notifier Server Relay SNS Message Success +++")
			}
		}()
	} else {
		log.Println(">>> Notifier Server Skipped SNS Relay Because Message is Blank <<<")
	}

	log.Println("... Notifier Server SNS Relay Completed")

	c.String(200, "SNS Relay Sent")
}

func snsupdate(c *gin.Context, bindingInputPtr interface{}) {
	if notifierServer == nil {
		c.String(412, "notifierServer Not Exist")
		return
	}

	topicArn := c.Param("topicArn")

	if util.LenTrim(topicArn) == 0 {
		c.String(412, "SNS Update Requires TopicArn")
		return
	}

	if c.Request == nil {
		c.String(412, "SNS Update Requires Http Request Not Nil")
		return
	}

	if c.Request.Body == nil {
		c.String(412, "SNS Update Requires Http Request Body Not Nil")
		return
	}

	buf := make([]byte, 1024)
	num, _ := c.Request.Body.Read(buf)
	subscriptionArn := string(buf[0:num])

	if util.LenTrim(subscriptionArn) == 0 {
		c.String(412, "SNS Update Requires SubscriptionArn in Http Request Body")
		return
	}

	if err := notifierServer.UpdateSubscriptionArnToTopic(topicArn, subscriptionArn); err != nil {
		c.String(412, err.Error())
	} else {
		c.Status(200)
	}
}
