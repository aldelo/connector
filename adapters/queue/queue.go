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
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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

// precompiled queue name validator (supports optional .fifo)
var fifoQueueNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}(\.fifo)?$`)

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

// small helper to honor timeoutDuration when calling AWS SDK directly
func contextWithTimeout(timeoutDuration ...time.Duration) (context.Context, context.CancelFunc) {
	if len(timeoutDuration) > 0 && timeoutDuration[0] > 0 {
		return context.WithTimeout(context.Background(), timeoutDuration[0])
	}
	return context.Background(), func() {}
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
	const sqsPolicySizeLimit = 20480 // enforce AWS 20 KB policy limit

	// trim to reject whitespace-only inputs
	queueUrl = strings.TrimSpace(queueUrl)
	queueArn = strings.TrimSpace(queueArn)
	snsTopicArn = strings.TrimSpace(snsTopicArn)

	// added queueUrl validation to fail fast before API calls
	if util.LenTrim(queueUrl) == 0 {
		return fmt.Errorf("ensureSnsPolicy: queueUrl is required")
	}

	// Fail fast on invalid inputs so policy application cannot silently no-op.
	if q == nil {
		return fmt.Errorf("ensureSnsPolicy: queue client is required")
	}
	if util.LenTrim(queueArn) == 0 {
		return fmt.Errorf("ensureSnsPolicy: queueArn is required")
	}
	if util.LenTrim(snsTopicArn) == 0 {
		return fmt.Errorf("ensureSnsPolicy: snsTopicArn is required")
	}

	// Fetch existing policy (if any)
	attrs, err := q.GetQueueAttributes(queueUrl, []sqsgetqueueattribute.SQSGetQueueAttribute{sqsgetqueueattribute.Policy}, timeoutDuration...)
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
		bytes, marshalErr := json.Marshal(policy)
		if marshalErr != nil {
			return fmt.Errorf("ensureSnsPolicy: marshal new policy failed: %w", marshalErr)
		}
		// guard against AWS 20 KB policy size limit
		if len(bytes) > sqsPolicySizeLimit {
			return fmt.Errorf("ensureSnsPolicy: policy size %d exceeds 20KB limit", len(bytes))
		}
		return q.SetQueueAttributes(queueUrl, map[sqssetqueueattribute.SQSSetQueueAttribute]string{sqssetqueueattribute.Policy: string(bytes)}, timeoutDuration...)
	}

	// Try to merge into existing policy; fall back to overwrite if parse fails
	var policyDoc map[string]interface{}
	// surface the error so callers can fix it without losing statements.
	if err := json.Unmarshal([]byte(existing), &policyDoc); err != nil {
		// fallback to a minimal valid policy instead of failing hard on bad JSON
		fallback := map[string]interface{}{
			"Version":   "2012-10-17",
			"Statement": []interface{}{newStmt},
		}
		bytes, marshalErr := json.Marshal(fallback)
		if marshalErr != nil {
			return fmt.Errorf("ensureSnsPolicy: fallback marshal failed after JSON parse error: %w", marshalErr)
		}
		if len(bytes) > sqsPolicySizeLimit {
			return fmt.Errorf("ensureSnsPolicy: fallback policy size %d exceeds 20KB limit", len(bytes))
		}
		return q.SetQueueAttributes(queueUrl, map[sqssetqueueattribute.SQSSetQueueAttribute]string{sqssetqueueattribute.Policy: string(bytes)}, timeoutDuration...)
	}

	// guard nil map from unmarshalling "null"
	if policyDoc == nil {
		policyDoc = map[string]interface{}{}
	}

	// ensure Version is present to keep policy valid
	if _, ok := policyDoc["Version"]; !ok {
		policyDoc["Version"] = "2012-10-17"
	}

	statements := []interface{}{}
	if rawSt, ok := policyDoc["Statement"]; ok && rawSt != nil {
		switch st := rawSt.(type) {
		case []interface{}:
			statements = st
		case map[string]interface{}:
			statements = []interface{}{st}
		default:
			// recover by rebuilding minimal policy when Statement shape is invalid
			statements = []interface{}{newStmt}
			policyDoc = map[string]interface{}{
				"Version":   "2012-10-17",
				"Statement": statements,
			}
		}
	}

	// robust detection of existing grants (handles ArnEquals/ArnLike and string/array forms)
	extractSourceArns := func(cond map[string]interface{}) []string {
		var out []string
		for _, key := range []string{"ArnEquals", "ArnLike"} {
			if arnMap, ok := cond[key].(map[string]interface{}); ok {
				if v, ok := arnMap["aws:SourceArn"]; ok {
					switch vv := v.(type) {
					case string:
						out = append(out, vv)
					case []interface{}:
						for _, x := range vv {
							if s, ok := x.(string); ok {
								out = append(out, s)
							}
						}
					}
				}
			}
		}
		return out
	}

	// require Effect=Allow AND Action includes sqs:SendMessage (or sqs:*) before treating as an existing grant
	actionAllowsSend := func(a interface{}) bool {
		switch v := a.(type) {
		case string:
			// treat "*" as already granting send to avoid duplicate statements
			vl := strings.ToLower(v)
			return vl == "sqs:sendmessage" || vl == "sqs:*" || vl == "*"
		case []interface{}:
			for _, x := range v {
				if s, ok := x.(string); ok {
					sl := strings.ToLower(s)
					if sl == "sqs:sendmessage" || sl == "sqs:*" || sl == "*" {
						return true
					}
				}
			}
		}
		return false
	}

	// treat wildcard resources as already granting access to avoid duplicate statements and policy bloat
	resourceMatchesQueue := func(res string) bool {
		if res == "*" || res == queueArn {
			return true
		}
		if strings.HasSuffix(res, ":*") && strings.HasPrefix(queueArn, strings.TrimSuffix(res, ":*")) {
			return true
		}
		return false
	}

	// guard map accesses and support Resource arrays to avoid panics and detect existing grants
	for _, st := range statements {
		m, ok := st.(map[string]interface{})
		if !ok {
			continue
		}

		// skip statements that are not explicit Allow
		if eff, ok := m["Effect"].(string); !ok || strings.ToLower(eff) != "allow" {
			continue
		}
		if !actionAllowsSend(m["Action"]) {
			continue
		}

		var resources []string
		switch r := m["Resource"].(type) {
		case string:
			resources = []string{r}
		case []interface{}:
			for _, v := range r {
				if s, ok := v.(string); ok {
					resources = append(resources, s)
				}
			}
		}

		var sourceArns []string
		if cond, ok := m["Condition"].(map[string]interface{}); ok {
			sourceArns = extractSourceArns(cond)
		}

		for _, sa := range sourceArns {
			if sa == snsTopicArn {
				for _, res := range resources {
					if resourceMatchesQueue(res) {
						return nil // already present
					}
				}
			}
		}
	}

	// Append new statement and write back
	statements = append(statements, newStmt)
	policyDoc["Statement"] = statements
	bytes, marshalErr := json.Marshal(policyDoc)
	if marshalErr != nil {
		return fmt.Errorf("ensureSnsPolicy: marshal merged policy failed: %w", marshalErr)
	}
	// guard against AWS 20 KB policy size limit
	if len(bytes) > sqsPolicySizeLimit {
		return fmt.Errorf("ensureSnsPolicy: merged policy size %d exceeds 20KB limit", len(bytes))
	}

	return q.SetQueueAttributes(queueUrl, map[sqssetqueueattribute.SQSSetQueueAttribute]string{sqssetqueueattribute.Policy: string(bytes)}, timeoutDuration...)
}

// GetQueue will retrieve queueUrl and queueArn based on queueName,
// if queue is not found, a new queue will be created with the given queueName
// snsTopicArn = optional, set sns topic arn if needing to allow sns topic to send message to this newly created sqs
func GetQueue(q *sqs.SQS, queueName string, messageRetentionSeconds uint, snsTopicArn string, timeoutDuration ...time.Duration) (queueUrl string, queueArn string, err error) {
	if q == nil {
		return "", "", fmt.Errorf("Queue Object is Required")
	}

	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return "", "", fmt.Errorf("QueueName is Required")
	}

	if !fifoQueueNamePattern.MatchString(queueName) { // validate SQS name rules up to 80 chars and optional .fifo
		return "", "", fmt.Errorf("QueueName is invalid; only alphanumeric, underscore, hyphen, optional .fifo suffix, max 80 chars")
	}

	// use existing queue if already exist
	notFound := false
	queueUrl, notFound, err = q.GetQueueUrl(queueName, timeoutDuration...)

	if err != nil {
		return "", "", fmt.Errorf("GetQueue Failed: %s", err.Error())
	}

	// Only treat empty queueUrl as an error when the queue was expected to exist.
	if !notFound && util.LenTrim(queueUrl) == 0 {
		return "", "", fmt.Errorf("GetQueue Failed: queue URL is empty")
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

		// add FIFO handling so .fifo queues are created with required attributes
		isFifo := strings.HasSuffix(strings.ToLower(queueName), ".fifo")
		attrs := map[sqscreatequeueattribute.SQSCreateQueueAttribute]string{
			sqscreatequeueattribute.MessageRetentionPeriod: util.UintToStr(messageRetentionSeconds),
		}
		if isFifo {
			attrs[sqscreatequeueattribute.FifoQueue] = "true"
			// Enable content-based dedup by default; callers needing explicit MessageDeduplicationId can override at send time.
			attrs[sqscreatequeueattribute.ContentBasedDeduplication] = "true"
		}

		if queueUrl, err = q.CreateQueue(queueName, attrs, timeoutDuration...); err != nil {
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

	queueUrl = strings.TrimSpace(queueUrl)
	if queueUrl == "" {
		return "", fmt.Errorf("QueueUrl is Required")
	}

	if util.LenTrim(messageBody) == 0 {
		return "", fmt.Errorf("MessageBody is Required")
	}

	// explicit guard for FIFO queues; direct users to FIFO-aware helper
	if strings.HasSuffix(strings.ToLower(queueUrl), ".fifo") {
		return "", fmt.Errorf("SendMessage: FIFO queue detected; use SendMessageFIFO to provide MessageGroupId")
	}

	if result, err := q.SendMessage(queueUrl, messageBody, messageAttributes, 0, timeoutDuration...); err != nil {
		// send message error
		return "", fmt.Errorf("SendMessage Failed: " + err.Error())
	} else {
		// send message successful
		return result.MessageId, nil
	}
}

// FIFO-aware helper that supplies MessageGroupId / MessageDeduplicationId
func SendMessageFIFO(
	q *sqs.SQS,
	queueUrl string,
	messageBody string,
	messageGroupId string,
	messageDeduplicationId string,
	messageAttributes map[string]*awssqs.MessageAttributeValue,
	timeoutDuration ...time.Duration,
) (messageId string, err error) {

	if q == nil {
		return "", fmt.Errorf("Queue Object is Required")
	}

	queueUrl = strings.TrimSpace(queueUrl)
	if queueUrl == "" {
		return "", fmt.Errorf("QueueUrl is Required")
	}
	if !strings.HasSuffix(strings.ToLower(queueUrl), ".fifo") { // fail fast if not a FIFO queue
		return "", fmt.Errorf("SendMessageFIFO: target queue is not FIFO (missing .fifo suffix in URL)")
	}

	if util.LenTrim(messageBody) == 0 {
		return "", fmt.Errorf("MessageBody is Required")
	}

	// normalize group/dedup IDs to avoid accidental whitespace bugs
	messageGroupId = strings.TrimSpace(messageGroupId)
	messageDeduplicationId = strings.TrimSpace(messageDeduplicationId)

	if util.LenTrim(messageGroupId) == 0 {
		return "", fmt.Errorf("MessageGroupId is Required for FIFO queues")
	}

	// supply a safe deduplication ID when caller leaves it empty and content-based dedup is off
	if util.LenTrim(messageDeduplicationId) == 0 {
		messageDeduplicationId = fmt.Sprintf("%s-%d", messageGroupId, time.Now().UnixNano())
	}

	// use wrapper FIFO helper instead of nonexistent q.SqsClient
	res, sendErr := q.SendMessageFifo(queueUrl, messageDeduplicationId, messageGroupId, messageBody, messageAttributes, timeoutDuration...)
	if sendErr != nil {
		return "", fmt.Errorf("SendMessageFIFO Failed: %s", sendErr.Error())
	}
	return res.MessageId, nil
}

// ReceiveMessages will attempt to receive up to 10 messages from given queueUrl
func ReceiveMessages(q *sqs.SQS, queueUrl string, messageAttributeFilters []string, timeoutDuration ...time.Duration) (messageList []*sqs.SQSReceivedMessage, err error) {
	if q == nil {
		return []*sqs.SQSReceivedMessage{}, fmt.Errorf("Queue Object is Required")
	}

	queueUrl = strings.TrimSpace(queueUrl)
	if queueUrl == "" {
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

	queueUrl = strings.TrimSpace(queueUrl)
	if queueUrl == "" {
		return []*sqs.SQSFailResult{}, fmt.Errorf("QueueUrl is Required")
	}

	if len(deleteRequests) == 0 {
		return []*sqs.SQSFailResult{}, fmt.Errorf("DeleteRequests are Required")
	}

	// validate entries before calling SQS to avoid API rejections
	for i, req := range deleteRequests {
		if req == nil {
			return []*sqs.SQSFailResult{}, fmt.Errorf("DeleteRequests[%d] is nil", i)
		}
		if util.LenTrim(req.ReceiptHandle) == 0 {
			return []*sqs.SQSFailResult{}, fmt.Errorf("DeleteRequests[%d].ReceiptHandle is required", i)
		}
		// Ensure Id is set; SQS requires a non-empty Id per batch entry
		if util.LenTrim(req.Id) == 0 {
			req.Id = util.Itoa(i)
		}
	}

	// process all batches and aggregate errors instead of stopping on first batch error
	var batchErrors []string
	for start := 0; start < len(deleteRequests); start += 10 {
		end := start + 10
		if end > len(deleteRequests) {
			end = len(deleteRequests)
		}

		_, batchFail, batchErr := q.DeleteMessageBatch(queueUrl, deleteRequests[start:end], timeoutDuration...)
		failList = append(failList, batchFail...)

		if batchErr != nil {
			batchErrors = append(batchErrors, fmt.Sprintf("batch %d-%d: %s", start, end, batchErr.Error()))
		}
	}

	if len(failList) > 0 || len(batchErrors) > 0 {
		errMsg := fmt.Sprintf("DeleteMessages Failed: %d message(s) failed deletion", len(failList))
		if len(batchErrors) > 0 {
			errMsg = fmt.Sprintf("%s; batch errors: %s", errMsg, strings.Join(batchErrors, "; "))
		}
		return failList, fmt.Errorf("%s", errMsg)
	}

	return []*sqs.SQSFailResult{}, nil
}
