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
	"log"
	"strings"
	"sync"
	"time"

	util "github.com/aldelo/common"
)

type Cache struct {
	ServiceEndpoints map[string][]*serviceEndpoint

	DisableLogging bool
	_mu            sync.Mutex
}

// normalize service names consistently across all cache operations
func normalizeServiceName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// AddServiceEndpoints will append slice of service endpoints associated with the given serviceName within map
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) AddServiceEndpoints(serviceName string, eps []*serviceEndpoint) {
	if c == nil {
		return
	}

	if util.LenTrim(serviceName) == 0 {
		return
	}

	if len(eps) == 0 {
		return
	}

	now := time.Now()

	// sanitize incoming endpoints to drop nil entries and avoid panics
	sanitized := make([]*serviceEndpoint, 0, len(eps))
	for _, v := range eps {
		if v == nil {
			continue
		}
		cp := *v
		cp.Host = strings.TrimSpace(cp.Host)
		cp.Version = strings.TrimSpace(cp.Version)
		if cp.Host == "" || cp.Port == 0 { // drop invalid
			continue
		}
		if !cp.CacheExpire.IsZero() && cp.CacheExpire.Before(now) { // drop expired
			continue
		}
		cpCopy := cp
		sanitized = append(sanitized, &cpCopy)
	}
	if len(sanitized) == 0 {
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	if c.ServiceEndpoints == nil {
		c.ServiceEndpoints = make(map[string][]*serviceEndpoint)
	}

	serviceName = normalizeServiceName(serviceName)
	if serviceName == "" { // guard empty key after normalization
		return
	}

	// shared key builder with normalized host/version
	buildKey := func(e *serviceEndpoint) string {
		if e == nil {
			return ""
		}
		host := strings.ToLower(strings.TrimSpace(e.Host))
		if host == "" || e.Port == 0 { // drop invalid existing entries (empty host or zero port)
			return ""
		}
		ver := strings.ToLower(strings.TrimSpace(e.Version))
		return host + ":" + util.UintToStr(e.Port) + "|" + ver
	}

	// stable, deduplicated merge (preserve existing order, then new)
	seen := make(map[string]struct{})
	merged := make([]*serviceEndpoint, 0)

	if list, ok := c.ServiceEndpoints[serviceName]; ok {
		for _, v := range list {
			if v == nil { // drop nil existing entries
				continue
			}
			if !v.CacheExpire.IsZero() && v.CacheExpire.Before(now) { // drop expired existing entries so fresh ones can replace them
				continue
			}
			k := buildKey(v)
			if k == "" {
				continue // skip malformed/nil
			}
			if _, exists := seen[k]; exists {
				continue
			}
			seen[k] = struct{}{}
			merged = append(merged, v)
		}
		if !c.DisableLogging {
			log.Println("Appending New Service Endpoints for " + serviceName)
		}
	} else {
		if !c.DisableLogging {
			log.Println("Adding New Service Endpoints for " + serviceName)
		}
	}

	for _, v := range sanitized {
		k := buildKey(v)
		if k == "" {
			continue // guard
		}
		if _, exists := seen[k]; exists {
			continue
		}
		seen[k] = struct{}{}
		merged = append(merged, v)
	}

	c.ServiceEndpoints[serviceName] = merged
}

// PurgeServiceEndpoints will remove all endpoints associated with the given serviceName within map
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) PurgeServiceEndpoints(serviceName string) {
	if c == nil {
		return
	}

	if util.LenTrim(serviceName) == 0 {
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	if c.ServiceEndpoints == nil {
		return
	}

	serviceName = normalizeServiceName(serviceName)
	if serviceName == "" { // guard empty key after normalization
		return
	}
	if !c.DisableLogging {
		log.Println("Cached Service Endpoints Purged for " + serviceName)
	}

	delete(c.ServiceEndpoints, serviceName)
}

// PurgeServiceEndpointByHostAndPort will remove a specific endpoint for a service based on host and port info
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) PurgeServiceEndpointByHostAndPort(serviceName string, host string, port uint) {
	if c == nil {
		return
	}

	if util.LenTrim(serviceName) == 0 {
		return
	}

	if util.LenTrim(host) == 0 {
		return
	}

	if port == 0 {
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	serviceName = normalizeServiceName(serviceName)
	if serviceName == "" { // guard empty key after normalization
		return
	}
	hostNormalized := strings.ToLower(strings.TrimSpace(host))
	if !c.DisableLogging {
		log.Println("Cached Service Endpoint Purging " + hostNormalized + ":" + util.UintToStr(port) + " From " + serviceName + "...")
	}

	eps, ok := c.ServiceEndpoints[serviceName]

	if !ok {
		// no service endpoints found for service name
		if !c.DisableLogging {
			log.Println("Cached Service Endpoint Purging OK: No Endpoints Found for " + serviceName)
		}
		return
	}

	newEps := make([]*serviceEndpoint, 0, len(eps)) // replace util.SliceDeleteElement with safe manual filter
	found := false

	for _, v := range eps {
		if v != nil {
			vHost := strings.ToLower(strings.TrimSpace(v.Host))
			if vHost == hostNormalized && v.Port == port {
				// found
				found = true
				continue
			}
		}
		newEps = append(newEps, v)
	}

	if found {
		if len(newEps) == 0 {
			delete(c.ServiceEndpoints, serviceName)
		} else {
			c.ServiceEndpoints[serviceName] = newEps
		}

		if !c.DisableLogging {
			log.Println("Cached Service Endpoint Purging OK: Endpoint '" + hostNormalized + ":" + util.UintToStr(port) + "' Removed From " + serviceName)
		}
	} else {
		if !c.DisableLogging {
			log.Println("Cached Service Endpoint Purging OK: No Endpoint Found '" + hostNormalized + ":" + util.UintToStr(port) + "' In " + serviceName)
		}
	}
}

// GetLiveServiceEndpoints will retrieve currently non-expired service endpoints and remove any expired service endpoints from map,
// for a given serviceName
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) GetLiveServiceEndpoints(serviceName string, version string, ignoreExpired ...bool) (liveEndpoints []*serviceEndpoint) {
	if c == nil {
		return []*serviceEndpoint{}
	}

	if util.LenTrim(serviceName) == 0 {
		return []*serviceEndpoint{}
	}

	ignExp := false
	if len(ignoreExpired) > 0 {
		ignExp = ignoreExpired[0]
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	if c.ServiceEndpoints == nil {
		return []*serviceEndpoint{}
	}

	serviceName = normalizeServiceName(serviceName) // normalize consistently
	if serviceName == "" {                          // guard empty key after normalization
		return []*serviceEndpoint{}
	}
	versionNormalized := strings.ToLower(strings.TrimSpace(version))

	eps, ok := c.ServiceEndpoints[serviceName]
	if !ok || len(eps) == 0 {
		if !c.DisableLogging {
			log.Println("Get Live Service Endpoints for " + serviceName + ", version '" + version + "': " + "None Found")
		}
		return []*serviceEndpoint{}
	}

	if !c.DisableLogging {
		log.Println("Get Live Service Endpoints for " + serviceName + ", version '" + version + "': " + util.Itoa(len(eps)) + " Found")
	}

	now := time.Now()
	expiredList := []int{}
	live := make([]*serviceEndpoint, 0, len(eps))

	for i, v := range eps {
		if v == nil { // guard nil to avoid panic
			expiredList = append(expiredList, i)
			continue
		}

		vVersion := strings.ToLower(strings.TrimSpace(v.Version))
		isExpired := !v.CacheExpire.IsZero() && v.CacheExpire.Before(now)

		if isExpired && !ignExp {
			if !c.DisableLogging {
				log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + version + "': (Expired) " + v.Host + ":" + util.UintToStr(v.Port))
			}
			expiredList = append(expiredList, i)
			continue
		}

		// even when ignoring expiry for return filtering, still purge expired entries from the cache
		if isExpired && ignExp {
			expiredList = append(expiredList, i)
		}

		if util.LenTrim(versionNormalized) > 0 && vVersion != versionNormalized {
			if !c.DisableLogging && !ignExp { // keep existing log behavior only when enforcing expiry
				log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + version + "': (Version Mismatch) " + v.Version + " vs " + version)
			}
			continue
		}

		live = append(live, v)
	}

	// remove expired entries
	if len(expiredList) > 0 {
		for _, idx := range expiredList {
			if idx >= 0 && idx < len(eps) {
				eps[idx] = nil
			}
		}

		newEps := make([]*serviceEndpoint, 0, len(eps))
		for _, v := range eps {
			if v != nil {
				newEps = append(newEps, v)
			}
		}

		if len(newEps) == 0 { // drop empty slice to avoid stale map entries
			delete(c.ServiceEndpoints, serviceName)
		} else {
			c.ServiceEndpoints[serviceName] = newEps
		}
	}

	// defensive copy to avoid caller mutating shared cache entries
	liveEndpoints = make([]*serviceEndpoint, 0, len(live))
	for _, v := range live {
		if v == nil {
			continue
		}
		cp := *v
		liveEndpoints = append(liveEndpoints, &cp)
	}

	if !c.DisableLogging {
		log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + version + "': " + util.Itoa(len(liveEndpoints)) + " Returned")
	}
	return
}
