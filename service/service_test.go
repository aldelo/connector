package service

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
	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/cloudmap"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginbindtype"
	"github.com/aldelo/common/wrapper/gin/gingzipcompression"
	"github.com/aldelo/common/wrapper/gin/ginhttpmethod"
	"github.com/aldelo/connector/adapters/metadata"
	testpb "github.com/aldelo/connector/example/proto/test"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
	"log"
	"net"
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

			meta := &metadata.MetaServer{}
			meta.SendHeader(ctx, map[string]string{
				"server": svc._localAddress,
			})

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
		AppName: "test-connector-service",
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

func TestCloudMap(t *testing.T) {
	cfg := &config{
		AppName: "test-connector-service",
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

	opID, e := sd.RegisterInstance(cfg.Service.Id, "test-" + uuid, uuid, map[string]string{
		"AWS_INSTANCE_IPV4": "192.168.1.13",
		"AWS_INSTANCE_PORT": "9999",
	})

	if e != nil {
		log.Fatal(e)
	} else {
		time.Sleep(3*time.Second)
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

func TestDNSLookup(t *testing.T) {
	ips, err := net.LookupIP("helloservice.example.private")

	if err != nil {
		log.Fatal(err)
	}

	for _, ip := range ips {
		log.Println("IP = " + ip.String())
	}
}

func TestGinServer(t *testing.T) {
	// z := ginw.NewGinZapMiddleware("Example", true)

	g := ginw.NewServer("Example", 8080, true, false, nil)

	g.SessionMiddleware = &ginw.SessionConfig{
		SecretKey: "Secret",
		SessionNames: []string{"MySession"},
	}

	g.CsrfMiddleware = &ginw.CsrfConfig{
		Secret: "Secrete",
	}

	g.Routes = map[string][]*ginw.RouteDefinition{
		"*": {
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

	if err := g.RunServer(); err != nil {
		log.Println("Error: " + err.Error())
		t.Fatal("Fail")
	} else {
		log.Println("Run OK")
		t.Log("OK")
	}
}