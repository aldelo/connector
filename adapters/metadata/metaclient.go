package metadata

import (
	"context"
	"fmt"
	util "github.com/aldelo/common"
	"google.golang.org/grpc/metadata"
)

type MetaClient struct {}

// NewOutgoingMetadataContext creates a new outgoing context with kv pairs, to be used during rpc call
//
// Example:
//		mc := &MetaClient{}
//		ctx, err := mc.NewOutgoingMetadataContext(context.Background(), map[string]string{...})
//
// 		<for unary rpc calls>
//			var header, trailer metadata.MD
//			r, err := x.UnaryRPCMethod(ctx, &pb.XyzRequest{...}, grpc.Header(&header), grpc.Trailer(&trailer))
//			v := mc.Value(header, "xyz")
//			v := mc.Value(trailer, "efg")
// 			v := mc.Slice(header, "yyy")
//
// 		<for stream calls>
//			stream, err := x.ServerStreamRPCMethod(ctx, &pb.XyzRequest{...})
//
//			header, err := stream.Header()
//			trailer, err := stream.Trailer()
//
//			v := mc.Value(header, "xyz")
//			v := mc.Value(header, "efg")
//			v := mc.Slice("header", "yyy")
func (c *MetaClient) NewOutgoingMetadataContext(sourceCtx context.Context, kvPairs map[string]string) (context.Context, error) {
	if kvPairs == nil {
		return nil, fmt.Errorf("Create New Outgoing Metadata Context Failed: %s", "KvPairs Must Be Set")
	}

	if len(kvPairs) == 0 {
		return nil, fmt.Errorf("Create New Outgoing Metadata Context Failed: %s", "KvPairs Minimum of One is Required")
	}

	md := metadata.New(kvPairs)
	return metadata.NewOutgoingContext(sourceCtx, md), nil
}

// Value is a helper that retrieves string value from metadata header or trailer
func (c *MetaClient) Value(md metadata.MD, key string) string {
	if md == nil {
		return ""
	}

	if util.LenTrim(key) == 0 {
		return ""
	}

	if v, ok := md[key]; ok {
		if len(v) > 0 {
			return v[0]
		} else {
			return ""
		}
	} else {
		return ""
	}
}

// Slice is a helper that retrieves string slice from metadata header or trailer
func (c *MetaClient) Slice(md metadata.MD, key string) []string {
	if md == nil {
		return []string{}
	}

	if util.LenTrim(key) == 0 {
		return []string{}
	}

	if v, ok := md[key]; ok {
		if len(v) > 0 {
			return v
		} else {
			return []string{}
		}
	} else {
		return []string{}
	}
}
