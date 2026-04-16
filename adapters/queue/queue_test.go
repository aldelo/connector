package queue

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
	"strings"
	"testing"

	"github.com/aldelo/common/wrapper/sqs"
)

// nonNilSQS returns a zero-value SQS struct pointer to pass nil-checks
// without requiring AWS credentials.
func nonNilSQS() *sqs.SQS {
	return &sqs.SQS{}
}

// ---------------------------------------------------------------------------
// GetQueue — input validation
// ---------------------------------------------------------------------------

func TestGetQueue_NilQueue(t *testing.T) {
	_, _, err := GetQueue(nil, "test-queue", 300, "")
	if err == nil {
		t.Fatal("expected error for nil queue, got nil")
	}
	if want := "Queue Object is Required"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestGetQueue_EmptyQueueName(t *testing.T) {
	_, _, err := GetQueue(nonNilSQS(), "", 300, "")
	if err == nil {
		t.Fatal("expected error for empty queue name, got nil")
	}
	if want := "QueueName is Required"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestGetQueue_WhitespaceOnlyQueueName(t *testing.T) {
	_, _, err := GetQueue(nonNilSQS(), "   ", 300, "")
	if err == nil {
		t.Fatal("expected error for whitespace-only queue name, got nil")
	}
	if want := "QueueName is Required"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestGetQueue_InvalidQueueName(t *testing.T) {
	tests := []struct {
		name      string
		queueName string
	}{
		{"contains special chars", "queue!@#$"},
		{"contains spaces", "queue name"},
		{"too long", strings.Repeat("a", 81)},
		{"too long with .fifo", strings.Repeat("a", 81) + ".fifo"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := GetQueue(nonNilSQS(), tc.queueName, 300, "")
			if err == nil {
				t.Fatal("expected error for invalid queue name, got nil")
			}
			if want := "QueueName is invalid"; !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q does not contain %q", err.Error(), want)
			}
		})
	}
}

func TestGetQueue_ValidQueueNamePatterns(t *testing.T) {
	// These names pass the regex validation but will fail at the AWS API call
	// (since we have no connection). We only verify the name validation passes.
	tests := []struct {
		name      string
		queueName string
	}{
		{"simple name", "my-queue"},
		{"with underscore", "my_queue"},
		{"alphanumeric", "queue123"},
		{"fifo suffix", "my-queue.fifo"},
		{"max length 80", strings.Repeat("a", 80)},
		{"max length with fifo", strings.Repeat("a", 75) + ".fifo"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := GetQueue(nonNilSQS(), tc.queueName, 300, "")
			if err == nil {
				// Shouldn't happen without AWS, but if it does the name was valid
				return
			}
			// The error should NOT be about the queue name being invalid
			if strings.Contains(err.Error(), "QueueName is invalid") {
				t.Fatalf("valid queue name %q was rejected as invalid", tc.queueName)
			}
			if strings.Contains(err.Error(), "QueueName is Required") {
				t.Fatalf("valid queue name %q was rejected as empty", tc.queueName)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SendMessage — input validation
// ---------------------------------------------------------------------------

func TestSendMessage_InputValidation(t *testing.T) {
	tests := []struct {
		name     string
		q        *sqs.SQS
		queueUrl string
		body     string
		wantErr  string
	}{
		{
			name:     "nil queue",
			q:        nil,
			queueUrl: "https://sqs.us-east-1.amazonaws.com/123456789/test",
			body:     "hello",
			wantErr:  "Queue Object is Required",
		},
		{
			name:     "empty queue URL",
			q:        nonNilSQS(),
			queueUrl: "",
			body:     "hello",
			wantErr:  "QueueUrl is Required",
		},
		{
			name:     "whitespace-only queue URL",
			q:        nonNilSQS(),
			queueUrl: "   ",
			body:     "hello",
			wantErr:  "QueueUrl is Required",
		},
		{
			name:     "empty message body",
			q:        nonNilSQS(),
			queueUrl: "https://sqs.us-east-1.amazonaws.com/123456789/test",
			body:     "",
			wantErr:  "MessageBody is Required",
		},
		{
			name:     "whitespace-only message body",
			q:        nonNilSQS(),
			queueUrl: "https://sqs.us-east-1.amazonaws.com/123456789/test",
			body:     "   ",
			wantErr:  "MessageBody is Required",
		},
		{
			name:     "FIFO queue URL rejected",
			q:        nonNilSQS(),
			queueUrl: "https://sqs.us-east-1.amazonaws.com/123456789/test.fifo",
			body:     "hello",
			wantErr:  "FIFO queue detected; use SendMessageFIFO",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SendMessage(tc.q, tc.queueUrl, tc.body, nil)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SendMessageFIFO — input validation
// ---------------------------------------------------------------------------

func TestSendMessageFIFO_InputValidation(t *testing.T) {
	fifoUrl := "https://sqs.us-east-1.amazonaws.com/123456789/test.fifo"
	nonFifoUrl := "https://sqs.us-east-1.amazonaws.com/123456789/test"

	tests := []struct {
		name    string
		q       *sqs.SQS
		url     string
		body    string
		groupId string
		dedupId string
		wantErr string
	}{
		{
			name:    "nil queue",
			q:       nil,
			url:     fifoUrl,
			body:    "hello",
			groupId: "group1",
			dedupId: "dedup1",
			wantErr: "Queue Object is Required",
		},
		{
			name:    "empty queue URL",
			q:       nonNilSQS(),
			url:     "",
			body:    "hello",
			groupId: "group1",
			dedupId: "dedup1",
			wantErr: "QueueUrl is Required",
		},
		{
			name:    "non-FIFO queue URL",
			q:       nonNilSQS(),
			url:     nonFifoUrl,
			body:    "hello",
			groupId: "group1",
			dedupId: "dedup1",
			wantErr: "target queue is not FIFO",
		},
		{
			name:    "empty message body",
			q:       nonNilSQS(),
			url:     fifoUrl,
			body:    "",
			groupId: "group1",
			dedupId: "dedup1",
			wantErr: "MessageBody is Required",
		},
		{
			name:    "whitespace-only message body",
			q:       nonNilSQS(),
			url:     fifoUrl,
			body:    "   ",
			groupId: "group1",
			dedupId: "dedup1",
			wantErr: "MessageBody is Required",
		},
		{
			name:    "empty message group ID",
			q:       nonNilSQS(),
			url:     fifoUrl,
			body:    "hello",
			groupId: "",
			dedupId: "dedup1",
			wantErr: "MessageGroupId is Required",
		},
		{
			name:    "whitespace-only message group ID",
			q:       nonNilSQS(),
			url:     fifoUrl,
			body:    "hello",
			groupId: "   ",
			dedupId: "dedup1",
			wantErr: "MessageGroupId is Required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SendMessageFIFO(tc.q, tc.url, tc.body, tc.groupId, tc.dedupId, nil)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ReceiveMessages — input validation
// ---------------------------------------------------------------------------

func TestReceiveMessages_InputValidation(t *testing.T) {
	tests := []struct {
		name     string
		q        *sqs.SQS
		queueUrl string
		wantErr  string
	}{
		{
			name:     "nil queue",
			q:        nil,
			queueUrl: "https://sqs.us-east-1.amazonaws.com/123456789/test",
			wantErr:  "Queue Object is Required",
		},
		{
			name:     "empty queue URL",
			q:        nonNilSQS(),
			queueUrl: "",
			wantErr:  "QueueUrl is Required",
		},
		{
			name:     "whitespace-only queue URL",
			q:        nonNilSQS(),
			queueUrl: "   ",
			wantErr:  "QueueUrl is Required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ReceiveMessages(tc.q, tc.queueUrl, nil)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DeleteMessages — input validation
// ---------------------------------------------------------------------------

func TestDeleteMessages_InputValidation(t *testing.T) {
	validUrl := "https://sqs.us-east-1.amazonaws.com/123456789/test"

	tests := []struct {
		name     string
		q        *sqs.SQS
		queueUrl string
		reqs     []*sqs.SQSDeleteMessageRequest
		wantErr  string
	}{
		{
			name:     "nil queue",
			q:        nil,
			queueUrl: validUrl,
			reqs:     []*sqs.SQSDeleteMessageRequest{{Id: "1", ReceiptHandle: "rh1"}},
			wantErr:  "Queue Object is Required",
		},
		{
			name:     "empty queue URL",
			q:        nonNilSQS(),
			queueUrl: "",
			reqs:     []*sqs.SQSDeleteMessageRequest{{Id: "1", ReceiptHandle: "rh1"}},
			wantErr:  "QueueUrl is Required",
		},
		{
			name:     "whitespace-only queue URL",
			q:        nonNilSQS(),
			queueUrl: "   ",
			reqs:     []*sqs.SQSDeleteMessageRequest{{Id: "1", ReceiptHandle: "rh1"}},
			wantErr:  "QueueUrl is Required",
		},
		{
			name:     "nil delete requests slice",
			q:        nonNilSQS(),
			queueUrl: validUrl,
			reqs:     nil,
			wantErr:  "DeleteRequests are Required",
		},
		{
			name:     "empty delete requests slice",
			q:        nonNilSQS(),
			queueUrl: validUrl,
			reqs:     []*sqs.SQSDeleteMessageRequest{},
			wantErr:  "DeleteRequests are Required",
		},
		{
			name:     "nil entry in delete requests",
			q:        nonNilSQS(),
			queueUrl: validUrl,
			reqs:     []*sqs.SQSDeleteMessageRequest{nil},
			wantErr:  "DeleteRequests[0] is nil",
		},
		{
			name:     "empty receipt handle",
			q:        nonNilSQS(),
			queueUrl: validUrl,
			reqs:     []*sqs.SQSDeleteMessageRequest{{Id: "1", ReceiptHandle: ""}},
			wantErr:  "DeleteRequests[0].ReceiptHandle is required",
		},
		{
			name:     "second entry nil",
			q:        nonNilSQS(),
			queueUrl: validUrl,
			reqs: []*sqs.SQSDeleteMessageRequest{
				{Id: "0", ReceiptHandle: "rh0"},
				nil,
			},
			wantErr: "DeleteRequests[1] is nil",
		},
		{
			name:     "second entry empty receipt handle",
			q:        nonNilSQS(),
			queueUrl: validUrl,
			reqs: []*sqs.SQSDeleteMessageRequest{
				{Id: "0", ReceiptHandle: "rh0"},
				{Id: "1", ReceiptHandle: ""},
			},
			wantErr: "DeleteRequests[1].ReceiptHandle is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DeleteMessages(tc.q, tc.queueUrl, tc.reqs)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DeleteMessages — auto-assigns Id when empty
// ---------------------------------------------------------------------------

func TestDeleteMessages_AutoAssignsId(t *testing.T) {
	// Verify the function assigns an Id to entries with empty Id
	// before validation continues. We pass a valid ReceiptHandle
	// so the entry passes receipt-handle validation.
	// The call will still fail because we have no AWS connection,
	// but the error should NOT be about Id being empty.
	reqs := []*sqs.SQSDeleteMessageRequest{
		{Id: "", ReceiptHandle: "rh-test"},
	}
	_, err := DeleteMessages(nonNilSQS(), "https://sqs.us-east-1.amazonaws.com/123456789/test", reqs)
	// We expect a failure downstream (no AWS connection), not a validation failure about Id
	if err == nil {
		return // unexpected success; nothing else to verify
	}
	if strings.Contains(err.Error(), "Id") {
		t.Fatalf("error should not be about Id field, got: %q", err.Error())
	}
	// Verify the Id was auto-assigned
	if reqs[0].Id != "0" {
		t.Fatalf("expected auto-assigned Id %q, got %q", "0", reqs[0].Id)
	}
}

// ---------------------------------------------------------------------------
// ensureSnsPolicy — input validation (unexported, tested from same package)
// ---------------------------------------------------------------------------

func TestEnsureSnsPolicy_InputValidation(t *testing.T) {
	tests := []struct {
		name        string
		q           *sqs.SQS
		queueUrl    string
		queueArn    string
		snsTopicArn string
		wantErr     string
	}{
		{
			name:        "empty queue URL",
			q:           nonNilSQS(),
			queueUrl:    "",
			queueArn:    "arn:aws:sqs:us-east-1:123456789:test",
			snsTopicArn: "arn:aws:sns:us-east-1:123456789:topic",
			wantErr:     "queueUrl is required",
		},
		{
			name:        "nil queue client",
			q:           nil,
			queueUrl:    "https://sqs.us-east-1.amazonaws.com/123456789/test",
			queueArn:    "arn:aws:sqs:us-east-1:123456789:test",
			snsTopicArn: "arn:aws:sns:us-east-1:123456789:topic",
			wantErr:     "queue client is required",
		},
		{
			name:        "empty queue ARN",
			q:           nonNilSQS(),
			queueUrl:    "https://sqs.us-east-1.amazonaws.com/123456789/test",
			queueArn:    "",
			snsTopicArn: "arn:aws:sns:us-east-1:123456789:topic",
			wantErr:     "queueArn is required",
		},
		{
			name:        "whitespace-only queue ARN",
			q:           nonNilSQS(),
			queueUrl:    "https://sqs.us-east-1.amazonaws.com/123456789/test",
			queueArn:    "   ",
			snsTopicArn: "arn:aws:sns:us-east-1:123456789:topic",
			wantErr:     "queueArn is required",
		},
		{
			name:        "empty SNS topic ARN",
			q:           nonNilSQS(),
			queueUrl:    "https://sqs.us-east-1.amazonaws.com/123456789/test",
			queueArn:    "arn:aws:sqs:us-east-1:123456789:test",
			snsTopicArn: "",
			wantErr:     "snsTopicArn is required",
		},
		{
			name:        "whitespace-only SNS topic ARN",
			q:           nonNilSQS(),
			queueUrl:    "https://sqs.us-east-1.amazonaws.com/123456789/test",
			queueArn:    "arn:aws:sqs:us-east-1:123456789:test",
			snsTopicArn: "   ",
			wantErr:     "snsTopicArn is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ensureSnsPolicy(tc.q, tc.queueUrl, tc.queueArn, tc.snsTopicArn)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// fifoQueueNamePattern — regex validation
// ---------------------------------------------------------------------------

func TestFifoQueueNamePattern(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"simple name", "my-queue", true},
		{"with underscore", "my_queue", true},
		{"alphanumeric", "queue123", true},
		{"fifo suffix", "my-queue.fifo", true},
		{"max length 80 chars", strings.Repeat("x", 80), true},
		{"75 chars plus .fifo", strings.Repeat("x", 75) + ".fifo", true},
		{"empty string", "", false},
		{"special chars", "queue!@#", false},
		{"spaces", "my queue", false},
		{"81 chars no fifo", strings.Repeat("x", 81), false},
		{"dot only", ".", false},
		{"leading dot fifo", ".fifo", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := fifoQueueNamePattern.MatchString(tc.input)
			if got != tc.want {
				t.Fatalf("fifoQueueNamePattern.MatchString(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}
