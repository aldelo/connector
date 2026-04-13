package logger

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 */

// Tests for the structured logger interceptors. These pin the contract
// added in R11 (deep-review-2026-04-12):
//   - sensitive headers are redacted in the metadata field
//   - error message bodies are truncated at maxErrorLen
//   - nil *data.ZapLog is treated as a no-op (no panic, no logging)
//   - status code is correctly extracted from grpc/status errors
//   - non-status errors map to codes.Unknown
//   - metadata redaction does NOT mutate the caller's MD

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// -----------------------------------------------------------------------
// Code extraction
// -----------------------------------------------------------------------

func TestCodeOf_Nil(t *testing.T) {
	if got := codeOf(nil); got != codes.OK {
		t.Errorf("nil error -> want OK, got %v", got)
	}
}

func TestCodeOf_StatusError(t *testing.T) {
	err := status.Error(codes.PermissionDenied, "no")
	if got := codeOf(err); got != codes.PermissionDenied {
		t.Errorf("status err -> want PermissionDenied, got %v", got)
	}
}

func TestCodeOf_PlainError(t *testing.T) {
	err := errString("not a status error")
	if got := codeOf(err); got != codes.Unknown {
		t.Errorf("plain err -> want Unknown, got %v", got)
	}
}

// -----------------------------------------------------------------------
// Truncation (P3-4: bound error message length to prevent log bloat / leak)
// -----------------------------------------------------------------------

func TestTruncate_Short(t *testing.T) {
	if got := truncate("hello", maxErrorLen); got != "hello" {
		t.Errorf("short string should pass through, got %q", got)
	}
}

func TestTruncate_AtLimit(t *testing.T) {
	in := strings.Repeat("a", maxErrorLen)
	if got := truncate(in, maxErrorLen); got != in {
		t.Errorf("at-limit string should pass through unchanged")
	}
}

func TestTruncate_OverLimit(t *testing.T) {
	in := strings.Repeat("a", maxErrorLen+50)
	got := truncate(in, maxErrorLen)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected trailing ellipsis, got %q", got[len(got)-10:])
	}
	// Truncated body should be exactly maxErrorLen chars + "..."
	if len(got) != maxErrorLen+3 {
		t.Errorf("expected len %d, got %d", maxErrorLen+3, len(got))
	}
}

// -----------------------------------------------------------------------
// Metadata redaction (the PII discipline test)
// -----------------------------------------------------------------------

func TestRedactMetadata_BuiltinDenylist(t *testing.T) {
	md := metadata.New(map[string]string{
		"authorization": "Bearer s3cret",
		"cookie":        "session=abc",
		"x-api-key":     "k-12345",
		"x-trace-id":    "trace-xyz",
		"content-type":  "application/grpc",
	})

	out := redactMetadata(md, copyDenylist(defaultSensitiveHeaders))

	for _, secretKey := range []string{"authorization", "cookie", "x-api-key"} {
		if out[secretKey] != "[REDACTED]" {
			t.Errorf("expected %s to be redacted, got %q", secretKey, out[secretKey])
		}
	}
	if out["x-trace-id"] != "trace-xyz" {
		t.Errorf("non-sensitive header should pass through, got %q", out["x-trace-id"])
	}
	if out["content-type"] != "application/grpc" {
		t.Errorf("content-type should pass through, got %q", out["content-type"])
	}
}

func TestRedactMetadata_CaseInsensitive(t *testing.T) {
	// gRPC normalizes incoming MD keys to lowercase, but redactMetadata
	// must still defend against direct callers that pass mixed case.
	md := metadata.MD{
		"Authorization": []string{"Bearer s3cret"},
	}
	out := redactMetadata(md, copyDenylist(defaultSensitiveHeaders))
	if out["authorization"] != "[REDACTED]" {
		t.Errorf("mixed-case key should still redact, got %q", out["authorization"])
	}
}

func TestRedactMetadata_ExtensionViaOption(t *testing.T) {
	cfg := &loggerOptions{
		sensitiveHeaders: copyDenylist(defaultSensitiveHeaders),
	}
	WithSensitiveHeaders("X-Tenant-Secret")(cfg)

	md := metadata.New(map[string]string{
		"x-tenant-secret": "tenant-key",
	})
	out := redactMetadata(md, cfg.sensitiveHeaders)
	if out["x-tenant-secret"] != "[REDACTED]" {
		t.Errorf("extension header should redact, got %q", out["x-tenant-secret"])
	}
}

func TestRedactMetadata_DoesNotMutateInput(t *testing.T) {
	md := metadata.New(map[string]string{"authorization": "Bearer keep-me"})
	_ = redactMetadata(md, copyDenylist(defaultSensitiveHeaders))
	// Caller's MD must still hold the original value.
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer keep-me" {
		t.Errorf("input mutated: %v", got)
	}
}

func TestCopyDenylist_Isolation(t *testing.T) {
	// Mutating one extended denylist must not leak into the package default.
	a := copyDenylist(defaultSensitiveHeaders)
	a["new-header"] = struct{}{}
	if _, leaked := defaultSensitiveHeaders["new-header"]; leaked {
		t.Fatal("extension leaked into package default — pollution risk")
	}
}

// -----------------------------------------------------------------------
// Constructor + nil-logger no-op
// -----------------------------------------------------------------------

func TestNewLoggerInterceptors_NilLoggerIsNoop(t *testing.T) {
	// Passing nil *data.ZapLog must not panic; the interceptor should
	// still successfully serve the request.
	uIntr, sIntr := NewLoggerInterceptors(nil)
	if uIntr == nil || sIntr == nil {
		t.Fatal("expected non-nil interceptors even with nil logger")
	}

	called := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		called = true
		return "ok", nil
	}
	resp, err := uIntr(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x.Y/Z"}, handler)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp != "ok" || !called {
		t.Fatalf("handler not invoked (resp=%v called=%v)", resp, called)
	}
}

func TestNewLoggerInterceptors_PropagatesHandlerError(t *testing.T) {
	uIntr, _ := NewLoggerInterceptors(nil)
	wantErr := status.Error(codes.NotFound, "missing")

	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return nil, wantErr
	}
	_, gotErr := uIntr(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x.Y/Z"}, handler)
	if gotErr != wantErr {
		t.Errorf("expected handler error to propagate, got %v", gotErr)
	}
}

// -----------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------

type errString string

func (e errString) Error() string { return string(e) }
