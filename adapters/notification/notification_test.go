package notification

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
	"testing"

	"github.com/aldelo/common/wrapper/sns"
	"github.com/aldelo/common/wrapper/sns/snsprotocol"
)

// nonNilSNS returns a zero-value SNS struct pointer to pass nil-checks
// without requiring AWS credentials.
func nonNilSNS() *sns.SNS {
	return &sns.SNS{}
}

func TestListTopics_NilSNS(t *testing.T) {
	_, err := ListTopics(nil)
	if err == nil {
		t.Fatal("expected error for nil SNS, got nil")
	}
	if want := "SNS notification object is required"; !containsStr(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestCreateTopic_NilSNS(t *testing.T) {
	_, err := CreateTopic(nil, "test-topic")
	if err == nil {
		t.Fatal("expected error for nil SNS, got nil")
	}
	if want := "SNS notification object is required"; !containsStr(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestCreateTopic_EmptyName(t *testing.T) {
	_, err := CreateTopic(nonNilSNS(), "")
	if err == nil {
		t.Fatal("expected error for empty topic name, got nil")
	}
	if want := "topic name is required"; !containsStr(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestCreateTopic_WhitespaceOnlyName(t *testing.T) {
	_, err := CreateTopic(nonNilSNS(), "   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only topic name, got nil")
	}
	if want := "topic name is required"; !containsStr(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestSubscribe_InputValidation(t *testing.T) {
	tests := []struct {
		name     string
		snsObj   *sns.SNS
		topicArn string
		protocol snsprotocol.SNSProtocol
		endPoint string
		wantErr  string
	}{
		{
			name:     "nil SNS",
			snsObj:   nil,
			topicArn: "arn:aws:sns:us-east-1:123456789:test",
			protocol: snsprotocol.Http,
			endPoint: "http://example.com",
			wantErr:  "SNS notification object is required",
		},
		{
			name:     "empty topic ARN",
			snsObj:   nonNilSNS(),
			topicArn: "",
			protocol: snsprotocol.Http,
			endPoint: "http://example.com",
			wantErr:  "topic ARN is required",
		},
		{
			name:     "whitespace-only topic ARN",
			snsObj:   nonNilSNS(),
			topicArn: "   ",
			protocol: snsprotocol.Http,
			endPoint: "http://example.com",
			wantErr:  "topic ARN is required",
		},
		{
			name:     "UNKNOWN protocol",
			snsObj:   nonNilSNS(),
			topicArn: "arn:aws:sns:us-east-1:123456789:test",
			protocol: snsprotocol.UNKNOWN,
			endPoint: "http://example.com",
			wantErr:  "protocol is required",
		},
		{
			name:     "invalid protocol value",
			snsObj:   nonNilSNS(),
			topicArn: "arn:aws:sns:us-east-1:123456789:test",
			protocol: snsprotocol.SNSProtocol(999),
			endPoint: "http://example.com",
			wantErr:  "protocol is required",
		},
		{
			name:     "empty endpoint",
			snsObj:   nonNilSNS(),
			topicArn: "arn:aws:sns:us-east-1:123456789:test",
			protocol: snsprotocol.Http,
			endPoint: "",
			wantErr:  "endpoint is required",
		},
		{
			name:     "whitespace-only endpoint",
			snsObj:   nonNilSNS(),
			topicArn: "arn:aws:sns:us-east-1:123456789:test",
			protocol: snsprotocol.Https,
			endPoint: "   ",
			wantErr:  "endpoint is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Subscribe(tc.snsObj, tc.topicArn, tc.protocol, tc.endPoint)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !containsStr(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestUnsubscribe_NilSNS(t *testing.T) {
	err := Unsubscribe(nil, "arn:aws:sns:us-east-1:123456789:test:sub-id")
	if err == nil {
		t.Fatal("expected error for nil SNS, got nil")
	}
	if want := "SNS notification object is required"; !containsStr(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestUnsubscribe_EmptySubscriptionArn(t *testing.T) {
	err := Unsubscribe(nonNilSNS(), "")
	if err == nil {
		t.Fatal("expected error for empty subscription ARN, got nil")
	}
	if want := "subscription ARN is required"; !containsStr(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestUnsubscribe_WhitespaceOnlySubscriptionArn(t *testing.T) {
	err := Unsubscribe(nonNilSNS(), "   ")
	if err == nil {
		t.Fatal("expected error for whitespace-only subscription ARN, got nil")
	}
	if want := "subscription ARN is required"; !containsStr(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

func TestPublish_InputValidation(t *testing.T) {
	tests := []struct {
		name     string
		snsObj   *sns.SNS
		topicArn string
		message  string
		wantErr  string
	}{
		{
			name:     "nil SNS",
			snsObj:   nil,
			topicArn: "arn:aws:sns:us-east-1:123456789:test",
			message:  "hello",
			wantErr:  "SNS notification object is required",
		},
		{
			name:     "empty topic ARN",
			snsObj:   nonNilSNS(),
			topicArn: "",
			message:  "hello",
			wantErr:  "topic ARN is required",
		},
		{
			name:     "whitespace-only topic ARN",
			snsObj:   nonNilSNS(),
			topicArn: "   ",
			message:  "hello",
			wantErr:  "topic ARN is required",
		},
		{
			name:     "empty message",
			snsObj:   nonNilSNS(),
			topicArn: "arn:aws:sns:us-east-1:123456789:test",
			message:  "",
			wantErr:  "message is required",
		},
		{
			name:     "whitespace-only message",
			snsObj:   nonNilSNS(),
			topicArn: "arn:aws:sns:us-east-1:123456789:test",
			message:  "   ",
			wantErr:  "message is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Publish(tc.snsObj, tc.topicArn, tc.message, nil)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !containsStr(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// containsStr checks if s contains substr (avoids importing strings in test).
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && searchStr(s, substr)
}

func searchStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
