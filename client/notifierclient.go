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
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/xray"
	notifierpb "github.com/aldelo/connector/notifierserver/proto"
	"google.golang.org/grpc"
)

// recvMapMaxSize is the maximum number of entries in the deduplication map
// before a full reset is performed. Prevents unbounded memory growth.
const recvMapMaxSize = 2000

// recvMapCleanupInterval controls how often (in processed messages) we scan
// the deduplication map for stale entries.
const recvMapCleanupInterval = 100

// recvMapEntryTTL is how long a deduplication entry stays valid.
const recvMapEntryTTL = 60 * time.Second

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
	_subscribeCancel             context.CancelFunc // cancels the Subscribe gRPC stream
	_subMu                       sync.Mutex         // protects _subscriberID, _subscriberTopicArn, and _subscribeCancel
	_notificationServicesStarted int32              // Use int32 for atomic operations
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

	return atomic.LoadInt32(&n._notificationServicesStarted) == 1
}

// PurgeEndpointCache removes current client connection's service name ip port from cache,
// if current service name ip port not found, entire cache will be purged.
//
// FIX #1: Uses RemoteAddress() (which holds connMu) instead of directly accessing _remoteAddress.
// FIX #2: Uses cacheMu-protected helpers instead of directly accessing _cache.
func (n *NotifierClient) PurgeEndpointCache() {
	if n == nil {
		return
	}

	// CL-F4: snapshot _config via atomic.Pointer so the nil check and
	// field deref cannot race against a Dial error path storing nil.
	cfg := n._grpcClient.getConfig()
	if n._grpcClient == nil || cfg == nil {
		ClearEndpointCache()
		return
	}

	serviceName := strings.ToLower(cfg.Target.ServiceName + "." + cfg.Target.NamespaceName)

	// FIX #1: Use the thread-safe RemoteAddress() getter instead of direct field access
	remoteAddr := n._grpcClient.RemoteAddress()

	// FIX: Use net.SplitHostPort instead of strings.Split to handle IPv6 addresses
	ip := ""
	port := uint(0)
	if host, portStr, splitErr := net.SplitHostPort(remoteAddr); splitErr == nil {
		ip = host
		port = util.StrToUint(portStr)
	}

	if util.LenTrim(serviceName) == 0 || len(ip) == 0 || port == 0 {
		// FIX #2: Use the thread-safe ClearEndpointCache() instead of direct _cache assignment
		ClearEndpointCache()
	} else {
		// FIX #2: Use the thread-safe cache helper instead of direct _cache access
		cachePurgeServiceEndpointByHostAndPort(serviceName, ip, port)
	}
}

// callAlertSkipped invokes ServiceAlertSkippedHandler with panic recovery (CL-F5).
// User-supplied handler panics must not propagate up through Subscribe →
// DoNotifierAlertService → reconnect goroutine and crash the process.
func (n *NotifierClient) callAlertSkipped(reason string) {
	if n == nil || n.ServiceAlertSkippedHandler == nil {
		return
	}
	safeCall("ServiceAlertSkippedHandler", func() { n.ServiceAlertSkippedHandler(reason) })
}

// callAlertStopped invokes ServiceAlertStoppedHandler with panic recovery (CL-F5).
// User-supplied handler panics must not propagate up through Subscribe →
// DoNotifierAlertService → reconnect goroutine and crash the process.
func (n *NotifierClient) callAlertStopped(reason string) {
	if n == nil || n.ServiceAlertStoppedHandler == nil {
		return
	}
	safeCall("ServiceAlertStoppedHandler", func() { n.ServiceAlertStoppedHandler(reason) })
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

	atomic.StoreInt32(&n._notificationServicesStarted, 0)
	n._subMu.Lock()
	if n._subscribeCancel != nil {
		n._subscribeCancel()
		n._subscribeCancel = nil
	}
	n._subscriberID = ""
	n._subscriberTopicArn = ""
	n._subMu.Unlock()

	if err := n._grpcClient.Dial(context.Background()); err != nil {
		if n._grpcClient.ZLog() != nil {
			n._grpcClient.ZLog().Errorf("!!! Notifier Client Dial Failed: (Connectivity State = %s) %s !!!", n._grpcClient.GetState().String(), err.Error())
		} else {
			log.Printf("!!! Notifier Client Dial Failed: %s !!!", err.Error())
		}
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

	if atomic.LoadInt32(&n._notificationServicesStarted) == 1 {
		atomic.StoreInt32(&n._notificationServicesStarted, 0)
	}

	// FIX #25: Cancel the subscribe stream context to stop the Recv loop
	n._subMu.Lock()
	if n._subscribeCancel != nil {
		n._subscribeCancel()
		n._subscribeCancel = nil
	}
	n._subscriberID = ""
	n._subscriberTopicArn = ""
	n._subMu.Unlock()

	if n._grpcClient != nil {
		n._grpcClient.Close()
	}
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
		atomic.StoreInt32(&n._notificationServicesStarted, 0)
		n._subMu.Lock()
		n._subscriberID = ""
		n._subscriberTopicArn = ""
		n._subMu.Unlock()
		err = fmt.Errorf("Notifier Client is Not Initialized, Obtain via NewNotifierClient Factory Func First")
		return err
	}

	n._subMu.Lock()
	if util.LenTrim(n._subscriberID) > 0 && util.LenTrim(n._subscriberTopicArn) > 0 {
		n._subMu.Unlock()
		err = fmt.Errorf("Notifier Client Subscription Already Engaged, Please Use Unsubscribe() First")
		return err
	}
	n._subscriberID = ""
	n._subscriberTopicArn = ""
	n._subMu.Unlock()

	if util.LenTrim(topicArn) == 0 {
		atomic.StoreInt32(&n._notificationServicesStarted, 0)
		err = fmt.Errorf("Notifier Client Subscription Requires Target TopicARN")
		return err
	}

	n._grpcClient.ZLog().Printf("Notifier Client Subscribe to TopicArn '" + topicArn + "' Started...")

	conn := n._grpcClient.ClientConnection()
	if conn == nil {
		atomic.StoreInt32(&n._notificationServicesStarted, 0)
		err = fmt.Errorf("Notifier Client Subscribe Failed: gRPC client connection is nil")
		return err
	}
	nc := notifierpb.NewNotifierServiceClient(conn)

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

	// FIX #25: Use a cancellable context so Close() can stop the stream
	subCtx, subCancel := context.WithCancel(context.Background())
	n._subMu.Lock()
	n._subscribeCancel = subCancel
	n._subMu.Unlock()

	var nsClient notifierpb.NotifierService_SubscribeClient
	nsClient, err = nc.Subscribe(subCtx, &notifierpb.NotificationSubscriber{
		Id:    sessionId,
		Topic: topicArn,
	})
	if err != nil {
		// Cancel the context and clear the stored cancel to prevent context leak
		subCancel()
		n._subMu.Lock()
		n._subscribeCancel = nil
		n._subMu.Unlock()

		atomic.StoreInt32(&n._notificationServicesStarted, 0)
		n._grpcClient.ZLog().Errorf("!!! Notifier Client Subscribe to TopicArn Failed: " + err.Error() + " !!!")
		err = fmt.Errorf("Notifier Client Subscribe to TopicArn Failed: %w", err)
		return err
	}

	// CL-F5: panic-safe dispatch to user callback.
	if n.ServiceAlertStartedHandler != nil {
		safeCall("ServiceAlertStartedHandler", n.ServiceAlertStartedHandler)
	}

	n._grpcClient.ZLog().Printf("+++ Notifier Client Subscribe TopicArn Success +++")

	atomic.StoreInt32(&n._notificationServicesStarted, 1)
	n._subMu.Lock()
	n._subscriberID = sessionId
	n._subscriberTopicArn = topicArn
	n._subMu.Unlock()

	ctxDone := nsClient.Context()

	// FIX #3 & #4: Consolidated deduplication map management.
	// Single cleanup threshold (recvMapMaxSize), single cleanup interval (recvMapCleanupInterval),
	// and removed duplicate recvMap[recvKey] = now write.
	recvMap := make(map[string]time.Time)
	cleanupCounter := 0

	for {
		select {
		case <-ctxDone.Done():
			// CL-F5: panic-safe dispatch to user callback.
			n.callAlertStopped("Notification Alert Services Stopped")

			atomic.StoreInt32(&n._notificationServicesStarted, 0)
			n._subMu.Lock()
			if n._subscribeCancel != nil {
				n._subscribeCancel()
				n._subscribeCancel = nil
			}
			n._subscriberID = ""
			n._subscriberTopicArn = ""
			n._subMu.Unlock()

			n._grpcClient.ZLog().Printf("### Notifier Client Received Context Done Signal ###")

			recvMap = nil
			err = fmt.Errorf("Notifier Client Context Done")
			return err

		default:
			// process notification receive event
			n._grpcClient.ZLog().Printf("~~~ Notifier Client Awaits Notifier Server's Notification Data Arrival ~~~")

			var data *notifierpb.NotificationData
			data, err = nsClient.Recv()
			if err == nil {
				n._grpcClient.ZLog().Printf("$$$ Notifier Client Received Notification Data From Server Stream, Ready to Process $$$")

				if data != nil {
					// notification data received from host stream provider
					n._grpcClient.ZLog().Printf("$$$ Received Server Stream Notification Data Not Nil $$$")

					if data.Topic != topicArn {
						n._grpcClient.ZLog().Printf("!!! Notifier Client Received Notification Data's TopicArn Mismatch: Received " + data.Topic + ", Expected " + topicArn + ", Recv Loop Skips to Next Cycle !!!")

						// CL-F5: panic-safe dispatch to user callback.
						n.callAlertSkipped("Received Topic " + data.Topic + ", Expected Topic " + topicArn)

						continue
					}

					if util.LenTrim(data.Message) == 0 {
						n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Data's Message is Blank, Recv Loop Skips to Next Cycle !!!")

						// CL-F5: panic-safe dispatch to user callback.
						n.callAlertSkipped("Notification Message is Blank")

						continue
					}

					// ensure message was within the last 15 minutes
					var t1 time.Time
					var parseErr error
					t1, parseErr = time.Parse(time.RFC3339, data.Timestamp)
					if parseErr != nil {
						n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Timestamp Parser Not Valid: " + parseErr.Error() + "， Recv Loop Skips to Next Cycle !!!")

						// CL-F5: panic-safe dispatch to user callback.
						n.callAlertSkipped("Notification Timestamp Parse Error: " + parseErr.Error())

						continue
					}

					t2 := time.Now().UTC()

					if util.AbsDuration(t2.Sub(t1)).Minutes() > 15 {
						n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Timestamp Exceeded 15 Minute Limit: Message Timestamp " + util.FormatDateTime(t1) + ", Current Timestamp " + util.FormatDateTime(t2) + ", Recv Loop Skips to Next Cycle !!!")

						// CL-F5: panic-safe dispatch to user callback.
						n.callAlertSkipped("Notification Expired (Exceeded 15 Minutes): Received " + util.FormatDateTime(t1) + ", Current " + util.FormatDateTime(t2))

						continue
					}

					// unmarshal message to host discovery notification object
					hostDiscNotification := new(HostDiscoveryNotification)

					var unmarshalErr error
					unmarshalErr = hostDiscNotification.Unmarshal(data.Message)
					if unmarshalErr != nil {
						n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Unmarshal Json Failed: " + unmarshalErr.Error() + ", Recv Loop Skips to Next Cycle !!!")

						// CL-F5: panic-safe dispatch to user callback.
						n.callAlertSkipped("Notification Message Unmarshal Failed: " + unmarshalErr.Error())

						continue
					}

					// received host discovery notification, push out for event alert
					if strings.ToUpper(hostDiscNotification.MsgType) == "HOST-DISCOVERY" {
						host, portStr, splitErr := net.SplitHostPort(hostDiscNotification.Host)
						if splitErr != nil {
							n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Host Not in IP:Port Format: Received '" + hostDiscNotification.Host + "', Recv Loop Skips to Next Cycle !!!")

							// CL-F5: panic-safe dispatch to user callback.
							n.callAlertSkipped("Notification Host Not in IP:Port Format: Received '" + hostDiscNotification.Host + "'")

							continue
						}

						{
							ip := host
							port := util.StrToUint(portStr)

							if net.ParseIP(ip) == nil {
								n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Host IP Not Valid: Received '" + ip + "', Recv Loop Skips to Next Cycle !!!")

								// CL-F5: panic-safe dispatch to user callback.
								n.callAlertSkipped("Notification Host IP Not Valid: Received '" + ip + "'")

								continue
							}

							if port == 0 || port > 65535 {
								n._grpcClient.ZLog().Warnf("!!! Notification Client Received Notification Host Port Not Valid: Received '" + util.UintToStr(port) + "', Recv Loop Skips to Next Cycle !!!")

								// CL-F5: panic-safe dispatch to user callback.
								n.callAlertSkipped("Notification Host Port Not Valid: Received '" + util.UintToStr(port) + "'")

								continue
							}

							// check if already received within the last 10 seconds (deduplication)
							// Include action in key so ONLINE and OFFLINE for the same host are not deduplicated
							recvKey := fmt.Sprintf("%s:%d:%s", ip, port, strings.ToUpper(hostDiscNotification.Action))
							now := time.Now()
							if t, ok := recvMap[recvKey]; ok && util.AbsInt(util.SecondsDiff(now, t)) <= 10 {
								// already in map within dedup window, skip
								n._grpcClient.ZLog().Warnf("*** Notification Client Received Repeated Notification Same Data '" + recvKey + "' Within 10 Seconds Duration, Alert Bypassed ***")
								continue
							}

							// FIX #3: Single write to recvMap (was written twice in original code)
							recvMap[recvKey] = now

							// FIX #4: Unified cleanup with single threshold
							cleanupCounter++
							if cleanupCounter >= recvMapCleanupInterval || len(recvMap) > recvMapMaxSize {
								cleanupCounter = 0
								for k, v := range recvMap {
									if now.Sub(v) > recvMapEntryTTL {
										delete(recvMap, k)
									}
								}
								// If still over capacity after TTL cleanup, hard reset
								if len(recvMap) > recvMapMaxSize {
									recvMap = make(map[string]time.Time)
								}
							}

							isOnline := strings.ToUpper(hostDiscNotification.Action) == "ONLINE"

							// notify the discovered host
							// CL-F5: wrap in safeCall so a panic in a
							// user-supplied handler does not propagate
							// back up through Subscribe into the
							// reconnect goroutine and crash the process.
							if isOnline {
								if n.ServiceHostOnlineHandler != nil {
									safeCall("ServiceHostOnlineHandler", func() { n.ServiceHostOnlineHandler(ip, port) })
								}
							} else {
								if n.ServiceHostOfflineHandler != nil {
									safeCall("ServiceHostOfflineHandler", func() { n.ServiceHostOfflineHandler(ip, port) })
								}
							}

							n._grpcClient.ZLog().Printf("### Notifier Client Received Notification Data Exposed to Handler, Now Ready for Next Recv Event ###")
						}
					} else {
						n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Message Type Not Expected: Received '" + hostDiscNotification.MsgType + "', Expected 'host-discovery', Recv Loop Skips to Next Cycle !!!")

						// CL-F5: panic-safe dispatch to user callback.
						n.callAlertSkipped("Notification Message Type Not Expected: 'Received " + hostDiscNotification.MsgType + "', Expected 'host-discovery'")

						continue
					}
				} else {
					// notify nil data received
					n._grpcClient.ZLog().Warnf("!!! Notifier Client Received Notification Data is Nil, Recv Loop Skips to Next Cycle !!!")

					// CL-F5: panic-safe dispatch to user callback.
					n.callAlertSkipped("Notification Data Nil")

					continue
				}
			} else {
				// on error exit stream client loop
				if err == io.EOF {
					n._grpcClient.ZLog().Printf("!!! Notifier Client Stream Receive Action Encountered EOF, Recv Loop Ending !!!")

					// CL-F5: panic-safe dispatch to user callback.
					n.callAlertStopped("Alert Service Stopped Due To Notifier Server Stream EOF")

					atomic.StoreInt32(&n._notificationServicesStarted, 0)
					n._subMu.Lock()
					if n._subscribeCancel != nil {
						n._subscribeCancel()
						n._subscribeCancel = nil
					}
					n._subscriberID = ""
					n._subscriberTopicArn = ""
					n._subMu.Unlock()
					err = fmt.Errorf("Notifier Client Received EOF, Recv Loop Ending")
					return err

				} else {
					// other error, continue
					n._grpcClient.ZLog().Errorf("!!! Notifier Client Stream Receive Action Encountered Error: " + err.Error() + ", Recv Loop Ending !!!")

					// CL-F5: panic-safe dispatch to user callback.
					n.callAlertStopped("Alert Service Stopped Due To Notifier Server Stream Error: " + err.Error())

					atomic.StoreInt32(&n._notificationServicesStarted, 0)
					n._subMu.Lock()
					if n._subscribeCancel != nil {
						n._subscribeCancel()
						n._subscribeCancel = nil
					}
					n._subscriberID = ""
					n._subscriberTopicArn = ""
					n._subMu.Unlock()
					err = fmt.Errorf("notifier client received error: %w, recv loop ending", err)
					return err

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
	if atomic.LoadInt32(&n._notificationServicesStarted) == 1 {
		atomic.StoreInt32(&n._notificationServicesStarted, 0)
	}

	// second, perform unsubscribe?
	// FIX #17: Protect _subscriberID and _subscriberTopicArn with _subMu
	n._subMu.Lock()
	subID := n._subscriberID
	subTopic := n._subscriberTopicArn
	subCancel := n._subscribeCancel
	if util.LenTrim(subID) == 0 || util.LenTrim(subTopic) == 0 {
		if subCancel != nil {
			subCancel()
		}
		n._subscriberID = ""
		n._subscriberTopicArn = ""
		n._subscribeCancel = nil
		n._subMu.Unlock()
		n._grpcClient.ZLog().Errorf("!!! Notifier Client Unsubscribe Failed: Subscriber ID and/or TopicArn Not Found, Client Not Yet Subscribed !!!")
		err = fmt.Errorf("Notifier Client Subscriber ID and TopicArn Not Found, Client Not Yet Subscribed")
		return err
	}
	n._subMu.Unlock()

	seg := xray.NewSegmentNullable("GrpcClient-NotifierClient-Unsubscribe")
	if seg != nil {
		_ = seg.Seg.AddMetadata("GrpcClient-SessionID", subID)
		_ = seg.Seg.AddMetadata("Subscribe-TopicARN", subTopic)

		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
			}
		}()
	}

	// perform unsubscribe action
	conn := n._grpcClient.ClientConnection()
	if conn == nil {
		if subCancel != nil {
			subCancel()
		}
		n._subMu.Lock()
		n._subscriberID = ""
		n._subscriberTopicArn = ""
		n._subscribeCancel = nil
		n._subMu.Unlock()
		err = fmt.Errorf("Notifier Client Unsubscribe Failed: gRPC client connection is nil")
		return err
	}
	nc := notifierpb.NewNotifierServiceClient(conn)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err = nc.Unsubscribe(ctx, &notifierpb.NotificationSubscriber{
		Id:    subID,
		Topic: subTopic,
	}); err != nil {
		n._grpcClient.ZLog().Errorf("!!! Notifier Client Unsubscribe Client ID '" + subID + "' From TopicArn '" + subTopic + "' Failed: " + err.Error() + " !!!")
		// Cancel context and clear state even on failure to prevent resource leak
		if subCancel != nil {
			subCancel()
		}
		n._subMu.Lock()
		n._subscriberID = ""
		n._subscriberTopicArn = ""
		n._subscribeCancel = nil
		n._subMu.Unlock()
		err = fmt.Errorf("Notifier Client ID %s Unsubscribe From TopicARN %s Failed: %w", subID, subTopic, err)
		return err
	} else {
		// unsubscribe ok
		n._grpcClient.ZLog().Printf("### Notifier Client Unsubscribe Client ID '" + subID + "' From TopicArn '" + subTopic + "' Success ###")
		// FIX: Cancel the subscribe stream context to stop the Recv loop goroutine
		if subCancel != nil {
			subCancel()
		}
		n._subMu.Lock()
		n._subscriberID = ""
		n._subscriberTopicArn = ""
		n._subscribeCancel = nil
		n._subMu.Unlock()
		return nil
	}
}
