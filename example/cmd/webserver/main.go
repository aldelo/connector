package main

/*
 * Copyright 2020 Aldelo, LP
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
	util "github.com/aldelo/common"
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginbindtype"
	"github.com/aldelo/common/wrapper/gin/ginhttpmethod"
	"github.com/gin-gonic/gin"
	"log"
	"time"
)

func main() {
	// z := ginw.NewGinZapMiddleware("Example", true)
	g := ginw.NewServer("Example", 8080, false, true)

	g.SessionMiddleware = &ginw.SessionConfig{
		SecretKey: "Secret",
		SessionNames: []string{"MySession"},
	}

	g.CsrfMiddleware = &ginw.CsrfConfig{
		Secret: "Secrete",
	}
	
	g.HtmlTemplateRenderer = &ginw.GinTemplate{
		TemplateBaseDir: "./templates",
		Templates: []ginw.TemplateDefintion{
			{
				LayoutPath: "/layouts/*.html",
				PagePath: "/*.html",
			},
		},
	}

	g.Routes = map[string][]*ginw.RouteDefinition{
		"*": {
			{
				Routes: []*ginw.Route{
					{
						RelativePath: "/hello",
						Method: ginhttpmethod.GET,
						Binding: ginbindtype.UNKNOWN,
						Handler: func(c *gin.Context, bindingInput interface{}) {
							c.String(200, "What's up")
						},
					},
					{
						RelativePath: "/",
						Method: ginhttpmethod.GET,
						Binding: ginbindtype.UNKNOWN,
						Handler: func(c *gin.Context, bindingInput interface{}) {
							c.HTML(200, "index.html", gin.H{
								"title": "Html Test",
							})
						},
					},
					{
						RelativePath: "/product",
						Method: ginhttpmethod.GET,
						Binding: ginbindtype.UNKNOWN,
						Handler: func(c *gin.Context, bindingInput interface{}) {
							c.HTML(200, "product.html", gin.H{

							})
						},
					},
				},
				MaxLimitMiddleware: util.IntPtr(10),
				PerClientQpsMiddleware: &ginw.PerClientQps{
					Qps: 100,
					Burst: 100,
					TTL: time.Hour,
				},
				//GZipMiddleware: &ginw.GZipConfig{
				//	Compression: gingzipcompression.BestCompression,
				//},
			},
		},
	}

	if err := g.RunServer(); err != nil {
		log.Println("Error: " + err.Error())
	} else {
		log.Println("Run OK")
	}
}
