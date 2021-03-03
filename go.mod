module github.com/aldelo/connector

go 1.15

require (
	github.com/aldelo/common v1.1.6
	github.com/aws/aws-sdk-go v1.36.29
	github.com/gin-contrib/cors v1.3.1
	github.com/gin-gonic/gin v1.6.3
	github.com/golang/protobuf v1.4.3
	google.golang.org/genproto v0.0.0-20201119123407-9b1e624d6bc4
	google.golang.org/grpc v1.35.0
	google.golang.org/protobuf v1.25.0
)

// remove the following code
replace github.com/aldelo/common => ../common
