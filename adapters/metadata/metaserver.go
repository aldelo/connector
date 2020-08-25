package metadata

/*
 * Copyright 2020 Aldelo, LP
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
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

type MetaServer struct {}

// FromIncomingMetadataContext retrieves kv pairs from incoming metadata context, and return as map
// this method is called from within server side RPC method handler
//
// Example:
//		ms := &MetaServer{}
//		m, err := ms.FromIncomingMetadataContext(ctx)
//		v1 := m["xyz"]
//		v2 := m["efg"]
func (s *MetaServer) FromIncomingMetadataContext(sourceCtx context.Context) (kv map[string]string, err error) {
	if sourceCtx == nil {
		return nil, fmt.Errorf("Retrieve Kv Pairs From Incoming Metadata Context Failed: %s", "Source Context is Required")
	}

	if md, ok := metadata.FromIncomingContext(sourceCtx); ok {
		kv = make(map[string]string)

		for k, v := range md {
			if len(v) > 0 {
				kv[k] = v[0]
			} else {
				kv[k] = ""
			}
		}

		return kv, nil
	} else {
		return nil, fmt.Errorf("Retrieve Kv Pairs From Incoming Metadata Context Failed: (Code: %d) %s", codes.DataLoss, "DataLoss - Fail To Get Metadata")
	}
}

// SendHeader will send metadata header by server,
// may be called at most only once per rpc call
//
// within RPC call, just before RPC returns response, call SendHeader to send header metadata to caller
func (s *MetaServer) SendHeader(ctx context.Context, kvPairs map[string]string) error {
	if ctx == nil {
		return fmt.Errorf("Send Metadata Header Failed: %s", "Context is Required")
	}

	if kvPairs == nil {
		return fmt.Errorf("Send Metadata Header Failed: %s", "KvPairs Must Be Set")
	}

	if len(kvPairs) == 0 {
		return fmt.Errorf("Send Metadata Header Failed: %s", "KvPairs Minimum of One is Required")
	}

	header := metadata.New(kvPairs)
	return grpc.SendHeader(ctx, header)
}

// SetTrailer is called via 'defer', so that after RPC call response is given, the defer is triggered to set Trailer, if applicable
// such as: defer ms.SetTrailer(...)
func (s *MetaServer) SetTrailer(ctx context.Context, kvPairs map[string]string) error {
	if ctx == nil {
		return fmt.Errorf("Set Metadata Trailer Failed: %s", "Context is Required")
	}

	if kvPairs == nil {
		return fmt.Errorf("Send Metadata Trailer Failed: %s", "KvPairs Must Be Set")
	}

	if len(kvPairs) == 0 {
		return fmt.Errorf("Send Metadata Trailer Failed: %s", "KvPairs Minimum of One is Required")
	}

	trailer := metadata.New(kvPairs)
	return grpc.SetTrailer(ctx, trailer)
}