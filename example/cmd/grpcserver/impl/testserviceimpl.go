package impl

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
	"time"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/xray"
	testpb "github.com/aldelo/connector/example/proto/test"
	"github.com/aldelo/connector/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type TestServiceImpl struct {
	testpb.UnimplementedAnswerServiceServer
}

// implemented service greeting
//
// Unary handlers that finish quickly do NOT need the rule #13 dual-ctx
// pattern — the RPC ctx already cancels on client disconnect, and a
// fast-returning handler naturally drains before shutdown completes.
// Only interruptible long-running handlers need to observe
// Service.ShutdownCtx() — see StreamGreeting below for that pattern.
func (*TestServiceImpl) Greeting(ctx context.Context, q *testpb.Question) (*testpb.Answer, error) {
	seg := xray.NewSegment("TestApp-Greeting-RPC")
	defer seg.Close()

	md := metadata.MD{
		"x-amzn-seg-id": []string{
			seg.Seg.ID,
		},
		"x-amzn-tr-id": []string{
			seg.Seg.TraceID,
		},
	}
	_ = grpc.SendHeader(ctx, md)

	s := q.Question + " = " + "Reply OK"
	return &testpb.Answer{
		Answer: s,
	}, nil
}

// TestStreamServiceImpl demonstrates rule #13 (gRPC handler ctx is
// client-lifetime, not server-drain-signal). A streaming handler that
// loops and does interruptible work MUST observe Service.ShutdownCtx()
// in addition to the per-RPC stream.Context() — otherwise long-lived
// streams will continue past SIGTERM and break rolling deploys.
//
// The svc reference is wired in from main.go so the handler can call
// svc.ShutdownCtx(). It may be nil in tests; the orNil helper below
// makes the select path safe in that case.
type TestStreamServiceImpl struct {
	testpb.UnimplementedAnswerServerStreamServiceServer

	svc *service.Service
}

// NewTestStreamServiceImpl constructs the streaming handler with a
// reference to the owning *service.Service so StreamGreeting can
// observe shutdown via svc.ShutdownCtx() per rule #13.
func NewTestStreamServiceImpl(svc *service.Service) *TestStreamServiceImpl {
	return &TestStreamServiceImpl{svc: svc}
}

// StreamGreeting demonstrates the rule #13 dual-ctx pattern.
//
// Rule #13: gRPC unary/stream handler ctx is client-lifetime, not
// server-drain-signal. The stream.Context() that gRPC hands us cancels
// when the CLIENT goes away, NOT when the server is draining on
// SIGTERM. A long-running streaming handler that only observes the RPC
// ctx will keep pumping data through graceful shutdown and prevent the
// drain window from completing cleanly. To react to server shutdown we
// must observe a SECOND ctx — Service.ShutdownCtx() — in the same
// select alongside the RPC ctx.
//
// NOTE: until C2-001 is fixed, the dual-ctx pattern only activates on
// the signal-driven shutdown path. Programmatic svc.GracefulStop() does
// not currently fire ShutdownCtx fire sites. See connector
// findings/2026-04-14-contrarian/C2-001.
func (s *TestStreamServiceImpl) StreamGreeting(in *testpb.Question, stream testpb.AnswerServerStreamService_StreamGreetingServer) error {
	rpcCtx := stream.Context()

	// ShutdownCtx() returns nil when the consumer has NOT opted in
	// (ShutdownCancel=false). orNil converts that to a never-firing
	// channel so the select case below blocks forever instead of
	// firing immediately — letting the same handler code work whether
	// or not the consumer enabled the capability.
	var shutdownDone <-chan struct{}
	if s.svc != nil {
		shutdownDone = orNil(s.svc.ShutdownCtx())
	}

	for i := 0; i < 50; i++ {
		select {
		case <-rpcCtx.Done():
			// Client disconnected — propagate their cancellation.
			return rpcCtx.Err()
		case <-shutdownDone:
			// Server is draining — tell the client to retry elsewhere.
			return status.Error(codes.Unavailable, "server draining")
		default:
			// Proceed with the next send.
		}

		if err := stream.Send(&testpb.Answer{
			Answer: util.CurrentDateTime(),
		}); err != nil {
			return err
		}

		// Pace the loop so shutdown has something to interrupt. A
		// tight synchronous loop finishes in microseconds and would
		// never observe a drain signal even if the dual-ctx select
		// were present — defeating the point of the example.
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

// orNil returns the Done() channel of ctx, or a nil channel when ctx
// itself is nil. A nil channel in a select case blocks forever, which
// is exactly what we want when Service.ShutdownCancel is disabled —
// the shutdown arm of the select becomes a no-op and the handler
// falls through to the default / rpcCtx cases as if the dual-ctx
// pattern were not there. This lets the example handler work
// unchanged regardless of the consumer's ShutdownCancel setting.
func orNil(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}
