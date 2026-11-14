package privilegedwire

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"

	"mvmctl/pkg/errs"
)

const requestPrefixBytes = len(requestMagic) + 4 + 8

// Request is one decoded typed request header and its separately framed payload length.
// T is the action-specific body type selected by the caller's named method.
type Request[T any] struct {
	Body          T
	PayloadLength uint64
}

type requestEnvelope[T any] struct {
	SchemaVersion uint32 `json:"schema_version"`
	Action        string `json:"action"`
	Body          T      `json:"body"`
}

var requestEnvelopeFields = fieldSet("schema_version", "action", "body")

// EncodeRequest writes one version-1 request header. It does not write the separately declared payload.
func EncodeRequest[T any](
	ctx context.Context,
	writer io.Writer,
	action string,
	body T,
	payloadLength uint64,
) error {
	if err := validateAction(action); err != nil {
		return err
	}
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "encode privileged request body", err)
	}
	if isJSONNull(encodedBody) {
		return errs.New(errs.CodeValidationFailed, "privileged request body must not be null")
	}
	header, err := json.Marshal(requestEnvelope[json.RawMessage]{
		SchemaVersion: 1,
		Action:        action,
		Body:          encodedBody,
	})
	if err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "encode privileged request header", err)
	}
	if len(header) > maxJSONFrameBytes {
		return errs.New(errs.CodeValidationFailed, "privileged request header exceeds 65536 bytes")
	}
	if err := validateStrictJSON(header); err != nil {
		return err
	}

	prefix := make([]byte, requestPrefixBytes)
	copy(prefix, requestMagic)
	appendUint32(prefix[len(requestMagic):], uint32(len(header)))
	appendUint64(prefix[len(requestMagic)+4:], payloadLength)
	return writeFrame(ctx, writer, prefix, header)
}

// DecodeRequest reads one version-1 typed request header and leaves any declared payload unread.
func DecodeRequest[T any](
	ctx context.Context,
	reader io.Reader,
	expectedAction string,
	maxPayloadLength uint64,
) (Request[T], error) {
	var zero Request[T]
	if err := validateAction(expectedAction); err != nil {
		return zero, err
	}
	prefix := make([]byte, requestPrefixBytes)
	if err := readExact(ctx, reader, prefix, "request prefix"); err != nil {
		return zero, err
	}
	if string(prefix[:len(requestMagic)]) != requestMagic {
		return zero, errs.New(errs.CodeValidationFailed, "invalid privileged request magic")
	}
	headerLength := binary.BigEndian.Uint32(prefix[len(requestMagic) : len(requestMagic)+4])
	if err := validateFrameLength(headerLength); err != nil {
		return zero, err
	}
	payloadLength := binary.BigEndian.Uint64(prefix[len(requestMagic)+4:])
	if payloadLength > maxPayloadLength {
		return zero, errs.New(errs.CodeValidationFailed, "privileged request payload length exceeds receiver limit")
	}
	header := make([]byte, headerLength)
	if err := readExact(ctx, reader, header, "request header"); err != nil {
		return zero, err
	}
	if err := validateStrictJSON(header); err != nil {
		return zero, err
	}
	fields, err := decodeExactObject(
		header,
		requestEnvelopeFields,
		[]string{"schema_version", "action", "body"},
		"request header",
	)
	if err != nil {
		return zero, err
	}
	schemaVersion, err := decodeJSONField[uint32](fields["schema_version"], "request schema version")
	if err != nil {
		return zero, err
	}
	if schemaVersion != 1 {
		return zero, errs.New(errs.CodeValidationFailed, "unsupported privileged request schema version")
	}
	action, err := decodeJSONField[string](fields["action"], "request action")
	if err != nil {
		return zero, err
	}
	if err := validateAction(action); err != nil {
		return zero, err
	}
	if action != expectedAction {
		return zero, errs.New(errs.CodeValidationFailed, "privileged request action does not match argv action")
	}
	if isJSONNull(fields["body"]) {
		return zero, errs.New(errs.CodeValidationFailed, "privileged request body must not be null")
	}
	body, err := decodeCanonicalJSONField[T](fields["body"], "request body")
	if err != nil {
		return zero, err
	}
	return Request[T]{
		Body:          body,
		PayloadLength: payloadLength,
	}, nil
}

func validateAction(action string) error {
	if !validASCIIIdentifier(action, maxActionBytes) {
		return errs.New(errs.CodeValidationFailed, "privileged action must be a 1-64 byte ASCII identifier")
	}
	return nil
}
