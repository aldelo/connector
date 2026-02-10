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
	"log"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	util "github.com/aldelo/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type HealthServer struct {
	grpc_health_v1.UnimplementedHealthServer // embed for forward compatibility
	DefaultHealthCheck                       func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus

	mu       sync.RWMutex
	handlers map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus
}

func NewHealthServer(
	defaultCheck func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus,
	serviceChecks map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus,
) *HealthServer {

	h := &HealthServer{
		DefaultHealthCheck: defaultCheck,
		handlers:           make(map[string]func(context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus),
	}

	for name, fn := range serviceChecks {
		if fn == nil {
			continue
		}

		trimmed := strings.TrimSpace(name)
		h.handlers[trimmed] = fn
	}

	return h

}

// public helper for safe, concurrent registration.
func (h *HealthServer) RegisterHandler(service string, fn func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus) {
	if h == nil || fn == nil {
		return
	}
	service = strings.TrimSpace(service)

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.handlers == nil { // should not happen, but defensive
		h.handlers = make(map[string]func(context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus)
	}
	h.handlers[service] = fn
}

// centralized, concurrency-safe handler lookup with wildcard support
func (h *HealthServer) handlerFor(service string) func(context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus {
	if h == nil {
		return nil
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.handlers == nil { // use internal map only
		return nil
	}

	service = strings.TrimSpace(service)

	if fn := h.handlers[service]; fn != nil {
		return fn
	}

	if service == "" {
		if fn := h.handlers[""]; fn != nil {
			return fn
		}
	}

	if fn := h.handlers["*"]; fn != nil {
		return fn
	}

	return nil
}

func (h *HealthServer) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (resp *grpc_health_v1.HealthCheckResponse, err error) {
	if h == nil {
		return nil, status.Errorf(codes.Internal, "Health Server Not Initialized")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if req == nil {
		return nil, status.Errorf(codes.InvalidArgument, "Health Check Request Nil")
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, status.FromContextError(ctxErr).Err()
	}

	svcName := strings.TrimSpace(req.GetService())
	if util.LenTrim(svcName) == 0 {
		svcName = ""
	}
	log.Println("Health Check Invoked for " + svcName + "...")

	// invoke health check handler
	fn := h.handlerFor(svcName)
	if fn == nil && h.DefaultHealthCheck != nil {
		fn = h.DefaultHealthCheck
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, status.FromContextError(ctxErr).Err() // surface context error if cancelled mid-call
	}

	// if no handler exists, return NOT_FOUND per gRPC health spec guidance
	if fn == nil { // distinguish “no handler” from “handler returned UNKNOWN”
		log.Printf("... Health Check Result for %s = %s [No Handler]", svcName, grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN.String())
		return nil, status.Errorf(codes.NotFound, "health check handler not found for %q", svcName)
	}

	var statusVal grpc_health_v1.HealthCheckResponse_ServingStatus

	// protect against handler panics
	defer func() {
		if r := recover(); r != nil {
			log.Printf("... Health Check panic for %s: %v\n%s", svcName, r, debug.Stack())
			statusVal = grpc_health_v1.HealthCheckResponse_SERVICE_UNKNOWN
			resp = nil
			err = status.Errorf(codes.Internal, "Health Check Handler Panic: %q", svcName)
		}
	}()

	statusVal = fn(ctx)

	// honor context cancellation after handler execution
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, status.FromContextError(ctxErr).Err() // surface context error if cancelled mid-call
	}

	log.Printf("... Health Check Result for %s = %s", svcName, statusVal.String())
	return &grpc_health_v1.HealthCheckResponse{Status: statusVal}, nil
}

func (h *HealthServer) Watch(req *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
	if h == nil {
		return status.Errorf(codes.Internal, "Health Server Not Initialized")
	}

	ctx := stream.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	ticker := time.NewTicker(30 * time.Second) // Heartbeat interval
	defer ticker.Stop()

	// Send initial status
	statusResp, err := h.Check(ctx, req)
	if err != nil {
		return err
	}
	if err := stream.Send(statusResp); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Send periodic heartbeat
			statusResp, err := h.Check(ctx, req)
			if err != nil {
				return err
			}
			if err := stream.Send(statusResp); err != nil {
				return err
			}
		}
	}
}
