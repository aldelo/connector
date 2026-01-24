package health

/*
 * Copyright 2020-2026 Aldelo, LP
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
	"errors"
	"fmt"
	"time"

	util "github.com/aldelo/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"strings"
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
	if h == nil { // guard nil receiver to avoid panic
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, fmt.Errorf("Health Check Failed: %s", "HealthClient receiver is nil")
	}

	if h.hcClient == nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, fmt.Errorf("Health Check Failed: %s", "Health Check Client Nil")
	}

	const defaultTimeout = 5 * time.Second // avoid indefinite hang

	// track effective timeout for clearer error messages
	effectiveTimeout := defaultTimeout
	if len(timeoutDuration) > 0 && timeoutDuration[0] > 0 {
		effectiveTimeout = timeoutDuration[0]
	}

	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	ctx, cancel = context.WithTimeout(context.Background(), effectiveTimeout)
	defer cancel()

	// trim service name before use
	trimmedSvc := strings.TrimSpace(svcName)

	in := &grpc_health_v1.HealthCheckRequest{}
	if util.LenTrim(trimmedSvc) > 0 {
		in.Service = trimmedSvc
	}

	resp, err := h.hcClient.Check(ctx, in)
	if err != nil {
		// classify gRPC status codes for accurate reporting
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.DeadlineExceeded:
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Timeout exceeded after %s)", effectiveTimeout)
			case codes.Canceled:
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Context canceled) %s", st.Message())
			case codes.Unavailable:
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Service unavailable) %s", st.Message())
			default:
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Call Health Server Error) [%s] %s", st.Code(), st.Message())
			}
		}

		// Fallback for non-status errors (kept from original logic)
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return grpc_health_v1.HealthCheckResponse_UNKNOWN,
				fmt.Errorf("Health Check Failed: (Timeout exceeded after %s)", effectiveTimeout)
		}

		return grpc_health_v1.HealthCheckResponse_UNKNOWN,
			fmt.Errorf("Health Check Failed: (Call Health Server Error) %s", err.Error())
	}

	if resp == nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, fmt.Errorf("Health Check Failed: (Health Server Response Nil)")
	}

	return resp.Status, nil
}
