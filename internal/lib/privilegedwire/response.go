package privilegedwire

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"

	"mvmctl/pkg/errs"
)

const responsePrefixBytes = len(responseMagic) + 4

const (
	responseStatusSuccess = "success"
	responseStatusError   = "error"
)

type successEnvelope[T any] struct {
	SchemaVersion uint32 `json:"schema_version"`
	Action        string `json:"action"`
	Status        string `json:"status"`
	Result        T      `json:"result"`
}

type errorEnvelope struct {
	SchemaVersion uint32          `json:"schema_version"`
	Action        string          `json:"action"`
	Status        string          `json:"status"`
	Error         domainErrorWire `json:"error"`
}

var responseEnvelopeFields = fieldSet("schema_version", "action", "status", "result", "error")

// Response is one successfully decoded privileged response envelope. OperationError is non-nil
// for an error envelope; otherwise Result is authoritative. DecodeResponse returns a separate
// error only when the response itself cannot be trusted as protocol input.
type Response[T any] struct {
	result         T
	operationError *errs.DomainError
}

// Result returns the typed result carried by a success envelope.
func (response Response[T]) Result() T {
	return response.result
}

// OperationError returns the preserved DomainError carried by an error envelope.
func (response Response[T]) OperationError() *errs.DomainError {
	return response.operationError
}

// EncodeSuccess writes one version-1 success response containing an action-specific typed result.
func EncodeSuccess[T any](ctx context.Context, writer io.Writer, action string, result T) error {
	if err := validateAction(action); err != nil {
		return err
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "encode privileged success result", err)
	}
	if isJSONNull(encodedResult) {
		return errs.New(errs.CodeValidationFailed, "privileged success result must not be null")
	}
	body, err := json.Marshal(successEnvelope[json.RawMessage]{
		SchemaVersion: 1,
		Action:        action,
		Status:        responseStatusSuccess,
		Result:        encodedResult,
	})
	if err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "encode privileged success response", err)
	}
	if len(body) > maxJSONFrameBytes {
		return errs.New(errs.CodeValidationFailed, "privileged success response exceeds 65536 bytes")
	}
	if err := validateStrictJSON(body); err != nil {
		return err
	}

	prefix := make([]byte, responsePrefixBytes)
	copy(prefix, responseMagic)
	appendUint32(prefix[len(responseMagic):], uint32(len(body)))
	return writeFrame(ctx, writer, prefix, body)
}

// EncodeError writes one version-1 error response after normalizing a DomainError to the bounded wire schema.
func EncodeError(ctx context.Context, writer io.Writer, action string, operationErr error) error {
	if err := validateAction(action); err != nil {
		return err
	}
	envelope := errorEnvelope{
		SchemaVersion: 1,
		Action:        action,
		Status:        responseStatusError,
		Error:         normalizeDomainError(operationErr),
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return errs.WrapMsg(errs.CodeInternal, "encode privileged error response", err)
	}
	if len(body) > maxJSONFrameBytes {
		envelope.Error = fallbackDomainError()
		body, err = json.Marshal(envelope)
		if err != nil {
			return errs.WrapMsg(errs.CodeInternal, "encode fallback privileged error response", err)
		}
	}

	prefix := make([]byte, responsePrefixBytes)
	copy(prefix, responseMagic)
	appendUint32(prefix[len(responseMagic):], uint32(len(body)))
	return writeFrame(ctx, writer, prefix, body)
}

// DecodeResponse reads one final version-1 response, separating a preserved operation error from protocol failures.
func DecodeResponse[T any](ctx context.Context, reader io.Reader, expectedAction string) (Response[T], error) {
	var zero Response[T]
	if err := validateAction(expectedAction); err != nil {
		return zero, err
	}
	body, err := readResponseFrame(ctx, reader)
	if err != nil {
		return zero, err
	}
	if err := validateStrictJSON(body); err != nil {
		return zero, err
	}
	fields, err := decodeExactObject(
		body,
		responseEnvelopeFields,
		[]string{"schema_version", "action", "status"},
		"response",
	)
	if err != nil {
		return zero, err
	}
	if err := validateResponseIdentity(fields, expectedAction); err != nil {
		return zero, err
	}
	status, err := decodeJSONField[string](fields["status"], "response status")
	if err != nil {
		return zero, err
	}
	switch status {
	case responseStatusSuccess:
		result, decodeErr := decodeSuccessResult[T](fields)
		if decodeErr != nil {
			return zero, decodeErr
		}
		return Response[T]{result: result}, nil
	case responseStatusError:
		operationErr, decodeErr := decodeErrorResult(fields)
		if decodeErr != nil {
			return zero, decodeErr
		}
		return Response[T]{operationError: operationErr}, nil
	default:
		return zero, errs.New(errs.CodeValidationFailed, "invalid privileged response status")
	}
}

func decodeErrorResult(fields map[string]json.RawMessage) (*errs.DomainError, error) {
	rawError, hasError := fields["error"]
	_, hasResult := fields["result"]
	if !hasError || hasResult {
		return nil, errs.New(
			errs.CodeValidationFailed,
			"privileged error response must contain only one error",
		)
	}
	if isJSONNull(rawError) {
		return nil, errs.New(errs.CodeValidationFailed, "privileged error response must not be null")
	}
	domainErr, err := decodeDomainError(rawError)
	if err != nil {
		return nil, err
	}
	return domainErr, nil
}

func readResponseFrame(ctx context.Context, reader io.Reader) ([]byte, error) {
	prefix := make([]byte, responsePrefixBytes)
	if err := readExact(ctx, reader, prefix, "response prefix"); err != nil {
		return nil, err
	}
	if string(prefix[:len(responseMagic)]) != responseMagic {
		return nil, errs.New(errs.CodeValidationFailed, "invalid privileged response magic")
	}
	bodyLength := binary.BigEndian.Uint32(prefix[len(responseMagic):])
	if err := validateFrameLength(bodyLength); err != nil {
		return nil, err
	}
	body := make([]byte, bodyLength)
	if err := readExact(ctx, reader, body, "response body"); err != nil {
		return nil, err
	}
	if err := requireReaderEOF(ctx, reader); err != nil {
		return nil, err
	}
	return body, nil
}

func validateResponseIdentity(fields map[string]json.RawMessage, expectedAction string) error {
	schemaVersion, err := decodeJSONField[uint32](fields["schema_version"], "response schema version")
	if err != nil {
		return err
	}
	if schemaVersion != 1 {
		return errs.New(errs.CodeValidationFailed, "unsupported privileged response schema version")
	}
	action, err := decodeJSONField[string](fields["action"], "response action")
	if err != nil {
		return err
	}
	if err := validateAction(action); err != nil {
		return err
	}
	if action != expectedAction {
		return errs.New(errs.CodeValidationFailed, "privileged response action does not match request action")
	}
	return nil
}

func decodeSuccessResult[T any](fields map[string]json.RawMessage) (T, error) {
	var zero T
	result, hasResult := fields["result"]
	_, hasError := fields["error"]
	if !hasResult || hasError {
		return zero, errs.New(
			errs.CodeValidationFailed,
			"privileged success response must contain only one result",
		)
	}
	if isJSONNull(result) {
		return zero, errs.New(errs.CodeValidationFailed, "privileged success result must not be null")
	}
	return decodeCanonicalJSONField[T](result, "success result")
}
