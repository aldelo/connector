package main

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
	"github.com/aldelo/connector/notifiergateway"
	"github.com/aldelo/common/wrapper/systemd"
	"github.com/aldelo/connector/webserver"
	"log"
)

var ng *webserver.WebServer

func main() {
	svc := &systemd.ServiceProgram{
		ServiceName: "NotifierGateway",
		DisplayName: "Notifier Gateway",
		Description: "Provides Http(s) Endpoint for SNS Notification Callbacks",
		StartServiceHandler: startServiceHandler,
		StopServiceHandler: nil,
	}

	svc.Launch()
}

func startServiceHandler(port int) {
	var err error

	if ng, err = notifiergateway.NewNotifierGateway("NotifierGateway", "gateway-webserver", "gateway-config", ""); err != nil {
		log.Fatalln(err)
	}

	if err = ng.Serve(); err != nil {
		log.Println(err)
	}
}
