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
	"strings"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var (
	tokenValidatorMu sync.RWMutex
	tokenValidator   func(token string) bool
)

// SetTokenValidator sets the token validation function.
// This should be called during initialization before the server starts processing requests.
func SetTokenValidator(validator func(token string) bool) {
	tokenValidatorMu.Lock()
	defer tokenValidatorMu.Unlock()
	tokenValidator = validator
}

// getTokenValidator safely retrieves the current token validator.
// FIX #1: If the new-style validator is nil, falls back to the deprecated
// TokenValidator variable — but reads it under the same lock to prevent a
// data race. If the deprecated variable has a value, it is promoted into
// the guarded field so subsequent calls take the fast path.
func getTokenValidator() func(token string) bool {
	tokenValidatorMu.RLock()
	v := tokenValidator
	tokenValidatorMu.RUnlock()

	if v != nil {
		return v
	}

	// Promote the deprecated variable under a write lock.
	tokenValidatorMu.Lock()
	defer tokenValidatorMu.Unlock()

	// Double-check: another goroutine may have set it between the RUnlock and Lock.
	if tokenValidator != nil {
		return tokenValidator
	}

	// Read the deprecated variable while holding the write lock.
	// This is safe because all concurrent readers are blocked by the write lock,
	// and the deprecated variable is only expected to be written once at init time.
	if TokenValidator != nil {
		tokenValidator = TokenValidator
	}

	return tokenValidator
}

// Deprecated: Use SetTokenValidator instead.
// TokenValidator is maintained for backward compatibility.
// It must be set during initialization before the server starts serving.
var TokenValidator func(token string) bool

// authenticate extracts the bearer token from metadata and validates it.
// Shared by both unary and stream interceptors to eliminate duplication.
func authenticate(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Errorf(codes.InvalidArgument, "Metadata Missing")
	}

	a := md["authorization"]
	if len(a) == 0 {
		return status.Errorf(codes.Unauthenticated, "Auth Token Not Valid")
	}

	// RFC 7235: auth scheme is case-insensitive
	val := a[0]
	token := val
	if len(val) >= 7 && strings.EqualFold(val[:7], "Bearer ") {
		token = val[7:]
	}

	validator := getTokenValidator()
	if validator == nil {
		return status.Errorf(codes.Internal, "Token validator not configured")
	}
	if !validator(token) {
		return status.Errorf(codes.Unauthenticated, "Auth Token Not Valid")
	}

	return nil
}

// ServerAuthUnaryInterceptor is a unary RPC server interceptor that validates
// auth via the request's metadata. It blocks the RPC call if auth fails.
func ServerAuthUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	if err := authenticate(ctx); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

// ServerAuthStreamInterceptor is a streaming RPC server interceptor that validates
// auth via the request's metadata. It blocks the stream if auth fails.
func ServerAuthStreamInterceptor(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := authenticate(stream.Context()); err != nil {
		return err
	}
	return handler(srv, stream)
}
