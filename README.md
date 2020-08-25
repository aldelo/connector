# Project Overview
 Golang based gRPC server and client connectors, for microservice development

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
    

