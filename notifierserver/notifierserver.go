package notifierserver

import (
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginhttpmethod"
	"github.com/aldelo/connector/notifierserver/impl"
	pb "github.com/aldelo/connector/notifierserver/proto"
	"github.com/aldelo/connector/service"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

var notifierServer *impl.NotifierImpl

func NewNotifierServer(appName string, configFileNameGrpcServer string, configFileNameWebServer string, customConfigPath string) *service.Service {
	notifierServer = new(impl.NotifierImpl)

	svr := service.NewService(appName, configFileNameGrpcServer, customConfigPath, func(grpcServer *grpc.Server) {
			pb.RegisterNotifierServer(grpcServer, notifierServer)
		})

	svr.WebServerConfig = &service.WebServerConfig{
		AppName: appName,
		ConfigFileName: configFileNameWebServer,
		CustomConfigPath: "",
		WebServerRoutes: map[string]*ginw.RouteDefinition{
			"base": {
				Routes: []*ginw.Route{
					{
						RelativePath: "/",
						Method: ginhttpmethod.GET,
						Handler: func(c *gin.Context, bindingInputPtr interface{}) {
							c.String(200, "OK")
						},
					},
				},
				CorsMiddleware: &cors.Config{},
			},
		},
	}

	return svr
}
