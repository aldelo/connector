package circuitbreaker

import (
	"context"
	"testing"

	data "github.com/aldelo/common/wrapper/zap"
)

// mockCircuitBreaker is a minimal stub that satisfies CircuitBreakerIFace.
// It proves the interface is coherently defined and implementable.
type mockCircuitBreaker struct{}

func (m *mockCircuitBreaker) Exec(
	async bool,
	runFn func(dataIn interface{}, ctx ...context.Context) (dataOut interface{}, err error),
	fallbackFn func(dataIn interface{}, errIn error, ctx ...context.Context) (dataOut interface{}, err error),
	dataIn interface{},
) (interface{}, error) {
	return nil, nil
}

func (m *mockCircuitBreaker) ExecWithContext(
	async bool,
	ctx context.Context,
	runFn func(dataIn interface{}, ctx ...context.Context) (dataOut interface{}, err error),
	fallbackFn func(dataIn interface{}, errIn error, ctx ...context.Context) (dataOut interface{}, err error),
	dataIn interface{},
) (interface{}, error) {
	return nil, nil
}

func (m *mockCircuitBreaker) Reset() {}

func (m *mockCircuitBreaker) Update(
	timeout int,
	maxConcurrentRequests int,
	requestVolumeThreshold int,
	sleepWindow int,
	errorPercentThreshold int,
	logger *data.ZapLog,
) error {
	return nil
}

func (m *mockCircuitBreaker) Disable(b bool) {}

// TestCircuitBreakerIFace_Implementable verifies that CircuitBreakerIFace is
// a coherent interface definition that a concrete type can satisfy.
// This is a compile-time + runtime check: the assignment would fail at compile
// time if the mock's method signatures drifted from the interface.
func TestCircuitBreakerIFace_Implementable(t *testing.T) {
	var iface CircuitBreakerIFace = &mockCircuitBreaker{}
	if iface == nil {
		t.Fatal("expected non-nil CircuitBreakerIFace implementation")
	}
}
