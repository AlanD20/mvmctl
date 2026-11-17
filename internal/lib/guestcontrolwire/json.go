package guestcontrolwire

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"unicode/utf8"

	"mvmctl/pkg/errs"
)

// closedObject checks exact field spelling and presence before decoding into Go structs.
// encoding/json alone accepts duplicate fields, case aliases and null scalar values.
func closedObject(raw []byte, allowed []string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, invalid("Guest Control JSON must use valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, invalid("Guest Control fields must be a JSON object")
	}
	fields := make(map[string]json.RawMessage, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeValidationFailed, "read Guest Control field", err)
		}
		field, ok := token.(string)
		if !ok || !slices.Contains(allowed, field) {
			return nil, invalid("unknown Guest Control field")
		}
		if _, duplicate := fields[field]; duplicate {
			return nil, invalid("duplicate Guest Control field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errs.WrapMsg(errs.CodeValidationFailed, "read Guest Control field value", err)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, invalid("Guest Control fields must not be null")
		}
		fields[field] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "close Guest Control object", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, invalid("Guest Control object contains trailing JSON")
	}
	if len(fields) != len(allowed) {
		return nil, invalid("Guest Control object is missing a required field")
	}
	return fields, nil
}
