package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/aldelo/connector/adapters/health"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
)

// startHealthTestServer serves only the connector's own health service, which
// does not import google.golang.org/grpc/health. The client-side health check
// function is therefore registered solely by client.go's blank import; if that
// import is removed, this test must fail.
func startHealthTestServer(t *testing.T, overall grpc_health_v1.HealthCheckResponse_ServingStatus, marker string) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	handlers := map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus{}
	if marker != "" {
		handlers[marker] = func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus {
			return grpc_health_v1.HealthCheckResponse_SERVING
		}
	}

	gs := grpc.NewServer()
	grpc_health_v1.RegisterHealthServer(gs, health.NewHealthServer(
		func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus { return overall },
		handlers,
	))

	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	return lis.Addr().String()
}

// TestClientHealthCheck_SkipsNotServingBackend proves client-side health checking is
// active: with round_robin + healthCheckConfig, every RPC must land on the SERVING
// backend. Without the grpc/health import, health checking is silently disabled,
// round_robin also picks the NOT_SERVING backend, and the probe below gets NotFound.
func TestClientHealthCheck_SkipsNotServingBackend(t *testing.T) {
	const marker = "only-on-serving-backend"

	servingAddr := startHealthTestServer(t, grpc_health_v1.HealthCheckResponse_SERVING, marker)
	notServingAddr := startHealthTestServer(t, grpc_health_v1.HealthCheckResponse_NOT_SERVING, "")

	r := manual.NewBuilderWithScheme("healthimporttest")
	r.InitialState(resolver.State{Addresses: []resolver.Address{
		{Addr: servingAddr},
		{Addr: notServingAddr},
	}})

	conn, err := grpc.NewClient(r.Scheme()+":///test",
		grpc.WithResolvers(r),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}], `+healthCheckServiceConfigFragment("")+`}`),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer conn.Close()

	hc := grpc_health_v1.NewHealthClient(conn)

	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		resp, err := hc.Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: marker}, grpc.WaitForReady(true))
		cancel()

		if err != nil {
			t.Fatalf("call %d reached the NOT_SERVING backend (client health checking inactive?): %v", i, err)
		}
		if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
			t.Fatalf("call %d: status = %v, want SERVING", i, resp.GetStatus())
		}
	}
}
