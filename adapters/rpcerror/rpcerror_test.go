package rpcerror

import (
	"context"
	"fmt"
	"testing"

	epb "google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// ---------------------------------------------------------------------------
// sliceToProto
// ---------------------------------------------------------------------------

func TestSliceToProto_EmptySlice(t *testing.T) {
	out := sliceToProto([]*epb.ErrorInfo{})
	if len(out) != 0 {
		t.Fatalf("expected empty result, got %d elements", len(out))
	}
}

func TestSliceToProto_NilSlice(t *testing.T) {
	var in []*epb.ErrorInfo
	out := sliceToProto(in)
	if len(out) != 0 {
		t.Fatalf("expected empty result for nil input, got %d elements", len(out))
	}
}

func TestSliceToProto_AllNilElements(t *testing.T) {
	in := []*epb.ErrorInfo{nil, nil, nil}
	out := sliceToProto(in)
	if len(out) != 0 {
		t.Fatalf("expected 0 elements after filtering nils, got %d", len(out))
	}
}

func TestSliceToProto_TypedNils(t *testing.T) {
	// This is the core scenario the fix addresses.
	// A typed nil like (*epb.ErrorInfo)(nil) wraps into an interface with a
	// non-nil type pointer, so `any(v) != nil` would incorrectly be true.
	var typedNil *epb.ErrorInfo // == (*epb.ErrorInfo)(nil)
	in := []*epb.ErrorInfo{typedNil}

	out := sliceToProto(in)
	if len(out) != 0 {
		t.Fatalf("typed nil should be filtered out, but got %d elements", len(out))
	}
}

func TestSliceToProto_ValidElements(t *testing.T) {
	a := &epb.ErrorInfo{Reason: "A"}
	b := &epb.ErrorInfo{Reason: "B"}
	out := sliceToProto([]*epb.ErrorInfo{a, b})

	if len(out) != 2 {
		t.Fatalf("expected 2 elements, got %d", len(out))
	}
	if out[0].(*epb.ErrorInfo).GetReason() != "A" {
		t.Error("first element mismatch")
	}
	if out[1].(*epb.ErrorInfo).GetReason() != "B" {
		t.Error("second element mismatch")
	}
}

func TestSliceToProto_MixedNilAndValid(t *testing.T) {
	valid := &epb.ErrorInfo{Reason: "valid"}
	var typedNil *epb.ErrorInfo

	in := []*epb.ErrorInfo{nil, valid, typedNil, nil}
	out := sliceToProto(in)

	if len(out) != 1 {
		t.Fatalf("expected 1 valid element, got %d", len(out))
	}
	if out[0].(*epb.ErrorInfo).GetReason() != "valid" {
		t.Error("valid element mismatch")
	}
}

func TestSliceToProto_DifferentProtoTypes(t *testing.T) {
	// Verify the generic works across different proto types.
	ri := &epb.RequestInfo{RequestId: "req-1"}
	out := sliceToProto([]*epb.RequestInfo{nil, ri})
	if len(out) != 1 {
		t.Fatalf("expected 1 element, got %d", len(out))
	}
	if out[0].(*epb.RequestInfo).GetRequestId() != "req-1" {
		t.Error("RequestInfo value mismatch")
	}
}

// ---------------------------------------------------------------------------
// RpcErrorDetails.list()
// ---------------------------------------------------------------------------

func TestList_EmptyDetails(t *testing.T) {
	d := RpcErrorDetails{}
	lst := d.list()
	if len(lst) != 0 {
		t.Fatalf("expected 0 for empty details, got %d", len(lst))
	}
}

func TestList_AllFieldsPopulated(t *testing.T) {
	d := RpcErrorDetails{
		RequestInfo:                   []*epb.RequestInfo{{RequestId: "r1"}},
		LocalizedMessage:              []*epb.LocalizedMessage{{Locale: "en"}},
		ResourceInfo:                  []*epb.ResourceInfo{{ResourceType: "t"}},
		RetryInfo:                     []*epb.RetryInfo{{}},
		DebugInfo:                     []*epb.DebugInfo{{Detail: "dbg"}},
		ErrorInfo:                     []*epb.ErrorInfo{{Reason: "err"}},
		PreconditionFailure:           []*epb.PreconditionFailure{{}},
		PreconditionFailure_Violation: []*epb.PreconditionFailure_Violation{{Type: "v"}},
		BadRequest:                    []*epb.BadRequest{{}},
		BadRequest_FieldViolation:     []*epb.BadRequest_FieldViolation{{Field: "f"}},
		QuotaFailure:                  []*epb.QuotaFailure{{}},
		QuotaFailure_Violation:        []*epb.QuotaFailure_Violation{{Subject: "s"}},
		Help:                          []*epb.Help{{}},
		Help_Link:                     []*epb.Help_Link{{Url: "http://example.com"}},
		Unknown:                       []proto.Message{&epb.ErrorInfo{Reason: "unknown"}},
	}

	lst := d.list()
	// 14 typed fields + 1 Unknown = 15
	if len(lst) != 15 {
		t.Fatalf("expected 15 elements, got %d", len(lst))
	}
}

func TestList_PartialFields(t *testing.T) {
	d := RpcErrorDetails{
		ErrorInfo:   []*epb.ErrorInfo{{Reason: "one"}, {Reason: "two"}},
		RequestInfo: []*epb.RequestInfo{{RequestId: "req"}},
	}
	lst := d.list()
	if len(lst) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(lst))
	}
}

func TestList_NilsInFieldsAreFiltered(t *testing.T) {
	d := RpcErrorDetails{
		ErrorInfo: []*epb.ErrorInfo{nil, {Reason: "keep"}, nil},
	}
	lst := d.list()
	if len(lst) != 1 {
		t.Fatalf("expected 1 element after nil filtering, got %d", len(lst))
	}
}

func TestList_NilUnknownElementsFiltered(t *testing.T) {
	// Unknown goes through appendAll which checks != nil.
	d := RpcErrorDetails{
		Unknown: []proto.Message{nil, &epb.ErrorInfo{Reason: "ok"}, nil},
	}
	lst := d.list()
	if len(lst) != 1 {
		t.Fatalf("expected 1 element, got %d", len(lst))
	}
}

func TestList_MultipleInstancesPerField(t *testing.T) {
	d := RpcErrorDetails{
		DebugInfo: []*epb.DebugInfo{
			{Detail: "d1"},
			{Detail: "d2"},
			{Detail: "d3"},
		},
	}
	lst := d.list()
	if len(lst) != 3 {
		t.Fatalf("expected 3, got %d", len(lst))
	}
}

// ---------------------------------------------------------------------------
// NewRpcError
// ---------------------------------------------------------------------------

func TestNewRpcError_CodesOK_ReturnsNil(t *testing.T) {
	err := NewRpcError(codes.OK, "this should not matter", RpcErrorDetails{})
	if err != nil {
		t.Fatal("expected nil for codes.OK")
	}
}

func TestNewRpcError_CodesOK_WithDetails_StillNil(t *testing.T) {
	err := NewRpcError(codes.OK, "msg", RpcErrorDetails{
		ErrorInfo: []*epb.ErrorInfo{{Reason: "test"}},
	})
	if err != nil {
		t.Fatal("expected nil for codes.OK even with details")
	}
}

func TestNewRpcError_WithoutDetails(t *testing.T) {
	err := NewRpcError(codes.NotFound, "item missing", RpcErrorDetails{})
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	s, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", s.Code())
	}
	if s.Message() != "item missing" {
		t.Fatalf("expected 'item missing', got %q", s.Message())
	}
	if len(s.Details()) != 0 {
		t.Fatalf("expected 0 details, got %d", len(s.Details()))
	}
}

func TestNewRpcError_WithDetails(t *testing.T) {
	err := NewRpcError(codes.InvalidArgument, "bad input", RpcErrorDetails{
		ErrorInfo: []*epb.ErrorInfo{{Reason: "INVALID", Domain: "test.domain"}},
		BadRequest: []*epb.BadRequest{
			{FieldViolations: []*epb.BadRequest_FieldViolation{
				{Field: "email", Description: "invalid format"},
			}},
		},
	})
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	s, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", s.Code())
	}

	details := s.Details()
	if len(details) != 2 {
		t.Fatalf("expected 2 details, got %d", len(details))
	}

	// Verify types are preserved after serialization round-trip through Any.
	var foundErrorInfo, foundBadRequest bool
	for _, d := range details {
		switch info := d.(type) {
		case *epb.ErrorInfo:
			foundErrorInfo = true
			if info.GetReason() != "INVALID" {
				t.Errorf("expected reason INVALID, got %q", info.GetReason())
			}
			if info.GetDomain() != "test.domain" {
				t.Errorf("expected domain test.domain, got %q", info.GetDomain())
			}
		case *epb.BadRequest:
			foundBadRequest = true
			if len(info.GetFieldViolations()) != 1 {
				t.Errorf("expected 1 field violation, got %d", len(info.GetFieldViolations()))
			}
		}
	}
	if !foundErrorInfo {
		t.Error("ErrorInfo not found in details")
	}
	if !foundBadRequest {
		t.Error("BadRequest not found in details")
	}
}

func TestNewRpcError_ErrorCodeWithMessage(t *testing.T) {
	testCases := []struct {
		code    codes.Code
		message string
	}{
		{codes.Internal, "internal server error"},
		{codes.PermissionDenied, "access denied"},
		{codes.Unavailable, "service temporarily unavailable"},
		{codes.Unauthenticated, ""},
	}

	for _, tc := range testCases {
		t.Run(tc.code.String(), func(t *testing.T) {
			err := NewRpcError(tc.code, tc.message, RpcErrorDetails{})
			if err == nil {
				t.Fatal("expected non-nil error")
			}
			s, ok := status.FromError(err)
			if !ok {
				t.Fatal("expected gRPC status error")
			}
			if s.Code() != tc.code {
				t.Fatalf("expected %v, got %v", tc.code, s.Code())
			}
			if s.Message() != tc.message {
				t.Fatalf("expected %q, got %q", tc.message, s.Message())
			}
		})
	}
}

func TestNewRpcError_DetailsWithNilsFiltered(t *testing.T) {
	// Nils (including typed nils) in detail slices should be filtered out,
	// leaving only the valid detail.
	err := NewRpcError(codes.Internal, "err", RpcErrorDetails{
		ErrorInfo: []*epb.ErrorInfo{nil, {Reason: "valid"}, nil},
	})
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	s, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if len(s.Details()) != 1 {
		t.Fatalf("expected 1 detail after nil filtering, got %d", len(s.Details()))
	}
}

func TestNewRpcError_MultipleDetailTypes(t *testing.T) {
	err := NewRpcError(codes.FailedPrecondition, "precondition", RpcErrorDetails{
		DebugInfo:    []*epb.DebugInfo{{Detail: "stack trace here"}},
		ResourceInfo: []*epb.ResourceInfo{{ResourceType: "db", ResourceName: "users"}},
		RetryInfo:    []*epb.RetryInfo{{}},
	})
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	s, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	if len(s.Details()) != 3 {
		t.Fatalf("expected 3 details, got %d", len(s.Details()))
	}
}

// ---------------------------------------------------------------------------
// ConvertToRpcError
// ---------------------------------------------------------------------------

func TestConvertToRpcError_NilError(t *testing.T) {
	s, details := ConvertToRpcError(nil)
	if s.Code() != codes.OK {
		t.Fatalf("expected OK, got %v", s.Code())
	}
	if s.Message() != "" {
		t.Fatalf("expected empty message, got %q", s.Message())
	}
	if len(details.list()) != 0 {
		t.Fatal("expected empty details for nil error")
	}
}

func TestConvertToRpcError_ContextCanceled(t *testing.T) {
	s, details := ConvertToRpcError(context.Canceled)
	if s.Code() != codes.Canceled {
		t.Fatalf("expected Canceled, got %v", s.Code())
	}
	if len(details.list()) != 0 {
		t.Fatal("expected empty details for context cancel")
	}
}

func TestConvertToRpcError_ContextDeadlineExceeded(t *testing.T) {
	s, details := ConvertToRpcError(context.DeadlineExceeded)
	if s.Code() != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded, got %v", s.Code())
	}
	if len(details.list()) != 0 {
		t.Fatal("expected empty details for deadline exceeded")
	}
}

func TestConvertToRpcError_WrappedContextCanceled(t *testing.T) {
	wrapped := fmt.Errorf("operation failed: %w", context.Canceled)
	s, _ := ConvertToRpcError(wrapped)
	if s.Code() != codes.Canceled {
		t.Fatalf("expected Canceled for wrapped context error, got %v", s.Code())
	}
}

func TestConvertToRpcError_WrappedContextDeadlineExceeded(t *testing.T) {
	wrapped := fmt.Errorf("timed out: %w", context.DeadlineExceeded)
	s, _ := ConvertToRpcError(wrapped)
	if s.Code() != codes.DeadlineExceeded {
		t.Fatalf("expected DeadlineExceeded for wrapped context error, got %v", s.Code())
	}
}

func TestConvertToRpcError_StandardGrpcError_NoDetails(t *testing.T) {
	grpcErr := status.Error(codes.NotFound, "not found")
	s, details := ConvertToRpcError(grpcErr)

	if s.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", s.Code())
	}
	if s.Message() != "not found" {
		t.Fatalf("expected 'not found', got %q", s.Message())
	}
	if len(details.list()) != 0 {
		t.Fatalf("expected 0 details, got %d", len(details.list()))
	}
}

func TestConvertToRpcError_StandardGrpcError_WithDetails(t *testing.T) {
	// Build a gRPC error with details via NewRpcError, then round-trip it.
	original := NewRpcError(codes.InvalidArgument, "bad input", RpcErrorDetails{
		ErrorInfo: []*epb.ErrorInfo{{Reason: "FIELD_INVALID", Domain: "api.example.com"}},
		BadRequest: []*epb.BadRequest{
			{FieldViolations: []*epb.BadRequest_FieldViolation{
				{Field: "name", Description: "must not be empty"},
			}},
		},
	})

	s, details := ConvertToRpcError(original)

	if s.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", s.Code())
	}
	if s.Message() != "bad input" {
		t.Fatalf("expected 'bad input', got %q", s.Message())
	}

	if len(details.ErrorInfo) != 1 {
		t.Fatalf("expected 1 ErrorInfo, got %d", len(details.ErrorInfo))
	}
	if details.ErrorInfo[0].GetReason() != "FIELD_INVALID" {
		t.Errorf("expected reason FIELD_INVALID, got %q", details.ErrorInfo[0].GetReason())
	}
	if details.ErrorInfo[0].GetDomain() != "api.example.com" {
		t.Errorf("expected domain api.example.com, got %q", details.ErrorInfo[0].GetDomain())
	}

	if len(details.BadRequest) != 1 {
		t.Fatalf("expected 1 BadRequest, got %d", len(details.BadRequest))
	}
	fvs := details.BadRequest[0].GetFieldViolations()
	if len(fvs) != 1 || fvs[0].GetField() != "name" {
		t.Error("BadRequest field violation mismatch")
	}
}

func TestConvertToRpcError_AllDetailTypes_RoundTrip(t *testing.T) {
	// Create an error that populates as many detail types as possible,
	// then verify they survive the NewRpcError -> ConvertToRpcError round-trip.
	original := NewRpcError(codes.Internal, "full details", RpcErrorDetails{
		RequestInfo:       []*epb.RequestInfo{{RequestId: "r1", ServingData: "s1"}},
		LocalizedMessage:  []*epb.LocalizedMessage{{Locale: "en", Message: "localized"}},
		ResourceInfo:      []*epb.ResourceInfo{{ResourceType: "bucket", ResourceName: "my-bucket"}},
		RetryInfo:         []*epb.RetryInfo{{}},
		DebugInfo:         []*epb.DebugInfo{{Detail: "debug detail"}},
		ErrorInfo:         []*epb.ErrorInfo{{Reason: "QUOTA", Domain: "example.com"}},
		PreconditionFailure: []*epb.PreconditionFailure{
			{Violations: []*epb.PreconditionFailure_Violation{{Type: "TOS", Subject: "user"}}},
		},
		BadRequest: []*epb.BadRequest{
			{FieldViolations: []*epb.BadRequest_FieldViolation{{Field: "age", Description: "negative"}}},
		},
		QuotaFailure: []*epb.QuotaFailure{
			{Violations: []*epb.QuotaFailure_Violation{{Subject: "project", Description: "exceeded"}}},
		},
		Help: []*epb.Help{
			{Links: []*epb.Help_Link{{Description: "docs", Url: "https://example.com/docs"}}},
		},
	})

	s, details := ConvertToRpcError(original)

	if s.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %v", s.Code())
	}

	// Check each type was populated.
	checks := []struct {
		name  string
		count int
	}{
		{"RequestInfo", len(details.RequestInfo)},
		{"LocalizedMessage", len(details.LocalizedMessage)},
		{"ResourceInfo", len(details.ResourceInfo)},
		{"RetryInfo", len(details.RetryInfo)},
		{"DebugInfo", len(details.DebugInfo)},
		{"ErrorInfo", len(details.ErrorInfo)},
		{"PreconditionFailure", len(details.PreconditionFailure)},
		{"BadRequest", len(details.BadRequest)},
		{"QuotaFailure", len(details.QuotaFailure)},
		{"Help", len(details.Help)},
	}
	for _, c := range checks {
		if c.count != 1 {
			t.Errorf("expected 1 %s, got %d", c.name, c.count)
		}
	}

	// Verify some values survived the round-trip.
	if details.RequestInfo[0].GetRequestId() != "r1" {
		t.Error("RequestInfo.RequestId mismatch")
	}
	if details.ErrorInfo[0].GetReason() != "QUOTA" {
		t.Error("ErrorInfo.Reason mismatch")
	}
	if details.DebugInfo[0].GetDetail() != "debug detail" {
		t.Error("DebugInfo.Detail mismatch")
	}
}

func TestConvertToRpcError_NonGrpcError(t *testing.T) {
	// A plain Go error should be converted with status.Convert (codes.Unknown).
	plainErr := fmt.Errorf("some random error")
	s, details := ConvertToRpcError(plainErr)

	if s.Code() != codes.Unknown {
		t.Fatalf("expected Unknown for plain error, got %v", s.Code())
	}
	if len(details.list()) != 0 {
		t.Fatalf("expected 0 details for plain error, got %d", len(details.list()))
	}
}

func TestConvertToRpcError_MultipleDetailsOfSameType(t *testing.T) {
	original := NewRpcError(codes.Internal, "multi", RpcErrorDetails{
		ErrorInfo: []*epb.ErrorInfo{
			{Reason: "FIRST", Domain: "a.com"},
			{Reason: "SECOND", Domain: "b.com"},
		},
	})

	_, details := ConvertToRpcError(original)

	if len(details.ErrorInfo) != 2 {
		t.Fatalf("expected 2 ErrorInfo entries, got %d", len(details.ErrorInfo))
	}
	if details.ErrorInfo[0].GetReason() != "FIRST" {
		t.Errorf("first ErrorInfo reason mismatch: %q", details.ErrorInfo[0].GetReason())
	}
	if details.ErrorInfo[1].GetReason() != "SECOND" {
		t.Errorf("second ErrorInfo reason mismatch: %q", details.ErrorInfo[1].GetReason())
	}
}

// ---------------------------------------------------------------------------
// Typed-nil filtering fix (integration-level)
// ---------------------------------------------------------------------------

func TestTypedNilFix_NewRpcError_DoesNotProduceEmptyDetails(t *testing.T) {
	// If the typed-nil fix were broken, the typed nil would pass through
	// sliceToProto and anypb.New would produce an Any with empty value bytes,
	// resulting in an extra (meaningless) detail sent to the client.
	var typedNil *epb.ErrorInfo

	err := NewRpcError(codes.Internal, "typed nil test", RpcErrorDetails{
		ErrorInfo: []*epb.ErrorInfo{typedNil, {Reason: "real"}},
	})
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	s, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}

	if len(s.Details()) != 1 {
		t.Fatalf("typed-nil fix failed: expected 1 detail, got %d", len(s.Details()))
	}
	ei, ok := s.Details()[0].(*epb.ErrorInfo)
	if !ok {
		t.Fatal("expected detail to be *epb.ErrorInfo")
	}
	if ei.GetReason() != "real" {
		t.Errorf("expected reason 'real', got %q", ei.GetReason())
	}
}

func TestTypedNilFix_AllTypedNils_ProducesNoDetails(t *testing.T) {
	var (
		nilEI *epb.ErrorInfo
		nilDI *epb.DebugInfo
		nilRI *epb.RequestInfo
	)
	err := NewRpcError(codes.Internal, "all typed nils", RpcErrorDetails{
		ErrorInfo:   []*epb.ErrorInfo{nilEI},
		DebugInfo:   []*epb.DebugInfo{nilDI},
		RequestInfo: []*epb.RequestInfo{nilRI},
	})
	if err == nil {
		t.Fatal("expected non-nil error")
	}

	s, ok := status.FromError(err)
	if !ok {
		t.Fatal("expected gRPC status error")
	}
	// All details were typed nils, so the error should have no details attached.
	if len(s.Details()) != 0 {
		t.Fatalf("expected 0 details when all are typed nils, got %d", len(s.Details()))
	}
}
