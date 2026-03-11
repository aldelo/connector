package tracer

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
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/xray"
	awsxray "github.com/aws/aws-xray-sdk-go/xray"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// keep stack for tracing while avoiding leaking it to callers.
func panicTraceError(r interface{}) error {
	return fmt.Errorf("panic: %v\n%s", r, debug.Stack())
}

// sanitized error returned to clients (no stack trace).
func panicClientError(r interface{}) error {
	return fmt.Errorf("panic: %v", r)
}

// sanitize segment names to be AWS X-Ray–compatible (DNS-safe and length-bounded).
func sanitizeSegmentName(prefix, method string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, method)

	safe = strings.Trim(safe, "-")
	if safe == "" {
		safe = "unknown"
	}

	name := prefix + safe
	if len(name) > 200 {
		name = name[:200]
	}

	return name
}

func TracerUnaryClientInterceptor(serviceName string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) (err error) {
		var seg *xray.XSegment
		createdSeg := false

		parentSegID, parentTraceID, sampled, _ := "", "", (*bool)(nil), ""

		defer func() {
			if r := recover(); r != nil {
				traceErr := panicTraceError(r)
				clientErr := panicClientError(r)
				if createdSeg && seg != nil && seg.Seg != nil {
					_ = seg.Seg.AddError(traceErr)
					seg.Close()
				} else if segCtx := awsxray.GetSegment(ctx); segCtx != nil {
					_ = segCtx.AddError(traceErr)
				}
				err = status.Error(codes.Internal, clientErr.Error())
				return
			}
			if createdSeg && seg != nil && seg.Seg != nil {
				if err != nil {
					_ = seg.Seg.AddError(err)
				}
				seg.Close()
			}
		}()

		if md, ok := metadata.FromOutgoingContext(ctx); ok {
			parentSegID, parentTraceID, sampled, _ = extractParentIDs(md)
			if sampled != nil && !*sampled {
				return invoker(ctx, method, req, reply, cc, opts...)
			}
		}

		if !xray.XRayServiceOn() {
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		if strings.HasPrefix(method, "/grpc.health") {
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		if awsxray.GetSegment(ctx) == nil {
			segName := sanitizeSegmentName("GrpcClient-UnaryRPC-", method)

			if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
				seg = xray.NewSegment(segName, &xray.XRayParentSegment{
					SegmentID: parentSegID,
					TraceID:   parentTraceID,
				})
			} else {
				seg = xray.NewSegment(segName)
			}

			if seg != nil && seg.Seg != nil {
				createdSeg = true
				ctx = context.WithValue(ctx, awsxray.ContextKey, seg.Seg)
			} else {
				seg = nil
				return invoker(ctx, method, req, reply, cc, opts...)
			}
		}

		return awsxray.UnaryClientInterceptor(
			awsxray.WithSegmentNamer(awsxray.NewFixedSegmentNamer(serviceName)),
		)(ctx, method, req, reply, cc, invoker, opts...)
	}
}

func TracerUnaryServerInterceptor(serviceName string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		var seg *xray.XSegment

		defer func() {
			if r := recover(); r != nil {
				traceErr := panicTraceError(r)
				clientErr := panicClientError(r)
				if seg != nil && seg.Seg != nil {
					_ = seg.Seg.AddError(traceErr)
					seg.Close()
				}
				err = status.Error(codes.Internal, clientErr.Error())
				return
			}
			if seg != nil && seg.Seg != nil {
				if err != nil {
					_ = seg.Seg.AddError(err)
				}
				seg.Close()
			}
		}()

		if !xray.XRayServiceOn() {
			return handler(ctx, req)
		}

		fullMethod := ""
		if info != nil {
			fullMethod = info.FullMethod
		}

		if strings.HasPrefix(fullMethod, "/grpc.health") {
			return handler(ctx, req)
		}

		parentSegID, parentTraceID, sampled, rawHeader := "", "", (*bool)(nil), ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			parentSegID, parentTraceID, sampled, rawHeader = extractParentIDs(md)
		}

		if sampled != nil && !*sampled {
			outgoingMD := metadata.New(nil)
			if rawHeader != "" {
				outgoingMD.Set("x-amzn-trace-id", rawHeader)
			} else if parentTraceID != "" {
				outgoingMD.Set("x-amzn-trace-id", formatTraceHeader(parentTraceID, parentSegID, false))
			}
			if parentSegID != "" {
				outgoingMD.Set("x-amzn-seg-id", parentSegID)
			}
			if parentTraceID != "" {
				outgoingMD.Set("x-amzn-tr-id", parentTraceID)
			}
			if hdrErr := grpc.SetHeader(ctx, outgoingMD); hdrErr != nil {
				err = status.Error(codes.Internal, hdrErr.Error())
				return nil, err
			}
			return handler(ctx, req)
		}

		segPrefix := "GrpcService-UnaryRPC-"
		if strings.TrimSpace(serviceName) != "" {
			segPrefix = sanitizeSegmentName("", serviceName+"-GrpcService-UnaryRPC-")
		}
		segName := sanitizeSegmentName(segPrefix, fullMethod)

		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
			seg = xray.NewSegment(segName, &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID:   parentTraceID,
			})
		} else {
			seg = xray.NewSegment(segName)
		}

		if seg == nil || seg.Seg == nil {
			seg = nil
			return handler(ctx, req)
		}

		segCtx := context.WithValue(ctx, awsxray.ContextKey, seg.Seg)

		outgoingMD := metadata.New(nil)
		outgoingMD.Set("x-amzn-seg-id", seg.Seg.ID)
		traceHeader := formatTraceHeader(seg.Seg.TraceID, seg.Seg.ID, seg.Seg.Sampled)
		if traceHeader != "" {
			outgoingMD.Set("x-amzn-trace-id", traceHeader)
		}
		outgoingMD.Set("x-amzn-tr-id", seg.Seg.TraceID)

		if hdrErr := grpc.SetHeader(segCtx, outgoingMD); hdrErr != nil {
			_ = seg.Seg.AddError(hdrErr)
			err = status.Error(codes.Internal, hdrErr.Error())
			return nil, err
		}

		return handler(segCtx, req)
	}
}

// contextServerStream wraps grpc.ServerStream to bind the X-Ray segment into the
// stream context and manage header lifecycle (stage → merge → send-once).
type contextServerStream struct {
	grpc.ServerStream
	ctx        context.Context
	headerSent *uint32
	stagedMD   metadata.MD

	mu sync.Mutex
}

func (w *contextServerStream) Context() context.Context {
	return w.ctx
}

// SetHeader stages headers (merged with existing) without sending.
// Preserves handler-set headers for later merge in SendHeader.
func (w *contextServerStream) SetHeader(md metadata.MD) error {
	if md == nil {
		return nil
	}
	incoming := metadata.Join(nil, md)

	w.mu.Lock()
	w.stagedMD = metadata.Join(w.stagedMD, incoming)
	if w.stagedMD == nil {
		w.stagedMD = metadata.MD{}
	}
	w.mu.Unlock()
	return nil
}

// SendHeader sends headers exactly once, merging staged + provided metadata.
//
// FIX: The original code had a race condition:
//   - The headerSent check was outside the lock (atomic read)
//   - The headerSent set was inside the lock
//   - The actual ServerStream.SendHeader was OUTSIDE the lock
//
// Two goroutines could both pass the outer check (both see 0), then serialize
// through the lock (both set headerSent=1), then BOTH call ServerStream.SendHeader,
// causing "SendHeader called multiple times" at the transport layer.
//
// Fix: Use atomic.CompareAndSwapUint32 as the single gate. Only the goroutine that
// wins the CAS (0→1) gets to call ServerStream.SendHeader. All others return nil.
// This eliminates the race without holding the mu lock during the transport write.
func (w *contextServerStream) SendHeader(md metadata.MD) error {
	if w.headerSent == nil {
		// No tracking — just forward directly
		return w.ServerStream.SendHeader(md)
	}

	// Fast path: already sent
	if atomic.LoadUint32(w.headerSent) == 1 {
		return nil
	}

	// Merge staged metadata with the provided md under lock
	w.mu.Lock()
	merged := metadata.Join(w.stagedMD, md)
	if merged == nil {
		merged = metadata.MD{}
	}
	w.mu.Unlock()

	// Gate: exactly one goroutine wins the CAS and performs the send.
	// Losers return nil — headers were (or will be) sent by the winner.
	if !atomic.CompareAndSwapUint32(w.headerSent, 0, 1) {
		return nil
	}

	return w.ServerStream.SendHeader(merged)
}

// SendMsg ensures headers are sent before the first message.
// gRPC auto-sends headers on first SendMsg, but we need to flush our staged
// metadata first, so we explicitly call SendHeader if not yet sent.
func (w *contextServerStream) SendMsg(m interface{}) error {
	if w.headerSent != nil && atomic.LoadUint32(w.headerSent) == 0 {
		if err := w.SendHeader(nil); err != nil {
			return err
		}
	}
	return w.ServerStream.SendMsg(m)
}

// helper to extract parent IDs from metadata with both canonical and legacy keys
func extractParentIDs(md metadata.MD) (segID, traceID string, sampled *bool, rawHeader string) {
	if v, ok := md["x-amzn-trace-id"]; ok && len(v) > 0 {
		rawHeader = v[0]
		for _, part := range strings.Split(rawHeader, ";") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) != 2 {
				continue
			}
			switch strings.ToLower(kv[0]) {
			case "root":
				traceID = kv[1]
			case "parent":
				segID = kv[1]
			case "sampled":
				val := strings.ToLower(strings.TrimSpace(kv[1]))
				if val == "1" || val == "true" {
					b := true
					sampled = &b
				} else if val == "0" || val == "false" {
					b := false
					sampled = &b
				}
			}
		}
	}

	if segID == "" {
		if v, ok := md["x-amzn-seg-id"]; ok && len(v) > 0 {
			segID = v[0]
		}
	}
	if traceID == "" {
		if v, ok := md["x-amzn-tr-id"]; ok && len(v) > 0 {
			traceID = v[0]
		}
	}

	return
}

// formatTraceHeader builds the canonical AWS X-Ray header value.
func formatTraceHeader(traceID, parentID string, sampled bool) string {
	if strings.TrimSpace(traceID) == "" {
		return ""
	}

	sampledVal := "0"
	if sampled {
		sampledVal = "1"
	}

	parts := []string{fmt.Sprintf("Root=%s", traceID)}
	if strings.TrimSpace(parentID) != "" {
		parts = append(parts, fmt.Sprintf("Parent=%s", parentID))
	}
	parts = append(parts, fmt.Sprintf("Sampled=%s", sampledVal))

	return strings.Join(parts, ";")
}

// TracerStreamServerInterceptor will perform xray tracing for each stream RPC call
func TracerStreamServerInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	var seg *xray.XSegment
	headerSent := uint32(0)

	defer func() {
		if r := recover(); r != nil {
			traceErr := panicTraceError(r)
			clientErr := panicClientError(r)
			if seg != nil && seg.Seg != nil {
				_ = seg.Seg.AddError(traceErr)
				seg.Close()
			}
			err = status.Error(codes.Internal, clientErr.Error())
			return
		}
		if seg != nil && seg.Seg != nil {
			if err != nil {
				_ = seg.Seg.AddError(err)
			}
			seg.Close()
		}
	}()

	if xray.XRayServiceOn() {
		fullMethod := "unknown"
		if info != nil {
			fullMethod = info.FullMethod
		}
		if strings.HasPrefix(fullMethod, "/grpc.health") {
			return handler(srv, ss)
		}

		parentSegID, parentTraceID, sampled, rawHeader := "", "", (*bool)(nil), ""
		streamType := "StreamRPC"
		if info != nil {
			switch {
			case info.IsClientStream && info.IsServerStream:
				streamType = "BidiStreamRPC"
			case info.IsClientStream:
				streamType = "ClientStreamRPC"
			case info.IsServerStream:
				streamType = "ServerStreamRPC"
			}
		}

		var incomingMD metadata.MD
		var ok bool
		ctx := ss.Context()

		if incomingMD, ok = metadata.FromIncomingContext(ctx); ok {
			parentSegID, parentTraceID, sampled, rawHeader = extractParentIDs(incomingMD)
		}

		// Respect Sampled=0 by propagating headers only.
		if sampled != nil && !*sampled {
			outgoingMD := metadata.New(nil)
			if rawHeader != "" {
				outgoingMD.Set("x-amzn-trace-id", rawHeader)
			} else if parentTraceID != "" {
				outgoingMD.Set("x-amzn-trace-id", formatTraceHeader(parentTraceID, parentSegID, false))
			}
			if parentSegID != "" {
				outgoingMD.Set("x-amzn-seg-id", parentSegID)
			}
			if parentTraceID != "" {
				outgoingMD.Set("x-amzn-tr-id", parentTraceID)
			}

			wrappedStream := &contextServerStream{
				ServerStream: ss,
				ctx:          ctx,
				headerSent:   &headerSent,
				stagedMD:     nil,
			}

			if hdrErr := wrappedStream.SetHeader(outgoingMD); hdrErr != nil {
				err = status.Error(codes.Internal, hdrErr.Error())
				return err
			}

			err = handler(srv, wrappedStream)

			// Ensure headers flush once if handler sent nothing.
			if atomic.LoadUint32(&headerSent) == 0 {
				if hdrErr := wrappedStream.SendHeader(nil); hdrErr != nil {
					if err == nil {
						err = status.Error(codes.Internal, hdrErr.Error())
					}
				}
			}

			return err
		}

		segmentName := sanitizeSegmentName("GrpcService-"+streamType+"-", fullMethod)
		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
			seg = xray.NewSegment(segmentName, &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID:   parentTraceID,
			})
		} else {
			seg = xray.NewSegment(segmentName)
		}

		if seg == nil || seg.Seg == nil {
			seg = nil
			return handler(srv, ss)
		}

		segCtx := context.WithValue(ctx, awsxray.ContextKey, seg.Seg)
		outgoingMD := metadata.New(nil)
		outgoingMD.Set("x-amzn-seg-id", seg.Seg.ID)
		traceHeader := formatTraceHeader(seg.Seg.TraceID, seg.Seg.ID, seg.Seg.Sampled)
		if traceHeader != "" {
			outgoingMD.Set("x-amzn-trace-id", traceHeader)
		}
		outgoingMD.Set("x-amzn-tr-id", seg.Seg.TraceID)

		wrappedStream := &contextServerStream{
			ServerStream: ss,
			ctx:          segCtx,
			headerSent:   &headerSent,
			stagedMD:     nil,
		}

		if hdrErr := wrappedStream.SetHeader(outgoingMD); hdrErr != nil {
			_ = seg.Seg.AddError(hdrErr)
			err = status.Error(codes.Internal, hdrErr.Error())
			return err
		}

		err = handler(srv, wrappedStream)

		// ensure headers are flushed once even if handler sent nothing
		if atomic.LoadUint32(&headerSent) == 0 {
			if hdrErr := wrappedStream.SendHeader(nil); hdrErr != nil {
				_ = seg.Seg.AddError(hdrErr)
				if err == nil {
					err = status.Error(codes.Internal, hdrErr.Error())
				}
			}
		}

		return err
	}

	return handler(srv, ss)
}
