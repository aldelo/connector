package resolver

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
	"log"
	"strings"
	"sync"

	util "github.com/aldelo/common"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
)

var (
	schemeMap         map[string]*manual.Resolver
	registeredSchemes map[string]string // track which service owns a registered scheme
	_mux              sync.Mutex        // thread-safety for accessing and modifying schemeMap
)

// collision-safe map key builder
func composeKey(schemeName string, serviceName string) string {
	return strings.ToLower(strings.TrimSpace(schemeName)) + ":" + strings.ToLower(strings.TrimSpace(serviceName))
}

func NewManualResolver(schemeName string, serviceName string, endpointAddrs []string) error {
	schemeName = strings.ToLower(strings.TrimSpace(schemeName))
	if util.LenTrim(schemeName) == 0 {
		schemeName = "clb"
	}

	serviceName = strings.ToLower(strings.TrimSpace(serviceName))
	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("ServiceName is Required")
	}

	addrs := make([]resolver.Address, 0, len(endpointAddrs))
	for _, raw := range endpointAddrs {
		addr := strings.TrimSpace(raw)
		if len(addr) == 0 {
			continue
		}
		addrs = append(addrs, resolver.Address{Addr: addr})
	}

	if len(addrs) == 0 {
		return fmt.Errorf("No Valid Endpoint Address Found")
	}

	r := manual.NewBuilderWithScheme(schemeName)
	r.InitialState(resolver.State{
		Addresses: addrs,
	})

	if err := setResolver(schemeName, serviceName, r); err != nil {
		return err
	}

	return nil
}

func UpdateManualResolver(schemeName string, serviceName string, endpointAddrs []string) error {
	addrs := make([]resolver.Address, 0, len(endpointAddrs))
	for _, raw := range endpointAddrs {
		addr := strings.TrimSpace(raw)
		if len(addr) == 0 {
			continue
		}
		addrs = append(addrs, resolver.Address{Addr: addr})
	}
	if len(addrs) == 0 {
		return fmt.Errorf("Endpoint Addresses Required")
	}

	schemeName = strings.ToLower(strings.TrimSpace(schemeName))
	if util.LenTrim(schemeName) == 0 {
		return fmt.Errorf("SchemeName is Required")
	}

	serviceName = strings.ToLower(strings.TrimSpace(serviceName))
	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("ServiceName is Required")
	}

	r, err := getResolver(schemeName, serviceName)
	if err != nil {
		return err
	}

	r.UpdateState(resolver.State{
		Addresses: addrs,
	})

	log.Println("[NOTE] Please Ignore Error 'Health check is requested but health check function is not set'; UpdateManualResolver is Completed")
	return nil
}

func setResolver(schemeName string, serviceName string, r *manual.Resolver) error {
	_mux.Lock()
	defer _mux.Unlock()

	if schemeMap == nil {
		schemeMap = make(map[string]*manual.Resolver)
	}
	if registeredSchemes == nil {
		registeredSchemes = make(map[string]string)
	}

	key := composeKey(schemeName, serviceName)

	if _, exists := schemeMap[key]; exists {
		return fmt.Errorf("Resolver already registered for scheme '%s' and service '%s'", schemeName, serviceName)
	}

	if existingService, taken := registeredSchemes[schemeName]; taken && existingService != serviceName {
		return fmt.Errorf("Scheme '%s' already registered for service '%s'; use a unique scheme per service", schemeName, existingService)
	}

	schemeMap[key] = r
	registeredSchemes[serviceName] = serviceName

	// 1/9/2025 by yao.bin - Caution: There is a global map in "grpc/resolver", so we have to put "resolver.Register" in mutex lock too
	var builder resolver.Builder
	builder = r

	resolver.Register(builder)
	//resolver.SetDefaultScheme(r.Scheme()) // optional
	return nil
}

func getResolver(schemeName string, serviceName string) (*manual.Resolver, error) {
	_mux.Lock()
	defer _mux.Unlock()

	if schemeMap == nil {
		return nil, fmt.Errorf("Resolver SchemeMap Nil")
	}

	key := composeKey(schemeName, serviceName)
	if r := schemeMap[key]; r != nil {
		return r, nil
	}

	return nil, fmt.Errorf("No Resolver Found for SchemeName '%s' and ServiceName '%s' in SchemeMap", schemeName, serviceName)
}
