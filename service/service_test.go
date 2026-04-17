package service

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
	"context"
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/cloudmap"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/route53"
	testpb "github.com/aldelo/connector/example/proto/test"
	"github.com/aldelo/connector/internal/safego"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

type AnswerServiceImpl struct {
	testpb.UnimplementedAnswerServiceServer
}

func (a *AnswerServiceImpl) Greeting(ctx context.Context, q *testpb.Question) (*testpb.Answer, error) {
	ans := q.Question + " = " + "Ok, Thanks!"
	return &testpb.Answer{Answer: ans}, nil
}

func TestService_Serve(t *testing.T) {
	// P1-6: This is a manual integration test. Serve() starts a real
	// gRPC listener, connects to AWS CloudMap for service discovery,
	// and blocks on awaitOsSigExit() waiting for a real OS signal.
	// Previously this test ran to the 10-minute test-framework timeout
	// and panicked, masking every other test in the package.
	//
	// CI safety: skip unless CONNECTOR_RUN_INTEGRATION=1 is set.
	// When enabled, spawn a watchdog goroutine that sends SIGTERM to
	// ourselves after a short window — since Serve() registers a
	// signal.Notify handler for SIGTERM, the signal routes to the
	// handler channel (unblocking awaitOsSigExit) rather than killing
	// the test process. Allows the happy path to be exercised when a
	// developer has valid AWS credentials + config, while guaranteeing
	// the test can never again hang CI.
	if os.Getenv("CONNECTOR_RUN_INTEGRATION") != "1" {
		t.Skip("skipping: manual integration test (requires real AWS CloudMap + service.yaml); set CONNECTOR_RUN_INTEGRATION=1 to run")
	}

	// Watchdog: send SIGTERM to ourselves after Serve has had a chance
	// to register its signal handler. Kept short so a broken Serve
	// doesn't hold the test suite up. Uses a channel-gated cancel so
	// the goroutine exits cleanly if the test ends via an early error.
	cancelWatchdog := make(chan struct{})
	go func() {
		select {
		case <-time.After(2 * time.Second):
			p, err := os.FindProcess(os.Getpid())
			if err == nil {
				_ = p.Signal(syscall.SIGTERM)
			}
		case <-cancelWatchdog:
			return
		}
	}()
	t.Cleanup(func() { close(cancelWatchdog) })

	svc := NewService("testservice", "service", "", func(grpcServer *grpc.Server) {
		testpb.RegisterAnswerServiceServer(grpcServer, &AnswerServiceImpl{})
	})

	svc.BeforeServerStart = func(svc *Service) {
		log.Println("In - Before Server Start")
	}

	svc.AfterServerStart = func(svc *Service) {
		log.Println("In - After Server Start")
	}

	svc.BeforeServerShutdown = func(svc *Service) {
		log.Println("In - Before Server Shutdown")
	}

	svc.AfterServerShutdown = func(svc *Service) {
		log.Println("In - After Server Shutdown")
	}

	svc.DefaultHealthCheckHandler = func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus {
		log.Println("In - Default Health Check")
		return grpc_health_v1.HealthCheckResponse_SERVING
	}

	svc.ServiceHealthCheckHandlers = map[string]func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus{
		"Test": func(ctx context.Context) grpc_health_v1.HealthCheckResponse_ServingStatus {
			log.Println("In - Service Health Check")
			return grpc_health_v1.HealthCheckResponse_SERVING
		},
	}

	svc.UnaryServerInterceptors = []grpc.UnaryServerInterceptor{
		func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
			log.Println("In - Unary Server Interceptor: " + info.FullMethod)

			return handler(ctx, req)
		},
	}

	svc.StreamServerInterceptors = []grpc.StreamServerInterceptor{
		func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			log.Println("In - Stream Server Interceptor: " + info.FullMethod)
			return handler(srv, ss)
		},
	}

	// svc.StatsHandler = grpc.StatsHandler()

	svc.UnknownStreamHandler = func(srv interface{}, stream grpc.ServerStream) error {
		log.Println("In - Unknown Stream Handler")
		return nil
	}

	if err := svc.Serve(); err != nil {
		t.Fatal(err)
	}
}

// TestService_GracefulStop_ReleasesRealServe is the F1 / SVC-F8
// regression test. Unlike TestService_GracefulStop_ReleasesFakeServe
// in service_svc_f7_test.go (which models awaitOsSigExit with a
// time.Sleep), this test invokes the REAL Serve() and the REAL
// programmatic GracefulStop() path, and asserts Serve() returns within
// a bounded wall-clock window.
//
// Before SVC-F8, Serve()'s main goroutine was parked in awaitOsSigExit
// reading from a function-local done channel fed by a sig-demux
// goroutine consuming from signal.Notify. The SVC-F7 unified quit
// routing correctly drove teardown but never unblocked awaitOsSigExit,
// so Serve()'s main goroutine stayed parked until a real OS signal
// arrived — a zombie-process hazard on programmatic-only stop paths.
// SVC-F8 fixes this by self-delivering SIGTERM from GracefulStop /
// ImmediateStop before the unified quit-send; signal.Notify routes
// the signal to the Go channel rather than the process default
// handler, so the self-signal wakes awaitOsSigExit without killing
// the process.
//
// Regression check:
//   - Without SVC-F8: this test hangs at svc.Serve() forever (watchdog
//     fires at `stopDelay + serveDeadline` and fails the test).
//   - With SVC-F8: Serve() returns nil shortly after GracefulStop()
//     completes.
//
// CI safety: gated by CONNECTOR_RUN_INTEGRATION=1 because Serve()
// requires real AWS CloudMap + service.yaml to pass its startup
// validation. Watchdog sends a real SIGTERM if the test blows its
// deadline, so the test CANNOT hang CI even on regression.
//
// Companion to F1 ticket at
// _src/docs/repos/connector/findings/2026-04-14-contrarian-pass3/
// F1-awaitossigexit-not-woken-by-programmatic-stop.md.
func TestService_GracefulStop_ReleasesRealServe(t *testing.T) {
	if os.Getenv("CONNECTOR_RUN_INTEGRATION") != "1" {
		t.Skip("skipping: F1/SVC-F8 real-Serve regression test (requires real AWS CloudMap + service.yaml); set CONNECTOR_RUN_INTEGRATION=1 to run")
	}

	const (
		stopDelay     = 2 * time.Second  // give Serve() time to register signal handlers + start the grpc listener
		serveDeadline = 15 * time.Second // hard upper bound — SVC-F8 should return within ~1s of GracefulStop
	)

	svc := NewService("testservice", "service", "", func(grpcServer *grpc.Server) {
		testpb.RegisterAnswerServiceServer(grpcServer, &AnswerServiceImpl{})
	})

	// Trigger GracefulStop from a separate goroutine after Serve has
	// had a chance to register its signal handlers. If SVC-F8 works,
	// Serve() will return nil promptly after GracefulStop returns.
	// If SVC-F8 is reverted, Serve() stays parked in awaitOsSigExit
	// forever — so we also arm a hard SIGTERM watchdog that fires
	// after the full serveDeadline to guarantee CI never hangs.
	var stopWg sync.WaitGroup
	stopWg.Add(1)
	go func() {
		defer stopWg.Done()
		time.Sleep(stopDelay)
		// This call must unblock Serve()'s awaitOsSigExit on its own
		// (via the SVC-F8 self-SIGTERM). We deliberately do NOT send
		// SIGTERM ourselves here — that would defeat the test.
		svc.GracefulStop()
	}()

	// Fail-safe watchdog: if Serve() is still blocked after
	// serveDeadline, send a real SIGTERM so the test process doesn't
	// hang forever. A regression will be visible as a test failure
	// message ("Serve() did not return ...") AND the watchdog will
	// still let the suite complete.
	watchdogFired := make(chan struct{})
	cancelWatchdog := make(chan struct{})
	go func() {
		select {
		case <-time.After(stopDelay + serveDeadline):
			close(watchdogFired)
			if p, err := os.FindProcess(os.Getpid()); err == nil {
				_ = p.Signal(syscall.SIGTERM)
			}
		case <-cancelWatchdog:
			return
		}
	}()

	serveStart := time.Now()
	serveErr := svc.Serve()
	serveElapsed := time.Since(serveStart)
	close(cancelWatchdog)
	stopWg.Wait()

	if serveErr != nil {
		t.Fatalf("Serve() returned error: %v", serveErr)
	}

	select {
	case <-watchdogFired:
		t.Fatalf("F1/SVC-F8 regression: Serve() hung past stopDelay+serveDeadline (%s) and only unblocked via watchdog SIGTERM — programmatic GracefulStop did not wake awaitOsSigExit", stopDelay+serveDeadline)
	default:
	}

	// Expect Serve to return very shortly after GracefulStop: the
	// delay is stopDelay (the wait before calling GracefulStop) plus
	// a small teardown window. We assert Serve did NOT take the full
	// watchdog deadline as a loose sanity check.
	if serveElapsed >= stopDelay+serveDeadline {
		t.Fatalf("Serve() took %s (>= watchdog deadline %s) — likely SVC-F8 regression", serveElapsed, stopDelay+serveDeadline)
	}
	t.Logf("Serve() released in %s after GracefulStop (SVC-F8 working)", serveElapsed)
}

func TestConfig(t *testing.T) {
	// SP-008 pass-5 soft-gap #2 fix: redirect the working directory to a
	// per-test tmp dir so config.Read()'s "create on first run" path
	// (service/config.go:652-655 → ViperConf.Save() → viperconf.go:319
	// `./ConfigName.yaml`) writes newtest.yaml into a Go-managed tmp
	// dir that is auto-cleaned, instead of persisting a stray
	// service/newtest.yaml in the working tree after `go test`.
	//
	// t.Chdir (Go 1.24+) restores the original CWD via t.Cleanup, so
	// this is safe to co-exist with other tests in the same binary.
	t.Chdir(t.TempDir())

	cfg := &config{
		AppName:        "test-connector-service",
		ConfigFileName: "newtest",
	}

	if err := cfg.Read(); err != nil {
		t.Fatal(err)
	} else {
		if cfg.Target.Region != "us-east-1" {
			t.Fatal("Setting Data Not Found: ", cfg.Target.Region)
		} else {
			t.Log("Success")
		}
	}
}

// TestCloudMap is a manual integration test that requires:
//   - A populated service.yaml with a real AWS region and Service.Id
//   - Live AWS CloudMap credentials and a pre-existing namespace+service
//
// Pre-fix: this test ran by default in CI, called sd.RegisterInstance
// against a config with an empty Service.Id, then log.Fatal'd on the
// "ServiceId is Required" error — killing the test binary and masking
// every test that ran after it. Now gated behind CONNECTOR_RUN_INTEGRATION
// to match TestService_Serve. P2-16 follow-up.
func TestCloudMap(t *testing.T) {
	if os.Getenv("CONNECTOR_RUN_INTEGRATION") != "1" {
		t.Skip("skipping: manual integration test (requires real AWS CloudMap + service.yaml); set CONNECTOR_RUN_INTEGRATION=1 to run")
	}

	cfg := &config{
		AppName:        "test-connector-service",
		ConfigFileName: "service",
	}

	if err := cfg.Read(); err != nil {
		t.Fatal(err)
	}

	sd := &cloudmap.CloudMap{
		AwsRegion: awsregion.GetAwsRegion(cfg.Target.Region),
	}

	_ = sd.Connect()
	defer sd.Disconnect()

	uuid := util.NewUUID()
	log.Println("UUID = " + uuid)

	opID, e := sd.RegisterInstance(cfg.Service.Id, "test-"+uuid, uuid, map[string]string{
		"AWS_INSTANCE_IPV4": "192.168.1.13",
		"AWS_INSTANCE_PORT": "9999",
	})

	if e != nil {
		t.Fatal(e) // P2-8: was log.Fatal — kills entire test binary
	} else {
		time.Sleep(3 * time.Second)
	}

	op, e2 := sd.GetOperation(opID)

	if e2 != nil {
		t.Fatal(e2) // P2-8: was log.Fatal — kills entire test binary
	}

	id := op.Targets["INSTANCE"]

	if util.LenTrim(*id) > 0 {
		log.Println("Instance = " + *id)
	} else {
		t.Fatal("Instance empty") // P2-8: was log.Fatal — kills entire test binary
	}

	found, e3 := sd.DiscoverInstances(cfg.Namespace.Name, cfg.Service.Name, true, nil, nil)

	if e3 != nil {
		t.Fatal(e3) // P2-8: was log.Fatal — kills entire test binary
	}

	for _, v := range found {
		ip := v.Attributes["AWS_INSTANCE_IPV4"]
		port := v.Attributes["AWS_INSTANCE_PORT"]
		log.Println("IP Found = " + *ip + ":" + *port)
	}

}

// TestDNSLookup is a manual integration test that requires a custom
// internal DNS resolver capable of resolving helloservice.example.private.
// Skipped by default; set CONNECTOR_RUN_INTEGRATION=1 to run. P2-16 follow-up.
func TestDNSLookup(t *testing.T) {
	if os.Getenv("CONNECTOR_RUN_INTEGRATION") != "1" {
		t.Skip("skipping: manual integration test (requires custom internal DNS); set CONNECTOR_RUN_INTEGRATION=1 to run")
	}

	ips, err := net.LookupIP("helloservice.example.private")

	if err != nil {
		t.Fatal(err) // P2-8: was log.Fatal — kills entire test binary
	}

	for _, ip := range ips {
		log.Println("IP = " + ip.String())
	}
}

// TestGinServer is a manual integration test — RunServer binds to
// port 8080 and blocks indefinitely. Skipped by default. P2-16 follow-up.
func TestGinServer(t *testing.T) {
	if os.Getenv("CONNECTOR_RUN_INTEGRATION") != "1" {
		t.Skip("skipping: manual integration test (RunServer binds port 8080 and blocks); set CONNECTOR_RUN_INTEGRATION=1 to run")
	}

	// z := ginw.NewGinZapMiddleware("Example", true)

	g := ginw.NewServer("Example", 8080, true, false, nil)

	g.SessionMiddleware = &ginw.SessionConfig{
		SecretKey:    "Secret",
		SessionNames: []string{"MySession"},
	}

	g.CsrfMiddleware = &ginw.CsrfConfig{
		Secret: "Secrete",
	}

	/*
		g.Routes = map[string]*ginw.RouteDefinition{
			"base": {
				{
					Routes: []*ginw.Route{
						{
							RelativePath: "/hello",
							Method: ginhttpmethod.GET,
							Binding: ginbindtype.UNKNOWN,
							Handler: func(c *gin.Context, bindingInput interface{}) {
								c.String(200, "What's up")
							},
						},
					},
					MaxLimitMiddleware: util.IntPtr(10),
					PerClientQpsMiddleware: &ginw.PerClientQps{
						Qps: 100,
						Burst: 100,
						TTL: time.Hour,
					},
					GZipMiddleware: &ginw.GZipConfig{
						Compression: gingzipcompression.BestCompression,
					},
				},
			},
		}

	*/

	if err := g.RunServer(); err != nil {
		log.Println("Error: " + err.Error())
		t.Fatal("Fail")
	} else {
		log.Println("Run OK")
		t.Log("OK")
	}
}

type Dummy struct {
	FirstName string
	LastName  string
}

// TestSliceDeleteFunc exercises common.SliceDeleteElement against the
// connector's typical pointer-slice usage pattern. The canonical contract
// tests for the helper itself live in aldelo/common/helper-other_test.go
// (TestSliceDeleteElement_*) added in common P0-13 / v1.7.10. This test
// stays here as a smoke check that connector's go.mod pin is on a common
// version that includes the P0-13 fix — if connector's pin ever regresses
// below v1.7.10, this test will panic inside reflect.Value.Set just like
// it did before the fix.
func TestSliceDeleteFunc(t *testing.T) {
	slice := []*Dummy{
		{FirstName: "John", LastName: "Smith"},
		{FirstName: "Mike", LastName: "Smith"},
		{FirstName: "Anna", LastName: "Smith"},
	}

	resultSlice := util.SliceDeleteElement(slice, -1)

	newSlice, ok := resultSlice.([]*Dummy)
	if !ok {
		t.Fatalf("expected []*Dummy result, got %T", resultSlice)
	}
	if len(newSlice) != 2 {
		t.Fatalf("expected len 2 after deleting index -1 from length-3 slice, got %d", len(newSlice))
	}

	for _, v := range newSlice {
		t.Log(v)
	}
}

// TestRoute53AddRecordset is a manual integration test — references a
// hardcoded production HostedZoneId (Z002784014TW7VUT92MDD) and writes
// a real DNS record. Skipped by default. P2-16 follow-up.
func TestRoute53AddRecordset(t *testing.T) {
	if os.Getenv("CONNECTOR_RUN_INTEGRATION") != "1" {
		t.Skip("skipping: manual integration test (writes to live Route53 hosted zone); set CONNECTOR_RUN_INTEGRATION=1 to run")
	}

	r := &route53.Route53{}

	_ = r.Connect()
	defer r.Disconnect()

	if e := r.CreateUpdateResourceRecordset("Z002784014TW7VUT92MDD", "i-10-2-31-128.notifier-ws-aldelo.nae", "10.2.31.128", 60, "A"); e != nil {
		log.Println(e.Error())
	}

	log.Println("OK")
}

// TestRoute53DeleteRecordset is a manual integration test — same hardcoded
// HostedZoneId as TestRoute53AddRecordset, deletes a live DNS record.
// Skipped by default. P2-16 follow-up.
func TestRoute53DeleteRecordset(t *testing.T) {
	if os.Getenv("CONNECTOR_RUN_INTEGRATION") != "1" {
		t.Skip("skipping: manual integration test (deletes live Route53 record); set CONNECTOR_RUN_INTEGRATION=1 to run")
	}

	r := &route53.Route53{}

	_ = r.Connect()
	defer r.Disconnect()

	if e := r.DeleteResourceRecordset("Z002784014TW7VUT92MDD", "i-10-2-31-128.notifier-ws-aldelo.nae", "10.2.31.128", 60, "A"); e != nil {
		log.Println(e.Error())
	}

	log.Println("OK")
}

// ----------------------------------------------------------------------
// SVC-F1 (P0) regression tests for stopGRPCServerBounded.
//
// Prior to SVC-F1 the default signal path called *grpc.Server.Stop
// directly — contradicting the Service's lifecycle godoc and killing
// in-flight RPCs. These tests pin the helper that now wraps the signal
// path so the regression cannot silently re-land.
// ----------------------------------------------------------------------

// fakeGRPCServer implements gracefulStopper so the escalation logic can
// be exercised without a real listener. GracefulStop blocks on
// gracefulStopBlock until the test releases it (or Stop releases it, as
// real *grpc.Server does). Stop records that it was called.
type fakeGRPCServer struct {
	gracefulStopCalled chan struct{}
	gracefulStopBlock  chan struct{}
	stopCalled         chan struct{}

	releaseOnce sync.Once
}

func newFakeGRPCServer() *fakeGRPCServer {
	return &fakeGRPCServer{
		gracefulStopCalled: make(chan struct{}, 1),
		gracefulStopBlock:  make(chan struct{}),
		stopCalled:         make(chan struct{}, 1),
	}
}

func (f *fakeGRPCServer) GracefulStop() {
	select {
	case f.gracefulStopCalled <- struct{}{}:
	default:
	}
	<-f.gracefulStopBlock
}

func (f *fakeGRPCServer) Stop() {
	select {
	case f.stopCalled <- struct{}{}:
	default:
	}
	// Real *grpc.Server.Stop unblocks any pending GracefulStop; mirror
	// that so the helper's post-escalation `<-stopped` wait returns.
	f.releaseOnce.Do(func() { close(f.gracefulStopBlock) })
}

// releaseGraceful unblocks a pending GracefulStop without escalating
// to Stop (models the real gRPC happy path).
func (f *fakeGRPCServer) releaseGraceful() {
	f.releaseOnce.Do(func() { close(f.gracefulStopBlock) })
}

// TestStopGRPCServerBounded_FastPath verifies a GracefulStop that
// returns quickly never escalates to Stop. This is the contract that
// SVC-F1 restores on the default signal path.
func TestStopGRPCServerBounded_FastPath(t *testing.T) {
	f := newFakeGRPCServer()
	f.releaseGraceful() // GracefulStop returns immediately

	done := make(chan struct{})
	go func() {
		defer close(done)
		stopGRPCServerBounded(f, 5*time.Second)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("stopGRPCServerBounded did not return on fast path within 1s")
	}

	select {
	case <-f.gracefulStopCalled:
	default:
		t.Error("GracefulStop was not called on fast path")
	}

	select {
	case <-f.stopCalled:
		t.Error("Stop was called on fast path — escalation triggered incorrectly")
	default:
	}
}

// TestStopGRPCServerBounded_EscalatesOnTimeout verifies a stuck
// GracefulStop escalates to Stop before wall-clock blows — which is the
// reason SVC-F1 adds a bounded wait rather than a plain GracefulStop.
func TestStopGRPCServerBounded_EscalatesOnTimeout(t *testing.T) {
	f := newFakeGRPCServer()
	// Do not release gracefulStopBlock; force the timeout path.

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		stopGRPCServerBounded(f, 100*time.Millisecond)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stopGRPCServerBounded hung past timeout — escalation did not trigger")
	}
	elapsed := time.Since(start)
	if elapsed < 100*time.Millisecond {
		t.Errorf("returned before timeout elapsed: %s", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("returned too late (>2s) after 100ms timeout: %s", elapsed)
	}

	select {
	case <-f.gracefulStopCalled:
	default:
		t.Error("GracefulStop was not called — helper skipped the graceful attempt")
	}

	select {
	case <-f.stopCalled:
	default:
		t.Error("Stop was not called on timeout path — escalation missing")
	}
}

// TestStopGRPCServerBoundedWithHook_OrderingPostGraceExpiry verifies
// the C2-008 claim: the onEscalate hook (the SVC-F5 PostGraceExpiry
// fire site) runs strictly BEFORE gs.Stop() severs the transport when
// the graceful drain exceeds its timeout. Handlers observing
// ShutdownCtx on the PostGraceExpiry phase must see the cancel happen
// before in-flight streams are force-closed, otherwise they learn of
// the shutdown via a transport error instead of a cooperative signal.
//
// Mechanism: wrap fakeGRPCServer's Stop() with an order recorder that
// writes "stop" to a shared slice, and pass an onEscalate hook that
// writes "hook". Force the escalation path (no releaseGraceful) with a
// short timeout, then assert the slice is ["hook", "stop"] in order.
//
// Without the ordering guarantee, a future refactor could invoke
// gs.Stop() before the hook (e.g. by reordering the lines in
// stopGRPCServerBoundedWithHook) and rule #13 on the PostGraceExpiry
// phase would silently regress.
func TestStopGRPCServerBoundedWithHook_OrderingPostGraceExpiry(t *testing.T) {
	inner := newFakeGRPCServer()
	// Do not release gracefulStopBlock — force the timeout/escalation path.

	var mu sync.Mutex
	var order []string
	wrapped := &orderingRecorderGS{inner: inner, mu: &mu, order: &order}

	hook := func() {
		mu.Lock()
		order = append(order, "hook")
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		stopGRPCServerBoundedWithHook(wrapped, 50*time.Millisecond, hook)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stopGRPCServerBoundedWithHook hung past deadline — escalation did not complete")
	}

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()

	if len(got) != 2 {
		t.Fatalf("expected exactly 2 recorded events (hook, stop); got %d: %v", len(got), got)
	}
	if got[0] != "hook" || got[1] != "stop" {
		t.Fatalf("ordering violation: expected [hook stop]; got %v — C2-008 regression: PostGraceExpiry fire site must happen-before gs.Stop()", got)
	}

	select {
	case <-inner.gracefulStopCalled:
	default:
		t.Error("GracefulStop was not called — helper skipped the graceful attempt")
	}
	select {
	case <-inner.stopCalled:
	default:
		t.Error("Stop was not called — escalation missing")
	}
}

// orderingRecorderGS wraps a fakeGRPCServer and records Stop() calls
// in the shared order slice so the test can assert hook-before-stop
// ordering. The inner fake is reused because its GracefulStop/Stop
// plumbing (blocking channel + releaseOnce) is already correct.
type orderingRecorderGS struct {
	inner *fakeGRPCServer
	mu    *sync.Mutex
	order *[]string
}

func (o *orderingRecorderGS) GracefulStop() { o.inner.GracefulStop() }

func (o *orderingRecorderGS) Stop() {
	o.mu.Lock()
	*o.order = append(*o.order, "stop")
	o.mu.Unlock()
	o.inner.Stop()
}

// TestStopGRPCServerBoundedWithHook_FastPathSkipsHook verifies the
// complementary C2-008 invariant: when the graceful drain completes
// within the timeout, the onEscalate hook is NOT invoked. Consumers
// choosing ShutdownPhasePostGraceExpiry explicitly opt out of early
// cancellation, so firing the hook on the fast path would contradict
// the phase contract.
func TestStopGRPCServerBoundedWithHook_FastPathSkipsHook(t *testing.T) {
	f := newFakeGRPCServer()
	f.releaseGraceful() // GracefulStop returns immediately

	var mu sync.Mutex
	hookCalls := 0
	hook := func() {
		mu.Lock()
		hookCalls++
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		stopGRPCServerBoundedWithHook(f, 5*time.Second, hook)
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("stopGRPCServerBoundedWithHook did not return on fast path within 1s")
	}

	mu.Lock()
	n := hookCalls
	mu.Unlock()
	if n != 0 {
		t.Errorf("onEscalate hook fired on fast path (%d times) — PostGraceExpiry phase contract violated", n)
	}
	select {
	case <-f.stopCalled:
		t.Error("Stop was called on fast path — escalation triggered incorrectly")
	default:
	}
}

// TestStopGRPCServerBounded_NilServer verifies a nil server is a no-op
// (should not panic; should return immediately).
func TestStopGRPCServerBounded_NilServer(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		stopGRPCServerBounded(nil, 5*time.Second)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("nil gracefulStopper did not return immediately")
	}
}

// newServeGuardTestService builds a Service whose Serve() is guaranteed
// to fail at the earliest possible point in readConfig() — the empty-
// AppName check at config.Read() — without touching the filesystem,
// binding a listener, or spawning any goroutines. This lets the
// SVC-F3 tests exercise the one-shot CAS guard at the top of Serve()
// deterministically and without hanging on real lifecycle work.
//
// We deliberately do NOT use NewService: NewService with a
// nonexistent CustomConfigPath triggers viper's "save a default
// config file" path and succeeds, which in turn lets Serve() proceed
// to setupServer / listener bind and hang the test.
func newServeGuardTestService() *Service {
	return &Service{
		// AppName intentionally empty — readConfig returns
		// "App Name is Required" as the very first action after the
		// _started CAS flips.
		AppName:                 "",
		RegisterServiceHandlers: func(*grpc.Server) {},
	}
}

// -------- SVC-F3 Serve re-entry guard tests --------
//
// Serve is single-use by contract: the first call claims the Service
// via CAS on s._started, and every subsequent call returns
// ErrServiceAlreadyStarted without touching any lifecycle state.
// These tests exercise the CAS path without needing a real gRPC
// listener or CloudMap — Serve() past the CAS drops immediately into
// readConfig(), which fails deterministically for a Service that has
// no config file on disk. That is the "Serve started, then errored
// out" case, and the subsequent re-entry attempt is exactly the
// scenario the guard protects against.

func TestService_Serve_RejectsReentryAfterFailure(t *testing.T) {
	// Build a Service that will fail readConfig (no config file on disk
	// under the default CustomConfigPath). The first Serve() call
	// claims _started via CAS, then errors out of readConfig. The
	// second Serve() call MUST observe _started=true and return
	// ErrServiceAlreadyStarted without re-entering readConfig.
	svc := newServeGuardTestService()

	// First call: will fail at readConfig (config file missing), but
	// the CAS flipped _started to true as the first line of Serve.
	firstErr := svc.Serve()
	if firstErr == nil {
		t.Fatalf("expected first Serve to fail (no config file); got nil")
	}
	// Sanity: the first failure is NOT the re-entry sentinel.
	if firstErr == ErrServiceAlreadyStarted {
		t.Fatalf("first Serve returned re-entry sentinel, want a different error")
	}

	// Second call: must be refused via the CAS guard without re-running
	// readConfig or touching any lifecycle state.
	secondErr := svc.Serve()
	if secondErr != ErrServiceAlreadyStarted {
		t.Fatalf("second Serve: got %v, want ErrServiceAlreadyStarted", secondErr)
	}
}

func TestService_Serve_RejectsReentryConcurrently(t *testing.T) {
	// Two goroutines call Serve on the SAME Service concurrently.
	// Exactly one must win the CAS (and proceed to fail readConfig
	// deterministically), and exactly one must lose the CAS and
	// return ErrServiceAlreadyStarted. Order is nondeterministic; we
	// check the set of results rather than the ordering.
	svc := newServeGuardTestService()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			errs[idx] = svc.Serve()
		}(i)
	}
	wg.Wait()

	// Count outcomes.
	reentryCount := 0
	otherCount := 0
	for _, e := range errs {
		if e == ErrServiceAlreadyStarted {
			reentryCount++
		} else if e != nil {
			otherCount++
		}
	}
	if reentryCount != 1 || otherCount != 1 {
		t.Fatalf("concurrent Serve outcomes wrong: reentry=%d other=%d errs=%v",
			reentryCount, otherCount, errs)
	}
}

func TestService_Serve_FreshInstancesIndependent(t *testing.T) {
	// Two separate Service instances must each get their own CAS token.
	// Neither should observe the other's _started flag. Both Serve
	// calls should fail readConfig (not the re-entry sentinel).
	a := newServeGuardTestService()
	b := newServeGuardTestService()

	errA := a.Serve()
	errB := b.Serve()

	if errA == nil || errA == ErrServiceAlreadyStarted {
		t.Errorf("instance A: got %v, want a readConfig-style error", errA)
	}
	if errB == nil || errB == ErrServiceAlreadyStarted {
		t.Errorf("instance B: got %v, want a readConfig-style error", errB)
	}
}

// -----------------------------------------------------------------------
// SVC-F2 — hook panic recovery
// -----------------------------------------------------------------------
//
// runHook wraps user-supplied lifecycle callbacks
// (BeforeServerStart/AfterServerStart/BeforeServerShutdown/
// AfterServerShutdown) with defer recover() so a panicking hook cannot
// crash the Service lifecycle. Pre-fix behavior: a panic in
// BeforeServerShutdown crashed Serve() mid-cleanup, leaking the signal
// handler, quit-handler goroutine, and listener; panics in the startup
// hooks crashed the startup goroutine silently.
//
// These tests exercise runHook directly because it is the ONLY new
// surface area for the fix, and driving it through Serve() would
// require real gRPC lifecycle machinery (listener, signal handler,
// cloudmap) that has nothing to do with the invariant under test.

// TestService_RunHook_NilHookIsNoOp verifies that a nil hook is silently
// skipped and no panic escapes. This protects the common case where the
// caller does not supply a given lifecycle hook.
func TestService_RunHook_NilHookIsNoOp(t *testing.T) {
	s := &Service{}
	// Must not panic. Must not deadlock. Must simply return.
	s.runHook("BeforeServerStart", nil)
}

// TestService_RunHook_NormalHookInvoked verifies that a well-behaved
// hook runs to completion and the caller observes its side effects.
func TestService_RunHook_NormalHookInvoked(t *testing.T) {
	s := &Service{}
	called := false
	var receivedSvc *Service

	s.runHook("AfterServerStart", func(svc *Service) {
		called = true
		receivedSvc = svc
	})

	if !called {
		t.Error("hook was not invoked")
	}
	if receivedSvc != s {
		t.Errorf("hook received %p, want %p (runHook must pass receiver)", receivedSvc, s)
	}
}

// TestService_RunHook_PanickingHookRecovered verifies the core SVC-F2
// invariant: a hook that panics does NOT propagate the panic out of
// runHook. The test itself would abort with `panic: boom` if recovery
// was broken (defer/recover inside runHook is the only thing standing
// between the panic and the test goroutine).
func TestService_RunHook_PanickingHookRecovered(t *testing.T) {
	s := &Service{}

	// If runHook re-panics, the `defer` below never runs and `recovered`
	// stays false — but more importantly the test binary would abort.
	// We use a local recover as an extra belt-and-braces assertion that
	// no panic escaped runHook.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("runHook let panic escape: %v", r)
		}
	}()

	s.runHook("BeforeServerShutdown", func(svc *Service) {
		panic("boom")
	})

	// Reaching this line proves the panic did not escape runHook.
}

// TestService_RunHook_PanickingHookDoesNotBlockNextHook verifies that
// recovery does not leave runHook in a wedged state — subsequent hook
// invocations on the same Service still work. This guards against a
// future refactor that puts runHook state behind a sync primitive and
// forgets to release it on the recover path.
func TestService_RunHook_PanickingHookDoesNotBlockNextHook(t *testing.T) {
	s := &Service{}
	s.runHook("BeforeServerStart", func(svc *Service) {
		panic("first hook panicked")
	})

	// Second hook must still run. If runHook was holding a lock on the
	// recover path, this call would deadlock — so we wrap it in a
	// timeout via a channel.
	done := make(chan struct{})
	var called bool
	go func() {
		defer close(done)
		s.runHook("AfterServerShutdown", func(svc *Service) {
			called = true
		})
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runHook deadlocked on second invocation after recovery")
	}
	if !called {
		t.Error("second hook was not invoked after first hook recovered")
	}
}

// TestService_RunHook_PanickingHookRuntimeError verifies recovery for
// a runtime.Error panic (e.g., nil pointer deref), which is the most
// likely real-world panic shape — user hook accidentally dereferences
// a field that isn't set yet. Distinct from explicit `panic("str")`
// because runtime errors flow through a different panic path internally.
func TestService_RunHook_PanickingHookRuntimeError(t *testing.T) {
	s := &Service{}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("runHook let runtime panic escape: %v", r)
		}
	}()

	s.runHook("BeforeServerStart", func(svc *Service) {
		var p *int
		_ = *p // nil pointer deref, SIGSEGV-ish runtime panic
	})
}

// -----------------------------------------------------------------------
// SVC-F6 safeGo tests
// -----------------------------------------------------------------------
//
// safeGo wraps every service-internal goroutine spawn so a panic in
// background code (AWS SDK poll loops, startup orchestrator, quit
// handler, etc.) is recovered and logged instead of crashing the
// process. Pre-SVC-F6, grpc_recovery was the ONLY panic safety net,
// and it covers ONLY handler interceptor invocations — the 7 background
// goroutines in service.go had zero protection.
//
// These tests pin the observable contract of safeGo:
//   - nil fn is a no-op (does not spawn, does not panic)
//   - normal fn runs to completion in a new goroutine
//   - panicking fn is recovered; caller never sees the panic escape
//   - runtime panics (nil deref) are also recovered

// TestSafeGo_NilFnIsNoOp: nil fn must NOT spawn a goroutine. The test
// completes immediately without any observable side effect.
func TestSafeGo_NilFnIsNoOp(t *testing.T) {
	// If safeGo spawned a goroutine despite nil fn, the anonymous func
	// inside would call fn() and panic on nil — the defer would catch
	// it but the log line would still fire. We can't observe that
	// directly, so just verify that no panic escapes the caller.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("safeGo(nil) panicked at the call site: %v", r)
		}
	}()

	ch := safego.GoWait("nil-fn", nil)
	// GoWait must close the channel immediately for a nil fn.
	select {
	case <-ch:
		// channel closed immediately (nil fn) — no goroutine spawned
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait(nil) channel was not closed")
	}
}

// TestSafeGo_NormalFnCompletes: a well-behaved fn runs in a goroutine
// and signals completion via channel. Verifies the happy path.
func TestSafeGo_NormalFnCompletes(t *testing.T) {
	done := make(chan struct{})

	safego.Go("normal-fn", func() {
		close(done)
	})

	select {
	case <-done:
		// pass
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo fn did not execute within 2s")
	}
}

// TestSafeGo_PanickingFnRecovered: a panicking fn must be recovered
// inside the goroutine. Since the goroutine runs concurrently, the
// caller's defer/recover cannot catch a panic escaping from it — a
// runtime that didn't recover would crash the test binary with
// `panic: runtime error` and `go test` would report FAIL with the
// panic trace. The fact that the assertion after sleep runs at all
// proves the recovery worked.
func TestSafeGo_PanickingFnRecovered(t *testing.T) {
	entered := make(chan struct{})

	ch := safego.GoWait("panicking-fn", func() {
		close(entered)
		panic("deliberate panic in safeGo test")
	})

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo fn did not start within 2s")
	}

	// Wait for panic recovery + logging to complete.
	select {
	case <-ch:
		// recovery complete
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait channel not closed after panic recovery")
	}

	// If we got here without the test binary crashing, recovery worked.
}

// TestSafeGo_PanickingFnRuntimeError: recovers a runtime.Error panic
// (nil pointer deref) — the most likely real-world shape, since AWS SDK
// poll loops tend to deref cached response fields.
func TestSafeGo_PanickingFnRuntimeError(t *testing.T) {
	entered := make(chan struct{})

	ch := safego.GoWait("runtime-error-fn", func() {
		close(entered)
		var p *int
		_ = *p // nil deref
	})

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo fn did not start within 2s")
	}

	// Wait for panic recovery + logging to complete.
	select {
	case <-ch:
		// recovery complete
	case <-time.After(2 * time.Second):
		t.Fatal("GoWait channel not closed after runtime error recovery")
	}
}

// TestSafeGo_ConcurrentPanicsAllRecovered: fleet of 50 panicking
// goroutines — all must be recovered independently. A single escaped
// panic would crash the binary. Also verifies safeGo has no shared
// state causing recovery contention.
func TestSafeGo_ConcurrentPanicsAllRecovered(t *testing.T) {
	const n = 50
	var started sync.WaitGroup
	started.Add(n)

	channels := make([]<-chan struct{}, n)
	for i := 0; i < n; i++ {
		channels[i] = safego.GoWait("concurrent-panic", func() {
			started.Done()
			panic("concurrent panic")
		})
	}

	// Wait for all goroutines to have at least entered fn.
	done := make(chan struct{})
	go func() {
		started.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("not all safeGo goroutines started within 3s")
	}

	// Wait for all recoveries to complete.
	for j, ch := range channels {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("goroutine %d recovery did not complete within 5s", j)
		}
	}
}

// TestStopGRPCServerBounded_ZeroTimeoutUsesDefault verifies that a zero
// or negative timeout falls back to the 30-second default rather than
// timing out immediately.
func TestStopGRPCServerBounded_ZeroTimeoutUsesDefault(t *testing.T) {
	f := newFakeGRPCServer()
	// Release immediately so the call returns via the fast path —
	// otherwise this test would block for 30s on the default timeout.
	f.releaseGraceful()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		stopGRPCServerBounded(f, 0)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("zero-timeout call did not return via fast path")
	}
	// Confirms the helper didn't interpret 0 as "timeout immediately":
	// if it had, Stop would have been called and the call would have
	// returned before GracefulStop was observed.
	select {
	case <-f.stopCalled:
		t.Error("zero-timeout path hit Stop escalation — should have used default 30s timeout")
	default:
	}
	_ = start
}

// -----------------------------------------------------------------------
// SVC-F4 — _deregFired atomic.Bool replaces sync.Once on Cloud Map dereg
// -----------------------------------------------------------------------
//
// The P1-3 fix used sync.Once to guarantee at-most-once Cloud Map
// DeregisterInstance under concurrent shutdown, but sync.Once.Do BLOCKS
// every concurrent caller behind the first caller's AWS poll loop.
// That broke ImmediateStop's break-glass semantics: an operator hitting
// ImmediateStop while the signal path was mid-dereg would stall up to
// ~5s typical / ~100s pathological behind AWS.
//
// The SVC-F4 fix swaps sync.Once for an atomic.Bool one-shot CAS:
// the first caller wins and runs doDeregisterInstance; every concurrent
// or later caller CAS-fails and returns nil IMMEDIATELY. The at-most-
// once guarantee is preserved (AWS DeregisterInstance is itself
// idempotent; the CAS prevents any duplicate).
//
// These tests pin the two invariants of the new primitive:
//  1. Single-call fires the body and flips _deregFired to true.
//  2. A concurrent second call does NOT block behind the first caller's
//     doDeregisterInstance — this is the actual "ImmediateStop stays
//     immediate" invariant.

// TestService_DeregisterInstance_FirstCallFires verifies the happy path:
// a fresh Service with nil _sd (so the body fast-returns nil) correctly
// CAS-wins, runs the body, and flips _deregFired to true.
func TestService_DeregisterInstance_FirstCallFires(t *testing.T) {
	s := &Service{}
	if s._deregFired.Load() {
		t.Fatal("pre: _deregFired should start false on a fresh Service")
	}

	if err := s.deregisterInstance(); err != nil {
		t.Errorf("first call returned error: %v (_sd is nil so body should no-op)", err)
	}
	if !s._deregFired.Load() {
		t.Error("_deregFired did not transition to true after first call")
	}
}

// TestService_DeregisterInstance_SecondCallSkipsBody verifies the
// CAS-fail fast path: once _deregFired is true, subsequent calls return
// nil without touching any Service state. Pre-setting the flag is
// equivalent to "another goroutine already claimed the dereg" — if
// the implementation regressed to sync.Once or any blocking primitive,
// the second call would still enter doDeregisterInstance and hit RLock.
func TestService_DeregisterInstance_SecondCallSkipsBody(t *testing.T) {
	s := &Service{}
	s._deregFired.Store(true) // simulate "another goroutine already claimed it"

	// This call must return nil without entering doDeregisterInstance.
	// We don't have a body-entry counter, but the non-blocking test
	// below proves the fast path; here we just check the return value
	// and that the flag stays true.
	if err := s.deregisterInstance(); err != nil {
		t.Errorf("second call returned error: %v, want nil", err)
	}
	if !s._deregFired.Load() {
		t.Error("_deregFired got reset by a skipped call — should stay true")
	}
}

// TestService_DeregisterInstance_SecondCallDoesNotBlock is the critical
// SVC-F4 regression test. It pins the exact invariant that motivated
// the fix: when goroutine A is inside doDeregisterInstance (blocked
// here on _mu.RLock because the test holds _mu.Lock), goroutine B's
// deregisterInstance call must return within a tight deadline.
//
// If this test regresses to sync.Once, goroutine B will block on
// sync.Once.Do until goroutine A completes (which it can't, because
// we hold _mu.Lock) — the test times out with a deadlock diagnostic
// instead of passing.
func TestService_DeregisterInstance_SecondCallDoesNotBlock(t *testing.T) {
	s := &Service{}

	// Hold _mu.Lock so that any goroutine entering doDeregisterInstance
	// blocks on its _mu.RLock. This simulates "AWS poll loop taking
	// forever" without needing to mock AWS.
	s._mu.Lock()

	// Goroutine A: claim the dereg CAS, then block inside
	// doDeregisterInstance on _mu.RLock.
	aEntered := make(chan struct{})
	aReturned := make(chan struct{})
	go func() {
		close(aEntered)
		_ = s.deregisterInstance()
		close(aReturned)
	}()

	// Give goroutine A time to flip the CAS and block on RLock.
	// (10ms is generous; the CAS itself is nanoseconds.)
	<-aEntered
	time.Sleep(20 * time.Millisecond)

	if !s._deregFired.Load() {
		s._mu.Unlock()
		t.Fatal("goroutine A did not reach the CAS — test setup broken")
	}

	// Goroutine B: must return within 50ms even though goroutine A is
	// still blocked inside the body. 50ms is generously above any
	// scheduler jitter but well below a real AWS poll (~250ms+).
	bDone := make(chan error, 1)
	go func() {
		bDone <- s.deregisterInstance()
	}()

	select {
	case err := <-bDone:
		if err != nil {
			t.Errorf("goroutine B returned error: %v, want nil", err)
		}
	case <-time.After(50 * time.Millisecond):
		s._mu.Unlock() // release A so we don't leak it
		<-aReturned
		t.Fatal("SVC-F4 regression: second deregisterInstance call blocked — sync.Once deadlock semantics have returned")
	}

	// Release goroutine A so it can finish and not leak.
	s._mu.Unlock()
	select {
	case <-aReturned:
	case <-time.After(2 * time.Second):
		t.Error("goroutine A never returned after _mu was released — doDeregisterInstance is wedged")
	}
}

// TestService_DeregisterInstance_ConcurrentCallsNoDeadlock spawns a
// fleet of goroutines all calling deregisterInstance simultaneously.
// Invariant: every goroutine returns, no deadlock, _deregFired ends
// true. This guards against future refactors that add locking on the
// CAS path and forget to handle the fast-path release.
func TestService_DeregisterInstance_ConcurrentCallsNoDeadlock(t *testing.T) {
	s := &Service{}

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = s.deregisterInstance()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent deregisterInstance calls deadlocked")
	}

	if !s._deregFired.Load() {
		t.Error("_deregFired is false after concurrent calls — the CAS path is broken")
	}
}

// ----------------------------------------------------------------------
// SVC-F5 ShutdownCtx tests.
//
// The opt-in handler shutdown-signal context is allocated inside
// Serve() when ShutdownCancel is true and cancelled at the configured
// phase. These tests exercise the helpers directly — Serve() remains
// integration-gated (TestService_Serve), so unit coverage is the
// practical option for a CI-safe test suite.
//
// newShutdownCancelTestService mirrors Serve()'s allocation step so
// tests can drive fireShutdownCancelIfPhase / fireShutdownCancelFinal
// against a realistic Service state without running Serve itself.
// ----------------------------------------------------------------------

func newShutdownCancelTestService(enabled bool, phase ShutdownPhase) *Service {
	s := &Service{ShutdownCancel: enabled, ShutdownCancelPhase: phase}
	if enabled {
		s._mu.Lock()
		s._shutdownCtx, s._shutdownCancel = context.WithCancel(context.Background())
		s._mu.Unlock()
	}
	return s
}

// Test 1 (design-doc §3): ShutdownCtx() returns nil on a fresh Service.
// Zero-value Service must see the capability disabled and ShutdownCtx()
// return nil. This is the default "existing consumer sees no change"
// guarantee.
func TestService_ShutdownCtx_DisabledByDefault(t *testing.T) {
	s := &Service{}
	if got := s.ShutdownCtx(); got != nil {
		t.Fatalf("ShutdownCtx() = %v, want nil when ShutdownCancel is false", got)
	}
}

// Test 2 (design-doc §3): ShutdownCancel = true alone does not
// allocate the ctx — allocation is Serve's job. Until Serve runs,
// ShutdownCtx() still returns nil. Helpers must no-op rather than
// panic on a partially-configured Service.
func TestService_ShutdownCtx_NilUntilAllocated(t *testing.T) {
	s := &Service{ShutdownCancel: true, ShutdownCancelPhase: ShutdownPhaseImmediate}
	if got := s.ShutdownCtx(); got != nil {
		t.Fatalf("ShutdownCtx() = %v, want nil until Serve allocates it", got)
	}
	// Helpers must not panic on an unallocated Service.
	s.fireShutdownCancelIfPhase(ShutdownPhaseImmediate)
	s.fireShutdownCancelFinal()
}

// Test 3 (design-doc §3): Immediate phase — matching fire cancels the
// ctx; non-matching phases leave it alive. This is the sig-demux
// in-goroutine fire path that Serve wires into awaitOsSigExit.
func TestService_ShutdownCtx_ImmediatePhase_CancelsOnFire(t *testing.T) {
	s := newShutdownCancelTestService(true, ShutdownPhaseImmediate)
	ctx := s.ShutdownCtx()
	if ctx == nil {
		t.Fatal("ShutdownCtx() returned nil after allocation")
	}

	// Non-matching phases must not cancel.
	s.fireShutdownCancelIfPhase(ShutdownPhasePreDrain)
	s.fireShutdownCancelIfPhase(ShutdownPhasePostGraceExpiry)
	if err := ctx.Err(); err != nil {
		t.Fatalf("ctx cancelled by non-matching phase fire: err=%v", err)
	}

	// Matching phase must cancel.
	s.fireShutdownCancelIfPhase(ShutdownPhaseImmediate)
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ctx did not cancel after matching Immediate phase fire")
	}
}

// Test 4 (design-doc §3): PreDrain phase — matching fire cancels,
// Immediate (earlier phase) does not. Verifies phase matching is
// exact, not ordered, so an earlier-phase fire on a later-configured
// service leaves the ctx alive until its designated point.
func TestService_ShutdownCtx_PreDrainPhase_CancelsOnFire(t *testing.T) {
	s := newShutdownCancelTestService(true, ShutdownPhasePreDrain)
	ctx := s.ShutdownCtx()
	if ctx == nil {
		t.Fatal("ShutdownCtx() returned nil after allocation")
	}

	s.fireShutdownCancelIfPhase(ShutdownPhaseImmediate)
	if err := ctx.Err(); err != nil {
		t.Fatalf("ctx cancelled by Immediate on PreDrain-configured service: err=%v", err)
	}

	s.fireShutdownCancelIfPhase(ShutdownPhasePreDrain)
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ctx did not cancel after matching PreDrain phase fire")
	}
}

// Test 5 (design-doc §3): PostGraceExpiry — stopGRPCServerBoundedWithHook
// calls the hook synchronously, on the caller's goroutine, immediately
// before escalating to gs.Stop(). This wires the SVC-F5 fire site to
// the SVC-F1 escalation point. Verify:
//
//   - the hook IS invoked when the grace timeout elapses (forced by
//     not releasing gracefulStopBlock),
//   - the hook runs BEFORE gs.Stop (because the design promises the
//     "last warning" ordering — handlers observing ShutdownCtx see
//     cancellation strictly before the transport is severed),
//   - the ctx is cancelled by the time the helper returns.
func TestService_ShutdownCtx_PostGraceExpiry_CancelsOnEscalate(t *testing.T) {
	s := newShutdownCancelTestService(true, ShutdownPhasePostGraceExpiry)
	ctx := s.ShutdownCtx()
	if ctx == nil {
		t.Fatal("ShutdownCtx() returned nil after allocation")
	}

	f := newFakeGRPCServer()
	// Do not release gracefulStopBlock → force the timeout/escalation path.

	// hookFiredBeforeStop captures whether ctx was already cancelled at
	// the moment Stop was observed by the test's wrapper. Since the
	// hook must fire synchronously before gs.Stop per the helper's
	// contract, the observed ctx must be cancelled at Stop-call time.
	var hookRanAt time.Time
	hook := func() {
		hookRanAt = time.Now()
		s.fireShutdownCancelIfPhase(ShutdownPhasePostGraceExpiry)
	}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		stopGRPCServerBoundedWithHook(f, 50*time.Millisecond, hook)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stopGRPCServerBoundedWithHook hung past escalation deadline")
	}

	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("escalation path returned before timeout elapsed: %s", elapsed)
	}

	// Ctx must be cancelled because the hook fired before gs.Stop.
	if err := ctx.Err(); err == nil {
		t.Fatal("ctx was not cancelled — PostGraceExpiry hook did not fire on escalation")
	}
	if hookRanAt.IsZero() {
		t.Fatal("hook was never invoked")
	}

	// Stop must have been called (escalation is the whole point).
	select {
	case <-f.stopCalled:
	default:
		t.Fatal("Stop was not called on escalation path")
	}
}

// Test 6 (design-doc §3): Safety net — fireShutdownCancelFinal cancels
// ShutdownCtx regardless of phase, and is idempotent. Serve() defers
// this call so a handler observing ShutdownCtx is guaranteed to see
// Done() close before Serve returns, even if no phase matched.
func TestService_ShutdownCtx_SafetyNetAlwaysFires(t *testing.T) {
	s := newShutdownCancelTestService(true, ShutdownPhaseImmediate)
	ctx := s.ShutdownCtx()
	if ctx == nil {
		t.Fatal("ShutdownCtx() returned nil after allocation")
	}

	// No phase fire — ctx still live.
	if err := ctx.Err(); err != nil {
		t.Fatalf("ctx already cancelled before safety net: err=%v", err)
	}

	s.fireShutdownCancelFinal()
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ctx did not cancel after Final safety-net fire")
	}

	// Idempotent: second call must be safe (context.CancelFunc allows
	// multiple invocations).
	s.fireShutdownCancelFinal()
}

// Test 7 (design-doc §3): handler-observable shape — a handler that
// checks ShutdownCtx in its work loop returns early when the cancel
// fires. This test runs the canonical select pattern from the
// ShutdownCtx godoc and verifies it actually exits on cancel, rather
// than proving it end-to-end through Serve (which remains
// integration-gated).
func TestService_ShutdownCtx_HandlerObservingCtxExitsEarly(t *testing.T) {
	s := newShutdownCancelTestService(true, ShutdownPhaseImmediate)
	shut := s.ShutdownCtx()
	if shut == nil {
		t.Fatal("ShutdownCtx() returned nil after allocation")
	}

	// Simulated handler: processes chunks until ShutdownCtx fires,
	// then returns the number of chunks processed and an "unavailable"
	// marker. Mirrors the godoc example pattern exactly.
	handlerDone := make(chan int, 1)
	startWork := make(chan struct{})
	// P3-N4 (2026-04-17): deterministic "handler has begun meaningful work"
	// signal, closed after the first chunk is processed. Replaces the prior
	// time.Sleep(10ms) race that could flake under GC or scheduler pressure.
	processedFirst := make(chan struct{})
	safego.Go("svc-f5-test-handler", func() {
		<-startWork
		processed := 0
		for {
			select {
			case <-shut.Done():
				handlerDone <- processed
				return
			default:
			}
			processed++
			if processed == 1 {
				close(processedFirst)
			}
			// Tiny deterministic step so the test terminates even if
			// cancel never arrives (failure will time out below).
			time.Sleep(time.Millisecond)
		}
	})

	close(startWork)
	// Deterministic wait: handler has processed ≥ 1 chunk before cancel fires.
	select {
	case <-processedFirst:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not process first chunk within 2s")
	}

	s.fireShutdownCancelIfPhase(ShutdownPhaseImmediate)

	select {
	case processed := <-handlerDone:
		if processed == 0 {
			t.Error("handler exited without processing any chunks — tight loop never saw progress")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not observe ShutdownCtx.Done() within 2s")
	}
}

// Test 8 (design-doc §3): opt-in = no coercion. A handler that never
// checks ShutdownCtx still runs to completion, regardless of the
// cancel state. The framework does not reach into the handler or
// translate status codes; handler authors who want cooperation must
// write it themselves.
func TestService_ShutdownCtx_HandlerIgnoringCtxStillCompletes(t *testing.T) {
	s := newShutdownCancelTestService(true, ShutdownPhaseImmediate)

	// Simulated handler: ignores ShutdownCtx, always returns success.
	handlerResult := make(chan error, 1)
	// P3-N4 (2026-04-17): deterministic "handler has started" signal, closed
	// at handler entry. Replaces the prior time.Sleep(10ms) race — we need
	// the cancel to fire WHILE the handler is mid-work, not potentially
	// before the goroutine is even scheduled.
	handlerStarted := make(chan struct{})
	safego.Go("svc-f5-test-handler-ignoring", func() {
		close(handlerStarted)
		// No ShutdownCtx observation.
		time.Sleep(50 * time.Millisecond)
		handlerResult <- nil // "successful" RPC
	})

	// Fire the cancel while the handler is running (after it has entered).
	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("ignoring-ctx handler did not start within 2s")
	}
	s.fireShutdownCancelIfPhase(ShutdownPhaseImmediate)

	select {
	case err := <-handlerResult:
		if err != nil {
			t.Errorf("ignoring-ctx handler returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ignoring-ctx handler did not complete within 2s")
	}

	// And the safety net still closes Done() for any later observer.
	s.fireShutdownCancelFinal()
	select {
	case <-s.ShutdownCtx().Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ShutdownCtx.Done() not closed after final safety-net fire")
	}
}

// ----------------------------------------------------------------------
// SVC-F5 edge cases on stopGRPCServerBoundedWithHook itself.
// ----------------------------------------------------------------------

// Fast path (GracefulStop returns quickly) MUST NOT fire the hook.
// PostGraceExpiry means "after grace expiry" — a successful graceful
// drain leaves ShutdownCtx uncancelled until the Serve safety net.
func TestStopGRPCServerBoundedWithHook_FastPathNoFire(t *testing.T) {
	f := newFakeGRPCServer()
	f.releaseGraceful() // GracefulStop returns immediately

	var fired bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		stopGRPCServerBoundedWithHook(f, 5*time.Second, func() { fired = true })
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("fast-path hung")
	}
	if fired {
		t.Error("onEscalate hook fired on fast path — must only fire on timeout")
	}
}

// A nil hook on the escalation path must not panic.
func TestStopGRPCServerBoundedWithHook_NilHookSafeOnEscalate(t *testing.T) {
	f := newFakeGRPCServer()

	done := make(chan struct{})
	go func() {
		defer close(done)
		stopGRPCServerBoundedWithHook(f, 50*time.Millisecond, nil)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("nil-hook escalate path hung")
	}
}

// A panicking hook must not block gs.Stop from running — the
// escalation guarantee stands regardless of hook behavior.
func TestStopGRPCServerBoundedWithHook_PanickingHookDoesNotBlockStop(t *testing.T) {
	f := newFakeGRPCServer()

	done := make(chan struct{})
	go func() {
		defer close(done)
		stopGRPCServerBoundedWithHook(f, 50*time.Millisecond, func() {
			panic("hook panic for test")
		})
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("escalation hung after hook panic")
	}

	select {
	case <-f.stopCalled:
	default:
		t.Error("Stop was not called after hook panic")
	}
}

// Zero-value ShutdownCancelPhase promotes to Immediate when the
// capability is enabled. "Just turn it on" is a supported shape.
func TestService_EffectiveShutdownCancelPhase_ZeroPromotedToImmediate(t *testing.T) {
	s := &Service{ShutdownCancel: true} // phase left at zero value
	if got := s.effectiveShutdownCancelPhase(); got != ShutdownPhaseImmediate {
		t.Errorf("effectiveShutdownCancelPhase() = %v, want Immediate", got)
	}
}

// AD-3: Frozen() returns false on a fresh Service and true after
// Serve() has been called (even if Serve returns an error). This
// lets callers guard against post-Serve mutation of exported fields.
//
// Uses newServeGuardTestService (empty AppName → readConfig fails
// immediately, no listener bind, no goroutines) so the test is
// deterministic and fast.
func TestService_Frozen_LifecycleContract(t *testing.T) {
	t.Run("false_before_Serve", func(t *testing.T) {
		svc := newServeGuardTestService()
		if svc.Frozen() {
			t.Error("Frozen() returned true before Serve — expected false")
		}
	})

	t.Run("true_after_Serve_even_on_error", func(t *testing.T) {
		// Serve fails at readConfig (empty AppName), but the _started
		// CAS at the top of Serve fires before readConfig runs.
		svc := newServeGuardTestService()
		err := svc.Serve()
		if err == nil {
			t.Fatal("expected Serve to fail (empty AppName); got nil")
		}
		if !svc.Frozen() {
			t.Error("Frozen() returned false after Serve — expected true")
		}
	})

	t.Run("true_blocks_reentry", func(t *testing.T) {
		// Verify Frozen is consistent with the re-entry guard: once
		// Frozen() is true, a second Serve returns ErrServiceAlreadyStarted.
		svc := newServeGuardTestService()
		_ = svc.Serve() // first call — fails at readConfig but flips _started
		if !svc.Frozen() {
			t.Fatal("precondition: Frozen() should be true after first Serve")
		}
		err := svc.Serve()
		if err != ErrServiceAlreadyStarted {
			t.Errorf("second Serve returned %v, want ErrServiceAlreadyStarted", err)
		}
	})
}
