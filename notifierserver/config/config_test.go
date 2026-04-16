package config

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Zero-value Config behavior
// ---------------------------------------------------------------------------

func TestConfig_ZeroValue_ServerDataIsNil(t *testing.T) {
	c := &Config{}
	if c.NotifierServerData != nil {
		t.Fatal("expected NotifierServerData to be nil on zero-value Config")
	}
	if c.SubscriptionsData != nil {
		t.Fatal("expected SubscriptionsData to be nil on zero-value Config")
	}
}

func TestConfig_EnsureServerData_InitializesNil(t *testing.T) {
	c := &Config{}
	c.ensureServerData()
	if c.NotifierServerData == nil {
		t.Fatal("expected ensureServerData to initialize NotifierServerData")
	}
}

func TestConfig_EnsureServerData_PreservesExisting(t *testing.T) {
	existing := &NotifierServerData{ServerKey: "test-key"}
	c := &Config{NotifierServerData: existing}
	c.ensureServerData()
	if c.NotifierServerData.ServerKey != "test-key" {
		t.Fatal("ensureServerData should not overwrite existing data")
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
		{"SetNotifierGatewayUrl", func() error { return c.SetNotifierGatewayUrl("http://gw") }},
		{"SetServerKey", func() error { return c.SetServerKey("key") }},
		{"SetDynamoDBAwsRegion", func() error { return c.SetDynamoDBAwsRegion("us-east-1") }},
		{"SetDynamoDBUseDax", func() error { return c.SetDynamoDBUseDax(false) }},
		{"SetDynamoDBDaxUrl", func() error { return c.SetDynamoDBDaxUrl("dax://cluster") }},
		{"SetDynamoDBTable", func() error { return c.SetDynamoDBTable("my-table") }},
		{"SetDynamoDBTimeoutSeconds", func() error { return c.SetDynamoDBTimeoutSeconds(5) }},
		{"SetDynamoDBActionRetries", func() error { return c.SetDynamoDBActionRetries(3) }},
		{"SetSnsAwsRegion", func() error { return c.SetSnsAwsRegion("us-west-2") }},
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
// GetSubscriptionArn tests (pure logic, no Viper dependency)
// ---------------------------------------------------------------------------

func TestConfig_GetSubscriptionArn_NilReceiver(t *testing.T) {
	var c *Config
	result := c.GetSubscriptionArn("arn:aws:sns:us-east-1:123:topic")
	if result != "" {
		t.Fatalf("expected empty string for nil receiver, got: %s", result)
	}
}

func TestConfig_GetSubscriptionArn_EmptySubscriptions(t *testing.T) {
	c := &Config{}
	result := c.GetSubscriptionArn("arn:aws:sns:us-east-1:123:topic")
	if result != "" {
		t.Fatalf("expected empty string for empty subscriptions, got: %s", result)
	}
}

func TestConfig_GetSubscriptionArn_Found(t *testing.T) {
	c := &Config{
		SubscriptionsData: []*SubscriptionsData{
			{TopicArn: "arn:aws:sns:us-east-1:123:topicA", SubscriptionArn: "arn:sub:A"},
			{TopicArn: "arn:aws:sns:us-east-1:123:topicB", SubscriptionArn: "arn:sub:B"},
		},
	}

	result := c.GetSubscriptionArn("arn:aws:sns:us-east-1:123:topicB")
	if result != "arn:sub:B" {
		t.Fatalf("expected 'arn:sub:B', got: %s", result)
	}
}

func TestConfig_GetSubscriptionArn_CaseInsensitive(t *testing.T) {
	c := &Config{
		SubscriptionsData: []*SubscriptionsData{
			{TopicArn: "arn:aws:sns:us-east-1:123:MyTopic", SubscriptionArn: "arn:sub:X"},
		},
	}

	result := c.GetSubscriptionArn("arn:aws:sns:us-east-1:123:mytopic")
	if result != "arn:sub:X" {
		t.Fatalf("expected case-insensitive match to return 'arn:sub:X', got: %s", result)
	}
}

func TestConfig_GetSubscriptionArn_NotFound(t *testing.T) {
	c := &Config{
		SubscriptionsData: []*SubscriptionsData{
			{TopicArn: "arn:aws:sns:us-east-1:123:topicA", SubscriptionArn: "arn:sub:A"},
		},
	}

	result := c.GetSubscriptionArn("arn:aws:sns:us-east-1:123:nonexistent")
	if result != "" {
		t.Fatalf("expected empty string for not-found topic, got: %s", result)
	}
}

func TestConfig_GetSubscriptionArn_SkipsNilElements(t *testing.T) {
	c := &Config{
		SubscriptionsData: []*SubscriptionsData{
			nil,
			{TopicArn: "arn:aws:sns:us-east-1:123:topicA", SubscriptionArn: "arn:sub:A"},
			nil,
		},
	}

	result := c.GetSubscriptionArn("arn:aws:sns:us-east-1:123:topicA")
	if result != "arn:sub:A" {
		t.Fatalf("expected 'arn:sub:A', got: %s", result)
	}
}

// ---------------------------------------------------------------------------
// GetAllSubscriptionArns tests
// ---------------------------------------------------------------------------

func TestConfig_GetAllSubscriptionArns_NilReceiver(t *testing.T) {
	var c *Config
	result := c.GetAllSubscriptionArns()
	if result != nil {
		t.Fatal("expected nil for nil receiver")
	}
}

func TestConfig_GetAllSubscriptionArns_Empty(t *testing.T) {
	c := &Config{}
	result := c.GetAllSubscriptionArns()
	if len(result) != 0 {
		t.Fatalf("expected empty slice, got %d elements", len(result))
	}
}

func TestConfig_GetAllSubscriptionArns_SkipsNilElements(t *testing.T) {
	c := &Config{
		SubscriptionsData: []*SubscriptionsData{
			nil,
			{TopicArn: "topic1", SubscriptionArn: "sub1"},
			nil,
			{TopicArn: "topic2", SubscriptionArn: "sub2"},
		},
	}

	result := c.GetAllSubscriptionArns()
	if len(result) != 2 {
		t.Fatalf("expected 2 elements (nil entries skipped), got %d", len(result))
	}
	if result[0].TopicArn != "topic1" || result[1].TopicArn != "topic2" {
		t.Fatal("unexpected subscription data in result")
	}
}

func TestConfig_GetAllSubscriptionArns_ReturnsSnapshot(t *testing.T) {
	c := &Config{
		SubscriptionsData: []*SubscriptionsData{
			{TopicArn: "topic1", SubscriptionArn: "sub1"},
		},
	}

	snapshot := c.GetAllSubscriptionArns()
	// Mutating the snapshot should not affect the original
	snapshot[0].TopicArn = "mutated"
	if c.SubscriptionsData[0].TopicArn == "mutated" {
		t.Fatal("GetAllSubscriptionArns should return a copy, not a reference to original data")
	}
}

// ---------------------------------------------------------------------------
// SetSubscriptionData guard tests (no Viper)
// ---------------------------------------------------------------------------

func TestConfig_SetSubscriptionData_NilReceiver(t *testing.T) {
	var c *Config
	// Should not panic
	c.SetSubscriptionData("topic", "sub")
}

func TestConfig_SetSubscriptionData_NilViper(t *testing.T) {
	c := &Config{}
	// Should not panic — silently returns when _v is nil
	c.SetSubscriptionData("topic", "sub")
}

func TestConfig_SetSubscriptionData_EmptyTopicArn(t *testing.T) {
	c := &Config{}
	// Should not panic — silently returns for empty topicArn
	c.SetSubscriptionData("", "sub")
}

// ---------------------------------------------------------------------------
// RemoveSubscriptionData guard tests (no Viper)
// ---------------------------------------------------------------------------

func TestConfig_RemoveSubscriptionData_NilReceiver(t *testing.T) {
	var c *Config
	// Should not panic
	c.RemoveSubscriptionData("topic")
}

func TestConfig_RemoveSubscriptionData_NilViper(t *testing.T) {
	c := &Config{}
	// Should not panic — silently returns when _v is nil
	c.RemoveSubscriptionData("topic")
}

// ---------------------------------------------------------------------------
// Read validation tests
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
// NotifierServerData zero-value field defaults
// ---------------------------------------------------------------------------

func TestNotifierServerData_ZeroValue_Defaults(t *testing.T) {
	d := &NotifierServerData{}

	if d.GatewayUrl != "" {
		t.Error("expected empty GatewayUrl on zero value")
	}
	if d.ServerKey != "" {
		t.Error("expected empty ServerKey on zero value")
	}
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
	if d.SnsAwsRegion != "" {
		t.Error("expected empty SnsAwsRegion on zero value")
	}
}

// ---------------------------------------------------------------------------
// SubscriptionsData struct tests
// ---------------------------------------------------------------------------

func TestSubscriptionsData_Fields(t *testing.T) {
	sd := SubscriptionsData{
		TopicArn:        "arn:aws:sns:us-east-1:123:my-topic",
		SubscriptionArn: "arn:aws:sns:us-east-1:123:my-topic:sub-id",
	}
	if sd.TopicArn != "arn:aws:sns:us-east-1:123:my-topic" {
		t.Errorf("unexpected TopicArn: %s", sd.TopicArn)
	}
	if sd.SubscriptionArn != "arn:aws:sns:us-east-1:123:my-topic:sub-id" {
		t.Errorf("unexpected SubscriptionArn: %s", sd.SubscriptionArn)
	}
}
