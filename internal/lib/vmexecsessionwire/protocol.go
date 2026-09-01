package vmexecsessionwire

import (
	"context"
	"encoding/json"
	"io"

	"mvmctl/pkg/errs"
)

const (
	// Action is the sole privileged action using this retained session protocol.
	Action = "jailer-vm-exec-session"

	phaseOpenSource      = "open_source"
	phaseSourceRetained  = "source_retained"
	phaseBindDestination = "bind_destination"
	phaseReady           = "ready"
	phaseRelease         = "release"
	phaseReleased        = "released"
	phaseFailure         = "failure"
)

var fixedPhaseFields = []string{"schema_version", "phase"}
var openSourceFields = []string{"schema_version", "phase", "source_vm_id", "destination_vm_id"}
var failureFields = []string{"schema_version", "phase", "error"}

// OpenSource identifies the exact distinct VM pair whose live source connection accompanies the first frame.
type OpenSource struct {
	SourceVMID      string `json:"source_vm_id"`
	DestinationVMID string `json:"destination_vm_id"`
}

type openSourceEnvelope struct {
	SchemaVersion   uint16 `json:"schema_version"`
	Phase           string `json:"phase"`
	SourceVMID      string `json:"source_vm_id"`
	DestinationVMID string `json:"destination_vm_id"`
}

type fixedPhaseEnvelope struct {
	SchemaVersion uint16 `json:"schema_version"`
	Phase         string `json:"phase"`
}

type failureEnvelope struct {
	SchemaVersion uint16          `json:"schema_version"`
	Phase         string          `json:"phase"`
	Error         domainErrorWire `json:"error"`
}

// Outcome is the terminal released or failure response. A failure preserves only the bounded safe DomainError wire
// projection; its cause and sensitive diagnostic material never cross this protocol.
type Outcome struct {
	released       bool
	operationError *errs.DomainError
}

// Released reports whether root released the retained session cleanly.
func (outcome Outcome) Released() bool {
	return outcome.released
}

// OperationError returns the bounded remote failure, or nil for a released outcome.
func (outcome Outcome) OperationError() *errs.DomainError {
	return outcome.operationError
}

// Validate enforces the exact distinct source and destination VM identities.
func (request OpenSource) Validate() error {
	if !validLowerHexVMID(request.SourceVMID) {
		return validationError("retained VM-exec source VM ID must be 32 lowercase hexadecimal characters")
	}
	if !validLowerHexVMID(request.DestinationVMID) {
		return validationError("retained VM-exec destination VM ID must be 32 lowercase hexadecimal characters")
	}
	if request.SourceVMID == request.DestinationVMID {
		return validationError("retained VM-exec source and destination VM IDs must be distinct")
	}
	return nil
}

// EncodeOpenSource writes the exact first-phase VM-pair request.
func EncodeOpenSource(ctx context.Context, writer io.Writer, request OpenSource) error {
	if err := request.Validate(); err != nil {
		return err
	}
	return encodeEnvelope(ctx, writer, openSourceEnvelope{
		SchemaVersion:   wireVersion,
		Phase:           phaseOpenSource,
		SourceVMID:      request.SourceVMID,
		DestinationVMID: request.DestinationVMID,
	}, "open-source")
}

// DecodeOpenSource reads the exact first-phase VM-pair request.
func DecodeOpenSource(ctx context.Context, reader io.Reader) (OpenSource, error) {
	var zero OpenSource
	body, err := decodeAndValidateFrame(ctx, reader)
	if err != nil {
		return zero, err
	}
	fields, err := decodeClosedObject(body, openSourceFields, "open-source request")
	if err != nil {
		return zero, err
	}
	if err := decodePhaseIdentity(fields, phaseOpenSource); err != nil {
		return zero, err
	}
	sourceVMID, err := decodeField[string](fields["source_vm_id"], "source VM ID")
	if err != nil {
		return zero, err
	}
	destinationVMID, err := decodeField[string](fields["destination_vm_id"], "destination VM ID")
	if err != nil {
		return zero, err
	}
	request := OpenSource{SourceVMID: sourceVMID, DestinationVMID: destinationVMID}
	if err := request.Validate(); err != nil {
		return zero, err
	}
	return request, nil
}

// EncodeSourceRetained acknowledges that root retained and authenticated the source connection.
func EncodeSourceRetained(ctx context.Context, writer io.Writer) error {
	return encodeFixedPhase(ctx, writer, phaseSourceRetained)
}

// DecodeSourceRetained reads the exact source-retained acknowledgement.
func DecodeSourceRetained(ctx context.Context, reader io.Reader) error {
	return decodeFixedPhase(ctx, reader, phaseSourceRetained)
}

// EncodeBindDestination requests admission of the one destination descriptor attached to this phase.
func EncodeBindDestination(ctx context.Context, writer io.Writer) error {
	return encodeFixedPhase(ctx, writer, phaseBindDestination)
}

// DecodeBindDestination reads the exact destination-binding request.
func DecodeBindDestination(ctx context.Context, reader io.Reader) error {
	return decodeFixedPhase(ctx, reader, phaseBindDestination)
}

// EncodeReady acknowledges that root retains and authenticates both VM connections.
func EncodeReady(ctx context.Context, writer io.Writer) error {
	return encodeFixedPhase(ctx, writer, phaseReady)
}

// DecodeReady reads the exact ready acknowledgement.
func DecodeReady(ctx context.Context, reader io.Reader) error {
	return decodeFixedPhase(ctx, reader, phaseReady)
}

// EncodeRelease requests explicit release of the retained pair and capacity lease.
func EncodeRelease(ctx context.Context, writer io.Writer) error {
	return encodeFixedPhase(ctx, writer, phaseRelease)
}

// DecodeRelease reads the exact explicit-release request.
func DecodeRelease(ctx context.Context, reader io.Reader) error {
	return decodeFixedPhase(ctx, reader, phaseRelease)
}

// EncodeReleased writes the successful terminal release outcome.
func EncodeReleased(ctx context.Context, writer io.Writer) error {
	return encodeFixedPhase(ctx, writer, phaseReleased)
}

// EncodeFailure writes a terminal bounded failure without its Go cause or unsafe details.
func EncodeFailure(ctx context.Context, writer io.Writer, operationErr error) error {
	return encodeEnvelope(ctx, writer, failureEnvelope{
		SchemaVersion: wireVersion,
		Phase:         phaseFailure,
		Error:         normalizeDomainError(operationErr),
	}, "failure")
}

// DecodeOutcome reads exactly one released or failure terminal outcome.
func DecodeOutcome(ctx context.Context, reader io.Reader) (Outcome, error) {
	body, err := decodeAndValidateFrame(ctx, reader)
	if err != nil {
		return Outcome{}, err
	}
	phase, err := decodePhase(body)
	if err != nil {
		return Outcome{}, err
	}
	switch phase {
	case phaseReleased:
		fields, decodeErr := decodeClosedObject(body, fixedPhaseFields, "released outcome")
		if decodeErr != nil {
			return Outcome{}, decodeErr
		}
		if decodeErr := decodePhaseIdentity(fields, phaseReleased); decodeErr != nil {
			return Outcome{}, decodeErr
		}
		return Outcome{released: true}, nil
	case phaseFailure:
		fields, decodeErr := decodeClosedObject(body, failureFields, "failure outcome")
		if decodeErr != nil {
			return Outcome{}, decodeErr
		}
		if decodeErr := decodePhaseIdentity(fields, phaseFailure); decodeErr != nil {
			return Outcome{}, decodeErr
		}
		operationErr, decodeErr := decodeDomainError(fields["error"])
		if decodeErr != nil {
			return Outcome{}, decodeErr
		}
		return Outcome{operationError: operationErr}, nil
	default:
		return Outcome{}, validationError("retained VM-exec session outcome has an invalid phase")
	}
}

func encodeFixedPhase(ctx context.Context, writer io.Writer, phase string) error {
	return encodeEnvelope(ctx, writer, fixedPhaseEnvelope{SchemaVersion: wireVersion, Phase: phase}, phase)
}

func decodeFixedPhase(ctx context.Context, reader io.Reader, expectedPhase string) error {
	body, err := decodeAndValidateFrame(ctx, reader)
	if err != nil {
		return err
	}
	fields, err := decodeClosedObject(body, fixedPhaseFields, expectedPhase+" phase")
	if err != nil {
		return err
	}
	return decodePhaseIdentity(fields, expectedPhase)
}

// encodeEnvelope receives only the package's closed private envelope structs; json.Marshal requires an any value.
func encodeEnvelope(ctx context.Context, writer io.Writer, envelope any, description string) error {
	body, err := json.Marshal(envelope)
	if err != nil {
		return errs.WrapMsg(errs.CodeInternal, "encode retained VM-exec session "+description, err)
	}
	return encodeFrame(ctx, writer, body)
}

func decodeAndValidateFrame(ctx context.Context, reader io.Reader) ([]byte, error) {
	body, err := decodeFrame(ctx, reader)
	if err != nil {
		return nil, err
	}
	if err := validateStrictJSON(body); err != nil {
		return nil, err
	}
	return body, nil
}

func decodePhase(raw []byte) (string, error) {
	fields, err := decodeClosedObjectAllowing(raw, fixedPhaseFields, failureFields, "outcome")
	if err != nil {
		return "", err
	}
	return decodeField[string](fields["phase"], "outcome phase")
}

func decodePhaseIdentity(fields map[string]json.RawMessage, expectedPhase string) error {
	schemaVersion, err := decodeField[uint16](fields["schema_version"], "schema version")
	if err != nil {
		return err
	}
	if schemaVersion != wireVersion {
		return validationError("unsupported retained VM-exec session schema version")
	}
	phase, err := decodeField[string](fields["phase"], "phase")
	if err != nil {
		return err
	}
	if phase != expectedPhase {
		return validationError("retained VM-exec session phase does not match the expected protocol step")
	}
	return nil
}

func validLowerHexVMID(value string) bool {
	if len(value) != 32 {
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
