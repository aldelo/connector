package logger

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

	"google.golang.org/grpc"
)

// LoggerUnaryInterceptor is a unary RPC server interceptor that handles cloud logging of unary RPC calls.
// TODO(logging): Implement structured logging with the following features:
//   - Log request method, duration, and status code
//   - Log request/response metadata (configurable)
//   - Support for correlation IDs from context
//   - Integration with cloud logging services (CloudWatch, Stackdriver, etc.)
func LoggerUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	// Not implemented - passes through to handler without logging
	return handler(ctx, req)
}

// LoggerStreamInterceptor is a stream RPC server interceptor that handles cloud logging of stream RPC calls.
// TODO(logging): Implement structured logging with the following features:
//   - Log stream method, duration, and status code
//   - Log stream lifecycle events (start, end, error)
//   - Support for correlation IDs from context
//   - Integration with cloud logging services (CloudWatch, Stackdriver, etc.)
func LoggerStreamInterceptor(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	// Not implemented - passes through to handler without logging
	return handler(srv, stream)
}
