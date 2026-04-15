package grpc_recovery

import (
	"context"
	"log"
	"runtime/debug"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RecoveryHandlerFunc is a function that recovers from the panic `p` by returning an `error`.
type RecoveryHandlerFunc func(p interface{}) (err error)

// RecoveryHandlerFuncContext is a function that recovers from the panic `p` by returning an `error`.
// The context can be used to extract request scoped metadata and context values.
type RecoveryHandlerFuncContext func(ctx context.Context, p interface{}) (err error)

// UnaryServerInterceptor returns a new unary server interceptor for panic recovery.
func UnaryServerInterceptor(opts ...Option) grpc.UnaryServerInterceptor {
	o := evaluateOptions(opts)
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("grpc_recovery: recovered from panic: %v\n%s", r, debug.Stack())
				err = recoverFrom(ctx, r, o.recoveryHandlerFunc)
			}
		}()

		resp, err = handler(ctx, req)
		return resp, err
	}
}

// StreamServerInterceptor returns a new streaming server interceptor for panic recovery.
func StreamServerInterceptor(opts ...Option) grpc.StreamServerInterceptor {
	o := evaluateOptions(opts)
	return func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		// SP-008 P3-CONN-3 (2026-04-15): snapshot ctx once at the top
		// of the interceptor so the deferred recover never calls
		// stream.Context() on a potentially-broken stream. If the
		// stream itself is the panic trigger (test-harness injection,
		// future gRPC regression), stream.Context() inside the defer
		// could re-panic with no further recovery. Holding the ctx
		// reference from the entry moment eliminates that second-
		// panic window.
		ctx := stream.Context()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("grpc_recovery: recovered from panic: %v\n%s", r, debug.Stack())
				err = recoverFrom(ctx, r, o.recoveryHandlerFunc)
			}
		}()

		err = handler(srv, stream)
		return err
	}
}

func recoverFrom(ctx context.Context, p interface{}, r RecoveryHandlerFuncContext) error {
	if r == nil {
		// Return generic message to client to prevent leaking sensitive panic details
		// (stack trace, internal state). The panic value is already logged by the caller.
		return status.Errorf(codes.Internal, "internal server error")
	}
	return r(ctx, p)
}
