package vmexecsessionwire

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"mvmctl/pkg/errs"
)

const (
	maxErrorCodeBytes      = 128
	maxErrorMessageBytes   = 4 * 1024
	maxErrorOperationBytes = 128
	maxErrorEntityBytes    = 64
	maxErrorDetailCount    = maxJSONFields
	maxErrorDetailKeyBytes = 64

	omittedDetailsKey = "session_details_omitted"
)

type domainErrorWire struct {
	Code      string                     `json:"code"`
	Class     string                     `json:"class"`
	Message   string                     `json:"message"`
	Operation string                     `json:"operation"`
	Entity    string                     `json:"entity"`
	Details   map[string]json.RawMessage `json:"details"`
}

var domainErrorFields = []string{"code", "class", "message", "operation", "entity", "details"}

func normalizeDomainError(operationErr error) domainErrorWire {
	domainErr := errs.AsDomainError(operationErr)
	if domainErr == nil || !validErrorClass(domainErr.Class) ||
		!validOptionalIdentifier(domainErr.Op, maxErrorOperationBytes) ||
		!validErrorEntity(domainErr.Entity) {
		return fallbackDomainError()
	}
	publicMessage, ok := publicErrorMessage(domainErr.Code)
	if !ok {
		return fallbackDomainError()
	}
	return domainErrorWire{
		Code:      string(domainErr.Code),
		Class:     encodeErrorClass(domainErr.Class),
		Message:   publicMessage,
		Operation: domainErr.Op,
		Entity:    domainErr.Entity,
		Details:   normalizeErrorDetails(domainErr.Details),
	}
}

func fallbackDomainError() domainErrorWire {
	return domainErrorWire{
		Code:      string(errs.CodeInternal),
		Class:     encodeErrorClass(errs.ClassInternal),
		Message:   "retained VM-exec session failed",
		Operation: "",
		Entity:    "",
		Details:   map[string]json.RawMessage{omittedDetailsKey: json.RawMessage("true")},
	}
}

func normalizeErrorDetails(details map[string]any) map[string]json.RawMessage {
	if len(details) == 0 {
		return map[string]json.RawMessage{}
	}
	keys := make([]string, 0, len(details))
	omitted := false
	for key, value := range details {
		if key == omittedDetailsKey {
			omitted = true
			continue
		}
		if !validDetailKey(key) || !safeDetailValue(value) {
			omitted = true
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	retainedLimit := maxErrorDetailCount
	if omitted || len(keys) > maxErrorDetailCount {
		omitted = true
		retainedLimit--
	}
	if len(keys) > retainedLimit {
		keys = keys[:retainedLimit]
	}
	result := make(map[string]json.RawMessage, maxErrorDetailCount)
	for _, key := range keys {
		encoded, err := json.Marshal(details[key])
		if err != nil {
			omitted = true
			continue
		}
		result[key] = encoded
	}
	if omitted {
		result[omittedDetailsKey] = json.RawMessage("true")
	}
	return result
}

func decodeDomainError(raw json.RawMessage) (*errs.DomainError, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, validationError("retained VM-exec session failure error must not be null")
	}
	fields, err := decodeClosedObject(raw, domainErrorFields, "failure error")
	if err != nil {
		return nil, err
	}
	codeValue, err := decodeField[string](fields["code"], "failure error code")
	if err != nil {
		return nil, err
	}
	code := errs.Code(codeValue)
	publicMessage, ok := publicErrorMessage(code)
	if !ok {
		return nil, validationError("retained VM-exec session failure error code is invalid")
	}
	if !validErrorCode(codeValue) {
		return nil, validationError("retained VM-exec session failure error code is invalid")
	}
	classValue, err := decodeField[string](fields["class"], "failure error class")
	if err != nil {
		return nil, err
	}
	class, ok := decodeErrorClass(classValue)
	if !ok {
		return nil, validationError("retained VM-exec session failure error class is invalid")
	}
	message, err := decodeField[string](fields["message"], "failure error message")
	if err != nil || message != publicMessage || !validText(message, maxErrorMessageBytes, false) {
		if err != nil {
			return nil, err
		}
		return nil, validationError("retained VM-exec session failure error message is invalid")
	}
	operation, err := decodeField[string](fields["operation"], "failure error operation")
	if err != nil || !validOptionalIdentifier(operation, maxErrorOperationBytes) {
		if err != nil {
			return nil, err
		}
		return nil, validationError("retained VM-exec session failure error operation is invalid")
	}
	entity, err := decodeField[string](fields["entity"], "failure error entity")
	if err != nil || !validErrorEntity(entity) {
		if err != nil {
			return nil, err
		}
		return nil, validationError("retained VM-exec session failure error entity is invalid")
	}
	details, err := decodeErrorDetails(fields["details"])
	if err != nil {
		return nil, err
	}
	return &errs.DomainError{
		Code: code, Class: class, Message: message, Op: operation, Entity: entity, Details: details,
	}, nil
}

func decodeErrorDetails(raw json.RawMessage) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "decode retained VM-exec session failure details", err)
	}
	if fields == nil || len(fields) > maxErrorDetailCount {
		return nil, validationError("retained VM-exec session failure details are invalid or oversized")
	}
	details := make(map[string]any, len(fields))
	for key, rawValue := range fields {
		if !validDetailKey(key) {
			return nil, validationError("retained VM-exec session failure detail key is unsafe")
		}
		value, err := decodeSafeDetailValue(rawValue)
		if err != nil {
			return nil, err
		}
		if key == omittedDetailsKey {
			flag, ok := value.(bool)
			if !ok || !flag {
				return nil, validationError("retained VM-exec session detail omission marker is invalid")
			}
		}
		details[key] = value
	}
	return details, nil
}

func decodeSafeDetailValue(raw json.RawMessage) (any, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, validationError("retained VM-exec session failure detail is empty")
	}
	switch trimmed[0] {
	case 't', 'f':
		return decodeField[bool](trimmed, "failure detail")
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		value, err := strconv.ParseInt(string(trimmed), 10, 64)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeValidationFailed, "decode retained VM-exec integer detail", err)
		}
		return value, nil
	default:
		return nil, validationError("retained VM-exec session failure detail type is unsafe")
	}
}

func validDetailKey(key string) bool {
	if key == omittedDetailsKey {
		return true
	}
	if !validRequiredIdentifier(key, maxErrorDetailKeyBytes) {
		return false
	}
	tokenStart := 0
	for index := 0; index <= len(key); index++ {
		if index < len(key) && key[index] != '.' && key[index] != '_' && key[index] != '-' {
			continue
		}
		if sensitiveDetailToken(key[tokenStart:index]) {
			return false
		}
		tokenStart = index + 1
	}
	return true
}

func sensitiveDetailToken(token string) bool {
	switch strings.ToLower(token) {
	case "cause", "command", "credential", "err", "error", "output", "password", "path", "secret", "stack",
		"stacktrace", "stderr", "stdout", "token", "trace":
		return true
	default:
		return false
	}
}

func safeDetailValue(value any) bool {
	switch value.(type) {
	case bool, int64:
		return true
	default:
		return false
	}
}

func validErrorCode(code string) bool {
	return validRequiredIdentifier(code, maxErrorCodeBytes)
}

func publicErrorMessage(code errs.Code) (string, bool) {
	switch code {
	case errs.CodeValidationFailed:
		return "retained VM-exec session request was invalid", true
	case errs.CodeProcessError:
		return "retained VM-exec session process failed", true
	case errs.CodeVMAtomicFailed:
		return "retained VM-exec session transition failed", true
	case errs.CodeVMResourceExhausted:
		return "retained VM-exec session capacity is exhausted", true
	case errs.CodeUnauthorized:
		return "retained VM-exec session is not authorized", true
	case errs.CodeInternal:
		return "retained VM-exec session failed", true
	default:
		return "", false
	}
}

func validErrorEntity(entity string) bool {
	if entity == "" {
		return true
	}
	return len(entity) <= maxErrorEntityBytes && (validLowerHexVMID(entity) || validLowerHexIdentity(entity, 64))
}

func validLowerHexIdentity(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validOptionalIdentifier(value string, maxBytes int) bool {
	return value == "" || validRequiredIdentifier(value, maxBytes)
}

func validRequiredIdentifier(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes {
		return false
	}
	if (value[0] < 'a' || value[0] > 'z') && (value[0] < '0' || value[0] > '9') {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validErrorClass(class errs.Class) bool {
	return encodeErrorClass(class) != ""
}

func encodeErrorClass(class errs.Class) string {
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

func decodeErrorClass(value string) (errs.Class, bool) {
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
