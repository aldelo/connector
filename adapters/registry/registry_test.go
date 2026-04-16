package registry

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
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
)

// ---------------------------------------------------------------------------
// validateInstanceID
// ---------------------------------------------------------------------------

func TestValidateInstanceID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string // substring expected in error message
	}{
		// --- happy path ---
		{name: "valid simple id", input: "my-instance-01", wantErr: false},
		{name: "valid alphanumeric", input: "abc123", wantErr: false},
		{name: "valid with underscore", input: "my_instance", wantErr: false},
		{name: "valid with dot", input: "my.instance", wantErr: false},
		{name: "valid with dash", input: "my-instance", wantErr: false},
		{name: "valid mixed chars", input: "A.b_c-D.1_2-3", wantErr: false},
		{name: "valid single char", input: "a", wantErr: false},
		{name: "valid exactly 64 chars", input: strings.Repeat("a", 64), wantErr: false},

		// --- empty / whitespace ---
		{name: "empty string", input: "", wantErr: true, errMsg: "InstanceId is Required"},
		{name: "whitespace only", input: "   ", wantErr: true, errMsg: "InstanceId is Required"},
		{name: "tabs only", input: "\t\t", wantErr: true, errMsg: "InstanceId is Required"},

		// --- too long ---
		{name: "65 chars exceeds max", input: strings.Repeat("a", 65), wantErr: true, errMsg: "exceeds maximum length"},

		// --- invalid characters ---
		{name: "contains space", input: "my instance", wantErr: true, errMsg: "must match pattern"},
		{name: "contains slash", input: "my/instance", wantErr: true, errMsg: "must match pattern"},
		{name: "contains colon", input: "my:instance", wantErr: true, errMsg: "must match pattern"},
		{name: "contains at-sign", input: "my@instance", wantErr: true, errMsg: "must match pattern"},
		{name: "contains exclamation", input: "hello!", wantErr: true, errMsg: "must match pattern"},

		// --- whitespace trimming ---
		{name: "leading whitespace trimmed", input: "  valid_id", wantErr: false},
		{name: "trailing whitespace trimmed", input: "valid_id  ", wantErr: false},
		{name: "leading+trailing whitespace trimmed", input: "  valid_id  ", wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateInstanceID(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errMsg)
				}
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tc.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// validateServiceID
// ---------------------------------------------------------------------------

func TestValidateServiceID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// --- happy path ---
		{name: "valid service id", input: "srv-abcdef0123456789", wantErr: false},
		{name: "valid all zeros", input: "srv-0000000000000000", wantErr: false},
		{name: "valid all letters", input: "srv-abcdefabcdefabcd", wantErr: false},

		// --- empty / whitespace ---
		{name: "empty string", input: "", wantErr: true, errMsg: "ServiceId is Required"},
		{name: "whitespace only", input: "   ", wantErr: true, errMsg: "ServiceId is Required"},

		// --- pattern mismatch ---
		{name: "missing srv prefix", input: "abcdef0123456789", wantErr: true, errMsg: "must match pattern"},
		{name: "wrong prefix", input: "svc-abcdef0123456789", wantErr: true, errMsg: "must match pattern"},
		{name: "too short suffix", input: "srv-abcdef01234567", wantErr: true, errMsg: "must match pattern"},
		{name: "too long suffix", input: "srv-abcdef01234567890", wantErr: true, errMsg: "must match pattern"},
		{name: "uppercase in suffix", input: "srv-ABCDEF0123456789", wantErr: true, errMsg: "must match pattern"},
		{name: "special chars in suffix", input: "srv-abcdef012345678!", wantErr: true, errMsg: "must match pattern"},

		// --- whitespace trimming ---
		{name: "leading whitespace trimmed", input: "  srv-abcdef0123456789", wantErr: false},
		{name: "trailing whitespace trimmed", input: "srv-abcdef0123456789  ", wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateServiceID(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errMsg)
				}
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tc.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// validateNamespaceID
// ---------------------------------------------------------------------------

func TestValidateNamespaceID(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// --- happy path ---
		{name: "valid namespace id", input: "ns-abcdef0123456789", wantErr: false},
		{name: "valid all zeros", input: "ns-0000000000000000", wantErr: false},
		{name: "valid all letters", input: "ns-abcdefabcdefabcd", wantErr: false},

		// --- empty / whitespace ---
		{name: "empty string", input: "", wantErr: true, errMsg: "NamespaceId is Required"},
		{name: "whitespace only", input: "   ", wantErr: true, errMsg: "NamespaceId is Required"},

		// --- pattern mismatch ---
		{name: "missing ns prefix", input: "abcdef0123456789", wantErr: true, errMsg: "must match pattern"},
		{name: "wrong prefix", input: "srv-abcdef0123456789", wantErr: true, errMsg: "must match pattern"},
		{name: "too short suffix", input: "ns-abcdef01234567", wantErr: true, errMsg: "must match pattern"},
		{name: "too long suffix", input: "ns-abcdef01234567890", wantErr: true, errMsg: "must match pattern"},
		{name: "uppercase in suffix", input: "ns-ABCDEF0123456789", wantErr: true, errMsg: "must match pattern"},
		{name: "special chars in suffix", input: "ns-abcdef012345678!", wantErr: true, errMsg: "must match pattern"},

		// --- whitespace trimming ---
		{name: "leading whitespace trimmed", input: "  ns-abcdef0123456789", wantErr: false},
		{name: "trailing whitespace trimmed", input: "ns-abcdef0123456789  ", wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNamespaceID(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errMsg)
				}
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tc.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// validateServiceName
// ---------------------------------------------------------------------------

func TestValidateServiceName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// --- DNS-valid names (lowercase alnum + dashes, max 63) ---
		{name: "valid dns simple", input: "my-service", wantErr: false},
		{name: "valid dns single char", input: "a", wantErr: false},
		{name: "valid dns two chars", input: "ab", wantErr: false},
		{name: "valid dns numeric", input: "123", wantErr: false},
		{name: "valid dns 63 chars", input: strings.Repeat("a", 63), wantErr: false},
		{name: "valid dns with dashes", input: "my-cool-service", wantErr: false},

		// --- HTTP-valid names (mixed case alnum + dashes, max 64) ---
		{name: "valid http mixed case", input: "MyService", wantErr: false},
		{name: "valid http 64 chars mixed", input: "A" + strings.Repeat("b", 62) + "C", wantErr: false},

		// --- empty / whitespace ---
		{name: "empty string", input: "", wantErr: true, errMsg: "Service Name is Required"},
		{name: "whitespace only", input: "   ", wantErr: true, errMsg: "Service Name is Required"},

		// --- too long ---
		{name: "dns name 64 chars lowercase fails dns but passes http", input: strings.Repeat("a", 64), wantErr: false},
		// 65 chars: exceeds HTTP regex max (0-62 middle chars = 64 total), so it fails pattern, not length
		{name: "http name 65 chars fails pattern", input: "A" + strings.Repeat("b", 63) + "C", wantErr: true, errMsg: "must match"},

		// --- invalid characters ---
		{name: "contains underscore", input: "my_service", wantErr: true, errMsg: "must match"},
		{name: "contains dot", input: "my.service", wantErr: true, errMsg: "must match"},
		{name: "contains space", input: "my service", wantErr: true, errMsg: "must match"},
		{name: "starts with dash", input: "-service", wantErr: true, errMsg: "must match"},
		{name: "ends with dash", input: "service-", wantErr: true, errMsg: "must match"},

		// --- whitespace trimming ---
		{name: "leading whitespace trimmed", input: "  my-service", wantErr: false},
		{name: "trailing whitespace trimmed", input: "my-service  ", wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateServiceName(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errMsg)
				}
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tc.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// validateNamespaceName
// ---------------------------------------------------------------------------

func TestValidateNamespaceName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// --- DNS-valid names (dotted lowercase labels, max 253) ---
		{name: "valid dns simple", input: "example.com", wantErr: false},
		{name: "valid dns single label", input: "local", wantErr: false},
		{name: "valid dns multi-label", input: "my.namespace.example.com", wantErr: false},
		{name: "valid dns single char", input: "a", wantErr: false},
		{name: "valid dns with dashes", input: "my-ns.example.com", wantErr: false},
		{name: "valid dns 253 chars", input: buildDNSName(253), wantErr: false},

		// --- HTTP-valid names (mixed case, up to 1024) ---
		{name: "valid http mixed case", input: "MyNamespace", wantErr: false},
		{name: "valid http with dashes", input: "My-Namespace", wantErr: false},

		// --- empty / whitespace ---
		{name: "empty string", input: "", wantErr: true, errMsg: "Namespace Name is Required"},
		{name: "whitespace only", input: "   ", wantErr: true, errMsg: "Namespace Name is Required"},

		// --- too long ---
		{name: "exceeds 1024 chars", input: "A" + strings.Repeat("b", 1023) + "C", wantErr: true, errMsg: "exceeds maximum length"},

		// --- invalid characters ---
		{name: "contains underscore", input: "my_namespace", wantErr: true, errMsg: "must match"},
		{name: "contains space", input: "my namespace", wantErr: true, errMsg: "must match"},
		{name: "starts with dash", input: "-namespace", wantErr: true, errMsg: "must match"},
		{name: "ends with dash", input: "namespace-", wantErr: true, errMsg: "must match"},
		{name: "starts with dot", input: ".example.com", wantErr: true, errMsg: "must match"},

		// --- whitespace trimming ---
		{name: "leading whitespace trimmed", input: "  example.com", wantErr: false},
		{name: "trailing whitespace trimmed", input: "example.com  ", wantErr: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNamespaceName(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errMsg)
				}
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tc.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

// buildDNSName constructs a valid DNS name of exactly targetLen characters.
// Uses repeated "a{61}.a" segments joined by dots, trimmed to exact length.
func buildDNSName(targetLen int) string {
	if targetLen <= 0 {
		return ""
	}
	// Build with short labels: "aa.aa.aa..." each label is 2 chars, dot is 1 => 3 per segment.
	// For simplicity, build a long dotted name and trim.
	var b strings.Builder
	for b.Len() < targetLen+100 {
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		// Write a label up to 62 chars (alnum, starts and ends with alnum)
		labelLen := 62
		if targetLen-b.Len() < labelLen {
			labelLen = targetLen - b.Len()
		}
		if labelLen <= 0 {
			break
		}
		for i := 0; i < labelLen; i++ {
			b.WriteByte('a')
		}
	}
	s := b.String()
	if len(s) > targetLen {
		s = s[:targetLen]
	}
	// Ensure it does not end with a dot or dash
	s = strings.TrimRight(s, ".-")
	// Pad if needed
	for len(s) < targetLen {
		s += "a"
	}
	return s
}

// ---------------------------------------------------------------------------
// validateInstancePrefix
// ---------------------------------------------------------------------------

func TestValidateInstancePrefix(t *testing.T) {
	// instanceIdMaxLen=64, uuidLength=36, so max prefix = 28
	tests := []struct {
		name    string
		input   string
		wantErr bool
		errMsg  string
	}{
		// --- happy path ---
		{name: "empty prefix is valid", input: "", wantErr: false},
		{name: "valid short prefix", input: "svc-", wantErr: false},
		{name: "valid 28 char prefix", input: strings.Repeat("a", 28), wantErr: false},
		{name: "valid with dot", input: "my.svc.", wantErr: false},
		{name: "valid with underscore", input: "my_svc_", wantErr: false},
		{name: "valid with dash", input: "my-svc-", wantErr: false},

		// --- too long ---
		{name: "29 chars exceeds max prefix", input: strings.Repeat("a", 29), wantErr: true, errMsg: "prefix too long"},

		// --- invalid characters ---
		{name: "contains space", input: "my svc", wantErr: true, errMsg: "must match pattern"},
		{name: "contains slash", input: "my/svc", wantErr: true, errMsg: "must match pattern"},
		{name: "contains at-sign", input: "my@svc", wantErr: true, errMsg: "must match pattern"},
		{name: "contains colon", input: "svc:", wantErr: true, errMsg: "must match pattern"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateInstancePrefix(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.errMsg)
				}
				if !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("expected error containing %q, got %q", tc.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// sanitizeMaxResults
// ---------------------------------------------------------------------------

func TestSanitizeMaxResults(t *testing.T) {
	tests := []struct {
		name     string
		input    *int64
		wantNil  bool
		wantVal  int64
	}{
		// nil input → nil output
		{name: "nil input", input: nil, wantNil: true},

		// non-positive → nil (drop to service default)
		{name: "zero", input: aws.Int64(0), wantNil: true},
		{name: "negative one", input: aws.Int64(-1), wantNil: true},
		{name: "negative large", input: aws.Int64(-100), wantNil: true},

		// within range → pass through
		{name: "one", input: aws.Int64(1), wantNil: false, wantVal: 1},
		{name: "fifty", input: aws.Int64(50), wantNil: false, wantVal: 50},
		{name: "hundred", input: aws.Int64(100), wantNil: false, wantVal: 100},

		// above cap → clamped to 100
		{name: "hundred one clamped", input: aws.Int64(101), wantNil: false, wantVal: 100},
		{name: "thousand clamped", input: aws.Int64(1000), wantNil: false, wantVal: 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := sanitizeMaxResults(tc.input)
			if tc.wantNil {
				if result != nil {
					t.Fatalf("expected nil, got %d", *result)
				}
			} else {
				if result == nil {
					t.Fatalf("expected %d, got nil", tc.wantVal)
				}
				if *result != tc.wantVal {
					t.Fatalf("expected %d, got %d", tc.wantVal, *result)
				}
			}
		})
	}
}
