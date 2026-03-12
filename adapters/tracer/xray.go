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

func panicTraceError(r interface{}) error {
	return fmt.Errorf("panic: %v\n%s", r, debug.Stack())
}

func panicClientError(r interface{}) error {
	return fmt.Errorf("panic: %v", r)
}

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

// contextServerStream overrides only Context() to bind the X-Ray segment.
//
// FIX: The previous implementation overrode SetHeader, SendHeader, and SendMsg
// with CAS-gated header management. This caused "SendHeader called multiple times"
// because grpc.SendHeader(stream.Context(), md) bypasses any stream wrapper — it
// extracts the transport stream directly from the context via
// ServerTransportStreamFromContext. Our wrapper's CAS only tracked its own calls,
// not transport-level sends from grpc.SendHeader, grpc.SetHeader + auto-send, or
// handler code using the context-based API. Any combination of wrapper-send +
// transport-send = double send = transport error.
//
// The fix: don't manage headers in the wrapper at all. Stage trace headers via
// grpc.SetHeader(ctx, md) at the transport level. gRPC auto-sends staged headers
// with the first response message (or with trailers if no messages are sent).
// This is the standard gRPC pattern and cannot double-send regardless of what
// the handler, other interceptors, or libraries do with headers.
type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *contextServerStream) Context() context.Context {
	return w.ctx
}

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

// TracerStreamServerInterceptor performs X-Ray tracing for each stream RPC call.
// Trace headers are staged via grpc.SetHeader at the transport level — gRPC
// auto-sends them with the first response, eliminating any possibility of
// "SendHeader called multiple times".
func TracerStreamServerInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
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
		return handler(srv, ss)
	}

	fullMethod := "unknown"
	if info != nil {
		fullMethod = info.FullMethod
	}
	if strings.HasPrefix(fullMethod, "/grpc.health") {
		return handler(srv, ss)
	}

	ctx := ss.Context()

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

	if incomingMD, ok := metadata.FromIncomingContext(ctx); ok {
		parentSegID, parentTraceID, sampled, rawHeader = extractParentIDs(incomingMD)
	}

	// Respect Sampled=0: stage pass-through headers, no segment.
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

		// Stage at transport level — gRPC auto-sends with first response.
		if hdrErr := grpc.SetHeader(ctx, outgoingMD); hdrErr != nil {
			return status.Error(codes.Internal, hdrErr.Error())
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

	if seg == nil || seg.Seg == nil {
		seg = nil
		return handler(srv, ss)
	}

	// Bind segment into a derived context.
	segCtx := context.WithValue(ctx, awsxray.ContextKey, seg.Seg)

	// Stage trace headers at the transport level.
	// grpc.SetHeader finds the transport stream via ServerTransportStreamFromContext
	// on the context chain — our context.WithValue preserves the parent's reference.
	// gRPC auto-sends staged headers with the first response message or trailers.
	outgoingMD := metadata.New(nil)
	outgoingMD.Set("x-amzn-seg-id", seg.Seg.ID)
	traceHeader := formatTraceHeader(seg.Seg.TraceID, seg.Seg.ID, seg.Seg.Sampled)
	if traceHeader != "" {
		outgoingMD.Set("x-amzn-trace-id", traceHeader)
	}
	outgoingMD.Set("x-amzn-tr-id", seg.Seg.TraceID)

	if hdrErr := grpc.SetHeader(segCtx, outgoingMD); hdrErr != nil {
		_ = seg.Seg.AddError(hdrErr)
		return status.Error(codes.Internal, hdrErr.Error())
	}

	// Minimal wrapper — only overrides Context() to expose the segment.
	wrappedStream := &contextServerStream{
		ServerStream: ss,
		ctx:          segCtx,
	}

	return handler(srv, wrappedStream)
}
