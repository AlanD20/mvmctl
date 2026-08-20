package privileged

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	markerPrefix    = "__mvm_privileged_v"
	maxActionBytes  = 64
	maxRequestBytes = 64 * 1024
	maxJSONDepth    = 32

	maxJSONArrayItems   = 1024
	maxJSONObjectFields = 64
)

// Marker selects the current internal privileged protocol before normal CLI initialization.
const Marker = infra.PrivilegedProtocolMarker

type invocation struct {
	action string
}

// IsInvocation reports whether args select the reserved privileged protocol namespace.
// Unsupported versions stay in this namespace so they cannot fall through to the public CLI.
func IsInvocation(args []string) bool {
	return len(args) > 0 && strings.HasPrefix(args[0], markerPrefix)
}

func parseInvocation(args []string) (invocation, error) {
	if len(args) == 0 || args[0] != Marker {
		return invocation{}, errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("unsupported privileged protocol; ask an administrator to upgrade %s", infra.SystemBinaryPath),
		)
	}
	if len(args) != 2 {
		return invocation{}, errs.New(
			errs.CodeValidationFailed,
			"privileged protocol requires exactly one action",
		)
	}
	if args[1] == "" {
		return invocation{}, errs.New(errs.CodeValidationFailed, "privileged action is required")
	}
	if len(args[1]) > maxActionBytes {
		return invocation{}, errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("privileged action exceeds %d bytes", maxActionBytes),
		)
	}
	return invocation{action: args[1]}, nil
}

func decodeRequest[T any](input io.Reader) (T, error) {
	var zero T
	if input == nil {
		return zero, errs.New(errs.CodeValidationFailed, "privileged request body is required")
	}

	raw, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil {
		return zero, errs.WrapMsg(errs.CodeValidationFailed, "read privileged request body", err)
	}
	if len(raw) == 0 {
		return zero, errs.New(errs.CodeValidationFailed, "privileged request body is required")
	}
	if len(raw) > maxRequestBytes {
		return zero, errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("privileged request body exceeds %d bytes", maxRequestBytes),
		)
	}
	if err := rejectDuplicateFields(raw); err != nil {
		return zero, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var request T
	if err := decoder.Decode(&request); err != nil {
		return zero, errs.WrapMsg(
			errs.CodeValidationFailed,
			"decode privileged request: "+err.Error(),
			err,
		)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return zero, err
	}
	return request, nil
}

func rejectDuplicateFields(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSONValue(decoder, 0); err != nil {
		if errs.AsDomainError(err) != nil {
			return err
		}
		return errs.WrapMsg(
			errs.CodeValidationFailed,
			"decode privileged request: "+err.Error(),
			err,
		)
	}
	return requireJSONEOF(decoder)
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= maxJSONDepth {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("privileged request JSON nesting exceeds %d levels", maxJSONDepth),
		)
	}

	switch delim {
	case '{':
		seen := make(map[string]struct{})
		fieldCount := 0
		for decoder.More() {
			fieldCount++
			if fieldCount > maxJSONObjectFields {
				return errs.New(
					errs.CodeValidationFailed,
					fmt.Sprintf("privileged request JSON object exceeds %d fields", maxJSONObjectFields),
				)
			}
			fieldToken, err := decoder.Token()
			if err != nil {
				return err
			}
			field, ok := fieldToken.(string)
			if !ok {
				return fmt.Errorf("object field is not a string")
			}
			if _, exists := seen[field]; exists {
				return errs.New(
					errs.CodeValidationFailed,
					fmt.Sprintf("duplicate JSON field %q in privileged request", field),
				)
			}
			for existing := range seen {
				if strings.EqualFold(existing, field) {
					return errs.New(
						errs.CodeValidationFailed,
						fmt.Sprintf(
							"case-folded duplicate JSON field %q conflicts with %q in privileged request",
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
		_, err = decoder.Token()
		return err
	case '[':
		itemCount := 0
		for decoder.More() {
			itemCount++
			if itemCount > maxJSONArrayItems {
				return errs.New(
					errs.CodeValidationFailed,
					fmt.Sprintf("privileged request JSON array exceeds %d items", maxJSONArrayItems),
				)
			}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}

func requireJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errs.New(errs.CodeValidationFailed, "unexpected trailing JSON in privileged request")
		}
		return errs.WrapMsg(
			errs.CodeValidationFailed,
			"decode privileged request: "+err.Error(),
			err,
		)
	}
	return nil
}
