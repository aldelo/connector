package client

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
		log.Println("Adding New Service Endpoints")
		c.ServiceEndpoints[serviceName] = eps
	} else {
		log.Println("Appending New Service Endpoints")
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

	delete(c.ServiceEndpoints, serviceName)
}

// GetLiveServiceEndpoints will retrieve currently non-expired service endpoints and remove any expired service endpoints from map,
// for a given serviceName
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
		log.Println("Get Live Service Endpoints: " + "None Found")
		return []*serviceEndpoint{}
	}

	if len(eps) > 0{
		log.Println("Get Live Service Endpoints: " + util.Itoa(len(eps)) + " Found")
		expiredList := []int{}

		for i, v := range eps{
			if v.CacheExpire.After(time.Now()){
				// not yet expired
				if util.LenTrim(version) > 0 {
					if strings.ToLower(v.Version) != strings.ToLower(version) {
						log.Println("Get Live Service Endpoints: (Version Mismatch) " + v.Version + " vs " + version)
						continue
					}
				}

				liveEndpoints = append(liveEndpoints, v)
			} else{
				log.Println("Get Live Service Endpoints: (Expired) " + v.Host + ":" + util.UintToStr(v.Port))
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

		log.Println("Get Live Service Endpoints: " + util.Itoa(len(liveEndpoints)) + " Returned")
		return
	} else {
		return []*serviceEndpoint{}
	}
}



