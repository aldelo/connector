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
	"context"
	"fmt"
	"github.com/aldelo/common/wrapper/systemd"
	"github.com/aldelo/connector/example/cmd/server/impl"
	"github.com/aldelo/connector/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"log"
	"time"

	testpb "github.com/aldelo/connector/example/proto/test"
)

// define package level var for grpcServer object
var (
	grpcServer *service.Service
)

// example gRPC server using connector service
func main() {
	// ServiceProgram prepares this program to be executed as a systemd / launchd / windows service
	// ServiceProgram wraps the service running feature provided by the Kardianos Service Package
	// the actual gRPC server setup and startup code is executed within the ServiceHandler func
	prog := &systemd.ServiceProgram{
		ServiceName: "ExampleService",
		DisplayName: "gRPC Example Service",
		Description: "Demo of gRPC Example Service",
		StartServiceHandler: startServiceHandler,
		StopServiceHandler: stopServiceHandler,
	}

	// launch the service program,
	// which triggers the gRPC server setup and launch as well
	prog.Launch()
}

// startServiceHandler implements the gRPC service startup
// startServiceHandler is to be called by ServiceProgram
func startServiceHandler(port int) {
	// setup grpc service server
	grpcServer = service.NewService("ExampleServer", "service", "", func(grpcServer *grpc.Server) {
		testpb.RegisterAnswerServiceServer(grpcServer, &impl.TestServiceImpl{})
	})

	// code to execute before server starts
	grpcServer.BeforeServerStart = func(svc *service.Service) {
		log.Println("Before Server Start...")
	}

	// code to execute after server started
	grpcServer.AfterServerStart = func(svc *service.Service) {
		log.Println("... After Server Start")
	}

	// code to execute before server shuts down
	grpcServer.BeforeServerShutdown = func(svc *service.Service) {
		log.Println("Before Server Shutdown...")
	}

	// code to execute after server shuts down
	grpcServer.AfterServerShutdown = func(svc *service.Service) {
		log.Println("... After Server Shutdown")
	}

	// code to execute before each unary server rpc invocation
	grpcServer.UnaryServerInterceptors = []grpc.UnaryServerInterceptor{
		func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
			log.Println("Unary Server Interceptor Invoked: " + info.FullMethod)
			return handler(ctx, req)
		},
	}

	// code to execute before each stream server rpc invocation
	grpcServer.StreamServerInterceptors = []grpc.StreamServerInterceptor{
		func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			log.Println("Stream Server Interceptor Invoked: " + info.FullMethod)
			return handler(srv, ss)
		},
	}

	// code to execute for monitoring related actions
	grpcServer.StatsHandler = nil

	// code to execute when stream rpc is unknown
	grpcServer.UnknownStreamHandler = func(srv interface{}, stream grpc.ServerStream) error {
		log.Println("Unknown Stream Encountered")
		return fmt.Errorf("Unknown Stream Encountered")
	}

	// default grpc health check handler
	grpcServer.DefaultHealthCheckHandler = func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus {
		log.Println("Default gRPC Health Check Invoked")

		// just for test, set health probe to not serving if not even second
		if time.Now().Second() % 2 == 0 {
			return grpc_health_v1.HealthCheckResponse_SERVING
		} else {
			return grpc_health_v1.HealthCheckResponse_NOT_SERVING
		}
	}

	// per service grpc health check handler
	grpcServer.ServiceHealthCheckHandlers = map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus{
		"ExampleService": func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus {
			log.Println("Service gRPC Health Check Invoked: ExampleService")
			return grpc_health_v1.HealthCheckResponse_SERVING
		},
	}

	//
	// start grpc service server
	//
	if err := grpcServer.Serve(); err != nil {
		log.Println("Start gRPC Server Failed: " + err.Error())
	}
}

// stopServiceHandler will be invoked when service program stops, for clean up actions
func stopServiceHandler() {
	if grpcServer != nil {
		log.Println("Service Program Clean Up During Shutdown...")
		grpcServer.ImmediateStop()
	}
}
