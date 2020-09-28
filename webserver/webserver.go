package webserver

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
	ginw "github.com/aldelo/common/wrapper/gin"
	"github.com/aldelo/common/wrapper/gin/ginbindtype"
	"github.com/aldelo/common/wrapper/gin/gingzipcompression"
	"github.com/aldelo/common/wrapper/gin/ginjwtsignalgorithm"
	// "github.com/aldelo/common/wrapper/sns"
	// "github.com/aldelo/common/wrapper/sqs"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"net/http"

	"fmt"
	util "github.com/aldelo/common"
	"log"
	"strings"
	// "sync"
	"time"
)

// WebServer defines gin http web server,
// loads config from yaml config file,
// contains fields to config custom handlers
type WebServer struct {
	// csrf handler definitions
	CsrfErrorHandler func(c *gin.Context)
	CsrfTokenGetterHandler func(c *gin.Context) string

	// http error handler definitions
	HttpStatusErrorHandler func(status int, trace string, c *gin.Context)

	// jwt auth handler definitions
	LoginRequestDataPtr interface{}

	AuthenticateHandler func(loginRequestDataPtr interface{}) (loggedInCredentialPtr interface{})
	AddClaimsHandler func(loggedInCredentialPtr interface{}) (identityKeyValue string, claims map[string]interface{})
	GetIdentityHandler func(claims map[string]interface{}) interface{}
	AuthorizerHandler func(loggedInCredentialPtr interface{}, c *gin.Context) bool

	LoginResponseHandler func(c *gin.Context, statusCode int, token string, expires time.Time)
	LogoutResponseHandler func(c *gin.Context, statusCode int)
	RefreshTokenResponseHandler func(c *gin.Context, statusCode int, token string, expires time.Time)

	UnauthorizedHandler func(c *gin.Context, code int, message string)
	NoRouteHandler func(claims map[string]interface{}, c *gin.Context)
	MiddlewareErrorEvaluator func(e error, c *gin.Context) string
	TimeHandler func() time.Time

	// route definitions
	Routes map[string]*ginw.RouteDefinition

	// -----------------------------------------------------------------------------------------------------------------

	// read or persist web server config settings
	_config *config

	// sqs sns var
	// _sqs *sqs.SQS
	// _sns *sns.SNS

	// instantiated web server objects
	_ginwebserver *ginw.Gin

	// web server mutex locking
	// _mu sync.RWMutex
}

// NewWebServer creates a prepared web server for further setup and use
func NewWebServer(appName string, configFileName string, customConfigPath string) *WebServer {
	// load config
	if c, e := readConfig(appName, configFileName, customConfigPath); e != nil {
		log.Println("Create Web Server Failed: " + e.Error())
		return nil
	} else {
		var gz *ginw.GinZap
		if c.Logging.CustomLogging {
			gz = ginw.NewGinZapMiddleware(c.WebServer.Name, c.Logging.CustomLoggingToConsole)
		}

		var ge func(status int, trace string, c *gin.Context)
		if c.Recovery.CustomRecovery {
			ge = func(status int, trace string, c *gin.Context) {
				// custom recovery output
				c.String(status, trace)
			}
		}

		return &WebServer{
			_config: c,
			_ginwebserver: ginw.NewServer(c.WebServer.Name, c.WebServer.Port, !c.WebServer.Debug, c.Recovery.CustomRecovery, ge, gz),
		}
	}
}

// SetRouterGroupCustomMiddleware sets additional custom gin middleware (RouterFunc) to engine or router groups,
//
// routerGroupName = blank, to set routerFunc to engine
// routerGroupName = not blank, to set routerFunc to the named router group if found
//
// return true if set; false if not set
func (w *WebServer) SetRouteGroupCustomMiddleware(routeGroupName string, routerFunc []gin.HandlerFunc) bool {
	if w._ginwebserver == nil {
		return false
	}

	if w._ginwebserver.Routes == nil {
		return false
	}

	if len(routerFunc) == 0 {
		return false
	}

	k := strings.ToLower(util.Trim(routeGroupName))

	if k == "base" || k == "" {
		k = "*"
	}

	if rg, ok := w._ginwebserver.Routes[k]; ok {
		if rg != nil {
			rg.CustomMiddleware = routerFunc
			return true
		} else {
			return false
		}
	} else {
		return false
	}
}

// ExtractJwtClaims returns map from gin context extract
func (w *WebServer) ExtractJwtClaims(c *gin.Context) map[string]interface{} {
	if w._ginwebserver != nil {
		return w._ginwebserver.ExtractJwtClaims(c)
	} else {
		return nil
	}
}

// Port returns the web server port configured
func (w *WebServer) Port() uint {
	if w._config != nil {
		return w._config.WebServer.Port
	} else {
		return 0
	}
}

// Serve will setup and start the web server
func (w *WebServer) Serve() error {
	if w._config == nil {
		return fmt.Errorf("Config Object Not Initialized, Use NewWebServer(...) First")
	}

	if w._ginwebserver == nil {
		return fmt.Errorf("Web Server Not Initialized, Use NewWebServer(...) First")
	}

	if err := w.setupWebServer(); err != nil {
		return fmt.Errorf("Web Server Setup Failed: %s", err)
	}

	if err := w._ginwebserver.RunServer(); err != nil {
		return fmt.Errorf("Start Web Server Failed: %s", err)
	}

	return nil
}

// readConfig will read in config data
func readConfig(appName string, configFileName string, customConfigPath string) (c *config, err error) {
	c = &config{
		AppName: appName,
		ConfigFileName: configFileName,
		CustomConfigPath: customConfigPath,
	}

	if err := c.Read(); err != nil {
		return nil, fmt.Errorf("Read Config Failed: %s", err.Error())
	}

	if c.WebServer.Port > 65535 {
		return nil, fmt.Errorf("Configured Instance Port Not Valid: %s", "Tcp Port Max is 65535")
	}

	return c, nil
}

// setupWebServer configures the given web server with settings defined from web server yaml config file
func (w *WebServer) setupWebServer() error {
	if w._config == nil {
		return fmt.Errorf("Setup Web Server Failed: %s", "Config is Required")
	}

	if w._ginwebserver == nil {
		return fmt.Errorf("Setup Web Server Failed: %s", "Web Server Not Yet Created")
	}

	if util.LenTrim(w._ginwebserver.Name) == 0 {
		return fmt.Errorf("Setup Web Server Failed: %s", "Web Server Name is Required")
	}

	if w._ginwebserver.Port > 65535 {
		return fmt.Errorf("Setup Web Server Failed: %s", "Web Server Port is Required")
	}

	// set web server tls
	if util.LenTrim(w._config.WebServer.ServerPem) > 0 && util.LenTrim(w._config.WebServer.ServerKey) > 0 {
		w._ginwebserver.TlsCertPemFile = w._config.WebServer.ServerPem
		w._ginwebserver.TlsCertKeyFile = w._config.WebServer.ServerKey
	}

	// set jwt auth
	if util.LenTrim(w._config.JwtAuth.Realm) > 0 {
		signAlg := ginjwtsignalgorithm.HS256

		// HS256, HS384, HS512, RS256, RS384 or RS512
		switch strings.ToLower(w._config.JwtAuth.SignAlgorithm) {
		case "hs256":
			signAlg = ginjwtsignalgorithm.HS256
		case "hs384":
			signAlg = ginjwtsignalgorithm.HS384
		case "hs512":
			signAlg = ginjwtsignalgorithm.HS512
		case "rs256":
			signAlg = ginjwtsignalgorithm.RS256
		case "rs384":
			signAlg = ginjwtsignalgorithm.RS384
		case "rs512":
			signAlg = ginjwtsignalgorithm.RS512
		}

		bindType := ginbindtype.BindJson

		// json, xml, yaml, proto, header, query, uri, unknown
		switch strings.ToLower(w._config.JwtAuth.LoginDataBinding) {
		case "json":
			bindType = ginbindtype.BindJson
		case "xml":
			bindType = ginbindtype.BindXml
		case "yaml":
			bindType = ginbindtype.BindYaml
		case "proto":
			bindType = ginbindtype.BindProtoBuf
		case "header":
			bindType = ginbindtype.BindHeader
		case "query":
			bindType = ginbindtype.BindQuery
		case "uri":
			bindType = ginbindtype.BindUri
		case "unknown":
			bindType = ginbindtype.UNKNOWN
		}

		sameSite := http.SameSiteDefaultMode

		// default, lax, strict, none
		switch strings.ToLower(w._config.JwtAuth.CookieSameSite) {
		case "default":
			sameSite = http.SameSiteDefaultMode
		case "lax":
			sameSite = http.SameSiteLaxMode
		case "strict":
			sameSite = http.SameSiteStrictMode
		case "none":
			sameSite = http.SameSiteNoneMode
		}

		if !w._ginwebserver.NewAuthMiddleware(w._config.JwtAuth.Realm, w._config.JwtAuth.IdentityKey, w._config.JwtAuth.SignSecret, bindType,
			  							     func(j *ginw.GinJwt) {
										  		// perform jwt auth setup
			  							     	j.PrivateKeyFile = w._config.JwtAuth.PrivateKey
			  							     	j.PublicKeyFile = w._config.JwtAuth.PublicKey
			  							     	j.SigningAlgorithm = signAlg
			  							     	j.TokenValidDuration = time.Duration(w._config.JwtAuth.TokenValidMinutes) * time.Minute
			  							     	j.TokenMaxRefreshDuration = time.Duration(w._config.JwtAuth.RefreshValidMinutes) * time.Minute
			  							     	j.LoginRoutePath = w._config.JwtAuth.LoginRoutePath
			  							     	j.LogoutRoutePath = w._config.JwtAuth.LogoutRoutePath
			  							     	j.RefreshTokenRoutePath = w._config.JwtAuth.RefreshTokenRoutePath
			  							     	j.TokenLookup = w._config.JwtAuth.TokenLookup
											 	j.TokenHeadName = w._config.JwtAuth.TokenHeadName
											 	j.DisableAbort = w._config.JwtAuth.DisableAbort
											 	j.SendAuthorization = w._config.JwtAuth.SendAuthorization

			  							     	j.LoginRequestDataPtr = w.LoginRequestDataPtr

			  							     	if w._config.JwtAuth.SendCookie {
			  							     		j.SendCookie = w._config.JwtAuth.SendCookie
													j.SecureCookie = &w._config.JwtAuth.SecureCookie
													j.CookieHTTPOnly = &w._config.JwtAuth.CookieHttpOnly
			  							     		j.CookieSameSite = &sameSite
													j.CookieDomain = w._config.JwtAuth.CookieDomain
			  							     		j.CookieName = w._config.JwtAuth.CookieName
			  							     		j.CookieMaxAge = time.Duration(w._config.JwtAuth.CookieMaxAgeDays) * 24 * time.Hour
												}

												j.AuthenticateHandler = w.AuthenticateHandler
												j.AddClaimsHandler = w.AddClaimsHandler
											 	j.GetIdentityHandler = w.GetIdentityHandler
											 	j.AuthorizerHandler = w.AuthorizerHandler

											    j.LoginResponseHandler = w.LoginResponseHandler
											    j.LogoutResponseHandler = w.LogoutResponseHandler
											    j.RefreshTokenResponseHandler = w.RefreshTokenResponseHandler

											 	j.UnauthorizedHandler = w.UnauthorizedHandler
											 	j.NoRouteHandler = w.NoRouteHandler
											 	j.MiddlewareErrorEvaluator = w.MiddlewareErrorEvaluator
											 	j.TimeHandler = w.TimeHandler
										     }) {
			// setup jwt auth middleware failed
			return fmt.Errorf("Setup Web Server Failed: %s", "Jwt Auth Middleware Failed to Config")
		}
	}

	// set session
	if util.LenTrim(w._config.Session.SessionSecret) > 0 {
		w._ginwebserver.SessionMiddleware = &ginw.SessionConfig{
			SecretKey: w._config.Session.SessionSecret,
			SessionNames: w._config.Session.SessionNames,
			RedisHostAndPort: w._config.Session.RedisHost,
			RedisMaxIdleConnections: int(w._config.Session.RedisMaxIdleConnections),
		}
	}

	// set csrf
	if util.LenTrim(w._config.Csrf.CsrfSecret) > 0 {
		w._ginwebserver.CsrfMiddleware = &ginw.CsrfConfig{
			Secret: w._config.Csrf.CsrfSecret,
			ErrorFunc: func(c *gin.Context) {
				if w.CsrfErrorHandler != nil {
					w.CsrfErrorHandler(c)
				} else {
					c.String(500, "Csrf Error")
				}
			},
		}

		if w.CsrfTokenGetterHandler != nil {
			w._ginwebserver.CsrfMiddleware.TokenGetter = w.CsrfTokenGetterHandler
		}
	}

	// set html templates renderer
	if util.LenTrim(w._config.HtmlTemplates.TemplateBaseDir) > 0 {
		var tmpl []ginw.TemplateDefintion

		if len(w._config.HtmlTemplates.TemplateDefinitions) > 0 {
			for _, v := range w._config.HtmlTemplates.TemplateDefinitions {
				tmpl = append(tmpl, ginw.TemplateDefintion{
					LayoutPath: v.LayoutPath,
					PagePath: v.PagePath,
				})
			}
		}

		w._ginwebserver.HtmlTemplateRenderer = &ginw.GinTemplate{
			TemplateBaseDir: w._config.HtmlTemplates.TemplateBaseDir,
			Templates: tmpl,
		}
	}

	// set http status error handler
	if w.HttpStatusErrorHandler != nil {
		w._ginwebserver.HttpStatusErrorHandler = w.HttpStatusErrorHandler
	}

	// set web server routes
	if w.Routes	!= nil && len(w.Routes) > 0 {
		// merge yaml configured routes definition (middleware setup)
		// into appropriate base or route groups
		if len(w._config.Routes) > 0 {
			for _, d := range w._config.Routes {
				key := strings.ToLower(d.RouteGroupName)

				if rd, ok := w.Routes[key]; ok {
					rd.UseAuthMiddleware = d.JwtAuthSecured

					if !d.CorsAllowAllOrigins {
						rd.CorsMiddleware = &cors.Config{
							AllowAllOrigins: d.CorsAllowAllOrigins,
							AllowOrigins: d.CorsAllowOrigins,
							AllowMethods: d.CorsAllowMethods,
							AllowHeaders: d.CorsAllowHeaders,
							AllowCredentials: d.CorsAllowCredentials,
							MaxAge: time.Duration(d.CorsMaxAgeMinutes) * time.Minute,
							AllowWildcard: d.CorsAllowWildCard,
							AllowBrowserExtensions: d.CorsAllowBrowserExtensions,
							AllowWebSockets: d.CorsAllowWebSockets,
							AllowFiles: d.CorsAllowFiles,
						}
					}

					// default, best-speed, best-compression
					gz := gingzipcompression.UNKNOWN
					switch strings.ToLower(d.GzipCompressionType) {
					case "default":
						gz = gingzipcompression.Default
					case "best-speed":
						gz = gingzipcompression.BestSpeed
					case "best-compression":
						gz = gingzipcompression.BestCompression
					}

					if gz.Valid() && gz != gingzipcompression.UNKNOWN {
						rd.GZipMiddleware = &ginw.GZipConfig{
							Compression: gz,
							ExcludedExtensions: d.GzipExcludeExtensions,
							ExcludedPaths: d.GzipExcludePaths,
							ExcludedPathsRegex: d.GzipExcludePathsRegex,
						}
					}

					if d.PerClientIpQps > 0 {
						rd.PerClientQpsMiddleware = &ginw.PerClientQps{
							Qps: int(d.PerClientIpQps),
							Burst: int(d.PerClientIpBurst),
							TTL: time.Duration(d.PerClientIpTtlMinutes) * time.Minute,
						}
					}

					if d.MaxConcurrentRequestLimit > 0 {
						rd.MaxLimitMiddleware = util.IntPtr(int(d.MaxConcurrentRequestLimit))
					}
				}
			}
		}

		// now ready to set configured routes into gin web server
		w._ginwebserver.Routes = w.Routes
	}

	// success
	return nil
}







