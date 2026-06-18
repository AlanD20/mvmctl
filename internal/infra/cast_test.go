package infra_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
)

// --- ToString ---

func TestToString(t *testing.T) {
	tests := map[string]struct {
		input      any
		defaultVal string
		want       string
	}{
		"string_value":              {input: "hello", defaultVal: "fallback", want: "hello"},
		"empty_string":              {input: "", defaultVal: "fallback", want: ""},
		"int_value":                 {input: 42, defaultVal: "fallback", want: "fallback"},
		"nil_value":                 {input: nil, defaultVal: "default", want: "default"},
		"bool_value":                {input: true, defaultVal: "nope", want: "nope"},
		"empty_default":             {input: 99, defaultVal: "", want: ""},
		"tricky_string":             {input: "42", defaultVal: "x", want: "42"},
		"empty_input_empty_default": {input: nil, defaultVal: "", want: ""},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.ToString(tc.input, tc.defaultVal)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// --- ToInt ---

func TestToInt(t *testing.T) {
	tests := map[string]struct {
		input      any
		defaultVal int
		want       int
	}{
		// Happy paths
		"int_direct":             {input: 42, defaultVal: 0, want: 42},
		"int64_value":            {input: int64(42), defaultVal: 0, want: 42},
		"float64_value":          {input: float64(3.14), defaultVal: 0, want: 3},
		"float64_truncates_down": {input: float64(3.99), defaultVal: 0, want: 3},
		"string_numeric":         {input: "42", defaultVal: 0, want: 42},
		"string_zero":            {input: "0", defaultVal: 99, want: 0},
		"string_negative":        {input: "-5", defaultVal: 0, want: -5},

		// Edge cases — fallback to default
		"nil":                       {input: nil, defaultVal: -1, want: -1},
		"string_non_numeric":        {input: "abc", defaultVal: 99, want: 99},
		"string_empty":              {input: "", defaultVal: 99, want: 99},
		"bool_value":                {input: true, defaultVal: 99, want: 99},
		"slice_value":               {input: []int{1, 2, 3}, defaultVal: 99, want: 99},
		"negative_default_returned": {input: "abc", defaultVal: -1, want: -1},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.ToInt(tc.input, tc.defaultVal)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// --- ToBool ---

func TestToBool(t *testing.T) {
	tests := map[string]struct {
		input      any
		defaultVal bool
		want       bool
	}{
		// Happy paths
		"bool_true":            {input: true, defaultVal: false, want: true},
		"bool_false":           {input: false, defaultVal: true, want: false},
		"string_true":          {input: "true", defaultVal: false, want: true},
		"string_false":         {input: "false", defaultVal: true, want: false},
		"string_1":             {input: "1", defaultVal: false, want: true},
		"string_0":             {input: "0", defaultVal: true, want: false},
		"int_one_true":         {input: 1, defaultVal: false, want: true},
		"int_zero_false":       {input: 0, defaultVal: true, want: false},
		"float64_nonzero_true": {input: float64(3.14), defaultVal: false, want: true},
		"float64_zero_false":   {input: float64(0), defaultVal: true, want: false},

		// Edge cases — fallback to default
		"nil":               {input: nil, defaultVal: true, want: true},
		"nil_false_default": {input: nil, defaultVal: false, want: false},
		"string_invalid":    {input: "maybe", defaultVal: true, want: true},
		"empty_string":      {input: "", defaultVal: false, want: false},
		"slice_value":       {input: []string{"a"}, defaultVal: true, want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.ToBool(tc.input, tc.defaultVal)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// --- BoolToInt ---

func TestBoolToInt(t *testing.T) {
	tests := map[string]struct {
		input bool
		want  int
	}{
		"true_returns_1":  {input: true, want: 1},
		"false_returns_0": {input: false, want: 0},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.BoolToInt(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// --- DerefOrZero ---

func TestDerefOrZero(t *testing.T) {
	t.Run("int_non_nil", func(t *testing.T) {
		v := 42
		got := infra.DerefOrZero(&v)
		assert.Equal(t, 42, got)
	})

	t.Run("int_nil", func(t *testing.T) {
		got := infra.DerefOrZero[int](nil)
		assert.Equal(t, 0, got)
	})

	t.Run("string_non_nil", func(t *testing.T) {
		v := "hello"
		got := infra.DerefOrZero(&v)
		assert.Equal(t, "hello", got)
	})

	t.Run("string_nil", func(t *testing.T) {
		got := infra.DerefOrZero[string](nil)
		assert.Equal(t, "", got)
	})

	t.Run("bool_non_nil", func(t *testing.T) {
		v := true
		got := infra.DerefOrZero(&v)
		assert.Equal(t, true, got)
	})

	t.Run("bool_nil", func(t *testing.T) {
		got := infra.DerefOrZero[bool](nil)
		assert.Equal(t, false, got)
	})

	t.Run("struct_non_nil", func(t *testing.T) {
		type s struct{ X int }
		v := s{X: 99}
		got := infra.DerefOrZero(&v)
		assert.Equal(t, s{X: 99}, got)
	})

	t.Run("struct_nil_returns_zero", func(t *testing.T) {
		type s struct{ X int }
		got := infra.DerefOrZero[*s](nil)
		assert.Nil(t, got)
	})
}

// --- DerefOrNil ---

func TestDerefOrNil(t *testing.T) {
	t.Run("int_non_nil", func(t *testing.T) {
		v := 42
		got := infra.DerefOrNil(&v)
		assert.Equal(t, 42, got)
	})

	t.Run("int_nil", func(t *testing.T) {
		got := infra.DerefOrNil[int](nil)
		assert.Nil(t, got)
	})

	t.Run("string_non_nil", func(t *testing.T) {
		v := "hello"
		got := infra.DerefOrNil(&v)
		assert.Equal(t, "hello", got)
	})

	t.Run("string_nil", func(t *testing.T) {
		got := infra.DerefOrNil[string](nil)
		assert.Nil(t, got)
	})

	t.Run("string_zero_value_not_nil", func(t *testing.T) {
		v := ""
		got := infra.DerefOrNil(&v)
		// nil, not "", because we pass a non-nil pointer
		assert.NotNil(t, got)
		assert.Equal(t, "", got)
	})
}

// --- ShlexQuote ---

func TestShlexQuote(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"empty_string":        {input: "", want: "''"},
		"simple_word":         {input: "hello", want: "hello"},
		"alphanumeric":        {input: "abc123", want: "abc123"},
		"path_with_slashes":   {input: "/usr/local/bin", want: "/usr/local/bin"},
		"mixed_safe_chars":    {input: "user@host:port/path", want: "user@host:port/path"},
		"needs_quoting":       {input: "hello world", want: "'hello world'"},
		"single_quote_inside": {input: "it's", want: `'it'"'"'s'`},
		"double_quote_inside": {input: `say "hi"`, want: `'say "hi"'`},
		"special_chars":       {input: "foo$bar", want: "'foo$bar'"},
		"backtick":            {input: "cmd`ls`", want: "'cmd`ls`'"},
		"newline":             {input: "line1\nline2", want: "'line1\nline2'"},
		"multiple_quotes":     {input: `'a'"b`, want: `''"'"'a'"'"'"b'`},
		"dash_prefix":         {input: "--flag=value", want: "--flag=value"},
		"underscore":          {input: "my_var", want: "my_var"},
		"percent":             {input: "100%", want: "100%"},
		"tricky_quotes":       {input: "'", want: `''"'"''`},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.ShlexQuote(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}

// --- NonZero ---

func TestNonZero(t *testing.T) {
	tests := map[string]struct {
		value    any // stored as any to test different types
		fallback any
		want     any
	}{
		"int_nonzero":    {value: 42, fallback: 0, want: 42},
		"int_zero":       {value: 0, fallback: 99, want: 99},
		"string_nonzero": {value: "hello", fallback: "default", want: "hello"},
		"string_empty":   {value: "", fallback: "default", want: "default"},
		"bool_true":      {value: true, fallback: false, want: true},
		"bool_false":     {value: false, fallback: true, want: true},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			// Use type switch to call the right instantiation
			switch v := tc.value.(type) {
			case int:
				f := tc.fallback.(int)
				got := infra.NonZero(v, f)
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Errorf("NonZero[int]() (-want +got):\n%s", diff)
				}
			case string:
				f := tc.fallback.(string)
				got := infra.NonZero(v, f)
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Errorf("NonZero[string]() (-want +got):\n%s", diff)
				}
			case bool:
				f := tc.fallback.(bool)
				got := infra.NonZero(v, f)
				if diff := cmp.Diff(tc.want, got); diff != "" {
					t.Errorf("NonZero[bool]() (-want +got):\n%s", diff)
				}
			default:
				t.Fatalf("unhandled type %T in test case %s", tc.value, name)
			}
		})
	}
}

// --- MapToStruct ---

type testMapStruct struct {
	Name  string `json:"name"`
	Value int    `json:"value"`
}

func TestMapToStruct(t *testing.T) {
	t.Run("valid_map", func(t *testing.T) {
		input := map[string]any{"name": "test", "value": 42}
		got := infra.MapToStruct[testMapStruct](input)
		require.NotNil(t, got)
		want := &testMapStruct{Name: "test", Value: 42}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("nil_input", func(t *testing.T) {
		got := infra.MapToStruct[testMapStruct](nil)
		assert.Nil(t, got)
	})

	t.Run("empty_map", func(t *testing.T) {
		input := map[string]any{}
		got := infra.MapToStruct[testMapStruct](input)
		require.NotNil(t, got)
		want := &testMapStruct{}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("partial_map", func(t *testing.T) {
		input := map[string]any{"name": "partial"}
		got := infra.MapToStruct[testMapStruct](input)
		require.NotNil(t, got)
		want := &testMapStruct{Name: "partial", Value: 0}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("map_with_extra_fields", func(t *testing.T) {
		input := map[string]any{"name": "test", "value": 10, "ignored": "field"}
		got := infra.MapToStruct[testMapStruct](input)
		require.NotNil(t, got)
		want := &testMapStruct{Name: "test", Value: 10}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})

	t.Run("map_with_invalid_json_types", func(t *testing.T) {
		// Function handles this via JSON marshal/unmarshal
		input := map[string]any{"name": "test", "value": "not_a_number"}
		got := infra.MapToStruct[testMapStruct](input)
		// JSON unmarshal fails silently, returns nil
		assert.Nil(t, got)
	})

	t.Run("struct_with_nested_map", func(t *testing.T) {
		type nested struct {
			Items []string `json:"items"`
		}
		input := map[string]any{"items": []any{"a", "b", "c"}}
		got := infra.MapToStruct[nested](input)
		require.NotNil(t, got)
		want := &nested{Items: []string{"a", "b", "c"}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("(-want +got):\n%s", diff)
		}
	})
}

// --- ToTitle ---

func TestToTitle(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"single_word":             {input: "hello", want: "Hello"},
		"multiple_words":          {input: "hello world", want: "Hello World"},
		"already_capitalized":     {input: "Hello World", want: "Hello World"},
		"empty_string":            {input: "", want: ""},
		"all_lower":               {input: "the quick brown fox", want: "The Quick Brown Fox"},
		"mixed_case":              {input: "hELLO wORLD", want: "HELLO WORLD"},
		"single_letter_words":     {input: "a b c", want: "A B C"},
		"numbers_and_letters":     {input: "hello 2nd world", want: "Hello 2nd World"},
		"leading_trailing_spaces": {input: "  hello world  ", want: "Hello World"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.ToTitle(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("(-want +got):\n%s", diff)
			}
		})
	}
}
