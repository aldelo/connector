package main

/*
 * Copyright 2020-2023 Aldelo, LP
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import (
	"fmt"
	util "github.com/aldelo/common"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginbindtype"
	"github.com/aldelo/common/wrapper/gin/ginhttpmethod"
	ws "github.com/aldelo/connector/webserver"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"log"
	"time"
)

type SimpleData struct {
	Line1 string `form:"line1" json:"line1"` // binding:"required"
	Line2 string `form:"line2" json:"line2"` // binding:"required"
}

func main() {
	g := ws.NewWebServer("Example", "server", "")

	g.LoginRequestDataPtr = &ginw.UserLogin{}

	g.AuthenticateHandler = func(loginRequestDataPtr interface{}) (loggedInCredentialPtr interface{}) {
		if lg, ok := loginRequestDataPtr.(*ginw.UserLogin); !ok {
			return nil
		} else {
			if lg.Username == "Happy" && lg.Password == "Puppy" {
				return &ginw.UserInfo{
					UserName:  "Happy",
					FirstName: "Super Happy",
					LastName:  "Very Happy",
				}
			} else {
				return nil
			}
		}
	}

	g.AddClaimsHandler = func(loggedInCredentialPtr interface{}) (identityKeyValue string, claims map[string]interface{}) {
		return "Happy", map[string]interface{}{
			"productcode": "xyz",
			"productage":  "old",
		}
	}

	g.AuthorizerHandler = func(loggedInCredentialPtr interface{}, c *gin.Context) bool {
		return true
	}

	g.Routes = map[string]*ginw.RouteDefinition{
		"*": {
			Routes: []*ginw.Route{
				{
					RelativePath: "/hello",
					Method:       ginhttpmethod.GET,
					Binding:      ginbindtype.UNKNOWN,
					Handler: func(c *gin.Context, bindingInput interface{}) {
						c.String(200, "What's up")
					},
				},
				{
					RelativePath:    "/",
					Method:          ginhttpmethod.GET,
					Binding:         ginbindtype.UNKNOWN,
					BindingInputPtr: &SimpleData{},
					Handler: func(c *gin.Context, bindingInput interface{}) {
						o := bindingInput.(*SimpleData)

						c.HTML(200, "index.html", gin.H{
							"title": o.Line1 + " // " + o.Line2,
						})
					},
				},
				{
					RelativePath: "/product",
					Method:       ginhttpmethod.GET,
					Binding:      ginbindtype.UNKNOWN,
					Handler: func(c *gin.Context, bindingInput interface{}) {
						c.HTML(200, "product.html", gin.H{})
					},
				},
				{
					RelativePath: "/xyz",
					Method:       ginhttpmethod.GET,
					Binding:      ginbindtype.UNKNOWN,
					Handler: func(c *gin.Context, bindingInput interface{}) {
						c.HTML(200, "productx.html", gin.H{})
					},
				},
			},
			CorsMiddleware:     &cors.Config{},
			MaxLimitMiddleware: util.IntPtr(10),
			PerClientQpsMiddleware: &ginw.PerClientQps{
				Qps:   100,
				Burst: 100,
				TTL:   time.Hour,
			},
			//GZipMiddleware: &ginw.GZipConfig{
			//	Compression: gingzipcompression.BestCompression,
			//},
			UseAuthMiddleware: false,
		},
		"auth": {
			Routes: []*ginw.Route{
				{
					RelativePath: "/secured",
					Method:       ginhttpmethod.GET,
					Binding:      ginbindtype.UNKNOWN,
					Handler: func(c *gin.Context, bindingInput interface{}) {
						claims := g.ExtractJwtClaims(c)
						str := ""

						for k, v := range claims {
							str += fmt.Sprintf("%s:%v; ", k, v)
						}

						c.String(200, "Secured Hello: "+str)
					},
				},
			},
			UseAuthMiddleware: true,
		},
	}

	if err := g.Serve(); err != nil {
		log.Println("Error: " + err.Error())
	} else {
		log.Println("Run OK")
	}
}
