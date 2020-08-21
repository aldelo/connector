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
	"google.golang.org/grpc"
	"testing"
	testpb "github.com/aldelo/connector/internal/proto/test"
)

func TestClient_Dial(t *testing.T) {
	cli := &Client{
		ServiceName: "ms.aldelo.helloservice",
		ServiceAddr: "",
	}

	if err := cli.Dial(grpc.WithInsecure()); err != nil {
		t.Fatal(err)
	}

	defer cli.Close()

	a := testpb.NewAnswerServiceClient(cli._conn)

	if result, e := a.Greeting(context.Background(), &testpb.Question{Question: "What's for dinner?"}); e != nil {
		t.Error(e)
	} else {
		fmt.Println("Answer = " + result.Answer + ", From = " + cli.RemoteAddress())
	}
}
