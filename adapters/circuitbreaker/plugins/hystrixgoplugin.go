package plugins

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
	"fmt"

	util "github.com/aldelo/common"
	"github.com/aldelo/common/wrapper/hystrixgo"
	data "github.com/aldelo/common/wrapper/zap"

	"sync"
)

// HystrixGoPlugin each instance represents a specific command
// circuit breaker is tracking against multiple commands
// use a map to track all commands and invoke circuit breaker per command
type HystrixGoPlugin struct {
	HystrixGo *hystrixgo.CircuitBreaker
	mu        sync.RWMutex
}

// NewHystrixGoPlugin creates a hystrixgo plugin struct object
// this plugin implements the CircuitBreakerIFace interface
//
// Config Properties:
//  1. commandName = (required) name of the circuit breaker command
//  1. Timeout = (optional) how long to wait for command to complete, in milliseconds, default = 1000
//  2. MaxConcurrentRequests = (optional) how many commands of the same type can run at the same time, default = 10
//  3. RequestVolumeThreshold = (optional) minimum number of requests needed before a circuit can be tripped due to health, default = 20
//  4. SleepWindow = (optional) how long to wait after a circuit opens before testing for recovery, in milliseconds, default = 5000
//  5. ErrorPercentThreshold = (optional) causes circuits to open once the rolling measure of errors exceeds this percent of requests, default = 50
//  6. Logger = (optional) indicates the logger that will be used in the Hystrix package, nil = logs nothing
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

	// assign defaults for zero values
	if timeout <= 0 {
		timeout = 1000
	}
	if maxConcurrentRequests <= 0 {
		maxConcurrentRequests = 10
	}
	if requestVolumeThreshold <= 0 {
		requestVolumeThreshold = 20
	}
	if sleepWindow <= 0 {
		sleepWindow = 5000
	}
	if errorPercentThreshold <= 0 {
		errorPercentThreshold = 50
	}

	// create plugin
	p := &HystrixGoPlugin{
		HystrixGo: &hystrixgo.CircuitBreaker{
			CommandName:            commandName,
			TimeOut:                timeout,
			MaxConcurrentRequests:  maxConcurrentRequests,
			RequestVolumeThreshold: requestVolumeThreshold,
			SleepWindow:            sleepWindow,
			ErrorPercentThreshold:  errorPercentThreshold,
			Logger:                 logger,
			DisableCircuitBreaker:  false,
		},
	}

	// invoke hystrixgo init
	if err := p.HystrixGo.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize HystrixGo: %w", err) // preserve context
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

	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.HystrixGo == nil {
		return nil, fmt.Errorf("HystrixGo Object Not Initialized")
	}

	if runFn == nil {
		return nil, fmt.Errorf("HystrixGo Exec runFn Function Is Nil")
	}

	if async {
		return p.HystrixGo.Go(runFn, fallbackFn, dataIn)
	} else {
		return p.HystrixGo.Do(runFn, fallbackFn, dataIn)
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

	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.HystrixGo == nil {
		return nil, fmt.Errorf("HystrixGo Object Not Initialized")
	}

	if ctx == nil {
		return nil, fmt.Errorf("HystrixGo ExecWithContext ctx Context Is Nil")
	}

	if runFn == nil {
		return nil, fmt.Errorf("HystrixGo ExecWithContext runFn Function Is Nil")
	}

	if async {
		return p.HystrixGo.GoC(ctx, runFn, fallbackFn, dataIn)
	} else {
		return p.HystrixGo.DoC(ctx, runFn, fallbackFn, dataIn)
	}
}

// Reset will cause circuit breaker to reset all circuits from memory
func (p *HystrixGoPlugin) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.HystrixGo != nil {
		p.HystrixGo.FlushAll()
	}
}

// Update will update circuit breaker internal config
//  1. Timeout = (optional) how long to wait for command to complete, in milliseconds, default = 1000
//  2. MaxConcurrentRequests = (optional) how many commands of the same type can run at the same time, default = 10
//  3. RequestVolumeThreshold = (optional) minimum number of requests needed before a circuit can be tripped due to health, default = 20
//  4. SleepWindow = (optional) how long to wait after a circuit opens before testing for recovery, in milliseconds, default = 5000
//  5. ErrorPercentThreshold = (optional) causes circuits to open once the rolling measure of errors exceeds this percent of requests, default = 50
//  6. Logger = (optional) indicates the logger that will be used in the Hystrix package, nil = logs nothing
func (p *HystrixGoPlugin) Update(timeout int,
	maxConcurrentRequests int,
	requestVolumeThreshold int,
	sleepWindow int,
	errorPercentThreshold int,
	logger *data.ZapLog) error {

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.HystrixGo == nil {
		return fmt.Errorf("HystrixGo Object Not Initialized")
	}

	// Only update values if > 0, otherwise preserve current values
	if timeout > 0 {
		p.HystrixGo.TimeOut = timeout
	}
	if maxConcurrentRequests > 0 {
		p.HystrixGo.MaxConcurrentRequests = maxConcurrentRequests
	}
	if requestVolumeThreshold > 0 {
		p.HystrixGo.RequestVolumeThreshold = requestVolumeThreshold
	}
	if sleepWindow > 0 {
		p.HystrixGo.SleepWindow = sleepWindow
	}
	if errorPercentThreshold > 0 {
		p.HystrixGo.ErrorPercentThreshold = errorPercentThreshold
	}

	if logger != nil {
		p.HystrixGo.Logger = logger
	}

	// These functions presumably handle their own concurrency
	p.HystrixGo.UpdateConfig()
	p.HystrixGo.UpdateLogger()

	return nil
}

// Disable will disable circuit breaker services
// true = disable; false = re-engage circuit breaker service
func (p *HystrixGoPlugin) Disable(b bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.HystrixGo != nil {
		p.HystrixGo.DisableCircuitBreaker = b
	}
}
