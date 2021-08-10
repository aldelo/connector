package client

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
	"context"
	testpb "github.com/aldelo/connector/example/proto/test"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"log"
	"testing"
)

func TestClient_Dial(t *testing.T) {
	cli := NewClient("testclient", "client", "")

	// cli.StatsHandler

	cli.BeforeClientDial = func(cli *Client) {
		log.Println("In - Before Client Dial")
	}

	cli.AfterClientDial = func(cli *Client) {
		log.Println("In - After Client Dial")
	}

	cli.BeforeClientClose = func(cli *Client) {
		log.Println("In - Before Client Close")
	}

	cli.AfterClientClose = func(cli *Client) {
		log.Println("In - After Client Close")
	}

	if err := cli.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}

	defer cli.Close()

	a := testpb.NewAnswerServiceClient(cli._conn)

	for i := 0; i < 1000; i++ {
		var header metadata.MD

		if result, e := a.Greeting(context.Background(), &testpb.Question{Question: "What's for dinner?"}, grpc.Header(&header)); e != nil {
			t.Error(e)
		} else {
			log.Println("Answer = " + result.Answer + ", From = " + header.Get("server")[0])
		}
	}

	if status, err := cli.HealthProbe(""); err != nil {
		log.Println("Health Probe Failed: " + err.Error())
	} else {
		log.Println(status.String())
	}
}
