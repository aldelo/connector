module github.com/aldelo/connector

go 1.15

require (
	github.com/aldelo/common v0.0.0-20200817181312-6d915fc35319
	github.com/golang/protobuf v1.4.1
	google.golang.org/grpc v1.31.0
	google.golang.org/protobuf v1.25.0
)

replace github.com/aldelo/common => ../common
