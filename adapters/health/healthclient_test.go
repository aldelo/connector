package health

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/health/grpc_health_v1"
)

// TestHealthClient_Check_NilReceiver verifies that a nil receiver returns an error
func TestHealthClient_Check_NilReceiver(t *testing.T) {
	var h *HealthClient
	status, err := h.Check("test")
	if err == nil {
		t.Error("expected error for nil receiver")
	}
	if status != grpc_health_v1.HealthCheckResponse_UNKNOWN {
		t.Errorf("expected UNKNOWN status, got %v", status)
	}
}

// TestHealthClient_Check_NilClient verifies that a nil internal client returns an error
func TestHealthClient_Check_NilClient(t *testing.T) {
	h := &HealthClient{hcClient: nil}
	status, err := h.Check("test")
	if err == nil {
		t.Error("expected error for nil hcClient")
	}
	if status != grpc_health_v1.HealthCheckResponse_UNKNOWN {
		t.Errorf("expected UNKNOWN status, got %v", status)
	}
}

// TestHealthClient_CheckContext_NegativeTimeout verifies that negative timeout returns an error
func TestHealthClient_CheckContext_NegativeTimeout(t *testing.T) {
	h := &HealthClient{} // Would need mock client for full test
	_, err := h.CheckContext(context.Background(), "test", -1*time.Second)
	if err == nil {
		t.Error("expected error for negative timeout")
	}
}

// TestHealthClient_CheckContext_MultipleTimeouts verifies that multiple timeout arguments return an error
func TestHealthClient_CheckContext_MultipleTimeouts(t *testing.T) {
	h := &HealthClient{} // Would need mock client for full test
	_, err := h.CheckContext(context.Background(), "test", 1*time.Second, 2*time.Second)
	if err == nil {
		t.Error("expected error for multiple timeout arguments")
	}
}

// TestNewHealthClient_NilConnection verifies that nil connection returns an error
func TestNewHealthClient_NilConnection(t *testing.T) {
	client, err := NewHealthClient(nil)
	if err == nil {
		t.Error("expected error for nil connection")
	}
	if client != nil {
		t.Error("expected nil client when error occurs")
	}
}
