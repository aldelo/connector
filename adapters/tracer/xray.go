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
		if xray.XRayServiceOn() {
			log.Println("!!! xray service on and tracing !!! ", info.FullMethod)
			if info != nil && strings.HasPrefix(info.FullMethod, "/grpc.health") {
				return handler(ctx, req)
			}
			return awsxray.UnaryServerInterceptor(awsxray.WithSegmentNamer(awsxray.NewFixedSegmentNamer(serviceName)))(ctx, req, info, handler)
		} else {
			log.Println("!!! xray service off !!!")
			return handler(ctx, req)
		}
	}
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
	if xray.XRayServiceOn() {
		// skip health check calls from tracing noise
		if info != nil && strings.HasPrefix(info.FullMethod, "/grpc.health") {
			return handler(srv, ss)
		}

		parentSegID := ""
		parentTraceID := ""

		var incomingMD metadata.MD
		var ok bool

		ctx := ss.Context()
		streamType := "StreamRPC"
		if info.IsClientStream {
			streamType = "Client" + streamType
		} else if info.IsServerStream {
			streamType = "Server" + streamType
		}

		if incomingMD, ok = metadata.FromIncomingContext(ctx); ok {
			if v, ok2 := incomingMD["x-amzn-seg-id"]; ok2 && len(v) > 0 {
				parentSegID = v[0]
			}

			if v, ok2 := incomingMD["x-amzn-tr-id"]; ok2 && len(v) > 0 {
				parentTraceID = v[0]
			}
		}

		var seg *xray.XSegment
		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
			seg = xray.NewSegment("GrpcService-"+streamType+"-"+info.FullMethod, &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID:   parentTraceID,
			})
		} else {
			seg = xray.NewSegment("GrpcService-" + streamType + "-" + info.FullMethod)
		}

		// mark panics as errors in X-Ray while rethrowing
		defer func() {
			if r := recover(); r != nil {
				_ = seg.Seg.AddError(fmt.Errorf("panic: %v", r))
				panic(r)
			}
		}()
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
			}
		}()

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
		if hdrErr := ss.SendHeader(outgoingMD); hdrErr != nil {
			_ = seg.Seg.AddError(hdrErr)
			return hdrErr
		}

		err = handler(srv, ss)
		return err
	}

	return handler(srv, ss)
}
