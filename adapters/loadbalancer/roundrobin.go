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
	_ "google.golang.org/grpc/balancer/roundrobin"
)

var schemeRE = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)

var serviceRE = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

var hostLabelRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// WithRoundRobin returns gRPC dial target, and defaultServiceConfig set for Round Robin client side load balancer
func WithRoundRobin(schemeName string, serviceName string, endpointAddrs []string) (target string, loadBalancerPolicy string, err error) {
	scheme := strings.TrimSpace(strings.ToLower(schemeName))
	service := strings.TrimSpace(serviceName)

	if scheme == "" {
		return "", "", fmt.Errorf("resolver scheme name is required")
	}
	if !schemeRE.MatchString(scheme) {
		return "", "", fmt.Errorf("resolver scheme name %q is invalid (must match [a-z][a-z0-9+.-]*)", scheme)
	}

	if service == "" {
		return "", "", fmt.Errorf("resolver service name is required")
	}
	if !serviceRE.MatchString(service) {
		return "", "", fmt.Errorf("resolver service name %q is invalid (allowed: A-Z a-z 0-9 . _ ~ -)", service)
	}
	if strings.ContainsAny(service, "/ \t\r\n") {
		return "", "", fmt.Errorf("resolver service name %q is invalid (must not contain slashes or whitespace)", service)
	}
	if strings.Contains(service, "://") {
		return "", "", fmt.Errorf("resolver service name %q is invalid (must not contain \"://\")", service)
	}

	if len(endpointAddrs) == 0 {
		return "", "", fmt.Errorf("resolver endpoint addresses are required")
	}

	seen := make(map[string]struct{}, len(endpointAddrs))
	cleanAddrs := make([]string, 0, len(endpointAddrs))
	for i, a := range endpointAddrs {
		trimmed := strings.TrimSpace(a)
		if trimmed == "" {
			continue
		}
		if strings.Contains(trimmed, "://") {
			return "", "", fmt.Errorf("resolver endpoint address %d (%q) is invalid (must not include a URI scheme)", i, a)
		}

		host, port, splitErr := net.SplitHostPort(trimmed)
		if splitErr != nil || host == "" || port == "" {
			return "", "", fmt.Errorf("resolver endpoint address %d (%q) is invalid (want host:port; IPv6 must be in [::1]:port form): %w", i, a, splitErr)
		}

		if !strings.Contains(host, ":") && strings.HasSuffix(host, ".") {
			host = strings.TrimSuffix(host, ".")
			if host == "" {
				return "", "", fmt.Errorf("resolver endpoint address %d (%q) is invalid (host cannot be empty)", i, a)
			}
		}

		if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#") {
			return "", "", fmt.Errorf("resolver endpoint address %d (%q) is invalid (host must not contain scheme or path)", i, a)
		}

		var ipHost net.IP
		ipZone := ""

		if strings.Contains(host, ":") {
			baseHost := host
			if zoneIdx := strings.LastIndex(host, "%"); zoneIdx != -1 {
				baseHost = host[:zoneIdx]
				ipZone = host[zoneIdx+1:]
				if ipZone == "" {
					return "", "", fmt.Errorf("resolver endpoint address %d (%q) has invalid IPv6 zone (empty)", i, a)
				}
			}
			ipHost = net.ParseIP(baseHost)
			if ipHost == nil {
				return "", "", fmt.Errorf("resolver endpoint address %d (%q) has invalid IP literal %q", i, a, baseHost)
			}
		} else {
			labels := strings.Split(host, ".")
			for _, lbl := range labels {
				if lbl == "" || !hostLabelRE.MatchString(lbl) {
					return "", "", fmt.Errorf("resolver endpoint address %d (%q) has invalid host label %q", i, a, lbl)
				}
			}
		}

		if p, convErr := strconv.Atoi(port); convErr != nil || p < 1 || p > 65535 {
			return "", "", fmt.Errorf("resolver endpoint address %d (%q) has invalid port %q (must be numeric 1-65535): %v", i, a, port, convErr)
		}

		canonicalHost := host
		if ipHost != nil {
			canonicalHost = ipHost.String()
			if ipZone != "" {
				canonicalHost = canonicalHost + "%" + ipZone
			}
		} else {
			canonicalHost = strings.ToLower(host)
		}
		canonical := net.JoinHostPort(canonicalHost, port)

		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		cleanAddrs = append(cleanAddrs, canonical)
	}
	if len(cleanAddrs) == 0 {
		return "", "", fmt.Errorf("resolver endpoint addresses are required (all were empty, duplicate, or invalid)")
	}

	if err = res.NewManualResolver(scheme, service, cleanAddrs); err != nil {
		return "", "", fmt.Errorf("setup CLB new resolver failed: %w", err)
	}

	target = fmt.Sprintf("%s:///%s", scheme, service)

	// FIX: Return a JSON *fragment* (without outer braces), not a complete JSON object.
	// The caller in client.go wraps this in fmt.Sprintf(`{%s}`, defSvrConf), so returning
	// a full object like `{"loadBalancingConfig":[...]}` produces `{{"loadBalancingConfig":[...]}}` —
	// double-wrapped braces, invalid JSON. gRPC silently ignores invalid service configs,
	// meaning round-robin load balancing never activates and all RPCs go to one backend.
	//
	// With this fragment, the caller produces:
	//   LB only:        `{"loadBalancingConfig":[{"round_robin":{}}]}`
	//   LB + health:    `{"loadBalancingConfig":[{"round_robin":{}}], "healthCheckConfig":{"serviceName":""}}`
	// Both are valid gRPC service config JSON.
	loadBalancerPolicy = `"loadBalancingConfig":[{"round_robin":{}}]`

	return target, loadBalancerPolicy, nil
}
