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
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
)

// ---------------------------------------------------------------------------
// parseDialFailureHostPort — pure parser (the plan flags mis-parsing as the key risk)
// ---------------------------------------------------------------------------

func TestParseDialFailureHostPort_RealGrpcMessages(t *testing.T) {
	cases := []struct {
		name     string
		errMsg   string
		wantHost string
		wantPort uint
	}{
		{
			name:     "connection refused ipv4",
			errMsg:   `rpc error: code = Unavailable desc = connection error: desc = "transport: error while dialing: dial tcp 10.2.100.204:37387: connect: connection refused"`,
			wantHost: "10.2.100.204",
			wantPort: 37387,
		},
		{
			name:     "i/o timeout ipv4",
			errMsg:   `transport: error while dialing: dial tcp 10.2.136.233:35965: i/o timeout`,
			wantHost: "10.2.136.233",
			wantPort: 35965,
		},
		{
			name:     "no route to host ipv4",
			errMsg:   `transport: error while dialing: dial tcp 10.2.129.150:37065: connect: no route to host`,
			wantHost: "10.2.129.150",
			wantPort: 37065,
		},
		{
			name:     "connection timed out ipv4",
			errMsg:   `transport: error while dialing: dial tcp 10.2.116.228:33141: connect: connection timed out`,
			wantHost: "10.2.116.228",
			wantPort: 33141,
		},
		{
			name:     "ipv6 bracketed",
			errMsg:   `transport: error while dialing: dial tcp [2001:db8::1]:8443: connect: connection refused`,
			wantHost: "2001:db8::1",
			wantPort: 8443,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, port, ok := parseDialFailureHostPort(tc.errMsg)
			if !ok {
				t.Fatalf("expected ok=true for %q", tc.errMsg)
			}
			if host != tc.wantHost || port != tc.wantPort {
				t.Fatalf("parsed %s:%d, want %s:%d", host, port, tc.wantHost, tc.wantPort)
			}
		})
	}
}

func TestParseDialFailureHostPort_RejectsNonDialErrors(t *testing.T) {
	// None of these are dial failures; the parser must NOT extract an endpoint (so an
	// app-level error that merely contains a host:port cannot be mistaken for a dead endpoint).
	cases := []string{
		``,
		`rpc error: code = NotFound desc = merchant 10.2.0.5:80 not found`, // host:port in payload, not a dial failure
		`rpc error: code = InvalidArgument desc = bad request`,
		`hystrix: circuit open`,
		`context deadline exceeded`,
		`transport: error while dialing: dial tcp: missing port`,          // no host:port
		`transport: error while dialing: dial tcp 10.2.0.5: no colon`,     // no port
		`transport: error while dialing: dial tcp 10.2.0.5:0: bad port`,   // port 0 invalid
		`transport: error while dialing: dial tcp 10.2.0.5:99999: bad`,    // port out of range
		`some error mentioning dial tcp 10.2.0.5:80: but not a dial fail`, // lacks "error while dialing"
	}
	for _, msg := range cases {
		if host, port, ok := parseDialFailureHostPort(msg); ok {
			t.Errorf("expected ok=false for %q, got %s:%d", msg, host, port)
		}
	}
}

// ---------------------------------------------------------------------------
// unaryDeadEndpointFailoverHandler — gating behavior (no live conn required)
// ---------------------------------------------------------------------------

// countingInvoker returns a UnaryInvoker that records how many times it was called and
// returns the given error on every call.
func countingInvoker(calls *int, retErr error) grpc.UnaryInvoker {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, opts ...grpc.CallOption) error {
		*calls++
		return retErr
	}
}

func failoverTestClient(enabled bool) *Client {
	c := &Client{}
	c._config.Store(&config{
		Target: targetData{
			ServiceName:          "dal-config-read-get-service",
			NamespaceName:        "live.aldelo.nae",
			ServiceDiscoveryType: "api",
		},
		Grpc: grpcData{
			UseLoadBalancer:           true,
			EvictOnDialFailureEnabled: enabled,
		},
	})
	return c
}

func TestUnaryDeadEndpointFailover_SuccessPassesThroughNoRetry(t *testing.T) {
	c := failoverTestClient(true)
	calls := 0
	err := c.unaryDeadEndpointFailoverHandler(context.Background(), "/svc/M", nil, nil, nil, countingInvoker(&calls, nil))
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 invoke on success, got %d", calls)
	}
}

func TestUnaryDeadEndpointFailover_DisabledDoesNotRetry(t *testing.T) {
	c := failoverTestClient(false) // feature off
	calls := 0
	dialErr := errors.New(`transport: error while dialing: dial tcp 10.2.100.204:37387: connect: connection refused`)
	err := c.unaryDeadEndpointFailoverHandler(context.Background(), "/svc/M", nil, nil, nil, countingInvoker(&calls, dialErr))
	if err == nil {
		t.Fatalf("expected the dial error to pass through")
	}
	if calls != 1 {
		t.Fatalf("feature disabled: expected exactly 1 invoke (no retry), got %d", calls)
	}
}

func TestUnaryDeadEndpointFailover_NonDialErrorDoesNotRetry(t *testing.T) {
	c := failoverTestClient(true)
	calls := 0
	appErr := errors.New(`rpc error: code = NotFound desc = merchant not found`)
	err := c.unaryDeadEndpointFailoverHandler(context.Background(), "/svc/M", nil, nil, nil, countingInvoker(&calls, appErr))
	if err == nil {
		t.Fatalf("expected the app error to pass through")
	}
	if calls != 1 {
		t.Fatalf("non-dial error: expected exactly 1 invoke (no retry), got %d", calls)
	}
}

func TestUnaryDeadEndpointFailover_DirectConnectDoesNotRetry(t *testing.T) {
	c := failoverTestClient(true)
	// direct discovery has no alternate endpoints to fail over to => must not retry.
	cfg := c.getConfig()
	cfg.Target.ServiceDiscoveryType = "direct"
	c._config.Store(cfg)

	calls := 0
	dialErr := errors.New(`transport: error while dialing: dial tcp 10.2.100.204:37387: connect: connection refused`)
	err := c.unaryDeadEndpointFailoverHandler(context.Background(), "/svc/M", nil, nil, nil, countingInvoker(&calls, dialErr))
	if err == nil {
		t.Fatalf("expected the dial error to pass through")
	}
	if calls != 1 {
		t.Fatalf("direct connect: expected exactly 1 invoke (no retry), got %d", calls)
	}
}

func TestUnaryDeadEndpointFailover_ClosedClientReturnsUnavailable(t *testing.T) {
	c := failoverTestClient(true)
	c.closed.Store(true)
	calls := 0
	err := c.unaryDeadEndpointFailoverHandler(context.Background(), "/svc/M", nil, nil, nil, countingInvoker(&calls, nil))
	if err == nil {
		t.Fatalf("expected an error when client is closed")
	}
	if calls != 0 {
		t.Fatalf("closed client: invoker must not be called, got %d calls", calls)
	}
}
