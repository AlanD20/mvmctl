package vmexecwire

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"mvmctl/pkg/errs"
)

const (
	// SourceSessionMagic identifies the dedicated source-session bootstrap protocol.
	SourceSessionMagic = "MVMSRC01"
	// SourceSessionVersion is the only supported source-session wire and schema version.
	SourceSessionVersion = uint16(1)
	// MaxSourceBootstrapBytes bounds a source bootstrap or ready frame before allocation.
	MaxSourceBootstrapBytes = 512

	sourceFramePrefixBytes = len(SourceSessionMagic) + 2 + 4
)

// SourceBootstrap is the authenticated host-to-agent source-session opening request.
type SourceBootstrap struct {
	AgentToken      string
	SourceSessionID string
}

// SourceReady acknowledges registration of one exact source session.
type SourceReady struct {
	SourceSessionID string
}

type sourceBootstrapEnvelope struct {
	SchemaVersion   uint16 `json:"schema_version"`
	AgentToken      string `json:"agent_token"`
	SourceSessionID string `json:"source_session_id"`
}

type sourceReadyEnvelope struct {
	SchemaVersion   uint16 `json:"schema_version"`
	SourceSessionID string `json:"source_session_id"`
}

var sourceBootstrapFields = []string{"schema_version", "agent_token", "source_session_id"}
var sourceReadyFields = []string{"schema_version", "source_session_id"}

// NewRequestID returns a cryptographically random 16-byte lowercase hexadecimal request ID.
func NewRequestID() (string, error) {
	return newRandomWireID("request")
}

// NewSourceSessionID returns a cryptographically random 16-byte lowercase hexadecimal source-session ID.
func NewSourceSessionID() (string, error) {
	return newRandomWireID("source-session")
}

func newRandomWireID(kind string) (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", errs.WrapMsg(errs.CodeProcessError, "generate VM-exec "+kind+" ID", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// Validate enforces the closed source bootstrap contract.
func (bootstrap SourceBootstrap) Validate() error {
	if !canonicalUUIDV4.MatchString(bootstrap.AgentToken) {
		return validationError("source-session agent token must be a canonical lowercase UUIDv4")
	}
	if err := validateRequestID(bootstrap.SourceSessionID); err != nil {
		return validationError("source-session ID must be exactly 32 lowercase hexadecimal characters")
	}
	return nil
}

// Validate enforces the closed source ready contract.
func (ready SourceReady) Validate() error {
	if err := validateRequestID(ready.SourceSessionID); err != nil {
		return validationError("source-session ID must be exactly 32 lowercase hexadecimal characters")
	}
	return nil
}

// EncodeSourceBootstrap writes one bounded authenticated source-session opening frame.
func EncodeSourceBootstrap(ctx context.Context, writer io.Writer, bootstrap SourceBootstrap) error {
	if err := bootstrap.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(sourceBootstrapEnvelope{
		SchemaVersion:   SourceSessionVersion,
		AgentToken:      bootstrap.AgentToken,
		SourceSessionID: bootstrap.SourceSessionID,
	})
	if err != nil {
		return validationError("encode source-session bootstrap")
	}
	return encodeSourceSessionFrame(ctx, writer, body)
}

// DecodeSourceBootstrap reads one bounded authenticated source-session opening frame.
func DecodeSourceBootstrap(ctx context.Context, reader io.Reader) (SourceBootstrap, error) {
	body, err := decodeSourceSessionFrame(ctx, reader)
	if err != nil {
		return SourceBootstrap{}, err
	}
	if err := validateStrictJSON(body); err != nil {
		return SourceBootstrap{}, err
	}
	fields, err := decodeClosedObject(body, sourceBootstrapFields, "source-session bootstrap")
	if err != nil {
		return SourceBootstrap{}, err
	}
	if err := validateSourceSessionSchema(fields["schema_version"]); err != nil {
		return SourceBootstrap{}, err
	}
	token, err := decodeField[string](fields["agent_token"], "source-session agent token")
	if err != nil {
		return SourceBootstrap{}, err
	}
	sessionID, err := decodeField[string](fields["source_session_id"], "source-session ID")
	if err != nil {
		return SourceBootstrap{}, err
	}
	bootstrap := SourceBootstrap{AgentToken: token, SourceSessionID: sessionID}
	if err := bootstrap.Validate(); err != nil {
		return SourceBootstrap{}, err
	}
	return bootstrap, nil
}

// EncodeSourceReady writes one bounded source-session readiness frame.
func EncodeSourceReady(ctx context.Context, writer io.Writer, ready SourceReady) error {
	if err := ready.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(sourceReadyEnvelope{
		SchemaVersion:   SourceSessionVersion,
		SourceSessionID: ready.SourceSessionID,
	})
	if err != nil {
		return validationError("encode source-session ready")
	}
	return encodeSourceSessionFrame(ctx, writer, body)
}

// DecodeSourceReady reads one ready frame and binds it to the requested source-session ID.
func DecodeSourceReady(ctx context.Context, reader io.Reader, expectedSessionID string) (SourceReady, error) {
	if err := validateRequestID(expectedSessionID); err != nil {
		return SourceReady{}, validationError("expected source-session ID is invalid")
	}
	body, err := decodeSourceSessionFrame(ctx, reader)
	if err != nil {
		return SourceReady{}, err
	}
	if err := validateStrictJSON(body); err != nil {
		return SourceReady{}, err
	}
	fields, err := decodeClosedObject(body, sourceReadyFields, "source-session ready")
	if err != nil {
		return SourceReady{}, err
	}
	if err := validateSourceSessionSchema(fields["schema_version"]); err != nil {
		return SourceReady{}, err
	}
	sessionID, err := decodeField[string](fields["source_session_id"], "source-session ready ID")
	if err != nil {
		return SourceReady{}, err
	}
	ready := SourceReady{SourceSessionID: sessionID}
	if err := ready.Validate(); err != nil {
		return SourceReady{}, err
	}
	if ready.SourceSessionID != expectedSessionID {
		return SourceReady{}, validationError("source-session ready ID does not match the opening request")
	}
	return ready, nil
}

func encodeSourceSessionFrame(ctx context.Context, writer io.Writer, body []byte) error {
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write source-session frame", err)
	}
	if writer == nil {
		return validationError("source-session frame writer is required")
	}
	if len(body) == 0 || len(body) > MaxSourceBootstrapBytes {
		return validationError(fmt.Sprintf(
			"source-session frame must contain 1-%d bytes",
			MaxSourceBootstrapBytes,
		))
	}
	prefix := make([]byte, sourceFramePrefixBytes)
	copy(prefix, SourceSessionMagic)
	binary.BigEndian.PutUint16(prefix[len(SourceSessionMagic):], SourceSessionVersion)
	binary.BigEndian.PutUint32(prefix[len(SourceSessionMagic)+2:], uint32(len(body)))
	if err := writeAll(ctx, writer, prefix); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write source-session frame prefix", err)
	}
	if err := writeAll(ctx, writer, body); err != nil {
		return errs.WrapMsg(errs.CodeProcessError, "write source-session frame body", err)
	}
	return nil
}

func decodeSourceSessionFrame(ctx context.Context, reader io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.WrapMsg(errs.CodeProcessError, "read source-session frame", err)
	}
	if reader == nil {
		return nil, validationError("source-session frame reader is required")
	}
	prefix := make([]byte, sourceFramePrefixBytes)
	if _, err := io.ReadFull(reader, prefix); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "read source-session frame prefix", err)
	}
	if string(prefix[:len(SourceSessionMagic)]) != SourceSessionMagic {
		return nil, validationError("invalid source-session frame magic")
	}
	if binary.BigEndian.Uint16(prefix[len(SourceSessionMagic):len(SourceSessionMagic)+2]) != SourceSessionVersion {
		return nil, validationError("unsupported source-session frame version")
	}
	bodyLength := binary.BigEndian.Uint32(prefix[len(SourceSessionMagic)+2:])
	if bodyLength == 0 {
		return nil, validationError("source-session frame must not be empty")
	}
	if bodyLength > MaxSourceBootstrapBytes {
		return nil, validationError(fmt.Sprintf(
			"source-session frame exceeds %d bytes",
			MaxSourceBootstrapBytes,
		))
	}
	body := make([]byte, bodyLength)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, errs.WrapMsg(errs.CodeValidationFailed, "read source-session frame body", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.WrapMsg(errs.CodeProcessError, "read source-session frame", err)
	}
	return body, nil
}

func validateSourceSessionSchema(raw json.RawMessage) error {
	schema, err := decodeField[uint16](raw, "source-session schema version")
	if err != nil {
		return err
	}
	if schema != SourceSessionVersion {
		return validationError("unsupported source-session schema version")
	}
	return nil
}
