package tracer

import (
	"strings"
	"testing"

	"google.golang.org/grpc/metadata"
)

// ---------------------------------------------------------------------------
// sanitizeSegmentName
// ---------------------------------------------------------------------------

func TestSanitizeSegmentName_NormalInput(t *testing.T) {
	got := sanitizeSegmentName("GrpcClient-UnaryRPC-", "/my.service/GetItem")
	want := "GrpcClient-UnaryRPC-my.service-GetItem"
	if got != want {
		t.Errorf("sanitizeSegmentName normal: got %q, want %q", got, want)
	}
}

func TestSanitizeSegmentName_EmptyMethod(t *testing.T) {
	got := sanitizeSegmentName("Prefix-", "")
	want := "Prefix-unknown"
	if got != want {
		t.Errorf("sanitizeSegmentName empty method: got %q, want %q", got, want)
	}
}

func TestSanitizeSegmentName_SpecialCharacters(t *testing.T) {
	got := sanitizeSegmentName("Seg-", "hello!@#world")
	want := "Seg-hello---world"
	if got != want {
		t.Errorf("sanitizeSegmentName special chars: got %q, want %q", got, want)
	}
}

func TestSanitizeSegmentName_LongName(t *testing.T) {
	method := strings.Repeat("a", 250)
	got := sanitizeSegmentName("P-", method)
	if len(got) != 200 {
		t.Errorf("sanitizeSegmentName long name: length = %d, want 200", len(got))
	}
	if got != "P-"+strings.Repeat("a", 198) {
		t.Errorf("sanitizeSegmentName long name: unexpected content")
	}
}

func TestSanitizeSegmentName_AllSpecialCharMethod(t *testing.T) {
	// All characters are special, so after mapping they become '-', then Trim("-") yields "".
	// The function should fall back to "unknown".
	got := sanitizeSegmentName("X-", "!@#$%^&*()")
	want := "X-unknown"
	if got != want {
		t.Errorf("sanitizeSegmentName all special: got %q, want %q", got, want)
	}
}

func TestSanitizeSegmentName_LeadingTrailingDashes(t *testing.T) {
	// Method that produces leading/trailing dashes after mapping should have them trimmed.
	got := sanitizeSegmentName("", "---abc---")
	want := "abc"
	if got != want {
		t.Errorf("sanitizeSegmentName leading/trailing dashes: got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// extractParentIDs
// ---------------------------------------------------------------------------

func boolPtr(b bool) *bool { return &b }

func TestExtractParentIDs_FullTraceHeader(t *testing.T) {
	md := metadata.New(map[string]string{
		"x-amzn-trace-id": "Root=1-abc-def;Parent=seg123;Sampled=1",
	})

	segID, traceID, sampled, rawHeader := extractParentIDs(md)

	if traceID != "1-abc-def" {
		t.Errorf("traceID: got %q, want %q", traceID, "1-abc-def")
	}
	if segID != "seg123" {
		t.Errorf("segID: got %q, want %q", segID, "seg123")
	}
	if sampled == nil || *sampled != true {
		t.Errorf("sampled: got %v, want true", sampled)
	}
	if rawHeader != "Root=1-abc-def;Parent=seg123;Sampled=1" {
		t.Errorf("rawHeader: got %q", rawHeader)
	}
}

func TestExtractParentIDs_MissingParts(t *testing.T) {
	// Only Root, no Parent or Sampled
	md := metadata.New(map[string]string{
		"x-amzn-trace-id": "Root=1-trace-only",
	})

	segID, traceID, sampled, _ := extractParentIDs(md)

	if traceID != "1-trace-only" {
		t.Errorf("traceID: got %q, want %q", traceID, "1-trace-only")
	}
	if segID != "" {
		t.Errorf("segID: got %q, want empty", segID)
	}
	if sampled != nil {
		t.Errorf("sampled: got %v, want nil", sampled)
	}
}

func TestExtractParentIDs_Sampled1(t *testing.T) {
	md := metadata.New(map[string]string{
		"x-amzn-trace-id": "Root=1-r;Sampled=1",
	})

	_, _, sampled, _ := extractParentIDs(md)
	if sampled == nil || *sampled != true {
		t.Errorf("Sampled=1: got %v, want true", sampled)
	}
}

func TestExtractParentIDs_Sampled0(t *testing.T) {
	md := metadata.New(map[string]string{
		"x-amzn-trace-id": "Root=1-r;Sampled=0",
	})

	_, _, sampled, _ := extractParentIDs(md)
	if sampled == nil || *sampled != false {
		t.Errorf("Sampled=0: got %v, want false", sampled)
	}
}

func TestExtractParentIDs_NoHeader(t *testing.T) {
	md := metadata.New(nil)

	segID, traceID, sampled, rawHeader := extractParentIDs(md)

	if segID != "" || traceID != "" {
		t.Errorf("no header: segID=%q traceID=%q, want empty", segID, traceID)
	}
	if sampled != nil {
		t.Errorf("no header: sampled=%v, want nil", sampled)
	}
	if rawHeader != "" {
		t.Errorf("no header: rawHeader=%q, want empty", rawHeader)
	}
}

func TestExtractParentIDs_FallbackSegID(t *testing.T) {
	// No x-amzn-trace-id, but x-amzn-seg-id present as fallback
	md := metadata.New(map[string]string{
		"x-amzn-seg-id": "fallback-seg-456",
	})

	segID, traceID, sampled, _ := extractParentIDs(md)

	if segID != "fallback-seg-456" {
		t.Errorf("fallback segID: got %q, want %q", segID, "fallback-seg-456")
	}
	if traceID != "" {
		t.Errorf("fallback segID: traceID should be empty, got %q", traceID)
	}
	if sampled != nil {
		t.Errorf("fallback segID: sampled should be nil, got %v", sampled)
	}
}

func TestExtractParentIDs_FallbackTraceID(t *testing.T) {
	// No x-amzn-trace-id, but x-amzn-tr-id present as fallback
	md := metadata.New(map[string]string{
		"x-amzn-tr-id": "fallback-trace-789",
	})

	segID, traceID, _, _ := extractParentIDs(md)

	if traceID != "fallback-trace-789" {
		t.Errorf("fallback traceID: got %q, want %q", traceID, "fallback-trace-789")
	}
	if segID != "" {
		t.Errorf("fallback traceID: segID should be empty, got %q", segID)
	}
}

func TestExtractParentIDs_BothFallbacks(t *testing.T) {
	md := metadata.New(map[string]string{
		"x-amzn-seg-id": "seg-fb",
		"x-amzn-tr-id":  "tr-fb",
	})

	segID, traceID, _, _ := extractParentIDs(md)

	if segID != "seg-fb" {
		t.Errorf("both fallbacks segID: got %q, want %q", segID, "seg-fb")
	}
	if traceID != "tr-fb" {
		t.Errorf("both fallbacks traceID: got %q, want %q", traceID, "tr-fb")
	}
}

func TestExtractParentIDs_TraceHeaderTakesPrecedenceOverFallback(t *testing.T) {
	// When x-amzn-trace-id supplies Parent and Root, fallbacks should not override.
	md := metadata.New(map[string]string{
		"x-amzn-trace-id": "Root=primary-trace;Parent=primary-seg",
		"x-amzn-seg-id":   "fallback-seg",
		"x-amzn-tr-id":    "fallback-trace",
	})

	segID, traceID, _, _ := extractParentIDs(md)

	if segID != "primary-seg" {
		t.Errorf("precedence segID: got %q, want %q", segID, "primary-seg")
	}
	if traceID != "primary-trace" {
		t.Errorf("precedence traceID: got %q, want %q", traceID, "primary-trace")
	}
}

// ---------------------------------------------------------------------------
// formatTraceHeader
// ---------------------------------------------------------------------------

func TestFormatTraceHeader_EmptyTraceID(t *testing.T) {
	got := formatTraceHeader("", "parent-1", true)
	if got != "" {
		t.Errorf("empty traceID: got %q, want empty", got)
	}
}

func TestFormatTraceHeader_WhitespaceTraceID(t *testing.T) {
	got := formatTraceHeader("   ", "parent-1", true)
	if got != "" {
		t.Errorf("whitespace traceID: got %q, want empty", got)
	}
}

func TestFormatTraceHeader_WithParentSampledTrue(t *testing.T) {
	got := formatTraceHeader("1-abc-def", "seg123", true)
	want := "Root=1-abc-def;Parent=seg123;Sampled=1"
	if got != want {
		t.Errorf("with parent sampled true: got %q, want %q", got, want)
	}
}

func TestFormatTraceHeader_WithParentSampledFalse(t *testing.T) {
	got := formatTraceHeader("1-abc-def", "seg123", false)
	want := "Root=1-abc-def;Parent=seg123;Sampled=0"
	if got != want {
		t.Errorf("with parent sampled false: got %q, want %q", got, want)
	}
}

func TestFormatTraceHeader_WithoutParent(t *testing.T) {
	got := formatTraceHeader("1-abc-def", "", true)
	want := "Root=1-abc-def;Sampled=1"
	if got != want {
		t.Errorf("without parent sampled true: got %q, want %q", got, want)
	}
}

func TestFormatTraceHeader_WithoutParentSampledFalse(t *testing.T) {
	got := formatTraceHeader("1-abc-def", "", false)
	want := "Root=1-abc-def;Sampled=0"
	if got != want {
		t.Errorf("without parent sampled false: got %q, want %q", got, want)
	}
}

func TestFormatTraceHeader_WhitespaceParentIDIgnored(t *testing.T) {
	got := formatTraceHeader("1-abc", "   ", false)
	want := "Root=1-abc;Sampled=0"
	if got != want {
		t.Errorf("whitespace parentID: got %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// panicTraceError
// ---------------------------------------------------------------------------

func TestPanicTraceError_StringPanic(t *testing.T) {
	err := panicTraceError("something broke")
	if err == nil {
		t.Fatal("panicTraceError returned nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "panic: something broke") {
		t.Errorf("error should contain panic value, got: %s", msg)
	}
	// debug.Stack() output should contain goroutine info
	if !strings.Contains(msg, "goroutine") {
		t.Errorf("error should contain stack trace with 'goroutine', got: %s", msg)
	}
}

func TestPanicTraceError_IntPanic(t *testing.T) {
	err := panicTraceError(42)
	if err == nil {
		t.Fatal("panicTraceError returned nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "panic: 42") {
		t.Errorf("error should contain 'panic: 42', got: %s", msg)
	}
	if !strings.Contains(msg, "goroutine") {
		t.Errorf("error should contain stack trace, got: %s", msg)
	}
}

// ---------------------------------------------------------------------------
// panicClientError
// ---------------------------------------------------------------------------

func TestPanicClientError_StringPanic(t *testing.T) {
	err := panicClientError("client issue")
	if err == nil {
		t.Fatal("panicClientError returned nil")
	}
	want := "panic: client issue"
	if err.Error() != want {
		t.Errorf("panicClientError: got %q, want %q", err.Error(), want)
	}
}

func TestPanicClientError_IntPanic(t *testing.T) {
	err := panicClientError(99)
	if err == nil {
		t.Fatal("panicClientError returned nil")
	}
	want := "panic: 99"
	if err.Error() != want {
		t.Errorf("panicClientError: got %q, want %q", err.Error(), want)
	}
}

func TestPanicClientError_NoStackTrace(t *testing.T) {
	err := panicClientError("boom")
	if strings.Contains(err.Error(), "goroutine") {
		t.Errorf("panicClientError should NOT contain stack trace, got: %s", err.Error())
	}
}
