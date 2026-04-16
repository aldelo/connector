package sdoperationstatus

import (
	"testing"
)

func TestConstantValues(t *testing.T) {
	tests := []struct {
		name     string
		val      SdOperationStatus
		wantInt  int
	}{
		{"UNKNOWN", UNKNOWN, 0},
		{"Submitted", Submitted, 1},
		{"Pending", Pending, 2},
		{"Success", Success, 3},
		{"Fail", Fail, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if int(tt.val) != tt.wantInt {
				t.Errorf("%s: got int %d, want %d", tt.name, int(tt.val), tt.wantInt)
			}
		})
	}
}

func TestKeyConstants(t *testing.T) {
	tests := []struct {
		name    string
		val     SdOperationStatus
		wantKey string
	}{
		{"UNKNOWN", UNKNOWN, "UNKNOWN"},
		{"Submitted", Submitted, "SUBMITTED"},
		{"Pending", Pending, "PENDING"},
		{"Success", Success, "SUCCESS"},
		{"Fail", Fail, "FAIL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.val.Key()
			if got != tt.wantKey {
				t.Errorf("Key() = %q, want %q", got, tt.wantKey)
			}
		})
	}
}

func TestCaptionConstants(t *testing.T) {
	tests := []struct {
		name        string
		val         SdOperationStatus
		wantCaption string
	}{
		{"UNKNOWN", UNKNOWN, "UNKNOWN"},
		{"Submitted", Submitted, "Submitted"},
		{"Pending", Pending, "Pending"},
		{"Success", Success, "Success"},
		{"Fail", Fail, "Fail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.val.Caption()
			if got != tt.wantCaption {
				t.Errorf("Caption() = %q, want %q", got, tt.wantCaption)
			}
		})
	}
}

func TestDescriptionConstants(t *testing.T) {
	tests := []struct {
		name            string
		val             SdOperationStatus
		wantDescription string
	}{
		{"UNKNOWN", UNKNOWN, "UNKNOWN"},
		{"Submitted", Submitted, "Submitted"},
		{"Pending", Pending, "Pending"},
		{"Success", Success, "Success"},
		{"Fail", Fail, "Fail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.val.Description()
			if got != tt.wantDescription {
				t.Errorf("Description() = %q, want %q", got, tt.wantDescription)
			}
		})
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		name     string
		val      SdOperationStatus
		wantStr  string
	}{
		{"UNKNOWN", UNKNOWN, "UNKNOWN"},
		{"Submitted", Submitted, "Submitted"},
		{"Pending", Pending, "Pending"},
		{"Success", Success, "Success"},
		{"Fail", Fail, "Fail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.val.String()
			if got != tt.wantStr {
				t.Errorf("String() = %q, want %q", got, tt.wantStr)
			}
		})
	}
}

func TestStringInvalidValue(t *testing.T) {
	invalid := SdOperationStatus(99)
	got := invalid.String()
	if got != "" {
		t.Errorf("String() for invalid value = %q, want empty string", got)
	}
}

func TestIntValue(t *testing.T) {
	tests := []struct {
		name    string
		val     SdOperationStatus
		wantInt int
	}{
		{"UNKNOWN", UNKNOWN, 0},
		{"Submitted", Submitted, 1},
		{"Pending", Pending, 2},
		{"Success", Success, 3},
		{"Fail", Fail, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.val.IntValue()
			if got != tt.wantInt {
				t.Errorf("IntValue() = %d, want %d", got, tt.wantInt)
			}
		})
	}
}

func TestIntString(t *testing.T) {
	tests := []struct {
		name    string
		val     SdOperationStatus
		wantStr string
	}{
		{"UNKNOWN", UNKNOWN, "0"},
		{"Submitted", Submitted, "1"},
		{"Pending", Pending, "2"},
		{"Success", Success, "3"},
		{"Fail", Fail, "4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.val.IntString()
			if got != tt.wantStr {
				t.Errorf("IntString() = %q, want %q", got, tt.wantStr)
			}
		})
	}
}

func TestParseByNameRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantVal SdOperationStatus
	}{
		{"UNKNOWN", "UNKNOWN", UNKNOWN},
		{"Submitted", "Submitted", Submitted},
		{"Pending", "Pending", Pending},
		{"Success", "Success", Success},
		{"Fail", "Fail", Fail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UNKNOWN.ParseByName(tt.input)
			if err != nil {
				t.Fatalf("ParseByName(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.wantVal {
				t.Errorf("ParseByName(%q) = %d, want %d", tt.input, got, tt.wantVal)
			}
			// Round-trip: String() of parsed value should match input
			if got.String() != tt.input {
				t.Errorf("Round-trip failed: ParseByName(%q).String() = %q", tt.input, got.String())
			}
		})
	}
}

func TestParseByNameInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty string", ""},
		{"nonexistent", "DoesNotExist"},
		{"lowercase variant", "unknown"},
		{"numeric string", "0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UNKNOWN.ParseByName(tt.input)
			if err == nil {
				t.Errorf("ParseByName(%q) expected error, got value %d", tt.input, got)
			}
			if got != -1 {
				t.Errorf("ParseByName(%q) expected -1 on error, got %d", tt.input, got)
			}
		})
	}
}

func TestParseByKeyRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantVal SdOperationStatus
	}{
		{"UNKNOWN", "UNKNOWN", UNKNOWN},
		{"SUBMITTED", "SUBMITTED", Submitted},
		{"PENDING", "PENDING", Pending},
		{"SUCCESS", "SUCCESS", Success},
		{"FAIL", "FAIL", Fail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UNKNOWN.ParseByKey(tt.input)
			if err != nil {
				t.Fatalf("ParseByKey(%q) unexpected error: %v", tt.input, err)
			}
			if got != tt.wantVal {
				t.Errorf("ParseByKey(%q) = %d, want %d", tt.input, got, tt.wantVal)
			}
			// Round-trip: Key() of parsed value should match input
			if got.Key() != tt.input {
				t.Errorf("Round-trip failed: ParseByKey(%q).Key() = %q", tt.input, got.Key())
			}
		})
	}
}

func TestParseByKeyInvalid(t *testing.T) {
	got, err := UNKNOWN.ParseByKey("NONEXISTENT")
	if err == nil {
		t.Errorf("ParseByKey(NONEXISTENT) expected error, got value %d", got)
	}
	if got != -1 {
		t.Errorf("ParseByKey(NONEXISTENT) expected -1 on error, got %d", got)
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		name  string
		val   SdOperationStatus
		valid bool
	}{
		{"UNKNOWN is valid", UNKNOWN, true},
		{"Submitted is valid", Submitted, true},
		{"Pending is valid", Pending, true},
		{"Success is valid", Success, true},
		{"Fail is valid", Fail, true},
		{"negative is invalid", SdOperationStatus(-1), false},
		{"out of range is invalid", SdOperationStatus(99), false},
		{"5 is invalid", SdOperationStatus(5), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.val.Valid()
			if got != tt.valid {
				t.Errorf("Valid() = %v, want %v", got, tt.valid)
			}
		})
	}
}

func TestValueSlice(t *testing.T) {
	values := UNKNOWN.ValueSlice()
	if len(values) != 5 {
		t.Fatalf("ValueSlice() length = %d, want 5", len(values))
	}

	expected := []SdOperationStatus{UNKNOWN, Submitted, Pending, Success, Fail}
	for i, v := range expected {
		if values[i] != v {
			t.Errorf("ValueSlice()[%d] = %d, want %d", i, values[i], v)
		}
	}
}

func TestNameMap(t *testing.T) {
	m := UNKNOWN.NameMap()
	if len(m) != 5 {
		t.Fatalf("NameMap() length = %d, want 5", len(m))
	}

	expectations := map[string]SdOperationStatus{
		"UNKNOWN":   UNKNOWN,
		"Submitted": Submitted,
		"Pending":   Pending,
		"Success":   Success,
		"Fail":      Fail,
	}

	for name, wantVal := range expectations {
		gotVal, ok := m[name]
		if !ok {
			t.Errorf("NameMap() missing key %q", name)
			continue
		}
		if gotVal != wantVal {
			t.Errorf("NameMap()[%q] = %d, want %d", name, gotVal, wantVal)
		}
	}
}

func TestKeyMap(t *testing.T) {
	m := UNKNOWN.KeyMap()
	if len(m) != 5 {
		t.Fatalf("KeyMap() length = %d, want 5", len(m))
	}

	expectations := map[SdOperationStatus]string{
		UNKNOWN:   "UNKNOWN",
		Submitted: "SUBMITTED",
		Pending:   "PENDING",
		Success:   "SUCCESS",
		Fail:      "FAIL",
	}

	for val, wantKey := range expectations {
		gotKey, ok := m[val]
		if !ok {
			t.Errorf("KeyMap() missing key %d", val)
			continue
		}
		if gotKey != wantKey {
			t.Errorf("KeyMap()[%d] = %q, want %q", val, gotKey, wantKey)
		}
	}
}

func TestCaptionMap(t *testing.T) {
	m := UNKNOWN.CaptionMap()
	if len(m) != 5 {
		t.Fatalf("CaptionMap() length = %d, want 5", len(m))
	}
}

func TestDescriptionMap(t *testing.T) {
	m := UNKNOWN.DescriptionMap()
	if len(m) != 5 {
		t.Fatalf("DescriptionMap() length = %d, want 5", len(m))
	}
}

func TestKeyMethodForInvalidValue(t *testing.T) {
	invalid := SdOperationStatus(99)
	if got := invalid.Key(); got != "" {
		t.Errorf("Key() for invalid value = %q, want empty string", got)
	}
}

func TestCaptionMethodForInvalidValue(t *testing.T) {
	invalid := SdOperationStatus(99)
	if got := invalid.Caption(); got != "" {
		t.Errorf("Caption() for invalid value = %q, want empty string", got)
	}
}

func TestDescriptionMethodForInvalidValue(t *testing.T) {
	invalid := SdOperationStatus(99)
	if got := invalid.Description(); got != "" {
		t.Errorf("Description() for invalid value = %q, want empty string", got)
	}
}
