package client

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
	"sync/atomic"
	"testing"
	"time"

	testpb "github.com/aldelo/connector/example/proto/test"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"log"
)

// TestClient_Dial is an integration test that requires:
//   - A populated client.yaml with real Target.ServiceName / NamespaceName / Region
//   - A reachable AWS Cloud Map namespace for service discovery
//   - A running gRPC AnswerService at the discovered endpoint
//
// It is skipped under `go test -short` (and therefore in CI) because the
// default client.yaml has empty discovery fields and no server is running.
// Run manually with: `go test -run TestClient_Dial ./client/` after
// configuring client.yaml and starting the example server. P2-16.
func TestClient_Dial(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test: requires configured client.yaml + running gRPC AnswerService")
	}

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

// -------- CL-F1 watchdog helper tests --------
// closeOnClosed bridges Client.Close()'s closedCh into an in-flight
// notifier Subscribe by calling a cancel func when closedCh fires first,
// and exiting cleanly when the local doneCh fires first. See client.go
// CL-F1 fix for context.

func TestCloseOnClosed_FiresCancelOnClosedCh(t *testing.T) {
	closedCh := make(chan struct{})
	doneCh := make(chan struct{})
	var called atomic.Int32
	fired := make(chan struct{})

	go func() {
		closeOnClosed(closedCh, doneCh, func() {
			called.Add(1)
			close(fired)
		})
	}()

	// Simulate Client.Close() firing the closedCh.
	close(closedCh)

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelFn not called within 2s after closedCh fired")
	}
	if got := called.Load(); got != 1 {
		t.Errorf("cancelFn called %d times, want 1", got)
	}
}

func TestCloseOnClosed_ExitsOnDone(t *testing.T) {
	closedCh := make(chan struct{})
	doneCh := make(chan struct{})
	var called atomic.Int32
	exited := make(chan struct{})

	go func() {
		defer close(exited)
		closeOnClosed(closedCh, doneCh, func() { called.Add(1) })
	}()

	// Simulate the Subscribe call returning normally: close doneCh
	// before closedCh ever fires.
	close(doneCh)

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("closeOnClosed did not exit within 2s after doneCh fired")
	}
	if got := called.Load(); got != 0 {
		t.Errorf("cancelFn called %d times on done-path, want 0", got)
	}
}

func TestCloseOnClosed_NilCancelFn(t *testing.T) {
	closedCh := make(chan struct{})
	doneCh := make(chan struct{})
	exited := make(chan struct{})

	go func() {
		defer close(exited)
		// nil cancelFn must not panic when closedCh fires.
		closeOnClosed(closedCh, doneCh, nil)
	}()

	close(closedCh)

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("closeOnClosed did not exit within 2s with nil cancelFn")
	}
}

func TestCloseOnClosed_ClosedChWinsRace(t *testing.T) {
	// If closedCh is already closed at entry, cancelFn must fire
	// (Go select picks a ready case; with both ready either can win,
	// but when only closedCh is ready at entry the result is
	// deterministic: cancelFn fires). This mirrors the realistic case
	// where Client.Close() has already run by the time
	// DoNotifierAlertService reaches the watchdog spawn point.
	closedCh := make(chan struct{})
	close(closedCh)
	doneCh := make(chan struct{})
	var called atomic.Int32
	exited := make(chan struct{})

	go func() {
		defer close(exited)
		closeOnClosed(closedCh, doneCh, func() { called.Add(1) })
	}()

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("closeOnClosed did not exit within 2s")
	}
	if got := called.Load(); got != 1 {
		t.Errorf("cancelFn called %d times, want 1", got)
	}
}
