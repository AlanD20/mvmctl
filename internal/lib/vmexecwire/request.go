package vmexecwire

import (
	"context"
	"encoding/json"
	"io"
	"regexp"
	"strings"

	"mvmctl/internal/lib/validators"
	"mvmctl/pkg/errs"
)

var canonicalUUIDV4 = regexp.MustCompile(
	`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
)

// SourceRequest is the guest-source intent that the host relay must authorize.
type SourceRequest struct {
	RequestID   string `json:"request_id"`
	Destination string `json:"destination"`
	TargetUser  string `json:"target_user"`
	Command     string `json:"command"`
}

// DestinationRequest is the exact request the authorized host relay sends to the destination agent.
type DestinationRequest struct {
	RequestID  string `json:"request_id"`
	AgentToken string `json:"agent_token"`
	TargetUser string `json:"target_user"`
	Command    string `json:"command"`
}

type sourceRequestEnvelope struct {
	SchemaVersion uint16 `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Destination   string `json:"destination"`
	TargetUser    string `json:"target_user"`
	Command       string `json:"command"`
}

type destinationRequestEnvelope struct {
	SchemaVersion uint16 `json:"schema_version"`
	RequestID     string `json:"request_id"`
	AgentToken    string `json:"agent_token"`
	TargetUser    string `json:"target_user"`
	Command       string `json:"command"`
}

var sourceRequestFields = []string{"schema_version", "request_id", "destination", "target_user", "command"}
var destinationRequestFields = []string{"schema_version", "request_id", "agent_token", "target_user", "command"}

// Validate enforces the bounded guest-source request contract.
func (request SourceRequest) Validate() error {
	if err := validateRequestID(request.RequestID); err != nil {
		return err
	}
	if !validText(request.Destination, maxDestinationLen, false) {
		return validationError("VM-exec destination must be 1-64 bytes without control characters")
	}
	if err := validateTargetUser(request.TargetUser); err != nil {
		return err
	}
	return validateCommand(request.Command)
}

// Validate enforces the bounded destination-agent request contract.
func (request DestinationRequest) Validate() error {
	if err := validateRequestID(request.RequestID); err != nil {
		return err
	}
	if !canonicalUUIDV4.MatchString(request.AgentToken) {
		return validationError("VM-exec agent token must be a canonical lowercase UUIDv4")
	}
	if err := validateTargetUser(request.TargetUser); err != nil {
		return err
	}
	return validateCommand(request.Command)
}

// EncodeSourceRequest writes one validated guest-source request frame.
func EncodeSourceRequest(ctx context.Context, writer io.Writer, request SourceRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(sourceRequestEnvelope{
		SchemaVersion: wireVersion,
		RequestID:     request.RequestID,
		Destination:   request.Destination,
		TargetUser:    request.TargetUser,
		Command:       request.Command,
	})
	if err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "encode VM-exec source request", err)
	}
	return encodeFrame(ctx, writer, body)
}

// DecodeSourceRequest reads and validates one guest-source request frame.
func DecodeSourceRequest(ctx context.Context, reader io.Reader) (SourceRequest, error) {
	var zero SourceRequest
	body, err := decodeFrame(ctx, reader)
	if err != nil {
		return zero, err
	}
	if err := validateStrictJSON(body); err != nil {
		return zero, err
	}
	fields, err := decodeClosedObject(body, sourceRequestFields, "source request")
	if err != nil {
		return zero, err
	}
	if err := validateSchema(fields["schema_version"]); err != nil {
		return zero, err
	}
	request, err := decodeSourceRequest(fields)
	if err != nil {
		return zero, err
	}
	if err := request.Validate(); err != nil {
		return zero, err
	}
	return request, nil
}

// EncodeDestinationRequest writes one validated destination-agent request frame.
func EncodeDestinationRequest(ctx context.Context, writer io.Writer, request DestinationRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(destinationRequestEnvelope{
		SchemaVersion: wireVersion,
		RequestID:     request.RequestID,
		AgentToken:    request.AgentToken,
		TargetUser:    request.TargetUser,
		Command:       request.Command,
	})
	if err != nil {
		return errs.WrapMsg(errs.CodeValidationFailed, "encode VM-exec destination request", err)
	}
	return encodeFrame(ctx, writer, body)
}

// DecodeDestinationRequest reads and validates one destination-agent request frame.
func DecodeDestinationRequest(ctx context.Context, reader io.Reader) (DestinationRequest, error) {
	var zero DestinationRequest
	body, err := decodeFrame(ctx, reader)
	if err != nil {
		return zero, err
	}
	if err := validateStrictJSON(body); err != nil {
		return zero, err
	}
	fields, err := decodeClosedObject(body, destinationRequestFields, "destination request")
	if err != nil {
		return zero, err
	}
	if err := validateSchema(fields["schema_version"]); err != nil {
		return zero, err
	}
	request, err := decodeDestinationRequest(fields)
	if err != nil {
		return zero, err
	}
	if err := request.Validate(); err != nil {
		return zero, err
	}
	return request, nil
}

func decodeSourceRequest(fields map[string]json.RawMessage) (SourceRequest, error) {
	requestID, err := decodeField[string](fields["request_id"], "source request ID")
	if err != nil {
		return SourceRequest{}, err
	}
	destination, err := decodeField[string](fields["destination"], "source request destination")
	if err != nil {
		return SourceRequest{}, err
	}
	targetUser, err := decodeField[string](fields["target_user"], "source request target user")
	if err != nil {
		return SourceRequest{}, err
	}
	command, err := decodeField[string](fields["command"], "source request command")
	if err != nil {
		return SourceRequest{}, err
	}
	return SourceRequest{
		RequestID: requestID, Destination: destination, TargetUser: targetUser, Command: command,
	}, nil
}

func decodeDestinationRequest(fields map[string]json.RawMessage) (DestinationRequest, error) {
	requestID, err := decodeField[string](fields["request_id"], "destination request ID")
	if err != nil {
		return DestinationRequest{}, err
	}
	agentToken, err := decodeField[string](fields["agent_token"], "destination request agent token")
	if err != nil {
		return DestinationRequest{}, err
	}
	targetUser, err := decodeField[string](fields["target_user"], "destination request target user")
	if err != nil {
		return DestinationRequest{}, err
	}
	command, err := decodeField[string](fields["command"], "destination request command")
	if err != nil {
		return DestinationRequest{}, err
	}
	return DestinationRequest{
		RequestID: requestID, AgentToken: agentToken, TargetUser: targetUser, Command: command,
	}, nil
}

func validateSchema(raw json.RawMessage) error {
	schema, err := decodeField[uint16](raw, "schema version")
	if err != nil {
		return err
	}
	if schema != wireVersion {
		return validationError("unsupported VM-exec schema version")
	}
	return nil
}

func validateRequestID(requestID string) error {
	if len(requestID) != 32 {
		return validationError("VM-exec request ID must be exactly 32 lowercase hexadecimal characters")
	}
	for index := range len(requestID) {
		if !strings.ContainsRune("0123456789abcdef", rune(requestID[index])) {
			return validationError("VM-exec request ID must be exactly 32 lowercase hexadecimal characters")
		}
	}
	return nil
}

func validateTargetUser(targetUser string) error {
	if targetUser == "root" {
		return validationError("VM-exec target user must be non-root")
	}
	if !validText(targetUser, maxTargetUserLen, false) {
		return validationError("invalid VM-exec target user")
	}
	if err := validators.SSHUsername(targetUser); err != nil {
		return validationError("invalid VM-exec target user")
	}
	return nil
}

func validateCommand(command string) error {
	if !validText(command, MaxCommandBytes, true) {
		return validationError("VM-exec command must be 1-32768 bytes of valid UTF-8 without unsafe controls")
	}
	return nil
}
