module github.com/aldelo/connector

go 1.15

require (
	github.com/aldelo/common v1.2.2
	github.com/aws/aws-sdk-go v1.40.18
	github.com/gin-contrib/cors v1.3.1
	github.com/gin-gonic/gin v1.7.3
	github.com/golang/protobuf v1.5.2
	google.golang.org/genproto v0.0.0-20210602131652-f16073e35f0c
	google.golang.org/grpc v1.39.1
	google.golang.org/protobuf v1.27.1
)

// remove the following code
//replace github.com/aldelo/common => ../common
