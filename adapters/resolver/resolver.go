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
	util "github.com/aldelo/common"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
	"log"
	"strings"
)

var schemeMap map[string]*manual.Resolver

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

	if schemeMap == nil {
		schemeMap = make(map[string]*manual.Resolver)
	}

	schemeMap[strings.ToLower(schemeName+serviceName)] = r

	var builder resolver.Builder
	builder = r

	resolver.Register(builder)
	//resolver.SetDefaultScheme(r.Scheme())

	return nil
}

func UpdateManualResolver(schemeName string, serviceName string, endpointAddrs []string) error {
	if schemeMap == nil {
		return fmt.Errorf("Resolver SchemeMap Nil")
	}

	if len(endpointAddrs) == 0 {
		return fmt.Errorf("Endpoint Addresses Required")
	}

	if util.LenTrim(schemeName) == 0 {
		return fmt.Errorf("SchemeName is Required")
	}

	if util.LenTrim(serviceName) == 0 {
		return fmt.Errorf("ServiceName is Required")
	}

	if r := schemeMap[strings.ToLower(schemeName+serviceName)]; r != nil {
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
	} else {
		return fmt.Errorf("No Resolver Found for SchemeName '" + schemeName + "' in SchemeMap")
	}
}
