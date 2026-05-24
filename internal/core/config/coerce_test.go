package config

import (
	"testing"
)

func TestCoerce_bool(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    any
		wantErr string
	}{
		// Python: isistance(True, bool) → returns True
		{"bool_true", true, true, ""},
		{"bool_false", false, false, ""},
		// Python: isinstance(value, str) → value.lower() in ("true", "1", "yes", "on")
		{"str_true", "true", true, ""},
		{"str_TRUE_upper", "TRUE", true, ""},
		{"str_1", "1", true, ""},
		{"str_yes", "yes", true, ""},
		{"str_on", "on", true, ""},
		{"str_false", "false", false, ""},
		{"str_FALSE", "FALSE", false, ""},
		{"str_0", "0", false, ""},
		{"str_no", "no", false, ""},
		{"str_off", "off", false, ""},
		{"str_random", "random", false, ""},
		// Python: coerce does NOT trim spaces — " true" → False
		{"str_spaces_prefix", " true", false, ""},
		{"str_spaces_suffix", "true ", false, ""},
		// Python: int(1) is NOT bool → TypeError
		{"int_1", 1, nil, "Expected bool, got int"},
		{"int_0", 0, nil, "Expected bool, got int"},
		// Python: float64 is not bool → TypeError
		{"float_1", 1.0, nil, "Expected bool, got float"},
		// Python: str is not bool unless string → already handled above
		{"nil", nil, nil, "Expected bool, got nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Coerce(tt.input, "bool")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Coerce(%v, 'bool') = (%v, nil), want error containing %q", tt.input, got, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("Coerce(%v, 'bool') error = %q, want %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Coerce(%v, 'bool') = (_, %v), want nil error", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Coerce(%v, 'bool') = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCoerce_int(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    any
		wantErr string
	}{
		// Python: isinstance(5, int) → returns 5
		{"int_5", 5, 5, ""},
		{"int_0", 0, 0, ""},
		{"int_neg", -3, -3, ""},
		// Python: isinstance("123", str) → int("123") = 123
		{"str_123", "123", 123, ""},
		{"str_0", "0", 0, ""},
		{"str_neg", "-5", -5, ""},
		// Python: int("abc") raises ValueError: invalid literal for int() with base 10: 'abc'
		{"str_invalid", "abc", nil, "invalid literal for int() with base 10: 'abc'"},
		{"str_empty", "", nil, "invalid literal for int() with base 10: ''"},
		// Python: isinstance(42.0, int) → False → TypeError
		{"float64", float64(42), nil, "Expected int, got float"},
		{"float64_neg", float64(-3), nil, "Expected int, got float"},
		{"float64_trunc", float64(3.9), nil, "Expected int, got float"},
		// Python: isinstance("true", int) → False, not str at int → ValueError
		// Already handled by str_invalid
		// Python: isinstance(True, int) → True (bool is subclass of int in Python)
		// Go: return the bool value as-is without converting to int, matching Python behavior
		{"bool_true", true, true, ""},
		{"bool_false", false, false, ""},
		// nil
		{"nil", nil, nil, "Expected int, got nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Coerce(tt.input, "int")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Coerce(%v, 'int') = (%v, nil), want error containing %q", tt.input, got, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("Coerce(%v, 'int') error = %q, want %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Coerce(%v, 'int') = (_, %v), want nil error", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Coerce(%v, 'int') = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCoerce_str(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    any
		wantErr string
	}{
		// Python: isinstance("hello", str) → returns "hello"
		{"str_hello", "hello", "hello", ""},
		{"str_empty", "", "", ""},
		// Python: isinstance(5, str) → False → TypeError
		{"int", 5, nil, "Expected string, got int"},
		{"bool_true", true, nil, "Expected string, got bool"},
		{"float", 3.14, nil, "Expected string, got float"},
		{"nil", nil, nil, "Expected string, got nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Coerce(tt.input, "string")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Coerce(%v, 'string') = (%v, nil), want error containing %q", tt.input, got, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("Coerce(%v, 'string') error = %q, want %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Coerce(%v, 'string') = (_, %v), want nil error", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Coerce(%v, 'string') = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCoerce_float(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    any
		wantErr string
	}{
		// Python: isinstance(3.14, float) → returns 3.14
		{"float_3_14", 3.14, 3.14, ""},
		{"float_0", 0.0, 0.0, ""},
		// Python: isinstance("3.14", str) → float("3.14") = 3.14
		{"str_3_14", "3.14", 3.14, ""},
		{"str_42", "42", 42.0, ""},
		{"str_invalid", "abc", nil, "could not convert string to float: 'abc'"},
		// Python: isinstance(5, float) → False → TypeError
		{"int", 5, nil, "Expected float, got int"},
		// Python: isinstance(True, float) → False
		{"bool_true", true, nil, "Expected float, got bool"},
		{"nil", nil, nil, "Expected float, got nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Coerce(tt.input, "float")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Coerce(%v, 'float') = (%v, nil), want error containing %q", tt.input, got, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("Coerce(%v, 'float') error = %q, want %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Coerce(%v, 'float') = (_, %v), want nil error", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Coerce(%v, 'float') = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCoerce_nil(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    any
		wantErr string
	}{
		{"nil", nil, nil, ""},
		{"not_nil", "hello", nil, "Expected nil, got string"},
		{"int_5", 5, nil, "Expected nil, got int"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Coerce(tt.input, "nil")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Coerce(%v, 'nil') = (%v, nil), want error containing %q", tt.input, got, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("Coerce(%v, 'nil') error = %q, want %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Coerce(%v, 'nil') = (_, %v), want nil error", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("Coerce(%v, 'nil') = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCoerce_dict(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    any
		wantErr string
	}{
		// Python: isinstance({"a": 1}, dict) → returns as-is
		{"map", map[string]any{"a": int(1)}, map[string]any{"a": int(1)}, ""},
		// Python: isinstance('{"a": 1}', str) → json.loads
		{"json_str", `{"a": 1}`, map[string]any{"a": float64(1)}, ""},
		// invalid JSON
		{"invalid_json", `{bad}`, nil, "invalid JSON dict value: {bad}"},
		// wrong type
		{"int", 5, nil, "Expected map, got int"},
		{"bool", true, nil, "Expected map, got bool"},
		{"nil", nil, nil, "Expected map, got nil"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Coerce(tt.input, "map")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Coerce(%v, 'map') = (%v, nil), want error containing %q", tt.input, got, tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Errorf("Coerce(%v, 'map') error = %q, want %q", tt.input, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Coerce(%v, 'map') = (_, %v), want nil error", tt.input, err)
			}
			// For map[string]any, compare deeply
			gotMap, _ := got.(map[string]any)
			wantMap, _ := tt.want.(map[string]any)
			if len(gotMap) != len(wantMap) {
				t.Errorf("Coerce(%v, 'map') = %v, want %v", tt.input, got, tt.want)
			}
			for k, v := range wantMap {
				if gotMap[k] != v {
					t.Errorf("Coerce(%v, 'map') = %v, want %v", tt.input, got, tt.want)
				}
			}
		})
	}
}

func TestCoerce_unknownType(t *testing.T) {
	// Unknown type names should return value as-is
	got, err := Coerce(42, "unknown_type")
	if err != nil {
		t.Fatalf("Coerce(42, 'unknown_type') error = %v, want nil", err)
	}
	if got != 42 {
		t.Errorf("Coerce(42, 'unknown_type') = %v, want 42", got)
	}
}

func TestTypeError(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		got      any
		want     string
	}{
		{"int_to_bool", "bool", 42, "Expected bool, got int"},
		{"str_to_int", "int", "hello", "Expected int, got string"},
		{"bool_to_str", "string", true, "Expected string, got bool"},
		{"float_to_bool", "bool", 3.14, "Expected bool, got float"},
		{"nil_to_bool", "bool", nil, "Expected bool, got nil"},
		{"map_to_bool", "bool", map[string]any{}, "Expected bool, got map"},
		{"list_to_bool", "bool", []any{1, 2}, "Expected bool, got slice"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := typeError(tt.expected, tt.got)
			if err == nil {
				t.Fatalf("typeError(%q, %v) = nil, want %q", tt.expected, tt.got, tt.want)
			}
			if err.Error() != tt.want {
				t.Errorf("typeError(%q, %v) = %q, want %q", tt.expected, tt.got, err.Error(), tt.want)
			}
		})
	}
}

func TestGoTypeName(t *testing.T) {
	tests := []struct {
		name  string
		input any
		want  string
	}{
		{"nil", nil, "nil"},
		{"bool_true", true, "bool"},
		{"bool_false", false, "bool"},
		{"int", 42, "int"},
		{"int64", int64(42), "int"},
		{"float32", float32(3.14), "float"},
		{"float64", 3.14, "float"},
		{"string", "hello", "string"},
		{"map", map[string]any{}, "map"},
		{"slice", []any{1, 2}, "slice"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := goTypeName(tt.input)
			if got != tt.want {
				t.Errorf("goTypeName(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
