package auth

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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// ServerAuthUnaryInterceptor is an unary rpc server interceptor handler that handles auth via request's metadata
// this interceptor will block rpc call if auth fails
func ServerAuthUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "Metadata Missing")
	}

	a := md["authorization"]
	if len(a) == 0 {
		return nil, status.Errorf(codes.Unauthenticated, "Auth Token Not Valid")
	}

	// Auth token validation not implemented - reject all requests until properly configured
	return nil, status.Errorf(codes.Unimplemented, "Auth Token Validation Not Implemented - Configure ValidateToken handler")
}

func ServerAuthStreamInterceptor(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	// TODO(security): Stream authentication not implemented - all stream RPCs bypass auth validation
	// WARNING: This interceptor currently allows all stream requests without authentication
	// Implement proper stream auth before using in production
	return handler(srv, stream)
}
