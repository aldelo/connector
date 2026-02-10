// Copyright 2017 David Ackroyd. All Rights Reserved.
// See LICENSE for licensing terms.

package grpc_recovery

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestUnaryServerInterceptor_NoPanic tests that the interceptor doesn't interfere when no panic occurs
func TestUnaryServerInterceptor_NoPanic(t *testing.T) {
	interceptor := UnaryServerInterceptor()

	expectedResp := "success response"
	expectedErr := errors.New("handler error")

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return expectedResp, expectedErr
	}

	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)

	if resp != expectedResp {
		t.Errorf("expected response %v, got %v", expectedResp, resp)
	}

	if err != expectedErr {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

// TestUnaryServerInterceptor_WithPanic tests that the interceptor catches panics
func TestUnaryServerInterceptor_WithPanic(t *testing.T) {
	interceptor := UnaryServerInterceptor()

	panicValue := "test panic"

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		panic(panicValue)
	}

	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)

	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}

	if err == nil {
		t.Fatal("expected error from panic recovery, got nil")
	}

	// Check that it's an Internal error
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}

	if st.Code() != codes.Internal {
		t.Errorf("expected Internal code, got %v", st.Code())
	}
}

// TestUnaryServerInterceptor_WithCustomRecoveryHandler tests custom recovery handler
func TestUnaryServerInterceptor_WithCustomRecoveryHandler(t *testing.T) {
	customErr := errors.New("custom recovery error")

	interceptor := UnaryServerInterceptor(
		WithRecoveryHandler(func(p interface{}) error {
			return customErr
		}),
	)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		panic("test panic")
	}

	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)

	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}

	if err != customErr {
		t.Errorf("expected custom error %v, got %v", customErr, err)
	}
}

// TestUnaryServerInterceptor_WithContextRecoveryHandler tests context-aware recovery handler
func TestUnaryServerInterceptor_WithContextRecoveryHandler(t *testing.T) {
	customErr := errors.New("context recovery error")

	interceptor := UnaryServerInterceptor(
		WithRecoveryHandlerContext(func(ctx context.Context, p interface{}) error {
			// Verify context is passed correctly
			if ctx == nil {
				t.Error("expected non-nil context")
			}
			return customErr
		}),
	)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		panic("test panic")
	}

	resp, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)

	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}

	if err != customErr {
		t.Errorf("expected custom error %v, got %v", customErr, err)
	}
}

// TestStreamServerInterceptor_NoPanic tests that the stream interceptor doesn't interfere when no panic occurs
func TestStreamServerInterceptor_NoPanic(t *testing.T) {
	interceptor := StreamServerInterceptor()

	expectedErr := errors.New("handler error")

	handler := func(srv interface{}, stream grpc.ServerStream) error {
		return expectedErr
	}

	err := interceptor(nil, &mockServerStream{}, &grpc.StreamServerInfo{}, handler)

	if err != expectedErr {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

// TestStreamServerInterceptor_WithPanic tests that the stream interceptor catches panics
func TestStreamServerInterceptor_WithPanic(t *testing.T) {
	interceptor := StreamServerInterceptor()

	panicValue := "test stream panic"

	handler := func(srv interface{}, stream grpc.ServerStream) error {
		panic(panicValue)
	}

	err := interceptor(nil, &mockServerStream{}, &grpc.StreamServerInfo{}, handler)

	if err == nil {
		t.Fatal("expected error from panic recovery, got nil")
	}

	// Check that it's an Internal error
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}

	if st.Code() != codes.Internal {
		t.Errorf("expected Internal code, got %v", st.Code())
	}
}

// TestStreamServerInterceptor_WithCustomRecoveryHandler tests custom recovery handler for streams
func TestStreamServerInterceptor_WithCustomRecoveryHandler(t *testing.T) {
	customErr := errors.New("custom stream recovery error")

	interceptor := StreamServerInterceptor(
		WithRecoveryHandler(func(p interface{}) error {
			return customErr
		}),
	)

	handler := func(srv interface{}, stream grpc.ServerStream) error {
		panic("test stream panic")
	}

	err := interceptor(nil, &mockServerStream{}, &grpc.StreamServerInfo{}, handler)

	if err != customErr {
		t.Errorf("expected custom error %v, got %v", customErr, err)
	}
}

// mockServerStream is a mock implementation of grpc.ServerStream for testing
type mockServerStream struct {
	grpc.ServerStream
}

func (m *mockServerStream) Context() context.Context {
	return context.Background()
}
