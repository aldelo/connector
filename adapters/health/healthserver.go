package health

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
	"google.golang.org/grpc/health/grpc_health_v1"
	"log"
)

type HealthServer struct {
	DefaultHealthCheck  func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus
	HealthCheckHandlers map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus
}

func NewHealthServer(defaultCheck func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus,
	serviceChecks map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus) *HealthServer {
	return &HealthServer{
		DefaultHealthCheck:  defaultCheck,
		HealthCheckHandlers: serviceChecks,
	}
}

func (h *HealthServer) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	svcName := req.Service

	if util.LenTrim(svcName) == 0 {
		svcName = "*"
	}

	log.Println("Health Check Invoked for " + svcName + "...")

	// invoke health check handler
	var fn func(context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus

	if h.HealthCheckHandlers != nil {
		fn = h.HealthCheckHandlers[svcName]
	}

	if fn == nil && h.DefaultHealthCheck != nil {
		fn = h.DefaultHealthCheck
	}

	var status grpc_health_v1.HealthCheckResponse_ServingStatus
	noHandler := ""

	if fn != nil {
		status = fn(ctx)
	} else {
		// if no handlers, default to serving
		status = grpc_health_v1.HealthCheckResponse_SERVING
		noHandler = " [No Handler]"
	}

	log.Println("... Health Check Result for " + svcName + " = " + status.String() + noHandler)

	// return status result
	return &grpc_health_v1.HealthCheckResponse{
		Status: status,
	}, nil
}

func (h *HealthServer) Watch(req *grpc_health_v1.HealthCheckRequest, server grpc_health_v1.Health_WatchServer) error {
	return fmt.Errorf("Health Server Watch Not Supported, Use Check Instead")
}
