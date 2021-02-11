package resolver

/*
 * Copyright 2020-2021 Aldelo, LP
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
)

func NewManualResolver(schemeName string, endpointAddrs []string) error {
	if util.LenTrim(schemeName) == 0 {
		schemeName = "clb"
	}

	if len(endpointAddrs) == 0 {
		return fmt.Errorf("Endpoint Address is Required")
	}

	// r, _ := manual.GenerateAndRegisterManualResolver()

	r :=  manual.NewBuilderWithScheme(schemeName)

	addrs := []resolver.Address{}

	for _, v := range endpointAddrs {
		addrs = append(addrs, resolver.Address{
			Addr: v,
		})
	}

	r.InitialState(resolver.State{
		Addresses: addrs,
	})

	var builder resolver.Builder
	builder = r

	resolver.Register(builder)
	resolver.SetDefaultScheme(schemeName)

	return nil
}