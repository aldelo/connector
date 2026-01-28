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

// normalize and validate service names in one place.
func normalizeServiceName(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if util.LenTrim(s) == 0 {
		return "", fmt.Errorf("ServiceName is Required")
	}
	return s, nil
}

func safeRegister(builder resolver.Builder) (err error) {
	if builder == nil {
		return fmt.Errorf("resolver builder is nil")
	}

	scheme := builder.Scheme()

	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("failed to register resolver for scheme '%s': %v", scheme, rec)
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

	// centralized service-name validation to avoid inconsistent checks.
	svc, err := normalizeServiceName(serviceName)
	if err != nil {
		return err
	}

	addrs, err := normalizeAddresses(endpointAddrs, nil)
	if err != nil {
		return err
	}

	r := manual.NewBuilderWithScheme(schemeName)
	r.InitialState(resolver.State{
		Addresses: addrs,
	})

	// wrap for clearer context when registration fails.
	if err := setResolver(schemeName, svc, r); err != nil {
		return fmt.Errorf("register manual resolver: %w", err)
	}

	return nil
}

func UpdateManualResolver(schemeName string, serviceName string, endpointAddrs []string) error {
	addrs, err := normalizeAddresses(endpointAddrs, fmt.Errorf("Endpoint Addresses Required"))
	if err != nil {
		return err
	}

	schemeName = normalizeSchemeName(schemeName)

	// centralized service-name validation to avoid inconsistent checks.
	svc, err := normalizeServiceName(serviceName)
	if err != nil {
		return err
	}

	r, err := getResolver(schemeName, svc)
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
	if r == nil {
		return fmt.Errorf("Resolver is nil; cannot register")
	}

	// enforce normalization inside the setter to avoid caller mistakes.
	schemeName = normalizeSchemeName(schemeName)
	svc, err := normalizeServiceName(serviceName)
	if err != nil {
		return err
	}

	if strings.ToLower(strings.TrimSpace(r.Scheme())) != schemeName {
		return fmt.Errorf("Resolver scheme '%s' does not match normalized scheme '%s'", r.Scheme(), schemeName)
	}

	_mux.Lock()
	defer _mux.Unlock()

	if schemeMap == nil {
		schemeMap = make(map[string]*manual.Resolver)
	}
	if registeredSchemes == nil {
		registeredSchemes = make(map[string]string)
	}

	key := composeKey(schemeName, svc)

	if _, exists := schemeMap[key]; exists {
		return fmt.Errorf("Resolver already registered for scheme '%s' and service '%s'", schemeName, svc)
	}

	if existingService, taken := registeredSchemes[schemeName]; taken && existingService != svc {
		return fmt.Errorf("Scheme '%s' already registered for service '%s'; use a unique scheme per service", schemeName, existingService)
	}

	schemeMap[key] = r
	registeredSchemes[schemeName] = svc

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
	// normalize inside getter to ensure consistent map keying.
	schemeName = normalizeSchemeName(schemeName)
	svc, err := normalizeServiceName(serviceName)
	if err != nil {
		return nil, err
	}

	_mux.Lock()
	defer _mux.Unlock()

	if schemeMap == nil {
		return nil, fmt.Errorf("Resolver SchemeMap Nil")
	}

	key := composeKey(schemeName, svc)
	if r := schemeMap[key]; r != nil {
		return r, nil
	}

	return nil, fmt.Errorf("No Resolver Found for SchemeName '%s' and ServiceName '%s' in SchemeMap", schemeName, svc)
}
