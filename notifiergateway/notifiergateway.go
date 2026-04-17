package notifiergateway

import (
	"crypto/subtle"
	"fmt"
	"log"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/crypto"
	"github.com/aldelo/common/rest"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/cloudmap"
	"github.com/aldelo/common/wrapper/dynamodb"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginbindtype"
	"github.com/aldelo/common/wrapper/gin/ginhttpmethod"
	"github.com/aldelo/common/wrapper/xray"
	"github.com/aldelo/connector/adapters/registry"
	"github.com/aldelo/connector/adapters/registry/sdoperationstatus"
	"github.com/aldelo/connector/notifiergateway/config"
	"github.com/aldelo/connector/notifiergateway/model"
	"github.com/aldelo/connector/webserver"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

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
	Type             string `json:"Type"`
	MessageId        string `json:"MessageId"`
	Token            string `json:"Token"`
	TopicArn         string `json:"TopicArn"`
	Message          string `json:"Message"`
	SubscribeURL     string `json:"SubscribeURL"`
	Timestamp        string `json:"Timestamp"`
	SignatureVersion string `json:"SignatureVersion"`
	Signature        string `json:"Signature"`
	SigningCertURL   string `json:"SigningCertURL"`
}

type notification struct {
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

type healthreport struct {
	NamespaceId   string `json:"NamespaceId"`
	ServiceId     string `json:"ServiceId"`
	InstanceId    string `json:"InstanceId"`
	AwsRegion     string `json:"AWSRegion"`
	ServiceInfo   string `json:"ServiceInfo"`
	HostInfo      string `json:"HostInfo"`
	HashKeyName   string `json:"HashKeyName"`
	HashSignature string `json:"HashSignature"`
}

// secureCompare performs constant-time comparison to prevent timing attacks on token/signature validation.
func secureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// NewNotifierGateway constructs a new web server object for use as the notifier gateway host
func NewNotifierGateway(appName string, configFileNameWebServer string, configFileNameGateway string, customConfigPath string) (*webserver.WebServer, error) {
	// read gateway config data
	cfg := &config.Config{
		AppName:          appName,
		ConfigFileName:   configFileNameGateway,
		CustomConfigPath: customConfigPath,
	}

	if err := cfg.Read(); err != nil {
		return nil, fmt.Errorf("Read Notifier Gateway Config Failed: %w", err)
	}

	// establish data store connection
	// FIX #9: ConnectDataStore already sets DynamoDBActionRetryAttempts atomically,
	// so the redundant SetDynamoDBActionRetryAttempts call is removed.
	if err := model.ConnectDataStore(cfg); err != nil {
		return nil, fmt.Errorf("Connect Notifier Gateway Data Store Failed: %w", err)
	} else {
		model.SetGatewayKey(cfg.NotifierGatewayData.GatewayKey)

		hashKeysMap := make(map[string]string)

		for _, v := range cfg.NotifierGatewayData.HashKeys {
			hashKeysMap[v.HashKeyName] = v.HashKeySecret
		}
		model.SetHashKeys(hashKeysMap)

		// BL-1: Publish configurable DDB endpoint lookup retry parameters
		// so snsconfirmation uses config values instead of hardcoded 250ms / 3.
		model.SetEndpointRetryDelayMs(cfg.NotifierGatewayData.EndpointRetryDelayMs)
		model.SetEndpointRetryMaxAttempts(cfg.NotifierGatewayData.EndpointRetryMaxAttempts)

		// Publish the secure-by-default SNS signature enforcement flag to
		// the model package so snsrouter handlers can read it. When the
		// operator explicitly disables verification via config, emit a WARN
		// so the state is observable in logs.
		model.SetRequireSNSSignature(cfg.NotifierGatewayData.RequireSNSSignature)
		if !cfg.NotifierGatewayData.RequireSNSSignature {
			log.Println("WARN: Notifier Gateway SNS signature verification is DISABLED by config (require_sns_signature=false). Inbound SNS callbacks will NOT be cryptographically verified. This is insecure and should only be used for local development or simulator testing.")
		} else {
			log.Println("Notifier Gateway SNS signature verification is ENABLED (secure by default).")
		}
	}

	// setup gateway server
	gatewayServer, err := webserver.NewWebServer(appName, configFileNameWebServer, customConfigPath)
	if err != nil {
		return nil, fmt.Errorf("Notifier Gateway Web Server Failed: %s", err)
	}

	gatewayServer.Routes = map[string]*ginw.RouteDefinition{
		"base": {
			Routes: []*ginw.Route{
				{
					RelativePath:    "/snsrouter/:serverKey",
					Method:          ginhttpmethod.POST,
					Binding:         ginbindtype.UNKNOWN,
					BindingInputPtr: nil,
					Handler:         snsrouter,
				},
				{
					RelativePath:    "/callerid",
					Method:          ginhttpmethod.GET,
					BindingInputPtr: nil,
					Handler:         callerid,
				},
				{
					RelativePath:    "/reporthealth",
					Method:          ginhttpmethod.POST,
					Binding:         ginbindtype.BindJson,
					BindingInputPtr: &healthreport{},
					Handler:         healthreporter,
				},
				{
					RelativePath:    "/reporthealth/:instanceid",
					Method:          ginhttpmethod.DELETE,
					Binding:         ginbindtype.UNKNOWN,
					BindingInputPtr: nil,
					Handler:         healthreporterdelete,
				},
			},
			CorsMiddleware: &cors.Config{},
		},
	}

	// return gateway server to caller
	return gatewayServer, nil
}

// escapeUserInput replaces control characters (newlines, carriage returns, tabs) with spaces to mitigate log-injection vulnerability
func escapeUserInput(data string) string {
	result := strings.ReplaceAll(data, "\n", " ")
	result = strings.ReplaceAll(result, "\r", " ")
	result = strings.ReplaceAll(result, "\t", " ")
	return result
}

// isValidSNSUrl validates that a URL matches the expected AWS SNS domain pattern
// to prevent SSRF attacks via attacker-controlled SubscribeURL/UnsubscribeURL.
// Legitimate AWS SNS URLs follow: https://sns.<region>.amazonaws.com/...
// The regex ensures the domain ends with .amazonaws.com (no attacker subdomains).
var snsUrlPattern = regexp.MustCompile(`^https://sns\.[a-z0-9-]+\.amazonaws\.com/`)
var snsTopicArnPattern = regexp.MustCompile(`^arn:aws:sns:[a-z0-9-]+:\d{12}:[A-Za-z0-9_-]{1,256}$`)

func isValidSNSUrl(rawUrl string) bool {
	return snsUrlPattern.MatchString(strings.ToLower(rawUrl))
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
		log.Println("!!! Inbound Health Report Payload Missing or Malformed From " + escapeUserInput(c.ClientIP()) + " !!!")
		c.String(400, "Inbound Health Report Payload Missing or Malformed")
		return
	}

	if util.LenTrim(data.NamespaceId) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing NamespaceId !!!")
		c.String(400, "Inbound Health Report Payload Missing Required Value (Namespace)")
		return
	}

	if util.LenTrim(data.ServiceId) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing ServiceId !!!")
		c.String(400, "Inbound Health Report Payload Missing Required Value (Service)")
		return
	}

	if util.LenTrim(data.InstanceId) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing InstanceId !!!")
		c.String(400, "Inbound Health Report Payload Missing Required Value (Instance)")
		return
	}

	if util.LenTrim(data.AwsRegion) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing AWSRegion !!!")
		c.String(400, "Inbound Health Report Payload Missing Required Value (Region)")
		return
	}

	if util.LenTrim(data.ServiceInfo) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing ServiceInfo !!!")
		c.String(400, "Inbound Health Report Payload Missing Required Value (ServiceInfo)")
		return
	}

	if util.LenTrim(data.HostInfo) == 0 {
		log.Println("!!! Inbound Health Report Payload Missing HostInfo !!!")
		c.String(400, "Inbound Health Report Payload Missing Required Value (HostInfo)")
		return
	}

	if model.GetHashKeysCount() == 0 {
		log.Println("!!! Internal Server Config Error, Missing Hash Keys !!!")
		c.String(500, "Internal Server Config Error, Missing Hash Keys")
		return
	}

	// FIX #8: Escape user-controlled HashKeyName before logging
	escapedHashKeyName := escapeUserInput(data.HashKeyName)

	hashSecret, hsFound := model.GetHashKey(strings.TrimSpace(data.HashKeyName))
	if !hsFound || util.LenTrim(hashSecret) == 0 {
		log.Println("!!! Inbound Health Report Hash Key Name '" + escapedHashKeyName + "' From " + escapeUserInput(c.ClientIP()) + " Not Valid: Host Does Not Expect This Hash Key Name !!!")
		c.String(401, "Inbound Health Report Hash Key Name Not Valid")
		return
	} else {
		buf := data.NamespaceId + data.ServiceId + data.InstanceId + util.FormatDate(time.Now().UTC())
		bufHash := crypto.Sha256(buf, hashSecret)

		// FIX #7: Use constant-time comparison to prevent timing attacks
		if !secureCompare(bufHash, data.HashSignature) {
			log.Println("!!! Inbound Health Report Hash Signature From " + escapeUserInput(c.ClientIP()) + " Not Valid: Hash Signature Mismatch !!!")
			// FIX #6: Do not log hash secret in plaintext
			log.Println("... Host = Sha256 Source: " + escapeUserInput(buf))
			log.Println("... Client Sha256 Signature Mismatch")
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
				log.Println("!!! Inbound Health Report (NamespaceID '" + escapeUserInput(data.NamespaceId) + "', ServiceID '" + escapeUserInput(data.ServiceId) + "', InstanceID '" + escapeUserInput(data.InstanceId) + "') Persist to Data Store Failed: " + e.Error() + " !!!")
				c.String(500, "Persist Instance Status to Data Store Failed")
				return fmt.Errorf("Persist Instance Status to Data Store Failed: %s", e.Error())
			} else {
				log.Println("+++ Inbound Health Report (NamespaceID '" + escapeUserInput(data.NamespaceId) + "', ServiceID '" + escapeUserInput(data.ServiceId) + "', InstanceID '" + escapeUserInput(data.InstanceId) + "') Persist to Data Store OK +++")
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
			log.Println("!!! Inbound Health Report (NamespaceID '" + escapeUserInput(data.NamespaceId) + "', ServiceID '" + escapeUserInput(data.ServiceId) + "', InstanceID '" + escapeUserInput(data.InstanceId) + "') Persist to Data Store Failed: " + e.Error() + " !!!")
			c.String(500, "Persist Instance Status to Data Store Failed")
		} else {
			log.Println("+++ Inbound Health Report (NamespaceID '" + escapeUserInput(data.NamespaceId) + "', ServiceID '" + escapeUserInput(data.ServiceId) + "', InstanceID '" + escapeUserInput(data.InstanceId) + "') Persist to Data Store OK +++")
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
		c.String(400, "Delete Health Report Service Record Request Missing InstanceID")
		return
	}

	hashKeyName := strings.TrimSpace(escapeUserInput(c.GetHeader("x-nts-gateway-hash-name")))

	if util.LenTrim(hashKeyName) == 0 {
		log.Println("!!! Delete Health Report Service Record Failed: Hash Key Name is Missing From Header 'x-nts-gateway-hash-name' !!!")
		c.String(400, "Delete Health Report Service Record Request Missing Key Name")
		return
	}

	hashKeySignature := escapeUserInput(c.GetHeader("x-nts-gateway-hash-signature"))

	if util.LenTrim(hashKeySignature) == 0 {
		log.Println("!!! Delete Health Report Service Record Failed: Hash Signature is Missing From Header 'x-nts-gateway-hash-signature' !!!")
		c.String(400, "Delete Health Report Service Record Request Missing Signature")
		return
	}

	if model.GetHashKeysCount() == 0 {
		log.Println("!!! Internal Server Config Error, Missing Hash Keys !!!")
		c.String(500, "Internal Server Config Error, Missing Hash Keys")
		return
	}

	hashSecret, hsFound := model.GetHashKey(hashKeyName)
	if !hsFound || util.LenTrim(hashSecret) == 0 {
		log.Println("!!! Delete Health Report Service Record Request's Hash Key Name '" + hashKeyName + "' From " + escapeUserInput(c.ClientIP()) + " Not Valid: Host Does Not Expect This Hash Key Name !!!")
		c.String(401, "Delete Health Report Service Record Request's Hash Key Name Not Valid")
		return
	} else {
		buf := instanceId + util.FormatDate(time.Now().UTC())
		bufHash := crypto.Sha256(buf, hashSecret)

		// FIX #7: Use constant-time comparison to prevent timing attacks
		if !secureCompare(bufHash, hashKeySignature) {
			log.Println("!!! Delete Health Report Service Record Request's Hash Signature From " + escapeUserInput(c.ClientIP()) + " Not Valid: Hash Signature Mismatch !!!")
			// FIX #6: Do not log hash secret in plaintext
			log.Println("... Host = Sha256 Source: " + escapeUserInput(buf))
			log.Println("... Client Sha256 Signature Mismatch")
			c.String(401, "Delete Health Report Service Record Request's Hash Signature Not Valid")
			return
		}
	}

	// FIX #10: Use model key builder helpers instead of hardcoded patterns
	pk := model.BuildHealthPK()
	sk := model.BuildHealthSK(instanceId)

	if xray.XRayServiceOn() {
		trace := xray.NewSegmentFromHeader(c.Request)
		if !trace.Ready() {
			trace = xray.NewSegment("healthreporter-delete")
		}
		defer trace.Close()

		trace.Capture("healthReporter.deleteFromDataStore", func() error {
			if _, e := model.DeleteInstanceHealthFromDataStore(&dynamodb.DynamoDBTableKeyValue{PK: pk, SK: sk}); e != nil {
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
		if _, e := model.DeleteInstanceHealthFromDataStore(&dynamodb.DynamoDBTableKeyValue{PK: pk, SK: sk}); e != nil {
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

	if util.LenTrim(model.GetGatewayKey()) == 0 {
		c.String(500, "Internal Server Config Error, Missing nts-gateway Key")
		return
	}

	// validate inbound request
	if token := c.GetHeader("x-nts-gateway-token"); util.LenTrim(token) > 0 {
		key := crypto.Sha256(util.FormatDate(time.Now().UTC()), model.GetGatewayKey())

		// FIX #7: Use constant-time comparison to prevent timing attacks
		if secureCompare(token, key) {
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
//
// SNS signature verification is performed inside snsconfirmation / snsnotification
// immediately after BindJSON succeeds. Verification is gated by
// model.GetRequireSNSSignature() which defaults to true (secure by default).
// See snssigverify.go for the verification implementation.
func snsrouter(c *gin.Context, bindingInputPtr interface{}) {
	// validate gin context input
	if c == nil {
		return
	}

	log.Println("/snsrouter Invoked: x-amz-sns-message-type = " + escapeUserInput(c.GetHeader("x-amz-sns-message-type")) + ", serverKey = " + escapeUserInput(c.Param("serverKey")))

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
			xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' BindJSON Error: %w", err)))
		}
	} else {
		// SNS signature verification — secure by default.
		// When enforcement is enabled, reject any payload whose RSA signature
		// does not verify against the AWS-published signing cert. The cert URL
		// is host-allowlisted before fetch (SSRF pre-gate in verifyAWSSNSSignature).
		if model.GetRequireSNSSignature() {
			if verr := verifySNSConfirmationSignature(confirm); verr != nil {
				log.Printf("/snsrouter 'subscriptionconfirmation' signature verification failed (MessageId=%s, IP=%s): %s",
					escapeUserInput(confirm.MessageId), escapeUserInput(c.ClientIP()), escapeUserInput(verr.Error()))
				c.String(403, "SNS Signature Verification Failed")

				if seg != nil && seg.Ready() {
					xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' signature verification failed: %s", escapeUserInput(verr.Error()))))
				}
				return
			}
		}

		// auto confirm
		if url := confirm.SubscribeURL; util.LenTrim(url) > 0 {
			log.Println("/snsrouter 'subscriptionconfirmation' SubscribeURL From SNS = " + escapeUserInput(url))

			// FIX: Validate SubscribeURL domain to prevent SSRF.
			// Legitimate AWS SNS confirmation URLs always match https://sns.<region>.amazonaws.com/
			if !isValidSNSUrl(url) {
				log.Println("/snsrouter 'subscriptionconfirmation' SubscribeURL rejected: does not match expected AWS SNS domain")
				c.String(403, "SubscribeURL domain not allowed")

				if seg != nil && seg.Ready() {
					xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' SubscribeURL rejected: %s", escapeUserInput(url))))
				}

				return
			}

			// validate serverKey
			if util.LenTrim(serverKey) <= 0 {
				log.Println("/snsrouter 'subscriptionconfirmation' Missing serverKey From Invoker")
				c.String(412, "Server Key is Required")

				if seg != nil && seg.Ready() {
					xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' Missing serverKey From Invoker")))
				}

				return
			}

			log.Println("/snsrouter 'subscriptionconfirmation' serverKey From Invoker = " + escapeUserInput(serverKey))

			// BL-1: configurable delay before first DDB endpoint lookup and
			// between retries. Defaults (250ms / 3 attempts) match prior
			// hardcoded behavior for backward compatibility.
			retryDelayMs := model.GetEndpointRetryDelayMs()
			if retryDelayMs == 0 {
				retryDelayMs = 250
			}
			retryDelay := time.Duration(retryDelayMs) * time.Millisecond

			maxAttempts := model.GetEndpointRetryMaxAttempts()
			if maxAttempts == 0 {
				maxAttempts = 3
			}

			// wait for notifier server to update ddb with server endpoint info
			waitTimer := time.NewTimer(retryDelay)
			select {
			case <-waitTimer.C:
			case <-c.Request.Context().Done():
				waitTimer.Stop()
				c.String(499, "Client Disconnected")
				return
			}

			serverEndpointUrl := ""

			for i := uint(0); i < maxAttempts; i++ {
				if serverUrl, err := model.GetServerRouteFromDataStore(serverKey); err != nil {
					log.Printf("Server Key %s Lookup Failed: (IP Source: %s) %s\n", escapeUserInput(serverKey), escapeUserInput(c.ClientIP()), err.Error())
					c.String(412, "Server Key Not Valid")

					if seg != nil && seg.Ready() {
						xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("Server Key %s Lookup Failed: (IP Source: %s) %w", escapeUserInput(serverKey), escapeUserInput(c.ClientIP()), err)))
					}

					return
				} else if util.LenTrim(serverUrl) == 0 && i == maxAttempts-1 {
					log.Printf("Server Key %s Not Found in DDB: (IP Source: %s) %s\n", escapeUserInput(serverKey), escapeUserInput(c.ClientIP()), "ServerUrl Returned is Blank")
					c.String(412, "Server Key Not Exist")

					if seg != nil && seg.Ready() {
						xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("Server Key %s Not Found in DDB: (IP Source: %s) %s", escapeUserInput(serverKey), escapeUserInput(c.ClientIP()), "ServerUrl Returned is Blank")))
					}

					return
				} else if util.LenTrim(serverUrl) > 0 {
					serverEndpointUrl = serverUrl
					break
				} else {
					retryTimer := time.NewTimer(retryDelay)
					select {
					case <-retryTimer.C:
					case <-c.Request.Context().Done():
						retryTimer.Stop()
						c.String(499, "Client Disconnected")
						return
					}
				}
			}

			log.Println("/snsrouter 'subscriptionconfirmation' GET " + escapeUserInput(url) + "...")

			// perform confirmation action
			if status, body, err := rest.GET(url, nil); err != nil {
				log.Println("/snsrouter 'subscriptionconfirmation' GET Failed: " + err.Error())
				c.String(412, "Subscription Confirm Callback Failed")

				if seg != nil && seg.Ready() {
					xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' GET Failed: %w", err)))
				}
			} else if status != 200 {
				log.Println("/snsrouter 'subscriptionconfirmation' GET Not Status 200: [" + util.Itoa(status) + "] Body: " + body)
				c.String(status, "Subscription Confirm Callback Status Code: %d", status)

				if seg != nil && seg.Ready() {
					xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' GET Not Status 200: ["+util.Itoa(status)+"] Body: %s", body)))
				}
			} else {
				// confirm success, with sns endpoint
				buf := util.SplitString(body, "<SubscriptionArn>", -1)
				buf = util.SplitString(buf, "</SubscriptionArn>", 0)

				if util.Left(buf, 12) == "arn:aws:sns:" {
					// subscription arn received
					log.Println("/snsrouter 'subscriptionconfirmation' GET Status 200 with SubscriptionArn '" + buf + "': " + body)

					// validate TopicArn format before using in URL to prevent path traversal
					if !snsTopicArnPattern.MatchString(confirm.TopicArn) {
						log.Printf("/snsrouter 'subscriptionconfirmation' TopicArn has invalid format: %s", escapeUserInput(confirm.TopicArn))
						c.String(412, "Invalid TopicArn Format")

						if seg != nil && seg.Ready() {
							xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' TopicArn has invalid format")))
						}
						return
					}

					serverEndpointUrl += "/snsupdate"
					serverEndpointUrl += "/" + confirm.TopicArn

					if status, body, err := rest.POST(serverEndpointUrl, []*rest.HeaderKeyValue{}, buf); err != nil {
						log.Println("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Failed: (SNS Subscription Still Valid, This is Warning Only) " + err.Error())

						if seg != nil && seg.Ready() {
							xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Failed: (SNS Subscription Still Valid, This is Warning Only) %w", err)))
						}
					} else if status != 200 {
						log.Println("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Not Receive Status 200: (SNS Subscription Still Valid, This is Warning Only) Info = " + body)

						if seg != nil && seg.Ready() {
							xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Not Receive Status 200: (SNS Subscription Still Valid, This is Warning Only) Info = %s", body)))
						}
					} else {
						log.Println("/snsrouter 'subscriptionconfirmation' POST to Notifier Server to Update SNS SubscriptionArn Success (Status 200)")
					}

					c.Status(200)
				} else {
					log.Println("/snsrouter 'subscriptionconfirmation' GET Status 200 But Response Did Not Include SubscriptionArn in XML From SNS: " + body)
					c.Status(412)

					if seg != nil && seg.Ready() {
						xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' GET Status 200 But Response Did Not Include SubscriptionArn in XML From SNS: %s", body)))
					}
				}
			}
		} else {
			log.Println("/snsrouter 'subscriptionconfirmation' Missing Expected SubscriberURL From SNS")
			c.String(412, "Expected Subscription Confirm URL")

			if seg != nil && seg.Ready() {
				xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'subscriptionconfirmation' Missing Expected SubscriberURL From SNS")))
			}
		}
	}
}

// buildConfirmationSigningPayload unescapes and builds subscription confirmation payload, to be ready for signing
//
// SP-008 P1-CONN-5 (2026-04-15): constructed via strings.Builder with a
// single Grow so the allocation cost is O(n) rather than the O(n^2)
// cost of repeated `buf += str` string concatenation (each `+=` on a
// Go string re-allocates the full buffer). This path fires BEFORE
// signature verification on every inbound SubscriptionConfirmation,
// so it is un-dodgeable load on the SNS webhook route.
func buildConfirmationSigningPayload(confirm *confirmation) string {
	if confirm == nil {
		return ""
	}
	var b strings.Builder
	// Pre-size the buffer: 7 fixed header labels (~60 bytes) plus the
	// worst-case length of the variable fields. Over-sizing slightly is
	// cheaper than mid-build re-growth.
	b.Grow(64 + len(confirm.Message) + len(confirm.MessageId) + len(confirm.SubscribeURL) +
		len(confirm.Timestamp) + len(confirm.Token) + len(confirm.TopicArn) + len(confirm.Type))
	b.WriteString("Message\n")
	b.WriteString(confirm.Message)
	b.WriteByte('\n')
	b.WriteString("MessageId\n")
	b.WriteString(confirm.MessageId)
	b.WriteByte('\n')
	b.WriteString("SubscribeURL\n")
	b.WriteString(confirm.SubscribeURL)
	b.WriteByte('\n')
	b.WriteString("Timestamp\n")
	b.WriteString(confirm.Timestamp)
	b.WriteByte('\n')
	b.WriteString("Token\n")
	b.WriteString(confirm.Token)
	b.WriteByte('\n')
	b.WriteString("TopicArn\n")
	b.WriteString(confirm.TopicArn)
	b.WriteByte('\n')
	b.WriteString("Type\n")
	b.WriteString(confirm.Type)
	b.WriteByte('\n')
	return b.String()
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
		c.String(412, "Expected Notification Payload")

		if seg != nil && seg.Ready() {
			xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'notification' BindJSON Error: %w", err)))
		}
	} else {
		// SNS signature verification — secure by default. See snsconfirmation
		// for the same gate. Rejects forged notifications before any downstream
		// relay occurs.
		if model.GetRequireSNSSignature() {
			if verr := verifySNSNotificationSignature(notify); verr != nil {
				log.Printf("/snsrouter 'notification' signature verification failed (MessageId=%s, IP=%s): %s",
					escapeUserInput(notify.MessageId), escapeUserInput(c.ClientIP()), escapeUserInput(verr.Error()))
				c.String(403, "SNS Signature Verification Failed")

				if seg != nil && seg.Ready() {
					xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'notification' signature verification failed: %s", escapeUserInput(verr.Error()))))
				}
				return
			}
		}

		// valid notification data
		if util.LenTrim(notify.MessageId) == 0 {
			log.Println("/snsrouter 'notification' SNS Data Missing MessageId")
			c.String(412, "Expected Notification Message ID")

			if seg != nil && seg.Ready() {
				xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'notification' SNS Data Missing MessageId")))
			}

			return
		}

		if util.LenTrim(notify.TopicArn) == 0 {
			log.Println("/snsrouter 'notification' SNS Data Missing TopicArn")
			c.String(412, "Expected Notification Topic ARN")

			if seg != nil && seg.Ready() {
				xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'notification' SNS Data Missing TopicArn")))
			}

			return
		}

		if util.LenTrim(notify.Message) == 0 {
			log.Println("/snsrouter 'notification' SNS Data Missing Message")
			c.String(412, "Expected Notification Message")

			if seg != nil && seg.Ready() {
				xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'notification' SNS Data Missing Message")))
			}

			return
		}

		if util.LenTrim(notify.Timestamp) == 0 {
			log.Println("/snsrouter 'notification' SNS Data Missing Timestamp")
			c.String(412, "Expected Notification Timestamp")

			if seg != nil && seg.Ready() {
				xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("/snsrouter 'notification' SNS Data Missing Timestamp")))
			}

			return
		}

		// validate serverKey
		if serverUrl, err := model.GetServerRouteFromDataStore(serverKey); err != nil {
			log.Printf("Server Key %s Lookup Failed: (IP Source: %s) %s\n", escapeUserInput(serverKey), escapeUserInput(c.ClientIP()), err.Error())
			c.String(412, "Server Key Not Valid")

			if seg != nil && seg.Ready() {
				xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("Server Key %s Lookup Failed: (IP Source: %s) %w", escapeUserInput(serverKey), escapeUserInput(c.ClientIP()), err)))
			}
		} else if util.LenTrim(serverUrl) == 0 {
			log.Printf("Server Key %s Not Found in DDB: (IP Source: %s) %s\n", escapeUserInput(serverKey), escapeUserInput(c.ClientIP()), "ServerUrl Returned is Blank")
			unsubscribeSNS(notify)
			c.String(412, "Server Key Not Exist")

			if seg != nil && seg.Ready() {
				xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("Server Key %s Not Found in DDB: (IP Source: %s) %s", escapeUserInput(serverKey), escapeUserInput(c.ClientIP()), "ServerUrl Returned is Blank")))
			}
		} else {
			// perform sns notification routing task
			if notifyJson, err := util.MarshalJSONCompact(notify); err != nil {
				log.Printf("Server Key %s at Host %s Marshal Notification Data to JSON Failed: %s", escapeUserInput(serverKey), serverUrl, err.Error())
				c.String(412, "Notification Marshal Failed")

				if seg != nil && seg.Ready() {
					xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("Server Key %s at Host %s Marshal Notification Data to JSON Failed: %w", escapeUserInput(serverKey), serverUrl, err)))
				}
			} else {
				// relay sns notification to target notify server
				log.Println("/snsrouter 'notification' Relaying SNS Data To Notifier Server Endpoint '" + serverUrl + "': " + notifyJson)

				serverUrl += "/snsrelay"

				if statusCode, _, err := rest.POST(serverUrl, []*rest.HeaderKeyValue{
					{
						Key:   "Content-Type",
						Value: "application/json",
					},
				}, notifyJson); err != nil {
					log.Printf("Server Key %s at Host %s Route Notification From SNS to Internal Host Failed: %s", escapeUserInput(serverKey), serverUrl, err.Error())
					c.String(412, "Route Notification to Internal Host Failed")

					if seg != nil && seg.Ready() {
						xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("Server Key %s at Host %s Route Notification From SNS to Internal Host Failed: %w", escapeUserInput(serverKey), serverUrl, err)))
					}
				} else if statusCode != 200 {
					log.Printf("Server Key %s at Host %s Route Notification From SNS to Internal Host Did Not Yield Status Code 200: Actual Code = %d", escapeUserInput(serverKey), serverUrl, statusCode)
					c.Status(statusCode)

					if seg != nil && seg.Ready() {
						xray.LogXrayAddFailure("NotifierGateway", seg.SafeAddError(fmt.Errorf("Server Key %s at Host %s Route Notification From SNS to Internal Host Did Not Yield Status Code 200: Actual Code = %d", escapeUserInput(serverKey), serverUrl, statusCode)))
					}
				} else {
					log.Printf("Server Key %s at Host %s Route Notification Complete", escapeUserInput(serverKey), serverUrl)
					c.Status(200)
				}
			}
		}
	}
}

// buildNotificationSigningPayload unescapes and builds notification payload, to be ready for signing
//
// SP-008 P1-CONN-5 (2026-04-15): constructed via strings.Builder with a
// single Grow — see buildConfirmationSigningPayload godoc for the
// rationale. This path fires BEFORE signature verification on every
// inbound SNS Notification, so it is un-dodgeable load on the hottest
// webhook route.
func buildNotificationSigningPayload(notify *notification) string {
	if notify == nil {
		return ""
	}
	var b strings.Builder
	b.Grow(64 + len(notify.Message) + len(notify.MessageId) + len(notify.Subject) +
		len(notify.Timestamp) + len(notify.TopicArn) + len(notify.Type))
	b.WriteString("Message\n")
	b.WriteString(notify.Message)
	b.WriteByte('\n')
	b.WriteString("MessageId\n")
	b.WriteString(notify.MessageId)
	b.WriteByte('\n')
	if util.LenTrim(notify.Subject) > 0 {
		b.WriteString("Subject\n")
		b.WriteString(notify.Subject)
		b.WriteByte('\n')
	}
	b.WriteString("Timestamp\n")
	b.WriteString(notify.Timestamp)
	b.WriteByte('\n')
	b.WriteString("TopicArn\n")
	b.WriteString(notify.TopicArn)
	b.WriteByte('\n')
	b.WriteString("Type\n")
	b.WriteString(notify.Type)
	b.WriteByte('\n')
	return b.String()
}

// unsubscribeSNS unsubscribes from an SNS topic using the UnsubscribeURL from the notification payload
func unsubscribeSNS(notify *notification) {
	if notify == nil {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic Aborted: Notification Object Parsed from SNS is Nil !!!")
		return
	}

	// FIX: Use escapeUserInput only for log-safe strings, not for the actual URL.
	// escapeUserInput replaces \n \r \t with spaces, which corrupts URLs.
	topicArnLog := escapeUserInput(notify.TopicArn)
	unsubUrl := notify.UnsubscribeURL

	if util.LenTrim(notify.TopicArn) == 0 {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic Aborted: TopicArn Not Set in Notification Object Parsed from SNS !!!")
		return
	}

	if util.LenTrim(unsubUrl) == 0 {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArnLog + "' Aborted: UnsubscribeURL Not Set in Notification Object Parsed from SNS !!!")
		return
	}

	if strings.ToUpper(util.Left(unsubUrl, 8)) != "HTTPS://" {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArnLog + "' From UnsubscribeURL '" + escapeUserInput(unsubUrl) + "' Aborted: UnsubscribeURL is Not Https or Malformed !!!")
		return
	}

	// Unescape BEFORE SSRF validation so the validated URL matches the fetched URL
	unsubUrl = strings.ReplaceAll(unsubUrl, `\u0026`, `&`)
	unsubUrl = strings.ReplaceAll(unsubUrl, `\"`, `"`)
	unsubUrl = strings.ReplaceAll(unsubUrl, `"`, "")

	// FIX: Validate UnsubscribeURL domain to prevent SSRF.
	// Legitimate AWS SNS unsubscribe URLs always match https://sns.<region>.amazonaws.com/
	if !isValidSNSUrl(unsubUrl) {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArnLog + "' From UnsubscribeURL '" + escapeUserInput(unsubUrl) + "' Aborted: URL domain not allowed !!!")
		return
	}

	// call HTTP GET to unsubscribe
	unsubUrlLog := escapeUserInput(unsubUrl)
	if status, body, err := rest.GET(unsubUrl, []*rest.HeaderKeyValue{}); err != nil {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArnLog + "' Failed for UnsubscribeURL '" + unsubUrlLog + "': (HTTP GET Error) " + err.Error() + " !!!")
	} else if status != 200 {
		log.Println("!!! Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArnLog + "' Failed for UnsubscribeURL '" + unsubUrlLog + "': (Status " + util.Itoa(status) + ") " + body + " !!!")
	} else {
		log.Println("$$$ Notifier Gateway Auto Unsubscribe SNS Topic '" + topicArnLog + "' Successful for UnsubscribeURL '" + unsubUrlLog + "': " + body + " $$$")
	}
}

// =====================================================================================================================
// instance health clean up helper
// =====================================================================================================================

// FIX #4: Package-level mutex and sdMap to protect against concurrent access
// from multiple goroutines (e.g. if RunStaleHealthReportRecordsRemoverService is called more than once).
//
// ⚠️ sdMapMu is held across CloudMap deregister network I/O in
// removeInactiveInstancesFromServiceDiscovery and sdDeregisterInstance.
// This is SAFE for the sole current caller (the single background goroutine
// spawned by RunStaleHealthReportRecordsRemoverService), where concurrency
// of at most one instance is guaranteed by construction.
//
// MUST NOT be called from a request-handler path or any other
// request-scoped goroutine: holding a mutex across unbounded AWS
// CloudMap I/O (≥ seconds under transient network failure) on the
// request path would serialize all incoming requests and surface as
// gateway-wide latency spikes. If a future caller needs request-path
// access to sdMap, refactor to scope the lock to map read/write only
// and perform deregister I/O outside the critical section.
// P2-CONN-1 v1.8.7 — see _src/docs/repos/connector/reviews/deep-review-full-2026-04-17.md.
var (
	sdMap   map[string]*cloudmap.CloudMap
	sdMapMu sync.Mutex
)

// RunStaleHealthReportRecordsRemoverService starts a go-routine and runs loop to continuously clean up stale records.
// FIX #3: Uses time.NewTicker inside select so the stop signal is immediately honored,
// instead of the original busy-wait select/default + time.Sleep which missed the stop signal during sleep.
func RunStaleHealthReportRecordsRemoverService(stopService chan bool) {
	if stopService == nil {
		log.Println("### RunStaleHealthReportRecordsRemoverService: stopService channel is nil, service not started ###")
		return
	}

	freq := model.GetHealthReportCleanUpFrequencySeconds()

	if freq == 0 {
		freq = 120
	} else if freq < 30 {
		freq = 30
	} else if freq > 3600 {
		freq = 3600
	}

	go func() {
		// SP-008 P1-CONN-1 (2026-04-15): panic boundary for this
		// long-lived background goroutine. A panic inside
		// removeInactiveInstancesFromServiceDiscovery (DynamoDB
		// scan, CloudMap deregister, xray segment write) would
		// otherwise crash the whole notifier-gateway process and
		// regresses the SVC-F6 / CL-F5 invariant that every
		// library-spawned goroutine recovers its own panic.
		// The recovery logs-and-continues rather than re-panicking
		// so transient dependency failures cannot take the service
		// down; the next ticker fire retries the cleanup cycle.
		defer func() {
			if r := recover(); r != nil {
				// SP-008 re-eval follow-up (2026-04-15): include debug.Stack()
				// for parity with safeGo / safeCall / runCleanup recovery
				// conventions in service/service.go. Without the stack, a
				// recovered panic in this long-lived background goroutine
				// surfaces as a bare "%v" with no traceback — diagnosing a
				// downstream DynamoDB / CloudMap / xray-go regression then
				// requires re-reproducing the panic, which defeats the
				// observability purpose of the recovery itself.
				log.Printf("!!! PANIC in StaleHealthReportRecordsRemover background goroutine, recovered: %v\n%s !!!", r, debug.Stack())
			}
		}()

		// Run immediately on startup before waiting for the first tick
		log.Println(">>> Stale Health Report Record Remover - Processing Invoked <<<")
		removeInactiveInstancesFromServiceDiscovery()

		ticker := time.NewTicker(time.Duration(freq) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-stopService:
				log.Println("### Stopping Stale Health Report Record Remover Service ###")
				return

			case <-ticker.C:
				log.Println(">>> Stale Health Report Record Remover - Processing Invoked <<<")
				removeInactiveInstancesFromServiceDiscovery()
			}
		}
	}()
}

// removeInactiveInstancesFromServiceDiscovery will query dynamodb table used for instance host health monitor,
// if greater than 15 minutes of no keep-alive timestamp update, gather for cloudmap service discovery de-register,
// then remove from dynamodb table upon de-register success.
// FIX #4: Uses the package-level sdMapMu instead of a per-goroutine mutex.
func removeInactiveInstancesFromServiceDiscovery() {
	items, e := model.ListInactiveInstancesFromDataStore()
	if e != nil {
		log.Println("!!! Remove Health Report Stale Records Failed: " + e.Error() + " !!!")
		return
	}

	if len(items) == 0 {
		log.Println("~~~ Health Report Stale Record Clean Up: 0 Found, All OK ~~~")
		return
	}

	// FIX #4: Lock the package-level mutex to protect sdMap and serialise deregister operations
	sdMapMu.Lock()
	defer sdMapMu.Unlock()

	// FIX #10: Use model key builder helpers instead of hardcoded patterns
	keys := []*dynamodb.DynamoDBTableKeyValue{}
	pk := model.BuildHealthPK()

	for _, v := range items {
		if v != nil {
			if err := sdDeregisterInstance(v.NamespaceId, v.ServiceId, v.InstanceId, v.AwsRegion); err == nil {
				// deregister success, queue instance for delete from data store
				keys = append(keys, &dynamodb.DynamoDBTableKeyValue{
					PK: pk,
					SK: model.BuildHealthSK(v.InstanceId),
				})
			}
		}
	}

	if len(keys) > 0 {
		// delete keys from data store
		deleteFailKeys, err := model.DeleteInstanceHealthFromDataStore(keys...)
		if err != nil {
			log.Println("!!! Health Report Stale Record Clean Up: Service Discovery De-Register OK, But Delete Instance From Data Store Failed, " + err.Error() + " !!!")
		} else if len(deleteFailKeys) > 0 {
			log.Println("@@@ Health Report Stale Record Clean Up: Service Discovery De-Register OK, But " + util.Itoa(len(deleteFailKeys)) + " Instances Fail To Delete From Data Store @@@")
		} else {
			log.Println("~~~ Health Report Stale Record Clean Up: OK ~~~")
		}
	} else {
		log.Println("~~~ Health Report Stale Record Clean Up: Zero Instance De-Registered From CloudMap ~~~")
	}
}

// sdDeregisterInstance handles de-register instance action, along with creating and using sd object for various namespaces.
// FIX #1: Returns errors instead of nil for empty params to prevent silent data deletion.
// Caller must be holding sdMapMu.
func sdDeregisterInstance(namespaceId string, serviceId string, instanceId string, awsRegion string) error {
	if util.LenTrim(namespaceId) == 0 {
		return fmt.Errorf("NamespaceId is required for de-register")
	}

	if util.LenTrim(serviceId) == 0 {
		return fmt.Errorf("ServiceId is required for de-register")
	}

	if util.LenTrim(instanceId) == 0 {
		return fmt.Errorf("InstanceId is required for de-register")
	}

	if util.LenTrim(awsRegion) == 0 {
		return fmt.Errorf("AwsRegion is required for de-register")
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
		return nil, fmt.Errorf("Connect SD for NamespaceID '%s' Failed: %w", namespaceId, err)
	} else {
		return sd, nil
	}
}

// deregisterInstance will remove instance from cloudmap and route 53 (used by sdDeregisterInstance).
// FIX #11: Checks for sdoperationstatus.Fail to exit immediately instead of retrying for 5 seconds on a permanent failure.
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

	timeoutDuration := time.Duration(model.GetServiceDiscoveryTimeoutSeconds()) * time.Second

	if timeoutDuration < 5*time.Second {
		timeoutDuration = 5 * time.Second
	}

	if operationId, err := registry.DeregisterInstance(sd, instanceId, serviceId, timeoutDuration); err != nil {
		log.Println("!!! Service Discovery De-Register Instance '" + instanceId + "' Failed: (Initial Deregister Action) " + err.Error() + " !!!")
		return fmt.Errorf("service discovery de-register instance '%s' fail: %w", instanceId, err)
	} else {
		tryCount := 0

		time.Sleep(250 * time.Millisecond)

		for {
			if status, e := registry.GetOperationStatus(sd, operationId, timeoutDuration); e != nil {
				log.Println("!!! Service Discovery De-Register Instance '" + instanceId + "' Failed: (Deregister GetOperationStatus Action) " + e.Error() + " !!!")
				return fmt.Errorf("service discovery de-register instance '%s' fail: %w", instanceId, e)
			} else {
				if status == sdoperationstatus.Success {
					log.Println("$$$ Service Discovery De-Register Instance '" + instanceId + "' OK $$$")
					return nil
				} else if status == sdoperationstatus.Fail {
					// FIX #11: Permanent failure — stop retrying immediately
					log.Println("!!! Service Discovery De-Register Instance '" + instanceId + "' Failed: Operation Returned Fail Status !!!")
					return fmt.Errorf("service discovery de-register instance '%s' failed with permanent Fail status", instanceId)
				} else {
					// wait 250 ms then retry, up until 20 counts of 250 ms (5 seconds)
					if tryCount < 20 {
						tryCount++
						log.Println("... Checking De-Register Instance '" + instanceId + "' Completion Status, Attempt " + strconv.Itoa(tryCount) + " (250ms)")
						time.Sleep(250 * time.Millisecond)
					} else {
						log.Println("... De-Register Instance '" + instanceId + "' Failed: Operation Timeout After 5 Seconds")
						return fmt.Errorf("service discovery de-register instance '%s' fail when operation timed out after 5 seconds", instanceId)
					}
				}
			}
		}
	}
}
