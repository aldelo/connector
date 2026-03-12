package resolver

import (
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// normalizeSchemeName
// ---------------------------------------------------------------------------

func TestNormalizeSchemeName_EmptyReturnsDefault(t *testing.T) {
	if got := normalizeSchemeName(""); got != "clb" {
		t.Errorf("normalizeSchemeName(%q) = %q; want %q", "", got, "clb")
	}
}

func TestNormalizeSchemeName_WhitespaceOnlyReturnsDefault(t *testing.T) {
	for _, input := range []string{" ", "  ", "\t", "\n", " \t\n "} {
		if got := normalizeSchemeName(input); got != "clb" {
			t.Errorf("normalizeSchemeName(%q) = %q; want %q", input, got, "clb")
		}
	}
}

func TestNormalizeSchemeName_ValidInputIsLowercasedAndTrimmed(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http", "http"},
		{"HTTP", "http"},
		{"  Http  ", "http"},
		{"MyScheme", "myscheme"},
		{"a+b.c-d", "a+b.c-d"},
	}
	for _, tc := range tests {
		got := normalizeSchemeName(tc.input)
		if got != tc.want {
			t.Errorf("normalizeSchemeName(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeSchemeName_InvalidCharsArePreserved(t *testing.T) {
	// normalizeSchemeName only lowercases/trims; it does NOT validate.
	// Characters like '!' are passed through.
	got := normalizeSchemeName("bad!")
	if got != "bad!" {
		t.Errorf("normalizeSchemeName(%q) = %q; want %q", "bad!", got, "bad!")
	}
}

// ---------------------------------------------------------------------------
// normalizeAndValidateSchemeName
// ---------------------------------------------------------------------------

func TestNormalizeAndValidateSchemeName_ValidSchemes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"http", "http"},
		{"HTTP", "http"},
		{"  grpc  ", "grpc"},
		{"a1", "a1"},
		{"a+b", "a+b"},
		{"a.b", "a.b"},
		{"a-b", "a-b"},
		{"scheme1+v2.rc-3", "scheme1+v2.rc-3"},
		// empty input normalizes to "clb", which is a valid scheme
		{"", "clb"},
		{"   ", "clb"},
	}
	for _, tc := range tests {
		got, err := normalizeAndValidateSchemeName(tc.input)
		if err != nil {
			t.Errorf("normalizeAndValidateSchemeName(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeAndValidateSchemeName(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeAndValidateSchemeName_InvalidSchemes(t *testing.T) {
	tests := []struct {
		input string
		desc  string
	}{
		{"1abc", "starts with digit"},
		{"+abc", "starts with plus"},
		{".abc", "starts with dot"},
		{"-abc", "starts with hyphen"},
		{"ab cd", "contains space after normalization"},
		{"ab!cd", "contains exclamation mark"},
		{"ab@cd", "contains at sign"},
		{"ab#cd", "contains hash"},
		{"ab/cd", "contains slash"},
		{"ab:cd", "contains colon"},
		{"ABC!", "uppercase with special char"},
	}
	for _, tc := range tests {
		got, err := normalizeAndValidateSchemeName(tc.input)
		if err == nil {
			t.Errorf("normalizeAndValidateSchemeName(%q) [%s] = %q; want error", tc.input, tc.desc, got)
		}
	}
}

// ---------------------------------------------------------------------------
// normalizeServiceName
// ---------------------------------------------------------------------------

func TestNormalizeServiceName_EmptyReturnsError(t *testing.T) {
	_, err := normalizeServiceName("")
	if err == nil {
		t.Error("normalizeServiceName(\"\") expected error; got nil")
	}
}

func TestNormalizeServiceName_WhitespaceOnlyReturnsError(t *testing.T) {
	for _, input := range []string{" ", "  ", "\t", "\n"} {
		_, err := normalizeServiceName(input)
		if err == nil {
			t.Errorf("normalizeServiceName(%q) expected error; got nil", input)
		}
	}
}

func TestNormalizeServiceName_ContainsColonReturnsError(t *testing.T) {
	_, err := normalizeServiceName("svc:name")
	if err == nil {
		t.Error("normalizeServiceName(\"svc:name\") expected error; got nil")
	}
	if err != nil && !strings.Contains(err.Error(), "must not contain") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNormalizeServiceName_ContainsSpaceAfterTrimReturnsError(t *testing.T) {
	_, err := normalizeServiceName("svc name")
	if err == nil {
		t.Error("normalizeServiceName(\"svc name\") expected error; got nil")
	}
}

func TestNormalizeServiceName_ValidNames(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"myService", "myservice"},
		{"  MyService  ", "myservice"},
		{"SVC", "svc"},
		{"service-1", "service-1"},
		{"service.v2", "service.v2"},
		{"service_name", "service_name"},
	}
	for _, tc := range tests {
		got, err := normalizeServiceName(tc.input)
		if err != nil {
			t.Errorf("normalizeServiceName(%q) unexpected error: %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeServiceName(%q) = %q; want %q", tc.input, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// composeKey
// ---------------------------------------------------------------------------

func TestComposeKey_BasicComposition(t *testing.T) {
	got := composeKey("http", "myservice")
	want := "http:myservice"
	if got != want {
		t.Errorf("composeKey(%q, %q) = %q; want %q", "http", "myservice", got, want)
	}
}

func TestComposeKey_CaseInsensitivity(t *testing.T) {
	a := composeKey("HTTP", "MyService")
	b := composeKey("http", "myservice")
	if a != b {
		t.Errorf("composeKey should be case-insensitive: %q != %q", a, b)
	}
}

func TestComposeKey_WhitespaceHandling(t *testing.T) {
	a := composeKey("  http  ", "  myservice  ")
	b := composeKey("http", "myservice")
	if a != b {
		t.Errorf("composeKey should trim whitespace: %q != %q", a, b)
	}
}

func TestComposeKey_MixedCaseAndWhitespace(t *testing.T) {
	a := composeKey("  HTTP  ", "  MyService  ")
	want := "http:myservice"
	if a != want {
		t.Errorf("composeKey(%q, %q) = %q; want %q", "  HTTP  ", "  MyService  ", a, want)
	}
}

func TestComposeKey_EmptyInputs(t *testing.T) {
	got := composeKey("", "")
	want := ":"
	if got != want {
		t.Errorf("composeKey(%q, %q) = %q; want %q", "", "", got, want)
	}
}

func TestComposeKey_DistinctPairsProduceDistinctKeys(t *testing.T) {
	k1 := composeKey("a", "b")
	k2 := composeKey("ab", "")
	if k1 == k2 {
		t.Errorf("different scheme:service pairs should produce different keys: %q == %q", k1, k2)
	}
}

// ---------------------------------------------------------------------------
// validatePort
// ---------------------------------------------------------------------------

func TestValidatePort_EmptyReturnsError(t *testing.T) {
	if err := validatePort(""); err == nil {
		t.Error("validatePort(\"\") expected error; got nil")
	}
}

func TestValidatePort_WhitespaceOnlyReturnsError(t *testing.T) {
	if err := validatePort("   "); err == nil {
		t.Error("validatePort(\"   \") expected error; got nil")
	}
}

func TestValidatePort_NonNumericReturnsError(t *testing.T) {
	for _, input := range []string{"abc", "12ab", "port", "3.14", "-1", "0"} {
		if err := validatePort(input); err == nil {
			t.Errorf("validatePort(%q) expected error; got nil", input)
		}
	}
}

func TestValidatePort_ZeroReturnsError(t *testing.T) {
	if err := validatePort("0"); err == nil {
		t.Error("validatePort(\"0\") expected error; got nil")
	}
}

func TestValidatePort_NegativeReturnsError(t *testing.T) {
	if err := validatePort("-1"); err == nil {
		t.Error("validatePort(\"-1\") expected error; got nil")
	}
}

func TestValidatePort_OutOfRangeReturnsError(t *testing.T) {
	for _, input := range []string{"65536", "70000", "100000"} {
		if err := validatePort(input); err == nil {
			t.Errorf("validatePort(%q) expected error; got nil", input)
		}
	}
}

func TestValidatePort_ValidPorts(t *testing.T) {
	for _, input := range []string{"1", "80", "443", "8080", "65535"} {
		if err := validatePort(input); err != nil {
			t.Errorf("validatePort(%q) unexpected error: %v", input, err)
		}
	}
}

func TestValidatePort_ValidPortWithWhitespace(t *testing.T) {
	if err := validatePort(" 8080 "); err != nil {
		t.Errorf("validatePort(\" 8080 \") unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// normalizeAddresses
// ---------------------------------------------------------------------------

func TestNormalizeAddresses_EmptyListReturnsDefaultError(t *testing.T) {
	_, err := normalizeAddresses(nil, nil)
	if err == nil {
		t.Fatal("expected error for nil list; got nil")
	}
	if !strings.Contains(err.Error(), "No Valid Endpoint Address Found") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNormalizeAddresses_EmptyListReturnsCustomError(t *testing.T) {
	custom := errors.New("custom empty error")
	_, err := normalizeAddresses([]string{}, custom)
	if err == nil {
		t.Fatal("expected error for empty list; got nil")
	}
	if err != custom {
		t.Errorf("expected custom error; got %v", err)
	}
}

func TestNormalizeAddresses_AllBlankEntriesReturnsError(t *testing.T) {
	_, err := normalizeAddresses([]string{"", "  ", "\t"}, nil)
	if err == nil {
		t.Fatal("expected error for all-blank list; got nil")
	}
}

func TestNormalizeAddresses_ValidHostPort(t *testing.T) {
	addrs, err := normalizeAddresses([]string{"localhost:8080", "example.com:443"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addresses; got %d", len(addrs))
	}
	if addrs[0].Addr != "localhost:8080" {
		t.Errorf("addrs[0].Addr = %q; want %q", addrs[0].Addr, "localhost:8080")
	}
	if addrs[1].Addr != "example.com:443" {
		t.Errorf("addrs[1].Addr = %q; want %q", addrs[1].Addr, "example.com:443")
	}
}

func TestNormalizeAddresses_HostsAreLowercased(t *testing.T) {
	addrs, err := normalizeAddresses([]string{"MyHost:8080"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addrs[0].Addr != "myhost:8080" {
		t.Errorf("addrs[0].Addr = %q; want %q", addrs[0].Addr, "myhost:8080")
	}
}

func TestNormalizeAddresses_DuplicatesAreRemoved(t *testing.T) {
	addrs, err := normalizeAddresses([]string{
		"host:80",
		"host:80",
		"HOST:80",
		"  host:80  ",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 1 {
		t.Errorf("expected 1 address after dedup; got %d: %v", len(addrs), addrs)
	}
}

func TestNormalizeAddresses_IPv6BracketedWithPort(t *testing.T) {
	addrs, err := normalizeAddresses([]string{"[::1]:8080"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("expected 1 address; got %d", len(addrs))
	}
	if addrs[0].Addr != "[::1]:8080" {
		t.Errorf("addrs[0].Addr = %q; want %q", addrs[0].Addr, "[::1]:8080")
	}
}

func TestNormalizeAddresses_IPv6UnbracketedWithPortLikeTailReturnsError(t *testing.T) {
	// An unbracketed IPv6 address with a trailing segment that looks numeric
	// should be rejected because it is ambiguous (could be misread as host:port).
	_, err := normalizeAddresses([]string{"::1:8080"}, nil)
	if err == nil {
		t.Fatal("expected error for unbracketed IPv6 with port-like tail; got nil")
	}
	if !strings.Contains(err.Error(), "must be bracketed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNormalizeAddresses_SchemePrefixedAddresses(t *testing.T) {
	tests := []struct {
		input string
	}{
		{"http://example.com:8080"},
		{"https://secure.example.com"},
		{"unix:/var/run/service.sock"},
		{"dns:///my-service"},
	}
	for _, tc := range tests {
		addrs, err := normalizeAddresses([]string{tc.input}, nil)
		if err != nil {
			t.Errorf("normalizeAddresses([%q]) unexpected error: %v", tc.input, err)
			continue
		}
		if len(addrs) != 1 {
			t.Errorf("expected 1 address for %q; got %d", tc.input, len(addrs))
			continue
		}
		// scheme-prefixed addresses are passed through as-is (no lowercasing)
		if addrs[0].Addr != tc.input {
			t.Errorf("addrs[0].Addr = %q; want %q (passthrough)", addrs[0].Addr, tc.input)
		}
	}
}

func TestNormalizeAddresses_SchemePrefixedDuplicatesAreRemoved(t *testing.T) {
	addrs, err := normalizeAddresses([]string{
		"http://example.com:8080",
		"http://example.com:8080",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 1 {
		t.Errorf("expected 1 address after dedup; got %d", len(addrs))
	}
}

func TestNormalizeAddresses_BareIPv4WithoutPortReturnsError(t *testing.T) {
	_, err := normalizeAddresses([]string{"192.168.1.1"}, nil)
	if err == nil {
		t.Fatal("expected error for bare IPv4 without port; got nil")
	}
	if !strings.Contains(err.Error(), "missing port") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestNormalizeAddresses_BareIPv4WithPortSucceeds(t *testing.T) {
	addrs, err := normalizeAddresses([]string{"192.168.1.1:9090"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("expected 1 address; got %d", len(addrs))
	}
	if addrs[0].Addr != "192.168.1.1:9090" {
		t.Errorf("addrs[0].Addr = %q; want %q", addrs[0].Addr, "192.168.1.1:9090")
	}
}

func TestNormalizeAddresses_InvalidPortReturnsError(t *testing.T) {
	_, err := normalizeAddresses([]string{"host:0"}, nil)
	if err == nil {
		t.Fatal("expected error for port 0; got nil")
	}

	_, err = normalizeAddresses([]string{"host:99999"}, nil)
	if err == nil {
		t.Fatal("expected error for port 99999; got nil")
	}

	_, err = normalizeAddresses([]string{"host:abc"}, nil)
	if err == nil {
		t.Fatal("expected error for non-numeric port; got nil")
	}
}

func TestNormalizeAddresses_BlankEntriesAreSkipped(t *testing.T) {
	addrs, err := normalizeAddresses([]string{"", "host:80", "  ", "host2:90"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 2 {
		t.Errorf("expected 2 addresses (blanks skipped); got %d", len(addrs))
	}
}

func TestNormalizeAddresses_MixedValidEntries(t *testing.T) {
	addrs, err := normalizeAddresses([]string{
		"host-a:80",
		"http://example.com",
		"[::1]:443",
	}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 3 {
		t.Errorf("expected 3 addresses; got %d", len(addrs))
	}
}

func TestNormalizeAddresses_HostColonMissingPortReturnsError(t *testing.T) {
	// "host:" has a colon but no port -- net.SplitHostPort returns empty port
	_, err := normalizeAddresses([]string{"host:"}, nil)
	if err == nil {
		t.Fatal("expected error for 'host:' (missing port); got nil")
	}
}

func TestNormalizeAddresses_IPv6FullFormBracketed(t *testing.T) {
	addrs, err := normalizeAddresses([]string{"[2001:db8::1]:8080"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("expected 1 address; got %d", len(addrs))
	}
	if addrs[0].Addr != "[2001:db8::1]:8080" {
		t.Errorf("addrs[0].Addr = %q; want %q", addrs[0].Addr, "[2001:db8::1]:8080")
	}
}
