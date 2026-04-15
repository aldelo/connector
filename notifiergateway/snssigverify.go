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
	"container/list"
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

// certCacheMax caps the in-process SNS signing cert cache at a small
// LRU.
//
// SP-008 P2-CONN-2 (2026-04-15): the prior implementation used an
// unbounded sync.Map. The allowlist pattern
// `^https://sns\.[a-z0-9-]+\.amazonaws\.com/` already bounds the
// reachable host set to ~30 AWS regions, so real-world growth is tiny
// — but "tiny but unbounded" is still unbounded, and a future
// allowlist loosening or a misconfigured regex could quietly uncap the
// cache. Pinning the cap here turns the failure mode from "slow growth
// until OOM" into "oldest entry evicted on LRU miss, visible in the
// process memory profile as a stable footprint". 16 entries comfortably
// covers every AWS region plus a few rotation-overlap entries.
const certCacheMax = 16

// lruCertCache is a bounded LRU cache of x509.Certificate values keyed
// by cert URL. A sync.Mutex guards both the map and the ordering list
// so Get/Put are atomic; read traffic is expected to be vastly higher
// than rotation-driven writes, but the lock cost is negligible at ~1-2
// fetches per rotation cycle.
type lruCertCache struct {
	mu    sync.Mutex
	items map[string]*list.Element
	order *list.List // MRU at front, LRU at back
	cap   int
}

type certCacheEntry struct {
	url  string
	cert *x509.Certificate
}

// newLRUCertCache allocates an empty cache with the given capacity.
// A non-positive cap disables the cache entirely (every Get misses,
// every Put is a no-op); production callers should always pass a
// positive cap such as certCacheMax.
func newLRUCertCache(cap int) *lruCertCache {
	return &lruCertCache{
		items: make(map[string]*list.Element, cap),
		order: list.New(),
		cap:   cap,
	}
}

// Get returns the cached certificate for url, promoting the entry to
// MRU on a hit. ok is false if the url is not cached.
func (c *lruCertCache) Get(url string) (*x509.Certificate, bool) {
	if c.cap <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[url]; ok {
		c.order.MoveToFront(elem)
		return elem.Value.(*certCacheEntry).cert, true
	}
	return nil, false
}

// Put inserts or refreshes a cache entry. If the cache is at capacity
// and the url is not already present, the least-recently-used entry
// is evicted to make room.
func (c *lruCertCache) Put(url string, cert *x509.Certificate) {
	if c.cap <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, ok := c.items[url]; ok {
		elem.Value.(*certCacheEntry).cert = cert
		c.order.MoveToFront(elem)
		return
	}
	if c.order.Len() >= c.cap {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*certCacheEntry).url)
		}
	}
	elem := c.order.PushFront(&certCacheEntry{url: url, cert: cert})
	c.items[url] = elem
}

// Reset drops all cached entries. Called by tests; safe to call at
// runtime but intended as a test-only knob.
func (c *lruCertCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items = make(map[string]*list.Element, c.cap)
	c.order = list.New()
}

// certCache memoizes fetched SNS signing certificates in a bounded
// LRU keyed by URL. SNS signing certs rotate infrequently; process
// restart is the expected full-invalidation event.
var certCache = newLRUCertCache(certCacheMax)

// maxCertBodyBytes caps the cert response body size to prevent a hostile
// upstream from exhausting memory. 1 MiB is ~500x the size of a real cert.
const maxCertBodyBytes = 1 << 20

// certHTTPClient is the package-level HTTP client used by
// defaultFetchSNSCert. SP-008 P1-CONN-4 (2026-04-15): hoisted from a
// per-call &http.Client{} so successive cert fetches on cache miss or
// rotation can reuse the underlying Transport connection pool and
// amortize TLS handshake cost across calls. The 10s timeout is
// preserved from the original per-call construction. This client is
// dedicated to cert fetching so it cannot compete with unrelated HTTP
// traffic for connection-pool slots.
var certHTTPClient = &http.Client{Timeout: 10 * time.Second}

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
	if cert, ok := certCache.Get(certURL); ok {
		return cert, nil
	}

	// Defense-in-depth — re-check the host allowlist inside the fetcher.
	if !isValidSNSUrl(certURL) {
		return nil, fmt.Errorf("cert URL host not allowed: %s", certURL)
	}

	resp, err := certHTTPClient.Get(certURL)
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

	certCache.Put(certURL, cert)
	return cert, nil
}
