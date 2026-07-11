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

// The health-check fragment feeds gRPC's WithDefaultServiceConfig. gRPC SILENTLY ignores an
// invalid service config (health checking then never activates), so the fragment must always
// assemble into valid JSON — even when an operator supplies a service name containing quotes
// or backslashes. These tests lock in both the empty-name (historical) shape and JSON safety.

func TestHealthCheckServiceConfigFragment_EmptyNamePreservesLegacyShape(t *testing.T) {
	got := healthCheckServiceConfigFragment("")
	want := `"healthCheckConfig":{"serviceName":""}`
	if got != want {
		t.Fatalf("empty name fragment = %q, want %q (must match pre-change behavior)", got, want)
	}
}

func TestHealthCheckServiceConfigFragment_NamedService(t *testing.T) {
	got := healthCheckServiceConfigFragment("dal-config-read-get")
	want := `"healthCheckConfig":{"serviceName":"dal-config-read-get"}`
	if got != want {
		t.Fatalf("named fragment = %q, want %q", got, want)
	}
}

// The fragment is wrapped by the caller as `{ <lb>, <fragment> }`. Verify that wrapping
// always yields parseable JSON with the expected serviceName, including hostile inputs.
func TestHealthCheckServiceConfigFragment_AlwaysValidJSON(t *testing.T) {
	names := []string{
		"",
		"simple",
		`has"quote`,
		`has\backslash`,
		"unicode-☃",
		"tab\tinside",
	}

	for _, name := range names {
		fragment := healthCheckServiceConfigFragment(name)
		wrapped := "{" + fragment + "}"

		var parsed struct {
			HealthCheckConfig struct {
				ServiceName string `json:"serviceName"`
			} `json:"healthCheckConfig"`
		}
		if err := json.Unmarshal([]byte(wrapped), &parsed); err != nil {
			t.Fatalf("wrapped service config for name %q is not valid JSON: %v (wrapped=%q)", name, err, wrapped)
		}
		if parsed.HealthCheckConfig.ServiceName != name {
			t.Fatalf("round-tripped serviceName = %q, want %q", parsed.HealthCheckConfig.ServiceName, name)
		}
	}
}

// Combined with the round-robin LB fragment (the real dial-time assembly), the full service
// config must still be valid JSON so BOTH load balancing and health checking take effect.
func TestHealthCheckServiceConfigFragment_ComposesWithLoadBalancerPolicy(t *testing.T) {
	lb := `"loadBalancingConfig":[{"round_robin":{}}]`
	full := "{" + lb + ", " + healthCheckServiceConfigFragment("dal-config-read-get") + "}"

	var parsed struct {
		LoadBalancingConfig []map[string]json.RawMessage `json:"loadBalancingConfig"`
		HealthCheckConfig   struct {
			ServiceName string `json:"serviceName"`
		} `json:"healthCheckConfig"`
	}
	if err := json.Unmarshal([]byte(full), &parsed); err != nil {
		t.Fatalf("full service config is not valid JSON: %v (full=%q)", err, full)
	}
	if len(parsed.LoadBalancingConfig) != 1 {
		t.Fatalf("expected 1 loadBalancingConfig entry, got %d", len(parsed.LoadBalancingConfig))
	}
	if _, ok := parsed.LoadBalancingConfig[0]["round_robin"]; !ok {
		t.Fatalf("expected round_robin policy in loadBalancingConfig")
	}
	if parsed.HealthCheckConfig.ServiceName != "dal-config-read-get" {
		t.Fatalf("serviceName = %q, want dal-config-read-get", parsed.HealthCheckConfig.ServiceName)
	}
}
