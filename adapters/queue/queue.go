package queue

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
	awshttp2 "github.com/aldelo/common/wrapper/aws"
	"github.com/aldelo/common/wrapper/aws/awsregion"
	"github.com/aldelo/common/wrapper/sqs"
)

// NewQueueAdapter creates a new sqs queue service provider, and auto connect for use
func NewQueueAdapter(awsRegion awsregion.AWSRegion, httpOptions *awshttp2.HttpClientSettings) (*sqs.SQS, error) {
	q := &sqs.SQS{
		AwsRegion: awsRegion,
		HttpOptions: httpOptions,
	}

	if err := q.Connect(); err != nil {
		return nil, err
	} else {
		return q, nil
	}
}

