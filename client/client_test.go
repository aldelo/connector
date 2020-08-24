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
	"github.com/aldelo/connector/adapters/health"
	testpb "github.com/aldelo/connector/example/proto/test"
	"testing"
	"time"
)

func TestClient_Dial(t *testing.T) {
	cli := &Client{
		AppName: "connector.client",
		ConfigFileName: "client",
	}

	if err := cli.Dial(context.Background()); err != nil {
		t.Fatal(err)
	}

	defer cli.Close()

	a := testpb.NewAnswerServiceClient(cli._conn)

	if result, e := a.Greeting(context.Background(), &testpb.Question{Question: "What's for dinner?"}); e != nil {
		t.Error(e)
	} else {
		fmt.Println("Answer = " + result.Answer + ", From = " + cli.RemoteAddress())
	}


	hc, _ := health.NewHealthClient(cli._conn)
	for {
		if hcResp, err := hc.Check(""); err != nil {
			fmt.Println("Health Check Fail: " + err.Error())
			break
		} else {
			fmt.Println("Health Check Result = " + hcResp.String())
		}

		time.Sleep(1 * time.Second)
	}
}
