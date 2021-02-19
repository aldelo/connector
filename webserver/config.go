package webserver

/*
 * Copyright 2020-2021 Aldelo, LP
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
	data "github.com/aldelo/common/wrapper/viper"
)

type config struct {
	AppName string								`mapstructure:"-"`
	ConfigFileName string						`mapstructure:"-"`
	CustomConfigPath string						`mapstructure:"-"`

	_v *data.ViperConf							`mapstructure:"-"`

	WebServer webServerData						`mapstructure:"web_server"`
	Recovery recoveryData						`mapstructure:"recovery"`
	Logging loggingData							`mapstructure:"logging"`
	Session sessionData							`mapstructure:"session"`
	Csrf csrfData								`mapstructure:"csrf"`
	JwtAuth jwtAuthData							`mapstructure:"jwt_auth"`
	HtmlTemplates htmlTemplatesData				`mapstructure:"html_templates"`
	Routes []routeDefinition					`mapstructure:"routes"`
}

type webServerData struct {
	Name string									`mapstructure:"ws_name"`
	Debug bool									`mapstructure:"ws_debug"`
	Port uint									`mapstructure:"ws_port"`
	ServerPem string							`mapstructure:"ws_server_pem"`
	ServerKey string							`mapstructure:"ws_server_key"`
	GoogleRecaptchaSecret string				`mapstructure:"google_recaptcha_secret"`
	TraceUseXRay bool							`mapstructure:"ws_trace_use_xray"`
	LoggingUseSQS bool							`mapstructure:"ws_logging_use_sqs"`
	HostUseRoute53 bool							`mapstructure:"ws_host_use_route53"`
	Route53HostedZoneID string					`mapstructure:"ws_route53_hosted_zone_id"`
	Route53DomainSuffix string					`mapstructure:"ws_route53_domain_suffix"`
	Route53TTL uint								`mapstructure:"ws_route53_ttl"`
	RestTargetCACertFiles string				`mapstructure:"rest_target_ca_cert_files"`
}

type recoveryData struct {
	CustomRecovery bool							`mapstructure:"custom_recovery"`
}

type loggingData struct {
	CustomLogging bool							`mapstructure:"custom_logging"`
	CustomLoggingToConsole bool					`mapstructure:"custom_logging_to_console"`
	SQSLoggerQueueNamePrefix string				`mapstructure:"sqs_logger_queue_name_prefix"`
	SQSLoggerMessageRetentionSeconds uint		`mapstructure:"sqs_logger_message_retention_seconds"`
	SQSLoggerQueueURL string					`mapstructure:"sqs_logger_queue_url"`
	SQSLoggerQueueARN string					`mapstructure:"sqs_logger_queue_arn"`
}

type sessionData struct {
	SessionSecret string						`mapstructure:"session_secret"`
	SessionNames []string						`mapstructure:"session_names"`
	RedisHost string							`mapstructure:"redis_host"`
	RedisMaxIdleConnections uint				`mapstructure:"redis_max_idle_connections"`
}

type csrfData struct {
	CsrfSecret string							`mapstructure:"csrf_secret"`
}

type jwtAuthData struct {
	Realm string								`mapstructure:"jwt_realm"`
	IdentityKey string							`mapstructure:"jwt_identity_key"`
	SignSecret string							`mapstructure:"jwt_sign_secret"`
	SignAlgorithm string						`mapstructure:"jwt_sign_algorithm"`
	PrivateKey string							`mapstructure:"jwt_private_key"`
	PublicKey string							`mapstructure:"jwt_public_key"`
	LoginDataBinding string						`mapstructure:"jwt_login_data_binding"`
	TokenValidMinutes uint						`mapstructure:"jwt_token_valid_minutes"`
	RefreshValidMinutes uint					`mapstructure:"jwt_refresh_valid_minutes"`
	SendCookie bool								`mapstructure:"jwt_send_cookie"`
	SecureCookie bool							`mapstructure:"jwt_secure_cookie"`
	CookieHttpOnly bool							`mapstructure:"jwt_cookie_http_only"`
	CookieMaxAgeDays uint						`mapstructure:"jwt_cookie_max_age_days"`
	CookieDomain string							`mapstructure:"jwt_cookie_domain"`
	CookieName string							`mapstructure:"jwt_cookie_name"`
	CookieSameSite string						`mapstructure:"jwt_cookie_same_site"`
	LoginRoutePath string						`mapstructure:"jwt_login_route_path"`
	LogoutRoutePath string						`mapstructure:"jwt_logout_route_path"`
	RefreshTokenRoutePath string				`mapstructure:"jwt_refresh_token_route_path"`
	TokenLookup string							`mapstructure:"jwt_token_lookup"`
	TokenHeadName string						`mapstructure:"jwt_token_head_name"`
	DisableAbort bool							`mapstructure:"jwt_disable_abort"`
	SendAuthorization bool						`mapstructure:"jwt_send_authorization"`
}

type templateDefinition struct {
	LayoutPath string							`mapstructure:"layout_path"`
	PagePath string								`mapstructure:"page_path"`
}

type htmlTemplatesData struct {
	TemplateBaseDir string						`mapstructure:"template_base_dir"`
	TemplateDefinitions []templateDefinition	`mapstructure:"template_definitions"`
}

type routeDefinition struct {
	RouteGroupName string						`mapstructure:"route_group_name"`
	JwtAuthSecured bool							`mapstructure:"jwt_auth_secured"`
	MaxConcurrentRequestLimit uint				`mapstructure:"max_concurrent_request_limit"`
	PerClientIpQps uint							`mapstructure:"per_client_ip_qps"`
	PerClientIpBurst uint						`mapstructure:"per_client_ip_burst"`
	PerClientIpTtlMinutes uint					`mapstructure:"per_client_ip_ttl_minutes"`
	GzipCompressionType string					`mapstructure:"gzip_compression_type"`
	GzipExcludeExtensions []string				`mapstructure:"gzip_exclude_extensions"`
	GzipExcludePaths []string					`mapstructure:"gzip_exclude_paths"`
	GzipExcludePathsRegex []string				`mapstructure:"gzip_exclude_paths_regex"`
	CorsAllowAllOrigins bool					`mapstructure:"cors_allow_all_origins"`
	CorsAllowOrigins []string					`mapstructure:"cors_allow_origins"`
	CorsAllowMethods []string					`mapstructure:"cors_allow_methods"`
	CorsAllowHeaders []string					`mapstructure:"cors_allow_headers"`
	CorsAllowCredentials bool					`mapstructure:"cors_allow_credentials"`
	CorsAllowWildCard bool						`mapstructure:"cors_allow_wild_card"`
	CorsAllowBrowserExtensions bool				`mapstructure:"cors_allow_browser_extensions"`
	CorsAllowWebSockets bool					`mapstructure:"cors_allow_web_sockets"`
	CorsAllowFiles bool							`mapstructure:"cors_allow_files"`
	CorsMaxAgeMinutes uint						`mapstructure:"cors_max_age_minutes"`
}

func (c *config) SetWebServerName(s string) {
	if c._v != nil {
		c._v.Set("web_server.ws_name", s)
		c.WebServer.Name = s
	}
}

func (c *config) SetWebServerDebug(b bool) {
	if c._v != nil {
		c._v.Set("web_server.ws_debug", b)
		c.WebServer.Debug = b
	}
}

func (c *config) SetWebServerPort(i uint) {
	if c._v != nil {
		c._v.Set("web_server.ws_port", i)
		c.WebServer.Port = i
	}
}

func (c *config) SetServerPem(s string) {
	if c._v != nil {
		c._v.Set("web_server.ws_server_pem", s)
		c.WebServer.ServerPem = s
	}
}

func (c *config) SetServerKey(s string) {
	if c._v != nil {
		c._v.Set("web_server.ws_server_key", s)
		c.WebServer.ServerKey = s
	}
}

func (c *config) SetGoogleRecaptchaSecret(s string) {
	if c._v != nil {
		c._v.Set("web_server.google_recaptcha_secret", s)
		c.WebServer.GoogleRecaptchaSecret = s
	}
}

func (c *config) SetTraceUseXRay(b bool) {
	if c._v != nil {
		c._v.Set("web_server.ws_trace_use_xray", b)
		c.WebServer.TraceUseXRay = b
	}
}

func (c *config) SetLoggingUseSQS(b bool) {
	if c._v != nil {
		c._v.Set("web_server.ws_logging_use_sqs", b)
		c.WebServer.LoggingUseSQS = b
	}
}

func (c *config) SetHostUseRoute53(b bool) {
	if c._v != nil {
		c._v.Set("web_server.ws_host_use_route53", b)
		c.WebServer.HostUseRoute53 = b
	}
}

func (c *config) SetRoute53HostedZoneID(s string) {
	if c._v != nil {
		c._v.Set("web_server.ws_route53_hosted_zone_id", s)
		c.WebServer.Route53HostedZoneID = s
	}
}

func (c *config) SetRoute53DomainSuffix(s string) {
	if c._v != nil {
		c._v.Set("web_server.ws_route53_domain_suffix", s)
		c.WebServer.Route53DomainSuffix = s
	}
}

func (c *config) SetRoute53TTL(i uint) {
	if c._v != nil {
		c._v.Set("web_server.ws_route53_hosted_zone_id", i)
		c.WebServer.Route53TTL = i
	}
}

func (c *config) SetRestTargetCACertFiles(s string) {
	if c._v != nil {
		c._v.Set("web_server.rest_target_ca_cert_files", s)
		c.WebServer.RestTargetCACertFiles = s
	}
}

func (c *config) SetCustomRecovery(b bool) {
	if c._v != nil {
		c._v.Set("recovery.custom_recovery", b)
		c.Recovery.CustomRecovery = b
	}
}

func (c *config) SetCustomLogging(b bool) {
	if c._v != nil {
		c._v.Set("logging.custom_logging", b)
		c.Logging.CustomLogging = b
	}
}

func (c *config) SetCustomLoggingToConsole(b bool) {
	if c._v != nil {
		c._v.Set("logging.custom_logging_to_console", b)
		c.Logging.CustomLoggingToConsole = b
	}
}

func (c *config) SetSQSLoggerQueueNamePrefix(s string) {
	if c._v != nil {
		c._v.Set("logging.sqs_logger_queue_name_prefix", s)
		c.Logging.SQSLoggerQueueNamePrefix = s
	}
}

func (c *config) SetSQSLoggerMessageRetentionSeconds(i uint) {
	if c._v != nil {
		c._v.Set("logging.sqs_logger_message_retention_seconds", i)
		c.Logging.SQSLoggerMessageRetentionSeconds = i
	}
}

func (c *config) SetSQSLoggerQueueURL(s string) {
	if c._v != nil {
		c._v.Set("logging.sqs_logger_queue_url", s)
		c.Logging.SQSLoggerQueueURL = s
	}
}

func (c *config) SetSQSLoggerQueueARN(s string) {
	if c._v != nil {
		c._v.Set("logging.sqs_logger_queue_arn", s)
		c.Logging.SQSLoggerQueueARN = s
	}
}

func (c *config) SetSessionSecret(s string) {
	if c._v != nil {
		c._v.Set("session.session_secret", s)
		c.Session.SessionSecret = s
	}
}

func (c *config) SetSessionNames(s ...string) {
	if c._v != nil {
		c._v.Set("session.session_names", s)
		c.Session.SessionNames = s
	}
}

func (c *config) SetRedisHost(s string) {
	if c._v != nil {
		c._v.Set("session.redis_host", s)
		c.Session.RedisHost = s
	}
}

func (c *config) SetRedisMaxIdleConnections(i uint) {
	if c._v != nil {
		c._v.Set("session.redis_max_idle_connections", i)
		c.Session.RedisMaxIdleConnections = i
	}
}

func (c *config) SetCsrfSecret(s string) {
	if c._v != nil {
		c._v.Set("csrf.csrf_secret", s)
		c.Csrf.CsrfSecret = s
	}
}

func (c *config) SetJwtRealm(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_realm", s)
		c.JwtAuth.Realm = s
	}
}

func (c *config) SetJwtIdentityKey(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_identity_key", s)
		c.JwtAuth.IdentityKey = s
	}
}

func (c *config) SetJwtSignSecret(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_sign_secret", s)
		c.JwtAuth.SignSecret = s
	}
}

func (c *config) SetJwtSignAlgorithm(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_sign_algorithm", s)
		c.JwtAuth.SignAlgorithm = s
	}
}

func (c *config) SetJwtPrivatekey(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_private_key", s)
		c.JwtAuth.PrivateKey = s
	}
}

func (c *config) SetJwtPublicKey(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_public_key", s)
		c.JwtAuth.PublicKey = s
	}
}

func (c *config) SetJwtLoginDataBinding(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_login_data_binding", s)
		c.JwtAuth.LoginDataBinding = s
	}
}

func (c *config) SetJwtTokenValidMinutes(i uint) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_token_valid_minutes", i)
		c.JwtAuth.TokenValidMinutes = i
	}
}

func (c *config) SetJwtRefreshValidMinutes(i uint) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_refresh_valid_minutes", i)
		c.JwtAuth.RefreshValidMinutes = i
	}
}

func (c *config) SetJwtSendCookie(b bool) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_send_cookie", b)
		c.JwtAuth.SendCookie = b
	}
}

func (c *config) SetJwtSecureCookie(b bool) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_secure_cookie", b)
		c.JwtAuth.SecureCookie = b
	}
}

func (c *config) SetJwtCookieHttpOnly(b bool) {
	if c._v !=  nil {
		c._v.Set("jwt_auth.jwt_cookie_http_only", b)
		c.JwtAuth.CookieHttpOnly = b
	}
}

func (c *config) SetJwtCookieMaxAgeDays(i uint) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_cookie_max_age_days", i)
		c.JwtAuth.CookieMaxAgeDays = i
	}
}

func (c *config) SetJwtCookieDomain(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_cookie_domain", s)
		c.JwtAuth.CookieDomain = s
	}
}

func (c *config) SetJwtCookieName(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_cookie_name", s)
		c.JwtAuth.CookieName = s
	}
}

func (c *config) SetJwtCookieSameSite(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_cookie_same_site", s)
		c.JwtAuth.CookieSameSite = s
	}
}

func (c *config) SetJwtLoginRoutePath(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_login_route_path", s)
		c.JwtAuth.LoginRoutePath = s
	}
}

func (c *config) SetJwtLogoutRoutePath(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_logout_route_path", s)
		c.JwtAuth.LogoutRoutePath = s
	}
}

func (c *config) SetJwtRefreshTokenRoutePath(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_refresh_token_route_path", s)
		c.JwtAuth.RefreshTokenRoutePath = s
	}
}

func (c *config) SetJwtTokenLookup(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_token_lookup", s)
		c.JwtAuth.TokenLookup = s
	}
}

func (c *config) SetJwtTokenHeadName(s string) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_token_head_name", s)
		c.JwtAuth.TokenHeadName = s
	}
}

func (c *config) SetJwtDisableAbort(b bool) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_disable_abort", b)
		c.JwtAuth.DisableAbort = b
	}
}

func (c *config) SetJwtSendAuthorization(b bool) {
	if c._v != nil {
		c._v.Set("jwt_auth.jwt_send_authorization", b)
		c.JwtAuth.SendAuthorization = b
	}
}

func (c *config) SetTemplateBaseDir(s string) {
	if c._v != nil {
		c._v.Set("html_templates.template_base_dir", s)
		c.HtmlTemplates.TemplateBaseDir = s
	}
}

func (c *config) SetTemplateDefinitions(d ...templateDefinition) {
	if c._v != nil {
		c._v.Set("html_templates.template_definitions", d)
		c.HtmlTemplates.TemplateDefinitions = d
	}
}

func (c *config) SetRoutes(r ...routeDefinition) {
	if c._v != nil {
		c._v.Set("routes", r)
		c.Routes = r
	}
}

// Read will load config settings from disk
func (c *config) Read() error {
	c._v = nil
	c.WebServer = webServerData{}
	c.Recovery = recoveryData{}
	c.Logging = loggingData{}
	c.Session = sessionData{}
	c.Csrf = csrfData{}
	c.JwtAuth = jwtAuthData{}
	c.HtmlTemplates = htmlTemplatesData{}
	c.Routes = []routeDefinition{}

	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	if util.LenTrim(c.ConfigFileName) == 0 {
		c.ConfigFileName = "webserver"
	}

	c._v = &data.ViperConf{
		AppName: c.AppName,
		ConfigName: c.ConfigFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML: true,
		UseAutomaticEnvVar: false,
	}

	c._v.Default("web_server.ws_name", "webserver").Default(					// required, web server descriptive name
	"web_server.ws_debug", false).Default(									// optional, true or false, indicates if web server running in debug mode, default = false
	"web_server.ws_port", 8080).Default(										// required, web server tcp port, default = 8080
	"web_server.ws_server_pem", "").Default(									// optional, web server tls server certificate pem file path, default = blank
	"web_server.ws_server_key", "").Default(									// optional, web server tls server certificate key file path, default = blank
	"web_server.google_recaptcha_secret", "").Default(						// optional, google recaptcha v2 secret assigned by google services
	"web_server.ws_trace_use_xray", false).Default(							// optional, enable xray tracing, default false
	"web_server.ws_logging_use_sqs", false).Default(							// optional, enable cloud logging, default false
	"web_server.ws_host_use_route53", false).Default(							// optional, enable route53 dns for host url, where host ip auto maintained by route53 api integration (if host using tls, use dns instead of ip for webhook callback)
	"web_server.ws_route53_hosted_zone_id", "").Default(						// optional, if using route53 for host url, configure route53 hosted zone id (pre-created in aws route53)
	"web_server.ws_route53_domain_suffix", "").Default(						// optional, if using route53 for host url, configure route53 domain suffix such as example.com (must match domain pre-configured in aws route53)
	"web_server.ws_route53_ttl", 60).Default(									// optional, if using route53 for host url, configure route53 ttl seconds, default = 60
	"web_server.rest_target_ca_cert_files", "")								// optional, self-signed ca certs file path, separated by comma if multiple ca pems,
																						// 			 used by rest get/post/put/delete against target server hosts that use self-signed certs for tls,
																						//			 to avoid bad certificate error during tls handshake

	c._v.Default("recovery.custom_recovery", false)							// optional, true or false, indicates if web server uses custom recovery logic, default = false

	c._v.Default("logging.custom_logging", false).Default(					// optional, true or false, indicates if web server uses custom logging logic, default = false
	"logging.custom_logging_to_console", false).Default(						// optional, true or false, indicates if custom logging is used, if the logging is to console rather than disk, default = false
	"logging.sqs_logger_queue_name_prefix", "").Default(						// sqs queue name prefix used for service logging data queuing, if name is not provided, default = service-logger-data-
	"logging.sqs_logger_message_retention_seconds", 14400).Default(			// sqs service logger queue's messages retention seconds, default = 14,400 seconds (4 Hours)
	"logging.sqs_logger_queue_url", "").Default(								// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service logger data queue used by this service (auto set by service upon creation)
	"logging.sqs_logger_queue_arn", "")										// sqs queue's queueUrl and queueArn as generated by aws sqs for the corresponding service logger data queue used by this service (auto set by service upon creation)

	c._v.Default("session.session_secret", "").Default(						// optional, session management secret key, default = blank (blank = session not used)
	"session.session_names", []string{}).Default(									// optional, list of session names, default = blank list
	"session.redis_host", "").Default(										// optional, redis host and port used for session, default = blank
	"session.redis_max_idle_connections", 10)									// optional, session redis host max idle connections, default = 10

	c._v.Default("csrf.csrf_secret", "")										// optional, csrf secret key, default = blank (blank = csrf not used)

	c._v.Default("jwt_auth.jwt_realm", "").Default(							// optional, jwt auth realm name, default = blank (blank = jwt auth not used)
	"jwt_auth.jwt_identity_key", "id").Default(								// optional, jwt auth identity key name, default = id
	"jwt_auth.jwt_sign_secret", "").Default(									// optional, jwt auth signing key secret, default = blank
	"jwt_auth.jwt_sign_algorithm", "H256").Default(							// optional, jwt auth signing algorithm, values: (HS256, HS384, HS512, RS256, RS384 or RS512) default = H256
	"jwt_auth.jwt_private_key", "").Default(									// optional, jwt auth aes private key file path, default = blank
	"jwt_auth.jwt_public_key", "").Default(									// optional, jwt auth aes public key file path, default = blank
	"jwt_auth.jwt_login_data_binding", "json").Default(						// optional, jwt auth login authentication data binding type, values: (json, xml, yaml, proto, header, query, uri, unknown) default = json
	"jwt_auth.jwt_token_valid_minutes", 15).Default(							// optional, jwt auth token valid minutes, default = 15
	"jwt_auth.jwt_refresh_valid_minutes", 1440).Default(						// optional, jwt auth token refresh valid minutes, default = 1440 (24 hours)
	"jwt_auth.jwt_send_cookie", false).Default(								// optional, jwt auth send cookie, default = false
	"jwt_auth.jwt_secure_cookie", true).Default(								// optional, jwt auth use secured cookie, default = true
	"jwt_auth.jwt_cookie_http_only", true).Default( 							// optional, jwt auth cookie server side http only, prevents edit at client, default = true
	"jwt_auth.jwt_cookie_max_age_days", 14).Default(							// optional, jwt auth cookie maximum age in days, default = 14 days
	"jwt_auth.jwt_cookie_domain", "").Default(								// optional, jwt auth cookie domain name, default = blank
	"jwt_auth.jwt_cookie_name", "").Default(									// optional, jwt auth cookie name, default = blank
	"jwt_auth.jwt_cookie_same_site", "default").Default(						// optional, jwt auth cookie same site type, values: (default, lax, strict, none) default = default
	"jwt_auth.jwt_login_route_path", "/login").Default(						// optional, jwt auth login route path, default = /login
	"jwt_auth.jwt_logout_route_path", "/logout").Default(						// optional, jwt auth logout route path, default = /logout
	"jwt_auth.jwt_refresh_token_route_path", "/refreshtoken").Default(		// optional, jwt auth refresh token route path, default = /refreshtoken
	"jwt_auth.jwt_token_lookup", "header:Authorization")						// optional, token lookup is a string in the form of <source>:<name> that is used to extract token from the request,
																						//    - default = "header:Authorization",
																						//    - other values:
																						//         - "header:<name>", "query:<name>", "cookie:<name>", "param:<name>"
																						//    - other examples:
																						//         - "header: Authorization, query: token, cookie: jwt"
																						//         - "query:token"
																						//         - "cookie:token
	c._v.Default("jwt_auth.jwt_token_head_name", "Bearer").Default(			// optional, token head name is a string in the header, default = Bearer
	"jwt_auth.jwt_disable_abort", false).Default(								// optional, true or false, disables abort() of context, default = false
	"jwt_auth.jwt_send_authorization", false)									// optional, true or false, allow return authorization header for every request, default = false

	c._v.Default("html_templates.template_base_dir", "").Default(				// optional, html templates base directory, default = blank
	"html_templates.template_definitions", []templateDefinition{})					// optional, html templates definitions list, default = empty list

	c._v.Default("routes", []routeDefinition{})										// optional, web server routes level middleware configurations, default = empty list
																						// - route_group_name: base = web server root folder; other values = web server route group name
																						// - max_concurrent_request_limit: max hit rate limit, 0 = turn off
																						// - per_client_ip_qps: per client ip qps rate limit, 0 = turn off
																						// - gzip_compression_type: gzip compression services, values: (default, best-speed, best-compression) blank = turn off
																						// - cors_allow_all_origins: cors protection services, true = turn off
																						// - cors_allow_origins: list of cors origins allowed
																						// - cors_allow_methods: list of cors methods allowed
																						// - cors_allow_headers: list of cors headers allowed

	if ok, err := c._v.Init(); err != nil {
		return err
	} else {
		if !ok {
			if e := c._v.Save(); e != nil {
				return fmt.Errorf("Create Config File Failed: " + e.Error())
			}
		} else {
			c._v.WatchConfig()
		}
	}

	if err := c._v.Unmarshal(c); err != nil {
		return err
	}

	return nil
}

// Save persists config settings to disk
func (c *config) Save() error {
	if c._v != nil {
		return c._v.Save()
	} else {
		return nil
	}
}
