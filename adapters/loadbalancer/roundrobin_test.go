package loadbalancer

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
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Compiled regex sanity checks — verify the package-level regexes exist and
// behave as the function under test relies on.
// ---------------------------------------------------------------------------

func TestSchemeRE_AcceptsValidSchemes(t *testing.T) {
	valid := []string{"a", "http", "grpc", "a1", "a+b", "a.b", "a-b", "scheme1+v2.rc-3"}
	for _, s := range valid {
		if !schemeRE.MatchString(s) {
			t.Errorf("schemeRE should match %q", s)
		}
	}
}

func TestSchemeRE_RejectsInvalidSchemes(t *testing.T) {
	invalid := []string{"1abc", "+abc", ".abc", "-abc", "A", "Http", "ab cd", "ab!cd", ""}
	for _, s := range invalid {
		if schemeRE.MatchString(s) {
			t.Errorf("schemeRE should not match %q", s)
		}
	}
}

func TestServiceRE_AcceptsValidServiceNames(t *testing.T) {
	valid := []string{"svc", "my-service", "my.service", "my_service", "MyService123", "a~b"}
	for _, s := range valid {
		if !serviceRE.MatchString(s) {
			t.Errorf("serviceRE should match %q", s)
		}
	}
}

func TestServiceRE_RejectsInvalidServiceNames(t *testing.T) {
	invalid := []string{"svc/name", "svc name", "svc:name", "a b", "a/b", ""}
	for _, s := range invalid {
		if serviceRE.MatchString(s) {
			t.Errorf("serviceRE should not match %q", s)
		}
	}
}

func TestHostLabelRE_AcceptsValidLabels(t *testing.T) {
	valid := []string{"a", "A", "localhost", "my-host", "a0", "a0b", "abc-def-ghi"}
	for _, s := range valid {
		if !hostLabelRE.MatchString(s) {
			t.Errorf("hostLabelRE should match %q", s)
		}
	}
}

func TestHostLabelRE_RejectsInvalidLabels(t *testing.T) {
	invalid := []string{"-start", "end-", "-both-", "", ".dot"}
	for _, s := range invalid {
		if hostLabelRE.MatchString(s) {
			t.Errorf("hostLabelRE should not match %q", s)
		}
	}
}

// ---------------------------------------------------------------------------
// WithRoundRobin — table-driven validation tests (error path)
// ---------------------------------------------------------------------------

func TestWithRoundRobin_ValidationErrors(t *testing.T) {
	tests := []struct {
		name      string
		scheme    string
		service   string
		endpoints []string
		wantSub   string // substring expected in the error message
	}{
		// ---- scheme validation ----
		{
			name:      "empty scheme",
			scheme:    "",
			service:   "svc",
			endpoints: []string{"localhost:8080"},
			wantSub:   "resolver scheme name is required",
		},
		{
			name:      "whitespace-only scheme",
			scheme:    "   ",
			service:   "svc",
			endpoints: []string{"localhost:8080"},
			wantSub:   "resolver scheme name is required",
		},
		{
			name:      "scheme starts with digit after lowering",
			scheme:    "1abc",
			service:   "svc",
			endpoints: []string{"localhost:8080"},
			wantSub:   "is invalid (must match",
		},
		{
			name:      "scheme with special char bang",
			scheme:    "bad!",
			service:   "svc",
			endpoints: []string{"localhost:8080"},
			wantSub:   "is invalid (must match",
		},
		{
			name:      "scheme with uppercase only letters",
			scheme:    "BAD SCHEME",
			service:   "svc",
			endpoints: []string{"localhost:8080"},
			// after TrimSpace+ToLower → "bad scheme" contains space → regex fails
			wantSub: "is invalid (must match",
		},
		{
			name:      "scheme with slash",
			scheme:    "a/b",
			service:   "svc",
			endpoints: []string{"localhost:8080"},
			wantSub:   "is invalid (must match",
		},
		{
			name:      "scheme with colon",
			scheme:    "a:b",
			service:   "svc",
			endpoints: []string{"localhost:8080"},
			wantSub:   "is invalid (must match",
		},

		// ---- service name validation ----
		{
			name:      "empty service name",
			scheme:    "myscheme",
			service:   "",
			endpoints: []string{"localhost:8080"},
			wantSub:   "resolver service name is required",
		},
		{
			name:      "whitespace-only service name",
			scheme:    "myscheme",
			service:   "   ",
			endpoints: []string{"localhost:8080"},
			wantSub:   "resolver service name is required",
		},
		{
			name:      "service name with slash",
			scheme:    "myscheme",
			service:   "svc/name",
			endpoints: []string{"localhost:8080"},
			// serviceRE check fires first because '/' not in [A-Za-z0-9._~-]
			wantSub: "is invalid",
		},
		{
			name:      "service name with embedded space",
			scheme:    "myscheme",
			service:   "svc name",
			endpoints: []string{"localhost:8080"},
			// serviceRE check fires first because ' ' not in [A-Za-z0-9._~-]
			wantSub: "is invalid",
		},
		{
			name:      "service name with tab",
			scheme:    "myscheme",
			service:   "svc\tname",
			endpoints: []string{"localhost:8080"},
			wantSub:   "is invalid",
		},
		{
			name:      "service name with colon-slash-slash",
			scheme:    "myscheme",
			service:   "http://svc",
			endpoints: []string{"localhost:8080"},
			// serviceRE check fires: ':' and '/' not in allowed chars
			wantSub: "is invalid",
		},

		// ---- endpoint list validation ----
		{
			name:      "nil endpoint list",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: nil,
			wantSub:   "resolver endpoint addresses are required",
		},
		{
			name:      "empty endpoint list",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{},
			wantSub:   "resolver endpoint addresses are required",
		},
		{
			name:      "all empty/whitespace endpoints",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"", "  ", "\t"},
			wantSub:   "all were empty",
		},

		// ---- individual endpoint validation ----
		{
			name:      "endpoint without port bare hostname",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"localhost"},
			wantSub:   "is invalid (want host:port",
		},
		{
			name:      "endpoint with colon but no port value",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"localhost:"},
			wantSub:   "is invalid (want host:port",
		},
		{
			name:      "endpoint with port 0",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"localhost:0"},
			wantSub:   "has invalid port",
		},
		{
			name:      "endpoint with port 65536",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"localhost:65536"},
			wantSub:   "has invalid port",
		},
		{
			name:      "endpoint with non-numeric port",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"localhost:abc"},
			wantSub:   "has invalid port",
		},
		{
			name:      "endpoint with negative port",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"localhost:-1"},
			// net.SplitHostPort may parse this; then strconv.Atoi("-1") = -1 < 1 → invalid port
			wantSub: "has invalid port",
		},
		{
			name:      "endpoint with URI scheme http",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"http://localhost:8080"},
			wantSub:   "must not include a URI scheme",
		},
		{
			name:      "endpoint with URI scheme https",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"https://example.com:443"},
			wantSub:   "must not include a URI scheme",
		},
		{
			name:      "endpoint with invalid host label starts with hyphen",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"-invalid:8080"},
			wantSub:   "has invalid host label",
		},
		{
			name:      "endpoint with invalid host label ends with hyphen",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"invalid-:8080"},
			wantSub:   "has invalid host label",
		},
		{
			name:      "endpoint with empty host after trailing-dot strip",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{".:8080"},
			wantSub:   "host cannot be empty",
		},
		{
			name:      "invalid IPv6 malformed brackets",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"[:::bad]:8080"},
			wantSub:   "has invalid IP literal",
		},
		{
			name:      "IPv6 with empty zone",
			scheme:    "myscheme",
			service:   "svc",
			endpoints: []string{"[::1%]:8080"},
			// net.SplitHostPort extracts host = "::1%" → zone parsing finds % at end → zone=""
			wantSub: "invalid IPv6 zone (empty)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, policy, err := WithRoundRobin(tc.scheme, tc.service, tc.endpoints)
			if err == nil {
				t.Fatalf("expected error containing %q; got nil (target=%q, policy=%q)", tc.wantSub, target, policy)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
			if target != "" {
				t.Errorf("expected empty target on error; got %q", target)
			}
			if policy != "" {
				t.Errorf("expected empty policy on error; got %q", policy)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// WithRoundRobin — success path tests
// ---------------------------------------------------------------------------

// uniqueScheme returns a test-unique scheme to avoid gRPC resolver registration
// conflicts between test cases. gRPC's resolver.Register is global and
// idempotent per scheme, but different schemes avoid any cross-test state.
func uniqueScheme(suffix string) string {
	return "rrt" + suffix // "rrt" = round robin test
}

func TestWithRoundRobin_SingleEndpoint(t *testing.T) {
	scheme := uniqueScheme("single")
	target, policy, err := WithRoundRobin(scheme, "myservice", []string{"localhost:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantTarget := fmt.Sprintf("%s:///myservice", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}

	if !strings.Contains(policy, "round_robin") {
		t.Errorf("policy %q does not contain %q", policy, "round_robin")
	}
	if !strings.Contains(policy, "loadBalancingConfig") {
		t.Errorf("policy %q does not contain %q", policy, "loadBalancingConfig")
	}

	// Verify it is a JSON fragment (no leading '{')
	if strings.HasPrefix(policy, "{") {
		t.Errorf("policy should be a JSON fragment without outer braces; got %q", policy)
	}
}

func TestWithRoundRobin_MultipleEndpoints(t *testing.T) {
	scheme := uniqueScheme("multi")
	endpoints := []string{"host1:8080", "host2:9090", "host3:443"}
	target, policy, err := WithRoundRobin(scheme, "multisvc", endpoints)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantTarget := fmt.Sprintf("%s:///multisvc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
	if !strings.Contains(policy, "round_robin") {
		t.Errorf("policy should contain round_robin; got %q", policy)
	}
}

func TestWithRoundRobin_DuplicateEndpoints_Deduplicated(t *testing.T) {
	scheme := uniqueScheme("dedup")
	// Duplicates (case-insensitive) should be deduplicated silently, not error.
	endpoints := []string{"localhost:8080", "LOCALHOST:8080", "  localhost:8080  "}
	target, _, err := WithRoundRobin(scheme, "dedupsvc", endpoints)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///dedupsvc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
}

func TestWithRoundRobin_IPv6Endpoint(t *testing.T) {
	scheme := uniqueScheme("ipv6")
	target, policy, err := WithRoundRobin(scheme, "ipv6svc", []string{"[::1]:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///ipv6svc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
	if !strings.Contains(policy, "round_robin") {
		t.Errorf("policy should contain round_robin; got %q", policy)
	}
}

func TestWithRoundRobin_IPv6WithValidZone(t *testing.T) {
	scheme := uniqueScheme("ipv6z")
	// IPv6 with a valid zone identifier (e.g., "eth0")
	target, _, err := WithRoundRobin(scheme, "zonesvc", []string{"[fe80::1%25eth0]:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///zonesvc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
}

func TestWithRoundRobin_TrailingDotOnHostname(t *testing.T) {
	scheme := uniqueScheme("trdot")
	// Trailing dot on FQDN should be stripped; the address is still valid.
	target, _, err := WithRoundRobin(scheme, "dotsvc", []string{"example.com.:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///dotsvc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
}

func TestWithRoundRobin_PortBoundary_Min(t *testing.T) {
	scheme := uniqueScheme("pmin")
	target, _, err := WithRoundRobin(scheme, "portsvc1", []string{"localhost:1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///portsvc1", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
}

func TestWithRoundRobin_PortBoundary_Max(t *testing.T) {
	scheme := uniqueScheme("pmax")
	target, _, err := WithRoundRobin(scheme, "portsvc2", []string{"localhost:65535"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///portsvc2", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
}

func TestWithRoundRobin_TargetFormat(t *testing.T) {
	// Verify the exact target format: scheme:///serviceName (triple-slash)
	scheme := uniqueScheme("fmt")
	target, _, err := WithRoundRobin(scheme, "formatsvc", []string{"localhost:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Must be exactly scheme + ":///" + service
	expected := scheme + ":///" + "formatsvc"
	if target != expected {
		t.Errorf("target = %q; want %q", target, expected)
	}

	// Verify triple-slash (no double, no single)
	if !strings.Contains(target, ":///") {
		t.Errorf("target should contain triple-slash (:///); got %q", target)
	}
}

func TestWithRoundRobin_PolicyFragment(t *testing.T) {
	scheme := uniqueScheme("pol")
	_, policy, err := WithRoundRobin(scheme, "polsvc", []string{"localhost:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The policy is a JSON *fragment* — no leading/trailing braces.
	// When wrapped by the caller: `{<policy>}` must produce valid JSON.
	wantExact := `"loadBalancingConfig":[{"round_robin":{}}]`
	if policy != wantExact {
		t.Errorf("policy = %q; want exact %q", policy, wantExact)
	}
}

func TestWithRoundRobin_SchemeIsTrimmedAndLowered(t *testing.T) {
	scheme := uniqueScheme("trim")
	// Pass uppercase + whitespace; function should normalize.
	target, _, err := WithRoundRobin("  "+strings.ToUpper(scheme)+"  ", "trimsvc", []string{"localhost:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///trimsvc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q (scheme should be lowered+trimmed)", target, wantTarget)
	}
}

func TestWithRoundRobin_ServiceIsTrimmed(t *testing.T) {
	scheme := uniqueScheme("svctrim")
	// Service is trimmed but NOT lowered (unlike resolver package, WithRoundRobin
	// only trims — the serviceRE allows uppercase).
	target, _, err := WithRoundRobin(scheme, "  MySvc  ", []string{"localhost:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///MySvc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q (service should be trimmed but preserve case)", target, wantTarget)
	}
}

func TestWithRoundRobin_ReRegisterSameSchemeService(t *testing.T) {
	// Calling WithRoundRobin twice with the same scheme+service should succeed
	// (the resolver package does an upsert, not a reject).
	scheme := uniqueScheme("rereg")
	_, _, err := WithRoundRobin(scheme, "reregsvc", []string{"localhost:8080"})
	if err != nil {
		t.Fatalf("first call: unexpected error: %v", err)
	}

	// Second call with different endpoints should also succeed.
	target, policy, err := WithRoundRobin(scheme, "reregsvc", []string{"localhost:9090"})
	if err != nil {
		t.Fatalf("second call: unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///reregsvc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
	if !strings.Contains(policy, "round_robin") {
		t.Errorf("policy should contain round_robin on re-registration; got %q", policy)
	}
}

func TestWithRoundRobin_MixedValidAndEmptyEndpoints(t *testing.T) {
	scheme := uniqueScheme("mixed")
	// Some empty, some valid — should succeed with only valid ones.
	endpoints := []string{"", "  ", "localhost:8080", "\t", "host2:9090"}
	target, _, err := WithRoundRobin(scheme, "mixedsvc", endpoints)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///mixedsvc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
}

func TestWithRoundRobin_IPv4Endpoint(t *testing.T) {
	scheme := uniqueScheme("ipv4")
	target, _, err := WithRoundRobin(scheme, "ipv4svc", []string{"192.168.1.1:8080"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///ipv4svc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
}

func TestWithRoundRobin_HostWithSubdomain(t *testing.T) {
	scheme := uniqueScheme("sub")
	target, _, err := WithRoundRobin(scheme, "subsvc", []string{"api.example.com:443"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTarget := fmt.Sprintf("%s:///subsvc", scheme)
	if target != wantTarget {
		t.Errorf("target = %q; want %q", target, wantTarget)
	}
}

// ---------------------------------------------------------------------------
// Edge-case error path tests that don't fit neatly into the table above
// ---------------------------------------------------------------------------

func TestWithRoundRobin_HostContainsScheme(t *testing.T) {
	// An endpoint host containing "://" should be caught.
	_, _, err := WithRoundRobin("testscheme1", "svc", []string{"ftp://host:80"})
	if err == nil {
		t.Fatal("expected error for endpoint with URI scheme; got nil")
	}
	if !strings.Contains(err.Error(), "must not include a URI scheme") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWithRoundRobin_HostContainsPathChars(t *testing.T) {
	// A host with path-like characters after the port would be caught by
	// the host validation. The "/" makes net.SplitHostPort fail.
	_, _, err := WithRoundRobin("testscheme2", "svc", []string{"host/path:8080"})
	if err == nil {
		t.Fatal("expected error for host with path characters; got nil")
	}
}

func TestWithRoundRobin_LargePortNumber(t *testing.T) {
	_, _, err := WithRoundRobin("testscheme3", "svc", []string{"localhost:99999"})
	if err == nil {
		t.Fatal("expected error for port 99999; got nil")
	}
	if !strings.Contains(err.Error(), "has invalid port") {
		t.Errorf("unexpected error: %v", err)
	}
}
