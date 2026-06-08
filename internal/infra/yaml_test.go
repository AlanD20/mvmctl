package infra_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
)

// ─── RequireString ───────────────────────────────────────────────────────────
// Rationale: RequireString is the primary YAML field extraction for required
// fields. Missing fields or type mismatches must produce clear error messages
// for debugging misconfigured YAML assets.

func TestRequireString(t *testing.T) {
	tests := map[string]struct {
		data map[string]any
		key  string
		want string
		err  string
	}{
		"found_string": {
			data: map[string]any{"name": "test-vm"},
			key:  "name",
			want: "test-vm",
		},
		"empty_value_valid": {
			data: map[string]any{"name": ""},
			key:  "name",
			want: "",
		},
		"missing_key": {
			data: map[string]any{},
			key:  "name",
			err:  "is required",
		},
		"wrong_type_int": {
			data: map[string]any{"count": 42},
			key:  "count",
			err:  "must be a string",
		},
		"wrong_type_bool": {
			data: map[string]any{"enabled": true},
			key:  "enabled",
			err:  "must be a string",
		},
		"nil_value": {
			data: map[string]any{"name": nil},
			key:  "name",
			err:  "must be a string",
		},
		"empty_data": {
			data: map[string]any{},
			key:  "x",
			err:  "is required",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := infra.RequireString(tc.data, tc.key)

			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("RequireString() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── OptionalString ─────────────────────────────────────────────────────────
// Rationale: OptionalString extracts optional fields from YAML assets without
// erroring on missing keys. Must return nil for absent/mismatched fields to
// distinguish "not provided" from "empty string".

func TestOptionalString(t *testing.T) {
	t.Run("found_string", func(t *testing.T) {
		data := map[string]any{"desc": "hello"}
		got := infra.OptionalString(data, "desc")
		require.NotNil(t, got)
		assert.Equal(t, "hello", *got)
	})

	t.Run("missing_key_returns_nil", func(t *testing.T) {
		data := map[string]any{}
		got := infra.OptionalString(data, "desc")
		assert.Nil(t, got)
	})

	t.Run("empty_value_returns_pointer", func(t *testing.T) {
		data := map[string]any{"desc": ""}
		got := infra.OptionalString(data, "desc")
		require.NotNil(t, got)
		assert.Equal(t, "", *got)
	})

	t.Run("wrong_type_returns_nil", func(t *testing.T) {
		data := map[string]any{"desc": 42}
		got := infra.OptionalString(data, "desc")
		assert.Nil(t, got)
	})

	t.Run("nil_value_returns_nil", func(t *testing.T) {
		data := map[string]any{"desc": nil}
		got := infra.OptionalString(data, "desc")
		assert.Nil(t, got)
	})
}

// ─── OptionalInt ─────────────────────────────────────────────────────────────
// Rationale: OptionalInt extracts optional integer fields from YAML.
// Must accept bool (Python compat: isinstance(True, int) is True) but reject
// float64 (isinstance(42.0, int) is False in Python).

func TestOptionalInt(t *testing.T) {
	t.Run("found_int", func(t *testing.T) {
		data := map[string]any{"count": 42}
		got := infra.OptionalInt(data, "count")
		require.NotNil(t, got)
		assert.Equal(t, 42, *got)
	})

	t.Run("missing_key_returns_nil", func(t *testing.T) {
		data := map[string]any{}
		got := infra.OptionalInt(data, "count")
		assert.Nil(t, got)
	})

	t.Run("bool_true_becomes_1", func(t *testing.T) {
		// Python compat: isinstance(True, int) is True → True == 1
		data := map[string]any{"count": true}
		got := infra.OptionalInt(data, "count")
		require.NotNil(t, got)
		assert.Equal(t, 1, *got)
	})

	t.Run("bool_false_becomes_0", func(t *testing.T) {
		// Python compat: isinstance(False, int) is True → False == 0
		data := map[string]any{"count": false}
		got := infra.OptionalInt(data, "count")
		require.NotNil(t, got)
		assert.Equal(t, 0, *got)
	})

	t.Run("float64_returns_nil", func(t *testing.T) {
		// Python compat: isinstance(42.0, int) is False
		data := map[string]any{"count": float64(42.0)}
		got := infra.OptionalInt(data, "count")
		assert.Nil(t, got)
	})

	t.Run("string_returns_nil", func(t *testing.T) {
		data := map[string]any{"count": "42"}
		got := infra.OptionalInt(data, "count")
		assert.Nil(t, got)
	})

	t.Run("zero_int_valid", func(t *testing.T) {
		data := map[string]any{"count": 0}
		got := infra.OptionalInt(data, "count")
		require.NotNil(t, got)
		assert.Equal(t, 0, *got)
	})
}

// ─── RequireStrList ──────────────────────────────────────────────────────────
// Rationale: RequireStrList extracts string list fields from YAML. Missing
// keys default to empty list (not an error). Type mismatches must be caught.

func TestRequireStrList(t *testing.T) {
	tests := map[string]struct {
		data map[string]any
		key  string
		want []string
		err  string
	}{
		"found_list": {
			data: map[string]any{"items": []any{"a", "b", "c"}},
			key:  "items",
			want: []string{"a", "b", "c"},
		},
		"missing_key_is_empty": {
			data: map[string]any{},
			key:  "items",
			want: []string{},
		},
		"empty_list": {
			data: map[string]any{"items": []any{}},
			key:  "items",
			want: []string{},
		},
		"single_element": {
			data: map[string]any{"items": []any{"only"}},
			key:  "items",
			want: []string{"only"},
		},
		"not_a_list_errors": {
			data: map[string]any{"items": "nope"},
			key:  "items",
			err:  "must be a list of strings",
		},
		"non_string_element_errors": {
			data: map[string]any{"items": []any{"a", 42, "c"}},
			key:  "items",
			err:  "must be a list of strings",
		},
		"nil_value": {
			data: map[string]any{"items": nil},
			key:  "items",
			err:  "must be a list of strings",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := infra.RequireStrList(tc.data, tc.key)

			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("RequireStrList() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// ─── ParseSetValList ─────────────────────────────────────────────────────────
// Rationale: ParseSetValList parses kernel config option/value pairs from YAML.
// Two formats accepted: map entries {"option": "x", "value": "y"} and
// two-element lists ["x", "y"]. Errors on invalid format.

func TestParseSetValList(t *testing.T) {
	t.Run("missing_key_returns_nil", func(t *testing.T) {
		got, err := infra.ParseSetValList(map[string]any{}, "config")
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("list_of_map_entries", func(t *testing.T) {
		data := map[string]any{
			"config": []any{
				map[string]any{"option": "CONFIG_A", "value": "y"},
				map[string]any{"option": "CONFIG_B", "value": "n"},
			},
		}
		got, err := infra.ParseSetValList(data, "config")
		require.NoError(t, err)
		want := []infra.SetValEntry{
			{Option: "CONFIG_A", Value: "y"},
			{Option: "CONFIG_B", Value: "n"},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("list_of_two_element_lists", func(t *testing.T) {
		data := map[string]any{
			"config": []any{
				[]any{"CONFIG_A", "y"},
				[]any{"CONFIG_B", "n"},
			},
		}
		got, err := infra.ParseSetValList(data, "config")
		require.NoError(t, err)
		want := []infra.SetValEntry{
			{Option: "CONFIG_A", Value: "y"},
			{Option: "CONFIG_B", Value: "n"},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("mixed_entries", func(t *testing.T) {
		data := map[string]any{
			"config": []any{
				map[string]any{"option": "CONFIG_A", "value": "y"},
				[]any{"CONFIG_B", "n"},
			},
		}
		got, err := infra.ParseSetValList(data, "config")
		require.NoError(t, err)
		want := []infra.SetValEntry{
			{Option: "CONFIG_A", Value: "y"},
			{Option: "CONFIG_B", Value: "n"},
		}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("not_a_list_errors", func(t *testing.T) {
		data := map[string]any{"config": "invalid"}
		_, err := infra.ParseSetValList(data, "config")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a list")
	})

	t.Run("wrong_entry_type_errors", func(t *testing.T) {
		data := map[string]any{
			"config": []any{"not-a-map-or-list"},
		}
		_, err := infra.ParseSetValList(data, "config")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be {option, value}")
	})

	t.Run("list_wrong_length_errors", func(t *testing.T) {
		data := map[string]any{
			"config": []any{
				[]any{"only_one"},
			},
		}
		_, err := infra.ParseSetValList(data, "config")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must have exactly 2 elements")
	})

	t.Run("map_missing_option_field_errors", func(t *testing.T) {
		data := map[string]any{
			"config": []any{
				map[string]any{"value": "y"},
			},
		}
		_, err := infra.ParseSetValList(data, "config")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "option")
	})

	t.Run("map_missing_value_field_errors", func(t *testing.T) {
		data := map[string]any{
			"config": []any{
				map[string]any{"option": "CONFIG_A"},
			},
		}
		_, err := infra.ParseSetValList(data, "config")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "value")
	})

	t.Run("empty_list", func(t *testing.T) {
		data := map[string]any{"config": []any{}}
		got, err := infra.ParseSetValList(data, "config")
		require.NoError(t, err)
		assert.Empty(t, got)
	})
}
