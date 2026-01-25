package loadbalancer

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
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	res "github.com/aldelo/connector/adapters/resolver"
)

// add precompiled regex for scheme validation
var schemeRE = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

// WithRoundRobin returns gRPC dial target, and defaultServiceConfig set for Round Robin client side load balancer
func WithRoundRobin(schemeName string, serviceName string, endpointAddrs []string) (target string, loadBalancerPolicy string, err error) {
	// trim inputs before validation/use
	scheme := strings.TrimSpace(strings.ToLower(schemeName))
	service := strings.TrimSpace(serviceName)

	if scheme == "" {
		return "", "", fmt.Errorf("Resolver Scheme Name is Required")
	}
	if !schemeRE.MatchString(scheme) { // validate scheme syntax
		return "", "", fmt.Errorf("resolver scheme name %q is invalid (must match [a-z][a-z0-9+.-]*)", scheme)
	}

	if service == "" {
		return "", "", fmt.Errorf("Resolver Service Name is Required")
	}
	if strings.ContainsAny(service, "/ \t\r\n") { // disallow slashes/whitespace to keep target valid
		return "", "", fmt.Errorf("resolver service name %q is invalid (must not contain slashes or whitespace)", service)
	}
	if strings.Contains(service, "://") { // prevent embedded scheme-like fragments
		return "", "", fmt.Errorf("resolver service name %q is invalid (must not contain \"://\")", service)
	}

	if len(endpointAddrs) == 0 {
		return "", "", fmt.Errorf("Resolver Endpoint Addresses Are Required")
	}

	seen := make(map[string]struct{}, len(endpointAddrs))
	cleanAddrs := make([]string, 0, len(endpointAddrs))
	for i, a := range endpointAddrs {
		trimmed := strings.TrimSpace(a)
		if trimmed == "" {
			continue
		}
		host, port, splitErr := net.SplitHostPort(trimmed)
		if splitErr != nil || host == "" || port == "" {
			return "", "", fmt.Errorf("resolver endpoint address %d (%q) is invalid (want host:port; IPv6 must be in [::1]:port form): %w", i, a, splitErr)
		}

		// validate port is numeric and within 1-65535
		if p, convErr := strconv.Atoi(port); convErr != nil || p < 1 || p > 65535 {
			return "", "", fmt.Errorf("resolver endpoint address %d (%q) has invalid port %q (must be numeric 1-65535): %v", i, a, port, convErr)
		}

		if _, ok := seen[trimmed]; ok {
			continue // skip duplicates
		}
		seen[trimmed] = struct{}{}
		cleanAddrs = append(cleanAddrs, trimmed)
	}
	if len(cleanAddrs) == 0 {
		return "", "", fmt.Errorf("Resolver Endpoint Addresses Are Required (all were empty, duplicate, or invalid)")
	}

	if err = res.NewManualResolver(scheme, service, cleanAddrs); err != nil {
		return "", "", fmt.Errorf("Setup CLB New Resolver Failed: %w", err)
	}

	// load balancer round robin is per call, rather than per connection
	// this means, load balancer will connect to all discovered ip and
	// perform per call actions in round robin fashion across all channels
	target = fmt.Sprintf("%s:///%s", scheme, service)

	// return a valid gRPC service config string for round_robin
	loadBalancerPolicy = `{"loadBalancingConfig":[{"round_robin":{}}]}`

	return target, loadBalancerPolicy, nil
}
