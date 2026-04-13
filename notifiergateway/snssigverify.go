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

// AWS SNS message signature verification.
//
// This file implements verification of SignatureVersion 1 (SHA1) and 2
// (SHA256) signed SNS messages per the AWS SNS documentation:
//
//	https://docs.aws.amazon.com/sns/latest/dg/sns-verify-signature-of-message.html
//
// Security properties:
//  1. SSRF defense — the SigningCertURL is matched against an allowlist
//     (isValidSNSUrl) BEFORE any HTTP fetch. The allowlist is re-applied
//     inside defaultFetchSNSCert so that if a future caller bypasses the
//     outer check the fetcher still refuses off-AWS URLs.
//  2. Replay-safe cert caching — certificates are cached in process by
//     URL; AWS rotates SNS signing certs infrequently and restart is the
//     expected invalidation path.
//  3. Testability — snsCertFetcher is a package-level function variable so
//     tests can inject a stub that returns an in-memory x509 certificate
//     without touching the network.

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// snsCertFetcher resolves a signing certificate URL into a parsed x509
// certificate. Tests override this variable to inject a stub fetcher.
var snsCertFetcher = defaultFetchSNSCert

// certCache memoizes fetched SNS signing certificates keyed by URL.
// SNS signing certs rotate infrequently; process restart is the expected
// invalidation event.
var certCache sync.Map

// maxCertBodyBytes caps the cert response body size to prevent a hostile
// upstream from exhausting memory. 1 MiB is ~500x the size of a real cert.
const maxCertBodyBytes = 1 << 20

// verifySNSConfirmationSignature verifies the signature of an SNS
// SubscriptionConfirmation / UnsubscribeConfirmation message.
func verifySNSConfirmationSignature(confirm *confirmation) error {
	if confirm == nil {
		return fmt.Errorf("confirmation payload is nil")
	}
	payload := buildConfirmationSigningPayload(confirm)
	return verifyAWSSNSSignature(payload, confirm.Signature, confirm.SignatureVersion, confirm.SigningCertURL)
}

// verifySNSNotificationSignature verifies the signature of an SNS
// Notification message.
func verifySNSNotificationSignature(notify *notification) error {
	if notify == nil {
		return fmt.Errorf("notification payload is nil")
	}
	payload := buildNotificationSigningPayload(notify)
	return verifyAWSSNSSignature(payload, notify.Signature, notify.SignatureVersion, notify.SigningCertURL)
}

// verifyAWSSNSSignature is the low-level verifier. It:
//  1. Validates that payload / signature / cert URL are present.
//  2. Host-allowlists the cert URL (SSRF pre-gate).
//  3. Resolves the cert via snsCertFetcher (production: HTTP + cache).
//  4. Decodes the base64 signature.
//  5. Computes the digest per SignatureVersion.
//  6. Verifies RSA PKCS1v15 against the cert's public key.
func verifyAWSSNSSignature(payload, signature, signatureVersion, signingCertURL string) error {
	if len(payload) == 0 {
		return fmt.Errorf("empty signing payload")
	}
	if len(signature) == 0 {
		return fmt.Errorf("missing signature")
	}
	if len(signingCertURL) == 0 {
		return fmt.Errorf("missing signing cert URL")
	}

	// SSRF pre-gate — host allowlist check BEFORE any network activity.
	if !isValidSNSUrl(signingCertURL) {
		return fmt.Errorf("signing cert URL host not allowed")
	}

	cert, err := snsCertFetcher(signingCertURL)
	if err != nil {
		return fmt.Errorf("fetch signing cert: %w", err)
	}

	pubKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("signing cert public key is not RSA")
	}

	sigBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	var hashFunc crypto.Hash
	var digest []byte
	switch signatureVersion {
	case "", "1":
		// AWS default is SignatureVersion 1 (SHA1) when the field is absent.
		h := sha1.Sum([]byte(payload))
		digest = h[:]
		hashFunc = crypto.SHA1
	case "2":
		h := sha256.Sum256([]byte(payload))
		digest = h[:]
		hashFunc = crypto.SHA256
	default:
		return fmt.Errorf("unsupported SignatureVersion: %s", signatureVersion)
	}

	if err := rsa.VerifyPKCS1v15(pubKey, hashFunc, digest, sigBytes); err != nil {
		return fmt.Errorf("signature verification failed: %w", err)
	}
	return nil
}

// defaultFetchSNSCert is the production cert fetcher. It:
//   - Checks the in-process cache first.
//   - Re-applies the host allowlist as defense-in-depth.
//   - HTTP GETs the cert with a 10s timeout.
//   - Caps the body at maxCertBodyBytes.
//   - PEM-decodes and x509-parses.
//   - Caches on success.
func defaultFetchSNSCert(certURL string) (*x509.Certificate, error) {
	if cached, ok := certCache.Load(certURL); ok {
		if cert, ok := cached.(*x509.Certificate); ok {
			return cert, nil
		}
	}

	// Defense-in-depth — re-check the host allowlist inside the fetcher.
	if !isValidSNSUrl(certURL) {
		return nil, fmt.Errorf("cert URL host not allowed: %s", certURL)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(certURL)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCertBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	block, _ := pem.Decode(body)
	if block == nil {
		return nil, fmt.Errorf("invalid PEM in cert response")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse x509: %w", err)
	}

	certCache.Store(certURL, cert)
	return cert, nil
}
