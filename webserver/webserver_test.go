package webserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ginw "github.com/aldelo/common/wrapper/gin"
	data "github.com/aldelo/common/wrapper/viper"
	"github.com/gin-gonic/gin"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newInitializedViperConf creates a ViperConf that has been Init'd against a
// temp directory so that Set/Get work. The caller owns the temp dir via
// t.TempDir().
func newInitializedViperConf(t *testing.T) *data.ViperConf {
	t.Helper()
	dir := t.TempDir()

	v := &data.ViperConf{
		AppName:          "test-app",
		ConfigName:       "webserver",
		CustomConfigPath: dir,
		UseYAML:          true,
	}

	// Set defaults so Init is happy even without a file on disk.
	v.Default("web_server.ws_name", "webserver")
	v.Default("web_server.ws_port", 8080)

	if _, err := v.Init(); err != nil {
		// Init returns (false, nil) when file not found — that is OK.
		t.Fatalf("ViperConf.Init() unexpected error: %v", err)
	}
	return v
}

// writeYAML writes a minimal YAML config file in dir with the given content
// and returns the directory path (for use as CustomConfigPath).
func writeYAML(t *testing.T, dir, fileName, content string) {
	t.Helper()
	p := filepath.Join(dir, fileName+".yaml")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("writeYAML: %v", err)
	}
}

// ===========================================================================
// 1. Config setters — nil-viper guard
// ===========================================================================

func TestConfigSetters_NilViper_NoOp(t *testing.T) {
	// config with _v == nil — every setter should be a no-op.
	c := &config{}

	// String setter
	c.SetWebServerName("should-not-stick")
	if c.WebServer.Name != "" {
		t.Errorf("SetWebServerName with nil _v modified Name: got %q", c.WebServer.Name)
	}

	// Uint setter
	c.SetWebServerPort(9090)
	if c.WebServer.Port != 0 {
		t.Errorf("SetWebServerPort with nil _v modified Port: got %d", c.WebServer.Port)
	}

	// Bool setter
	c.SetWebServerDebug(true)
	if c.WebServer.Debug {
		t.Error("SetWebServerDebug with nil _v modified Debug to true")
	}

	// Another string setter (different nested struct)
	c.SetJwtRealm("test-realm")
	if c.JwtAuth.Realm != "" {
		t.Errorf("SetJwtRealm with nil _v modified Realm: got %q", c.JwtAuth.Realm)
	}

	// Another uint setter (different nested struct)
	c.SetRoute53TTL(120)
	if c.WebServer.Route53TTL != 0 {
		t.Errorf("SetRoute53TTL with nil _v modified Route53TTL: got %d", c.WebServer.Route53TTL)
	}

	// Another bool setter
	c.SetTraceUseXRay(true)
	if c.WebServer.TraceUseXRay {
		t.Error("SetTraceUseXRay with nil _v modified TraceUseXRay to true")
	}
}

// ===========================================================================
// 2. Config setters — value propagation with non-nil viper
// ===========================================================================

func TestConfigSetters_WithViper_Propagate(t *testing.T) {
	v := newInitializedViperConf(t)
	c := &config{_v: v}

	// String
	c.SetWebServerName("my-server")
	if c.WebServer.Name != "my-server" {
		t.Errorf("SetWebServerName struct field: got %q, want %q", c.WebServer.Name, "my-server")
	}

	// Uint
	c.SetWebServerPort(3000)
	if c.WebServer.Port != 3000 {
		t.Errorf("SetWebServerPort struct field: got %d, want %d", c.WebServer.Port, 3000)
	}

	// Bool
	c.SetWebServerDebug(true)
	if !c.WebServer.Debug {
		t.Error("SetWebServerDebug struct field: got false, want true")
	}

	// Route53TTL — exercises the FIX #1 correction
	c.SetRoute53TTL(120)
	if c.WebServer.Route53TTL != 120 {
		t.Errorf("SetRoute53TTL struct field: got %d, want %d", c.WebServer.Route53TTL, 120)
	}

	// Jwt realm (different sub-struct)
	c.SetJwtRealm("api")
	if c.JwtAuth.Realm != "api" {
		t.Errorf("SetJwtRealm struct field: got %q, want %q", c.JwtAuth.Realm, "api")
	}

	// Bool on logging sub-struct
	c.SetCustomLogging(true)
	if !c.Logging.CustomLogging {
		t.Error("SetCustomLogging struct field: got false, want true")
	}
}

// ===========================================================================
// 3. Config Read() validation
// ===========================================================================

func TestConfigRead_EmptyAppName_Error(t *testing.T) {
	c := &config{AppName: ""}
	err := c.Read()
	if err == nil {
		t.Fatal("expected error for empty AppName, got nil")
	}
	if !strings.Contains(err.Error(), "App Name is Required") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestConfigRead_DefaultConfigFileName(t *testing.T) {
	dir := t.TempDir()
	c := &config{
		AppName:          "test-app",
		ConfigFileName:   "", // should default to "webserver"
		CustomConfigPath: dir,
	}
	if err := c.Read(); err != nil {
		t.Fatalf("Read() unexpected error: %v", err)
	}
	if c.ConfigFileName != "webserver" {
		t.Errorf("ConfigFileName: got %q, want %q", c.ConfigFileName, "webserver")
	}
}

func TestConfigRead_PortZero_DefaultsTo8080(t *testing.T) {
	dir := t.TempDir()
	// Write a config where port is explicitly 0
	writeYAML(t, dir, "webserver", "web_server:\n  ws_port: 0\n")

	c := &config{
		AppName:          "test-app",
		ConfigFileName:   "webserver",
		CustomConfigPath: dir,
	}
	if err := c.Read(); err != nil {
		t.Fatalf("Read() unexpected error: %v", err)
	}
	if c.WebServer.Port != 8080 {
		t.Errorf("Port with 0 in config: got %d, want 8080", c.WebServer.Port)
	}
}

func TestConfigRead_PortOver65535_Error(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "webserver", "web_server:\n  ws_port: 70000\n")

	c := &config{
		AppName:          "test-app",
		ConfigFileName:   "webserver",
		CustomConfigPath: dir,
	}
	err := c.Read()
	if err == nil {
		t.Fatal("expected error for port > 65535, got nil")
	}
	if !strings.Contains(err.Error(), "invalid") && !strings.Contains(err.Error(), "65535") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestConfigRead_ValidPort(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "webserver", "web_server:\n  ws_port: 9090\n  ws_name: my-ws\n")

	c := &config{
		AppName:          "test-app",
		ConfigFileName:   "webserver",
		CustomConfigPath: dir,
	}
	if err := c.Read(); err != nil {
		t.Fatalf("Read() unexpected error: %v", err)
	}
	if c.WebServer.Port != 9090 {
		t.Errorf("Port: got %d, want 9090", c.WebServer.Port)
	}
	if c.WebServer.Name != "my-ws" {
		t.Errorf("Name: got %q, want %q", c.WebServer.Name, "my-ws")
	}
}

func TestConfigRead_ViperInitialized(t *testing.T) {
	dir := t.TempDir()
	c := &config{
		AppName:          "test-app",
		ConfigFileName:   "webserver",
		CustomConfigPath: dir,
	}
	if err := c.Read(); err != nil {
		t.Fatalf("Read() unexpected error: %v", err)
	}
	if c._v == nil {
		t.Error("_v should be non-nil after successful Read()")
	}
}

// ===========================================================================
// 4. Config Save()
// ===========================================================================

func TestConfigSave_ReadOnlyEnv_ReturnsNil(t *testing.T) {
	t.Setenv("CONFIG_READ_ONLY", "true")

	c := &config{} // _v is nil, but CONFIG_READ_ONLY short-circuits first
	if err := c.Save(); err != nil {
		t.Errorf("Save() with CONFIG_READ_ONLY=true: got error %v, want nil", err)
	}
}

func TestConfigSave_ReadOnlyEnvCaseInsensitive(t *testing.T) {
	t.Setenv("CONFIG_READ_ONLY", "TRUE")

	c := &config{}
	if err := c.Save(); err != nil {
		t.Errorf("Save() with CONFIG_READ_ONLY=TRUE: got error %v, want nil", err)
	}
}

func TestConfigSave_NilViper_Error(t *testing.T) {
	// Ensure CONFIG_READ_ONLY is not set
	t.Setenv("CONFIG_READ_ONLY", "")

	c := &config{} // _v is nil
	err := c.Save()
	if err == nil {
		t.Fatal("expected error for nil _v, got nil")
	}
	if !strings.Contains(err.Error(), "cannot save") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// ===========================================================================
// 5. WebServer nil receiver guards
// ===========================================================================

func TestWebServer_Port_NilReceiver(t *testing.T) {
	var w *WebServer
	if got := w.Port(); got != 0 {
		t.Errorf("Port() on nil receiver: got %d, want 0", got)
	}
}

func TestWebServer_UseTls_NilReceiver(t *testing.T) {
	var w *WebServer
	if got := w.UseTls(); got {
		t.Error("UseTls() on nil receiver: got true, want false")
	}
}

func TestWebServer_ExtractJwtClaims_NilReceiver(t *testing.T) {
	var w *WebServer
	if got := w.ExtractJwtClaims(nil); got != nil {
		t.Errorf("ExtractJwtClaims() on nil receiver: got %v, want nil", got)
	}
}

func TestWebServer_SetRouteGroupCustomMiddleware_NilReceiver(t *testing.T) {
	var w *WebServer
	if got := w.SetRouteGroupCustomMiddleware("test", []gin.HandlerFunc{func(c *gin.Context) {}}); got {
		t.Error("SetRouteGroupCustomMiddleware() on nil receiver: got true, want false")
	}
}

// ===========================================================================
// 6. WebServer.Port()
// ===========================================================================

func TestWebServer_Port_NilConfig(t *testing.T) {
	w := &WebServer{}
	if got := w.Port(); got != 0 {
		t.Errorf("Port() with nil config: got %d, want 0", got)
	}
}

func TestWebServer_Port_ReturnsConfiguredPort(t *testing.T) {
	w := &WebServer{
		_config: &config{
			WebServer: webServerData{Port: 4443},
		},
	}
	if got := w.Port(); got != 4443 {
		t.Errorf("Port(): got %d, want 4443", got)
	}
}

// ===========================================================================
// 7. WebServer.UseTls()
// ===========================================================================

func TestWebServer_UseTls_NilConfig(t *testing.T) {
	w := &WebServer{}
	if got := w.UseTls(); got {
		t.Error("UseTls() with nil config: got true, want false")
	}
}

func TestWebServer_UseTls_BothSet(t *testing.T) {
	w := &WebServer{
		_config: &config{
			WebServer: webServerData{
				ServerPem: "/path/to/cert.pem",
				ServerKey: "/path/to/cert.key",
			},
		},
	}
	if !w.UseTls() {
		t.Error("UseTls() with both pem and key: got false, want true")
	}
}

func TestWebServer_UseTls_OnlyPem(t *testing.T) {
	w := &WebServer{
		_config: &config{
			WebServer: webServerData{
				ServerPem: "/path/to/cert.pem",
				ServerKey: "",
			},
		},
	}
	if w.UseTls() {
		t.Error("UseTls() with only pem: got true, want false")
	}
}

func TestWebServer_UseTls_OnlyKey(t *testing.T) {
	w := &WebServer{
		_config: &config{
			WebServer: webServerData{
				ServerPem: "",
				ServerKey: "/path/to/cert.key",
			},
		},
	}
	if w.UseTls() {
		t.Error("UseTls() with only key: got true, want false")
	}
}

func TestWebServer_UseTls_NeitherSet(t *testing.T) {
	w := &WebServer{
		_config: &config{
			WebServer: webServerData{
				ServerPem: "",
				ServerKey: "",
			},
		},
	}
	if w.UseTls() {
		t.Error("UseTls() with neither: got true, want false")
	}
}

// ===========================================================================
// 8. WebServer.SetRouteGroupCustomMiddleware()
// ===========================================================================

func TestSetRouteGroupCustomMiddleware_NilGinWebServer(t *testing.T) {
	w := &WebServer{_ginwebserver: nil}
	handler := []gin.HandlerFunc{func(c *gin.Context) {}}
	if w.SetRouteGroupCustomMiddleware("test", handler) {
		t.Error("expected false when _ginwebserver is nil")
	}
}

func TestSetRouteGroupCustomMiddleware_NilRoutes(t *testing.T) {
	w := &WebServer{
		_ginwebserver: &ginw.Gin{},
	}
	handler := []gin.HandlerFunc{func(c *gin.Context) {}}
	if w.SetRouteGroupCustomMiddleware("test", handler) {
		t.Error("expected false when Routes is nil")
	}
}

func TestSetRouteGroupCustomMiddleware_EmptyRouterFunc(t *testing.T) {
	rd := &ginw.RouteDefinition{}
	w := &WebServer{
		_ginwebserver: &ginw.Gin{
			Routes: map[string]*ginw.RouteDefinition{"*": rd},
		},
	}
	if w.SetRouteGroupCustomMiddleware("base", nil) {
		t.Error("expected false for nil routerFunc")
	}
	if w.SetRouteGroupCustomMiddleware("base", []gin.HandlerFunc{}) {
		t.Error("expected false for empty routerFunc")
	}
}

func TestSetRouteGroupCustomMiddleware_BaseMapsToStar(t *testing.T) {
	rd := &ginw.RouteDefinition{}
	w := &WebServer{
		_ginwebserver: &ginw.Gin{
			Routes: map[string]*ginw.RouteDefinition{"*": rd},
		},
	}
	handler := []gin.HandlerFunc{func(c *gin.Context) {}}
	if !w.SetRouteGroupCustomMiddleware("base", handler) {
		t.Error("expected true for 'base' mapping to '*'")
	}
	if rd.CustomMiddleware == nil {
		t.Error("CustomMiddleware not set on '*' route")
	}
}

func TestSetRouteGroupCustomMiddleware_EmptyStringMapsToStar(t *testing.T) {
	rd := &ginw.RouteDefinition{}
	w := &WebServer{
		_ginwebserver: &ginw.Gin{
			Routes: map[string]*ginw.RouteDefinition{"*": rd},
		},
	}
	handler := []gin.HandlerFunc{func(c *gin.Context) {}}
	if !w.SetRouteGroupCustomMiddleware("", handler) {
		t.Error("expected true for empty string mapping to '*'")
	}
	if rd.CustomMiddleware == nil {
		t.Error("CustomMiddleware not set on '*' route")
	}
}

func TestSetRouteGroupCustomMiddleware_NonExistentGroup(t *testing.T) {
	rd := &ginw.RouteDefinition{}
	w := &WebServer{
		_ginwebserver: &ginw.Gin{
			Routes: map[string]*ginw.RouteDefinition{"api": rd},
		},
	}
	handler := []gin.HandlerFunc{func(c *gin.Context) {}}
	if w.SetRouteGroupCustomMiddleware("nonexistent", handler) {
		t.Error("expected false for non-existent route group")
	}
}

func TestSetRouteGroupCustomMiddleware_NamedGroup(t *testing.T) {
	rd := &ginw.RouteDefinition{}
	w := &WebServer{
		_ginwebserver: &ginw.Gin{
			Routes: map[string]*ginw.RouteDefinition{"api": rd},
		},
	}
	handler := []gin.HandlerFunc{func(c *gin.Context) {}}
	if !w.SetRouteGroupCustomMiddleware("API", handler) {
		t.Error("expected true for existing 'api' group (case insensitive)")
	}
	if rd.CustomMiddleware == nil {
		t.Error("CustomMiddleware not set on 'api' route")
	}
}

// ===========================================================================
// 9. readConfig()
// ===========================================================================

func TestReadConfig_EmptyAppName_Error(t *testing.T) {
	_, err := readConfig("", "", "")
	if err == nil {
		t.Fatal("expected error for empty appName")
	}
	if !strings.Contains(err.Error(), "App Name") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestReadConfig_ValidAppName_Success(t *testing.T) {
	dir := t.TempDir()
	c, err := readConfig("test-app", "webserver", dir)
	if err != nil {
		t.Fatalf("readConfig() unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("readConfig() returned nil config")
	}
	if c.WebServer.Port == 0 {
		t.Error("readConfig() port should not be 0 after defaults")
	}
}

func TestReadConfig_PortOver65535_Error(t *testing.T) {
	dir := t.TempDir()
	writeYAML(t, dir, "webserver", "web_server:\n  ws_port: 70000\n")

	_, err := readConfig("test-app", "webserver", dir)
	if err == nil {
		t.Fatal("expected error for port > 65535")
	}
}

// ===========================================================================
// 10. setupWebServer() — validation errors
// ===========================================================================

func TestSetupWebServer_NilConfig_Error(t *testing.T) {
	w := &WebServer{
		_config:       nil,
		_ginwebserver: ginw.NewServer("test", 8080, true, false, nil),
	}
	err := w.setupWebServer()
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	if !strings.Contains(err.Error(), "Config is Required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSetupWebServer_NilGinServer_Error(t *testing.T) {
	w := &WebServer{
		_config:       &config{},
		_ginwebserver: nil,
	}
	err := w.setupWebServer()
	if err == nil {
		t.Fatal("expected error for nil gin server")
	}
	if !strings.Contains(err.Error(), "Web Server Not Yet Created") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSetupWebServer_EmptyName_Error(t *testing.T) {
	w := &WebServer{
		_config:       &config{},
		_ginwebserver: ginw.NewServer("", 8080, true, false, nil),
	}
	// NewServer sets the Name to the arg — empty string means empty Name
	err := w.setupWebServer()
	if err == nil {
		t.Fatal("expected error for empty server name")
	}
	if !strings.Contains(err.Error(), "Name is Required") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSetupWebServer_InvalidPort_Zero_Error(t *testing.T) {
	g := ginw.NewServer("test", 0, true, false, nil)
	w := &WebServer{
		_config:       &config{},
		_ginwebserver: g,
	}
	err := w.setupWebServer()
	if err == nil {
		t.Fatal("expected error for port 0")
	}
	if !strings.Contains(err.Error(), "Port is Invalid") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSetupWebServer_InvalidPort_Over65535_Error(t *testing.T) {
	g := ginw.NewServer("test", 70000, true, false, nil)
	w := &WebServer{
		_config:       &config{},
		_ginwebserver: g,
	}
	err := w.setupWebServer()
	if err == nil {
		t.Fatal("expected error for port > 65535")
	}
	if !strings.Contains(err.Error(), "Port is Invalid") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSetupWebServer_ValidMinimal_Success(t *testing.T) {
	g := ginw.NewServer("test-ws", 8080, true, false, nil)
	w := &WebServer{
		_config:       &config{},
		_ginwebserver: g,
	}
	if err := w.setupWebServer(); err != nil {
		t.Errorf("setupWebServer() with valid minimal config: unexpected error %v", err)
	}
}

// ===========================================================================
// Additional edge cases
// ===========================================================================

func TestWebServer_ExtractJwtClaims_NilGinWebServer(t *testing.T) {
	w := &WebServer{_ginwebserver: nil}
	if got := w.ExtractJwtClaims(nil); got != nil {
		t.Errorf("ExtractJwtClaims() with nil _ginwebserver: got %v, want nil", got)
	}
}

func TestConfigSetters_SetSessionNames(t *testing.T) {
	v := newInitializedViperConf(t)
	c := &config{_v: v}

	c.SetSessionNames("sess1", "sess2")
	if len(c.Session.SessionNames) != 2 {
		t.Errorf("SetSessionNames: got %d names, want 2", len(c.Session.SessionNames))
	}
	if c.Session.SessionNames[0] != "sess1" || c.Session.SessionNames[1] != "sess2" {
		t.Errorf("SetSessionNames: got %v, want [sess1 sess2]", c.Session.SessionNames)
	}
}

func TestConfigSetters_SetSessionNames_NilViper(t *testing.T) {
	c := &config{}
	c.SetSessionNames("sess1")
	if c.Session.SessionNames != nil {
		t.Errorf("SetSessionNames with nil _v should be no-op, got %v", c.Session.SessionNames)
	}
}

func TestConfigSetters_SetRoutes_NilViper(t *testing.T) {
	c := &config{}
	c.SetRoutes(routeDefinition{RouteGroupName: "api"})
	if c.Routes != nil {
		t.Errorf("SetRoutes with nil _v should be no-op, got %v", c.Routes)
	}
}

func TestConfigSetters_SetTemplateDefinitions_NilViper(t *testing.T) {
	c := &config{}
	c.SetTemplateDefinitions(templateDefinition{LayoutPath: "/a", PagePath: "/b"})
	if c.HtmlTemplates.TemplateDefinitions != nil {
		t.Errorf("SetTemplateDefinitions with nil _v should be no-op, got %v", c.HtmlTemplates.TemplateDefinitions)
	}
}

func TestSetRouteGroupCustomMiddleware_NilRouteDefinition(t *testing.T) {
	w := &WebServer{
		_ginwebserver: &ginw.Gin{
			Routes: map[string]*ginw.RouteDefinition{"*": nil},
		},
	}
	handler := []gin.HandlerFunc{func(c *gin.Context) {}}
	if w.SetRouteGroupCustomMiddleware("", handler) {
		t.Error("expected false when route definition is nil")
	}
}
