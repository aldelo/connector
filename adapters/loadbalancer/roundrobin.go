package loadbalancer

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
