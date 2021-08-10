package main

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

import (
	"github.com/aldelo/common/wrapper/systemd"
	"github.com/aldelo/connector/notifierserver"
	"github.com/aldelo/connector/service"
	"log"
)

var ns *service.Service

func main() {
	svc := &systemd.ServiceProgram{
		ServiceName: "NotifierService",
		DisplayName: "Notifier Service",
		Description: "Provides Http to gRPC Notification Services",
		StartServiceHandler: startServiceHandler,
		StopServiceHandler: stopServiceHandler,
	}

	svc.Launch()
}

func startServiceHandler(port int) {
	var err error

	if ns, err = notifierserver.NewNotifierServer("NotifierServer", "notifier-grpcserver", "notifier-webserver", "notifier-config", ""); err != nil {
		log.Fatalln(err)
	}

	if err = ns.Serve(); err != nil {
		log.Println(err)
	}
}

func stopServiceHandler() {
	// unsubscribe all subscriptions
	notifierserver.UnsubscribeAllTopics()

	// stop service
	if ns != nil {
		ns.GracefulStop()
	}
}
