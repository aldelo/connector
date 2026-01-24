package circuitbreaker

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

	data "github.com/aldelo/common/wrapper/zap"
)

type CircuitBreakerIFace interface {
	// Exec offers both async and sync execution of circuit breaker action
	//
	// runFn = function to be executed under circuit breaker
	// fallbackFn = function to be executed if runFn fails
	// dataIn = input parameter value for runFn and fallbackFn
	Exec(async bool,
		runFn func(dataIn interface{}, ctx ...context.Context) (dataOut interface{}, err error),
		fallbackFn func(dataIn interface{}, errIn error, ctx ...context.Context) (dataOut interface{}, err error),
		dataIn interface{}) (interface{}, error)

	// ExecWithContext offers both async and sync execution of circuit breaker action with context
	//
	// runFn = function to be executed under circuit breaker
	// fallbackFn = function to be executed if runFn fails
	// dataIn = input parameter value for runFn and fallbackFn
	ExecWithContext(async bool,
		ctx context.Context,
		runFn func(dataIn interface{}, ctx ...context.Context) (dataOut interface{}, err error),
		fallbackFn func(dataIn interface{}, errIn error, ctx ...context.Context) (dataOut interface{}, err error),
		dataIn interface{}) (interface{}, error)

	// Reset will cause circuit breaker to reset all circuits from memory
	Reset()

	// Update will update circuit breaker internal config
	//		1) Timeout = (optional) how long to wait for command to complete, in milliseconds, default = 1000
	//		2) MaxConcurrentRequests = (optional) how many commands of the same type can run at the same time, default = 10
	//		3) RequestVolumeThreshold = (optional) minimum number of requests needed before a circuit can be tripped due to health, default = 20
	//		4) SleepWindow = (optional) how long to wait after a circuit opens before testing for recovery, in milliseconds, default = 5000
	//		5) ErrorPercentThreshold = (optional) causes circuits to open once the rolling measure of errors exceeds this percent of requests, default = 50
	//		6) Logger = (optional) indicates the logger that will be used in the package, nil = logs nothing
	Update(timeout int,
		maxConcurrentRequests int,
		requestVolumeThreshold int,
		sleepWindow int,
		errorPercentThreshold int,
		logger *data.ZapLog) error

	// Disable will disable circuit breaker services
	// true = disable; false = re-engage circuit breaker service
	Disable(b bool)
}
