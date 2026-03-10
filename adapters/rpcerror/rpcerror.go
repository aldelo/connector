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
	proto "google.golang.org/protobuf/proto"

	epb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
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

	Unknown []proto.Message
}

// return []proto.Message so it is directly compatible with status.WithDetails
func (d RpcErrorDetails) list() (lst []proto.Message) {
	// append all instances instead of only one.
	appendAll := func(ms []proto.Message) {
		for _, m := range ms {
			if m != nil {
				lst = append(lst, m)
			}
		}
	}

	appendAll(sliceToProto(d.RequestInfo))
	appendAll(sliceToProto(d.LocalizedMessage))
	appendAll(sliceToProto(d.ResourceInfo))
	appendAll(sliceToProto(d.RetryInfo))
	appendAll(sliceToProto(d.DebugInfo))
	appendAll(sliceToProto(d.ErrorInfo))
	appendAll(sliceToProto(d.PreconditionFailure))
	appendAll(sliceToProto(d.PreconditionFailure_Violation))
	appendAll(sliceToProto(d.BadRequest))
	appendAll(sliceToProto(d.BadRequest_FieldViolation))
	appendAll(sliceToProto(d.QuotaFailure))
	appendAll(sliceToProto(d.QuotaFailure_Violation))
	appendAll(sliceToProto(d.Help))
	appendAll(sliceToProto(d.Help_Link))
	appendAll(d.Unknown)

	return
}

// helper to convert typed slices to []proto.Message without allocations for each element.
//
// FIX: The original used `any(v) != nil` to filter nils, but this fails for typed nils.
// When T is e.g. *epb.ErrorInfo, a nil value like (*epb.ErrorInfo)(nil) creates an
// interface{} with type=*epb.ErrorInfo and value=nil. Since the interface has a type,
// `any(v) != nil` evaluates to TRUE — the typed nil leaks through.
//
// Downstream, anypb.New on a typed-nil proto.Message doesn't crash (protobuf handles
// nil receivers), but it produces an Any with empty value bytes, sending a meaningless
// empty error detail to the client.
//
// Fix: compare against the zero value of T. For all proto.Message implementations
// (which are pointer types), the zero value is nil. When v is a typed nil,
// any(v) == any(zero) is true because both have the same dynamic type and nil value.
// For non-nil v, the pointer values differ so the comparison is false.
func sliceToProto[T proto.Message](in []T) []proto.Message {
	out := make([]proto.Message, 0, len(in))
	var zero T
	for _, v := range in {
		if any(v) == any(zero) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// NewRpcError creates an error with rpc error detail data, to be returned via rpc method call's error field,
// so that rpc error details can be sent back to the rpc caller client
//
// withDetail = one or more detail proto error types from google.golang.org/genproto/googleapis/rpc/errdetails
func NewRpcError(code codes.Code, message string, details RpcErrorDetails) error {
	if code == codes.OK { // prevent nil-error result for OK code
		return nil
	}

	s := status.New(code, message)

	detailsList := details.list()

	if len(detailsList) == 0 { // early return for no details
		return s.Err()
	}

	p := s.Proto() // manually attach details to avoid protoadapter mismatch
	p.Details = make([]*anypb.Any, 0, len(detailsList))

	for _, d := range detailsList {
		anyObj, err := anypb.New(d) // marshals proto.Message
		if err != nil {
			return status.Errorf(codes.Internal, "failed to attach rpc error details: %v", err)
		}
		p.Details = append(p.Details, anyObj)
	}

	return status.FromProto(p).Err() // rebuild status with details
}

// ConvertToRpcError will convert error object into rpc status and error details
func ConvertToRpcError(err error) (*status.Status, RpcErrorDetails) {
	if err == nil {
		return status.New(codes.OK, ""), RpcErrorDetails{}
	}

	// normalize context errors (including wrapped) to their canonical gRPC statuses.
	if ctxStatus := status.FromContextError(err); ctxStatus.Code() == codes.Canceled || ctxStatus.Code() == codes.DeadlineExceeded {
		return ctxStatus, RpcErrorDetails{}
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
			if m, ok := info.(proto.Message); ok { // only keep details that satisfy proto.Message
				details.Unknown = append(details.Unknown, m)
			}
		}
	}

	return s, details
}
