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

// Check preserves the original API but now delegates to the context-aware version
func (h *HealthClient) Check(svcName string, timeoutDuration ...time.Duration) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) { // CHANGED
	return h.CheckContext(context.Background(), svcName, timeoutDuration...)
}

// CheckContext allows callers to propagate cancellation, deadlines, and metadata
func (h *HealthClient) CheckContext(ctx context.Context, svcName string, timeoutDuration ...time.Duration) (grpc_health_v1.HealthCheckResponse_ServingStatus, error) {
	if h == nil { // guard nil receiver to avoid panic
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, fmt.Errorf("Health Check Failed: %s", "HealthClient receiver is nil")
	}

	if h.hcClient == nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, fmt.Errorf("Health Check Failed: %s", "Health Check Client Nil")
	}

	// reject multiple timeout arguments to avoid silent misuse.
	if len(timeoutDuration) > 1 {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN,
			fmt.Errorf("Health Check Failed: only one timeoutDuration argument is allowed (got %d)", len(timeoutDuration))
	}

	const defaultTimeout = 5 * time.Second // avoid indefinite hang

	// track effective timeout for clearer error messages
	effectiveTimeout := defaultTimeout
	useTimeout := true
	if len(timeoutDuration) > 0 {
		switch t := timeoutDuration[0]; {
		case t < 0:
			return grpc_health_v1.HealthCheckResponse_UNKNOWN,
				fmt.Errorf("Health Check Failed: invalid timeout %s (must be >= 0)", t)
		case t == 0:
			useTimeout = false // caller explicitly requested no timeout
		default:
			effectiveTimeout = t
		}
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var (
		childCtx context.Context
		cancel   context.CancelFunc
	)
	if useTimeout {
		childCtx, cancel = context.WithTimeout(ctx, effectiveTimeout)
		defer cancel()
	} else {
		childCtx = ctx
	}

	// trim service name before use
	trimmedSvc := strings.TrimSpace(svcName)

	in := &grpc_health_v1.HealthCheckRequest{}
	if util.LenTrim(trimmedSvc) > 0 {
		in.Service = trimmedSvc
	}

	resp, err := h.hcClient.Check(childCtx, in)
	if err != nil {
		// classify gRPC status codes for accurate reporting
		if st, ok := status.FromError(err); ok {
			switch st.Code() {
			case codes.DeadlineExceeded:
				if useTimeout {
					return grpc_health_v1.HealthCheckResponse_UNKNOWN,
						fmt.Errorf("Health Check Failed: (Timeout exceeded after %s): %w", effectiveTimeout, err)
				}
				// no-timeout call should not hit this unless server imposed its own deadline
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Server/transport deadline exceeded): %w", err)
			case codes.Canceled:
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Context canceled) %w", err)
			case codes.Unavailable:
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Service unavailable) %w", err)
			case codes.NotFound:
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Service not found) %w", err)
			case codes.Unimplemented:
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Health service unimplemented on server) %w", err)
			default:
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Call Health Server Error) [%s]: %w", st.Code(), err)
			}
		}

		// distinguish cancellation from timeout in non-status errors
		if errors.Is(err, context.DeadlineExceeded) {
			if useTimeout {
				return grpc_health_v1.HealthCheckResponse_UNKNOWN,
					fmt.Errorf("Health Check Failed: (Timeout exceeded after %s): %w", effectiveTimeout, err)
			}
			return grpc_health_v1.HealthCheckResponse_UNKNOWN,
				fmt.Errorf("Health Check Failed: (Server/transport deadline exceeded): %w", err)
		}

		// report caller/transport cancellation accurately
		if errors.Is(err, context.Canceled) {
			return grpc_health_v1.HealthCheckResponse_UNKNOWN,
				fmt.Errorf("Health Check Failed: (Context canceled) %w", err)
		}

		return grpc_health_v1.HealthCheckResponse_UNKNOWN,
			fmt.Errorf("Health Check Failed: (Call Health Server Error) %w", err)
	}

	if resp == nil {
		return grpc_health_v1.HealthCheckResponse_UNKNOWN, fmt.Errorf("Health Check Failed: (Health Server Response Nil)")
	}

	return resp.Status, nil
}
