package vmexecwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"mvmctl/pkg/errs"
)

const (
	maxJSONDepth  = 8
	maxJSONFields = 8
)

func validateStrictJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return validationError("VM-exec JSON must use valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		if errs.AsDomainError(err) != nil {
			return err
		}
		return errs.WrapMsg(errs.CodeValidationFailed, "decode VM-exec JSON", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return validationError("VM-exec frame contains trailing JSON")
		}
		return errs.WrapMsg(errs.CodeValidationFailed, "decode trailing VM-exec JSON", err)
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= maxJSONDepth {
		return validationError("VM-exec JSON exceeds nesting limit")
	}
	switch delimiter {
	case '{':
		return scanJSONObject(decoder, depth)
	case '[':
		return scanJSONArray(decoder, depth)
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func scanJSONObject(decoder *json.Decoder, depth int) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		if len(seen) >= maxJSONFields {
			return validationError("VM-exec JSON object exceeds field limit")
		}
		fieldToken, err := decoder.Token()
		if err != nil {
			return err
		}
		field, ok := fieldToken.(string)
		if !ok {
			return fmt.Errorf("JSON object field is not a string")
		}
		if _, exists := seen[field]; exists {
			return validationError(fmt.Sprintf("duplicate JSON field %q in VM-exec frame", field))
		}
		for existing := range seen {
			if strings.EqualFold(existing, field) {
				return validationError(fmt.Sprintf(
					"case-colliding JSON field %q conflicts with %q in VM-exec frame",
					field,
					existing,
				))
			}
		}
		seen[field] = struct{}{}
		if err := scanJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func scanJSONArray(decoder *json.Decoder, depth int) error {
	for decoder.More() {
		if err := scanJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func decodeClosedObject(raw []byte, allowed []string, description string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "decode VM-exec "+description, err)
	}
	if fields == nil {
		return nil, validationError("VM-exec " + description + " must be a JSON object")
	}
	if len(fields) != len(allowed) {
		for _, field := range allowed {
			if _, ok := fields[field]; !ok {
				return nil, validationError(fmt.Sprintf(
					"VM-exec %s is missing required field %q",
					description,
					field,
				))
			}
		}
	}
	for field := range fields {
		if !containsField(allowed, field) {
			return nil, validationError(fmt.Sprintf("unknown JSON field %q in VM-exec %s", field, description))
		}
	}
	for _, field := range allowed {
		if _, ok := fields[field]; !ok {
			return nil, validationError(fmt.Sprintf(
				"VM-exec %s is missing required field %q",
				description,
				field,
			))
		}
	}
	return fields, nil
}

func decodeField[T any](raw json.RawMessage, description string) (T, error) {
	var zero T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, errs.WrapMsg(errs.CodeValidationFailed, "decode VM-exec "+description, err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return zero, validationError("VM-exec " + description + " contains trailing JSON")
		}
		return zero, errs.WrapMsg(errs.CodeValidationFailed, "decode trailing VM-exec "+description, err)
	}
	return value, nil
}

func containsField(fields []string, candidate string) bool {
	for _, field := range fields {
		if field == candidate {
			return true
		}
	}
	return false
}

func validText(value string, maxBytes int, allowLineBreaks bool) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if (character == '\n' || character == '\t') && allowLineBreaks {
			continue
		}
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			return false
		}
	}
	return true
}
