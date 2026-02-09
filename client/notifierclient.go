package client

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
	"github.com/aldelo/common/wrapper/xray"
	notifierpb "github.com/aldelo/connector/notifierserver/proto"
	"google.golang.org/grpc"
	"io"
	"log"
	"strings"
	"time"
)

// HostDiscoveryNotification struct contains the field values for notification discovery payload
//
// `{"msg_type":"host-discovery", "action":"online | offline", "host":"123.123.123.123:9999"}`
type HostDiscoveryNotification struct {
	MsgType string `json:"msg_type"`
	Action  string `json:"action"`
	Host    string `json:"host"`
}

func (d *HostDiscoveryNotification) Marshal() (string, error) {
	if d == nil {
		return "", nil
	}
	buf, err := util.MarshalJSONCompact(d)
	if err != nil {
		return "", err
	}
	return buf, nil
}

func (d *HostDiscoveryNotification) Unmarshal(jsonData string) error {
	if d == nil {
		return nil
	}

	if util.LenTrim(jsonData) == 0 {
		return fmt.Errorf("Unmarshal Requires Json Data")
	}

	err := util.UnmarshalJSON(jsonData, d)
	if err != nil {
		return err
	}
	return nil
}

type NotifierClient struct {
	AppName          string
	ConfigFileName   string
	CustomConfigPath string

	BeforeClientDialHandler  func(*Client)
	AfterClientDialHandler   func(*Client)
	BeforeClientCloseHandler func(*Client)
	AfterClientCloseHandler  func(*Client)

	UnaryClientInterceptorHandlers  []grpc.UnaryClientInterceptor
	StreamClientInterceptorHandlers []grpc.StreamClientInterceptor

	ServiceAlertStartedHandler func()
	ServiceAlertSkippedHandler func(reason string)
	ServiceAlertStoppedHandler func(reason string)
	ServiceHostOnlineHandler   func(host string, port uint)
	ServiceHostOfflineHandler  func(host string, port uint)

	_grpcClient                  *Client
	_subscriberID                string
	_subscriberTopicArn          string
	_notificationServicesStarted bool

	//_stopNotificationServices chan bool
}

// NewNotifierClient creates a new prepared notifier client for use in service discovery notification
func NewNotifierClient(appName string, configFileName string, customConfigPath string, enableLogging ...bool) *NotifierClient {
	var logging bool

	if len(enableLogging) > 0 {
		logging = enableLogging[0]
	} else {
		logging = false
	}

	// set param info into notifier client struct object
	cli := &NotifierClient{
		AppName:          appName,
		ConfigFileName:   configFileName,
		CustomConfigPath: customConfigPath,
	}

	// create new grpc client object in notifier client struct object
	cli._grpcClient = NewClient(cli.AppName, cli.ConfigFileName, cli.CustomConfigPath)

	// always wait for health check pass
	cli._grpcClient.WaitForServerReady = true

	// default before and after handlers
	cli._grpcClient.BeforeClientDial = func(cli *Client) {
		if logging {
			log.Println("Before Notifier Client Dial to Notifier Server...")
		}
	}

	cli._grpcClient.AfterClientDial = func(cli *Client) {
		if logging {
			log.Println("... After Notifier Client Dialed to Notifier Server")
		}
	}

	cli._grpcClient.BeforeClientClose = func(cli *Client) {
		if logging {
			log.Println("Before Notifier Client Disconnects from Notifier Server...")
		}
	}

	cli._grpcClient.AfterClientClose = func(cli *Client) {
		if logging {
			log.Println("... After Notifier Client Disconnected from Notifier Server")
		}
	}

	cli._grpcClient.UnaryClientInterceptors = []grpc.UnaryClientInterceptor{
		func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			if logging {
				log.Println(">>> Unary Client Interceptor Invoked for Method " + method)
			}
			return invoker(ctx, method, req, reply, cc, opts...)
		},
	}

	cli._grpcClient.StreamClientInterceptors = []grpc.StreamClientInterceptor{
		func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			if logging {
				log.Println(">>> Stream Client Interceptor Invoked for Method " + method)
			}
			return streamer(ctx, desc, cc, method, opts...)
		},
	}

	cli.ServiceAlertStartedHandler = func() {
		if logging {
			log.Println("+++ Service Discovery Alert Notification Started +++")
		}
	}

	cli.ServiceAlertSkippedHandler = func(reason string) {
		if logging {
			log.Println("^^^ Service Discovery Alert Notification Skipped: " + reason + " ^^^")
		}
	}

	cli.ServiceAlertStoppedHandler = func(reason string) {
		if logging {
			log.Println("--- Service Discovery Alert Notification Stopped: " + reason + " ---")
		}
	}

	cli.ServiceHostOnlineHandler = func(host string, port uint) {
		if logging {
			log.Println("+++ Service Discovery Host Online Notification: " + fmt.Sprintf("%s:%d", host, port) + " +++")
		}
	}

	cli.ServiceHostOfflineHandler = func(host string, port uint) {
		if logging {
			log.Println("--- Service Discovery Host Offline Notification: " + fmt.Sprintf("%s:%d", host, port) + " ---")
		}
	}

	// return the factory built cli
	return cli
}

// ConfiguredForNotifierClientDial checks if the notifier client is configured for options, where Dial can be attempted to invoke
func (n *NotifierClient) ConfiguredForNotifierClientDial() bool {
	if n == nil {
		return false
	}

	if n._grpcClient == nil {
		return false
	}

	if err := n._grpcClient.PreloadConfigData(); err != nil {
		log.Println("!!! Preload Notifier Client Config Failed: " + err.Error() + " !!!")
		return false
	}

	if !n._grpcClient.ConfiguredForClientDial() {
		log.Println("!!! Notifier Client Config Not Setup for Client Dial Operations Yet !!!")
		return false
	}

	if !n._grpcClient.ConfiguredForSNSDiscoveryTopicArn() {
		log.Println("!!! Notifier Client Config Not Setup for SNS Service Discovery Topic Yet !!!")
		return false
	}

	return true
}

// ConfiguredSNSDiscoveryTopicArn gets the topicArn defined for the notifier client service discovery endpoints
func (n *NotifierClient) ConfiguredSNSDiscoveryTopicArn() string {
	if n == nil {
		return ""
	}

	if n._grpcClient != nil {
		return n._grpcClient.ConfiguredSNSDiscoveryTopicArn()
	} else {
		return ""
	}
}

// NotifierClientAlertServicesStarted indicates notifier client services started via Subscribe() action
func (n *NotifierClient) NotifierClientAlertServicesStarted() bool {
	if n == nil {
		return false
	}

	return n._notificationServicesStarted
}

// PurgeEndpointCache removes current client connection's service name ip port from cache,
// if current service name ip port not found, entire cache will be purged
func (n *NotifierClient) PurgeEndpointCache() {
	if n == nil {
		return
	}

	serviceName := strings.ToLower(n._grpcClient._config.Target.ServiceName + "." + n._grpcClient._config.Target.NamespaceName)
	buf := strings.Split(n._grpcClient._remoteAddress, ":")
	ip := ""
	port := uint(0)
	if len(buf) == 2 {
		ip = buf[0]
		port = util.StrToUint(buf[1])
	}

	if util.LenTrim(serviceName) == 0 || len(ip) == 0 || port == 0 {
		_cache = new(Cache)
	} else if _cache == nil {
		_cache = new(Cache)
	} else {
		_cache.PurgeServiceEndpointByHostAndPort(serviceName, ip, port)
	}
}

// Dial will connect the notifier client to the notifier server
func (n *NotifierClient) Dial() error {
	if n == nil {
		return fmt.Errorf("NotifierClient Object Nil")
	}

	if n._grpcClient == nil {
		return fmt.Errorf("Notifier's gRPC Client is Not Initialized, Obtain via NewNotifierClient Factory Func First")
	}

	if n.BeforeClientDialHandler != nil {
		n._grpcClient.BeforeClientDial = n.BeforeClientDialHandler
	}

	if n.AfterClientDialHandler != nil {
		n._grpcClient.AfterClientDial = n.AfterClientDialHandler
	}

	if n.BeforeClientCloseHandler != nil {
		n._grpcClient.BeforeClientClose = n.BeforeClientCloseHandler
	}

	if n.AfterClientCloseHandler != nil {
		n._grpcClient.AfterClientClose = n.AfterClientCloseHandler
	}

	if len(n.UnaryClientInterceptorHandlers) > 0 {
		n._grpcClient.UnaryClientInterceptors = n.UnaryClientInterceptorHandlers
	}

	if len(n.StreamClientInterceptorHandlers) > 0 {
		n._grpcClient.StreamClientInterceptors = n.StreamClientInterceptorHandlers
	}

	n._notificationServicesStarted = false
	n._subscriberID = ""
	n._subscriberTopicArn = ""

	if err := n._grpcClient.Dial(context.Background()); err != nil {
		n._grpcClient.ZLog().Errorf("!!! Notifier Client Dial Failed: (Connectivity State = %s) %s !!!", n._grpcClient.GetState().String(), err.Error())
		return err
	} else {
		// dial success
		return nil
	}
}

// Close will disconnect the notifier client from the notifier server
func (n *NotifierClient) Close() {
	if n == nil {
		return
	}

	if n._notificationServicesStarted {
		//n._stopNotificationServices <-true
		n._notificationServicesStarted = false
	}

	if n._grpcClient != nil {
		n._grpcClient.Close()
	}

	n._subscriberID = ""
	n._subscriberTopicArn = ""
}

// Subscribe will subscribe this notifier client to a specified topicArn with sns, via notifier server;
// this subscription will also start the recurring loop to wait for notifier server stream data, for receiving service discovery host info;
// when service discovery host info is received, the appropriate ServiceHostOnlineHandler or ServiceHostOfflineHandler is triggered;
// calling the Close() or Unsubscribe() or receiving error conditions from notifier server will sever the long running service discovery process.
func (n *NotifierClient) Subscribe(topicArn string) (err error) {
	if n == nil {
		return fmt.Errorf("NotifierClient Object Nil")
	}

	if n._grpcClient == nil {
		n._notificationServicesStarted = false
		n._subscriberID = ""
		n._subscriberTopicArn = ""
		err = fmt.Errorf("Notifier Client is Not Initialized, Obtain via NewNotifierClient Factory Func First")
		return err
	}

	if util.LenTrim(n._subscriberID) > 0 && util.LenTrim(n._subscriberTopicArn) > 0 {
		err = fmt.Errorf("Notifier Client Subscription Already Engaged, Please Use Unsubscribe() First")
		return err
	} else {
		n._subscriberID = ""
		n._subscriberTopicArn = ""
	}

	if util.LenTrim(topicArn) == 0 {
		n._notificationServicesStarted = false
		err = fmt.Errorf("Notifier Client Subscription Requires Target TopicARN")
		return err
	}

	n._grpcClient.ZLog().Printf("Notifier Client Subscribe to TopicArn '" + topicArn + "' Started...")

	nc := notifierpb.NewNotifierServiceClient(n._grpcClient.ClientConnection())

	sessionId := util.NewULID() + util.GetLocalIP()
	n._grpcClient.ZLog().Printf("Notifier Client " + n.AppName + " Session ID Generated: " + sessionId)

	seg := xray.NewSegmentNullable("GrpcClient-NotifierClient-SubscribeTopic")
	if seg != nil {
		_ = seg.Seg.AddMetadata("GrpcClient-SessionID", sessionId)
		_ = seg.Seg.AddMetadata("Subscribe-TopicARN", topicArn)

		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
			}
		}()
	}

	if nsClient, err := nc.Subscribe(context.Background(), &notifierpb.NotificationSubscriber{
		Id:    sessionId,
		Topic: topicArn,
	}); err != nil {
		n._notificationServicesStarted = false
		n._grpcClient.ZLog().Errorf("!!! Notifier Client Subscribe to TopicArn Failed: " + err.Error() + " !!!")
		err = fmt.Errorf("Notifier Client Subscribe to TopicArn Failed: %s", err.Error())
		return err
	} else {
		if n.ServiceAlertStartedHandler != nil {
			n.ServiceAlertStartedHandler()
		}

		//n._stopNotificationServices = make(chan bool)

		n._grpcClient.ZLog().Printf("+++ Notifier Client Subscribe TopicArn Success +++")

		n._notificationServicesStarted = true
		n._subscriberID = sessionId
		n._subscriberTopicArn = topicArn

		ctxDone := nsClient.Context()
		recvMap := make(map[string]time.Time)

		for {
			select {
			case <-ctxDone.Done():
				if n.ServiceAlertStoppedHandler != nil {
					n.ServiceAlertStoppedHandler("Notification Alert Services Stopped")
				}

				n._notificationServicesStarted = false

				n._grpcClient.ZLog().Printf("### Notifier Client Received Context Done Signal ###")

				recvMap = nil
				err = fmt.Errorf("Notifier Client Context Done")
				return err

			/*
				case <-n._stopNotificationServices:
					if n.ServiceAlertStoppedHandler != nil {
						n.ServiceAlertStoppedHandler("Notification Alert Services Stopped")
					}

					n._notificationServicesStarted = false

					n._grpcClient.ZLog().Printf("### Notifier Client Received Stop Notification Services Signal ###")

					recvMap = nil
					err = fmt.Errorf("Notifier Client Received Stop Notification Signal")
					return err
			*/

			default:
				// process notification receive event
				n._grpcClient.ZLog().Printf("~~~ Notifier Client Awaits Notifier Server's Notification Data Arrival ~~~")

				if data, err := nsClient.Recv(); err == nil {
					n._grpcClient.ZLog().Printf("$$$ Notifier Client Received Notification Data From Server Stream, Ready to Process $$$")

					if data != nil {
						// notification data received from host stream provider
						n._grpcClient.ZLog().Printf("$$$ Received Server Stream Notification Data Not Nil $$$")

						if data.Topic != topicArn {
							n._grpcClient.ZLog().Printf("!!! Notifier Client Received Notification Data's TopicArn Mismatch: Received " + data.Topic + ", Expected " + topicArn + ", Recv Loop Skips to Next Cycle !!!")

							if n.ServiceAlertSkippedHandler != nil {
								n.ServiceAlertSkippedHandler("Received Topic " + data.Topic + ", Expected Topic " + topicArn)
							}

							continue
						}

						// evaluate sns callback relay message
						// Id = sns message id (assigned by sns) < Id is not used in this code block
						// Timestamp = sns message timestamp, formatted as "yyyy-mm-ddThh:mm:ss.mmmZ"
						// Message = sns message content:
						// 			 `{"msg_type":"host-discovery", "action":"online | offline", "host":"123.123.123.123"}`
						if util.LenTrim(data.Message) == 0 {
							n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Data's Message is Blank, Recv Loop Skips to Next Cycle !!!")

							if n.ServiceAlertSkippedHandler != nil {
								n.ServiceAlertSkippedHandler("Notification Message is Blank")
							}

							continue
						}

						// ensure message was within the last 15 minutes
						// t1 is utc value
						// t2 converts to utc for comparison
						if t1, err := time.Parse(time.RFC3339, data.Timestamp); err != nil {
							n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Timestamp Parser Not Valid: " + err.Error() + "， Recv Loop Skips to Next Cycle !!!")

							if n.ServiceAlertSkippedHandler != nil {
								n.ServiceAlertSkippedHandler("Notification Timestamp Parse Error: " + err.Error())
							}

							continue
						} else {
							t2 := time.Now().UTC()

							if util.AbsDuration(t2.Sub(t1)).Minutes() > 15 {
								n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Timestamp Exceeded 15 Minute Limit: Message Timestamp " + util.FormatDateTime(t1) + ", Current Timestamp " + util.FormatDateTime(t2) + ", Recv Loop Skips to Next Cycle !!!")

								if n.ServiceAlertSkippedHandler != nil {
									n.ServiceAlertSkippedHandler("Notification Expired (Exceeded 15 Minutes): Received " + util.FormatDateTime(t1) + ", Current " + util.FormatDateTime(t2))
								}

								continue
							}
						}

						// unmarshal message to host discovery notification object
						hostDiscNotification := new(HostDiscoveryNotification)

						if err := hostDiscNotification.Unmarshal(data.Message); err != nil {
							n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Unmarshal Json Failed: " + err.Error() + ", Recv Loop Skips to Next Cycle !!!")

							if n.ServiceAlertSkippedHandler != nil {
								n.ServiceAlertSkippedHandler("Notification Message Unmarshal Failed: " + err.Error())
							}

							continue
						}

						// received host discovery notification, push out for event alert
						if strings.ToUpper(hostDiscNotification.MsgType) == "HOST-DISCOVERY" {
							if ipPort := strings.Split(hostDiscNotification.Host, ":"); len(ipPort) != 2 {
								n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Host Not in IP:Port Format: Received '" + hostDiscNotification.Host + "', Recv Loop Skips to Next Cycle !!!")

								if n.ServiceAlertSkippedHandler != nil {
									n.ServiceAlertSkippedHandler("Notification Host Not in IP:Port Format: Received '" + hostDiscNotification.Host + "'")
								}

								continue
							} else {
								ip := ipPort[0]
								port := util.StrToUint(ipPort[1])

								if ipParts := strings.Split(ip, "."); len(ipParts) != 4 || !util.IsNumericIntOnly(strings.Replace(ip, ".", "", -1)) {
									n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Host IP Not Valid: Received '" + ip + "', Recv Loop Skips to Next Cycle !!!")

									if n.ServiceAlertSkippedHandler != nil {
										n.ServiceAlertSkippedHandler("Notification Host IP Not Valid: Received '" + ip + "'")
									}

									continue
								}

								if port <= 0 || port > 65535 {
									n._grpcClient.ZLog().Warnf("!!! Notification Client Received Notification Host Port Not Valid: Received '" + util.UintToStr(port) + "', Recv Loop Skips to Next Cycle !!!")

									if n.ServiceAlertSkippedHandler != nil {
										n.ServiceAlertSkippedHandler("Notification Host Port Not Valid: Received '" + util.UintToStr(port) + "'")
									}

									continue
								}

								// check if already received within the last 10 seconds
								recvKey := fmt.Sprintf("%s:%d", ip, port)
								if t, ok := recvMap[recvKey]; ok && util.AbsInt(util.SecondsDiff(time.Now(), t)) <= 10 {
									// already in map, skip this one
									n._grpcClient.ZLog().Warnf("*** Notification Client Received Repeated Notification Same Data '" + recvKey + "' Within 10 Seconds Duration, Alert Bypassed ***")
									continue
								} else {
									// add or update to map
									if len(recvMap) > 5000 {
										// if map exceed 5000 entries, reset
										recvMap = make(map[string]time.Time)
									}
									recvMap[recvKey] = time.Now()
								}

								isOnline := strings.ToUpper(hostDiscNotification.Action) == "ONLINE"

								// notify the discovered host
								if isOnline {
									if n.ServiceHostOnlineHandler != nil {
										n.ServiceHostOnlineHandler(ip, port)
									}
								} else {
									if n.ServiceHostOfflineHandler != nil {
										n.ServiceHostOfflineHandler(ip, port)
									}
								}

								n._grpcClient.ZLog().Printf("### Notifier Client Received Notification Data Exposed to Handler, Now Ready for Next Recv Event ###")
							}
						} else {
							n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Message Type Not Expected: Received '" + hostDiscNotification.MsgType + "', Expected 'host-discovery', Recv Loop Skips to Next Cycle !!!")

							if n.ServiceAlertSkippedHandler != nil {
								n.ServiceAlertSkippedHandler("Notification Message Type Not Expected: 'Received " + hostDiscNotification.MsgType + "', Expected 'host-discovery'")
							}

							continue
						}
					} else {
						// notify nil data received
						n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Data is Nil, Recv Loop Skips to Next Cycle !!!")

						if n.ServiceAlertSkippedHandler != nil {
							n.ServiceAlertSkippedHandler("Notification Data Nil")
						}

						continue
					}
				} else {
					// on error exit stream client loop
					if err == io.EOF {
						n._grpcClient.ZLog().Printf("!!! Notifier Client Stream Receive Action Encountered EOF, Recv Loop Ending !!!")

						if n.ServiceAlertStoppedHandler != nil {
							n.ServiceAlertStoppedHandler("Alert Service Stopped Due To Notifier Server Stream EOF")
						}

						n._notificationServicesStarted = false
						err = fmt.Errorf("Notifier Client Received EOF, Recv Loop Ending")
						return err

					} else {
						// other error, continue
						n._grpcClient.ZLog().Errorf("!!! Notifier Client Stream Receive Action Encountered Error: " + err.Error() + ", Recv Loop Ending !!!")

						if n.ServiceAlertStoppedHandler != nil {
							n.ServiceAlertStoppedHandler("Alert Service Stopped Due To Notifier Server Stream Error: " + err.Error())
						}

						n._notificationServicesStarted = false
						err = fmt.Errorf("Notifier Client Received Error: " + err.Error() + ", Recv Loop Ending")
						return err

					}
				}
			}
		}
	}
}

// Unsubscribe will stop notification alert services and disconnect from subscription on notifier server
func (n *NotifierClient) Unsubscribe() (err error) {
	if n == nil {
		return fmt.Errorf("NotifierClient Object Nil")
	}

	if n._grpcClient == nil {
		err = fmt.Errorf("Notifier Client is Not Initialized, Obtain via NewNotifierClient Factory Func First")
		return err
	}

	n._grpcClient.ZLog().Printf("Notifier Client Unsubscribe Started...")

	// first, stop notification alert services
	if n._notificationServicesStarted {
		//n._stopNotificationServices <-true
		n._notificationServicesStarted = false
	}

	// second, perform unsubscribe?
	if util.LenTrim(n._subscriberID) == 0 || util.LenTrim(n._subscriberTopicArn) == 0 {
		n._subscriberID = ""
		n._subscriberTopicArn = ""
		n._grpcClient.ZLog().Errorf("!!! Notifier Client Unsubscribe Failed: Subscriber ID and/or TopicArn Not Found, Client Not Yet Subscribed !!!")
		err = fmt.Errorf("Notifier Client Subscriber ID and TopicArn Not Found, Client Not Yet Subscribed")
		return err
	}

	seg := xray.NewSegmentNullable("GrpcClient-NotifierClient-Unsubscribe")
	if seg != nil {
		_ = seg.Seg.AddMetadata("GrpcClient-SessionID", n._subscriberID)
		_ = seg.Seg.AddMetadata("Subscribe-TopicARN", n._subscriberTopicArn)

		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
			}
		}()
	}

	// perform unsubscribe action
	nc := notifierpb.NewNotifierServiceClient(n._grpcClient.ClientConnection())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err = nc.Unsubscribe(ctx, &notifierpb.NotificationSubscriber{
		Id:    n._subscriberID,
		Topic: n._subscriberTopicArn,
	}); err != nil {
		n._grpcClient.ZLog().Errorf("!!! Notifier Client Unsubscribe Client ID '" + n._subscriberID + "' From TopicArn '" + n._subscriberTopicArn + "' Failed: " + err.Error() + " !!!")
		err = fmt.Errorf("Notifier Client ID %s Unsubscribe From TopicARN %s Failed: %s", n._subscriberID, n._subscriberTopicArn, err.Error())
		return err
	} else {
		// unsubscribe ok
		n._grpcClient.ZLog().Printf("### Notifier Client Unsubscribe Client ID '" + n._subscriberID + "' From TopicArn '" + n._subscriberTopicArn + "' Success ###")
		n._subscriberID = ""
		n._subscriberTopicArn = ""
		return nil
	}
}
