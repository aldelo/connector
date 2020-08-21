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
	"context"
	"fmt"
	util "github.com/aldelo/common"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

type Client struct {
	ServiceName string // (required) registered service name
	ServiceAddr string // (optional) direct host:port [leave blank so registry finds it]

	_conn *grpc.ClientConn
	_remoteAddress string
}

func (c *Client) Dial(opts ...grpc.DialOption) error {
	return c.DialWithContext(context.Background(), opts...)
}

func (c *Client) DialWithContext(ctx context.Context, opts ...grpc.DialOption) error {
	c._remoteAddress = ""

	if util.LenTrim(c.ServiceName) == 0 {
		return fmt.Errorf("Dial Service Endpoint Failed: %s", "Service Name Not Defined")
	}

	addr := c.ServiceAddr

	if util.LenTrim(addr) == 0 {
		// get current service address from registry
		// TODO:

		if util.LenTrim(addr) == 0 {
			return fmt.Errorf("Dial Service Endpoint %s Failed: %s", c.ServiceName, "Endpoint Address Not Found in Registry")
		}
	}

	// dial connection to service host
	var err error

	if len(opts) == 0 {
		opts = append(opts, grpc.WithInsecure())
	}

	if c._conn, err = grpc.DialContext(ctx, addr, opts...); err != nil {
		return fmt.Errorf("Dial Service Endpoint %s to %s Failed: %s", c.ServiceName, addr, err.Error())
	} else {
		// dial grpc server success
		c._remoteAddress = addr
		return nil
	}
}

func (c *Client) GetState() connectivity.State {
	if c._conn != nil {
		return c._conn.GetState()
	} else {
		return connectivity.Shutdown
	}
}

func (c *Client) Close() {
	c._remoteAddress = ""

	if c._conn != nil {
		_ = c._conn.Close()
	}
}

func (c *Client) RemoteAddress() string {
	return c._remoteAddress
}


