package notifiergateway

/*
 * Copyright 2020-2023 Aldelo, LP
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

// SNS signature verification tests.
//
// Hermetic strategy: every test generates a fresh in-memory RSA keypair +
// self-signed x509 cert, injects a stub snsCertFetcher that returns the
// in-process cert, signs the canonical SNS payload with the in-memory
// private key, and verifies end-to-end. No disk fixtures, no network.

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"log"
	"math/big"
	"strings"
	"testing"
	"time"
)

// testSigner bundles an RSA keypair + x509 cert used to sign SNS payloads
// inside tests. Matches the shape AWS SNS produces: RSA public key cert
// served from a host under sns.<region>.amazonaws.com.
type testSigner struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
}

// newTestSigner generates a fresh 2048-bit RSA keypair and wraps it in a
// self-signed x509 certificate. 2048 bits is the minimum AWS uses for SNS
// signing certs.
func newTestSigner(t *testing.T) *testSigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sns.us-east-1.amazonaws.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create x509 cert: %v", err)
	}
	cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		t.Fatalf("parse x509 cert: %v", err)
	}
	return &testSigner{key: key, cert: cert}
}

// signV1 produces a base64-encoded SHA1+RSA PKCS1v15 signature over payload.
func (s *testSigner) signV1(t *testing.T, payload string) string {
	t.Helper()
	h := sha1.Sum([]byte(payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA1, h[:])
	if err != nil {
		t.Fatalf("sign v1: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// signV2 produces a base64-encoded SHA256+RSA PKCS1v15 signature over payload.
func (s *testSigner) signV2(t *testing.T, payload string) string {
	t.Helper()
	h := sha256.Sum256([]byte(payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatalf("sign v2: %v", err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

// withStubFetcher installs a package-level stub fetcher for the duration of
// the current test. It also resets certCache so prior test state cannot leak.
// Restoration is registered with t.Cleanup so tests stay independent.
func withStubFetcher(t *testing.T, s *testSigner) {
	t.Helper()
	prev := snsCertFetcher
	snsCertFetcher = func(url string) (*x509.Certificate, error) {
		return s.cert, nil
	}
	certCache.Reset()
	t.Cleanup(func() {
		snsCertFetcher = prev
		certCache.Reset()
	})
}

// -------------------------------------------------------------------------
// Test 1 — valid SignatureVersion 1 confirmation verifies.
// -------------------------------------------------------------------------
func TestVerifySNSConfirmationSignature_V1_Valid(t *testing.T) {
	signer := newTestSigner(t)
	withStubFetcher(t, signer)

	confirm := &confirmation{
		Type:             "SubscriptionConfirmation",
		MessageId:        "abc-123",
		Token:            "token-xyz",
		TopicArn:         "arn:aws:sns:us-east-1:123456789012:my-topic",
		Message:          "You have chosen to subscribe",
		SubscribeURL:     "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=foo&Token=bar",
		Timestamp:        "2026-04-13T00:00:00.000Z",
		SignatureVersion: "1",
		SigningCertURL:   "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem",
	}
	payload := buildConfirmationSigningPayload(confirm)
	confirm.Signature = signer.signV1(t, payload)

	if err := verifySNSConfirmationSignature(confirm); err != nil {
		t.Fatalf("expected valid v1 signature to verify, got error: %v", err)
	}
}

// -------------------------------------------------------------------------
// Test 2 — valid SignatureVersion 2 notification verifies.
// -------------------------------------------------------------------------
func TestVerifySNSNotificationSignature_V2_Valid(t *testing.T) {
	signer := newTestSigner(t)
	withStubFetcher(t, signer)

	notify := &notification{
		Type:             "Notification",
		MessageId:        "msg-456",
		TopicArn:         "arn:aws:sns:us-east-1:123456789012:my-topic",
		Subject:          "Test Subject",
		Message:          `{"event":"ping"}`,
		Timestamp:        "2026-04-13T00:00:00.000Z",
		SignatureVersion: "2",
		SigningCertURL:   "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem",
	}
	payload := buildNotificationSigningPayload(notify)
	notify.Signature = signer.signV2(t, payload)

	if err := verifySNSNotificationSignature(notify); err != nil {
		t.Fatalf("expected valid v2 signature to verify, got error: %v", err)
	}
}

// -------------------------------------------------------------------------
// Test 3 — forged signature is rejected.
// Constructs a valid payload and cert but replaces the signature with noise.
// -------------------------------------------------------------------------
func TestVerifySNSNotificationSignature_Forged(t *testing.T) {
	signer := newTestSigner(t)
	withStubFetcher(t, signer)

	notify := &notification{
		Type:             "Notification",
		MessageId:        "msg-456",
		TopicArn:         "arn:aws:sns:us-east-1:123456789012:my-topic",
		Message:          "payload",
		Timestamp:        "2026-04-13T00:00:00.000Z",
		SignatureVersion: "1",
		SigningCertURL:   "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem",
		// Base64-decodable garbage that will not match the payload digest.
		Signature: base64.StdEncoding.EncodeToString([]byte("this-is-not-a-valid-rsa-signature-bytes-0123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345678901234567890123456789012345")),
	}

	err := verifySNSNotificationSignature(notify)
	if err == nil {
		t.Fatal("expected forged signature to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "signature verification failed") &&
		!strings.Contains(err.Error(), "decode signature") {
		t.Fatalf("expected signature verification or decode error, got: %v", err)
	}
}

// -------------------------------------------------------------------------
// Test 4 — off-AWS SigningCertURL is rejected before any HTTP fetch.
// This verifies the SSRF pre-gate: even if the stub fetcher would happily
// return a valid cert, the host allowlist runs FIRST and short-circuits.
// -------------------------------------------------------------------------
func TestVerifyAWSSNSSignature_OffAWSCertURL_Rejected(t *testing.T) {
	signer := newTestSigner(t)

	// Install a fetcher that would PASS if called — but we assert it is never
	// reached because the host check runs before the fetcher.
	fetcherCalled := false
	prev := snsCertFetcher
	snsCertFetcher = func(url string) (*x509.Certificate, error) {
		fetcherCalled = true
		return signer.cert, nil
	}
	certCache.Reset()
	t.Cleanup(func() {
		snsCertFetcher = prev
		certCache.Reset()
	})

	evilURLs := []string{
		"https://attacker.example.com/cert.pem",
		"https://sns.us-east-1.amazonaws.com.evil.com/cert.pem",
		"http://sns.us-east-1.amazonaws.com/cert.pem", // http:// (not https://)
		"https://evil.com/sns.us-east-1.amazonaws.com/cert.pem",
	}
	for _, u := range evilURLs {
		err := verifyAWSSNSSignature("payload", "sig", "1", u)
		if err == nil {
			t.Fatalf("expected off-AWS cert URL %q to be rejected, got nil", u)
		}
		if !strings.Contains(err.Error(), "host not allowed") {
			t.Fatalf("expected host-not-allowed error for %q, got: %v", u, err)
		}
	}
	if fetcherCalled {
		t.Fatal("fetcher should NOT be invoked when the host allowlist rejects the URL")
	}
}

// -------------------------------------------------------------------------
// Test 5 — unsupported SignatureVersion is rejected.
// -------------------------------------------------------------------------
func TestVerifyAWSSNSSignature_UnsupportedVersion(t *testing.T) {
	signer := newTestSigner(t)
	withStubFetcher(t, signer)

	// Produce a real v1 signature but pretend it is v99.
	payload := "Type\nNotification\n"
	sig := signer.signV1(t, payload)

	err := verifyAWSSNSSignature(payload, sig, "99",
		"https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem")
	if err == nil {
		t.Fatal("expected unsupported SignatureVersion to be rejected")
	}
	if !strings.Contains(err.Error(), "unsupported SignatureVersion") {
		t.Fatalf("expected unsupported version error, got: %v", err)
	}
}

// -------------------------------------------------------------------------
// Test 6 — defaultFetchSNSCert short-circuits off-AWS URLs before any HTTP.
// This is the defense-in-depth check inside the fetcher itself.
// -------------------------------------------------------------------------
func TestDefaultFetchSNSCert_HostAllowlistDefenseInDepth(t *testing.T) {
	// Reset certCache so a prior test entry cannot satisfy the Load() path.
	certCache.Reset()
	t.Cleanup(func() { certCache.Reset() })

	_, err := defaultFetchSNSCert("https://attacker.example.com/cert.pem")
	if err == nil {
		t.Fatal("expected defaultFetchSNSCert to reject off-AWS host")
	}
	if !strings.Contains(err.Error(), "host not allowed") {
		t.Fatalf("expected host-not-allowed error, got: %v", err)
	}
}

// -------------------------------------------------------------------------
// Sanity — buildConfirmationSigningPayload produces the exact canonical
// format AWS expects. Keeps dead-code rot at bay if the helper is ever
// modified.
// -------------------------------------------------------------------------
func TestBuildConfirmationSigningPayload_CanonicalFormat(t *testing.T) {
	c := &confirmation{
		Type:         "SubscriptionConfirmation",
		MessageId:    "id",
		Token:        "tok",
		TopicArn:     "arn",
		Message:      "msg",
		SubscribeURL: "url",
		Timestamp:    "ts",
	}
	got := buildConfirmationSigningPayload(c)
	want := "Message\nmsg\nMessageId\nid\nSubscribeURL\nurl\nTimestamp\nts\nToken\ntok\nTopicArn\narn\nType\nSubscriptionConfirmation\n"
	if got != want {
		t.Fatalf("canonical confirmation payload mismatch\n got: %q\nwant: %q", got, want)
	}
}

// -------------------------------------------------------------------------
// Sanity — buildNotificationSigningPayload handles optional Subject.
// -------------------------------------------------------------------------
func TestBuildNotificationSigningPayload_OptionalSubject(t *testing.T) {
	// With Subject present
	n1 := &notification{
		Type:      "Notification",
		MessageId: "id",
		TopicArn:  "arn",
		Subject:   "subj",
		Message:   "msg",
		Timestamp: "ts",
	}
	got1 := buildNotificationSigningPayload(n1)
	want1 := "Message\nmsg\nMessageId\nid\nSubject\nsubj\nTimestamp\nts\nTopicArn\narn\nType\nNotification\n"
	if got1 != want1 {
		t.Fatalf("with-subject payload mismatch\n got: %q\nwant: %q", got1, want1)
	}

	// Without Subject — should omit Subject lines entirely
	n2 := &notification{
		Type:      "Notification",
		MessageId: "id",
		TopicArn:  "arn",
		Message:   "msg",
		Timestamp: "ts",
	}
	got2 := buildNotificationSigningPayload(n2)
	want2 := "Message\nmsg\nMessageId\nid\nTimestamp\nts\nTopicArn\narn\nType\nNotification\n"
	if got2 != want2 {
		t.Fatalf("without-subject payload mismatch\n got: %q\nwant: %q", got2, want2)
	}
}

// =========================================================================
// Group A — log-injection regression tests (CodeQL #8/#9/#10/#11)
// =========================================================================

// TestEscapeUserInput_StripsCRLF verifies the escapeUserInput helper strips
// CR/LF/TAB control characters that enable log-forgery attacks (CWE-117).
// This is the foundational unit test: if escapeUserInput works, every call
// site that wraps user input with it is protected.
func TestEscapeUserInput_StripsCRLF(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"newline", "before\nafter", "before after"},
		{"carriage_return", "before\rafter", "before after"},
		{"tab", "before\tafter", "before after"},
		{"crlf", "line1\r\nline2", "line1  line2"},
		{"forged_log_line", "legit\n2026-06-14 WARN fake log entry", "legit 2026-06-14 WARN fake log entry"},
		{"clean_input", "no-control-chars", "no-control-chars"},
		{"empty", "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := escapeUserInput(tc.input)
			if got != tc.want {
				t.Errorf("escapeUserInput(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

// TestLogInjection_InstanceIdEscaped_Regression verifies that logging an
// instanceId containing CR/LF through the escapeUserInput wrapper produces
// output with NO raw control characters. This is the regression test for
// CodeQL alerts #8/#9/#10/#11 (go/log-injection on lines 421/425/437/440).
//
// Strategy: capture log output into a buffer, call escapeUserInput on a
// malicious instanceId, format the log line the same way the handler does,
// and assert no raw CR/LF survives.
func TestLogInjection_InstanceIdEscaped_Regression(t *testing.T) {
	// Simulate the attacker-controlled instanceId containing a forged log line
	maliciousInstanceId := "legit-id\n2026-06-14 CRITICAL: forged entry\rmore-injection"

	// Capture log output
	var buf bytes.Buffer
	origOutput := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(origOutput) })

	// Log the same pattern as lines 421/425/437/440 — but WITH the fix applied
	escaped := escapeUserInput(maliciousInstanceId)
	log.Println("!!! Delete Health Report Service Record For InstanceID '" + escaped + "' From Data Store Failed: some error !!!")

	output := buf.String()
	if strings.Contains(output, "\n2026-06-14 CRITICAL") {
		t.Errorf("log output contains forged newline-injected entry:\n%s", output)
	}
	if strings.ContainsAny(output[strings.Index(output, "InstanceID"):], "\r") {
		t.Errorf("log output contains raw carriage return after InstanceID:\n%s", output)
	}
}

// =========================================================================
// Group B — isValidSNSUrl comprehensive test matrix (CodeQL #36 / SSRF)
// =========================================================================

// TestIsValidSNSUrl_ComprehensiveMatrix validates the SSRF allowlist used to
// gate cert fetches and subscribe/unsubscribe URL requests. It covers
// legitimate AWS regions, case insensitivity, and a broad set of attack
// vectors including credential embedding, port manipulation, subdomain
// tricks, and scheme downgrade.
func TestIsValidSNSUrl_ComprehensiveMatrix(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		// --- Legitimate AWS SNS URLs (MUST accept) ---
		{"us-east-1 cert", "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-xxxx.pem", true},
		{"eu-west-1 cert", "https://sns.eu-west-1.amazonaws.com/SimpleNotificationService-xxxx.pem", true},
		{"ap-southeast-2 cert", "https://sns.ap-southeast-2.amazonaws.com/SimpleNotificationService-xxxx.pem", true},
		{"cn-north-1 cert", "https://sns.cn-north-1.amazonaws.com/SimpleNotificationService-xxxx.pem", true},
		{"us-gov-west-1 cert", "https://sns.us-gov-west-1.amazonaws.com/SimpleNotificationService-xxxx.pem", true},
		{"case insensitive", "https://SNS.us-east-1.AMAZONAWS.COM/SimpleNotificationService-xxxx.pem", true},
		{"mixed case region", "https://sns.US-EAST-1.amazonaws.com/cert.pem", true},
		{"confirm URL with query", "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=foo&Token=bar", true},
		{"deeply nested path", "https://sns.us-east-1.amazonaws.com/a/b/c/d.pem", true},

		// --- Attack vectors (MUST reject) ---
		{"http scheme", "http://sns.us-east-1.amazonaws.com/cert.pem", false},
		{"attacker subdomain suffix", "https://sns.us-east-1.amazonaws.com.evil.com/cert.pem", false},
		{"attacker with path prefix", "https://evil.com/sns.us-east-1.amazonaws.com/cert.pem", false},
		{"userinfo credential embed", "https://sns.us-east-1.amazonaws.com@evil.com/cert.pem", false},
		{"userinfo with password", "https://user:pass@sns.us-east-1.amazonaws.com/cert.pem", false},
		{"non-standard port", "https://sns.us-east-1.amazonaws.com:8443/cert.pem", false},
		{"attacker host plain", "https://attacker.example.com/cert.pem", false},
		{"empty string", "", false},
		{"ftp scheme", "ftp://sns.us-east-1.amazonaws.com/cert.pem", false},
		{"javascript scheme", "javascript:alert(1)//sns.us-east-1.amazonaws.com/", false},
		{"data uri", "data:text/html,<h1>test</h1>", false},
		{"no path", "https://sns.us-east-1.amazonaws.com", false},
		{"double slash trick", "https://evil.com\\@sns.us-east-1.amazonaws.com/cert.pem", false},
		{"port 443 explicit", "https://sns.us-east-1.amazonaws.com:443/cert.pem", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isValidSNSUrl(tc.url)
			if got != tc.want {
				t.Errorf("isValidSNSUrl(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

// TestSanitizeSNSUrl_ReconstructsCleanURL verifies that sanitizeSNSUrl
// returns a reconstructed URL from parsed components (severing the taint
// chain for CodeQL) and that the reconstructed URL is functionally
// equivalent to the input for legitimate AWS SNS URLs.
func TestSanitizeSNSUrl_ReconstructsCleanURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantURL string // only checked when wantOK is true
	}{
		{
			name:    "standard cert URL",
			input:   "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-xxxx.pem",
			wantOK:  true,
			wantURL: "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-xxxx.pem",
		},
		{
			name:    "confirm URL with query",
			input:   "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=foo&Token=bar",
			wantOK:  true,
			wantURL: "https://sns.us-east-1.amazonaws.com/?Action=ConfirmSubscription&TopicArn=foo&Token=bar",
		},
		{
			name:    "mixed case normalized to lowercase host",
			input:   "https://SNS.us-east-1.AMAZONAWS.COM/cert.pem",
			wantOK:  true,
			wantURL: "https://sns.us-east-1.amazonaws.com/cert.pem",
		},
		{
			name:   "attacker subdomain suffix rejected",
			input:  "https://sns.us-east-1.amazonaws.com.evil.com/cert.pem",
			wantOK: false,
		},
		{
			name:   "userinfo rejected",
			input:  "https://user:pass@sns.us-east-1.amazonaws.com/cert.pem",
			wantOK: false,
		},
		{
			name:   "http scheme rejected",
			input:  "http://sns.us-east-1.amazonaws.com/cert.pem",
			wantOK: false,
		},
		{
			name:   "attacker host rejected",
			input:  "https://attacker.example.com/cert.pem",
			wantOK: false,
		},
		{
			name:   "empty rejected",
			input:  "",
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotURL, gotOK := sanitizeSNSUrl(tc.input)
			if gotOK != tc.wantOK {
				t.Errorf("sanitizeSNSUrl(%q) ok = %v, want %v", tc.input, gotOK, tc.wantOK)
				return
			}
			if tc.wantOK && gotURL != tc.wantURL {
				t.Errorf("sanitizeSNSUrl(%q) url = %q, want %q", tc.input, gotURL, tc.wantURL)
			}
		})
	}
}
