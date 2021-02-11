package plugins

/*
 * Copyright 2020-2021 Aldelo, LP
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
	"github.com/aldelo/common/wrapper/hystrixgo"
	"context"
	data "github.com/aldelo/common/wrapper/zap"
)

// each hystrixgoplugin represents a specific command
// circuit breaker is tracking against multiple commands
// use a map to track all commands and invoke circuit breaker per command
type HystrixGoPlugin struct {
	HystrixGo *hystrixgo.CircuitBreaker
}

// NewHystrixGoPlugin creates a hystrixgo plugin struct object
// this plugin implements the CircuitBreakerIFace interface
//
// Config Properties:
//		1) commandName = (required) name of the circuit breaker command
//		1) Timeout = (optional) how long to wait for command to complete, in milliseconds, default = 1000
//		2) MaxConcurrentRequests = (optional) how many commands of the same type can run at the same time, default = 10
//		3) RequestVolumeThreshold = (optional) minimum number of requests needed before a circuit can be tripped due to health, default = 20
//		4) SleepWindow = (optional) how long to wait after a circuit opens before testing for recovery, in milliseconds, default = 5000
//		5) ErrorPercentThreshold = (optional) causes circuits to open once the rolling measure of errors exceeds this percent of requests, default = 50
//		6) Logger = (optional) indicates the logger that will be used in the Hystrix package, nil = logs nothing
func NewHystrixGoPlugin(commandName string,
						timeout int,
						maxConcurrentRequests int,
						requestVolumeThreshold int,
						sleepWindow int,
						errorPercentThreshold int,
						logger *data.ZapLog) (*HystrixGoPlugin, error) {
	// validate required
	if util.LenTrim(commandName) == 0 {
		return nil, fmt.Errorf("HystrixGo Circuit Breaker Command Name is Required")
	}

	// create plugin
	p := &HystrixGoPlugin{
		HystrixGo: &hystrixgo.CircuitBreaker{
			CommandName: commandName,
			TimeOut: timeout,
			MaxConcurrentRequests: maxConcurrentRequests,
			RequestVolumeThreshold: requestVolumeThreshold,
			SleepWindow: sleepWindow,
			ErrorPercentThreshold: errorPercentThreshold,
			Logger: logger,
			DisableCircuitBreaker: false,
		},
	}

	// invoke hystrixgo init
	if err := p.HystrixGo.Init(); err != nil {
		return nil, err
	}

	// return plugin
	return p, nil
}

// Exec offers both async and sync execution of circuit breaker action
//
// runFn = function to be executed under circuit breaker
// fallbackFn = function to be executed if runFn fails
// dataIn = input parameter value for runFn and fallbackFn
func (p *HystrixGoPlugin) Exec(async bool,
							   runFn func(dataIn interface{}, ctx ...context.Context) (dataOut interface{}, err error),
							   fallbackFn func(dataIn interface{}, errIn error, ctx ...context.Context) (dataOut interface{}, err error),
							   dataIn interface{}) (interface{}, error) {
	if p.HystrixGo != nil {
		if async {
			return p.HystrixGo.Go(runFn, fallbackFn, dataIn)
		} else {
			return p.HystrixGo.Do(runFn, fallbackFn, dataIn)
		}
	} else {
		return nil, fmt.Errorf("HystrixGo Object Not Initialized")
	}
}

// ExecWithContext offers both async and sync execution of circuit breaker action with context
//
// runFn = function to be executed under circuit breaker
// fallbackFn = function to be executed if runFn fails
// dataIn = input parameter value for runFn and fallbackFn
func (p *HystrixGoPlugin) ExecWithContext(async bool,
										  ctx context.Context,
										  runFn func(dataIn interface{}, ctx ...context.Context) (dataOut interface{}, err error),
										  fallbackFn func(dataIn interface{}, errIn error, ctx ...context.Context) (dataOut interface{}, err error),
										  dataIn interface{}) (interface{}, error) {
	if p.HystrixGo != nil {
		if async {
			return p.HystrixGo.GoC(ctx, runFn, fallbackFn, dataIn)
		} else {
			return p.HystrixGo.DoC(ctx, runFn, fallbackFn, dataIn)
		}
	} else {
		return nil, fmt.Errorf("HystrixGo Object Not Initialized")
	}
}

// Reset will cause circuit breaker to reset all circuits from memory
func (p *HystrixGoPlugin) Reset() {
	if p.HystrixGo != nil {
		p.HystrixGo.FlushAll()
	}
}

// Update will update circuit breaker internal config
//		1) Timeout = (optional) how long to wait for command to complete, in milliseconds, default = 1000
//		2) MaxConcurrentRequests = (optional) how many commands of the same type can run at the same time, default = 10
//		3) RequestVolumeThreshold = (optional) minimum number of requests needed before a circuit can be tripped due to health, default = 20
//		4) SleepWindow = (optional) how long to wait after a circuit opens before testing for recovery, in milliseconds, default = 5000
//		5) ErrorPercentThreshold = (optional) causes circuits to open once the rolling measure of errors exceeds this percent of requests, default = 50
//		6) Logger = (optional) indicates the logger that will be used in the Hystrix package, nil = logs nothing
func (p *HystrixGoPlugin) Update(timeout int,
								 maxConcurrentRequests int,
								 requestVolumeThreshold int,
								 sleepWindow int,
								 errorPercentThreshold int,
								 logger *data.ZapLog) error {
	if p.HystrixGo != nil {
		p.HystrixGo.TimeOut = timeout
		p.HystrixGo.MaxConcurrentRequests = maxConcurrentRequests
		p.HystrixGo.RequestVolumeThreshold = requestVolumeThreshold
		p.HystrixGo.SleepWindow = sleepWindow
		p.HystrixGo.ErrorPercentThreshold = errorPercentThreshold
		p.HystrixGo.Logger = logger

		p.HystrixGo.UpdateConfig()
		p.HystrixGo.UpdateLogger()

		return nil
	} else {
		return fmt.Errorf("HystrixGo Object Not Initialized")
	}
}

// Disable will disable circuit breaker services
// true = disable; false = re-engage circuit breaker service
func (p *HystrixGoPlugin) Disable(b bool) {
	if p.HystrixGo != nil {
		p.HystrixGo.DisableCircuitBreaker = b
	}
}


