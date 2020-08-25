package loadbalancer

/*
 * Copyright 2020 Aldelo, LP
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
	util "github.com/aldelo/common"
	"github.com/aldelo/connector/adapters/resolver"
)

// WithRoundRobin returns gRPC dial target, and defaultServiceConfig set for Round Robin client side load balancer
func WithRoundRobin(schemeName string, serviceName string, endpointAddrs []string) (target string, loadBalancerPolicy string, err error) {
	if util.LenTrim(schemeName) == 0 {
		return "", "", fmt.Errorf("Resolver Scheme Name is Required")
	}

	if util.LenTrim(serviceName) == 0 {
		return "", "", fmt.Errorf("Resolver Service Name is Required")
	}

	if len(endpointAddrs) == 0 {
		return "", "", fmt.Errorf("Resolver Endpoint Addresses Are Required")
	}

	// init custom resolver with given
	resolver.Update(schemeName, serviceName, endpointAddrs)

	return fmt.Sprintf("%s:///%s", schemeName, serviceName), `"loadBalancingPolicy":"round_robin"`, nil
}
