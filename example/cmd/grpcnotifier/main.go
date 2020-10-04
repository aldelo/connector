package main

import (
	"github.com/aldelo/connector/notifierserver"
	"github.com/aldelo/common/wrapper/systemd"
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
	ns = notifierserver.NewNotifierServer("NotifierServer", "notifier-grpcserver", "notifier-webserver", "")

	if err := ns.Serve(); err != nil {
		log.Println(err)
	}
}

func stopServiceHandler() {
	if ns != nil {
		ns.GracefulStop()
	}
}
