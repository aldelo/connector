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

// AddServiceEndpoints will append slice of service endpoints associated with the given serviceName within map
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) AddServiceEndpoints(serviceName string, eps []*serviceEndpoint) {
	if util.LenTrim(serviceName) == 0 {
		return
	}

	if len(eps) == 0 {
		return
	}

	// sanitize incoming endpoints to drop nil entries and avoid panics
	sanitized := make([]*serviceEndpoint, 0, len(eps))
	for _, v := range eps {
		if v != nil {
			sanitized = append(sanitized, v)
		}
	}
	if len(sanitized) == 0 {
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	if c.ServiceEndpoints == nil {
		c.ServiceEndpoints = make(map[string][]*serviceEndpoint)
	}

	serviceName = strings.ToLower(serviceName)

	// stable, deduplicated merge (preserve existing order, then new)
	seen := make(map[string]struct{})
	merged := make([]*serviceEndpoint, 0)

	if list, ok := c.ServiceEndpoints[serviceName]; ok {
		for _, v := range list {
			if v == nil {
				continue
			}
			k := strings.ToLower(v.Host + ":" + util.UintToStr(v.Port) + "|" + strings.ToLower(strings.TrimSpace(v.Version)))
			if _, exists := seen[k]; exists {
				continue
			}
			seen[k] = struct{}{}
			merged = append(merged, v)
		}
		if !c.DisableLogging {
			log.Println("Adding New Service Endpoints for " + serviceName)
		}
	} else {
		if !c.DisableLogging {
			log.Println("Appending New Service Endpoints for " + serviceName)
		}
	}

	for _, v := range sanitized {
		k := strings.ToLower(v.Host + ":" + util.UintToStr(v.Port) + "|" + strings.ToLower(strings.TrimSpace(v.Version)))
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
	if util.LenTrim(serviceName) == 0 {
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	if c.ServiceEndpoints == nil {
		return
	}

	serviceName = strings.ToLower(serviceName)
	if !c.DisableLogging {
		log.Println("Cached Service Endpoints Purged for " + serviceName)
	}

	delete(c.ServiceEndpoints, serviceName)
}

// PurgeServiceEndpointByHostAndPort will remove a specific endpoint for a service based on host and port info
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) PurgeServiceEndpointByHostAndPort(serviceName string, host string, port uint) {
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

	serviceName = strings.ToLower(serviceName)
	if !c.DisableLogging {
		log.Println("Cached Service Endpoint Purging " + host + ":" + util.UintToStr(port) + " From " + serviceName + "...")
	}

	eps, ok := c.ServiceEndpoints[serviceName]

	if !ok {
		// no service endpoints found for service name
		if !c.DisableLogging {
			log.Println("Cached Service Endpoint Purging OK: No Endpoints Found for " + serviceName)
		}
	} else {
		removeIndex := -1

		for idx, v := range eps {
			if v != nil && strings.ToLower(v.Host) == strings.ToLower(host) && v.Port == port {
				// found
				removeIndex = idx
				break
			}
		}

		if removeIndex >= 0 {
			if s := util.SliceDeleteElement(eps, removeIndex); s == nil {
				delete(c.ServiceEndpoints, serviceName)
			} else {
				if newEps, epsOk := s.([]*serviceEndpoint); !epsOk {
					delete(c.ServiceEndpoints, serviceName)
				} else {
					// also remove the map entry if the resulting slice is empty.
					if len(newEps) == 0 {
						delete(c.ServiceEndpoints, serviceName)
					} else {
						c.ServiceEndpoints[serviceName] = newEps
					}
				}
			}

			if !c.DisableLogging {
				log.Println("Cached Service Endpoint Purging OK: Endpoint '" + host + ":" + util.UintToStr(port) + "' Removed From " + serviceName)
			}

		} else {
			if !c.DisableLogging {
				log.Println("Cached Service Endpoint Purging OK: No Endpoint Found '" + host + ":" + util.UintToStr(port) + "' In " + serviceName)
			}
		}
	}
}

// GetLiveServiceEndpoints will retrieve currently non-expired service endpoints and remove any expired service endpoints from map,
// for a given serviceName
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) GetLiveServiceEndpoints(serviceName string, version string, ignoreExpired ...bool) (liveEndpoints []*serviceEndpoint) {
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

	serviceName = strings.ToLower(serviceName)

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
	needsCompaction := false

	if !ignExp {
		for i, v := range eps {
			if v == nil { // guard nil to avoid panic
				expiredList = append(expiredList, i)
				continue
			}

			if v.CacheExpire.After(now) {
				if util.LenTrim(version) > 0 && strings.ToLower(v.Version) != strings.ToLower(version) {
					// has version, check to match version
					if !c.DisableLogging {
						log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + version + "': (Version Mismatch) " + v.Version + " vs " + version)
					}
					continue
				}
				live = append(live, v)
			} else {
				if !c.DisableLogging {
					log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + version + "': (Expired) " + v.Host + ":" + util.UintToStr(v.Port))
				}
				expiredList = append(expiredList, i)
			}
		}
	} else {
		for _, v := range eps {
			if v == nil { // skip nil to avoid panic
				needsCompaction = true
				continue
			}
			live = append(live, v)
		}
	}

	// remove expired entries
	if len(expiredList) > 0 || needsCompaction {
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

		c.ServiceEndpoints[serviceName] = newEps
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
