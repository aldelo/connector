package resolver

/*
 * Copyright 2020-2023 Aldelo, LP
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
	schemeMap map[string]*manual.Resolver
	_mux      sync.Mutex // thread-safety for accessing and modifying schemeMap
)

func NewManualResolver(schemeName string, serviceName string, endpointAddrs []string) error {
	if util.LenTrim(schemeName) == 0 {
		schemeName = "clb"
	}

	if len(endpointAddrs) == 0 {
		return fmt.Errorf("Endpoint Address is Required")
	}

	r := manual.NewBuilderWithScheme(schemeName)

	addrs := []resolver.Address{}

	for _, v := range endpointAddrs {
		addrs = append(addrs, resolver.Address{
			Addr: v,
		})
	}

	r.InitialState(resolver.State{
		Addresses: addrs,
	})

	setResolver(schemeName, serviceName, r)

	return nil
}

func UpdateManualResolver(schemeName string, serviceName string, endpointAddrs []string) error {
	if len(endpointAddrs) == 0 {
		return fmt.Errorf("Endpoint Addresses Required")
	}

	if util.LenTrim(schemeName) == 0 {
		return fmt.Errorf("SchemeName is Required")
	}

	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("ServiceName is Required")
	}

	r, err := getResolver(schemeName, serviceName)
	if err != nil {
		return err
	}

	addrs := []resolver.Address{}

	for _, v := range endpointAddrs {
		addrs = append(addrs, resolver.Address{
			Addr: v,
		})
	}

	r.UpdateState(resolver.State{
		Addresses: addrs,
	})

	log.Println("[NOTE] Please Ignore Error 'Health check is requested but health check function is not set'; UpdateManualResolver is Completed")

	return nil
}

func setResolver(schemeName string, serviceName string, r *manual.Resolver) {
	_mux.Lock()
	defer _mux.Unlock()

	if schemeMap == nil {
		schemeMap = make(map[string]*manual.Resolver)
	}

	schemeMap[strings.ToLower(schemeName+serviceName)] = r

	// 1/9/2025 by yao.bin - Caution: There is a global map in "grpc/resolver", so we have to put "resolver.Register" in mutex lock too
	var builder resolver.Builder
	builder = r

	resolver.Register(builder)
	//resolver.SetDefaultScheme(r.Scheme())
}

func getResolver(schemeName string, serviceName string) (*manual.Resolver, error) {
	_mux.Lock()
	defer _mux.Unlock()

	if schemeMap == nil {
		return nil, fmt.Errorf("Resolver SchemeMap Nil")
	}

	if r := schemeMap[strings.ToLower(schemeName+serviceName)]; r != nil {
		return r, nil
	} else {
		return nil, fmt.Errorf("No Resolver Found for SchemeName '" + schemeName + "' in SchemeMap")
	}
}
