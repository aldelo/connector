package logger

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

// Package logger provides gRPC server interceptors that emit structured,
// PII-aware access logs.
//
// Two integration paths are supported:
//
//  1. Legacy (unstructured): LoggerUnaryInterceptor and LoggerStreamInterceptor
//     are package-level functions kept for backward compatibility with any
//     downstream consumer that wired them up before R11. They write a single
//     line per RPC via the standard library log package. They never emit
//     metadata (no PII risk) and never include error message bodies beyond
//     the gRPC code, so they are safe-by-default but not very informative.
//
//  2. Structured (recommended): NewLoggerInterceptors returns a pair of
//     interceptors backed by the caller's *data.ZapLog. Method, peer, gRPC
//     code, duration, and an opt-in metadata snapshot are emitted as
//     structured key/value pairs. A built-in denylist redacts common secret
//     headers (authorization, cookie, x-api-key, ...); callers may extend
//     the denylist via WithSensitiveHeaders. Error message bodies are
//     truncated to maxErrorLen to bound log volume and prevent leakage of
//     long internal trace strings (P3-4 from the 2026-04-12 deep review).
//
// The structured constructor is intentionally dependency-light: it only
// uses the data.ZapLog wrapper that the rest of the connector already
// depends on, plus google.golang.org/grpc/status for code extraction.
//
// PII defaults (SP-010 pass-5, 2026-04-15):
//
//   - WithLogMetadata defaults to false — gRPC metadata is off unless
//     explicitly enabled, and even then the built-in denylist redacts
//     common secret headers.
//   - WithLogPeer defaults to false (A4-F1) — client IPs are personal
//     data under GDPR/CCPA/UK-DPA and must not ship enabled by default.
//     Set WithLogPeer(true) explicitly if your service requires peer
//     logging and has documented the legal basis.
//   - Error message bodies are truncated to maxErrorLen to bound log
//     volume and prevent leakage of long internal trace strings.
//
// Wire it in from a Service:
//
//	z := svc.ZLog() // or any *data.ZapLog the caller manages
//	uIntr, sIntr := logger.NewLoggerInterceptors(z,
//	    logger.WithSensitiveHeaders("x-tenant-secret"),
//	    logger.WithLogMetadata(true),
//	    logger.WithLogPeer(true), // opt-in if peer logging required
//	)
//	svc.UnaryServerInterceptors  = append(svc.UnaryServerInterceptors,  uIntr)
//	svc.StreamServerInterceptors = append(svc.StreamServerInterceptors, sIntr)

import (
	"context"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	data "github.com/aldelo/common/wrapper/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// maxErrorLen caps the length of error message strings copied into logs.
// Anything beyond this is replaced with an ellipsis. The cap is deliberately
// small (256 bytes) because long error strings frequently embed stack
// traces, SQL fragments, or vendor SDK payloads that may contain PII.
// Callers who need full error bodies should look at server-side logs
// scoped to the request_id, not the access log.
const maxErrorLen = 256

// defaultSensitiveHeaders is the built-in denylist applied to gRPC metadata
// when WithLogMetadata(true) is set. All comparisons are lowercase because
// gRPC metadata keys are normalized to lowercase by the framework.
//
// This list is intentionally NOT exported — callers should extend via
// WithSensitiveHeaders rather than mutating the package state.
var defaultSensitiveHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"x-api-key":           {},
	"x-auth-token":        {},
	"x-access-token":      {},
	"x-csrf-token":        {},
	"x-session-id":        {},
	"password":            {},
	"token":               {},
	"secret":              {},
}

// loggerOptions is the internal configuration assembled by Option funcs.
type loggerOptions struct {
	sensitiveHeaders map[string]struct{}
	logMetadata      bool
	logPeer          bool
}

// newDefaultLoggerOptions returns the default options applied when no
// Option functions are passed to NewLoggerInterceptors. Centralized in
// one place so that:
//
//  1. The constructor initializes exactly one definitive set of
//     defaults (no risk of drift between multiple initialization sites).
//  2. Tests can call this directly and assert the defaults without
//     having to reach into the unexported cfg built inside the
//     constructor's closure.
//
// SP-010 pass-5 A4-F1 (2026-04-15): logPeer defaults to false. See the
// WithLogPeer godoc for the full rationale.
func newDefaultLoggerOptions() *loggerOptions {
	return &loggerOptions{
		sensitiveHeaders: copyDenylist(defaultSensitiveHeaders),
		logMetadata:      false,
		logPeer:          false,
	}
}

// Option configures the structured logger interceptors returned by
// NewLoggerInterceptors. Options follow the standard functional-options
// idiom — pass any number; later options override earlier ones.
type Option func(*loggerOptions)

// WithSensitiveHeaders extends the metadata redaction denylist beyond the
// built-in set (authorization, cookie, x-api-key, ...). Header names are
// matched case-insensitively. Use this to redact tenant-specific or
// product-specific secrets that the default list does not know about.
func WithSensitiveHeaders(headers ...string) Option {
	return func(o *loggerOptions) {
		for _, h := range headers {
			o.sensitiveHeaders[strings.ToLower(h)] = struct{}{}
		}
	}
}

// WithLogMetadata controls whether incoming gRPC metadata is included as a
// structured field on each log entry. Defaults to false because metadata
// is a common PII source. When enabled, sensitive headers are still
// redacted via the denylist.
func WithLogMetadata(enabled bool) Option {
	return func(o *loggerOptions) { o.logMetadata = enabled }
}

// WithLogPeer controls whether the peer address (from grpc/peer) is
// included on each log entry.
//
// Default: false. SP-010 pass-5 A4-F1 (2026-04-15) flipped this from
// true → false because client IPs are personal data under GDPR, CCPA,
// and UK-DPA; the previous default silently pulled every deployment of
// this package into a data-protection amendment that nobody signed up
// for. A package named "PII-aware logger" should not require opt-out
// to be PII-safe.
//
// Set WithLogPeer(true) explicitly in environments where peer logging
// is required (typically internal-only backends, compliance-audit
// pipelines, or non-regulated B2B deployments). Document the decision
// in your service's data-retention policy when you do.
//
// Breaking change notice: services that previously relied on the
// implicit true default will need to add WithLogPeer(true) to their
// interceptor construction when upgrading past v1.8.2. Grep for
// NewLoggerInterceptors at upgrade time.
func WithLogPeer(enabled bool) Option {
	return func(o *loggerOptions) { o.logPeer = enabled }
}

// NewLoggerInterceptors returns a paired (unary, stream) gRPC server
// interceptor that logs each RPC's method, peer, status code, and
// duration to the supplied *data.ZapLog. Errors are logged at Errorw;
// successes at Infow.
//
// If z is nil, both interceptors degrade to no-ops (the request is still
// served — only logging is skipped). This matches the semantics of
// data.ZapLog.DisableLogger so that consumers can disable logging by
// passing a nil logger without changing their interceptor wiring.
//
// PII discipline: error messages are truncated to maxErrorLen bytes;
// metadata is only logged when WithLogMetadata(true) is set, and even
// then sensitive headers are redacted (value replaced with "[REDACTED]").
//
// This API is additive — the legacy LoggerUnaryInterceptor and
// LoggerStreamInterceptor functions remain available as package-level
// symbols for backward compatibility. New integrations should use this
// constructor.
func NewLoggerInterceptors(z *data.ZapLog, opts ...Option) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	cfg := newDefaultLoggerOptions()
	for _, opt := range opts {
		opt(cfg)
	}

	unary := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		emit(ctx, z, cfg, info.FullMethod, "unary", time.Since(start), err)
		return resp, err
	}

	stream := func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		emit(ss.Context(), z, cfg, info.FullMethod, "stream", time.Since(start), err)
		return err
	}

	return unary, stream
}

// emit assembles the structured fields and dispatches to the appropriate
// log level. Centralized so unary and stream paths cannot drift.
func emit(ctx context.Context, z *data.ZapLog, cfg *loggerOptions, method, kind string, dur time.Duration, err error) {
	if z == nil {
		return
	}

	code := codeOf(err)
	fields := make([]interface{}, 0, 12)
	fields = append(fields,
		"grpc.method", method,
		"grpc.kind", kind,
		"grpc.code", code.String(),
		"grpc.duration_ms", dur.Milliseconds(),
	)

	if cfg.logPeer {
		if p, ok := peer.FromContext(ctx); ok && p != nil && p.Addr != nil {
			fields = append(fields, "grpc.peer", p.Addr.String())
		}
	}

	if cfg.logMetadata {
		if md, ok := metadata.FromIncomingContext(ctx); ok && md != nil {
			fields = append(fields, "grpc.metadata", redactMetadata(md, cfg.sensitiveHeaders))
		}
	}

	if err != nil {
		fields = append(fields, "grpc.error", truncate(status.Convert(err).Message(), maxErrorLen))
		z.Errorw("grpc.request.error", fields...)
		return
	}
	z.Infow("grpc.request.ok", fields...)
}

// codeOf extracts the gRPC status code from an error. nil error -> OK.
// Non-status errors are reported as Unknown, matching grpc-go semantics.
func codeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	return status.Code(err)
}

// truncate returns s if it fits in max bytes, otherwise truncates s at the
// largest rune boundary <= max bytes and appends "..." as a continuation
// marker.
//
// MET-F2 fix: pre-fix this byte-sliced at exactly `max`, which lands
// mid-rune for any non-ASCII string (Japanese/emoji/accented chars), and
// Zap's JSON encoder then either rejects the field with InvalidUTF8Error
// or escapes the partial bytes as \ufffd — corrupting structured logs
// downstream. Walking forward via utf8.DecodeRuneInString ensures we
// always cut on a rune boundary, even when the input itself contains
// invalid bytes (DecodeRuneInString returns size=1 for an invalid byte,
// so the loop still makes forward progress).
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	n := 0
	for n < len(s) {
		_, size := utf8.DecodeRuneInString(s[n:])
		if n+size > max {
			break
		}
		n += size
	}
	return s[:n] + "..."
}

// redactMetadata returns a flat map[string]string copy of md with
// denylisted keys' values replaced by "[REDACTED]". Multi-value headers
// are joined with "," after redaction. The original md is not mutated.
func redactMetadata(md metadata.MD, deny map[string]struct{}) map[string]string {
	out := make(map[string]string, len(md))
	for k, vs := range md {
		lk := strings.ToLower(k)
		if _, isSecret := deny[lk]; isSecret {
			out[lk] = "[REDACTED]"
			continue
		}
		out[lk] = strings.Join(vs, ",")
	}
	return out
}

// copyDenylist clones the package-level default denylist so per-interceptor
// extensions via WithSensitiveHeaders do not bleed across interceptor
// instances.
func copyDenylist(src map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(src))
	for k := range src {
		out[k] = struct{}{}
	}
	return out
}

// LoggerUnaryInterceptor is the legacy unary interceptor preserved for
// backward compatibility. It logs method + duration + status to the
// standard library log package without structured fields and without
// any metadata extraction. New code should prefer NewLoggerInterceptors.
//
// This function never panics, never blocks, and never mutates the
// request/response. It is safe to wire into an interceptor chain
// regardless of context.
func LoggerUnaryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	duration := time.Since(start)
	if err != nil {
		// Use status code instead of raw error string to avoid leaking
		// internal error details (P3-4). Callers who need full error
		// context should switch to NewLoggerInterceptors.
		log.Printf("[gRPC] %s | %v | %s", info.FullMethod, duration, codeOf(err))
	} else {
		log.Printf("[gRPC] %s | %v | OK", info.FullMethod, duration)
	}
	return resp, err
}

// LoggerStreamInterceptor is the legacy stream interceptor preserved for
// backward compatibility. See LoggerUnaryInterceptor for the rationale —
// new code should prefer NewLoggerInterceptors.
func LoggerStreamInterceptor(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	start := time.Now()
	err := handler(srv, stream)
	duration := time.Since(start)
	if err != nil {
		log.Printf("[gRPC-Stream] %s | %v | %s", info.FullMethod, duration, codeOf(err))
	} else {
		log.Printf("[gRPC-Stream] %s | %v | OK", info.FullMethod, duration)
	}
	return err
}
