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

func TestConfig(t *testing.T) {
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
		log.Fatal(e)
	} else {
		time.Sleep(3 * time.Second)
	}

	op, e2 := sd.GetOperation(opID)

	if e2 != nil {
		log.Fatal(e2)
	}

	id := op.Targets["INSTANCE"]

	if util.LenTrim(*id) > 0 {
		log.Println("Instance = " + *id)
	} else {
		log.Fatal("Instance empty")
	}

	found, e3 := sd.DiscoverInstances(cfg.Namespace.Name, cfg.Service.Name, true, nil, nil)

	if e3 != nil {
		log.Fatal(e3)
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
		log.Fatal(err)
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
