package health

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
	"context"
	util "github.com/aldelo/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"fmt"
	"time"
)

type HealthClient struct {
	hcClient grpc_health_v1.HealthClient
}

func NewHealthClient(conn *grpc.ClientConn) (*HealthClient, error) {
	if conn == nil {
		return nil, fmt.Errorf("New Health Client Failed: %s", "gRPC Client Connection Nil")
	}

	return &HealthClient{
		hcClient: grpc_health_v1.NewHealthClient(conn),
	}, nil
}

func (h *HealthClient) Check(svcName string, timeoutDuration ...time.Duration) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
	if h.hcClient == nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, fmt.Errorf("Health Check Failed: %s", "Health Check Client Nil")
	}

	var ctx context.Context
	var cancel context.CancelFunc

	if len(timeoutDuration) > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeoutDuration[0])
	} else {
		ctx = context.Background()
	}

	in := &grpc_health_v1.HealthCheckRequest{}

	if util.LenTrim(svcName) > 0 {
		in.Service = svcName
	}

	if cancel != nil {
		defer cancel()
	}

	if resp, err := h.hcClient.Check(ctx, in); err != nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, fmt.Errorf("Health Check Failed: (Call Health Server Error) %s", err.Error())
	} else {
		return resp.Status, nil
	}
}