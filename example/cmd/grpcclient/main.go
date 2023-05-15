package main

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
	"fmt"
	util "github.com/aldelo/common"
	"github.com/aldelo/connector/client"
	"google.golang.org/grpc"
	"log"
	"time"

	testpb "github.com/aldelo/connector/example/proto/test"
)

type healthTest struct {
	PK            string `json:"pk" dynamodbav:"PK"`
	SK            string `json:"sk" dynamodbav:"SK"`
	NamespaceId   string `json:"namespaceid" dynamodbav:"NamespaceId"`
	ServiceId     string `json:"serviceid" dynamodbav:"ServiceId"`
	InstanceId    string `json:"instanceid" dynamodbav:"InstanceId"`
	LastTimestamp int64  `json:"lasttimestamp" dynamodbav:"LastTimestamp"`
}

// example gRPC client using connector client
func main() {
	fmt.Println("*** Example Client Consuming gRPC Server Services ***")

	//
	// establish grpc client connection to grpc service server - service-1.yaml connection
	//
	var svc1Cli *client.Client
	var err error

	if svc1Cli, err = DialService1(); err != nil {
		log.Fatal("Start Client Failed: " + err.Error())
	}
	defer svc1Cli.Close()

	go func() {
		_ = svc1Cli.DoNotifierAlertService()
	}()

	//
	// now operate client to service interactions
	//
	for {
		choice := ""

		fmt.Println("Please Select Choice:")
		fmt.Println("... 1 = Call service-1 RPC Test Method")
		fmt.Println("... 2 = Call service-1 Server Stream RPC Test Method")
		fmt.Println("... 3 = Call service-1 Health Probe")
		_, _ = fmt.Scanln(&choice)

		switch util.RightTrimLF(choice) {
		case "1":
			if !svc1Cli.Ready() {
				log.Println("*** CLIENT CONN NOT READY ***")
				break
			}

			answerClient := testpb.NewAnswerServiceClient(svc1Cli.ClientConnection())

			// call service 1000 times
			for i := 0; i < 1000; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)

				if answer, e := answerClient.Greeting(ctx, &testpb.Question{
					Question: "How is Weather Today " + util.NewULID() + "?",
				}); e != nil {
					fmt.Println("Call gRPC Service Error: " + e.Error())
				} else {
					fmt.Println("> gRPC Service Response = " + answer.Answer)
				}

				cancel()
			}

			// manual health probe
			if status, e := svc1Cli.HealthProbe("", 5*time.Second); e != nil {
				log.Println("Health Check v1 Manual = (Error) " + e.Error())
			} else {
				log.Println("Health Check v1 Manual = (Status) " + status.String())
			}

		case "2":
			answerClient := testpb.NewAnswerServerStreamServiceClient(svc1Cli.ClientConnection())
			if stream, err := answerClient.StreamGreeting(context.Background(), &testpb.Question{}); err != nil {
				log.Println(err)
			} else {
				for {
					if answer, e := stream.Recv(); e != nil {
						log.Println("Stream Receive Error: " + e.Error())
						break
					} else {
						log.Println("Answer = " + answer.Answer)
					}
				}

				log.Println("For Loop Ended")
			}

		case "3":
			v, e := svc1Cli.HealthProbe("")
			if e != nil {
				log.Println("Error = " + e.Error())
			} else {
				log.Println("Probe = " + v.String())
			}

		default:
			fmt.Println("--- EXIT CLIENT ---")
			return
		}
	}
}

// DialService1 will dial grpc server as defined under ./endpoint/service-1.yaml
// if there are other grpc service targets, dial to the other yaml defined service endpoints,
// this way, service target configuration can be edited via yaml config without having to recompile code,
// where each endpoint is separately maintained via individual yaml config files under ./endpoint
func DialService1() (cli *client.Client, err error) {
	cli = client.NewClient("ExampleClient", "service-1", "./endpoint")

	/*
		cli.WebServerConfig = &client.WebServerConfig{
			AppName: cli.AppName,
			ConfigFileName: "webserver",
			CustomConfigPath: "",
		}
	*/

	cli.WaitForServerReady = true

	cli.BeforeClientDial = func(cli *client.Client) {
		log.Println("Before Client Dial...")
	}

	cli.AfterClientDial = func(cli *client.Client) {
		log.Println("... After Client Dial")
	}

	cli.BeforeClientClose = func(cli *client.Client) {
		log.Println("Before Client Close...")
	}

	cli.AfterClientClose = func(cli *client.Client) {
		log.Println("... After Client Close")
	}

	cli.UnaryClientInterceptors = []grpc.UnaryClientInterceptor{
		func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			log.Println("Unary Client Interceptor Invoked: " + method)
			return invoker(ctx, method, req, reply, cc, opts...)
		},
	}

	cli.StreamClientInterceptors = []grpc.StreamClientInterceptor{
		func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
			log.Println("Stream Client Interceptor Invoked: " + method)
			return streamer(ctx, desc, cc, method, opts...)
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err = cli.Dial(ctx); err != nil {
		log.Println("Client Dial Failed: " + err.Error())
		log.Println("Connectivity State: " + cli.GetState().String())
	}

	if err != nil {
		return nil, err
	}

	return cli, nil
}
