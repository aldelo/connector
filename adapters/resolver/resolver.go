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
	"net"
	"regexp"
	"strconv"
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
	schemePattern     = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)
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

// enforce RFC-ish scheme validity to avoid resolver.Register panics.
func normalizeAndValidateSchemeName(raw string) (string, error) {
	s := normalizeSchemeName(raw)
	if !schemePattern.MatchString(s) {
		return "", fmt.Errorf("invalid resolver scheme '%s'; must match %s", s, schemePattern.String())
	}
	return s, nil
}

// normalize and validate service names in one place.
func normalizeServiceName(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if util.LenTrim(s) == 0 {
		return "", fmt.Errorf("ServiceName is Required")
	}
	if strings.ContainsAny(s, " :") {
		return "", fmt.Errorf("ServiceName must not contain ':' or spaces")
	}
	return s, nil
}

func safeRegister(builder resolver.Builder) (err error) {
	if builder == nil {
		return fmt.Errorf("resolver builder is nil")
	}

	scheme := builder.Scheme()
	if util.LenTrim(scheme) == 0 { // guard against empty scheme; resolver.Register would panic
		return fmt.Errorf("resolver scheme is empty")
	}

	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("failed to register resolver for scheme '%s': %v", scheme, rec)
		}
	}()
	resolver.Register(builder)
	return nil
}

// explicit port validation with range check.
func validatePort(port string) error {
	if util.LenTrim(port) == 0 {
		return fmt.Errorf("missing port")
	}
	p, err := strconv.Atoi(strings.TrimSpace(port))
	if err != nil || p <= 0 || p > 65535 {
		return fmt.Errorf("invalid port '%s'", port)
	}
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

		// Treat scheme-prefixed / opaque addresses early so colons in them don't
		// get misclassified as host:port.
		if strings.Contains(addr, "://") || strings.HasPrefix(addr, "unix:") {
			if _, exists := seen[addr]; exists {
				continue
			}
			seen[addr] = struct{}{}
			addrs = append(addrs, resolver.Address{Addr: addr})
			continue
		}

		// reject bracketless IPv6 with a numeric tail that looks like a port.
		if strings.Count(addr, ":") > 1 && !strings.HasPrefix(addr, "[") {
			tail := addr[strings.LastIndex(addr, ":")+1:]
			if _, err := strconv.Atoi(tail); err == nil {
				return nil, fmt.Errorf("invalid endpoint address '%s': IPv6 addresses with ports must be bracketed, e.g. [addr]:port", addr)
			}
		}

		// Heuristic: only treat as host:port when it looks like one.
		likelyHostPort := strings.Count(addr, ":") >= 1 || strings.HasPrefix(addr, "[")
		if host, port, err := net.SplitHostPort(addr); err == nil && len(host) != 0 {
			if err := validatePort(port); err != nil {
				return nil, fmt.Errorf("endpoint address '%s': %w", addr, err)
			}

			trimmedPort := strings.TrimSpace(port)

			// Preserve IPv6 zone casing; only lowercase when no zone is present.
			hostKey := host
			hostOut := host
			if !strings.Contains(host, "%") {
				hostKey = strings.ToLower(host)
				hostOut = hostKey
			}

			canonicalKey := hostKey + ":" + trimmedPort
			if _, exists := seen[canonicalKey]; exists {
				continue
			}
			seen[canonicalKey] = struct{}{}
			addrs = append(addrs, resolver.Address{
				Addr: net.JoinHostPort(hostOut, trimmedPort),
			})
			continue
		} else if err != nil && likelyHostPort {
			// Likely malformed host:port (e.g., missing port or bad IPv6 bracket)
			return nil, fmt.Errorf("invalid endpoint address '%s': %v", addr, err)
		}

		// fail fast on bare IP literals that are missing a port.
		if net.ParseIP(addr) != nil {
			return nil, fmt.Errorf("invalid endpoint address '%s': missing port", addr)
		}

		// Fallback: treat as opaque (non-host:port).
		if _, exists := seen[addr]; exists {
			continue
		}
		seen[addr] = struct{}{}
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
	schemeName, err := normalizeAndValidateSchemeName(schemeName)
	if err != nil {
		return err
	}

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

	schemeName, err = normalizeAndValidateSchemeName(schemeName)
	if err != nil {
		return err
	}

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
	schemeName, err := normalizeAndValidateSchemeName(schemeName)
	if err != nil {
		return err
	}
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
	schemeName, err := normalizeAndValidateSchemeName(schemeName)
	if err != nil {
		return nil, err
	}
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
