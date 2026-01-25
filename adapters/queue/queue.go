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
	"encoding/json"
	"fmt"
	"time"

	util "github.com/aldelo/common"
	awshttp2 "github.com/aldelo/common/wrapper/aws"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/sqs"
	"github.com/aldelo/common/wrapper/sqs/sqscreatequeueattribute"
	"github.com/aldelo/common/wrapper/sqs/sqsgetqueueattribute"
	"github.com/aldelo/common/wrapper/sqs/sqssetqueueattribute"
	awssqs "github.com/aws/aws-sdk-go/service/sqs"
)

// NewQueueAdapter creates a new sqs queue service provider, and auto connect for use
func NewQueueAdapter(awsRegion awsregion.AWSRegion, httpOptions *awshttp2.HttpClientSettings) (*sqs.SQS, error) {
	q := &sqs.SQS{
		AwsRegion:   awsRegion,
		HttpOptions: httpOptions,
	}

	if err := q.Connect(); err != nil {
		return nil, err
	} else {
		return q, nil
	}
}

// composeSnsPolicy builds the policy that allows an SNS topic to send to the queue
func composeSnsPolicy(snsTopicArn, queueArn string) string {
	if util.LenTrim(snsTopicArn) == 0 || util.LenTrim(queueArn) == 0 {
		return ""
	}

	policy := `{
		  "Version":"2012-10-17",
		  "Statement": [{ 
			"Effect":"Allow", 
			"Principal": { 
			  "Service": "sns.amazonaws.com" 
			}, 
			"Action":"sqs:SendMessage", 
			"Resource":"[QUEUE-ARN]", 
			"Condition":{ 
			  "ArnEquals":{ 
				"aws:SourceArn":"[TOPIC-ARN]" 
			  } 
			} 
		  }] 
		}`

	policy = util.Replace(policy, "[TOPIC-ARN]", snsTopicArn)
	policy = util.Replace(policy, "[QUEUE-ARN]", queueArn)
	return policy
}

// helper to safely merge SNS policy without overwriting existing statements
func ensureSnsPolicy(q *sqs.SQS, queueUrl, queueArn, snsTopicArn string, timeoutDuration ...time.Duration) error {
	if q == nil || util.LenTrim(snsTopicArn) == 0 || util.LenTrim(queueArn) == 0 {
		return nil
	}

	// Fetch existing policy (if any)
	attrs, err := q.GetQueueAttributes(queueUrl, []sqsgetqueueattribute.SQSGetQueueAttribute{
		sqsgetqueueattribute.Policy,
	}, timeoutDuration...)
	if err != nil {
		return fmt.Errorf("GetQueue Failed: (Get Queue Attributes Error) %s", err.Error())
	}

	existing := ""
	if v, ok := attrs[sqsgetqueueattribute.Policy]; ok {
		existing = v
	}

	newStmt := map[string]interface{}{
		"Effect":    "Allow",
		"Principal": map[string]interface{}{"Service": "sns.amazonaws.com"},
		"Action":    "sqs:SendMessage",
		"Resource":  queueArn,
		"Condition": map[string]interface{}{"ArnEquals": map[string]interface{}{"aws:SourceArn": snsTopicArn}},
	}

	// If no existing policy, create a new one
	if util.LenTrim(existing) == 0 {
		policy := map[string]interface{}{
			"Version":   "2012-10-17",
			"Statement": []interface{}{newStmt},
		}
		bytes, _ := json.Marshal(policy)
		return q.SetQueueAttributes(queueUrl, map[sqssetqueueattribute.SQSSetQueueAttribute]string{
			sqssetqueueattribute.Policy: string(bytes),
		}, timeoutDuration...)
	}

	// Try to merge into existing policy; fall back to overwrite if parse fails
	var policyDoc map[string]interface{}
	if err := json.Unmarshal([]byte(existing), &policyDoc); err != nil {
		// safest path: set a minimal policy that enables SNS (better than silently failing)
		policy := composeSnsPolicy(snsTopicArn, queueArn)
		return q.SetQueueAttributes(queueUrl, map[sqssetqueueattribute.SQSSetQueueAttribute]string{
			sqssetqueueattribute.Policy: policy,
		}, timeoutDuration...)
	}

	statements := []interface{}{}
	switch st := policyDoc["Statement"].(type) {
	case []interface{}:
		statements = st
	case map[string]interface{}:
		statements = []interface{}{st}
	}

	// Check if the statement already exists
	for _, st := range statements {
		if m, ok := st.(map[string]interface{}); ok {
			resource, _ := m["Resource"].(string)
			cond, _ := m["Condition"].(map[string]interface{})
			arnEq, _ := cond["ArnEquals"].(map[string]interface{})
			sourceArn, _ := arnEq["aws:SourceArn"].(string)
			if resource == queueArn && sourceArn == snsTopicArn {
				// already present; no update needed
				return nil
			}
		}
	}

	// Append new statement and write back
	statements = append(statements, newStmt)
	policyDoc["Statement"] = statements
	bytes, _ := json.Marshal(policyDoc)

	return q.SetQueueAttributes(queueUrl, map[sqssetqueueattribute.SQSSetQueueAttribute]string{
		sqssetqueueattribute.Policy: string(bytes),
	}, timeoutDuration...)
}

// GetQueue will retrieve queueUrl and queueArn based on queueName,
// if queue is not found, a new queue will be created with the given queueName
// snsTopicArn = optional, set sns topic arn if needing to allow sns topic to send message to this newly created sqs
func GetQueue(q *sqs.SQS, queueName string, messageRetentionSeconds uint, snsTopicArn string, timeoutDuration ...time.Duration) (queueUrl string, queueArn string, err error) {
	if q == nil {
		return "", "", fmt.Errorf("Queue Object is Required")
	}

	if util.LenTrim(queueName) == 0 {
		return "", "", fmt.Errorf("QueueName is Required")
	}

	// use existing queue if already exist
	notFound := false
	queueUrl, notFound, err = q.GetQueueUrl(queueName, timeoutDuration...)

	if err != nil {
		return "", "", fmt.Errorf("GetQueue Failed: %s", err.Error())
	}

	if !notFound {
		// found queue
		if queueArn, e := q.GetQueueArnFromQueue(queueUrl, timeoutDuration...); e != nil {
			return "", "", fmt.Errorf("GetQueue Failed: (%s) %s", "Get Queue ARN From Attribute Error", e.Error())
		} else {
			// Apply SNS policy even when queue already exists
			if util.LenTrim(snsTopicArn) > 0 {
				if err = ensureSnsPolicy(q, queueUrl, queueArn, snsTopicArn, timeoutDuration...); err != nil {
					return "", "", err
				}
			}

			return queueUrl, queueArn, nil
		}
	} else {
		// queue not exist, create new
		if messageRetentionSeconds == 0 {
			messageRetentionSeconds = 300
		} else if messageRetentionSeconds < 60 {
			messageRetentionSeconds = 60
		} else if messageRetentionSeconds > 1209600 {
			messageRetentionSeconds = 1209600
		}

		if queueUrl, err = q.CreateQueue(queueName, map[sqscreatequeueattribute.SQSCreateQueueAttribute]string{
			sqscreatequeueattribute.MessageRetentionPeriod: util.UintToStr(messageRetentionSeconds),
		}, timeoutDuration...); err != nil {
			// create queue failed
			return "", "", fmt.Errorf("CreateQueue Failed: %s", err.Error())
		} else {
			// queue created
			queueArn, e := q.GetQueueArnFromQueue(queueUrl, timeoutDuration...)

			if e != nil {
				return "", "", fmt.Errorf("CreateQueue Failed: (%s) %s", "Get Queue ARN From Attribute Error", e.Error())
			}

			// apply SNS policy using merge helper (safe even if future defaults add a policy)
			if util.LenTrim(snsTopicArn) > 0 {
				if err = ensureSnsPolicy(q, queueUrl, queueArn, snsTopicArn, timeoutDuration...); err != nil {
					return "", "", err
				}
			}

			return queueUrl, queueArn, nil
		}
	}
}

// SendMessage will send a message to given queue
func SendMessage(q *sqs.SQS, queueUrl string, messageBody string, messageAttributes map[string]*awssqs.MessageAttributeValue, timeoutDuration ...time.Duration) (messageId string, err error) {
	if q == nil {
		return "", fmt.Errorf("Queue Object is Required")
	}

	if util.LenTrim(queueUrl) == 0 {
		return "", fmt.Errorf("QueueUrl is Required")
	}

	if util.LenTrim(messageBody) == 0 {
		return "", fmt.Errorf("MessageBody is Required")
	}

	if result, err := q.SendMessage(queueUrl, messageBody, messageAttributes, 0, timeoutDuration...); err != nil {
		// send message error
		return "", fmt.Errorf("SendMessage Failed: " + err.Error())
	} else {
		// send message successful
		return result.MessageId, nil
	}
}

// ReceiveMessages will attempt to receive up to 10 messages from given queueUrl
func ReceiveMessages(q *sqs.SQS, queueUrl string, messageAttributeFilters []string, timeoutDuration ...time.Duration) (messageList []*sqs.SQSReceivedMessage, err error) {
	if q == nil {
		return []*sqs.SQSReceivedMessage{}, fmt.Errorf("Queue Object is Required")
	}

	if util.LenTrim(queueUrl) == 0 {
		return []*sqs.SQSReceivedMessage{}, fmt.Errorf("QueueUrl is Required")
	}

	if list, err := q.ReceiveMessage(queueUrl, 10, messageAttributeFilters,
		nil, 0, 0, "", timeoutDuration...); err != nil {
		// error
		return []*sqs.SQSReceivedMessage{}, fmt.Errorf("ReceiveMessage Failed: " + err.Error())
	} else {
		// success
		return list, nil
	}
}

// DeleteMessages will delete one or more messages defined in deleteRequests,
// if any delete failures, failed deletions will be returned via failList
func DeleteMessages(q *sqs.SQS, queueUrl string, deleteRequests []*sqs.SQSDeleteMessageRequest, timeoutDuration ...time.Duration) (failList []*sqs.SQSFailResult, err error) {
	if q == nil {
		return []*sqs.SQSFailResult{}, fmt.Errorf("Queue Object is Required")
	}

	if util.LenTrim(queueUrl) == 0 {
		return []*sqs.SQSFailResult{}, fmt.Errorf("QueueUrl is Required")
	}

	if len(deleteRequests) == 0 {
		return []*sqs.SQSFailResult{}, fmt.Errorf("DeleteRequests are Required")
	}

	// enforce SQS batch limit (10) by chunking requests
	for start := 0; start < len(deleteRequests); start += 10 {
		end := start + 10
		if end > len(deleteRequests) {
			end = len(deleteRequests)
		}

		_, batchFail, batchErr := q.DeleteMessageBatch(queueUrl, deleteRequests[start:end], timeoutDuration...)
		if batchErr != nil {
			return []*sqs.SQSFailResult{}, fmt.Errorf("DeleteMessages Failed: " + batchErr.Error())
		}
		failList = append(failList, batchFail...)
	}

	if len(failList) == 0 {
		return nil, nil
	}
	return failList, nil
}
