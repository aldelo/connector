package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// resetValidators clears both the guarded tokenValidator and the deprecated
// TokenValidator package var. Every test MUST call this via t.Cleanup to
// prevent cross-test pollution of shared state.
func resetValidators(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		tokenValidatorMu.Lock()
		tokenValidator = nil
		tokenValidatorMu.Unlock()
		TokenValidator = nil
	})
}

// alwaysValid is a validator that accepts every token.
func alwaysValid(_ string) bool { return true }

// alwaysInvalid is a validator that rejects every token.
func alwaysInvalid(_ string) bool { return false }

// tokenCapture returns a validator that records the token it received and
// returns the supplied result. Useful for asserting what token string was
// passed to the validator.
func tokenCapture(result bool) (validator func(string) bool, captured *string) {
	var s string
	captured = &s
	validator = func(token string) bool {
		*captured = token
		return result
	}
	return
}

// incomingCtx builds a context with gRPC incoming metadata containing the
// given authorization header value. Pass "" to omit the authorization key
// entirely. Pass a non-empty string to set authorization to that value.
func incomingCtx(authValue string) context.Context {
	if authValue == "" {
		// Metadata present but authorization key absent.
		md := metadata.New(map[string]string{"other-key": "other-value"})
		return metadata.NewIncomingContext(context.Background(), md)
	}
	md := metadata.New(map[string]string{"authorization": authValue})
	return metadata.NewIncomingContext(context.Background(), md)
}

// mockServerStream implements grpc.ServerStream with a fixed context.
type mockServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (m *mockServerStream) Context() context.Context {
	return m.ctx
}

// --- Tests for SetTokenValidator / getTokenValidator ---

func TestSetTokenValidator_SetsAndReturns(t *testing.T) {
	resetValidators(t)

	SetTokenValidator(alwaysValid)

	v := getTokenValidator()
	if v == nil {
		t.Fatal("expected non-nil validator after SetTokenValidator")
	}
	if !v("anything") {
		t.Error("expected validator to return true")
	}
}

func TestGetTokenValidator_PromotesDeprecatedVar(t *testing.T) {
	resetValidators(t)

	// Set the deprecated package-level variable directly.
	TokenValidator = alwaysValid

	// getTokenValidator should promote it.
	v := getTokenValidator()
	if v == nil {
		t.Fatal("expected getTokenValidator to promote deprecated TokenValidator")
	}
	if !v("anything") {
		t.Error("promoted validator should return true")
	}

	// After promotion, the guarded field should be set so subsequent calls
	// take the fast path without re-reading the deprecated var.
	TokenValidator = nil // Clear deprecated var.
	v2 := getTokenValidator()
	if v2 == nil {
		t.Fatal("expected guarded field to persist after promotion")
	}
}

func TestGetTokenValidator_NilWhenNothingConfigured(t *testing.T) {
	resetValidators(t)

	v := getTokenValidator()
	if v != nil {
		t.Fatal("expected nil when neither SetTokenValidator nor TokenValidator is set")
	}
}

// --- Tests for authenticate ---

func TestAuthenticate_NoMetadata(t *testing.T) {
	resetValidators(t)
	SetTokenValidator(alwaysValid)

	// A plain background context has no incoming metadata.
	err := authenticate(context.Background())
	assertGRPCCode(t, err, codes.InvalidArgument)
}

func TestAuthenticate_EmptyAuthorization(t *testing.T) {
	resetValidators(t)
	SetTokenValidator(alwaysValid)

	// Metadata present, but no "authorization" key.
	ctx := incomingCtx("")
	err := authenticate(ctx)
	assertGRPCCode(t, err, codes.Unauthenticated)
}

func TestAuthenticate_BearerPrefix_ValidToken(t *testing.T) {
	resetValidators(t)

	validator, captured := tokenCapture(true)
	SetTokenValidator(validator)

	ctx := incomingCtx("Bearer valid-token")
	err := authenticate(ctx)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if *captured != "valid-token" {
		t.Errorf("expected token 'valid-token', got %q", *captured)
	}
}

func TestAuthenticate_BearerPrefix_CaseInsensitive(t *testing.T) {
	resetValidators(t)

	validator, captured := tokenCapture(true)
	SetTokenValidator(validator)

	// lowercase "bearer " — RFC 7235 says scheme is case-insensitive.
	ctx := incomingCtx("bearer VALID")
	err := authenticate(ctx)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if *captured != "VALID" {
		t.Errorf("expected token 'VALID', got %q", *captured)
	}
}

func TestAuthenticate_NoBearerPrefix_PassesRawToken(t *testing.T) {
	resetValidators(t)

	validator, captured := tokenCapture(true)
	SetTokenValidator(validator)

	ctx := incomingCtx("raw-token-no-bearer")
	err := authenticate(ctx)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if *captured != "raw-token-no-bearer" {
		t.Errorf("expected raw token 'raw-token-no-bearer', got %q", *captured)
	}
}

func TestAuthenticate_ValidatorReturnsFalse(t *testing.T) {
	resetValidators(t)
	SetTokenValidator(alwaysInvalid)

	ctx := incomingCtx("Bearer some-token")
	err := authenticate(ctx)
	assertGRPCCode(t, err, codes.Unauthenticated)
}

func TestAuthenticate_NoValidatorConfigured(t *testing.T) {
	resetValidators(t)
	// Neither SetTokenValidator nor TokenValidator is set.

	ctx := incomingCtx("Bearer some-token")
	err := authenticate(ctx)
	assertGRPCCode(t, err, codes.Internal)
}

func TestAuthenticate_TableDriven(t *testing.T) {
	tests := []struct {
		name          string
		validator     func(string) bool
		authHeader    string
		noMetadata    bool
		wantCode      codes.Code
		wantNilErr    bool
		wantToken     string // expected token passed to validator ("" = don't check)
	}{
		{
			name:       "no metadata",
			validator:  alwaysValid,
			noMetadata: true,
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "empty authorization key",
			validator:  alwaysValid,
			authHeader: "",
			wantCode:   codes.Unauthenticated,
		},
		{
			name:       "Bearer uppercase valid",
			validator:  alwaysValid,
			authHeader: "Bearer my-token",
			wantNilErr: true,
			wantToken:  "my-token",
		},
		{
			name:       "bearer lowercase valid",
			validator:  alwaysValid,
			authHeader: "bearer my-token",
			wantNilErr: true,
			wantToken:  "my-token",
		},
		{
			name:       "BEARER uppercase valid",
			validator:  alwaysValid,
			authHeader: "BEARER my-token",
			wantNilErr: true,
			wantToken:  "my-token",
		},
		{
			name:       "BeArEr mixed case valid",
			validator:  alwaysValid,
			authHeader: "BeArEr my-token",
			wantNilErr: true,
			wantToken:  "my-token",
		},
		{
			name:       "raw token no prefix",
			validator:  alwaysValid,
			authHeader: "abc123",
			wantNilErr: true,
			wantToken:  "abc123",
		},
		{
			name:       "short value under 7 chars",
			validator:  alwaysValid,
			authHeader: "Bear",
			wantNilErr: true,
			wantToken:  "Bear",
		},
		{
			name:       "exactly 7 chars not bearer prefix",
			validator:  alwaysValid,
			authHeader: "Bearerx",
			wantNilErr: true,
			wantToken:  "Bearerx",
		},
		{
			name:       "validator rejects token",
			validator:  alwaysInvalid,
			authHeader: "Bearer bad-token",
			wantCode:   codes.Unauthenticated,
		},
		{
			name:       "no validator configured",
			validator:  nil,
			authHeader: "Bearer some-token",
			wantCode:   codes.Internal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetValidators(t)

			var captured string
			if tt.validator != nil {
				wrapped := tt.validator
				SetTokenValidator(func(token string) bool {
					captured = token
					return wrapped(token)
				})
			}
			// When tt.validator is nil, we intentionally leave no validator set.

			var ctx context.Context
			if tt.noMetadata {
				ctx = context.Background()
			} else {
				ctx = incomingCtx(tt.authHeader)
			}

			err := authenticate(ctx)

			if tt.wantNilErr {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				if tt.wantToken != "" && captured != tt.wantToken {
					t.Errorf("expected token %q passed to validator, got %q", tt.wantToken, captured)
				}
			} else {
				assertGRPCCode(t, err, tt.wantCode)
			}
		})
	}
}

// --- Tests for ServerAuthUnaryInterceptor ---

func TestServerAuthUnaryInterceptor_ValidToken_HandlerCalled(t *testing.T) {
	resetValidators(t)
	SetTokenValidator(alwaysValid)

	ctx := incomingCtx("Bearer test-token")

	handlerCalled := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		handlerCalled = true
		return "ok", nil
	}

	resp, err := ServerAuthUnaryInterceptor(ctx, "request", nil, handler)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !handlerCalled {
		t.Error("expected handler to be called")
	}
	if resp != "ok" {
		t.Errorf("expected response 'ok', got %v", resp)
	}
}

func TestServerAuthUnaryInterceptor_InvalidToken_HandlerNotCalled(t *testing.T) {
	resetValidators(t)
	SetTokenValidator(alwaysInvalid)

	ctx := incomingCtx("Bearer bad-token")

	handlerCalled := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		handlerCalled = true
		return "ok", nil
	}

	resp, err := ServerAuthUnaryInterceptor(ctx, "request", nil, handler)
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	if handlerCalled {
		t.Error("handler should NOT be called when auth fails")
	}
	if resp != nil {
		t.Errorf("expected nil response, got %v", resp)
	}
	assertGRPCCode(t, err, codes.Unauthenticated)
}

func TestServerAuthUnaryInterceptor_NoMetadata(t *testing.T) {
	resetValidators(t)
	SetTokenValidator(alwaysValid)

	handlerCalled := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		handlerCalled = true
		return "ok", nil
	}

	_, err := ServerAuthUnaryInterceptor(context.Background(), "request", nil, handler)
	if err == nil {
		t.Fatal("expected error for missing metadata")
	}
	if handlerCalled {
		t.Error("handler should NOT be called when metadata is missing")
	}
	assertGRPCCode(t, err, codes.InvalidArgument)
}

// --- Tests for ServerAuthStreamInterceptor ---

func TestServerAuthStreamInterceptor_ValidToken_HandlerCalled(t *testing.T) {
	resetValidators(t)
	SetTokenValidator(alwaysValid)

	ctx := incomingCtx("Bearer test-token")
	stream := &mockServerStream{ctx: ctx}

	handlerCalled := false
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		handlerCalled = true
		return nil
	}

	err := ServerAuthStreamInterceptor("server", stream, nil, handler)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !handlerCalled {
		t.Error("expected stream handler to be called")
	}
}

func TestServerAuthStreamInterceptor_InvalidToken_HandlerNotCalled(t *testing.T) {
	resetValidators(t)
	SetTokenValidator(alwaysInvalid)

	ctx := incomingCtx("Bearer bad-token")
	stream := &mockServerStream{ctx: ctx}

	handlerCalled := false
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		handlerCalled = true
		return nil
	}

	err := ServerAuthStreamInterceptor("server", stream, nil, handler)
	if err == nil {
		t.Fatal("expected error for invalid token")
	}
	if handlerCalled {
		t.Error("stream handler should NOT be called when auth fails")
	}
	assertGRPCCode(t, err, codes.Unauthenticated)
}

func TestServerAuthStreamInterceptor_NoMetadata(t *testing.T) {
	resetValidators(t)
	SetTokenValidator(alwaysValid)

	stream := &mockServerStream{ctx: context.Background()}

	handlerCalled := false
	handler := func(srv interface{}, stream grpc.ServerStream) error {
		handlerCalled = true
		return nil
	}

	err := ServerAuthStreamInterceptor("server", stream, nil, handler)
	if err == nil {
		t.Fatal("expected error for missing metadata")
	}
	if handlerCalled {
		t.Error("stream handler should NOT be called when metadata is missing")
	}
	assertGRPCCode(t, err, codes.InvalidArgument)
}

// --- Concurrency test ---

func TestConcurrentSetAndAuthenticate(t *testing.T) {
	resetValidators(t)

	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup
	var validCount atomic.Int64
	var errorCount atomic.Int64

	ctx := incomingCtx("Bearer concurrent-token")

	// Half the goroutines set validators, the other half authenticate.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		if i%2 == 0 {
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					SetTokenValidator(func(token string) bool {
						return token == "concurrent-token"
					})
				}
			}()
		} else {
			go func() {
				defer wg.Done()
				for j := 0; j < iterations; j++ {
					err := authenticate(ctx)
					if err == nil {
						validCount.Add(1)
					} else {
						errorCount.Add(1)
					}
				}
			}()
		}
	}

	wg.Wait()

	t.Logf("concurrent results: valid=%d, error=%d", validCount.Load(), errorCount.Load())

	// We primarily care that there are no data races (detected by -race).
	// Some early authenticate calls may hit codes.Internal (no validator yet)
	// which is acceptable — the point is no panics or corrupted state.
	total := validCount.Load() + errorCount.Load()
	expected := int64(goroutines/2) * iterations
	if total != expected {
		t.Errorf("expected %d total results, got %d", expected, total)
	}
}

// --- Helper ---

func assertGRPCCode(t *testing.T, err error, wantCode codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected gRPC error with code %v, got nil", wantCode)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != wantCode {
		t.Errorf("expected code %v, got %v (message: %s)", wantCode, st.Code(), st.Message())
	}
}
