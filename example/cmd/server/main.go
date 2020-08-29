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

	testpb "github.com/aldelo/connector/example/proto/test"
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
		ServiceHandler: ServiceHandler,
	}

	// launch the service program,
	// which triggers the gRPC server setup and launch as well
	prog.Launch()
}

// ServiceHandler implements the gRPC service startup
// ServiceHandler is to be called by ServiceProgram
func ServiceHandler(runAsService bool, port int) {
	// setup grpc service server
	svc := service.NewService("ExampleServer", "service", "", func(grpcServer *grpc.Server) {
		testpb.RegisterAnswerServiceServer(grpcServer, &impl.TestServiceImpl{})
	})

	// code to execute before server starts
	svc.BeforeServerStart = func(svc *service.Service) {
		log.Println("Before Server Start...")
	}

	// code to execute after server started
	svc.AfterServerStart = func(svc *service.Service) {
		log.Println("... After Server Start")
	}

	// code to execute before server shuts down
	svc.BeforeServerShutdown = func(svc *service.Service) {
		log.Println("Before Server Shutdown...")
	}

	// code to execute after server shuts down
	svc.AfterServerShutdown = func(svc *service.Service) {
		log.Println("... After Server Shutdown")
	}

	// code to execute before each unary server rpc invocation
	svc.UnaryServerInterceptors = []grpc.UnaryServerInterceptor{
		func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
			log.Println("Unary Server Interceptor Invoked: " + info.FullMethod)
			return handler(ctx, req)
		},
	}

	// code to execute before each stream server rpc invocation
	svc.StreamServerInterceptors = []grpc.StreamServerInterceptor{
		func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			log.Println("Stream Server Interceptor Invoked: " + info.FullMethod)
			return handler(srv, ss)
		},
	}

	// code to execute for monitoring related actions
	svc.StatsHandler = nil

	// code to execute when stream rpc is unknown
	svc.UnknownStreamHandler = func(srv interface{}, stream grpc.ServerStream) error {
		log.Println("Unknown Stream Encountered")
		return fmt.Errorf("Unknown Stream Encountered")
	}

	// default grpc health check handler
	svc.DefaultHealthCheckHandler = func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus {
		log.Println("Default gRPC Health Check Invoked")
		return grpc_health_v1.HealthCheckResponse_SERVING
	}

	// per service grpc health check handler
	svc.ServiceHealthCheckHandlers = map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus{
		"ExampleService": func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus {
			log.Println("Service gRPC Health Check Invoked: ExampleService")
			return grpc_health_v1.HealthCheckResponse_SERVING
		},
	}

	//
	// start grpc service server
	//
	if err := svc.Serve(); err != nil {
		log.Println("Start gRPC Server Failed: " + err.Error())
	}
}
