#!/bin/bash

cd keys/ca
cp ca_cert.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/grpcclient/x509/ca_cert.pem
cp ca_cert.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/grpcserver/x509/ca_cert.pem
cp ca_cert.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/grpcnotifier/x509/ca_cert.pem
cp ca_cert.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/snsgateway/x509/ca_cert.pem

cd ..
cd client
cp client_cert.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/grpcclient/x509/client_cert.pem

cd private
cp client_key.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/grpcclient/x509/client_key.pem

cd ..
cd ..
cd server
cp server_cert.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/grpcserver/x509/server_cert.pem
cp server_cert.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/grpcnotifier/x509/server_cert.pem

cd private
cp server_key.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/grpcserver/x509/server_key.pem
cp server_key.pem $GOPATH/src/github.com/aldelo/connector/example/cmd/grpcnotifier/x509/server_key.pem
