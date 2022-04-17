module github.com/aldelo/connector

go 1.15

require (
	github.com/aldelo/common v1.2.6
	github.com/aws/aws-sdk-go v1.43.34
	github.com/gin-contrib/cors v1.3.1
	github.com/gin-gonic/gin v1.7.7
	github.com/golang/protobuf v1.5.2
	google.golang.org/genproto v0.0.0-20220405205423-9d709892a2bf
	google.golang.org/grpc v1.45.0
	google.golang.org/protobuf v1.28.0
)

// remove the following code
replace github.com/aldelo/common => ../common
