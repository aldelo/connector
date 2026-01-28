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
	"log"
	"strings"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/xray"
	awsxray "github.com/aws/aws-xray-sdk-go/xray"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TracerUnaryClientInterceptor(serviceName string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if xray.XRayServiceOn() {
			log.Println("!!! xray is on")
			// bypass health check
			if strings.HasPrefix(method, "/grpc.health") {
				return invoker(ctx, method, req, reply, cc, opts...)
			}
			// bypass xray tracer if no segment exists
			if awsxray.GetSegment(ctx) == nil {
				log.Println("!!! segment context is nil")
				return invoker(ctx, method, req, reply, cc, opts...)
			}
			log.Println("!!! all ready, trace it. ")
			return awsxray.UnaryClientInterceptor(awsxray.WithSegmentNamer(awsxray.NewFixedSegmentNamer(serviceName)))(ctx, method, req, reply, cc, invoker, opts...)
		} else {
			log.Println("!!! xray is off")
			return invoker(ctx, method, req, reply, cc, opts...)
		}
	}
}

func TracerUnaryServerInterceptor(serviceName string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
		var seg *xray.XSegment // keep segment in outer scope for deferred recovery/close

		// global panic guard even when X-Ray is disabled, to avoid crashing the server
		defer func() {
			if r := recover(); r != nil {
				recErr := fmt.Errorf("panic: %v", r)
				if seg != nil && seg.Seg != nil {
					_ = seg.Seg.AddError(recErr)
					seg.Close()
				}
				err = recErr
				return
			}
			if seg != nil {
				if err != nil {
					_ = seg.Seg.AddError(err)
				}
				seg.Close()
			}
		}()

		if !xray.XRayServiceOn() {
			log.Println("!!! xray service off !!!")
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
		parentSegID, parentTraceID := "", ""
		if md, ok := metadata.FromIncomingContext(ctx); ok {
			if v, ok2 := md["x-amzn-seg-id"]; ok2 && len(v) > 0 {
				parentSegID = v[0]
			}
			if v, ok2 := md["x-amzn-tr-id"]; ok2 && len(v) > 0 {
				parentTraceID = v[0]
			}
		}

		// Create segment with optional parent.
		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
			seg = xray.NewSegment("GrpcService-UnaryRPC-"+fullMethod, &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID:   parentTraceID,
			})
		} else {
			seg = xray.NewSegment("GrpcService-UnaryRPC-" + fullMethod)
		}

		// Bind segment to context for downstream code.
		segCtx := context.WithValue(ctx, awsxray.ContextKey, seg.Seg)

		// Propagate tracing headers back to caller.
		outgoingMD := metadata.New(nil)
		outgoingMD.Set("x-amzn-seg-id", seg.Seg.ID)
		outgoingMD.Set("x-amzn-tr-id", seg.Seg.TraceID)
		if hdrErr := grpc.SendHeader(segCtx, outgoingMD); hdrErr != nil {
			_ = seg.Seg.AddError(hdrErr)
			return nil, hdrErr
		}

		return handler(segCtx, req)
	}
}

// bind the created segment into the stream context and use the wrapped stream for SendHeader and handler.
type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *contextServerStream) Context() context.Context {
	return w.ctx
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

	// global panic guard even when X-Ray is disabled
	defer func() {
		if r := recover(); r != nil {
			recErr := fmt.Errorf("panic: %v", r)
			if seg != nil && seg.Seg != nil {
				_ = seg.Seg.AddError(recErr)
				seg.Close()
			}
			err = recErr
			return
		}
		if seg != nil {
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

		parentSegID := ""
		parentTraceID := ""

		var incomingMD metadata.MD
		var ok bool

		ctx := ss.Context()
		streamType := "StreamRPC"
		if info != nil {
			// Guard against nil info before deref.
			if info.IsClientStream {
				streamType = "Client" + streamType
			} else if info.IsServerStream {
				streamType = "Server" + streamType
			}
		}

		if incomingMD, ok = metadata.FromIncomingContext(ctx); ok {
			if v, ok2 := incomingMD["x-amzn-seg-id"]; ok2 && len(v) > 0 {
				parentSegID = v[0]
			}

			if v, ok2 := incomingMD["x-amzn-tr-id"]; ok2 && len(v) > 0 {
				parentTraceID = v[0]
			}
		}

		// use fullMethod (with safe fallback) instead of directly dereferencing info
		segmentName := "GrpcService-" + streamType + "-" + fullMethod

		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
			seg = xray.NewSegment("GrpcService-"+streamType+"-"+info.FullMethod, &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID:   parentTraceID,
			})
		} else {
			seg = xray.NewSegment(segmentName)
		}

		// bind the segment into the stream context so downstream code can see it
		segCtx := context.WithValue(ctx, awsxray.ContextKey, seg.Seg)
		wrappedStream := &contextServerStream{ServerStream: ss, ctx: segCtx}

		// avoid mutating incoming metadata; build an outgoing copy
		outgoingMD := metadata.New(nil)
		if incomingMD != nil {
			for k, v := range incomingMD {
				outgoingMD[k] = append([]string(nil), v...)
			}
		}
		outgoingMD.Set("x-amzn-seg-id", seg.Seg.ID)
		outgoingMD.Set("x-amzn-tr-id", seg.Seg.TraceID)

		// surface header send failures to the caller and trace
		if hdrErr := wrappedStream.SendHeader(outgoingMD); hdrErr != nil {
			_ = seg.Seg.AddError(hdrErr)
			err = hdrErr
			return hdrErr
		}

		err = handler(srv, wrappedStream)
		return err
	}

	return handler(srv, ss)
}
