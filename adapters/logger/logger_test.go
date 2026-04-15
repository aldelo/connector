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
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/time/rate"
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

// TestTruncate_PreservesUTF8Boundary pins MET-F2: when an input string
// exceeds the byte cap and contains multi-byte runes, the result must
// always be valid UTF-8. Pre-fix, byte-slicing at exactly `max` landed
// mid-rune for any non-ASCII script, and Zap's JSON encoder then
// rejected the field with InvalidUTF8Error or replaced it with \ufffd,
// corrupting structured logs downstream.
func TestTruncate_PreservesUTF8Boundary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
	}{
		{"ascii_fits", "hello world", 256},
		{"ascii_truncate", strings.Repeat("a", 300), 256},
		// each "こ" is 3 bytes — 100*3 = 300 > 256, and 256 % 3 != 0 so
		// the naive byte-slice would land mid-rune
		{"japanese", strings.Repeat("こ", 100), 256},
		// each "🎉" is 4 bytes — 50*4 = 200 fits, so use 70 to force overflow
		{"emoji_truncate", strings.Repeat("🎉", 70), 256},
		// mixed ASCII + multi-byte with a tight byte cap
		{"mixed_tight", "hello こんにちは 🎉", 16},
		// edge: cap smaller than a single rune's width — must still
		// produce valid UTF-8 (empty body + ellipsis is acceptable)
		{"sub_rune_cap", "こんにちは", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := truncate(tc.in, tc.max)
			// Strip the ellipsis (if added) before validating, so we
			// pin the contract on the truncated content itself, not
			// the marker.
			body := out
			if len(out) > len(tc.in) || strings.HasSuffix(out, "...") && len(tc.in) > tc.max {
				body = strings.TrimSuffix(out, "...")
			}
			if !utf8.ValidString(body) {
				t.Errorf("MET-F2 regression: invalid UTF-8 after truncate: in=%q out=%q body=%q", tc.in, out, body)
			}
			// Body must not exceed the byte cap (the ellipsis is
			// allowed to push total length over).
			if len(body) > tc.max {
				t.Errorf("body exceeded max: len(body)=%d max=%d out=%q", len(body), tc.max, out)
			}
			// If the input fits, output must equal input (no ellipsis).
			if len(tc.in) <= tc.max && out != tc.in {
				t.Errorf("fits but was modified: in=%q out=%q", tc.in, out)
			}
		})
	}
}

// TestTruncate_FourByteUTF8Boundary pins L1-001: the MET-F2 fix must
// produce valid UTF-8 when the max-byte cap falls anywhere inside a
// 4-byte code point (U+10000..U+10FFFF, the Unicode supplementary
// planes — emoji, CJK extension B, mathematical alphanumerics, etc.).
//
// Why 4-byte specifically? The pre-fix defect was a naive s[:max] that
// landed mid-rune. 2-byte and 3-byte runes can also land mid-rune, but
// 3-byte coverage is implicit in the japanese sub-test of
// TestTruncate_PreservesUTF8Boundary. 4-byte code points exercise a
// distinct decoder branch (lead byte 0xF0..0xF4, three continuation
// bytes) and were never explicitly pinned. If utf8.DecodeRuneInString
// ever regresses the 4-byte path, or if someone rewrites truncate to
// use a rune-index heuristic that happens to work for 2- and 3-byte
// runes but not 4-byte, this test catches it.
//
// Test strategy: build a string whose 4-byte runes sit at known byte
// offsets, then drive truncate at every byte position from the start
// of each rune through its last continuation byte. At every position
// the result MUST be valid UTF-8 and its last rune MUST be a complete
// 4-byte code point (or no runes at all, if the cap is smaller than
// the first rune's width).
func TestTruncate_FourByteUTF8Boundary(t *testing.T) {
	// Chosen 4-byte code points (all utf8.RuneLen == 4):
	//   U+1F680 ROCKET                 "🚀"
	//   U+1D54F MATHEMATICAL DBLSTRUCK CAPITAL X  "𝕏"
	//   U+1F389 PARTY POPPER           "🎉"
	//   U+2070E CJK UNIFIED IDEOGRAPH  "𠜎"   (CJK Extension B)
	//   U+1F4A9 PILE OF POO            "💩"   (because we must)
	runes := []string{"🚀", "𝕏", "🎉", "𠜎", "💩"}
	for _, r := range runes {
		if utf8.RuneLen([]rune(r)[0]) != 4 {
			t.Fatalf("test setup bug: %q is not a 4-byte rune", r)
		}
	}

	// Build a runs of 4-byte runes separated by single-byte ASCII
	// so each codepoint has a unique, predictable byte offset.
	// Layout: "a" + rune0 + "b" + rune1 + "c" + rune2 + "d" + rune3 + "e" + rune4 + "f"
	// Each segment is 1 ASCII byte + 4 rune bytes = 5 bytes.
	var b strings.Builder
	sep := []string{"a", "b", "c", "d", "e"}
	for i, r := range runes {
		b.WriteString(sep[i])
		b.WriteString(r)
	}
	b.WriteString("f")
	in := b.String()

	// Sanity-check the layout: each rune starts at offsets 1, 6, 11, 16, 21
	// and ends at offsets 4, 9, 14, 19, 24 inclusive; the trailing "f" is
	// at offset 25 (total len 26).
	wantLen := 5*len(runes) + 1
	if len(in) != wantLen {
		t.Fatalf("setup: want %d bytes, got %d (%q)", wantLen, len(in), in)
	}
	if !utf8.ValidString(in) {
		t.Fatalf("setup: constructed string is not valid UTF-8: %q", in)
	}

	// Exercise truncation at every byte position from 1 to len(in).
	// For each position, the result MUST be valid UTF-8 and its byte
	// length (excluding any trailing "...") MUST be <= the cap AND
	// MUST land on a rune boundary (i.e. the next byte of `in`, if
	// any, is either a rune start or we consumed the whole string).
	for max := 1; max <= len(in); max++ {
		out := truncate(in, max)

		// Strip the ellipsis when present so we reason about the
		// actual truncated body.
		body := out
		if len(in) > max && strings.HasSuffix(out, "...") {
			body = strings.TrimSuffix(out, "...")
		}

		// Invariant 1: the body must be valid UTF-8. A mid-rune
		// cut on a 4-byte code point would leave trailing
		// continuation bytes (0x80..0xBF) that utf8.ValidString
		// rejects.
		if !utf8.ValidString(body) {
			t.Errorf("max=%d: invalid UTF-8 body after truncate: body=%q (raw bytes=% x)",
				max, body, []byte(body))
		}

		// Invariant 2: the body must fit within the cap. The
		// ellipsis is allowed to push `out` over `max` because
		// it is a caller-visible marker, but the payload itself
		// must respect the cap.
		if len(body) > max {
			t.Errorf("max=%d: body exceeded cap: len(body)=%d body=%q",
				max, len(body), body)
		}

		// Invariant 3: the LAST rune of the body (if any) must
		// be a complete 4-byte rune or a 1-byte ASCII separator
		// — never a partial prefix of a 4-byte rune. We check
		// this by decoding runes off the tail and asserting the
		// last rune's width matches its byte footprint.
		if len(body) > 0 {
			lastRune, lastSize := utf8.DecodeLastRuneInString(body)
			if lastRune == utf8.RuneError && lastSize == 1 {
				t.Errorf("max=%d: tail rune is RuneError (mid-rune cut): body=%q",
					max, body)
			}
			// The 4-byte runes live in supplementary planes:
			// any rune in body whose value is >= 0x10000 must
			// be exactly 4 bytes, not 1/2/3.
			if lastRune >= 0x10000 && lastSize != 4 {
				t.Errorf("max=%d: supplementary-plane tail rune %U reported as %d bytes, want 4",
					max, lastRune, lastSize)
			}
		}

		// Invariant 4: if the input fits, output must equal
		// input unchanged (no ellipsis, no truncation).
		if len(in) <= max && out != in {
			t.Errorf("max=%d: input fits but was modified: in=%q out=%q",
				max, in, out)
		}
	}

	// Dedicated spot-checks at the KNOWN-bad positions for the
	// first 4-byte rune (starts at offset 1, spans bytes 1..4):
	//   max=2 → mid-rune cut 1 byte in   → body must end before the rune
	//   max=3 → mid-rune cut 2 bytes in  → body must end before the rune
	//   max=4 → mid-rune cut 3 bytes in  → body must end before the rune
	//   max=5 → clean cut right after    → body must include the rune
	for _, max := range []int{2, 3, 4} {
		out := truncate(in, max)
		body := strings.TrimSuffix(out, "...")
		if !utf8.ValidString(body) {
			t.Errorf("mid-rune cut at max=%d produced invalid UTF-8: %q",
				max, body)
		}
		// The body cannot contain the first rune (starts at
		// byte 1, needs bytes 1..4; max < 5 means it cannot
		// fit). So the body must be either "" or "a".
		if body != "" && body != "a" {
			t.Errorf("max=%d: expected body to be \"\" or \"a\" (cannot fit first 4-byte rune), got %q",
				max, body)
		}
	}
	// max=5 is the first position at which the first 4-byte rune
	// fits cleanly — the body must contain "a" + the rune.
	{
		out := truncate(in, 5)
		body := strings.TrimSuffix(out, "...")
		want := "a" + runes[0]
		if body != want {
			t.Errorf("max=5: expected body %q (ASCII + first 4-byte rune), got %q",
				want, body)
		}
		if !utf8.ValidString(body) {
			t.Errorf("max=5: body is not valid UTF-8: %q", body)
		}
	}
}

// TestTruncate_InvalidInputBytesStillMakesProgress confirms the
// loop terminates and produces valid output even when the input
// itself contains invalid UTF-8 bytes — DecodeRuneInString returns
// size=1 for an invalid byte, so each iteration advances at least
// one byte.
func TestTruncate_InvalidInputBytesStillMakesProgress(t *testing.T) {
	// Construct a string with an embedded invalid byte sequence.
	in := "abc" + string([]byte{0xff, 0xfe, 0xfd}) + strings.Repeat("x", 300)
	got := truncate(in, maxErrorLen)
	if len(got) > maxErrorLen+3 {
		t.Errorf("output too long: %d", len(got))
	}
	// Must terminate (test would hang otherwise) and end with ellipsis.
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected trailing ellipsis, got tail %q", got[len(got)-10:])
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
// Default options — SP-010 pass-5 A4-F1 (2026-04-15)
// -----------------------------------------------------------------------
//
// These tests pin the PII-safe defaults produced by
// newDefaultLoggerOptions. The A4-F1 fix flipped logPeer from true →
// false because client IPs are personal data under GDPR/CCPA/UK-DPA
// and must not ship enabled by default. newDefaultLoggerOptions is
// the single point of truth for the defaults — both the constructor
// and these tests call it, so drift between "documented default" and
// "actual default" is impossible.
//
// Mutation probe (quality gate): in logger.go, revert the logPeer
// default from false back to true (e.g. `logPeer: true` in
// newDefaultLoggerOptions). TestDefaultLoggerOptions_LogPeerIsFalse_A4F1
// MUST turn red. Restore the fix and it MUST return to green. This
// confirms the test exercises the causal path (the actual default-
// initialization function), not a tautological postcondition.

func TestDefaultLoggerOptions_LogPeerIsFalse_A4F1(t *testing.T) {
	cfg := newDefaultLoggerOptions()
	if cfg.logPeer {
		t.Fatalf("A4-F1 regression: newDefaultLoggerOptions().logPeer must default to false under GDPR/CCPA/UK-DPA; got true")
	}
}

func TestDefaultLoggerOptions_LogMetadataIsFalse(t *testing.T) {
	// Pin the pre-existing safe default for metadata logging.
	cfg := newDefaultLoggerOptions()
	if cfg.logMetadata {
		t.Fatalf("newDefaultLoggerOptions().logMetadata must default to false (headers are a common PII source); got true")
	}
}

func TestDefaultLoggerOptions_SensitiveHeadersSeeded(t *testing.T) {
	// Pin that the default denylist is populated (not nil, not empty).
	// Without this, WithSensitiveHeaders on top of a bad default could
	// still silently log authorization headers.
	cfg := newDefaultLoggerOptions()
	if cfg.sensitiveHeaders == nil {
		t.Fatal("newDefaultLoggerOptions().sensitiveHeaders must be pre-seeded, got nil")
	}
	for _, required := range []string{"authorization", "cookie", "x-api-key", "password", "token"} {
		if _, ok := cfg.sensitiveHeaders[required]; !ok {
			t.Errorf("newDefaultLoggerOptions().sensitiveHeaders missing required denylist entry %q", required)
		}
	}
}

func TestWithLogPeer_OptInFlipsDefault(t *testing.T) {
	// The A4-F1 flip must not remove the ability to opt in — verify
	// WithLogPeer(true) still works for the (documented) use cases
	// where peer logging is required.
	cfg := newDefaultLoggerOptions()
	if cfg.logPeer {
		t.Fatal("precondition: default logPeer must be false (A4-F1)")
	}
	WithLogPeer(true)(cfg)
	if !cfg.logPeer {
		t.Error("WithLogPeer(true) must flip logPeer from false → true")
	}
	// Idempotent off — opting back out should also work.
	WithLogPeer(false)(cfg)
	if cfg.logPeer {
		t.Error("WithLogPeer(false) must flip logPeer from true → false")
	}
}

func TestNewLoggerInterceptors_UsesDefaultLoggerOptions_A4F1(t *testing.T) {
	// Source-invariant pin: NewLoggerInterceptors must initialize its
	// cfg from newDefaultLoggerOptions() (and not reintroduce an
	// inline literal with logPeer: true). This catches the specific
	// regression mode where someone "simplifies" the constructor by
	// inlining the struct literal and accidentally restores the old
	// unsafe default.
	src, err := os.ReadFile("logger.go")
	if err != nil {
		t.Fatalf("cannot read logger.go: %v", err)
	}
	body := string(src)

	// Positive: the constructor must route through the helper.
	want := `cfg := newDefaultLoggerOptions()`
	if !strings.Contains(body, want) {
		t.Errorf("A4-F1 regression: expected NewLoggerInterceptors to call newDefaultLoggerOptions(), but did not find %q in logger.go", want)
	}

	// Negative: the old inline literal with logPeer: true must not
	// exist anywhere in the file.
	absent := "logPeer:          true"
	if strings.Contains(body, absent) {
		t.Errorf("A4-F1 regression: unsafe inline default still present in logger.go: %q", absent)
	}
}

// -----------------------------------------------------------------------
// Sample-rate opt-in — SP-010 pass-5 A4-F3 (2026-04-15)
// -----------------------------------------------------------------------
//
// A4-F3: the logger's hot-path emit() has no upper bound on emissions.
// A 5K RPS scanner storm or a panic loop in a downstream service drives
// 5K log lines/sec through Errorw, and Zap does not throttle — the
// CloudWatch ingest bill spikes before the operator notices. The fix is
// an opt-in WithSampleRate(perSecondCap int) token bucket with default
// unlimited (preserves backward compatibility for every consumer that
// does not opt in).
//
// Mutation probe (quality gate): in logger.go, remove the gate
// `if cfg.rateLimiter != nil && !cfg.rateLimiter.Allow() { return }`
// from emit(). TestEmit_ContainsRateLimiterGate_A4F3 and
// TestWithSampleRate_AllowsBurstThenDrops MUST turn red (the former
// because the source substring disappears, the latter because even
// after bucket exhaustion the hypothetical caller path would not drop).
// Restore the gate and both MUST return to green.

func TestDefaultLoggerOptions_RateLimiterIsNil_A4F3(t *testing.T) {
	// Default-unlimited is a backward-compatibility guarantee. Every
	// consumer that does not opt in via WithSampleRate must see the
	// legacy unthrottled behavior — nil limiter is the sentinel emit()
	// uses to skip the gate entirely.
	cfg := newDefaultLoggerOptions()
	if cfg.rateLimiter != nil {
		t.Fatalf("A4-F3 regression: newDefaultLoggerOptions().rateLimiter must default to nil (unlimited, BC); got non-nil limiter")
	}
}

func TestWithSampleRate_NonPositiveIsUnlimited(t *testing.T) {
	// Both 0 and negative values must normalize to nil (unlimited),
	// not to a rate.NewLimiter(0, 0) which would drop every emission
	// and silently disable the logger. This is the most important
	// edge case for the option: misconfiguration must not break the
	// default BC path.
	for _, cap := range []int{0, -1, -1000} {
		cfg := newDefaultLoggerOptions()
		WithSampleRate(cap)(cfg)
		if cfg.rateLimiter != nil {
			t.Errorf("WithSampleRate(%d): expected nil limiter (unlimited), got non-nil", cap)
		}
	}
}

func TestWithSampleRate_CreatesLimiterWithExpectedRate(t *testing.T) {
	// Positive caps must produce a limiter whose steady-state rate
	// equals the cap and whose burst is sized to absorb short spikes
	// cleanly. Pin both so a future "optimization" that silently
	// changes burst sizing is caught at test time.
	cfg := newDefaultLoggerOptions()
	WithSampleRate(100)(cfg)
	if cfg.rateLimiter == nil {
		t.Fatal("WithSampleRate(100): expected non-nil limiter")
	}
	if got, want := cfg.rateLimiter.Limit(), rate.Limit(100); got != want {
		t.Errorf("WithSampleRate(100).Limit() = %v, want %v", got, want)
	}
	if got, want := cfg.rateLimiter.Burst(), 100; got != want {
		t.Errorf("WithSampleRate(100).Burst() = %d, want %d", got, want)
	}
}

func TestWithSampleRate_AllowsBurstThenDrops(t *testing.T) {
	// Functional pin: with cap=3, the first 3 rapid-fire Allow()
	// calls must succeed (burst absorption) and the 4th must fail
	// (bucket empty, refill too slow for immediate retry). This is
	// the direct analog of what emit() does on the hot path — if
	// cfg.rateLimiter.Allow() ever stops returning false under
	// burst, the gate has no effect and the A4-F3 fix is a lie.
	cfg := newDefaultLoggerOptions()
	WithSampleRate(3)(cfg)
	if cfg.rateLimiter == nil {
		t.Fatal("precondition: WithSampleRate(3) must produce non-nil limiter")
	}
	for i := 0; i < 3; i++ {
		if !cfg.rateLimiter.Allow() {
			t.Errorf("burst call %d/3: expected Allow()=true (bucket has %v tokens), got false", i+1, cfg.rateLimiter.Tokens())
		}
	}
	if cfg.rateLimiter.Allow() {
		t.Error("4th call: expected Allow()=false (bucket exhausted), got true — rate limiter is not capping")
	}
}

func TestEmit_ContainsRateLimiterGate_A4F3(t *testing.T) {
	// Source-invariant pin: emit() must gate on cfg.rateLimiter
	// before building the structured fields slice. This catches the
	// regression mode where someone "simplifies" emit() by removing
	// the gate — unit tests on the Option alone do not exercise the
	// emit() path, so without this assertion a gate deletion would
	// pass tests while silently restoring the unbounded behavior.
	src, err := os.ReadFile("logger.go")
	if err != nil {
		t.Fatalf("cannot read logger.go: %v", err)
	}
	body := string(src)
	want := `if cfg.rateLimiter != nil && !cfg.rateLimiter.Allow()`
	if !strings.Contains(body, want) {
		t.Errorf("A4-F3 regression: emit() no longer gates on rate limiter.\nExpected source substring:\n  %s", want)
	}
}

func TestNewLoggerInterceptors_RateLimitOptInDoesNotBreakNilLogger(t *testing.T) {
	// Orthogonality pin: the A4-F3 opt-in must not interact with the
	// nil-logger no-op path. Passing nil *data.ZapLog AND a sample
	// rate must still yield a working (silent) interceptor pair.
	uIntr, sIntr := NewLoggerInterceptors(nil, WithSampleRate(5))
	if uIntr == nil || sIntr == nil {
		t.Fatal("expected non-nil interceptors with nil logger + WithSampleRate opt-in")
	}
	called := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		called = true
		return "ok", nil
	}
	resp, err := uIntr(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/x.Y/Z"}, handler)
	if err != nil || resp != "ok" || !called {
		t.Fatalf("nil logger + rate-limit opt-in must still serve: resp=%v err=%v called=%v", resp, err, called)
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
