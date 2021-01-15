package client

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
	util "github.com/aldelo/common"
	"log"
	"strings"
	"sync"
	"time"
)

type Cache struct {
	ServiceEndpoints map[string][]*serviceEndpoint

	_mu sync.Mutex
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

	c._mu.Lock()
	defer c._mu.Unlock()

	if c.ServiceEndpoints == nil {
		c.ServiceEndpoints = make(map[string][]*serviceEndpoint)
	}

	serviceName = strings.ToLower(serviceName)

	if list, ok := c.ServiceEndpoints[serviceName]; !ok {
		log.Println("Adding New Service Endpoints for " + serviceName)
		c.ServiceEndpoints[serviceName] = eps
	} else {
		log.Println("Appending New Service Endpoints for " + serviceName)
		set := make(map[string]*serviceEndpoint)

		for _, v := range list {
			k := strings.ToLower(v.Host + ":" + util.UintToStr(v.Port))
			set[k] = v
		}

		for _, v := range eps {
			k := strings.ToLower(v.Host + ":" + util.UintToStr(v.Port))
			set[k] = v
		}

		lst := []*serviceEndpoint{}

		for _, v := range set {
			lst = append(lst, v)
		}

		c.ServiceEndpoints[serviceName] = lst
	}
}

// PurgeServiceEndpoints will remove all endpoints associated with the given serviceName within map
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) PurgeServiceEndpoints(serviceName string) {
	if util.LenTrim(serviceName) == 0 {
		return
	}

	if c.ServiceEndpoints == nil {
		return
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	serviceName = strings.ToLower(serviceName)
	log.Println("Cached Service Endpoints Purged for " + serviceName)

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
	log.Println("Cached Service Endpoint Purging " + host + ":" + util.UintToStr(port) + " From " + serviceName + "...")

	eps, ok := c.ServiceEndpoints[serviceName]

	if !ok {
		// no service endpoints found for service name
		log.Println("Cached Service Endpoint Purging OK: No Endpoints Found for " + serviceName)
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
					c.ServiceEndpoints[serviceName] = newEps
				}
			}

			log.Println("Cached Service Endpoint Purging OK: Endpoint '" + host + ":" + util.UintToStr(port) + "' Removed From " + serviceName)

		} else {
			log.Println("Cached Service Endpoint Purging OK: No Endpoint Found '" + host + ":" + util.UintToStr(port) + "' In " + serviceName)
		}
	}
}

// GetLiveServiceEndpoints will retrieve currently non-expired service endpoints and remove any expired service endpoints from map,
// for a given serviceName
//
// serviceName = lowercase of servicename.namespacename
func (c *Cache) GetLiveServiceEndpoints(serviceName string, version string) (liveEndpoints []*serviceEndpoint) {
	if util.LenTrim(serviceName) == 0 {
		return []*serviceEndpoint{}
	}

	if c.ServiceEndpoints == nil {
		return []*serviceEndpoint{}
	}

	c._mu.Lock()
	defer c._mu.Unlock()

	serviceName = strings.ToLower(serviceName)

	eps, ok := c.ServiceEndpoints[serviceName]

	if !ok {
		log.Println("Get Live Service Endpoints for " + serviceName + ", version '" + version + "': " + "None Found")
		return []*serviceEndpoint{}
	}

	if len(eps) > 0{
		log.Println("Get Live Service Endpoints for " + serviceName + ", version '" + version + "': " + util.Itoa(len(eps)) + " Found")
		expiredList := []int{}

		for i, v := range eps{
			if v.CacheExpire.After(time.Now()){
				// not yet expired
				if util.LenTrim(version) > 0 {
					// has version, check to match version
					if strings.ToLower(v.Version) != strings.ToLower(version) {
						log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + version + "': (Version Mismatch) " + v.Version + " vs " + version)
						continue
					}
				}

				liveEndpoints = append(liveEndpoints, v)
			} else{
				log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + version + "': (Expired) " + v.Host + ":" + util.UintToStr(v.Port))
				expiredList = append(expiredList, i)
			}
		}

		// remove expired entries
		if len(expiredList) > 0 {
			for _, v := range expiredList {
				eps[v] = nil
			}

			newEps := []*serviceEndpoint{}

			for _, v := range eps {
				if v != nil {
					newEps = append(newEps, v)
				}
			}

			c.ServiceEndpoints[serviceName] = newEps
		}

		log.Println("Get Live Service Endpoints for " + serviceName + ", Version '" + version + "': " + util.Itoa(len(liveEndpoints)) + " Returned")
		return
	} else {
		return []*serviceEndpoint{}
	}
}



