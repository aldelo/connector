package notification

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
	"fmt"
	util "github.com/aldelo/common"
	awshttp2 "github.com/aldelo/common/wrapper/aws"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/sns"
	"github.com/aldelo/common/wrapper/sns/snsprotocol"
	snsaws "github.com/aws/aws-sdk-go/service/sns"
	"time"
)

// NewNotificationAdapter creates a new sns service provider, and auto connect for use
func NewNotificationAdapter(awsRegion awsregion.AWSRegion, httpOptions *awshttp2.HttpClientSettings) (*sns.SNS, error) {
	n := &sns.SNS{
		AwsRegion: awsRegion,
		HttpOptions: httpOptions,
	}

	if err := n.Connect(); err != nil {
		return nil, err
	} else {
		return n, nil
	}
}

// ListTopics will return list of all topic arns for the given sns object
func ListTopics(n *sns.SNS, timeoutDuration ...time.Duration) (topicArnsList []string, err error) {
	if n == nil {
		return []string{}, fmt.Errorf("ListTopics Failed: SNS Notification Object is Required")
	}

	var nextToken string

	for {
		if t, nt, e := n.ListTopics(nextToken, timeoutDuration...); e != nil {
			return []string{}, fmt.Errorf("ListTopics Failed: %s", e)
		} else {
			topicArnsList = append(topicArnsList, t...)

			if util.LenTrim(nt) > 0 {
				nextToken = nt
			} else {
				break
			}
		}
	}

	return topicArnsList, nil
}

// CreateTopic will create a new sns notification's topic
func CreateTopic(n *sns.SNS, topicName string, timeoutDuration ...time.Duration) (topicArn string, err error) {
	if n == nil {
		return "", fmt.Errorf("CreateTopic Failed: SNS Notification Object is Required")
	}

	if util.LenTrim(topicName) == 0 {
		return "", fmt.Errorf("CreateTopic Failed: Topic Name is Required")
	}

	if topicArn, err = n.CreateTopic(topicName, nil, timeoutDuration...); err != nil {
		return "", fmt.Errorf("CreateTopic Failed: %s", err)
	} else {
		return topicArn, nil
	}
}

// Subscribe will subscribe client to a sns notification's topic arn
func Subscribe(n *sns.SNS, topicArn string, protocol snsprotocol.SNSProtocol, endPoint string, timeoutDuration ...time.Duration) (subscriptionArn string, err error) {
	if n == nil {
		return "", fmt.Errorf("Subscribe Failed: SNS Notification Object is Required")
	}

	if util.LenTrim(topicArn) == 0 {
		return "", fmt.Errorf("Subscribe Failed: Topic ARN is Required")
	}

	if !protocol.Valid() || protocol == snsprotocol.UNKNOWN {
		return "", fmt.Errorf("Subscribe Failed: Protocol is Required")
	}

	if util.LenTrim(endPoint) == 0 {
		return "", fmt.Errorf("Subscribe Failed: Endpoint is Required")
	}

	return n.Subscribe(topicArn, protocol, endPoint, nil, timeoutDuration...)
}

// Unsubscribe will unsubscribe a subscription from sns notification system
func Unsubscribe(n *sns.SNS, subscriptionArn string, timeoutDuration ...time.Duration) error {
	if n == nil {
		return fmt.Errorf("Unsubscribe Failed: SNS Notification Object is Required")
	}

	if util.LenTrim(subscriptionArn) == 0 {
		return fmt.Errorf("Unsubscribe Failed: Subscription ARN is Required")
	}

	return n.Unsubscribe(subscriptionArn, timeoutDuration...)
}

// Publish will publish a message to sns notification system for subscribers to consume
func Publish(n *sns.SNS, topicArn string, message string, attributes map[string]*snsaws.MessageAttributeValue, timeoutDuration ...time.Duration) (messageId string, err error) {
	if n == nil {
		return "", fmt.Errorf("Publish Failed: SNS Notification Object is Required")
	}

	if util.LenTrim(topicArn) == 0 {
		return "", fmt.Errorf("Publish Failed: Topic ARN is Required")
	}

	if util.LenTrim(message) == 0 {
		return "", fmt.Errorf("Publish Failed: Message is Required")
	}

	return n.Publish(topicArn, "", message, "", attributes, timeoutDuration...)
}
