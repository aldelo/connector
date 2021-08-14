module github.com/aldelo/connector

go 1.15

require (
	github.com/aldelo/common v1.2.3
	github.com/aws/aws-sdk-go v1.40.22
	github.com/gin-contrib/cors v1.3.1
	github.com/gin-gonic/gin v1.7.3
	github.com/golang/protobuf v1.5.2
	google.golang.org/genproto v0.0.0-20210813162853-db860fec028c
	google.golang.org/grpc v1.40.0
	google.golang.org/protobuf v1.27.1
)

// remove the following code
//replace github.com/aldelo/common => ../common
