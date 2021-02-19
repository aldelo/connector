package notifiergateway

import (
	"fmt"
	util "github.com/aldelo/common"
	"github.com/aldelo/common/crypto"
	"github.com/aldelo/common/rest"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/cloudmap"
	"github.com/aldelo/common/wrapper/dynamodb"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginbindtype"
	"github.com/aldelo/common/wrapper/gin/ginhttpmethod"
	"github.com/aldelo/connector/adapters/registry"
	"github.com/aldelo/connector/adapters/registry/sdoperationstatus"
	"github.com/aldelo/connector/notifiergateway/config"
	"github.com/aldelo/connector/notifiergateway/model"
	"github.com/aldelo/connector/webserver"
	"github.com/aldelo/common/wrapper/xray"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"log"
	"strconv"
	"strings"
	"time"
)

/*
 * Copyright 2020-2021 Aldelo, LP
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

type healthreport struct {
	NamespaceId string		`json:"NamespaceId"`
	ServiceId string		`json:"ServiceId"`
	InstanceId string		`json:"InstanceId"`
	AwsRegion string		`json:"AWSRegion"`
	ServiceInfo string		`json:"ServiceInfo"`
	HostInfo string			`json:"HostInfo"`
	HashKeyName string		`json:"HashKeyName"`
	HashSignature string	`json:"HashSignature"`
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

		model.HashKeys = make(map[string]string)

		for _, v := range cfg.NotifierGatewayData.HashKeys {
			model.HashKeys[v.HashKeyName] = v.HashKeySecret
		}
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
				{
					RelativePath: "/reporthealth",
					Method: ginhttpmethod.POST,
					Binding: ginbindtype.BindJson,
					BindingInputPtr: &healthreport{},
					Handler: healthreporter,
				},
				{
					RelativePath: "/reporthealth/:instanceid",
					Method: ginhttpmethod.DELETE,
					Binding: ginbindtype.UNKNOWN,
					BindingInputPtr: nil,
					Handler: healthreporterdelete,
				},
			},
			CorsMiddleware: &cors.Config{},
		},
	}

	// return gateway server to caller
	return gatewayServer, nil
}

// healthreporter handles client reporting to host health status of the client,
// the intent is so that the constant client (grpc services) reporting enables the host to monitor health,
// in the even health is lapsed, host can decide to mark client as unhealthy or deregister accordingly;
// security validation is via sha256 hash signature within healthreport struct field values (other than aws region, hash key name, and hash signature itself),
// the hashing source value is also combined with the current date in UTC formatted in yyyy-mm-dd,
// hash value is comprised of namespaceid + serviceid + instanceid + current date in UTC in yyyy-mm-dd format;
// the sha256 hash salt uses named HashKey on both client and server side
func healthreporter(c *gin.Context, bindingInputPtr interface{}) {
	// validate gin context input
	if c == nil {
		return
	}

	data, ok := bindingInputPtr.(*healthreport)

	if !ok || data == nil {
		log.Println("!!! Inbound Health Report Payload Missing or Malformed From " + c.ClientIP() + " !!!")
		c.String(404, "Inbound Health Report Payload Missing or Malformed")
		return
	}

	if util.LenTrim(data.NamespaceId) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing NamespaceId !!!")
		c.String(404, "Inbound Health Report Payload Missing Required Value (Namespace)")
		return
	}

	if util.LenTrim(data.ServiceId) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing ServiceId !!!")
		c.String(404, "Inbound Health Report Payload Missing Required Value (Service)")
		return
	}

	if util.LenTrim(data.InstanceId) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing InstanceId !!!")
		c.String(404, "Inbound Health Report Payload Missing Required Value (Instance)")
		return
	}

	if util.LenTrim(data.AwsRegion) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing AWSRegion !!!")
		c.String(404, "Inbound Health Report Payload Missing Required Value (Region)")
		return
	}

	if util.LenTrim(data.ServiceInfo) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing ServiceInfo !!!")
		c.String(404, "Inbound Health Report Payload Missing Required Value (ServiceInfo)")
		return
	}

	if util.LenTrim(data.HostInfo) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing HostInfo !!!")
		c.String(404, "Inbound Health Report Payload Missing Required Value (HostInfo)")
		return
	}

	if len(model.HashKeys) == 0 {
		log.Println("!!! Internal Server Config Error, Missing Hash Keys !!!")
		c.String(500, "Internal Server Config Error, Missing Hash Keys")
		return
	}

	if hashSecret, hsFound := model.HashKeys[data.HashKeyName]; !hsFound || util.LenTrim(hashSecret) == 0 {
		log.Println("!!! Inbound Health Report Hash Key Name '" + data.HashKeyName + "' From " + c.ClientIP() + " Not Valid: Host Does Not Expect This Hash Key Name !!!")
		c.String(401, "Inbound Health Report Hash Key Name '" + data.HashKeyName + "' Not Valid")
		return
	} else {
		buf := data.NamespaceId + data.ServiceId + data.InstanceId + util.FormatDate(time.Now().UTC())
		bufHash := crypto.Sha256(buf, hashSecret)

		if bufHash != data.HashSignature {
			log.Println("!!! Inbound Health Report Hash Signature From " + c.ClientIP() + " Not Valid: Hash Signature Mismatch !!!")
			log.Println("... Host = Sha256 Source: " + buf + ", Salt: " + hashSecret + ", Signature: " + bufHash)
			log.Println("... Client = Sha256 Source: " + data.NamespaceId + data.ServiceId + data.InstanceId + " + Client Side Date, Salt: Client Side Hash Key Secret, Signature: " + data.HashSignature)
			c.String(401, "Inbound Health Report Hash Signature Not Valid")
			return
		}
	}

	if xray.XRayServiceOn() {
		trace := xray.NewSegmentFromHeader(c.Request)
		if !trace.Ready() {
			trace = xray.NewSegment("healthreporter-write")
		}
		defer trace.Close()

		trace.Capture("healthReporter.writeToDataStore", func() error {
			if e := model.SetInstanceHealthToDataStore(data.NamespaceId, data.ServiceId, data.InstanceId, data.AwsRegion, data.ServiceInfo, data.HostInfo); e != nil {
				log.Println("!!! Inbound Health Report (NamespaceID '" + data.NamespaceId + "', ServiceID '" + data.ServiceId + "', InstanceID '" + data.InstanceId + "') Persist to Data Store Failed: " + e.Error() + " !!!")
				c.String(500, "Persist Instance Status to Data Store Failed")
				return fmt.Errorf("Persist Instance Status to Data Store Failed: %s", e.Error())
			} else {
				log.Println("+++ Inbound Health Report (NamespaceID '" + data.NamespaceId + "', ServiceID '" + data.ServiceId + "', InstanceID '" + data.InstanceId + "') Persist to Data Store OK +++")
				c.String(200, "Persist Instance Status to Data Store OK")
				return nil
			}
		}, &xray.XTraceData{
			Meta: map[string]interface{}{
				"NamespaceId": data.NamespaceId,
				"ServiceId":   data.ServiceId,
				"InstanceId":  data.InstanceId,
				"AwsRegion":   data.AwsRegion,
				"ServiceInfo": data.ServiceInfo,
				"HostInfo":    data.HostInfo,
			},
		})
	} else {
		// store health report update to dynamodb
		if e := model.SetInstanceHealthToDataStore(data.NamespaceId, data.ServiceId, data.InstanceId, data.AwsRegion, data.ServiceInfo, data.HostInfo); e != nil {
			log.Println("!!! Inbound Health Report (NamespaceID '" + data.NamespaceId + "', ServiceID '" + data.ServiceId + "', InstanceID '" + data.InstanceId + "') Persist to Data Store Failed: " + e.Error() + " !!!")
			c.String(500, "Persist Instance Status to Data Store Failed")
		} else {
			log.Println("+++ Inbound Health Report (NamespaceID '" + data.NamespaceId + "', ServiceID '" + data.ServiceId + "', InstanceID '" + data.InstanceId + "') Persist to Data Store OK +++")
			c.String(200, "Persist Instance Status to Data Store OK")
		}
	}
}

// healthreporterdelete handles service client record health status delete,
// instanceid to delete is passed via path argument,
// header 'x-nts-gateway-hash-name' is required and contains the hash key name for validation
// header 'x-nts-gateway-hash-signature' is required and contains the hash signature for validation
//
// the hashing source value is also combined with the current date in UTC formatted in yyyy-mm-dd,
// hash value is instanceid + current date in UTC in yyyy-mm-dd format;
// the sha256 hash salt uses named HashKey on both client and server side
func healthreporterdelete(c *gin.Context, bindingInputPtr interface{}) {
	// validate gin context input
	if c == nil {
		return
	}

	instanceId := c.Param("instanceid")

	if util.LenTrim(instanceId) == 0 {
		log.Println("!!! Delete Health Report Service Record Failed: InstanceID Missing From Path !!!")
		c.String(404, "Delete Health Report Service Record Request Missing InstanceID")
		return
	}

	hashKeyName := c.GetHeader("x-nts-gateway-hash-name")

	if util.LenTrim(hashKeyName) == 0 {
		log.Println("!!! Delete Health Report Service Record Failed: Hash Key Name is Missing From Header 'x-nts-gateway-hash-name' !!!")
		c.String(404, "Delete Health Report Service Record Request Missing Key Name")
		return
	}

	hashKeySignature := c.GetHeader("x-nts-gateway-hash-signature")

	if util.LenTrim(hashKeySignature) == 0 {
		log.Println("!!! Delete Health Report Service Record Failed: Hash Signature is Missing From Header 'x-nts-gateway-hash-signature' !!!")
		c.String(404, "Delete Health Report Service Record Request Missing Signature")
		return
	}

	if len(model.HashKeys) == 0 {
		log.Println("!!! Internal Server Config Error, Missing Hash Keys !!!")
		c.String(500, "Internal Server Config Error, Missing Hash Keys")
		return
	}

	if hashSecret, hsFound := model.HashKeys[hashKeyName]; !hsFound || util.LenTrim(hashSecret) == 0 {
		log.Println("!!! Delete Health Report Service Record Request's Hash Key Name '" + hashKeyName + "' From " + c.ClientIP() + " Not Valid: Host Does Not Expect This Hash Key Name !!!")
		c.String(401, "Delete Health Report Service Record Request's Hash Key Name '" + hashKeyName + "' Not Valid")
		return
	} else {
		buf := instanceId + util.FormatDate(time.Now().UTC())
		bufHash := crypto.Sha256(buf, hashSecret)

		if bufHash != hashKeySignature {
			log.Println("!!! Delete Health Report Service Record Request's Hash Signature From " + c.ClientIP() + " Not Valid: Hash Signature Mismatch !!!")
			log.Println("... Host = Sha256 Source: " + buf + ", Salt: " + hashSecret + ", Signature: " + bufHash)
			log.Println("... Client = Sha256 Source: " + instanceId + " + Client Side Date, Salt: Client Side Hash Key Secret, Signature: " + hashKeySignature)
			c.String(401, "Delete Health Report Service Record Request's Hash Signature Not Valid")
			return
		}
	}

	// delete health report from to dynamodb
	pk := fmt.Sprintf("%s#%s#service#discovery#host#health", "corems", "all")
	sk := fmt.Sprintf("InstanceID^%s", instanceId)

	if xray.XRayServiceOn() {
		trace := xray.NewSegmentFromHeader(c.Request)
		if !trace.Ready() {
			trace = xray.NewSegment("healthreporter-delete")
		}
		defer trace.Close()

		trace.Capture("healthReporter.deleteFromDataStore", func() error {
			if _, e := model.DeleteInstanceHealthFromDataStore(&dynamodb.DynamoDBTableKeys{PK: pk, SK: sk}); e != nil {
				log.Println("!!! Delete Health Report Service Record For InstanceID '" + instanceId + "' From Data Store Failed: " + e.Error() + " !!!")
				c.String(500, "Delete Health Report Service Record From Data Store Failed")
				return fmt.Errorf("Delete Health Report Service Record From Data Store Failed: %s", e.Error())
			} else {
				log.Println("--- Delete Health Report Service Record For InstanceID '" + instanceId + "' From Data Store OK ---")
				c.String(200, "Delete Health Report Service Record From Data Store OK")
				return nil
			}
		}, &xray.XTraceData{
			Meta: map[string]interface{}{
				"PK": pk,
				"SK": sk,
			},
		})
	} else {
		if _, e := model.DeleteInstanceHealthFromDataStore(&dynamodb.DynamoDBTableKeys{PK: pk, SK: sk}); e != nil {
			log.Println("!!! Delete Health Report Service Record For InstanceID '" + instanceId + "' From Data Store Failed: " + e.Error() + " !!!")
			c.String(500, "Delete Health Report Service Record From Data Store Failed")
		} else {
			log.Println("--- Delete Health Report Service Record For InstanceID '" + instanceId + "' From Data Store OK ---")
			c.String(200, "Delete Health Report Service Record From Data Store OK")
		}
	}
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

	log.Println("/snsrouter Invoked: x-amz-sns-message-type = " + c.GetHeader("x-amz-sns-message-type") + ", serverKey = " + c.Param("serverKey"))

	// handle confirmation or notification according to header
	switch strings.ToUpper(c.GetHeader("x-amz-sns-message-type")) {
	case "SUBSCRIPTIONCONFIRMATION":
		// subscription confirmation
		log.Println("/snsrouter Received Header is 'subscriptionconfirmation'")
		snsconfirmation(c, c.Param("serverKey"))
		return

	case "UNSUBSCRIBECONFIRMATION":
		// unsubscribe confirmation
		log.Println("/snsrouter Received Header is 'unsubscribeconfirmation'")
		c.Status(200)
		return

	case "NOTIFICATION":
		// sns notification
		log.Println("/snsrouter Received Header is 'notification'")
		snsnotification(c, c.Param("serverKey"))
		return

	default:
		// not sns message type, ignore call
		log.Println("/snsrouter Received Header Not 'subscriptionconfirmation' or 'notification'")
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

	var seg *xray.XSegment
	if xray.XRayServiceOn() {
		seg = xray.NewSegmentFromHeader(c.Request)
		if !seg.Ready() {
			seg = xray.NewSegment("sns-confirmation")
		}

		defer seg.Close()
	}

	if err := c.BindJSON(confirm); err != nil {
		log.Println("/snsrouter 'subscriptionconfirmation' BindJSON Error: " + err.Error())
		c.String(412, "Expected Confirmation Payload")

		if seg != nil && seg.Ready() {
			_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' BindJSON Error: %s", err.Error()))
		}
	} else {
		// auto confirm
		if url := confirm.SubscribeURL; util.LenTrim(url) > 0 {
			log.Println("/snsrouter 'subscriptionconfirmation' SubscribeURL From SNS = " + url)

			// validate serverKey
			if util.LenTrim(serverKey) <= 0 {
				log.Println("/snsrouter 'subscriptionconfirmation' Missing serverKey From Invoker")
				c.String(412, "Server Key is Required")

				if seg != nil && seg.Ready() {
					_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' Missing serverKey From Invoker"))
				}

				return
			}

			log.Println("/snsrouter 'subscriptionconfirmation' serverKey From Invoker = " + serverKey)

			// wait 250ms for notifier server to update ddb with server endpoint info
			time.Sleep(250*time.Millisecond)

			serverEndpointUrl := ""

			for i := 0; i < 3; i++ {
				if serverUrl, err := model.GetServerRouteFromDataStore(serverKey); err != nil {
					log.Printf("Server Key %s Lookup Failed: (IP Source: %s) %s\n", serverKey, c.ClientIP(), err.Error())
					c.String(412, "Server Key Not Valid")

					if seg != nil && seg.Ready() {
						_ = seg.Seg.AddError(fmt.Errorf("Server Key %s Lookup Failed: (IP Source: %s) %s\n", serverKey, c.ClientIP(), err.Error()))
					}

					return
				} else if util.LenTrim(serverUrl) == 0 && i == 2 {
					log.Printf("Server Key %s Not Found in DDB: (IP Source: %s) %s\n", serverKey, c.ClientIP(), "ServerUrl Returned is Blank")
					c.String(412, "Server Key Not Exist")

					if seg != nil && seg.Ready() {
						_ = seg.Seg.AddError(fmt.Errorf("Server Key %s Not Found in DDB: (IP Source: %s) %s\n", serverKey, c.ClientIP(), "ServerUrl Returned is Blank"))
					}

					return
				} else if util.LenTrim(serverUrl) > 0 {
					serverEndpointUrl = serverUrl
					break
				} else {
					time.Sleep(250*time.Millisecond)
				}
			}

			log.Println("/snsrouter 'subscriptionconfirmation' GET " + url + "...")

			// perform confirmation action
			if status, body, err := rest.GET(url, nil); err != nil {
				log.Println("/snsrouter 'subscriptionconfirmation' GET Failed: " + err.Error())
				c.String(412, "Subscription Confirm Callback Failed: %s", err.Error())

				if seg != nil && seg.Ready() {
					_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' GET Failed: %s", err.Error()))
				}
			} else if status != 200 {
				log.Println("/snsrouter 'subscriptionconfirmation' GET Not Status 200: [" + util.Itoa(status) + "] Body: " + body)
				c.String(status, "Subscription Confirm Callback Status Code: %d", status)

				if seg != nil && seg.Ready() {
					_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' GET Not Status 200: [" + util.Itoa(status) + "] Body: %s", body))
				}
			} else {
				// confirm success, with sns endpoint
				buf := util.SplitString(body, "<SubscriptionArn>", -1)
				buf = util.SplitString(buf, "</SubscriptionArn>", 0)

				if util.Left(buf, 12) == "arn:aws:sns:" {
					// subscription arn received
					log.Println("/snsrouter 'subscriptionconfirmation' GET Status 200 with SubscriptionArn '" + buf + "': " + body)

					serverEndpointUrl += "/snsupdate"
					serverEndpointUrl += "/" + confirm.TopicArn

					if status, body, err := rest.POST(serverEndpointUrl, []*rest.HeaderKeyValue{}, buf); err != nil {
						log.Println("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Failed: (SNS Subscription Still Valid, This is Warning Only) " + err.Error())

						if seg != nil && seg.Ready() {
							_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Failed: (SNS Subscription Still Valid, This is Warning Only) %s", err.Error()))
						}
					} else if status != 200 {
						log.Println("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Not Receive Status 200: (SNS Subscription Still Valid, This is Warning Only) Info = " + body)

						if seg != nil && seg.Ready() {
							_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Not Receive Status 200: (SNS Subscription Still Valid, This is Warning Only) Info = %s", body))
						}
					} else {
						log.Println("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Success (Status 200)")
					}

					c.Status(200)
				} else {
					log.Println("/snsrouter 'subscriptionconfirmation' GET Status 200 But Response Did Not Include SubscriptionArn in XML From SNS: " + body)
					c.Status(412)

					if seg != nil && seg.Ready() {
						_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' GET Status 200 But Response Did Not Include SubscriptionArn in XML From SNS: %s", body))
					}
				}
			}
		} else {
			log.Println("/snsrouter 'subscriptionconfirmation' Missing Expected SubscriberURL From SNS")
			c.String(412, "Expected Subscription Confirm URL")

			if seg != nil && seg.Ready() {
				_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' Missing Expected SubscriberURL From SNS"))
			}
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
		log.Println("/snsrouter 'notification' Missing serverKey From Invoker")
		c.String(412, "Server Key is Required")
		return
	}

	// bind body payload
	notify := &notification{}

	var seg *xray.XSegment
	if xray.XRayServiceOn() {
		seg = xray.NewSegmentFromHeader(c.Request)
		if !seg.Ready() {
			seg = xray.NewSegment("sns-notification")
		}

		defer seg.Close()
	}

	if err := c.BindJSON(notify); err != nil {
		log.Println("/snsrouter 'notification' BindJSON Error: " + err.Error())
		c.String(412, "Expected Notification Payload: %s", err.Error())

		if seg != nil && seg.Ready() {
			_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'notification' BindJSON Error: %s", err.Error()))
		}
	} else {
		// valid notification data
		if util.LenTrim(notify.MessageId) == 0 {
			log.Println("/snsrouter 'notification' SNS Data Missing MessageId")
			c.String(412, "Expected Notification Message ID")

			if seg != nil && seg.Ready() {
				_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'notification' SNS Data Missing MessageId"))
			}

			return
		}

		if util.LenTrim(notify.TopicArn) == 0 {
			log.Println("/snsrouter 'notification' SNS Data Missing TopicArn")
			c.String(412, "Expected Notification Topic ARN")

			if seg != nil && seg.Ready() {
				_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'notification' SNS Data Missing TopicArn"))
			}

			return
		}

		if util.LenTrim(notify.Message) == 0 {
			log.Println("/snsrouter 'notification' SNS Data Missing Message")
			c.String(412, "Expected Notification Message")

			if seg != nil && seg.Ready() {
				_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'notification' SNS Data Missing Message"))
			}

			return
		}

		if util.LenTrim(notify.Timestamp) == 0 {
			log.Println("/snsrouter 'notification' SNS Data Missing Timestamp")
			c.String(412, "Expected Notification Timestamp")

			if seg != nil && seg.Ready() {
				_ = seg.Seg.AddError(fmt.Errorf("/snsrouter 'notification' SNS Data Missing Timestamp"))
			}

			return
		}

		// validate serverKey
		if serverUrl, err := model.GetServerRouteFromDataStore(serverKey); err != nil {
			log.Printf("Server Key %s Lookup Failed: (IP Source: %s) %s\n", serverKey, c.ClientIP(), err.Error())
			c.String(412, "Server Key Not Valid")

			if seg != nil && seg.Ready() {
				_ = seg.Seg.AddError(fmt.Errorf("Server Key %s Lookup Failed: (IP Source: %s) %s\n", serverKey, c.ClientIP(), err.Error()))
			}
		} else if util.LenTrim(serverUrl) == 0 {
			log.Printf("Server Key %s Not Found in DDB: (IP Source: %s) %s\n", serverKey, c.ClientIP(), "ServerUrl Returned is Blank")
			unsubscribeSNS(notify)
			c.String(412, "Server Key Not Exist")

			if seg != nil && seg.Ready() {
				_ = seg.Seg.AddError(fmt.Errorf("Server Key %s Not Found in DDB: (IP Source: %s) %s\n", serverKey, c.ClientIP(), "ServerUrl Returned is Blank"))
			}
		} else {
			// perform sns notification routing task
			// server url begins with http or https, then ip and port as the fully qualified url domain path (controller path not part of the url)
			if notifyJson, err := util.MarshalJSONCompact(notify); err != nil {
				log.Printf("Server Key %s at Host %s Marshal Notification Data to JSON Failed: %s", serverKey, serverUrl, err.Error())
				c.String(412, "Notification Marshal Failed")

				if seg != nil && seg.Ready() {
					_ = seg.Seg.AddError(fmt.Errorf("Server Key %s at Host %s Marshal Notification Data to JSON Failed: %s", serverKey, serverUrl, err.Error()))
				}
			} else {
				// relay sns notification to target notify server
				log.Println("/snsrouter 'notification' Relaying SNS Data To Notifier Server Endpoint '" + serverUrl + "': " + notifyJson)

				serverUrl += "/snsrelay"

				if statusCode, _, err := rest.POST(serverUrl, []*rest.HeaderKeyValue{
					{
						Key: "Content-Type",
						Value: "application/json",
					},
				}, notifyJson); err != nil {
					// error
					log.Printf("Server Key %s at Host %s Route Notification From SNS to Internal Host Failed: %s", serverKey, serverUrl, err.Error())
					c.String(412, "Route Notification to Internal Host Failed")

					if seg != nil && seg.Ready() {
						_ = seg.Seg.AddError(fmt.Errorf("Server Key %s at Host %s Route Notification From SNS to Internal Host Failed: %s", serverKey, serverUrl, err.Error()))
					}
				} else if statusCode != 200 {
					// not status 200
					log.Printf("Server Key %s at Host %s Route Notification From SNS to Internal Host Did Not Yield Status Code 200: Actual Code = %d", serverKey, serverUrl, statusCode)
					c.Status(statusCode)

					if seg != nil && seg.Ready() {
						_ = seg.Seg.AddError(fmt.Errorf("Server Key %s at Host %s Route Notification From SNS to Internal Host Did Not Yield Status Code 200: Actual Code = %d", serverKey, serverUrl, statusCode))
					}
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

// unsubscribe sns from notification payload UnsubscribeURL path
func unsubscribeSNS(notify *notification) {
	if notify == nil {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic Aborted: Notification Object Parsed from SNS is Nil !!!")
		return
	}

	topicArn := notify.TopicArn
	unsubUrl := notify.UnsubscribeURL

	if util.LenTrim(topicArn) == 0 {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic Aborted: TopicArn Not Set in Notification Object Parsed from SNS !!!")
		return
	}

	if util.LenTrim(unsubUrl) == 0 {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArn + "' Aborted: UnsubscribeURL Not Set in Notification Object Parsed from SNS !!!")
		return
	}

	if strings.ToUpper(util.Left(unsubUrl, 8)) != "HTTPS://" {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArn + "' From UnsubscribeURL '" + unsubUrl + "' Aborted: UnsubscribeURL is Not Https or Malformed !!!")
		return
	}

	// unescape \u0026 to &
	// unescape \" to "
	unsubUrl = strings.Replace(unsubUrl, `\u0026`, `&`, -1)
	unsubUrl = strings.Replace(unsubUrl, `\"`, `"`, -1)
	unsubUrl = strings.Replace(unsubUrl, `"`, "", -1)

	// call HTTP GET to unsubscribe
	if status, body, err := rest.GET(unsubUrl, []*rest.HeaderKeyValue{}); err != nil {
		// error when GET
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArn + "' Failed for UnsubscribeURL '" + unsubUrl + "': (HTTP GET Error) " + err.Error() + " !!!")
	} else if status != 200 {
		// not status 200
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArn + "' Failed for UnsubscribeURL '" + unsubUrl + "': (Status " + util.Itoa(status) + ") " + body + " !!!")
	} else {
		// unsubscribe success
		log.Println("$$$ Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArn + "' Successful for UnsubscribeURL '" + unsubUrl + "': " + body + " $$$")
	}
}

// =====================================================================================================================
// instance health clean up helper
// =====================================================================================================================

var sdMap map[string]*cloudmap.CloudMap

// RunStaleHealthReportRecordsRemoverService starts a go-routine and runs loop to continuously clean up stale records
func RunStaleHealthReportRecordsRemoverService(stopService chan bool) {
	freq := model.HealthReportCleanUpFrequencySeconds

	if freq == 0 {
		freq = 120
	} else if freq < 30 {
		freq = 30
	} else if freq > 3600 {
		freq = 3600
	}

	go func() {
		for {
			select {
			case <-stopService:
				log.Println("### Stopping Stale Health Report Record Remover Service ###")
				return

			default:
				log.Println(">>> Stale Health Report Record Remover - Processing Invoked <<<")
				removeInactiveInstancesFromServiceDiscovery()
				time.Sleep(time.Duration(freq)*time.Second)
			}
		}
	}()
}

// removeInactiveInstancesFromServiceDiscovery will query dynamodb table used for instance host health monitor,
// if greater than 15 minutes of no keep-alive timestamp update, gather for cloudmap service discovery de-register,
// then remove from dynamodb table upon de-register success,
//
// (this function is to be run from goroutine in for loop so it continuously checks for removal needs,
//  suggest 60 second wait before re-invoke this function from for loop)
func removeInactiveInstancesFromServiceDiscovery() {
	if items, e := model.ListInactiveInstancesFromDataStore(); e != nil {
		// error encountered
		log.Println("!!! Remove Health Report Stale Records Failed: " + e.Error() + " !!!")
	} else if len(items) == 0 {
		// no items to remove
		log.Println("~~~ Health Report Stale Record Clean Up: 0 Found, All OK ~~~")
	} else {
		// has items to remove
		keys := []*dynamodb.DynamoDBTableKeys{}
		pk := fmt.Sprintf("%s#%s#service#discovery#host#health", "corems", "all")

		for _, v := range items {
			if v != nil {
				if e := sdDeregisterInstance(v.NamespaceId, v.ServiceId, v.InstanceId, v.AwsRegion); e == nil {
					// deregister success, queue instance for delete from data store
					keys = append(keys, &dynamodb.DynamoDBTableKeys{
						PK: pk,
						SK: fmt.Sprintf("InstanceID^%s", v.InstanceId),
					})
				}
			}
		}

		if len(keys) > 0 {
			// delete keys from data store
			var deleteFailKeys []*dynamodb.DynamoDBTableKeys

			if deleteFailKeys, e = model.DeleteInstanceHealthFromDataStore(keys...); e != nil {
				// all delete from data store failed
				log.Println("!!! Health Report Stale Record Clean Up: Service Discovery De-Register OK, But Delete Instance From Data Store Failed, " + e.Error() + " !!!")

			} else if len(deleteFailKeys) > 0 {
				// some delete failed
				log.Println("@@@ Health Report Stale Record Clean Up: Service Discovery De-Register OK, But " + util.Itoa(len(deleteFailKeys)) + " Instances Fail To Delete From Data Store @@@")

			} else {
				// delete success
				log.Println("~~~ Health Report Stale Record Clean Up: OK ~~~")
			}
		} else {
			log.Println("~~~ Health Report Stale Record Clean Up: Zero Instance De-Registered From CloudMap ~~~")
		}
	}
}

// sdDeregisterInstance handles de-register instance action, along with creating and using sd object for various namespaces
func sdDeregisterInstance(namespaceId string, serviceId string, instanceId string, awsRegion string) error {
	if util.LenTrim(namespaceId) == 0 {
		return nil
	}

	if util.LenTrim(serviceId) == 0 {
		return nil
	}

	if util.LenTrim(instanceId) == 0 {
		return nil
	}

	if util.LenTrim(awsRegion) == 0 {
		return nil
	}

	if sdMap == nil {
		sdMap = make(map[string]*cloudmap.CloudMap)
	}

	var e error
	sd, ok := sdMap[namespaceId+awsRegion]

	if !ok {
		// create new sd
		if sd, e = connectSd(namespaceId, awsRegion); e != nil {
			return fmt.Errorf("Service Discovery De-Register Instance Failed: (Connect Service Discovery Object Error) %s", e.Error())
		} else {
			sdMap[namespaceId+awsRegion] = sd
		}
	}

	// de-register
	if e = deregisterInstance(sd, serviceId, instanceId); e != nil {
		return fmt.Errorf("Service Discovery De-Register Instance Failed: (Invoke De-Register Error) %s", e.Error())
	}

	// success
	return nil
}

// connectSd will try to establish service discovery object to struct (used by sdDeregisterInstance)
func connectSd(namespaceId string, awsRegionName string) (sd *cloudmap.CloudMap, err error) {
	if util.LenTrim(namespaceId) == 0 {
		return nil, fmt.Errorf("NamespaceID is Required for Service Discovery")
	}

	if util.LenTrim(awsRegionName) == 0 {
		return nil, fmt.Errorf("AWSRegion is Required for Service Discovery")
	}

	sd = &cloudmap.CloudMap{
		AwsRegion: awsregion.GetAwsRegion(awsRegionName),
	}

	if err := sd.Connect(); err != nil {
		return nil, fmt.Errorf("Connect SD for NamespaceID '%s' Failed: %s", namespaceId, err.Error())
	} else {
		return sd, nil
	}
}

// deregisterInstance will remove instance from cloudmap and route 53 (used by sdDeregisterInstance)
func deregisterInstance(sd *cloudmap.CloudMap, serviceId string, instanceId string) error {
	if sd == nil {
		return fmt.Errorf("Service Discovery Object is Required for Instance Deregister")
	}

	if util.LenTrim(serviceId) == 0 {
		return fmt.Errorf("ServiceID is Required for Instance Deregister")
	}

	if util.LenTrim(instanceId) == 0 {
		return fmt.Errorf("InstanceID is Required for Instance Deregister")
	}

	log.Println("Service Discovery De-Register Instance '" + instanceId + "' Begin...")

	timeoutDuration := time.Duration(model.ServiceDiscoveryTimeoutSeconds)*time.Second

	if timeoutDuration < 5*time.Second {
		timeoutDuration = 5*time.Second
	}

	if operationId, err := registry.DeregisterInstance(sd, instanceId, serviceId, timeoutDuration); err != nil {
		log.Println("!!! Service Discovery De-Register Instance '" + instanceId + "' Failed: (Initial Deregister Action) " + err.Error() + " !!!")
		return fmt.Errorf("Service Discovery De-Register Instance '" + instanceId + "' Fail: " + err.Error())
	} else {
		tryCount := 0

		time.Sleep(250*time.Millisecond)

		for {
			if status, e := registry.GetOperationStatus(sd, operationId, timeoutDuration); e != nil {
				log.Println("!!! Service Discovery De-Register Instance '" + instanceId + "' Failed: (Deregister GetOperationStatus Action) " + e.Error() + " !!!")
				return fmt.Errorf("Service Discovery De-Register Instance '" + instanceId + "' Fail: " + e.Error())
			} else {
				if status == sdoperationstatus.Success {
					log.Println("$$$ Service Discovery De-Register Instance '" + instanceId + "' OK $$$")
					return nil
				} else {
					// wait 250 ms then retry, up until 20 counts of 250 ms (5 seconds)
					if tryCount < 20 {
						tryCount++
						log.Println("... Checking De-Register Instance '" + instanceId + "' Completion Status, Attempt " + strconv.Itoa(tryCount) + " (250ms)")
						time.Sleep(250*time.Millisecond)
					} else {
						log.Println("... De-Register Instance '" + instanceId + "' Failed: Operation Timeout After 5 Seconds")
						return fmt.Errorf("Service Discovery De-register Instance '" + instanceId + "' Fail When Operation Timed Out After 5 Seconds")
					}
				}
			}
		}
	}
}











