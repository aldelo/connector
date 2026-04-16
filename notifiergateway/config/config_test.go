package config

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Zero-value Config behavior
// ---------------------------------------------------------------------------

func TestConfig_ZeroValue_NotifierGatewayDataIsNil(t *testing.T) {
	c := &Config{}
	if c.NotifierGatewayData != nil {
		t.Fatal("expected NotifierGatewayData to be nil on zero-value Config")
	}
}

func TestConfig_EnsureGatewayData_InitializesNil(t *testing.T) {
	c := &Config{}
	c.ensureGatewayData()
	if c.NotifierGatewayData == nil {
		t.Fatal("expected ensureGatewayData to initialize NotifierGatewayData")
	}
}

func TestConfig_EnsureGatewayData_PreservesExisting(t *testing.T) {
	existing := &NotifierGatewayData{GatewayKey: "test-key"}
	c := &Config{NotifierGatewayData: existing}
	c.ensureGatewayData()
	if c.NotifierGatewayData.GatewayKey != "test-key" {
		t.Fatal("ensureGatewayData should not overwrite existing data")
	}
}

// ---------------------------------------------------------------------------
// Setter nil-guard tests (no Viper initialized)
// ---------------------------------------------------------------------------

func TestConfig_SettersReturnError_WhenViperNil(t *testing.T) {
	c := &Config{}

	tests := []struct {
		name string
		fn   func() error
	}{
		{"SetDynamoDBAwsRegion", func() error { return c.SetDynamoDBAwsRegion("us-east-1") }},
		{"SetDynamoDBUseDax", func() error { return c.SetDynamoDBUseDax(false) }},
		{"SetDynamoDBDaxUrl", func() error { return c.SetDynamoDBDaxUrl("dax://cluster") }},
		{"SetDynamoDBTable", func() error { return c.SetDynamoDBTable("my-table") }},
		{"SetDynamoDBTimeoutSeconds", func() error { return c.SetDynamoDBTimeoutSeconds(5) }},
		{"SetDynamoDBActionRetries", func() error { return c.SetDynamoDBActionRetries(3) }},
		{"SetGatewayKey", func() error { return c.SetGatewayKey("key") }},
		{"SetServiceDiscoveryTimeoutSeconds", func() error { return c.SetServiceDiscoveryTimeoutSeconds(5) }},
		{"SetHealthReportCleanUpFrequencySeconds", func() error { return c.SetHealthReportCleanUpFrequencySeconds(120) }},
		{"SetHealthReportRecordStaleMinutes", func() error { return c.SetHealthReportRecordStaleMinutes(5) }},
		{"SetRequireSNSSignature", func() error { return c.SetRequireSNSSignature(true) }},
		{"SetEndpointRetryDelayMs", func() error { return c.SetEndpointRetryDelayMs(250) }},
		{"SetEndpointRetryMaxAttempts", func() error { return c.SetEndpointRetryMaxAttempts(3) }},
		{"SetHashKeys", func() error {
			return c.SetHashKeys(HashKeyData{HashKeyName: "k", HashKeySecret: "s"})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if err == nil {
				t.Fatalf("%s should return error when viper is nil", tt.name)
			}
			if !strings.Contains(err.Error(), "viper config not initialized") {
				t.Fatalf("%s: unexpected error message: %s", tt.name, err.Error())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SetHashKeys validation tests (still requires viper=nil, so we test the
// nil-viper guard; but we can test validation logic by observing error messages)
// ---------------------------------------------------------------------------

func TestConfig_SetHashKeys_EmptyName(t *testing.T) {
	// SetHashKeys checks viper first, so with nil viper the name validation
	// is unreachable. This test documents that the nil-viper guard fires first.
	c := &Config{}
	err := c.SetHashKeys(HashKeyData{HashKeyName: "", HashKeySecret: "secret"})
	if err == nil {
		t.Fatal("expected error for empty hash key name (or nil viper)")
	}
}

// ---------------------------------------------------------------------------
// Read validation tests (no config file)
// ---------------------------------------------------------------------------

func TestConfig_Read_MissingAppName(t *testing.T) {
	c := &Config{}
	err := c.Read()
	if err == nil {
		t.Fatal("expected error when AppName is empty")
	}
	if !strings.Contains(err.Error(), "App Name is Required") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

// ---------------------------------------------------------------------------
// Save guard tests
// ---------------------------------------------------------------------------

func TestConfig_Save_ReturnsError_WhenViperNil(t *testing.T) {
	c := &Config{}
	err := c.Save()
	if err == nil {
		t.Fatal("expected error when saving with nil viper")
	}
	if !strings.Contains(err.Error(), "viper config not initialized") {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestConfig_Save_ReturnsNil_WhenReadOnly(t *testing.T) {
	t.Setenv("CONFIG_READ_ONLY", "true")
	c := &Config{} // _v is nil, but read-only short-circuits before that check
	err := c.Save()
	if err != nil {
		t.Fatalf("expected nil error when CONFIG_READ_ONLY=true, got: %s", err.Error())
	}
}

// ---------------------------------------------------------------------------
// NotifierGatewayData zero-value field defaults
// ---------------------------------------------------------------------------

func TestNotifierGatewayData_ZeroValue_Defaults(t *testing.T) {
	d := &NotifierGatewayData{}

	if d.DynamoDBAwsRegion != "" {
		t.Error("expected empty DynamoDBAwsRegion on zero value")
	}
	if d.DynamoDBUseDax != false {
		t.Error("expected false DynamoDBUseDax on zero value")
	}
	if d.DynamoDBDaxUrl != "" {
		t.Error("expected empty DynamoDBDaxUrl on zero value")
	}
	if d.DynamoDBTable != "" {
		t.Error("expected empty DynamoDBTable on zero value")
	}
	if d.DynamoDBTimeoutSeconds != 0 {
		t.Error("expected 0 DynamoDBTimeoutSeconds on zero value")
	}
	if d.DynamoDBActionRetries != 0 {
		t.Error("expected 0 DynamoDBActionRetries on zero value")
	}
	if d.GatewayKey != "" {
		t.Error("expected empty GatewayKey on zero value")
	}
	if d.RequireSNSSignature != false {
		t.Error("expected false RequireSNSSignature on zero value (Go default; config default is true)")
	}
	if d.HashKeys != nil {
		t.Error("expected nil HashKeys on zero value")
	}
}

// ---------------------------------------------------------------------------
// HashKeyData struct tests
// ---------------------------------------------------------------------------

func TestHashKeyData_Fields(t *testing.T) {
	hk := HashKeyData{
		HashKeyName:   "my-key",
		HashKeySecret: "my-secret",
	}
	if hk.HashKeyName != "my-key" {
		t.Errorf("expected HashKeyName 'my-key', got '%s'", hk.HashKeyName)
	}
	if hk.HashKeySecret != "my-secret" {
		t.Errorf("expected HashKeySecret 'my-secret', got '%s'", hk.HashKeySecret)
	}
}
