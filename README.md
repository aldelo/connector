# Project Overview
connector intends to provide a simpler coding experience with gRPC, and microservices development.

When it comes to setting up gRPC server, connecting from gRPC client, as well as supporting microservices related needs,
instead of creating a large framework or package, with many features that we may not use, an emphasis is placed on coding
productivity as well as reducing amount of repetitive typing / boilerplate coding.

On the server end, the entry point is /service folder's Service struct. 
On the client side, the entry point is /client folder's Client struct.
Both server and client supports yaml config file based setup.

Since we use AWS extensively, many features integrates with AWS: (Goal is for these features to work automatically with minimum configuration)
Additionally, SNS is used for service discovery push notification to all connected clients, so that when new gRPC services are spun up, the clients can 
utilize automatically.

- Service Discovery
    - connector's service upon launch, auto registers with AWS cloud map for service discovery
    - On the client side, service discovery is automatic, see config for discovery options
    - SNS is used for service discovery push notification to all subscribed clients, so whenever new services are started up,
      or existing services shuts down, connected gRPC clients may auto refresh endpoints dynamically
- Health Checks
    - service instance health is integrated with cloud map's health check status
    - auto register and deregister on service discovery based on instance health
    - container-level service serving status utilizes the gRPC health v1
    - config can be set on client to auto health check for serving status, as well as manual health probe
    - DynamoDB is also used to track service state information, so that a custom service can be used to clean up any stale connections. See example under /example/cmd/snsgateway
- Load Balancing
    - client side name resolver is setup to retrieve multiple service endpoints and perform round robin load balancing
    - load balancing is per rpc call rather than per connection
    - SNS service discovery will dynamically update load balancer's endpoints whenever services starts or shuts down
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
    - TODO: Future Implementation, currently using mTLS for transport level security
- Circuit Breaker
    - client side, default using Hystrix-Go package for circuit breaker
    - circuit breaker is handled in client side unary and stream interceptors
    - circuit breaker options configured via client config file
- Rate Limiter
    - server side, default using Uber-RateLimit package for rate limiter
    - rate limit is handled in server side In-Tap-Handle
    - rate limit option configured via server config file
- Logger
    - TODO: Future Implementation for Cloud Logging to DynamoDB table as log destination
- Monitoring
    - TODO: Future Implementation
- Tracer
    - AWS XRay is used for distributed tracing
    - enabled via yaml config file for /service, /client, /notifiergateay, /notifierserver code
    - XRay tracing is already built in for the following code wrappers:
      - kms, s3, dynamodb, redis, mysql via sqlx, ses, sns, sqs, cloudmap
      - gin web server
      - gRPC service, gRPC client
    - XRay requires aws xray daemon to be deployed as sidecar on EC2 or ECS, see AWS documentation for details,
      simple comments inline under XRay code wrapper provided too for setup guidance
- Queue
    - AWS SQS is used as the message queue within this package
- Notification
    - AWS SNS is used as the notification services within this package
    - SNS callback requires public accessible host, see /notifiergateway for supporting this requirement
    - gRPC client to /notifiergateway, and enabling SNS callback to stream down to the gRPC client requires /notifierserver
    - Using /notifiergateway deployed on public host such as under ALB, and /notifierserver deployed either public or private within VPC, 
      enables gRPC clients to be completely private in or out of VPC, where SNS callbacks can stream to gRPC client in real-time

# Service connector
#### Overview
/service folder contains the gRPC service (server) wrapper.  
#### Example of Use
See /example/cmd/server for a working gRPC server setup that serves test service
#### Notes
- TLS self sign certificates must be setup and placed into x509 sub folder
- Use /build/openssl-pem/make-pem.sh to create self-signed openssl ca, cert and key
- the service.yaml config file must be set properly and aws resources enabled
- to save aws access id and secret key, use aws cli => aws configure

# Client connector
#### Overview
/client folder contains the gRPC client (dialer) wrapper.
#### Example of Use
See /example/cmd/client for a working gRPC client setup to consume the gRPC service server
#### Notes
- Each gRPC server service that client needs to consume, create its own service yaml in /endpoint folder
- Each target gRPC service is described via service yaml config file in endpoint, so be sure to correctly define its config values
- TLS self sign certificates must be setup and placed into x509 sub folder
- Use /build/openssl-pem/make-pem.sh to create self-signed openssl ca, cert and key
- to save aws access id and secret key, use aws cli => aws configure

# Pre-Requisites
#### 1) Install or Upgrade ***protoc***
- In Terminal: (Mac)
    - ~ brew install protobuf
    - ~ brew upgrade protobuf
#### 2) Install ***protoc-gen-go***
- In Terminal:
    - ~ go install google.golang.org/protobuf/cmd/protoc-gen-go
    - More Info: https://developers.google.com/protocol-buffers/docs/reference/go-generated
#### 3) Install ***protoc-gen-go-grpc***
- install ***go-grpc_out***
    - Info: https://github.com/grpc/grpc-go/tree/master/cmd/protoc-gen-go-grpc
    - Info: https://grpc.io/docs/languages/go/quickstart/
- In Terminal:
    - ~ export GO111MODULE=on
    - ~ go install google.golang.org/grpc/cmd/protoc-gen-go-grpc
#### 4) Info on ***proto3***
- https://developers.google.com/protocol-buffers/docs/gotutorial
- Define ***package*** using OrganizationOrProject.Service.Type pattern
    - com.company.service
    - project.proto.service
    - domain.service.type, etc.
- Use ***option go_package = "xyz";***
    - xyz should point to the full path from $GOPATH/src to this proto file folder
    - for example, "github.com/aldelo/connector/example/proto/test"
        - where "test" is the folder that contains proto files
- ***import*** should contain full path from project folder to the proto file
    - in GoLand (JetBrains), Preferences -> Languages & Preferences -> Protocol Buffers
        - Uncheck "Configure Automatically"
        - Add path to $GOPATH/src
        - Add path to $GOPATH/pkg/mod
        - Also, under Descriptor Path
          - Input: /github.com/golang/protobuf@v1.3.5/protoc-gen-go/descriptor/descriptor.proto
          - Note: protobuf@v1.3.5, replace with later version if there is descriptor.proto file
#### 5) Executing ***protoc***
- In Terminal:
    - go to the folder containing .proto files
    - ~ protoc --go_out=$GOPATH/src --go-grpc_out=$GOPATH/src --proto_path=$GOPATH/src $GOPATH/src/xyz.../*.proto
        - where xyz... refers to the actual full path below $GOPATH/src up to the folder containing the proto files
    
# Checking Version Info
#### 1) Latest protoc Version on brew
- https://formulae.brew.sh/formula/protobuf
#### 2) Latest protobuf Version
- https://github.com/golang/protobuf/releases
#### 3) Latest grpc for go Version
- https://github.com/grpc/grpc-go/releases
#### 4) Latest protoc-gen-go Version
- https://pkg.go.dev/google.golang.org/protobuf/cmd/protoc-gen-go
#### 5) Latest protoc-gen-go-grpc Version
- https://github.com/grpc/grpc-go/tree/master/cmd/protoc-gen-go-grpc
#### 6) Latest genproto Version
- https://pkg.go.dev/google.golang.org/genproto

#go.mod Edit
#### remove the following code (Unless github.com/aldelo/common is in the path)
     replace github.com/aldelo/common => ../common
