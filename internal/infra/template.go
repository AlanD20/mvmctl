package infra

import (
	"fmt"
	"strings"
)

// RenderTemplate renders a template using Python's str.format(**vars) syntax.
// Variable placeholders use {key} syntax.
// If a variable referenced in the template is missing from vars, an error is returned.
// Supports {{ → { and }} → } escape sequences (like Python's {{ and }}).
// Mirrors Python's mvmctl.utils.template.render_template().
func RenderTemplate(tmpl string, vars map[string]string) (string, error) {
	// Handle {{ and }} escape sequences: replace {{ with sentinel, }} with sentinel
	const (
		braceOpenSentinel  = "\x00OPEN\x00"
		braceCloseSentinel = "\x00CLOSE\x00"
	)
	tmpl = strings.ReplaceAll(tmpl, "{{", braceOpenSentinel)
	tmpl = strings.ReplaceAll(tmpl, "}}", braceCloseSentinel)

	// Build old/new pairs for strings.NewReplacer
	pairs := make([]string, 0, len(vars)*2)
	for k, v := range vars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	replacer := strings.NewReplacer(pairs...)
	result := replacer.Replace(tmpl)

	// After substitution, check for remaining {key} placeholders that were NOT in vars.
	// These are the missing template variables. Python's str.format() raises KeyError
	// for such keys.
	missing := findMissingPlaceholders(result)
	if len(missing) > 0 {
		return "", fmt.Errorf("Missing template variable: %s", missing[0])
	}

	// Restore escaped braces
	result = strings.ReplaceAll(result, braceOpenSentinel, "{")
	result = strings.ReplaceAll(result, braceCloseSentinel, "}")

	return result, nil
}

// findMissingPlaceholders finds all {key} patterns in s.
// Only called AFTER substitution, so any remaining {key} is missing from vars.
func findMissingPlaceholders(s string) []string {
	var missing []string
	i := 0
	for i < len(s) {
		if s[i] == '{' {
			end := strings.IndexByte(s[i:], '}')
			if end != -1 {
				key := s[i+1 : i+end]
				if len(key) > 0 {
					missing = append(missing, key)
				}
				i += end + 1
				continue
			}
		}
		i++
	}
	return missing
}

// Dedent removes common leading whitespace from all non-empty lines.
// Matches Python's textwrap.dedent().
func Dedent(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) == 0 {
		return text
	}

	// Find minimum indentation across non-empty lines.
	minIndent := -1
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if len(trimmed) == 0 {
			continue
		}
		indent := len(line) - len(trimmed)
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}

	if minIndent <= 0 {
		return text
	}

	// Remove indentation from each line.
	var result strings.Builder
	for i, line := range lines {
		if i > 0 {
			result.WriteString("\n")
		}
		if len(line) >= minIndent {
			result.WriteString(line[minIndent:])
		} else {
			result.WriteString(line)
		}
	}
	return result.String()
}

// RenderOptionalTemplate renders a template, propagating errors.
// Python distinguishes None (not provided) from "" (empty string).
// - If tmpl is nil (None in Python), returns nil with no error.
// - If tmpl is "" (empty string in Python), calls RenderTemplate("", vars)
//   which returns "" with no error.
// Mirrors Python's mvmctl.utils.template.render_optional_template().
func RenderOptionalTemplate(tmpl *string, vars map[string]string) (*string, error) {
	if tmpl == nil {
		return nil, nil
	}
	result, err := RenderTemplate(*tmpl, vars)
	if err != nil {
		return nil, err
	}
	return &result, nil
}
