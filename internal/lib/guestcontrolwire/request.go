package guestcontrolwire

import (
	"context"
	"encoding/json"
	"io"
	"regexp"

	"mvmctl/pkg/errs"
)

// Operation identifies a named method of the floating-IP feature.
type Operation string

const (
	Inspect   Operation = "inspect"
	Associate Operation = "associate"
	Renew     Operation = "renew"
	Release   Operation = "release"
)

var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var resourceIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// FloatingIPInput carries an exact resource and its observed ownership generation.
// The requesting VM is derived from the authenticated session, never from this input.
type FloatingIPInput struct {
	ResourceID         string `json:"resource_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}

// Request is the first closed feature request. Future features require named typed inputs.
type Request struct {
	RequestID string          `json:"request_id"`
	Operation Operation       `json:"operation"`
	Input     FloatingIPInput `json:"input"`
}

type requestEnvelope struct {
	SchemaVersion uint16          `json:"schema_version"`
	RequestID     string          `json:"request_id"`
	Feature       string          `json:"feature"`
	Operation     Operation       `json:"operation"`
	Input         FloatingIPInput `json:"input"`
}

// Validate checks the request grammar, not permission to exercise the operation.
func (request Request) Validate() error {
	if !requestIDPattern.MatchString(request.RequestID) {
		return invalid("Guest Control request ID must be a canonical lowercase UUIDv4")
	}
	if !resourceIDPattern.MatchString(request.Input.ResourceID) {
		return invalid("floating-IP resource ID must be 32 lowercase hexadecimal characters")
	}
	switch request.Operation {
	case Inspect, Associate, Renew, Release:
		return nil
	default:
		return invalid("unsupported floating-IP operation")
	}
}

// EncodeRequest writes one bounded request without closing the session.
func EncodeRequest(ctx context.Context, writer io.Writer, request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(requestEnvelope{
		SchemaVersion: 1,
		RequestID:     request.RequestID,
		Feature:       "floating_ip",
		Operation:     request.Operation,
		Input:         request.Input,
	})
	if err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "encode Guest Control request", err)
	}
	return writeFrame(ctx, writer, body)
}

// DecodeRequest consumes exactly one frame, leaving later frames for the session owner.
func DecodeRequest(ctx context.Context, reader io.Reader) (Request, error) {
	body, err := readFrame(ctx, reader)
	if err != nil {
		return Request{}, err
	}
	fields, err := closedObject(body, []string{"schema_version", "request_id", "feature", "operation", "input"})
	if err != nil {
		return Request{}, err
	}
	if _, err := closedObject(fields["input"], []string{"resource_id", "expected_generation"}); err != nil {
		return Request{}, err
	}
	var envelope requestEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Request{}, errs.WrapMsg(errs.CodeValidationFailed, "decode Guest Control request fields", err)
	}
	if envelope.SchemaVersion != 1 || envelope.Feature != "floating_ip" {
		return Request{}, invalid("unsupported Guest Control schema version or feature")
	}
	request := Request{RequestID: envelope.RequestID, Operation: envelope.Operation, Input: envelope.Input}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}
