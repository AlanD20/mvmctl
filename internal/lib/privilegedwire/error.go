package privilegedwire

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"mvmctl/pkg/errs"
)

const (
	maxErrorCodeBytes       = 128
	maxErrorMessageBytes    = 8 * 1024
	maxErrorOperationBytes  = 128
	maxErrorEntityBytes     = 1024
	maxErrorDetailCount     = 32
	maxErrorDetailKeyBytes  = 64
	maxErrorDetailString    = 4 * 1024
	maxErrorDetailArray     = 32
	maxErrorDetailArrayItem = 1024

	omittedDetailsKey = "privileged_details_omitted"
)

type domainErrorWire struct {
	Code      string                     `json:"code"`
	Class     string                     `json:"class"`
	Message   string                     `json:"message"`
	Operation string                     `json:"operation"`
	Entity    string                     `json:"entity"`
	Details   map[string]json.RawMessage `json:"details"`
}

var domainErrorFields = fieldSet("code", "class", "message", "operation", "entity", "details")

func normalizeDomainError(operationErr error) domainErrorWire {
	domainErr := errs.AsDomainError(operationErr)
	if domainErr == nil || !validWireDomainError(domainErr) {
		return fallbackDomainError()
	}
	details := normalizeErrorDetails(domainErr.Details)
	return domainErrorWire{
		Code:      string(domainErr.Code),
		Class:     classString(domainErr.Class),
		Message:   domainErr.Message,
		Operation: domainErr.Op,
		Entity:    domainErr.Entity,
		Details:   details,
	}
}

func validWireDomainError(domainErr *errs.DomainError) bool {
	return validASCIIIdentifier(string(domainErr.Code), maxErrorCodeBytes) &&
		classString(domainErr.Class) != "" &&
		validWireText(domainErr.Message, maxErrorMessageBytes, true) &&
		validWireText(domainErr.Op, maxErrorOperationBytes, false) &&
		validWireText(domainErr.Entity, maxErrorEntityBytes, false)
}

func normalizeErrorDetails(details map[string]any) map[string]json.RawMessage {
	if len(details) == 0 {
		return map[string]json.RawMessage{}
	}
	_, includeFlag := details[omittedDetailsKey]
	candidates := make([]string, 0, maxErrorDetailCount)
	validDetailCount := 0
	for key, value := range details {
		if key == omittedDetailsKey {
			continue
		}
		if !validErrorDetail(key, value) {
			includeFlag = true
			continue
		}
		validDetailCount++
		candidates = retainErrorDetailCandidate(candidates, key)
	}
	if validDetailCount > maxErrorDetailCount {
		includeFlag = true
	}
	retainedLimit := maxErrorDetailCount
	if includeFlag {
		retainedLimit--
	}
	if len(candidates) > retainedLimit {
		candidates = candidates[:retainedLimit]
	}
	result := make(map[string]json.RawMessage, maxErrorDetailCount)
	for _, key := range candidates {
		encoded, ok := encodeErrorDetail(key, details[key])
		if !ok {
			includeFlag = true
			continue
		}
		result[key] = encoded
	}
	if includeFlag {
		result[omittedDetailsKey] = json.RawMessage("true")
	}
	return result
}

func retainErrorDetailCandidate(candidates []string, key string) []string {
	index := sort.SearchStrings(candidates, key)
	if len(candidates) < maxErrorDetailCount {
		candidates = append(candidates, "")
		copy(candidates[index+1:], candidates[index:len(candidates)-1])
		candidates[index] = key
		return candidates
	}
	if index == maxErrorDetailCount {
		return candidates
	}
	copy(candidates[index+1:], candidates[index:len(candidates)-1])
	candidates[index] = key
	return candidates
}

func encodeErrorDetail(key string, value any) (json.RawMessage, bool) {
	if key == omittedDetailsKey {
		flag, ok := value.(bool)
		if !ok || !flag {
			return nil, false
		}
		return json.RawMessage("true"), true
	}
	if !validErrorDetail(key, value) {
		return nil, false
	}
	return marshalDetailValue(value)
}

func validErrorDetail(key string, value any) bool {
	if key == omittedDetailsKey ||
		!validASCIIIdentifier(key, maxErrorDetailKeyBytes) || isSensitiveErrorDetailKey(key) {
		return false
	}
	switch typed := value.(type) {
	case bool, int64:
		return true
	case string:
		return validWireText(typed, maxErrorDetailString, true)
	case []string:
		if len(typed) > maxErrorDetailArray {
			return false
		}
		for _, item := range typed {
			if !validWireText(item, maxErrorDetailArrayItem, true) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func isSensitiveErrorDetailKey(key string) bool {
	tokenStart := 0
	for index := 0; index <= len(key); index++ {
		if index < len(key) && !isErrorDetailKeySeparator(key[index]) {
			continue
		}
		if isSensitiveErrorDetailToken(key[tokenStart:index]) {
			return true
		}
		tokenStart = index + 1
	}
	return false
}

func isErrorDetailKeySeparator(character byte) bool {
	return character == '.' || character == '_' || character == '-'
}

func isSensitiveErrorDetailToken(token string) bool {
	return strings.EqualFold(token, "err") ||
		strings.EqualFold(token, "error") ||
		strings.EqualFold(token, "cause") ||
		strings.EqualFold(token, "stack") ||
		strings.EqualFold(token, "stacktrace") ||
		strings.EqualFold(token, "stderr") ||
		strings.EqualFold(token, "stdout")
}

func marshalDetailValue(value any) (json.RawMessage, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func decodeDomainError(raw json.RawMessage) (*errs.DomainError, error) {
	fields, err := decodeExactObject(
		raw,
		domainErrorFields,
		[]string{"code", "class", "message", "operation", "entity", "details"},
		"error",
	)
	if err != nil {
		return nil, err
	}
	code, err := decodeJSONField[string](fields["code"], "error code")
	if err != nil {
		return nil, err
	}
	if !validASCIIIdentifier(code, maxErrorCodeBytes) {
		return nil, errs.New(errs.CodeValidationFailed, "invalid privileged error code")
	}
	classValue, err := decodeJSONField[string](fields["class"], "error class")
	if err != nil {
		return nil, err
	}
	class, ok := parseClassString(classValue)
	if !ok {
		return nil, errs.New(errs.CodeValidationFailed, "invalid privileged error class")
	}
	message, err := decodeBoundedWireString(fields["message"], "error message", maxErrorMessageBytes, true)
	if err != nil {
		return nil, err
	}
	operation, err := decodeBoundedWireString(fields["operation"], "error operation", maxErrorOperationBytes, false)
	if err != nil {
		return nil, err
	}
	entity, err := decodeBoundedWireString(fields["entity"], "error entity", maxErrorEntityBytes, false)
	if err != nil {
		return nil, err
	}
	details, err := decodeErrorDetails(fields["details"])
	if err != nil {
		return nil, err
	}
	return &errs.DomainError{
		Code:    errs.Code(code),
		Class:   class,
		Message: message,
		Op:      operation,
		Entity:  entity,
		Details: details,
	}, nil
}

func decodeErrorDetails(raw json.RawMessage) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "decode privileged error details", err)
	}
	if fields == nil {
		return nil, errs.New(errs.CodeValidationFailed, "privileged error details must be a JSON object")
	}
	if len(fields) > maxErrorDetailCount {
		return nil, errs.New(errs.CodeValidationFailed, "privileged error details exceed 32 entries")
	}
	details := make(map[string]any, len(fields))
	for key, value := range fields {
		if !validASCIIIdentifier(key, maxErrorDetailKeyBytes) || isSensitiveErrorDetailKey(key) {
			return nil, errs.New(errs.CodeValidationFailed, "invalid privileged error detail key")
		}
		decoded, err := decodeErrorDetail(value)
		if err != nil {
			return nil, err
		}
		if key == omittedDetailsKey {
			flag, ok := decoded.(bool)
			if !ok || !flag {
				return nil, errs.New(errs.CodeValidationFailed, "invalid privileged detail omission flag")
			}
		}
		details[key] = decoded
	}
	return details, nil
}

func decodeErrorDetail(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errs.New(errs.CodeValidationFailed, "privileged error detail is empty")
	}
	switch trimmed[0] {
	case 't', 'f':
		return decodeJSONField[bool](trimmed, "error detail")
	case '"':
		return decodeBoundedWireString(trimmed, "error detail", maxErrorDetailString, true)
	case '[':
		values, err := decodeJSONField[[]string](trimmed, "error detail string array")
		if err != nil {
			return nil, err
		}
		if len(values) > maxErrorDetailArray {
			return nil, errs.New(errs.CodeValidationFailed, "privileged error detail array exceeds 32 items")
		}
		for _, value := range values {
			if !validWireText(value, maxErrorDetailArrayItem, true) {
				return nil, errs.New(errs.CodeValidationFailed, "invalid privileged error detail array item")
			}
		}
		return values, nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		value, err := strconv.ParseInt(string(trimmed), 10, 64)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeValidationFailed, "invalid privileged error integer detail", err)
		}
		return value, nil
	default:
		return nil, errs.New(errs.CodeValidationFailed, "unsupported privileged error detail value")
	}
}

func decodeBoundedWireString(
	raw json.RawMessage,
	description string,
	maxBytes int,
	allowLineBreaks bool,
) (string, error) {
	value, err := decodeJSONField[string](raw, description)
	if err != nil {
		return "", err
	}
	if !validWireText(value, maxBytes, allowLineBreaks) {
		return "", errs.New(errs.CodeValidationFailed, "invalid privileged "+description)
	}
	return value, nil
}

func validWireText(value string, maxBytes int, allowLineBreaks bool) bool {
	if len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == '\n' || character == '\t' {
			if allowLineBreaks {
				continue
			}
			return false
		}
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			return false
		}
	}
	return true
}

func validASCIIIdentifier(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes {
		return false
	}
	if !isASCIIAlphaNumeric(value[0]) {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if isASCIIAlphaNumeric(character) || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func isASCIIAlphaNumeric(character byte) bool {
	return (character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z') ||
		(character >= '0' && character <= '9')
}

func classString(class errs.Class) string {
	switch class {
	case errs.ClassUnknown:
		return "unknown"
	case errs.ClassValidation:
		return "validation"
	case errs.ClassConflict:
		return "conflict"
	case errs.ClassRetryable:
		return "retryable"
	case errs.ClassInternal:
		return "internal"
	case errs.ClassNeedsInteraction:
		return "needs_interaction"
	default:
		return ""
	}
}

func parseClassString(value string) (errs.Class, bool) {
	switch value {
	case "unknown":
		return errs.ClassUnknown, true
	case "validation":
		return errs.ClassValidation, true
	case "conflict":
		return errs.ClassConflict, true
	case "retryable":
		return errs.ClassRetryable, true
	case "internal":
		return errs.ClassInternal, true
	case "needs_interaction":
		return errs.ClassNeedsInteraction, true
	default:
		return errs.ClassUnknown, false
	}
}

func fallbackDomainError() domainErrorWire {
	return domainErrorWire{
		Code:      string(errs.CodeInternal),
		Class:     "internal",
		Message:   "privileged operation outcome could not be encoded",
		Operation: "privileged",
		Entity:    "",
		Details: map[string]json.RawMessage{
			"outcome_unknown": json.RawMessage("true"),
		},
	}
}
