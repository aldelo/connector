package notifierserver

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
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginbindtype"
	"github.com/aldelo/common/wrapper/gin/ginhttpmethod"
	"github.com/aldelo/connector/notifierserver/impl"
	pb "github.com/aldelo/connector/notifierserver/proto"
	"github.com/aldelo/connector/service"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/grpclog"
	"time"
)

var notifierServer *impl.NotifierImpl

type snsNotification struct {
	Type string					`json:"Type"`
	MessageId string			`json:"MessageId"`
	TopicArn string				`json:"TopicArn"`
	Subject string				`json:"Subject"`
	Message string				`json:"Message"`
	Timestamp string			`json:"Timestamp"`
	SignatureVersion string		`json:"SignatureVersion"`
	Signature string			`json:"Signature"`
	SigningCertURL string		`json:"SigningCertURL"`
	UnsubscribeURL string		`json:"UnsubscribeURL"`
}

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
			pb.RegisterNotifierServer(grpcServer, notifierServer)
		})

	svr.WebServerConfig = &service.WebServerConfig{
		AppName: appName,
		ConfigFileName: configFileNameWebServer,
		CustomConfigPath: "",
		WebServerRoutes: map[string]*ginw.RouteDefinition{
			"base": {
				Routes: []*ginw.Route{
					{
						RelativePath: "/snsrelay",
						Method: ginhttpmethod.POST,
						Binding: ginbindtype.BindJson,
						BindingInputPtr: &snsNotification{},
						Handler: snsrelay,
					},
				},
				CorsMiddleware: &cors.Config{},
			},
		},
	}

	notifierServer.WebServerLocalAddressFunc = svr.WebServerConfig.GetWebServerLocalAddress

	// clean up prior sns subscriptions logged in config, upon initial launch
	notifierServer.UnsubscribeAllPrior()

	return svr, nil
}

func snsrelay(c *gin.Context, bindingInputPtr interface{}) {
	if notifierServer == nil {
		c.String(404, "notifierServer Not Exist")
		return
	}

	n, ok := bindingInputPtr.(*snsNotification)

	if !ok {
		c.String(500, "Assert SNS Notification Json Failed")
	}

	if util.LenTrim(n.Message) > 0 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if _, err := notifierServer.Broadcast(ctx, &pb.NotificationData{
				Id: n.MessageId,
				Topic: n.TopicArn,
				Message: n.Message,
				Timestamp: n.Timestamp,
			}); err != nil {
				// broadcast error encountered
				grpclog.Errorf("SNS Relay Failed: %s", err.Error())
			}
		}()
	}

	c.String(200, "SNS Relay Sent")
}