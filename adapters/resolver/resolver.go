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

func normalizeSchemeName(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if util.LenTrim(s) == 0 {
		return "clb"
	}
	return s
}

func safeRegister(builder resolver.Builder) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("failed to register resolver for scheme '%s': %v", builder.Scheme(), rec)
		}
	}()
	resolver.Register(builder)
	return nil
}

// centralized, case-insensitive address normalization and de-duplication.
func normalizeAddresses(endpointAddrs []string, onEmpty error) ([]resolver.Address, error) {
	addrs := make([]resolver.Address, 0, len(endpointAddrs))
	seen := make(map[string]struct{})
	for _, raw := range endpointAddrs {
		addr := strings.TrimSpace(raw)
		if len(addr) == 0 {
			continue
		}
		key := strings.ToLower(addr)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		addrs = append(addrs, resolver.Address{Addr: addr})
	}
	if len(addrs) == 0 {
		if onEmpty != nil {
			return nil, onEmpty
		}
		return nil, fmt.Errorf("No Valid Endpoint Address Found")
	}
	return addrs, nil
}

func NewManualResolver(schemeName string, serviceName string, endpointAddrs []string) error {
	schemeName = normalizeSchemeName(schemeName)

	serviceName = strings.ToLower(strings.TrimSpace(serviceName))
	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("ServiceName is Required")
	}

	addrs, err := normalizeAddresses(endpointAddrs, nil)
	if err != nil {
		return err
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
	addrs, err := normalizeAddresses(endpointAddrs, fmt.Errorf("Endpoint Addresses Required"))
	if err != nil {
		return err
	}

	schemeName = normalizeSchemeName(schemeName)

	serviceName = strings.ToLower(strings.TrimSpace(serviceName))
	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("ServiceName is Required")
	}

	r, err := getResolver(schemeName, serviceName)
	if err != nil {
		return err
	}

	// manual.Resolver.UpdateState does not return an error.
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
	registeredSchemes[schemeName] = serviceName

	// guard resolver.Register against panic and roll back on failure
	if err := safeRegister(r); err != nil {
		delete(schemeMap, key)                // rollback
		delete(registeredSchemes, schemeName) // rollback
		return err
	}

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
