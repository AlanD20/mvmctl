package logging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/logging/rotating"
	"mvmctl/internal/lib/validators"
	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

const vmExecAuditSchemaVersion = 1

// VMExecAuditResult is the closed outcome set accepted by the VM-exec audit boundary.
type VMExecAuditResult string

const (
	VMExecAuditSucceeded     VMExecAuditResult = "succeeded"
	VMExecAuditCommandFailed VMExecAuditResult = "command_failed"
	VMExecAuditDenied        VMExecAuditResult = "denied"
	VMExecAuditCancelled     VMExecAuditResult = "cancelled"
	VMExecAuditTimedOut      VMExecAuditResult = "timed_out"
	VMExecAuditRelayFailed   VMExecAuditResult = "relay_failed"
)

// VMExecAuditRecord contains only the command-redacted fields required to audit one VM-to-VM execution attempt.
type VMExecAuditRecord struct {
	RequestID       string
	SourceVMID      string
	DestinationVMID string
	TargetUser      string
	StartedAt       time.Time
	Duration        time.Duration
	Result          VMExecAuditResult
	ExitCode        *int
}

type vmExecAuditWriter interface {
	WriteContext(context.Context, []byte) (int, error)
	Close() error
}

// VMExecAuditSink writes fixed-schema command-redacted VM-exec records to the canonical audit log.
type VMExecAuditSink struct {
	writer vmExecAuditWriter
}

type vmExecAuditWireRecord struct {
	SchemaVersion   int               `json:"schema_version"`
	Operation       string            `json:"operation"`
	RequestID       string            `json:"request_id"`
	SourceVMID      string            `json:"source_vm_id"`
	DestinationVMID string            `json:"destination_vm_id"`
	TargetUser      string            `json:"target_user"`
	StartedAt       string            `json:"started_at"`
	DurationMS      int64             `json:"duration_ms"`
	Result          VMExecAuditResult `json:"result"`
	ExitCode        *int              `json:"exit_code"`
}

// NewVMExecAuditSink opens the canonical audit log with strict error reporting.
func NewVMExecAuditSink(ctx context.Context) (*VMExecAuditSink, error) {
	if err := validateVMExecAuditContext(ctx); err != nil {
		return nil, err
	}
	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		return nil, vmExecAuditFailure("resolve VM-exec audit log", err)
	}
	return newVMExecAuditSink(ctx, filepath.Join(cacheDir, "audit.log"))
}

func newVMExecAuditSink(ctx context.Context, path string) (*VMExecAuditSink, error) {
	if err := validateVMExecAuditContext(ctx); err != nil {
		return nil, err
	}
	writer, err := rotating.NewRotatingFileWriter(ctx, path)
	if err != nil {
		return nil, vmExecAuditFailure("open VM-exec audit log", err)
	}
	return &VMExecAuditSink{writer: writer}, nil
}

// Record appends one complete fixed-schema audit record.
func (sink *VMExecAuditSink) Record(ctx context.Context, record VMExecAuditRecord) error {
	if err := validateVMExecAuditContext(ctx); err != nil {
		return err
	}
	if sink == nil || sink.writer == nil {
		return vmExecAuditFailure("write VM-exec audit record", errors.New("VM-exec audit writer is unavailable"))
	}
	if err := validateVMExecAuditRecord(record); err != nil {
		return err
	}

	value, err := json.Marshal(vmExecAuditWireRecord{
		SchemaVersion:   vmExecAuditSchemaVersion,
		Operation:       "vm_exec",
		RequestID:       record.RequestID,
		SourceVMID:      record.SourceVMID,
		DestinationVMID: record.DestinationVMID,
		TargetUser:      record.TargetUser,
		StartedAt:       record.StartedAt.UTC().Format(time.RFC3339),
		DurationMS:      record.Duration.Milliseconds(),
		Result:          record.Result,
		ExitCode:        record.ExitCode,
	})
	if err != nil {
		return vmExecAuditFailure("encode VM-exec audit record", err)
	}
	value = append(value, '\n')

	written, writeErr := sink.writer.WriteContext(ctx, value)
	if written != len(value) {
		writeErr = errors.Join(writeErr, io.ErrShortWrite)
	}
	if writeErr != nil {
		return vmExecAuditFailure("write VM-exec audit record", writeErr)
	}
	return nil
}

// Close releases the sink-owned audit writer. Cleanup runs even when ctx is already cancelled.
func (sink *VMExecAuditSink) Close(ctx context.Context) error {
	if ctx == nil {
		return vmExecAuditInvalid("VM-exec audit context is required")
	}
	if sink == nil || sink.writer == nil {
		return vmExecAuditFailure("close VM-exec audit log", errors.New("VM-exec audit writer is unavailable"))
	}
	closeErr := sink.writer.Close()
	if err := errors.Join(ctx.Err(), closeErr); err != nil {
		return vmExecAuditFailure("close VM-exec audit log", err)
	}
	return nil
}

func validateVMExecAuditContext(ctx context.Context) error {
	if ctx == nil {
		return vmExecAuditInvalid("VM-exec audit context is required")
	}
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(
			errs.CodeVsockAuditInvalid,
			"VM-exec audit context is not active",
			err,
			errs.WithClass(errs.ClassValidation),
		)
	}
	return nil
}

func validateVMExecAuditRecord(record VMExecAuditRecord) error {
	if !isVMExecAuditID(record.RequestID) {
		return vmExecAuditInvalid("VM-exec audit request ID must be exactly 32 lowercase hexadecimal characters")
	}
	if !isVMExecAuditID(record.SourceVMID) {
		return vmExecAuditInvalid("VM-exec audit source VM ID must be exactly 32 lowercase hexadecimal characters")
	}
	if !isVMExecAuditID(record.DestinationVMID) {
		return vmExecAuditInvalid("VM-exec audit destination VM ID must be exactly 32 lowercase hexadecimal characters")
	}
	if record.SourceVMID == record.DestinationVMID {
		return vmExecAuditInvalid("VM-exec audit source and destination VMs must differ")
	}
	if record.TargetUser == "root" || len(record.TargetUser) > 32 {
		return vmExecAuditInvalid("VM-exec audit target user must be a non-root SSH username of at most 32 bytes")
	}
	if err := validators.SSHUsername(record.TargetUser); err != nil {
		return vmExecAuditInvalid("VM-exec audit target user is invalid")
	}
	if record.StartedAt.IsZero() {
		return vmExecAuditInvalid("VM-exec audit start time is required")
	}
	if record.Duration < 0 || record.Duration > vmexecwire.MaxExecutionDuration {
		return vmExecAuditInvalid("VM-exec audit duration exceeds the fixed execution limit")
	}

	switch record.Result {
	case VMExecAuditSucceeded:
		if record.ExitCode == nil || *record.ExitCode != 0 {
			return vmExecAuditInvalid("successful VM-exec audit records require exit code zero")
		}
	case VMExecAuditCommandFailed:
		if record.ExitCode == nil || *record.ExitCode < 1 || *record.ExitCode > 255 {
			return vmExecAuditInvalid("failed VM-exec command audit records require exit code 1 through 255")
		}
	case VMExecAuditDenied, VMExecAuditCancelled, VMExecAuditTimedOut, VMExecAuditRelayFailed:
		if record.ExitCode != nil {
			return vmExecAuditInvalid("non-command VM-exec audit outcomes must not contain an exit code")
		}
	default:
		return vmExecAuditInvalid("VM-exec audit result is invalid")
	}
	return nil
}

func isVMExecAuditID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for index := range len(value) {
		if !strings.ContainsRune("0123456789abcdef", rune(value[index])) {
			return false
		}
	}
	return true
}

func vmExecAuditInvalid(message string) *errs.DomainError {
	return errs.New(errs.CodeVsockAuditInvalid, message)
}

func vmExecAuditFailure(message string, cause error) *errs.DomainError {
	return errs.WrapMsg(errs.CodeVsockAuditFailed, message, cause, errs.WithClass(errs.ClassInternal))
}
