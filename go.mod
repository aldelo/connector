module github.com/aldelo/connector

go 1.15

require (
	github.com/aldelo/common v1.2.4
	github.com/aws/aws-sdk-go v1.42.19
	github.com/gin-contrib/cors v1.3.1
	github.com/gin-gonic/gin v1.7.7
	github.com/golang/protobuf v1.5.2
	google.golang.org/genproto v0.0.0-20211203200212-54befc351ae9
	google.golang.org/grpc v1.42.0
	google.golang.org/protobuf v1.27.1
)

// remove the following code
//replace github.com/aldelo/common => ../common
