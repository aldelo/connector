package webserver

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
	"os"
	"strings"

	util "github.com/aldelo/common"
	data "github.com/aldelo/common/wrapper/viper"
)

type config struct {
	AppName          string `mapstructure:"-"`
	ConfigFileName   string `mapstructure:"-"`
	CustomConfigPath string `mapstructure:"-"`

	_v *data.ViperConf `mapstructure:"-"`

	WebServer     webServerData     `mapstructure:"web_server"`
	Recovery      recoveryData      `mapstructure:"recovery"`
	Logging       loggingData       `mapstructure:"logging"`
	Session       sessionData       `mapstructure:"session"`
	Csrf          csrfData          `mapstructure:"csrf"`
	JwtAuth       jwtAuthData       `mapstructure:"jwt_auth"`
	HtmlTemplates htmlTemplatesData `mapstructure:"html_templates"`
	Routes        []routeDefinition `mapstructure:"routes"`
}

type webServerData struct {
	Name                  string `mapstructure:"ws_name"`
	Debug                 bool   `mapstructure:"ws_debug"`
	Port                  uint   `mapstructure:"ws_port"`
	ServerPem             string `mapstructure:"ws_server_pem"`
	ServerKey             string `mapstructure:"ws_server_key"`
	GoogleRecaptchaSecret string `mapstructure:"google_recaptcha_secret"`
	TraceUseXRay          bool   `mapstructure:"ws_trace_use_xray"`
	LoggingUseSQS         bool   `mapstructure:"ws_logging_use_sqs"`
	HostUseRoute53        bool   `mapstructure:"ws_host_use_route53"`
	Route53HostedZoneID   string `mapstructure:"ws_route53_hosted_zone_id"`
	Route53DomainSuffix   string `mapstructure:"ws_route53_domain_suffix"`
	Route53TTL            uint   `mapstructure:"ws_route53_ttl"`
	RestTargetCACertFiles string `mapstructure:"rest_target_ca_cert_files"`
}

type recoveryData struct {
	CustomRecovery bool `mapstructure:"custom_recovery"`
}

type loggingData struct {
	CustomLogging                    bool   `mapstructure:"custom_logging"`
	CustomLoggingToConsole           bool   `mapstructure:"custom_logging_to_console"`
	SQSLoggerQueueNamePrefix         string `mapstructure:"sqs_logger_queue_name_prefix"`
	SQSLoggerMessageRetentionSeconds uint   `mapstructure:"sqs_logger_message_retention_seconds"`
	SQSLoggerQueueURL                string `mapstructure:"sqs_logger_queue_url"`
	SQSLoggerQueueARN                string `mapstructure:"sqs_logger_queue_arn"`
}

type sessionData struct {
	SessionSecret           string   `mapstructure:"session_secret"`
	SessionNames            []string `mapstructure:"session_names"`
	RedisHost               string   `mapstructure:"redis_host"`
	RedisMaxIdleConnections uint     `mapstructure:"redis_max_idle_connections"`
}

type csrfData struct {
	CsrfSecret string `mapstructure:"csrf_secret"`
}

type jwtAuthData struct {
	Realm                 string `mapstructure:"jwt_realm"`
	IdentityKey           string `mapstructure:"jwt_identity_key"`
	SignSecret            string `mapstructure:"jwt_sign_secret"`
	SignAlgorithm         string `mapstructure:"jwt_sign_algorithm"`
	PrivateKey            string `mapstructure:"jwt_private_key"`
	PublicKey             string `mapstructure:"jwt_public_key"`
	LoginDataBinding      string `mapstructure:"jwt_login_data_binding"`
	TokenValidMinutes     uint   `mapstructure:"jwt_token_valid_minutes"`
	RefreshValidMinutes   uint   `mapstructure:"jwt_refresh_valid_minutes"`
	SendCookie            bool   `mapstructure:"jwt_send_cookie"`
	SecureCookie          bool   `mapstructure:"jwt_secure_cookie"`
	CookieHttpOnly        bool   `mapstructure:"jwt_cookie_http_only"`
	CookieMaxAgeDays      uint   `mapstructure:"jwt_cookie_max_age_days"`
	CookieDomain          string `mapstructure:"jwt_cookie_domain"`
	CookieName            string `mapstructure:"jwt_cookie_name"`
	CookieSameSite        string `mapstructure:"jwt_cookie_same_site"`
	LoginRoutePath        string `mapstructure:"jwt_login_route_path"`
	LogoutRoutePath       string `mapstructure:"jwt_logout_route_path"`
	RefreshTokenRoutePath string `mapstructure:"jwt_refresh_token_route_path"`
	TokenLookup           string `mapstructure:"jwt_token_lookup"`
	TokenHeadName         string `mapstructure:"jwt_token_head_name"`
	DisableAbort          bool   `mapstructure:"jwt_disable_abort"`
	SendAuthorization     bool   `mapstructure:"jwt_send_authorization"`
}

type templateDefinition struct {
	LayoutPath string `mapstructure:"layout_path"`
	PagePath   string `mapstructure:"page_path"`
}

type htmlTemplatesData struct {
	TemplateBaseDir     string               `mapstructure:"template_base_dir"`
	TemplateDefinitions []templateDefinition `mapstructure:"template_definitions"`
}

type routeDefinition struct {
	RouteGroupName             string   `mapstructure:"route_group_name"`
	JwtAuthSecured             bool     `mapstructure:"jwt_auth_secured"`
	MaxConcurrentRequestLimit  uint     `mapstructure:"max_concurrent_request_limit"`
	PerClientIpQps             uint     `mapstructure:"per_client_ip_qps"`
	PerClientIpBurst           uint     `mapstructure:"per_client_ip_burst"`
	PerClientIpTtlMinutes      uint     `mapstructure:"per_client_ip_ttl_minutes"`
	GzipCompressionType        string   `mapstructure:"gzip_compression_type"`
	GzipExcludeExtensions      []string `mapstructure:"gzip_exclude_extensions"`
	GzipExcludePaths           []string `mapstructure:"gzip_exclude_paths"`
	GzipExcludePathsRegex      []string `mapstructure:"gzip_exclude_paths_regex"`
	CorsAllowAllOrigins        bool     `mapstructure:"cors_allow_all_origins"`
	CorsAllowOrigins           []string `mapstructure:"cors_allow_origins"`
	CorsAllowMethods           []string `mapstructure:"cors_allow_methods"`
	CorsAllowHeaders           []string `mapstructure:"cors_allow_headers"`
	CorsAllowCredentials       bool     `mapstructure:"cors_allow_credentials"`
	CorsAllowWildCard          bool     `mapstructure:"cors_allow_wild_card"`
	CorsAllowBrowserExtensions bool     `mapstructure:"cors_allow_browser_extensions"`
	CorsAllowWebSockets        bool     `mapstructure:"cors_allow_web_sockets"`
	CorsAllowFiles             bool     `mapstructure:"cors_allow_files"`
	CorsMaxAgeMinutes          uint     `mapstructure:"cors_max_age_minutes"`
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

// FIX #1: Original wrote to "web_server.ws_route53_hosted_zone_id" (the hosted zone ID key)
// instead of "web_server.ws_route53_ttl" (the TTL key). This caused calling SetRoute53TTL
// to OVERWRITE the hosted zone ID with a uint TTL value, corrupting Route53 DNS config.
func (c *config) SetRoute53TTL(i uint) {
	if c._v != nil {
		c._v.Set("web_server.ws_route53_ttl", i)
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
	if c._v != nil {
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

// Read will load config settings from disk.
// FIX #2: Builds into local variables first and only overwrites c._v and data fields
// on success, so a failed Read() doesn't destroy previously valid state.
func (c *config) Read() error {
	if util.LenTrim(c.AppName) == 0 {
		return fmt.Errorf("App Name is Required")
	}

	configFileName := c.ConfigFileName
	if util.LenTrim(configFileName) == 0 {
		configFileName = "webserver"
	}

	v := &data.ViperConf{
		AppName:          c.AppName,
		ConfigName:       configFileName,
		CustomConfigPath: c.CustomConfigPath,

		UseYAML:            true,
		UseAutomaticEnvVar: false,
	}

	v.Default("web_server.ws_name", "webserver").Default(
		"web_server.ws_debug", false).Default(
		"web_server.ws_port", 8080).Default(
		"web_server.ws_server_pem", "").Default(
		"web_server.ws_server_key", "").Default(
		"web_server.google_recaptcha_secret", "").Default(
		"web_server.ws_trace_use_xray", false).Default(
		"web_server.ws_logging_use_sqs", false).Default(
		"web_server.ws_host_use_route53", false).Default(
		"web_server.ws_route53_hosted_zone_id", "").Default(
		"web_server.ws_route53_domain_suffix", "").Default(
		"web_server.ws_route53_ttl", 60).Default(
		"web_server.rest_target_ca_cert_files", "")

	v.Default("recovery.custom_recovery", false)

	v.Default("logging.custom_logging", false).Default(
		"logging.custom_logging_to_console", false).Default(
		"logging.sqs_logger_queue_name_prefix", "").Default(
		"logging.sqs_logger_message_retention_seconds", 14400).Default(
		"logging.sqs_logger_queue_url", "").Default(
		"logging.sqs_logger_queue_arn", "")

	v.Default("session.session_secret", "").Default(
		"session.session_names", []string{}).Default(
		"session.redis_host", "").Default(
		"session.redis_max_idle_connections", 10)

	v.Default("csrf.csrf_secret", "")

	v.Default("jwt_auth.jwt_realm", "").Default(
		"jwt_auth.jwt_identity_key", "id").Default(
		"jwt_auth.jwt_sign_secret", "").Default(
		// FIX #3: Default was "H256" — should be "HS256" to match the switch cases in
		// webserver.go setupWebServer() which checks for "hs256", "hs384", etc.
		// "H256" doesn't match any case, so the algorithm silently defaulted to HS256
		// from the code's fallback, but the config file would contain the wrong value.
		"jwt_auth.jwt_sign_algorithm", "HS256").Default(
		"jwt_auth.jwt_private_key", "").Default(
		"jwt_auth.jwt_public_key", "").Default(
		"jwt_auth.jwt_login_data_binding", "json").Default(
		"jwt_auth.jwt_token_valid_minutes", 15).Default(
		"jwt_auth.jwt_refresh_valid_minutes", 1440).Default(
		"jwt_auth.jwt_send_cookie", false).Default(
		"jwt_auth.jwt_secure_cookie", true).Default(
		"jwt_auth.jwt_cookie_http_only", true).Default(
		"jwt_auth.jwt_cookie_max_age_days", 14).Default(
		"jwt_auth.jwt_cookie_domain", "").Default(
		"jwt_auth.jwt_cookie_name", "").Default(
		"jwt_auth.jwt_cookie_same_site", "default").Default(
		"jwt_auth.jwt_login_route_path", "/login").Default(
		"jwt_auth.jwt_logout_route_path", "/logout").Default(
		"jwt_auth.jwt_refresh_token_route_path", "/refreshtoken").Default(
		"jwt_auth.jwt_token_lookup", "header:Authorization")

	v.Default("jwt_auth.jwt_token_head_name", "Bearer").Default(
		"jwt_auth.jwt_disable_abort", false).Default(
		"jwt_auth.jwt_send_authorization", false)

	v.Default("html_templates.template_base_dir", "").Default(
		"html_templates.template_definitions", []templateDefinition{})

	v.Default("routes", []routeDefinition{})

	if ok, err := v.Init(); err != nil {
		return err
	} else {
		if !ok {
			if e := v.Save(); e != nil {
				return fmt.Errorf("create config file failed: %w", e)
			}
		} else {
			v.WatchConfig()
		}
	}

	// Unmarshal into a temporary config to validate before committing
	tempCfg := &config{}
	if err := v.Unmarshal(tempCfg); err != nil {
		return err
	}

	// Validate port is within valid range
	if tempCfg.WebServer.Port == 0 {
		tempCfg.WebServer.Port = 8080
	}
	if tempCfg.WebServer.Port > 65535 {
		return fmt.Errorf("WebServer port %d is invalid (must be between 1-65535)", tempCfg.WebServer.Port)
	}

	// All succeeded — commit to receiver
	c._v = v
	c.ConfigFileName = configFileName
	c.WebServer = tempCfg.WebServer
	c.Recovery = tempCfg.Recovery
	c.Logging = tempCfg.Logging
	c.Session = tempCfg.Session
	c.Csrf = tempCfg.Csrf
	c.JwtAuth = tempCfg.JwtAuth
	c.HtmlTemplates = tempCfg.HtmlTemplates
	c.Routes = tempCfg.Routes

	return nil
}

// Save persists config settings to disk.
// FIX #4: Returns an error when _v is nil instead of silently succeeding.
func (c *config) Save() error {
	if strings.ToLower(os.Getenv("CONFIG_READ_ONLY")) == "true" {
		return nil
	}
	if c._v == nil {
		return fmt.Errorf("cannot save: viper config not initialized (call Read first)")
	}
	return c._v.Save()
}
