package tracer

/*
 * Copyright 2020-2021 Aldelo, LP
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
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/xray"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// TracerUnaryServerInterceptor will perform xray tracing for each unary RPC call
//
// to pass in parent xray segment id and trace id, set the metadata keys with:
//		x-amzn-seg-id = parent xray segment id, assign value to this key via metadata.MD
//		x-amzn-tr-id = parent xray trace id, assign value to this key via metadata.MD
//
// how to set metadata at client side?
//		ctx := context.Background()
//		md := metadata.Pairs("x-amzn-seg-id", "abc", "x-amzn-tr-id", "def")
//		ctx = metadata.NewOutgoingContext(ctx, md)
func TracerUnaryServerInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	if xray.XRayServiceOn() {
		parentSegID := ""
		parentTraceID := ""

		var md metadata.MD
		var ok bool

		if md, ok = metadata.FromIncomingContext(ctx); ok {
			if v, ok2 := md["x-amzn-seg-id"]; ok2 && len(v) > 0 {
				parentSegID = v[0]
			}

			if v, ok2 := md["x-amzn-tr-id"]; ok2 && len(v) > 0 {
				parentTraceID = v[0]
			}
		}

		var seg *xray.XSegment

		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
			seg = xray.NewSegment("GrpcService-UnaryRPC [" + info.FullMethod + "]", &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID: parentTraceID,
			})
		} else {
			seg = xray.NewSegment("GrpcService-UnaryRPC [" + info.FullMethod + "]")
		}
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
			}
		}()

		if md == nil {
			md = make(metadata.MD)
		}

		md.Set("x-amzn-seg-id", seg.Seg.ID)
		md.Set("x-amzn-tr-id", seg.Seg.TraceID)

		// header is sent by itself
		_ = grpc.SendHeader(ctx, md)

		resp, err = handler(ctx, req)
		return resp, err
	} else {
		return handler(ctx, req)
	}
}

// TracerStreamServerInterceptor will perform xray tracing for each stream RPC call
//
// to pass in parent xray segment id and trace id, set the metadata keys with:
//		x-amzn-seg-id = parent xray segment id, assign value to this key via metadata.MD
//		x-amzn-tr-id = parent xray trace id, assign value to this key via metadata.MD
//
// how to set metadata at client side?
//		ctx := context.Background()
//		md := metadata.Pairs("x-amzn-seg-id", "abc", "x-amzn-tr-id", "def")
//		ctx = metadata.NewOutgoingContext(ctx, md)
func TracerStreamServerInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	if xray.XRayServiceOn() {
		parentSegID := ""
		parentTraceID := ""

		var md metadata.MD
		var ok bool

		ctx := ss.Context()
		streamType := "StreamRPC"
		if info.IsClientStream {
			streamType = "Client" + streamType
		} else if info.IsServerStream {
			streamType = "Server" + streamType
		}

		if md, ok = metadata.FromIncomingContext(ctx); ok {
			if v, ok2 := md["x-amzn-seg-id"]; ok2 && len(v) > 0 {
				parentSegID = v[0]
			}

			if v, ok2 := md["x-amzn-tr-id"]; ok2 && len(v) > 0 {
				parentTraceID = v[0]
			}
		}

		var seg *xray.XSegment

		if util.LenTrim(parentSegID) > 0 && util.LenTrim(parentTraceID) > 0 {
			seg = xray.NewSegment("GrpcService-" + streamType + " [" + info.FullMethod + "]", &xray.XRayParentSegment{
				SegmentID: parentSegID,
				TraceID: parentTraceID,
			})
		} else {
			seg = xray.NewSegment("GrpcService-" + streamType + " [" + info.FullMethod + "]")
		}
		defer seg.Close()
		defer func() {
			if err != nil {
				_ = seg.Seg.AddError(err)
			}
		}()

		if md == nil {
			md = make(metadata.MD)
		}

		md.Set("x-amzn-seg-id", seg.Seg.ID)
		md.Set("x-amzn-tr-id", seg.Seg.TraceID)

		_ = ss.SendHeader(md)

		err = handler(srv, ss)
		return err
	} else {
		return handler(srv, ss)
	}
}
