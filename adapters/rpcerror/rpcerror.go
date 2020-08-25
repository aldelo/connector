package rpcerror

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
	epb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/runtime/protoiface"
)

type RpcErrorDetails struct {
	RequestInfo *epb.RequestInfo
	LocalizedMessage *epb.LocalizedMessage
	ResourceInfo *epb.ResourceInfo
	RetryInfo *epb.RetryInfo

	DebugInfo *epb.DebugInfo
	ErrorInfo *epb.ErrorInfo

	PreconditionFailure *epb.PreconditionFailure
	PreconditionFailure_Violation *epb.PreconditionFailure_Violation

	BadRequest *epb.BadRequest
	BadRequest_FieldViolation *epb.BadRequest_FieldViolation

	QuotaFailure *epb.QuotaFailure
	QuotaFailure_Violation *epb.QuotaFailure_Violation

	Help *epb.Help
	Help_Link *epb.Help_Link
}

func (d RpcErrorDetails) list() (lst []protoiface.MessageV1) {
	if d.RequestInfo != nil {
		lst = append(lst, d.RequestInfo)
	}

	if d.LocalizedMessage != nil {
		lst = append(lst, d.LocalizedMessage)
	}

	if d.ResourceInfo != nil {
		lst = append(lst, d.ResourceInfo)
	}

	if d.RetryInfo != nil {
		lst = append(lst, d.RetryInfo)
	}

	if d.DebugInfo != nil {
		lst = append(lst, d.DebugInfo)
	}

	if d.ErrorInfo != nil {
		lst = append(lst, d.ErrorInfo)
	}

	if d.PreconditionFailure != nil {
		lst = append(lst, d.PreconditionFailure)
	}

	if d.PreconditionFailure_Violation != nil {
		lst = append(lst, d.PreconditionFailure_Violation)
	}

	if d.BadRequest != nil {
		lst = append(lst, d.BadRequest)
	}

	if d.BadRequest_FieldViolation != nil {
		lst = append(lst, d.BadRequest_FieldViolation)
	}

	if d.QuotaFailure != nil {
		lst = append(lst, d.QuotaFailure)
	}

	if d.QuotaFailure_Violation != nil {
		lst = append(lst, d.QuotaFailure_Violation)
	}

	if d.Help != nil {
		lst = append(lst, d.Help)
	}

	if d.Help_Link != nil {
		lst = append(lst, d.Help_Link)
	}

	return
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
			return s.Err()
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
			details.ErrorInfo = info
		case *epb.BadRequest:
			details.BadRequest = info
		case *epb.PreconditionFailure:
			details.PreconditionFailure = info
		case *epb.BadRequest_FieldViolation:
			details.BadRequest_FieldViolation = info
		case *epb.DebugInfo:
			details.DebugInfo = info
		case *epb.Help:
			details.Help = info
		case *epb.Help_Link:
			details.Help_Link = info
		case *epb.LocalizedMessage:
			details.LocalizedMessage = info
		case *epb.PreconditionFailure_Violation:
			details.PreconditionFailure_Violation = info
		case *epb.QuotaFailure:
			details.QuotaFailure = info
		case *epb.QuotaFailure_Violation:
			details.QuotaFailure_Violation = info
		case *epb.RequestInfo:
			details.RequestInfo = info
		case *epb.ResourceInfo:
			details.ResourceInfo = info
		case *epb.RetryInfo:
			details.RetryInfo = info
		}
	}

	return s, details
}

