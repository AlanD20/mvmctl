package vmexecwire

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"
)

const (
	frameTypeStdout = "stdout"
	frameTypeStderr = "stderr"
	frameTypeResult = "result"
	frameTypeError  = "error"
	frameTypeCancel = "cancel"
)

// ResponseFrame is one closed response-stream member. Only this package can implement it.
type ResponseFrame interface {
	responseFrame()
}

// StdoutFrame carries one bounded stdout byte chunk.
type StdoutFrame struct {
	Data []byte
}

func (StdoutFrame) responseFrame() {}

// StderrFrame carries one bounded stderr byte chunk.
type StderrFrame struct {
	Data []byte
}

func (StderrFrame) responseFrame() {}

// ResultFrame is the successful terminal response for one execution.
type ResultFrame struct {
	ExitCode int
	Duration time.Duration
}

func (ResultFrame) responseFrame() {}

// ErrorFrame is the failed terminal response for one execution.
type ErrorFrame struct {
	Message string
}

func (ErrorFrame) responseFrame() {}

type outputEnvelope struct {
	SchemaVersion uint16 `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Type          string `json:"type"`
	Data          []byte `json:"data"`
}

type resultEnvelope struct {
	SchemaVersion uint16 `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Type          string `json:"type"`
	ExitCode      int    `json:"exit_code"`
	DurationMS    int64  `json:"duration_ms"`
}

type errorEnvelope struct {
	SchemaVersion uint16 `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Type          string `json:"type"`
	Message       string `json:"message"`
}

type cancelEnvelope struct {
	SchemaVersion uint16 `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Type          string `json:"type"`
}

var outputFrameFields = []string{"schema_version", "request_id", "type", "data"}
var resultFrameFields = []string{"schema_version", "request_id", "type", "exit_code", "duration_ms"}
var errorFrameFields = []string{"schema_version", "request_id", "type", "message"}
var cancelFrameFields = []string{"schema_version", "request_id", "type"}

// ResponseWriter serializes one response stream and enforces its output and terminal bounds.
type ResponseWriter struct {
	mu          sync.Mutex
	writer      io.Writer
	requestID   string
	outputBytes int
	terminal    bool
}

// NewResponseWriter creates a closed response writer for one request identity.
func NewResponseWriter(writer io.Writer, requestID string) (*ResponseWriter, error) {
	if writer == nil {
		return nil, validationError("VM-exec response writer is required")
	}
	if err := validateRequestID(requestID); err != nil {
		return nil, err
	}
	return &ResponseWriter{writer: writer, requestID: requestID}, nil
}

// WriteStdout writes one stdout frame.
func (writer *ResponseWriter) WriteStdout(ctx context.Context, data []byte) error {
	return writer.writeOutput(ctx, frameTypeStdout, data)
}

// WriteStderr writes one stderr frame.
func (writer *ResponseWriter) WriteStderr(ctx context.Context, data []byte) error {
	return writer.writeOutput(ctx, frameTypeStderr, data)
}

func (writer *ResponseWriter) writeOutput(ctx context.Context, frameType string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.terminal {
		return validationError("VM-exec response output is not allowed after terminal response")
	}
	if len(data) == 0 {
		return validationError("VM-exec response output frame must not be empty")
	}
	if len(data) > MaxOutputBytes-writer.outputBytes {
		return validationError("VM-exec response exceeds combined output limit")
	}
	body, err := json.Marshal(outputEnvelope{
		SchemaVersion: wireVersion, RequestID: writer.requestID, Type: frameType, Data: data,
	})
	if err != nil {
		return validationError("encode VM-exec output frame")
	}
	if err := encodeFrame(ctx, writer.writer, body); err != nil {
		return err
	}
	writer.outputBytes += len(data)
	return nil
}

// WriteResult writes the only successful terminal response.
func (writer *ResponseWriter) WriteResult(ctx context.Context, exitCode int, duration time.Duration) error {
	if err := validateResult(exitCode, duration); err != nil {
		return err
	}
	body, err := json.Marshal(resultEnvelope{
		SchemaVersion: wireVersion,
		RequestID:     writer.requestID,
		Type:          frameTypeResult,
		ExitCode:      exitCode,
		DurationMS:    duration.Milliseconds(),
	})
	if err != nil {
		return validationError("encode VM-exec result frame")
	}
	return writer.writeTerminal(ctx, body)
}

// WriteError writes the only failed terminal response.
func (writer *ResponseWriter) WriteError(ctx context.Context, message string) error {
	if !validText(message, maxErrorBytes, false) {
		return validationError("VM-exec error message must be 1-1024 bytes without control characters")
	}
	body, err := json.Marshal(errorEnvelope{
		SchemaVersion: wireVersion, RequestID: writer.requestID, Type: frameTypeError, Message: message,
	})
	if err != nil {
		return validationError("encode VM-exec error frame")
	}
	return writer.writeTerminal(ctx, body)
}

func (writer *ResponseWriter) writeTerminal(ctx context.Context, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.terminal {
		return validationError("VM-exec response already contains a terminal frame")
	}
	if err := encodeFrame(ctx, writer.writer, body); err != nil {
		return err
	}
	writer.terminal = true
	return nil
}

// ResponseReader validates one complete response stream for one request identity.
type ResponseReader struct {
	mu          sync.Mutex
	reader      io.Reader
	requestID   string
	outputBytes int
	terminal    bool
	complete    bool
}

// NewResponseReader creates a closed response reader for one request identity.
func NewResponseReader(reader io.Reader, requestID string) (*ResponseReader, error) {
	if reader == nil {
		return nil, validationError("VM-exec response reader is required")
	}
	if err := validateRequestID(requestID); err != nil {
		return nil, err
	}
	return &ResponseReader{reader: reader, requestID: requestID}, nil
}

// Read returns the next validated response. After a terminal response it requires transport EOF.
func (reader *ResponseReader) Read(ctx context.Context) (ResponseFrame, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reader.mu.Lock()
	defer reader.mu.Unlock()
	if reader.complete {
		return nil, io.EOF
	}
	if reader.terminal {
		if err := requireWireEOF(ctx, reader.reader); err != nil {
			return nil, err
		}
		reader.complete = true
		return nil, io.EOF
	}

	body, err := decodeFrame(ctx, reader.reader)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, validationError("VM-exec response stream ended before terminal response")
		}
		return nil, err
	}
	frame, outputBytes, terminal, err := decodeResponseFrame(body, reader.requestID)
	if err != nil {
		return nil, err
	}
	if outputBytes > MaxOutputBytes-reader.outputBytes {
		return nil, validationError("VM-exec response exceeds combined output limit")
	}
	reader.outputBytes += outputBytes
	reader.terminal = terminal
	return frame, nil
}

func decodeResponseFrame(body []byte, expectedRequestID string) (ResponseFrame, int, bool, error) {
	if err := validateStrictJSON(body); err != nil {
		return nil, 0, false, err
	}
	fields, frameType, err := decodeFrameIdentity(body, expectedRequestID)
	if err != nil {
		return nil, 0, false, err
	}
	switch frameType {
	case frameTypeStdout, frameTypeStderr:
		fields, err = decodeClosedObject(body, outputFrameFields, "output frame")
		if err != nil {
			return nil, 0, false, err
		}
		data, decodeErr := decodeField[[]byte](fields["data"], "output data")
		if decodeErr != nil {
			return nil, 0, false, decodeErr
		}
		if len(data) == 0 {
			return nil, 0, false, validationError("VM-exec response output frame must not be empty")
		}
		if frameType == frameTypeStdout {
			return StdoutFrame{Data: data}, len(data), false, nil
		}
		return StderrFrame{Data: data}, len(data), false, nil
	case frameTypeResult:
		fields, err = decodeClosedObject(body, resultFrameFields, "result frame")
		if err != nil {
			return nil, 0, false, err
		}
		exitCode, decodeErr := decodeField[int](fields["exit_code"], "result exit code")
		if decodeErr != nil {
			return nil, 0, false, decodeErr
		}
		durationMS, decodeErr := decodeField[int64](fields["duration_ms"], "result duration")
		if decodeErr != nil {
			return nil, 0, false, decodeErr
		}
		if durationMS < 0 || durationMS > MaxExecutionDuration.Milliseconds() {
			return nil, 0, false, validationError("VM-exec result duration exceeds the fixed execution limit")
		}
		duration := time.Duration(durationMS) * time.Millisecond
		if err := validateResult(exitCode, duration); err != nil {
			return nil, 0, false, err
		}
		return ResultFrame{ExitCode: exitCode, Duration: duration}, 0, true, nil
	case frameTypeError:
		fields, err = decodeClosedObject(body, errorFrameFields, "error frame")
		if err != nil {
			return nil, 0, false, err
		}
		message, decodeErr := decodeField[string](fields["message"], "error message")
		if decodeErr != nil {
			return nil, 0, false, decodeErr
		}
		if !validText(message, maxErrorBytes, false) {
			return nil, 0, false, validationError(
				"VM-exec error message must be 1-1024 bytes without control characters",
			)
		}
		return ErrorFrame{Message: message}, 0, true, nil
	case frameTypeCancel:
		if _, err := decodeClosedObject(body, cancelFrameFields, "cancel frame"); err != nil {
			return nil, 0, false, err
		}
		return nil, 0, false, validationError("VM-exec cancel frame is not a response")
	default:
		return nil, 0, false, validationError("unsupported VM-exec response frame type")
	}
}

func decodeFrameIdentity(
	body []byte,
	expectedRequestID string,
) (map[string]json.RawMessage, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, "", validationError("decode VM-exec response frame")
	}
	if fields == nil {
		return nil, "", validationError("VM-exec response frame must be a JSON object")
	}
	for _, required := range []string{"schema_version", "request_id", "type"} {
		if _, ok := fields[required]; !ok {
			return nil, "", validationError("VM-exec response frame is missing required identity field")
		}
	}
	if err := validateSchema(fields["schema_version"]); err != nil {
		return nil, "", err
	}
	requestID, err := decodeField[string](fields["request_id"], "response request ID")
	if err != nil {
		return nil, "", err
	}
	if err := validateRequestID(requestID); err != nil {
		return nil, "", err
	}
	if requestID != expectedRequestID {
		return nil, "", validationError("VM-exec response request ID does not match the active request")
	}
	frameType, err := decodeField[string](fields["type"], "response frame type")
	if err != nil {
		return nil, "", err
	}
	return fields, frameType, nil
}

func validateResult(exitCode int, duration time.Duration) error {
	if exitCode < 0 || exitCode > 255 {
		return validationError("VM-exec result exit code must be between 0 and 255")
	}
	if duration < 0 || duration > MaxExecutionDuration {
		return validationError("VM-exec result duration exceeds the fixed execution limit")
	}
	return nil
}

// WriteCancel writes one matching cancellation control frame.
func WriteCancel(ctx context.Context, writer io.Writer, requestID string) error {
	if err := validateRequestID(requestID); err != nil {
		return err
	}
	body, err := json.Marshal(cancelEnvelope{
		SchemaVersion: wireVersion, RequestID: requestID, Type: frameTypeCancel,
	})
	if err != nil {
		return validationError("encode VM-exec cancel frame")
	}
	return encodeFrame(ctx, writer, body)
}

// DecodeCancel reads one exact cancellation control frame for the active request.
func DecodeCancel(ctx context.Context, reader io.Reader, expectedRequestID string) error {
	if err := validateRequestID(expectedRequestID); err != nil {
		return err
	}
	body, err := decodeFrame(ctx, reader)
	if err != nil {
		return err
	}
	if err := validateStrictJSON(body); err != nil {
		return err
	}
	_, frameType, err := decodeFrameIdentity(body, expectedRequestID)
	if err != nil {
		return err
	}
	if frameType != frameTypeCancel {
		return validationError("VM-exec control frame must be cancel")
	}
	_, err = decodeClosedObject(body, cancelFrameFields, "cancel frame")
	return err
}

func requireWireEOF(ctx context.Context, reader io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var trailing [1]byte
	read, err := reader.Read(trailing[:])
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if read > 0 || err == nil {
		return validationError("VM-exec response contains data after terminal frame")
	}
	if !errors.Is(err, io.EOF) {
		return validationError("read VM-exec response EOF")
	}
	return nil
}
