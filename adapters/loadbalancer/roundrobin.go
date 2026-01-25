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
	"strings"

	util "github.com/aldelo/common"
	res "github.com/aldelo/connector/adapters/resolver"
)

// WithRoundRobin returns gRPC dial target, and defaultServiceConfig set for Round Robin client side load balancer
func WithRoundRobin(schemeName string, serviceName string, endpointAddrs []string) (target string, loadBalancerPolicy string, err error) {
	// trim inputs before validation/use
	scheme := strings.TrimSpace(schemeName)
	service := strings.TrimSpace(serviceName)

	if util.LenTrim(service) == 0 {
		return "", "", fmt.Errorf("Resolver Service Name is Required")
	}

	if util.LenTrim(scheme) == 0 {
		return "", "", fmt.Errorf("Resolver Scheme Name is Required")
	}

	if len(endpointAddrs) == 0 {
		return "", "", fmt.Errorf("Resolver Endpoint Addresses Are Required")
	}

	cleanAddrs := make([]string, 0, len(endpointAddrs))
	for _, a := range endpointAddrs {
		trimmed := strings.TrimSpace(a)
		if trimmed == "" {
			continue
		}
		cleanAddrs = append(cleanAddrs, trimmed)
	}
	if len(cleanAddrs) == 0 {
		return "", "", fmt.Errorf("Resolver Endpoint Addresses Are Required (All Were Empty)")
	}

	if err = res.NewManualResolver(scheme, service, cleanAddrs); err != nil {
		return "", "", fmt.Errorf("Setup CLB New Resolver Failed: %s", err.Error())
	}

	// load balancer round robin is per call, rather than per connection
	// this means, load balancer will connect to all discovered ip and
	// perform per call actions in round robin fashion across all channels
	target = fmt.Sprintf("%s:///%s", scheme, service)

	// return a valid gRPC service config string for round_robin
	loadBalancerPolicy = `{"loadBalancingConfig":[{"round_robin":{}}]}`

	return target, loadBalancerPolicy, nil
}
