package plugins

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
	"context"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// NewHystrixGoPlugin tests
// ---------------------------------------------------------------------------

func TestNewHystrixGoPlugin_EmptyCommandName(t *testing.T) {
	p, err := NewHystrixGoPlugin("", 0, 0, 0, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error for empty command name, got nil")
	}
	if p != nil {
		t.Fatalf("expected nil plugin, got %v", p)
	}
	if !strings.Contains(err.Error(), "Command Name is Required") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestNewHystrixGoPlugin_WhitespaceCommandName(t *testing.T) {
	p, err := NewHystrixGoPlugin("   ", 0, 0, 0, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error for whitespace-only command name, got nil")
	}
	if p != nil {
		t.Fatalf("expected nil plugin, got %v", p)
	}
}

func TestNewHystrixGoPlugin_ValidDefaults(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-new-valid-defaults", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil plugin")
	}
	if p.hystrixGo == nil {
		t.Fatal("expected non-nil internal hystrixGo")
	}
	// Verify defaults were applied
	if p.hystrixGo.TimeOut != 1000 {
		t.Errorf("expected default timeout 1000, got %d", p.hystrixGo.TimeOut)
	}
	if p.hystrixGo.MaxConcurrentRequests != 10 {
		t.Errorf("expected default MaxConcurrentRequests 10, got %d", p.hystrixGo.MaxConcurrentRequests)
	}
	if p.hystrixGo.RequestVolumeThreshold != 20 {
		t.Errorf("expected default RequestVolumeThreshold 20, got %d", p.hystrixGo.RequestVolumeThreshold)
	}
	if p.hystrixGo.SleepWindow != 5000 {
		t.Errorf("expected default SleepWindow 5000, got %d", p.hystrixGo.SleepWindow)
	}
	if p.hystrixGo.ErrorPercentThreshold != 50 {
		t.Errorf("expected default ErrorPercentThreshold 50, got %d", p.hystrixGo.ErrorPercentThreshold)
	}
}

func TestNewHystrixGoPlugin_CustomValues(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-new-custom-values", 2000, 20, 30, 10000, 75, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil plugin")
	}
	if p.hystrixGo.TimeOut != 2000 {
		t.Errorf("expected timeout 2000, got %d", p.hystrixGo.TimeOut)
	}
	if p.hystrixGo.MaxConcurrentRequests != 20 {
		t.Errorf("expected MaxConcurrentRequests 20, got %d", p.hystrixGo.MaxConcurrentRequests)
	}
	if p.hystrixGo.RequestVolumeThreshold != 30 {
		t.Errorf("expected RequestVolumeThreshold 30, got %d", p.hystrixGo.RequestVolumeThreshold)
	}
	if p.hystrixGo.SleepWindow != 10000 {
		t.Errorf("expected SleepWindow 10000, got %d", p.hystrixGo.SleepWindow)
	}
	if p.hystrixGo.ErrorPercentThreshold != 75 {
		t.Errorf("expected ErrorPercentThreshold 75, got %d", p.hystrixGo.ErrorPercentThreshold)
	}
}

func TestNewHystrixGoPlugin_NegativeValues_DefaultsApplied(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-new-negative-vals", -1, -5, -10, -100, -50, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil plugin")
	}
	if p.hystrixGo.TimeOut != 1000 {
		t.Errorf("expected default timeout 1000 for negative input, got %d", p.hystrixGo.TimeOut)
	}
	if p.hystrixGo.MaxConcurrentRequests != 10 {
		t.Errorf("expected default MaxConcurrentRequests 10 for negative input, got %d", p.hystrixGo.MaxConcurrentRequests)
	}
}

// ---------------------------------------------------------------------------
// Exec tests
// ---------------------------------------------------------------------------

func TestExec_NilHystrixGo(t *testing.T) {
	p := &HystrixGoPlugin{hystrixGo: nil}
	_, err := p.Exec(false, func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
		return "ok", nil
	}, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil hystrixGo")
	}
	if !strings.Contains(err.Error(), "Not Initialized") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestExec_NilRunFn(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-exec-nil-runfn", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	_, err = p.Exec(false, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil runFn")
	}
	if !strings.Contains(err.Error(), "runFn Function Is Nil") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestExec_SyncSuccess(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-exec-sync-success", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	result, err := p.Exec(false,
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			return "hello", nil
		},
		nil, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello" {
		t.Fatalf("expected 'hello', got %v", result)
	}
}

func TestExec_SyncWithDataIn(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-exec-sync-datain", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	result, err := p.Exec(false,
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			s, ok := dataIn.(string)
			if !ok {
				return nil, fmt.Errorf("expected string dataIn")
			}
			return "echo:" + s, nil
		},
		nil, "world")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "echo:world" {
		t.Fatalf("expected 'echo:world', got %v", result)
	}
}

func TestExec_SyncRunFailsWithFallback(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-exec-sync-fallback", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	result, err := p.Exec(false,
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			return nil, fmt.Errorf("primary failed")
		},
		func(dataIn interface{}, errIn error, ctx ...context.Context) (interface{}, error) {
			return "fallback-data", nil
		},
		nil)

	if err != nil {
		t.Fatalf("unexpected error when fallback succeeds: %v", err)
	}
	if result != "fallback-data" {
		t.Fatalf("expected 'fallback-data', got %v", result)
	}
}

func TestExec_SyncRunFailsNoFallback(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-exec-sync-nofallback", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	_, err = p.Exec(false,
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			return nil, fmt.Errorf("primary failed")
		},
		nil, nil)

	if err == nil {
		t.Fatal("expected error when runFn fails with no fallback")
	}
}

// ---------------------------------------------------------------------------
// ExecWithContext tests
// ---------------------------------------------------------------------------

func TestExecWithContext_NilHystrixGo(t *testing.T) {
	p := &HystrixGoPlugin{hystrixGo: nil}
	_, err := p.ExecWithContext(false, context.Background(),
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			return "ok", nil
		}, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil hystrixGo")
	}
	if !strings.Contains(err.Error(), "Not Initialized") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestExecWithContext_NilCtx(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-execctx-nil-ctx", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	_, err = p.ExecWithContext(false, nil,
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			return "ok", nil
		}, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil context")
	}
	if !strings.Contains(err.Error(), "ctx Context Is Nil") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestExecWithContext_NilRunFn(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-execctx-nil-runfn", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	_, err = p.ExecWithContext(false, context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for nil runFn")
	}
	if !strings.Contains(err.Error(), "runFn Function Is Nil") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestExecWithContext_SyncSuccess(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-execctx-sync-ok", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	result, err := p.ExecWithContext(false, context.Background(),
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			return "ctx-hello", nil
		}, nil, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "ctx-hello" {
		t.Fatalf("expected 'ctx-hello', got %v", result)
	}
}

func TestExecWithContext_CancelledCtx_NoFallback(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-execctx-cancel-nofb", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = p.ExecWithContext(false, ctx,
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			return "should-not-reach", nil
		}, nil, nil)

	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if err != context.Canceled {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestExecWithContext_CancelledCtx_WithFallback(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-execctx-cancel-fb", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result, err := p.ExecWithContext(false, ctx,
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			return "should-not-reach", nil
		},
		func(dataIn interface{}, errIn error, ctx ...context.Context) (interface{}, error) {
			return "fallback-on-cancel", nil
		},
		nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "fallback-on-cancel" {
		t.Fatalf("expected 'fallback-on-cancel', got %v", result)
	}
}

func TestExecWithContext_RunFailsWithFallback(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-execctx-fail-fb", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	result, err := p.ExecWithContext(false, context.Background(),
		func(dataIn interface{}, ctx ...context.Context) (interface{}, error) {
			return nil, fmt.Errorf("ctx primary failed")
		},
		func(dataIn interface{}, errIn error, ctx ...context.Context) (interface{}, error) {
			return "ctx-fallback-data", nil
		},
		nil)

	if err != nil {
		t.Fatalf("unexpected error when fallback succeeds: %v", err)
	}
	if result != "ctx-fallback-data" {
		t.Fatalf("expected 'ctx-fallback-data', got %v", result)
	}
}

// ---------------------------------------------------------------------------
// Reset tests
// ---------------------------------------------------------------------------

func TestReset_NilHystrixGo_NoPanic(t *testing.T) {
	p := &HystrixGoPlugin{hystrixGo: nil}
	// Should not panic
	p.Reset()
}

func TestReset_ValidPlugin(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-reset-valid", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	// Should not panic
	p.Reset()
}

// ---------------------------------------------------------------------------
// Update tests
// ---------------------------------------------------------------------------

func TestUpdate_NilHystrixGo(t *testing.T) {
	p := &HystrixGoPlugin{hystrixGo: nil}
	err := p.Update(1000, 10, 20, 5000, 50, nil)
	if err == nil {
		t.Fatal("expected error for nil hystrixGo")
	}
	if !strings.Contains(err.Error(), "Not Initialized") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestUpdate_ValidPlugin(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-update-valid", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	err = p.Update(3000, 15, 25, 8000, 60, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify fields were updated
	if p.hystrixGo.TimeOut != 3000 {
		t.Errorf("expected timeout 3000 after update, got %d", p.hystrixGo.TimeOut)
	}
	if p.hystrixGo.MaxConcurrentRequests != 15 {
		t.Errorf("expected MaxConcurrentRequests 15 after update, got %d", p.hystrixGo.MaxConcurrentRequests)
	}
	if p.hystrixGo.RequestVolumeThreshold != 25 {
		t.Errorf("expected RequestVolumeThreshold 25 after update, got %d", p.hystrixGo.RequestVolumeThreshold)
	}
	if p.hystrixGo.SleepWindow != 8000 {
		t.Errorf("expected SleepWindow 8000 after update, got %d", p.hystrixGo.SleepWindow)
	}
	if p.hystrixGo.ErrorPercentThreshold != 60 {
		t.Errorf("expected ErrorPercentThreshold 60 after update, got %d", p.hystrixGo.ErrorPercentThreshold)
	}
}

func TestUpdate_ZeroValuesPreservePrior(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-update-zero-preserve", 2000, 20, 30, 10000, 75, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	// Update with zero values — the Update method only applies positive values
	err = p.Update(0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Original values should be preserved since Update skips zero/negative
	if p.hystrixGo.TimeOut != 2000 {
		t.Errorf("expected timeout 2000 preserved, got %d", p.hystrixGo.TimeOut)
	}
	if p.hystrixGo.MaxConcurrentRequests != 20 {
		t.Errorf("expected MaxConcurrentRequests 20 preserved, got %d", p.hystrixGo.MaxConcurrentRequests)
	}
}

// ---------------------------------------------------------------------------
// Disable tests
// ---------------------------------------------------------------------------

func TestDisable_NilHystrixGo_NoPanic(t *testing.T) {
	p := &HystrixGoPlugin{hystrixGo: nil}
	// Should not panic
	p.Disable(true)
	p.Disable(false)
}

func TestDisable_Enable(t *testing.T) {
	p, err := NewHystrixGoPlugin("test-disable-enable", 0, 0, 0, 0, 0, nil)
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	p.Disable(true)
	if !p.hystrixGo.DisableCircuitBreaker {
		t.Error("expected DisableCircuitBreaker to be true after Disable(true)")
	}

	p.Disable(false)
	if p.hystrixGo.DisableCircuitBreaker {
		t.Error("expected DisableCircuitBreaker to be false after Disable(false)")
	}
}
