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

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/xray"
	awsxray "github.com/aws/aws-xray-sdk-go/xray"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// enrich panic errors with stack traces for X-Ray and gRPC status propagation.
func panicError(r interface{}) error {
	return fmt.Errorf("panic: %v\n%s", r, debug.Stack())
}

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

		// global panic guard always installed to prevent client crashes on any path
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
				err = status.Error(codes.Internal, clientErr.Error()) // sanitized message
				return
			}
			if createdSeg && seg != nil && seg.Seg != nil { // only close synthetic segment
				if err != nil {
					_ = seg.Seg.AddError(err)
				}
				seg.Close()
			}
		}()

		// honor upstream Sampled=0 headers even when no segment is present
		if md, ok := metadata.FromOutgoingContext(ctx); ok {
			_, _, sampled, _ := extractParentIDs(md)
			if sampled != nil && !*sampled {
				// do not create or propagate tracing; respect upstream opt-out
				return invoker(ctx, method, req, reply, cc, opts...)
			}
		}

		if !xray.XRayServiceOn() {
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		// bypass health check
		if strings.HasPrefix(method, "/grpc.health") {
			return invoker(ctx, method, req, reply, cc, opts...)
		}

		// If there is no current segment, create a temporary one to ensure trace propagation.
		if awsxray.GetSegment(ctx) == nil {
			seg = xray.NewSegment(sanitizeSegmentName("GrpcClient-UnaryRPC-", method))
			if seg != nil && seg.Seg != nil {
				createdSeg = true
				ctx = context.WithValue(ctx, awsxray.ContextKey, seg.Seg)
			} else {
				// fallback: run without tracing if segment creation failed
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
		var seg *xray.XSegment // keep segment in outer scope for deferred recovery/close

		// global panic guard even when X-Ray is disabled, to avoid crashing the server
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

		// Skip noisy health checks.
		if strings.HasPrefix(fullMethod, "/grpc.health") {
			return handler(ctx, req)
		}

		// Extract parent IDs from incoming metadata.
		parentSegID, parentTraceID, sampled, rawHeader := "", "", (*bool)(nil), ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			parentSegID, parentTraceID, sampled, rawHeader = extractParentIDs(md)
		}

		// If parent said Sampled=0, propagate headers without creating a segment.
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

		segName := sanitizeSegmentName("GrpcService-UnaryRPC-", fullMethod)
		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
			seg = xray.NewSegment(segName, &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID:   parentTraceID,
			})
		} else {
			seg = xray.NewSegment(segName)
		}

		// guard segment creation to avoid panics
		if seg == nil || seg.Seg == nil {
			seg = nil
			return handler(ctx, req)
		}

		// Bind segment to context for downstream code.
		segCtx := context.WithValue(ctx, awsxray.ContextKey, seg.Seg)

		// Propagate tracing headers back to caller.
		outgoingMD := metadata.New(nil)
		outgoingMD.Set("x-amzn-seg-id", seg.Seg.ID)
		traceHeader := formatTraceHeader(seg.Seg.TraceID, seg.Seg.ID, seg.Seg.Sampled)
		outgoingMD.Set("x-amzn-trace-id", traceHeader)
		outgoingMD.Set("x-amzn-tr-id", seg.Seg.TraceID)

		// stage headers (do not flush yet) so the handler can merge/override its own metadata
		if hdrErr := grpc.SetHeader(segCtx, outgoingMD); hdrErr != nil {
			_ = seg.Seg.AddError(hdrErr)
			err = status.Error(codes.Internal, hdrErr.Error())
			return nil, err
		}

		return handler(segCtx, req)
	}
}

// bind the created segment into the stream context and use the wrapped stream for SendHeader and handler.
type contextServerStream struct {
	grpc.ServerStream
	ctx        context.Context
	headerSent *bool
}

func (w *contextServerStream) Context() context.Context {
	return w.ctx
}

// track header send when handler or interceptor calls SendHeader.
func (w *contextServerStream) SendHeader(md metadata.MD) error {
	if w.headerSent != nil {
		*w.headerSent = true
	}
	return w.ServerStream.SendHeader(md)
}

// mark headers as sent when the first message goes out (gRPC auto-sends headers on first SendMsg).
func (w *contextServerStream) SendMsg(m interface{}) error {
	if w.headerSent != nil {
		*w.headerSent = true
	}
	return w.ServerStream.SendMsg(m)
}

// helper to extract parent IDs from metadata with both canonical and legacy keys
func extractParentIDs(md metadata.MD) (segID, traceID string, sampled *bool, rawHeader string) {
	// 1) canonical AWS header: "Root=1-...;Parent=...;Sampled=1"
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

	// 2) legacy split headers
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

// formatTraceHeader builds the canonical AWS X-Ray header value. // NEW
func formatTraceHeader(traceID, parentID string, sampled bool) string {
	sampledVal := "0"
	if sampled {
		sampledVal = "1"
	}
	return fmt.Sprintf("Root=%s;Parent=%s;Sampled=%s", traceID, parentID, sampledVal)
}

// TracerUnaryServerInterceptor will perform xray tracing for each unary RPC call
//
// to pass in parent xray segment id and trace id, set the metadata keys with:
//
//	x-amzn-seg-id = parent xray segment id, assign value to this key via metadata.MD
//	x-amzn-tr-id = parent xray trace id, assign value to this key via metadata.MD
//
// how to set metadata at client side?
//
//	ctx := context.Background()
//	md := metadata.Pairs("x-amzn-seg-id", "abc", "x-amzn-tr-id", "def")
//	ctx = metadata.NewOutgoingContext(ctx, md)
//func TracerUnaryServerInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
//	if xray.XRayServiceOn() {
//		parentSegID := ""
//		parentTraceID := ""
//
//		var md metadata.MD
//		var ok bool
//
//		if md, ok = metadata.FromIncomingContext(ctx); ok {
//			if v, ok2 := md["x-amzn-seg-id"]; ok2 && len(v) > 0 {
//				parentSegID = v[0]
//			}
//
//			if v, ok2 := md["x-amzn-tr-id"]; ok2 && len(v) > 0 {
//				parentTraceID = v[0]
//			}
//		}
//
//		var seg *xray.XSegment
//
//		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
//			seg = xray.NewSegment("GrpcService-UnaryRPC-"+info.FullMethod, &xray.XRayParentSegment{
//				SegmentID: parentSegID,
//				TraceID:   parentTraceID,
//			})
//		} else {
//			seg = xray.NewSegment("GrpcService-UnaryRPC-" + info.FullMethod)
//		}
//		defer seg.Close()
//		defer func() {
//			if err != nil {
//				_ = seg.Seg.AddError(err)
//			}
//		}()
//
//		if md == nil {
//			md = make(metadata.MD)
//		}
//
//		md.Set("x-amzn-seg-id", seg.Seg.ID)
//		md.Set("x-amzn-tr-id", seg.Seg.TraceID)
//
//		// header is sent by itself
//		_ = grpc.SendHeader(ctx, md)
//
//		resp, err = handler(ctx, req)
//		return resp, err
//	} else {
//		return handler(ctx, req)
//	}
//}

// TracerStreamServerInterceptor will perform xray tracing for each stream RPC call
//
// to pass in parent xray segment id and trace id, set the metadata keys with:
//
//	x-amzn-seg-id = parent xray segment id, assign value to this key via metadata.MD
//	x-amzn-tr-id = parent xray trace id, assign value to this key via metadata.MD
//
// how to set metadata at client side?
//
//	ctx := context.Background()
//	md := metadata.Pairs("x-amzn-seg-id", "abc", "x-amzn-tr-id", "def")
//	ctx = metadata.NewOutgoingContext(ctx, md)
func TracerStreamServerInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	var seg *xray.XSegment // outer-scope segment for recovery/close
	headerSent := false

	// global panic guard even when X-Ray is disabled
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
		// skip health check calls from tracing noise
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

			// force header delivery even if handler never sends headers/messages
			if hdrErr := ss.SendHeader(outgoingMD); hdrErr != nil {
				err = status.Error(codes.Internal, hdrErr.Error())
				return err
			}

			return handler(srv, ss)
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

		// guard segment before binding/propagation
		if seg == nil || seg.Seg == nil {
			seg = nil
			return handler(srv, ss)
		}

		// bind the segment into the stream context so downstream code can see it
		segCtx := context.WithValue(ctx, awsxray.ContextKey, seg.Seg)
		wrappedStream := &contextServerStream{ServerStream: ss, ctx: segCtx}

		// avoid mutating incoming metadata; build an outgoing copy
		outgoingMD := metadata.New(nil)
		outgoingMD.Set("x-amzn-seg-id", seg.Seg.ID)
		traceHeader := formatTraceHeader(seg.Seg.TraceID, seg.Seg.ID, seg.Seg.Sampled)
		outgoingMD.Set("x-amzn-trace-id", traceHeader)
		outgoingMD.Set("x-amzn-tr-id", seg.Seg.TraceID)

		if hdrErr := grpc.SetHeader(segCtx, outgoingMD); hdrErr != nil {
			if seg != nil && seg.Seg != nil { // guard segment before use
				_ = seg.Seg.AddError(hdrErr)
			}
			err = status.Error(codes.Internal, hdrErr.Error())
			return err
		}

		err = handler(srv, wrappedStream)

		// ensure headers are actually delivered even if handler sent no messages/headers.
		if !headerSent {
			if hdrErr := wrappedStream.SendHeader(outgoingMD); hdrErr != nil {
				if seg != nil && seg.Seg != nil {
					_ = seg.Seg.AddError(hdrErr)
				}
				if err == nil { // preserve handler error precedence
					err = status.Error(codes.Internal, hdrErr.Error())
				}
			}
		}

		return err
	}

	return handler(srv, ss)
}
