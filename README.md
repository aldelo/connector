# Project Overview
connector intends to provide a simpler coding experience with gRPC, and microservices development.

When it comes to setting up gRPC server, connecting from gRPC client, as well as supporting microservices related needs,
instead of creating a large framework or package, with many features that we may not use, an emphasis is placed on coding
productivity as well as reducing amount of repetitive typing / boilerplate coding.

On the server end, the entry point is /service folder's Service struct. 
On the client side, the entry point is /client folder's Client struct.
Both server and client supports config file based setup.

Since we use AWS extensively, many features integrates with AWS: (Goal is for these features to work automatically with minimum configuration)

- Service Discovery
    - connector's service upon launch, auto registers with AWS cloud map for service discovery
    - On the client side, service discovery is automatic, see config for discovery options
- Health Checks
    - service instance health is integrated with cloud map's health check status
    - auto register and deregister on service discovery based on instance health
    - container level service serving status utilizes the gRPC health v1
    - config can be set on client to auto health check for serving status, as well as manual health probe
    - TODO: need to create out of process health witness for instance health management
- Load Balancing
    - client side name resolver is setup to retrieve multiple service endpoints and perform round robin load balancing
    - load balancing is per rpc call rather than per connection
- Metadata
    - Metadata helper methods provided
- RpcError
    - Rpc Error helper methods provided
- Compressor
    - gzip decompressor supported on service level
    - note added on client struct for passing gzip compressor via RPC call
- Server TLS / mTLS
    - server TLS / mTLS is configured via service or client config file
    - see /build/openssl-pem/make-pem.sh for CA, Server and Client Pem and Key self-signed creation  
    - server TLS / mTLS setup in gRPC service and client is required in order to secure channel
- Auth
    - TODO: will integrate via interceptor
- Circuit Breaker
    - client side, default using Hystrix-Go package for circuit breaker
    - circuit breaker is handled in client side unary and stream interceptors
    - circuit breaker options configured via client config file
- Rate Limiter
    - server side, default using Uber-RateLimit package for rate limiter
    - rate limit is handled in server side In-Tap-Handle
    - rate limit option configured via server config file
- Logger
    - TODO: Currently local logging using log.* but will update to zap
- Monitoring
    - TODO: looking to use prometheus, but might end up using something else
- Tracer
    - TODO: planning on using aws xray
- Queue
    - TODO: SQS
- Notification
    - TODO: SNS

#### project currently under development and not considered stable, please do not use under production environment at this point until this warning is removed

# Service connector
#### Overview
#### Example of Use
#### Notes

# Client connector
#### Overview
#### Example of Use
#### Notes

# Pre-Requisites
#### 1) Install ***protoc***
- In Terminal: (Mac)
    - brew install protobuf
#### 2) Install ***protoc-gen-go***
- In Terminal:
    - go install google.golang.org/protobuf/cmd/protoc-gen-go
    - More Info: https://developers.google.com/protocol-buffers/docs/reference/go-generated
#### 3) Install ***protoc-gen-go-grpc***
- install ***go-grpc_out***
    - Info: https://github.com/grpc/grpc-go/tree/master/cmd/protoc-gen-go-grpc
    - git clone -b v1.31.0 https://github.com/grpc/grpc-go
    - ( cd ../../cmd/protoc-gen-go-grpc && go install . )
    - Note: v1.31.0 = replace with latest version
#### 4) Info on ***proto3***
- https://developers.google.com/protocol-buffers/docs/gotutorial
- Use ***option go_package = "xyz";***
    - xyz should point to the full path from $GOPATH/src to this proto file folder
    - for example, "github.com/aldelo/connector/example/proto/test"
        - where "test" is the folder that contains proto files
#### 5) Executing ***protoc***
- protoc --go_out=$GOPATH/src --go-grpc_out=$GOPATH/src ./*.proto
    

