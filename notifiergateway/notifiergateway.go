package notifiergateway

import (
	"fmt"
	util "github.com/aldelo/common"
	"github.com/aldelo/common/crypto"
	"github.com/aldelo/common/rest"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginbindtype"
	"github.com/aldelo/common/wrapper/gin/ginhttpmethod"
	"github.com/aldelo/connector/notifiergateway/config"
	"github.com/aldelo/connector/notifiergateway/model"
	"github.com/aldelo/connector/webserver"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"log"
	"strings"
	"time"
)

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

/*
=== PURPOSE ===
1) topology of the notification system is:
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

type confirmation struct {
	Type string 			`json:"Type"`
	MessageId string		`json:"MessageId"`
	Token string			`json:"Token"`
	TopicArn string			`json:"TopicArn"`
	Message string			`json:"Message"`
	SubscribeURL string		`json:"SubscribeURL"`
	Timestamp string		`json:"Timestamp"`
	SignatureVersion string	`json:"SignatureVersion"`
	Signature string		`json:"Signature"`
	SigningCertURL string	`json:"SigningCertURL"`
}

type notification struct {
	Type string 			`json:"Type"`
	MessageId string		`json:"MessageId"`
	TopicArn string			`json:"TopicArn"`
	Subject string			`json:"Subject"`
	Message string			`json:"Message"`
	Timestamp string		`json:"Timestamp"`
	SignatureVersion string	`json:"SignatureVersion"`
	Signature string		`json:"Signature"`
	SigningCertURL string	`json:"SigningCertURL"`
	UnsubscribeURL string	`json:"UnsubscribeURL"`
}

// NewNotifierGateway constructs a new web server object for use as the notifier gateway host
func NewNotifierGateway(appName string, configFileNameWebServer string, configFileNameGateway string, customConfigPath string) (*webserver.WebServer, error) {
	// read gateway config data
	cfg := &config.Config{
		AppName: appName,
		ConfigFileName: configFileNameGateway,
		CustomConfigPath: customConfigPath,
	}

	if err := cfg.Read(); err != nil {
		return nil, fmt.Errorf("Read Notifier Gateway Config Failed: %s", err.Error())
	}

	// establish data store connection
	if err := model.ConnectDataStore(cfg); err != nil {
		return nil, fmt.Errorf("Connect Notifier Gateway Data Store Failed: %s", err.Error())
	} else {
		model.DynamoDBActionRetryAttempts = cfg.NotifierGatewayData.DynamoDBActionRetries
		model.GatewayKey = cfg.NotifierGatewayData.GatewayKey
	}

	// setup gateway server
	gatewayServer := webserver.NewWebServer(appName, configFileNameWebServer, customConfigPath)

	gatewayServer.Routes = map[string]*ginw.RouteDefinition{
		"base": {
			Routes: []*ginw.Route{
				{
					RelativePath: "/snsrouter/:serverKey",
					Method: ginhttpmethod.POST,
					Binding: ginbindtype.UNKNOWN,
					BindingInputPtr: nil,
					Handler: snsrouter,
				},
				{
					RelativePath: "/callerid",
					Method: ginhttpmethod.GET,
					BindingInputPtr: nil,
					Handler: callerid,
				},
			},
			CorsMiddleware: &cors.Config{},
		},
	}

	// return gateway server to caller
	return gatewayServer, nil
}

// callerid returns the public ip of the caller, after security validation;
// security validation is via header element "x-nts-gateway-token";
// x-nts-gateway-token = takes the current date in utc, in yyyy-mm-dd format, as sha256 hash data, and hash secret as salt,
// if the hash matches to the caller, then method is performed, otherwise 412 is returned
func callerid(c *gin.Context, bindingInputPtr interface{}) {
	// validate gin context input
	if c == nil {
		return
	}

	if util.LenTrim(model.GatewayKey) == 0 {
		c.String(500, "Internal Server Config Error, Missing nts-gateway Key")
		return
	}

	// validate inbound request
	if token := c.GetHeader("x-nts-gateway-token"); util.LenTrim(token) > 0 {
		key := crypto.Sha256(util.FormatDate(time.Now().UTC()), model.GatewayKey)

		if token == key {
			// match
			c.String(200, c.ClientIP())
		} else {
			// not match
			c.String(404, "Request Not Valid, No Match")
		}
	} else {
		// validation failed
		c.String(404, "Request Not Valid, Key Missing")
	}
}

// snsrouter is a gin handler that processes inbound sns payload for either confirmation or notification callbacks
// note: bindingInputPtr is not used by this method, please ignore in code
func snsrouter(c *gin.Context, bindingInputPtr interface{}) {
	// validate gin context input
	if c == nil {
		return
	}

	// handle confirmation or notification according to header
	switch strings.ToUpper(c.GetHeader("x-amz-sns-message-type")) {
	case "SUBSCRIPTIONCONFIRMATION":
		// subscription confirmation
		snsconfirmation(c, c.Param("serverKey"))
		return

	case "NOTIFICATION":
		// sns notification
		snsnotification(c, c.Param("serverKey"))
		return

	default:
		// not sns message type, ignore call
		c.Status(404)
		return
	}
}

// snsconfirmation handles sns confirmation response
func snsconfirmation(c *gin.Context, serverKey string) {
	// validate gin context input
	if c == nil {
		return
	}

	// bind body payload
	confirm := &confirmation{}

	if err := c.BindJSON(confirm); err != nil {
		c.String(412, "Expected Confirmation Payload")
	} else {
		// auto confirm
		if url := confirm.SubscribeURL; util.LenTrim(url) > 0 {
			// validate serverKey
			if util.LenTrim(serverKey) <= 0 {
				c.String(412, "Server Key is Required")
				return
			}

			if serverUrl, err := model.GetServerRouteFromDataStore(serverKey, model.DynamoDBActionRetryAttempts); err != nil {
				log.Printf("Server Key %s Lookup Failed: (IP Source: %s) %s\n", serverKey, c.ClientIP(), err.Error())
				c.String(412, "Server Key Not Valid")
				return
			} else if util.LenTrim(serverUrl) == 0 {
				log.Printf("Server Key %s Not Found in DDB: (IP Source: %s) %s\n", serverKey, c.ClientIP(), "ServerUrl Returned is Blank")
				c.String(412, "Server Key Not Exist")
				return
			}

			// perform confirmation action
			if status, _, err := rest.GET(url, nil); err != nil {
				c.String(412, "Subscription Confirm Callback Failed: %s", err.Error())
			} else if status != 200 {
				c.String(status, "Subscription Confirm Callback Status Code: %d", status)
			} else {
				// confirm success, with sns endpoint
				c.Status(200)
			}
		} else {
			c.String(412, "Expected Subscription Confirm URL")
		}
	}
}

// buildConfirmationSigningPayload unescapes and builds subscription confirmation payload, to be ready for signing
func buildConfirmationSigningPayload(confirm *confirmation) string {
	if confirm != nil {
		buf := "Message\n"
		buf += confirm.Message + "\n"
		buf += "MessageId\n"
		buf += confirm.MessageId + "\n"
		buf += "SubscribeURL\n"
		buf += confirm.SubscribeURL + "\n"
		buf += "Timestamp\n"
		buf += confirm.Timestamp + "\n"
		buf += "Token\n"
		buf += confirm.Token + "\n"
		buf += "TopicArn\n"
		buf += confirm.TopicArn + "\n"
		buf += "Type\n"
		buf += confirm.Type + "\n"

		return buf
	} else {
		return ""
	}
}

// snsnotification handles sns notification response
func snsnotification(c *gin.Context, serverKey string) {
	// validate gin context input
	if c == nil {
		return
	}

	// validate serverKey
	if util.LenTrim(serverKey) <= 0 {
		c.String(412, "Server Key is Required")
		return
	}

	// bind body payload
	notify := &notification{}

	if err := c.BindJSON(notify); err != nil {
		c.String(412, "Expected Notification Payload: %s", err.Error())
	} else {
		// valid notification data
		if util.LenTrim(notify.MessageId) == 0 {
			c.String(412, "Expected Notification Message ID")
			return
		}

		if util.LenTrim(notify.TopicArn) == 0 {
			c.String(412, "Expected Notification Topic ARN")
			return
		}

		if util.LenTrim(notify.Message) == 0 {
			c.String(412, "Expected Notification Message")
			return
		}

		if util.LenTrim(notify.Timestamp) == 0 {
			c.String(412, "Expected Notification Timestamp")
			return
		}

		// validate serverKey
		if serverUrl, err := model.GetServerRouteFromDataStore(serverKey, model.DynamoDBActionRetryAttempts); err != nil {
			log.Printf("Server Key %s Lookup Failed: (IP Source: %s) %s\n", serverKey, c.ClientIP(), err.Error())
			c.String(412, "Server Key Not Valid")
		} else if util.LenTrim(serverUrl) == 0 {
			log.Printf("Server Key %s Not Found in DDB: (IP Source: %s) %s\n", serverKey, c.ClientIP(), "ServerUrl Returned is Blank")
			c.String(412, "Server Key Not Exist")
		} else {
			// perform sns notification routing task
			// server url begins with http or https, then ip and port as the fully qualified url domain path (controller path not part of the url)
			if notifyJson, err := util.MarshalJSONCompact(notify); err != nil {
				log.Printf("Server Key %s at Host %s Marshal Notification Data to JSON Failed: %s", serverKey, serverUrl, err.Error())
				c.String(412, "Notification Marshal Failed")
			} else {
				// relay sns notification to target notify server
				if statusCode, _, err := rest.POST(serverUrl, []*rest.HeaderKeyValue{
					{
						Key: "Content-Type",
						Value: "application/json",
					},
				}, notifyJson); err != nil {
					// error
					log.Printf("Server Key %s at Host %s Route Notification From SNS to Internal Host Failed: %s", serverKey, serverUrl, err.Error())
					c.String(412, "Route Notification to Internal Host Failed")
				} else if statusCode != 200 {
					// not status 200
					log.Printf("Server Key %s at Host %s Route Notification From SNS to Internal Host Did Not Yield Status Code 200: Actual Code = %d", serverKey, serverUrl, statusCode)
					c.Status(statusCode)
				} else {
					// success, reply to sns success
					log.Printf("Server Key %s at Host %s Route Notification Complete", serverKey, serverUrl)
					c.Status(200)
				}
			}
		}
	}
}

// buildNotificationSigningPayload unescapes and builds notification payload, to be ready for signing
func buildNotificationSigningPayload(notify *notification) string {
	if notify != nil {
		buf := "Message\n"
		buf += notify.Message + "\n"
		buf += "MessageId\n"
		buf += notify.MessageId + "\n"
		if util.LenTrim(notify.Subject) > 0 {
			buf += "Subject\n"
			buf += notify.Subject + "\n"
		}
		buf += "Timestamp\n"
		buf += notify.Timestamp + "\n"
		buf += "TopicArn\n"
		buf += notify.TopicArn + "\n"
		buf += "Type\n"
		buf += notify.Type + "\n"

		return buf
	} else {
		return ""
	}
}












