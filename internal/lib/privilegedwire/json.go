package privilegedwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"mvmctl/pkg/errs"
)

const (
	maxJSONDepth        = 32
	maxJSONObjectFields = 64
	maxJSONArrayItems   = 1024
)

func validateStrictJSON(raw []byte) error {
	if !utf8.Valid(raw) {
		return errs.New(errs.CodeValidationFailed, "privileged JSON must use valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		if errs.AsDomainError(err) != nil {
			return err
		}
		return errs.WrapMsg(errs.CodeValidationFailed, "decode privileged JSON", err)
	}
	return requireJSONEOF(decoder)
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
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("privileged JSON nesting exceeds %d levels", maxJSONDepth),
		)
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
	fieldCount := 0
	for decoder.More() {
		fieldCount++
		if fieldCount > maxJSONObjectFields {
			return errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("privileged JSON object exceeds %d fields", maxJSONObjectFields),
			)
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
			return errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("duplicate JSON field %q in privileged frame", field),
			)
		}
		for existing := range seen {
			if strings.EqualFold(existing, field) {
				return errs.New(
					errs.CodeValidationFailed,
					fmt.Sprintf(
						"case-folded duplicate JSON field %q conflicts with %q in privileged frame",
						field,
						existing,
					),
				)
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
	itemCount := 0
	for decoder.More() {
		itemCount++
		if itemCount > maxJSONArrayItems {
			return errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("privileged JSON array exceeds %d items", maxJSONArrayItems),
			)
		}
		if err := scanJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func decodeExactObject(
	raw []byte,
	allowed map[string]struct{},
	required []string,
	description string,
) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "decode privileged "+description, err)
	}
	if fields == nil {
		return nil, errs.New(errs.CodeValidationFailed, "privileged "+description+" must be a JSON object")
	}
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			return nil, errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("unknown JSON field %q in privileged %s", field, description),
			)
		}
	}
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return nil, errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("privileged %s is missing required field %q", description, field),
			)
		}
	}
	return fields, nil
}

func decodeJSONField[T any](raw json.RawMessage, description string) (T, error) {
	var zero T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var value T
	if err := decoder.Decode(&value); err != nil {
		return zero, errs.WrapMsg(
			errs.CodeValidationFailed,
			"decode privileged "+description+": "+err.Error(),
			err,
		)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return zero, err
	}
	return value, nil
}

func decodeCanonicalJSONField[T any](raw json.RawMessage, description string) (T, error) {
	var zero T
	value, err := decodeJSONField[T](raw, description)
	if err != nil {
		return zero, err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return zero, errs.WrapMsg(errs.CodeValidationFailed, "encode canonical privileged "+description, err)
	}
	if err := requireCanonicalJSONMemberNames(raw, canonical, description); err != nil {
		return zero, err
	}
	return value, nil
}

func requireCanonicalJSONMemberNames(raw json.RawMessage, canonical json.RawMessage, description string) error {
	raw = bytes.TrimSpace(raw)
	canonical = bytes.TrimSpace(canonical)
	if len(raw) == 0 || len(canonical) == 0 {
		return errs.New(errs.CodeValidationFailed, "privileged "+description+" is empty")
	}
	if raw[0] == '{' && canonical[0] == '{' {
		return requireCanonicalJSONObjectNames(raw, canonical, description)
	}
	if raw[0] == '[' && canonical[0] == '[' {
		return requireCanonicalJSONArrayNames(raw, canonical, description)
	}
	return nil
}

func requireCanonicalJSONObjectNames(raw json.RawMessage, canonical json.RawMessage, description string) error {
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawFields); err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "decode privileged "+description, err)
	}
	var canonicalFields map[string]json.RawMessage
	if err := json.Unmarshal(canonical, &canonicalFields); err != nil {
		return errs.WrapMsg(errs.CodeInternal, "decode canonical privileged "+description, err)
	}
	for field, rawValue := range rawFields {
		canonicalValue, ok := canonicalFields[field]
		if !ok {
			return errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("unknown JSON field %q in privileged %s", field, description),
			)
		}
		if err := requireCanonicalJSONMemberNames(rawValue, canonicalValue, description); err != nil {
			return err
		}
	}
	for field := range canonicalFields {
		if _, ok := rawFields[field]; !ok {
			return errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("privileged %s is missing required field %q", description, field),
			)
		}
	}
	return nil
}

func requireCanonicalJSONArrayNames(raw json.RawMessage, canonical json.RawMessage, description string) error {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(raw, &rawItems); err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "decode privileged "+description, err)
	}
	var canonicalItems []json.RawMessage
	if err := json.Unmarshal(canonical, &canonicalItems); err != nil {
		return errs.WrapMsg(errs.CodeInternal, "decode canonical privileged "+description, err)
	}
	if len(rawItems) != len(canonicalItems) {
		return errs.New(errs.CodeValidationFailed, "privileged "+description+" changed array shape during decoding")
	}
	for index := range rawItems {
		if err := requireCanonicalJSONMemberNames(rawItems[index], canonicalItems[index], description); err != nil {
			return err
		}
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errs.New(errs.CodeValidationFailed, "privileged frame contains trailing JSON")
		}
		return errs.WrapMsg(errs.CodeValidationFailed, "decode trailing privileged JSON", err)
	}
	return nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func fieldSet(names ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}
