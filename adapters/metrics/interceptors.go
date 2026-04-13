package metrics

/*
 * Copyright 2020-2026 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 */

// gRPC server and client interceptors that emit baseline RPC metrics
// through the Sink interface. Wire them via Service.UnaryServerInterceptors
// (server side) or grpc.WithUnaryInterceptor at Dial time (client side).
//
// Metric series emitted (all opt-in via the sink you choose):
//
//   counter   grpc_server_requests_total{method, code}
//   histogram grpc_server_request_duration_seconds{method}
//   counter   grpc_server_errors_total{method, code}     -- only on err
//   counter   grpc_client_requests_total{method, code}
//   histogram grpc_client_request_duration_seconds{method}
//   counter   grpc_client_errors_total{method, code}     -- only on err
//
// Naming convention follows Prometheus' gRPC ecosystem prefixes so a
// future Prometheus Sink will produce series names that are recognizable
// to existing dashboards.

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	mServerRequests = "grpc_server_requests_total"
	mServerDuration = "grpc_server_request_duration_seconds"
	mServerErrors   = "grpc_server_errors_total"
	mClientRequests = "grpc_client_requests_total"
	mClientDuration = "grpc_client_request_duration_seconds"
	mClientErrors   = "grpc_client_errors_total"
)

// NewServerInterceptors returns a (unary, stream) interceptor pair that
// emits baseline RPC metrics for every served request. If sink is nil,
// NopSink is substituted so the call site can omit the nil check.
//
// Wire up:
//
//	uIntr, sIntr := metrics.NewServerInterceptors(mySink)
//	svc.UnaryServerInterceptors  = append(svc.UnaryServerInterceptors,  uIntr)
//	svc.StreamServerInterceptors = append(svc.StreamServerInterceptors, sIntr)
//
// Place these AFTER any context-decoration interceptors (auth, tenancy)
// and BEFORE recovery so panics still register as Internal errors in the
// metrics stream.
func NewServerInterceptors(sink Sink) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	s := sinkOrNop(sink)

	unary := func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		recordServer(s, info.FullMethod, time.Since(start), err)
		return resp, err
	}

	stream := func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		recordServer(s, info.FullMethod, time.Since(start), err)
		return err
	}

	return unary, stream
}

// NewClientInterceptors returns a (unary, stream) interceptor pair that
// emits baseline RPC metrics for every outbound request. If sink is nil,
// NopSink is substituted.
//
// Wire up via grpc.WithUnaryInterceptor / grpc.WithStreamInterceptor at
// Dial time, OR via the connector Client's interceptor slots if exposed.
func NewClientInterceptors(sink Sink) (grpc.UnaryClientInterceptor, grpc.StreamClientInterceptor) {
	s := sinkOrNop(sink)

	unary := func(ctx context.Context, method string, req, reply interface{},
		cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		recordClient(s, method, time.Since(start), err)
		return err
	}

	stream := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn,
		method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		start := time.Now()
		cs, err := streamer(ctx, desc, cc, method, opts...)
		// We instrument stream creation only — full-stream duration would
		// require wrapping the ClientStream which is out of scope for the
		// baseline. Per-message metrics belong in a separate, opt-in
		// wrapper.
		recordClient(s, method, time.Since(start), err)
		return cs, err
	}

	return unary, stream
}

// recordServer emits the standard 3-metric set for one served RPC.
// Centralized so the unary and stream paths cannot drift on label keys.
func recordServer(s Sink, method string, dur time.Duration, err error) {
	code := codeOf(err).String()
	labels := map[string]string{"method": method, "code": code}

	s.Counter(mServerRequests, labels, 1)
	s.Observe(mServerDuration, map[string]string{"method": method}, dur.Seconds())
	if err != nil {
		s.Counter(mServerErrors, labels, 1)
	}
}

// recordClient emits the standard 3-metric set for one outbound RPC.
func recordClient(s Sink, method string, dur time.Duration, err error) {
	code := codeOf(err).String()
	labels := map[string]string{"method": method, "code": code}

	s.Counter(mClientRequests, labels, 1)
	s.Observe(mClientDuration, map[string]string{"method": method}, dur.Seconds())
	if err != nil {
		s.Counter(mClientErrors, labels, 1)
	}
}

// codeOf extracts the gRPC status code from an error. Mirrors the helper
// in adapters/logger — duplicated rather than imported to keep the
// metrics package free of cross-adapter dependencies.
func codeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	return status.Code(err)
}

// sinkOrNop returns sink if non-nil, otherwise NopSink. Centralized so
// every constructor handles nil identically.
func sinkOrNop(sink Sink) Sink {
	if sink == nil {
		return NopSink{}
	}
	return sink
}
