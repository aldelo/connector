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
	serviceEndpoints map[string][]*serviceEndpoint

	DisableLogging bool
	_mu            sync.Mutex
}

// normalize service names consistently across all cache operations
func normalizeServiceName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}

// GetServiceEndpoints returns a deep copy of endpoints for the given service
func (c *Cache) GetServiceEndpoints(serviceName string) []*serviceEndpoint {
	if c == nil {
		return nil
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	serviceName = normalizeServiceName(serviceName)
	if serviceName == "" {
		return nil
	}

	eps, ok := c.serviceEndpoints[serviceName]
	if !ok || len(eps) == 0 {
		return nil
	}

	// Deep copy to prevent race conditions
	result := make([]*serviceEndpoint, 0, len(eps))
	for _, ep := range eps {
		if ep != nil {
			copied := *ep
			result = append(result, &copied)
		}
	}
	return result
}

// AddServiceEndpoints will append slice of service endpoints associated with the given serviceName within map
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) AddServiceEndpoints(serviceName string, eps []*serviceEndpoint) {
	if c == nil {
		return
	}

	if len(eps) == 0 {
		return
	}

	serviceName = normalizeServiceName(serviceName) // normalize once up front
	if serviceName == "" {                          // guard empty normalized name early
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
		cp.Host = strings.ToLower(strings.TrimSpace(cp.Host))
		cp.Version = strings.ToLower(strings.TrimSpace(cp.Version))
		if cp.Host == "" || cp.Port == 0 { // drop invalid
			continue
		}
		if !cp.CacheExpire.IsZero() && cp.CacheExpire.Before(now) { // drop expired
			continue
		}
		sanitized = append(sanitized, &cp)
	}
	if len(sanitized) == 0 {
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	if c.serviceEndpoints == nil {
		c.serviceEndpoints = make(map[string][]*serviceEndpoint)
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
	// FIX: Pre-allocate with capacity hint to reduce allocations in hot path
	existingLen := 0
	if list, ok := c.serviceEndpoints[serviceName]; ok {
		existingLen = len(list)
	}
	seen := make(map[string]int, existingLen+len(eps))
	merged := make([]*serviceEndpoint, 0, existingLen+len(eps))

	if list, ok := c.serviceEndpoints[serviceName]; ok {
		for _, v := range list {
			if v == nil { // drop nil existing entries
				continue
			}
			if strings.TrimSpace(v.Host) == "" || v.Port == 0 { // drop invalid existing entries
				continue
			}
			if !v.CacheExpire.IsZero() && v.CacheExpire.Before(now) { // drop expired existing entries so fresh ones can replace them
				continue
			}
			k := buildKey(v)
			if k == "" {
				continue // skip malformed/nil
			}
			if idx, exists := seen[k]; exists {
				merged[idx] = v
				continue
			}
			seen[k] = len(merged)
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
		if idx, exists := seen[k]; exists {
			merged[idx] = v
			continue
		}
		seen[k] = len(merged)
		merged = append(merged, v)
	}

	c.serviceEndpoints[serviceName] = merged
}

// endpointKey builds the identity key host:port|version used to match endpoints across cycles.
func endpointKey(host string, port uint, version string) string {
	return strings.ToLower(strings.TrimSpace(host)) + ":" + util.UintToStr(port) + "|" +
		strings.ToLower(strings.TrimSpace(version))
}

// ReconcileServiceEndpoints reconciles the cached set for serviceName to the freshly discovered
// set `fresh`, but scoped to a single `version` and with per-endpoint grace/hysteresis:
//
//   - Entries of a DIFFERENT version are preserved untouched. The cache is a process-global
//     singleton keyed by service.namespace WITHOUT version, so a version-filtered discovery
//     (INSTANCE_VERSION) from one client must not wipe another client's version.
//   - An existing entry of THIS version that is absent from `fresh` is not dropped immediately;
//     its missing-counter is incremented and it is kept until it has been absent for
//     `graceCycles` consecutive reconciles. This absorbs a transient partial discovery result
//     (health-check flap, Cloud Map eventual consistency, >MaxResults truncation) so the
//     resolver is never shrunk on a single blip. A reappearing endpoint resets its counter.
//   - Endpoints present in `fresh` are refreshed (counter reset).
//
// `graceCycles` < 1 is treated as 1 (drop-absent-immediately within scope). An empty/fully
// invalid `fresh` is a no-op — the discovery caller fails fast on empty before reconciling, and
// reconcile must never shrink on empty. Atomic under one lock. serviceName = lowercase of
// servicename.namespacename.
func (c *Cache) ReconcileServiceEndpoints(serviceName string, version string, fresh []*serviceEndpoint, graceCycles int) {
	if c == nil {
		return
	}

	serviceName = normalizeServiceName(serviceName)
	if serviceName == "" {
		return
	}
	if graceCycles < 1 {
		graceCycles = 1
	}
	versionNorm := strings.ToLower(strings.TrimSpace(version))
	now := time.Now()

	// sanitize + dedup the fresh set (same rules as AddServiceEndpoints), keyed for matching.
	freshByKey := make(map[string]*serviceEndpoint, len(fresh))
	freshOrder := make([]*serviceEndpoint, 0, len(fresh))
	for _, v := range fresh {
		if v == nil {
			continue
		}
		cp := *v
		cp.Host = strings.ToLower(strings.TrimSpace(cp.Host))
		cp.Version = strings.ToLower(strings.TrimSpace(cp.Version))
		if cp.Host == "" || cp.Port == 0 {
			continue
		}
		if !cp.CacheExpire.IsZero() && cp.CacheExpire.Before(now) {
			continue
		}
		cp.missingCount = 0
		k := endpointKey(cp.Host, cp.Port, cp.Version)
		if _, exists := freshByKey[k]; exists {
			freshByKey[k] = &cp // last one wins
			continue
		}
		freshByKey[k] = &cp
		freshOrder = append(freshOrder, &cp)
	}

	if len(freshByKey) == 0 { // never shrink on an empty/invalid fresh result
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()
	if c.serviceEndpoints == nil {
		c.serviceEndpoints = make(map[string][]*serviceEndpoint)
	}

	existing := c.serviceEndpoints[serviceName]
	result := make([]*serviceEndpoint, 0, len(existing)+len(freshOrder))
	placed := make(map[string]bool, len(existing)+len(freshOrder))
	pruned := 0
	// Refresh in-grace (kept-but-absent) endpoints to the current batch's expiry so a short
	// SdEndpointCacheExpires can't TTL-prune them before grace elapses (grace is the sole prune
	// path). All fresh endpoints in a cycle share the discovery-computed expiry.
	keepExpire := freshOrder[0].CacheExpire

	for _, e := range existing {
		if e == nil {
			continue
		}
		host := strings.ToLower(strings.TrimSpace(e.Host))
		if host == "" || e.Port == 0 {
			continue // drop unusable
		}
		eVer := strings.ToLower(strings.TrimSpace(e.Version))
		k := endpointKey(host, e.Port, eVer)

		// Preserve-as-is ONLY for a specific (non-empty) scope that differs — that is the
		// version-thrash guard. An EMPTY scope means the discovery was unfiltered and is
		// authoritative for the WHOLE service, so every version is in scope (prune/grace absent).
		if versionNorm != "" && eVer != versionNorm {
			result = append(result, e)
			placed[k] = true
			continue
		}
		if f, ok := freshByKey[k]; ok { // still present -> take the fresh copy (counter reset)
			result = append(result, f)
			placed[k] = true
			continue
		}
		// absent within scope -> grace
		e.missingCount++
		if e.missingCount >= graceCycles {
			pruned++
			continue // dropped
		}
		e.CacheExpire = keepExpire // refresh so TTL doesn't pre-empt grace
		result = append(result, e)
		placed[k] = true
	}

	for _, f := range freshOrder { // brand-new endpoints not already carried over
		k := endpointKey(f.Host, f.Port, f.Version)
		if placed[k] {
			continue
		}
		result = append(result, f)
		placed[k] = true
	}

	if len(result) == 0 {
		delete(c.serviceEndpoints, serviceName)
	} else {
		c.serviceEndpoints[serviceName] = result
	}

	if !c.DisableLogging {
		log.Println("Reconciled Service Endpoints for " + serviceName + " (version '" + versionNorm +
			"'): " + util.Itoa(len(result)) + " kept, " + util.Itoa(pruned) + " pruned")
	}
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

	if c.serviceEndpoints == nil {
		return
	}

	serviceName = normalizeServiceName(serviceName)
	if serviceName == "" { // guard empty key after normalization
		return
	}
	if !c.DisableLogging {
		log.Println("Cached Service Endpoints Purged for " + serviceName)
	}

	delete(c.serviceEndpoints, serviceName)
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
	if hostNormalized == "" { // guard empty after normalization
		return
	}
	if !c.DisableLogging {
		log.Println("Cached Service Endpoint Purging " + hostNormalized + ":" + util.UintToStr(port) + " From " + serviceName + "...")
	}

	eps, ok := c.serviceEndpoints[serviceName]

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
		if v == nil { // FIX: drop nil entries instead of retaining them
			continue
		}
		vHost := strings.ToLower(strings.TrimSpace(v.Host))
		if vHost == "" || v.Port == 0 { // drop invalid cached entries while purging
			continue
		}
		if vHost == hostNormalized && v.Port == port {
			// found
			found = true
			continue
		}
		newEps = append(newEps, v)
	}

	if found {
		if len(newEps) == 0 {
			delete(c.serviceEndpoints, serviceName)
		} else {
			c.serviceEndpoints[serviceName] = newEps
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

	ignExp := false
	if len(ignoreExpired) > 0 {
		ignExp = ignoreExpired[0]
	}

	serviceName = normalizeServiceName(serviceName) // normalize before any other work
	if serviceName == "" {                          // guard empty normalized name
		return []*serviceEndpoint{}
	}

	versionNormalized := strings.ToLower(strings.TrimSpace(version))

	c._mu.Lock()
	defer c._mu.Unlock()

	if c.serviceEndpoints == nil {
		return []*serviceEndpoint{}
	}

	eps, ok := c.serviceEndpoints[serviceName]
	if !ok || len(eps) == 0 {
		if !c.DisableLogging {
			log.Println("Get Live Service Endpoints for " + serviceName + ", version '" + versionNormalized + "': " + "None Found")
		}
		return []*serviceEndpoint{}
	}

	if !c.DisableLogging {
		log.Println("Get Live Service Endpoints for " + serviceName + ", version '" + versionNormalized + "': " + util.Itoa(len(eps)) + " Found")
	}

	now := time.Now()
	expiredList := []int{}
	live := make([]*serviceEndpoint, 0, len(eps))

	for i, v := range eps {
		if v == nil { // guard nil to avoid panic
			expiredList = append(expiredList, i)
			continue
		}

		// remove unusable entries with empty host or zero port
		if util.LenTrim(v.Host) == 0 || v.Port == 0 {
			expiredList = append(expiredList, i)
			continue
		}

		vVersion := strings.ToLower(strings.TrimSpace(v.Version))
		isExpired := !v.CacheExpire.IsZero() && v.CacheExpire.Before(now)

		if isExpired && !ignExp {
			if !c.DisableLogging {
				log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + versionNormalized + "': (Expired) " + v.Host + ":" + util.UintToStr(v.Port))
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
				log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + versionNormalized + "': (Version Mismatch) " + v.Version + " vs " + versionNormalized)
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
			delete(c.serviceEndpoints, serviceName)
		} else {
			c.serviceEndpoints[serviceName] = newEps
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
		log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + versionNormalized + "': " + util.Itoa(len(liveEndpoints)) + " Returned")
	}
	return
}
