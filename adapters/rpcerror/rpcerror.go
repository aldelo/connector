package rpcerror

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
	epb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/runtime/protoiface"
)

// allow multiple instances of each detail type to avoid data loss
type RpcErrorDetails struct {
	RequestInfo      []*epb.RequestInfo
	LocalizedMessage []*epb.LocalizedMessage
	ResourceInfo     []*epb.ResourceInfo
	RetryInfo        []*epb.RetryInfo

	DebugInfo []*epb.DebugInfo
	ErrorInfo []*epb.ErrorInfo

	PreconditionFailure           []*epb.PreconditionFailure
	PreconditionFailure_Violation []*epb.PreconditionFailure_Violation

	BadRequest                []*epb.BadRequest
	BadRequest_FieldViolation []*epb.BadRequest_FieldViolation

	QuotaFailure           []*epb.QuotaFailure
	QuotaFailure_Violation []*epb.QuotaFailure_Violation

	Help      []*epb.Help
	Help_Link []*epb.Help_Link

	Unknown []protoiface.MessageV1
}

// return []proto.Message so it is directly compatible with status.WithDetails
func (d RpcErrorDetails) list() (lst []protoiface.MessageV1) {
	// CHANGED: append all instances instead of only one.
	appendAll := func(ms []protoiface.MessageV1) {
		for _, m := range ms {
			if m != nil {
				lst = append(lst, m)
			}
		}
	}

	appendAll(sliceToV1(d.RequestInfo))
	appendAll(sliceToV1(d.LocalizedMessage))
	appendAll(sliceToV1(d.ResourceInfo))
	appendAll(sliceToV1(d.RetryInfo))
	appendAll(sliceToV1(d.DebugInfo))
	appendAll(sliceToV1(d.ErrorInfo))
	appendAll(sliceToV1(d.PreconditionFailure))
	appendAll(sliceToV1(d.PreconditionFailure_Violation))
	appendAll(sliceToV1(d.BadRequest))
	appendAll(sliceToV1(d.BadRequest_FieldViolation))
	appendAll(sliceToV1(d.QuotaFailure))
	appendAll(sliceToV1(d.QuotaFailure_Violation))
	appendAll(sliceToV1(d.Help))
	appendAll(sliceToV1(d.Help_Link))
	appendAll(d.Unknown)

	return
}

// helper to convert typed slices to []protoiface.MessageV1 without allocations for each element.
func sliceToV1[T protoiface.MessageV1](in []*T) []protoiface.MessageV1 {
	out := make([]protoiface.MessageV1, 0, len(in))
	for _, v := range in {
		if v != nil {
			out = append(out, v)
		}
	}
	return out
}

// NewRpcError creates an error with rpc error detail data, to be returned via rpc method call's error field,
// so that rpc error details can be sent back to the rpc caller client
//
// withDetail = one or more detail proto error types from google.golang.org/genproto/googleapis/rpc/errdetails
func NewRpcError(code codes.Code, message string, details RpcErrorDetails) error {
	s := status.New(code, message)

	dlist := details.list()

	if len(dlist) > 0 {
		if d, e := s.WithDetails(dlist...); e != nil {
			return status.Errorf(codes.Internal, "failed to attach rpc error details: %v", e)
		} else {
			return d.Err()
		}
	} else {
		return s.Err()
	}
}

// ConvertToRpcError will convert error object into rpc status and error details
func ConvertToRpcError(err error) (*status.Status, RpcErrorDetails) {
	if err == nil {
		return nil, RpcErrorDetails{}
	}

	s := status.Convert(err)
	details := RpcErrorDetails{}

	for _, d := range s.Details() {
		switch info := d.(type) {
		case *epb.ErrorInfo:
			details.ErrorInfo = append(details.ErrorInfo, info)
		case *epb.BadRequest:
			details.BadRequest = append(details.BadRequest, info)
		case *epb.PreconditionFailure:
			details.PreconditionFailure = append(details.PreconditionFailure, info)
		case *epb.BadRequest_FieldViolation:
			details.BadRequest_FieldViolation = append(details.BadRequest_FieldViolation, info)
		case *epb.DebugInfo:
			details.DebugInfo = append(details.DebugInfo, info)
		case *epb.Help:
			details.Help = append(details.Help, info)
		case *epb.Help_Link:
			details.Help_Link = append(details.Help_Link, info)
		case *epb.LocalizedMessage:
			details.LocalizedMessage = append(details.LocalizedMessage, info)
		case *epb.PreconditionFailure_Violation:
			details.PreconditionFailure_Violation = append(details.PreconditionFailure_Violation, info)
		case *epb.QuotaFailure:
			details.QuotaFailure = append(details.QuotaFailure, info)
		case *epb.QuotaFailure_Violation:
			details.QuotaFailure_Violation = append(details.QuotaFailure_Violation, info)
		case *epb.RequestInfo:
			details.RequestInfo = append(details.RequestInfo, info)
		case *epb.ResourceInfo:
			details.ResourceInfo = append(details.ResourceInfo, info)
		case *epb.RetryInfo:
			details.RetryInfo = append(details.RetryInfo, info)
		default:
			details.Unknown = append(details.Unknown, info)
		}
	}

	return s, details
}
