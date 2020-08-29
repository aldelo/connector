#!/bin/bash

###  SET VAR VALUES ###
COUNTRY="US"
STATE="CA"
CITY="Pleasanton"
COMPANY="Example Company"
CN="*.example.com"

# SAN must contain the same DNS domain as in CN for gRPC TLS to work
SERVERSAN="DNS:localhost,DNS:*.example.com,IP:0.0.0.0,IP:127.0.0.1"
CLIENTSAN="DNS:localhost,DNS:*.example.com,IP:0.0.0.0,IP:127.0.0.1"

### CREATE PEM SCRIPT BELOW ###
mkdir keys
cd keys

# generate a self signed certificate for the CA along with a key
mkdir -p ca/private
chmod 700 ca/private
openssl req -x509 -nodes -days 3650 \
	-newkey rsa:4096 -sha256 \
	-keyout ca/private/ca_key.pem \
	-out ca/ca_cert.pem \
	-subj "/C=$COUNTRY/ST=$STATE/L=$CITY/O=$COMPANY/CN=$CN"

# create server private key and certificate request
mkdir -p server/private
chmod 700 server/private
openssl genrsa -out server/private/server_key.pem 4096
openssl req -new -key server/private/server_key.pem \
	-out server/server.csr \
	-subj "/C=$COUNTRY/ST=$STATE/L=$CITY/O=$COMPANY/CN=$CN" \
	-sha256 \
	-reqexts v3_req -extensions v3_req -config <(cat $GOPATH/src/github.com/aldelo/connector/build/openssl-pem/custom.cnf ; printf '[ v3_req ]\nsubjectAltName='$SERVERSAN)

# create client private key and certificate request
mkdir -p client/private
chmod 700 client/private
openssl genrsa -out client/private/client_key.pem 4096
openssl req -new -key client/private/client_key.pem \
	-out client/client.csr \
	-subj "/C=$COUNTRY/ST=$STATE/L=$CITY/O=$COMPANY/CN=$CN" \
	-sha256 \
	-reqexts v3_req -extensions v3_req -config <(cat $GOPATH/src/github.com/aldelo/connector/build/openssl-pem/custom.cnf ; printf '[ v3_req ]\nsubjectAltName='$CLIENTSAN)

# generate certificates
openssl x509 -req -days 3650 \
	-in server/server.csr \
	-CA ca/ca_cert.pem \
	-CAkey ca/private/ca_key.pem \
	-CAcreateserial \
	-out server/server_cert.pem \
	-sha256 \
	-extensions v3_req -extfile <(cat $GOPATH/src/github.com/aldelo/connector/build/openssl-pem/custom.cnf ; printf '[ v3_req ]\nsubjectAltName='$SERVERSAN)

openssl x509 -req -days 3650 \
	-in client/client.csr \
	-CA ca/ca_cert.pem \
	-CAkey ca/private/ca_key.pem \
	-CAcreateserial \
	-out client/client_cert.pem \
	-sha256 \
	-extensions v3_req -extfile <(cat $GOPATH/src/github.com/aldelo/connector/build/openssl-pem/custom.cnf ; printf '[ v3_req ]\nsubjectAltName='$CLIENTSAN)
