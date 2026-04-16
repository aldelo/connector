package model

import "testing"

// ---------------------------------------------------------------------------
// BL-1: Verify default values for endpoint retry config accessors
// ---------------------------------------------------------------------------

func TestGetEndpointRetryDelayMs_Default(t *testing.T) {
	// Package-level var initializes to 250 matching prior hardcoded behavior.
	got := GetEndpointRetryDelayMs()
	if got != 250 {
		t.Errorf("expected default EndpointRetryDelayMs=250, got %d", got)
	}
}

func TestGetEndpointRetryMaxAttempts_Default(t *testing.T) {
	// Package-level var initializes to 3 matching prior hardcoded behavior.
	got := GetEndpointRetryMaxAttempts()
	if got != 3 {
		t.Errorf("expected default EndpointRetryMaxAttempts=3, got %d", got)
	}
}

func TestSetGetEndpointRetryDelayMs_Roundtrip(t *testing.T) {
	// Save original and restore after test
	original := GetEndpointRetryDelayMs()
	defer SetEndpointRetryDelayMs(original)

	SetEndpointRetryDelayMs(500)
	got := GetEndpointRetryDelayMs()
	if got != 500 {
		t.Errorf("expected EndpointRetryDelayMs=500 after Set, got %d", got)
	}
}

func TestSetGetEndpointRetryMaxAttempts_Roundtrip(t *testing.T) {
	// Save original and restore after test
	original := GetEndpointRetryMaxAttempts()
	defer SetEndpointRetryMaxAttempts(original)

	SetEndpointRetryMaxAttempts(5)
	got := GetEndpointRetryMaxAttempts()
	if got != 5 {
		t.Errorf("expected EndpointRetryMaxAttempts=5 after Set, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Verify existing accessors still return expected defaults
// ---------------------------------------------------------------------------

func TestGetRequireSNSSignature_Default(t *testing.T) {
	got := GetRequireSNSSignature()
	if got != true {
		t.Errorf("expected default RequireSNSSignature=true, got %v", got)
	}
}
