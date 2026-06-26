package infra_test

import (
	"testing"
	"text/template"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
)

// --- RenderTemplate ---
// Rationale: RenderTemplate is used for cloud-init templates, config files,
// and user-facing output formatting. Missing var detection prevents silent
// generation of broken configs.

func TestRenderTemplate(t *testing.T) {
	tests := map[string]struct {
		tmpl string
		vars map[string]string
		want string
		err  string // empty = no error
	}{
		"simple_substitution": {
			tmpl: "Hello {name}!",
			vars: map[string]string{"name": "World"},
			want: "Hello World!",
		},
		"multiple_vars": {
			tmpl: "{greeting}, {name}!",
			vars: map[string]string{"greeting": "Hello", "name": "World"},
			want: "Hello, World!",
		},
		"adjacent_vars": {
			tmpl: "{a}{b}",
			vars: map[string]string{"a": "x", "b": "y"},
			want: "xy",
		},
		"repeated_var": {
			tmpl: "{x} + {x} = {y}",
			vars: map[string]string{"x": "1", "y": "2"},
			want: "1 + 1 = 2",
		},
		"empty_template": {
			tmpl: "",
			vars: map[string]string{"k": "v"},
			want: "",
		},
		"no_vars_needed": {
			tmpl: "static text",
			vars: map[string]string{},
			want: "static text",
		},
		"extra_vars_ignored": {
			tmpl: "{a}",
			vars: map[string]string{"a": "1", "b": "2"},
			want: "1",
		},
		"escaped_braces": {
			tmpl: "{{not_a_var}}",
			vars: map[string]string{},
			want: "{not_a_var}",
		},
		"mixed_escaped_and_real": {
			tmpl: "{{escaped}} {real}",
			vars: map[string]string{"real": "value"},
			want: "{escaped} value",
		},
		"value_with_special_chars": {
			tmpl: "path={p}",
			vars: map[string]string{"p": "/usr/local/bin"},
			want: "path=/usr/local/bin",
		},

		// Error cases
		"missing_var_returns_error": {
			tmpl: "Hello {name}!",
			vars: map[string]string{},
			err:  "Missing template variable",
		},
		"partially_missing_var": {
			tmpl: "{a}-{b}",
			vars: map[string]string{"a": "x"},
			err:  "Missing template variable",
		},
		"empty_placeholder": {
			tmpl: "{}",
			vars: map[string]string{},
			want: "{}",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := infra.RenderTemplate(tc.tmpl, tc.vars)

			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("RenderTemplate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- Dedent ---
// Rationale: Dedent is used for template strings and configuration text.
// Incorrect dedentation would produce misaligned config files or template errors.

func TestDedent(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"already_dedented": {
			input: "line1\nline2\nline3",
			want:  "line1\nline2\nline3",
		},
		"remove_leading_spaces": {
			input: "    line1\n    line2\n    line3",
			want:  "line1\nline2\nline3",
		},
		"varying_indent_takes_minimum": {
			input: "    line1\n        line2\n    line3",
			want:  "line1\n    line2\nline3",
		},
		"empty_lines_ignored": {
			input: "    line1\n\n    line3",
			want:  "line1\n\nline3",
		},
		"mixed_tabs_and_spaces": {
			input: "\t\tline1\n\t\t\tline2",
			want:  "line1\n\tline2",
		},
		"single_line": {
			input: "  hello",
			want:  "hello",
		},
		"no_indentation": {
			input: "line1\nline2",
			want:  "line1\nline2",
		},
		"empty_string": {
			input: "",
			want:  "",
		},
		"only_whitespace_lines": {
			input: "   \n    \n  ",
			want:  "   \n    \n  ",
		},
		"trailing_newline": {
			input: "  line1\n  line2\n",
			want:  "line1\nline2\n",
		},
		"one_line_less_indent_than_others": {
			input: "    a\n  b\n    c",
			want:  "  a\nb\n  c",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := infra.Dedent(tc.input)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Dedent() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- RenderOptionalTemplate ---
// Rationale: Wraps RenderTemplate with nil-safe optional template handling.
// Used in config fields that may or may not have template content.

func TestRenderOptionalTemplate(t *testing.T) {
	t.Run("nil_template_returns_nil", func(t *testing.T) {
		got, err := infra.RenderOptionalTemplate(nil, nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("valid_template", func(t *testing.T) {
		tmpl := "Hello {name}!"
		vars := map[string]string{"name": "World"}
		got, err := infra.RenderOptionalTemplate(&tmpl, vars)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "Hello World!", *got)
	})

	t.Run("missing_var_propagates_error", func(t *testing.T) {
		tmpl := "Hello {name}!"
		_, err := infra.RenderOptionalTemplate(&tmpl, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Missing template variable")
		return
	})
}

// --- ExecTemplate ---
// Rationale: ExecTemplate executes Go text/template and panics on failure.
// Used for critical template rendering where errors must be caught early.

func TestExecTemplate(t *testing.T) {
	t.Run("valid_template", func(t *testing.T) {
		tmpl := template.Must(template.New("test").Parse("Hello {{.Name}}!"))
		data := struct{ Name string }{"World"}
		got := infra.ExecTemplate(tmpl, data)
		assert.Equal(t, "Hello World!", got)
	})

	t.Run("empty_template", func(t *testing.T) {
		tmpl := template.Must(template.New("empty").Parse(""))
		got := infra.ExecTemplate(tmpl, nil)
		assert.Equal(t, "", got)
	})

	t.Run("template_with_multiple_fields", func(t *testing.T) {
		tmpl := template.Must(template.New("multi").Parse("{{.A}}-{{.B}}"))
		data := struct{ A, B string }{"x", "y"}
		got := infra.ExecTemplate(tmpl, data)
		assert.Equal(t, "x-y", got)
	})

	t.Run("template_with_conditionals", func(t *testing.T) {
		tmpl := template.Must(template.New("cond").Parse("{{if .Show}}{{.Val}}{{end}}"))
		data := struct {
			Show bool
			Val  string
		}{true, "visible"}
		got := infra.ExecTemplate(tmpl, data)
		assert.Equal(t, "visible", got)
	})

	t.Run("panics_on_type_mismatch", func(t *testing.T) {
		tmpl := template.Must(template.New("bad").Parse("{{.Missing}}"))
		data := struct{}{}
		assert.Panics(t, func() {
			infra.ExecTemplate(tmpl, data)
		})
	})
}
