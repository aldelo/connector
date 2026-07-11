package client

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
	"encoding/json"
	"testing"
)

// The retry-policy fragment feeds gRPC's WithDefaultServiceConfig alongside the round_robin LB
// policy. gRPC silently ignores an invalid service config, so the fragment must always parse,
// carry a maxAttempts within gRPC's [2,5] range, and retry ONLY UNAVAILABLE (so app-level
// errors like NOT_FOUND are never retried).

type parsedRetryServiceConfig struct {
	MethodConfig []struct {
		Name        []map[string]any `json:"name"`
		RetryPolicy struct {
			MaxAttempts          int      `json:"maxAttempts"`
			InitialBackoff       string   `json:"initialBackoff"`
			MaxBackoff           string   `json:"maxBackoff"`
			BackoffMultiplier    float64  `json:"backoffMultiplier"`
			RetryableStatusCodes []string `json:"retryableStatusCodes"`
		} `json:"retryPolicy"`
	} `json:"methodConfig"`
}

func parseRetryConfig(t *testing.T, maxAttempts uint) parsedRetryServiceConfig {
	t.Helper()
	wrapped := "{" + retryPolicyServiceConfigFragment(maxAttempts) + "}"
	var parsed parsedRetryServiceConfig
	if err := json.Unmarshal([]byte(wrapped), &parsed); err != nil {
		t.Fatalf("retry fragment for maxAttempts=%d is not valid JSON: %v (wrapped=%q)", maxAttempts, err, wrapped)
	}
	if len(parsed.MethodConfig) != 1 {
		t.Fatalf("expected 1 methodConfig entry, got %d", len(parsed.MethodConfig))
	}
	return parsed
}

func TestRetryPolicyServiceConfigFragment_RetriesOnlyUnavailable(t *testing.T) {
	parsed := parseRetryConfig(t, 3)
	codes := parsed.MethodConfig[0].RetryPolicy.RetryableStatusCodes
	if len(codes) != 1 || codes[0] != "UNAVAILABLE" {
		t.Fatalf("retryableStatusCodes = %v, want exactly [UNAVAILABLE]", codes)
	}
}

func TestRetryPolicyServiceConfigFragment_AppliesToAllMethods(t *testing.T) {
	parsed := parseRetryConfig(t, 3)
	names := parsed.MethodConfig[0].Name
	if len(names) != 1 || len(names[0]) != 0 {
		t.Fatalf("name selector = %v, want a single empty object {} (all methods)", names)
	}
}

func TestRetryPolicyServiceConfigFragment_ClampsMaxAttempts(t *testing.T) {
	cases := map[uint]int{
		0:   2, // below gRPC minimum -> clamp up to 2
		1:   2, // below gRPC minimum -> clamp up to 2
		2:   2,
		3:   3,
		5:   5,
		9:   5, // above gRPC maximum -> clamp down to 5
		100: 5,
	}
	for in, want := range cases {
		parsed := parseRetryConfig(t, in)
		if got := parsed.MethodConfig[0].RetryPolicy.MaxAttempts; got != want {
			t.Errorf("maxAttempts(in=%d) = %d, want %d", in, got, want)
		}
	}
}

func TestRetryPolicyServiceConfigFragment_HasBackoffFields(t *testing.T) {
	parsed := parseRetryConfig(t, 3)
	rp := parsed.MethodConfig[0].RetryPolicy
	if rp.InitialBackoff == "" || rp.MaxBackoff == "" {
		t.Fatalf("initialBackoff/maxBackoff must be set, got initial=%q max=%q", rp.InitialBackoff, rp.MaxBackoff)
	}
	if rp.BackoffMultiplier <= 1.0 {
		t.Fatalf("backoffMultiplier = %v, want > 1.0", rp.BackoffMultiplier)
	}
}

// The real dial-time assembly composes round_robin + health check + retry policy into one
// service config; verify all three coexist as valid JSON (a broken combination would silently
// disable load balancing AND health checking AND retries).
func TestRetryPolicyServiceConfigFragment_ComposesWithLbAndHealthCheck(t *testing.T) {
	lb := `"loadBalancingConfig":[{"round_robin":{}}]`
	full := "{" + lb + ", " +
		healthCheckServiceConfigFragment("dal-config-read-get") + ", " +
		retryPolicyServiceConfigFragment(3) + "}"

	var parsed struct {
		LoadBalancingConfig []map[string]json.RawMessage `json:"loadBalancingConfig"`
		HealthCheckConfig   struct {
			ServiceName string `json:"serviceName"`
		} `json:"healthCheckConfig"`
		MethodConfig []json.RawMessage `json:"methodConfig"`
	}
	if err := json.Unmarshal([]byte(full), &parsed); err != nil {
		t.Fatalf("composed service config is not valid JSON: %v (full=%q)", err, full)
	}
	if len(parsed.LoadBalancingConfig) != 1 {
		t.Fatalf("expected round_robin LB entry, got %d entries", len(parsed.LoadBalancingConfig))
	}
	if parsed.HealthCheckConfig.ServiceName != "dal-config-read-get" {
		t.Fatalf("healthCheck serviceName = %q, want dal-config-read-get", parsed.HealthCheckConfig.ServiceName)
	}
	if len(parsed.MethodConfig) != 1 {
		t.Fatalf("expected 1 methodConfig entry, got %d", len(parsed.MethodConfig))
	}
}
