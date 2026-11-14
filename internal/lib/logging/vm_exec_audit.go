package logging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/logging/rotating"
	"mvmctl/internal/lib/validators"
	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

const (
	vmExecAuditSchemaVersion = 1
	vmExecAuditFileName      = "vm-exec-audit.jsonl"
)

var errVMExecAuditPrivilegedConstruction = errors.New("privileged vm-exec audit construction is forbidden")

// VMExecAuditResult is the closed outcome set accepted by the VM-exec audit boundary.
type VMExecAuditResult string

const (
	// VMExecAuditSucceeded records a command that completed with exit code zero.
	VMExecAuditSucceeded VMExecAuditResult = "succeeded"
	// VMExecAuditCommandFailed records a command that completed with a nonzero exit code.
	VMExecAuditCommandFailed VMExecAuditResult = "command_failed"
	// VMExecAuditDenied records an authorization or identity denial before command execution.
	VMExecAuditDenied VMExecAuditResult = "denied"
	// VMExecAuditCancelled records an execution cancelled by its source request.
	VMExecAuditCancelled VMExecAuditResult = "cancelled"
	// VMExecAuditTimedOut records an execution stopped by its fixed absolute duration bound.
	VMExecAuditTimedOut VMExecAuditResult = "timed_out"
	// VMExecAuditRelayFailed records a transport failure that prevented an authoritative command result.
	VMExecAuditRelayFailed VMExecAuditResult = "relay_failed"
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
	// ExitCode is zero for succeeded, 1 through 255 for command_failed, and nil for every other result.
	ExitCode *int
}

type vmExecAuditWriter interface {
	WriteContext(context.Context, []byte) (int, error)
	Close() error
}

// VMExecAuditSink writes fixed-schema command-redacted VM-exec records to a dedicated unprivileged JSONL log.
//
// THREAD-SAFE: a pointer-shared lifecycle gate serializes every record, poison, and close transition.
type VMExecAuditSink struct {
	writer    vmExecAuditWriter
	lifecycle *vmExecAuditLifecycle
}

type vmExecAuditLifecycle struct {
	// gate guards the writer and every field below across copied sink values.
	gate      chan struct{}
	closed    bool
	closeErr  error
	poisonErr error
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

// NewVMExecAuditSink opens the dedicated VM-exec JSONL log with strict error reporting.
// Privileged callers are rejected because the user-cache pathname is not retained descriptor-relatively.
func NewVMExecAuditSink(ctx context.Context) (*VMExecAuditSink, error) {
	if err := validateVMExecAuditContext(ctx); err != nil {
		return nil, err
	}
	if err := validateVMExecAuditOwner(os.Geteuid()); err != nil {
		return nil, err
	}
	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		return nil, vmExecAuditFailure("resolve vm-exec audit log", err)
	}
	return newVMExecAuditSinkInCache(ctx, cacheDir)
}

func newVMExecAuditSinkInCache(ctx context.Context, cacheDir string) (*VMExecAuditSink, error) {
	return newVMExecAuditSink(ctx, filepath.Join(cacheDir, vmExecAuditFileName))
}

func newVMExecAuditSink(ctx context.Context, path string) (*VMExecAuditSink, error) {
	if err := validateVMExecAuditContext(ctx); err != nil {
		return nil, err
	}
	writer, err := rotating.NewRotatingFileWriter(ctx, path)
	if err != nil {
		return nil, vmExecAuditFailure("open vm-exec audit log", err)
	}
	return newVMExecAuditSinkWithWriter(writer), nil
}

func newVMExecAuditSinkWithWriter(writer vmExecAuditWriter) *VMExecAuditSink {
	lifecycle := &vmExecAuditLifecycle{gate: make(chan struct{}, 1)}
	lifecycle.gate <- struct{}{}
	return &VMExecAuditSink{writer: writer, lifecycle: lifecycle}
}

// Record appends one complete fixed-schema audit record. A terminal cancellation or timeout caller must derive a
// fresh bounded context from context.WithoutCancel; Record deliberately never detaches caller cancellation itself.
func (sink *VMExecAuditSink) Record(ctx context.Context, record VMExecAuditRecord) error {
	if err := validateVMExecAuditContext(ctx); err != nil {
		return err
	}
	if err := sink.validateAvailable("write vm-exec audit record"); err != nil {
		return err
	}
	if err := sink.lifecycle.acquire(ctx); err != nil {
		return vmExecAuditContextError(err)
	}
	defer sink.lifecycle.release()
	if err := ctx.Err(); err != nil {
		return vmExecAuditContextError(err)
	}
	if sink.lifecycle.closed {
		return vmExecAuditFailure("write vm-exec audit record", os.ErrClosed)
	}
	if sink.lifecycle.poisonErr != nil {
		return sink.lifecycle.poisonErr
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
		return vmExecAuditFailure("encode vm-exec audit record", err)
	}
	value = append(value, '\n')

	written, writeErr := sink.writer.WriteContext(ctx, value)
	if written != len(value) {
		writeErr = errors.Join(writeErr, io.ErrShortWrite)
	}
	if writeErr != nil {
		sink.lifecycle.poisonErr = vmExecAuditFailure("write vm-exec audit record", writeErr)
		return sink.lifecycle.poisonErr
	}
	return nil
}

// Close releases the sink-owned audit writer exactly once and returns the cached first cleanup result.
// Cleanup runs even when ctx is already cancelled.
func (sink *VMExecAuditSink) Close(ctx context.Context) error {
	if ctx == nil {
		return vmExecAuditInvalid("vm-exec audit context is required")
	}
	if err := sink.validateAvailable("close vm-exec audit log"); err != nil {
		return err
	}
	if err := sink.lifecycle.acquire(context.WithoutCancel(ctx)); err != nil {
		return vmExecAuditFailure("close vm-exec audit log", err)
	}
	defer sink.lifecycle.release()
	if sink.lifecycle.closed {
		return sink.lifecycle.closeErr
	}
	sink.lifecycle.closed = true
	closeErr := sink.writer.Close()
	if closeErr != nil {
		sink.lifecycle.closeErr = vmExecAuditFailure("close vm-exec audit log", closeErr)
	}
	return sink.lifecycle.closeErr
}

func (sink *VMExecAuditSink) validateAvailable(operation string) error {
	if sink == nil || sink.writer == nil || sink.lifecycle == nil || sink.lifecycle.gate == nil {
		return vmExecAuditFailure(operation, errors.New("vm-exec audit writer is unavailable"))
	}
	return nil
}

func (lifecycle *vmExecAuditLifecycle) acquire(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-lifecycle.gate:
		return nil
	}
}

func (lifecycle *vmExecAuditLifecycle) release() {
	lifecycle.gate <- struct{}{}
}

func validateVMExecAuditContext(ctx context.Context) error {
	if ctx == nil {
		return vmExecAuditInvalid("vm-exec audit context is required")
	}
	if err := ctx.Err(); err != nil {
		return vmExecAuditContextError(err)
	}
	return nil
}

func validateVMExecAuditOwner(euid int) error {
	if euid == 0 {
		return vmExecAuditFailure("open vm-exec audit log", errVMExecAuditPrivilegedConstruction)
	}
	return nil
}

func vmExecAuditContextError(cause error) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeVsockAuditInvalid,
		"vm-exec audit context is not active",
		cause,
		errs.WithClass(errs.ClassValidation),
	)
}

func validateVMExecAuditRecord(record VMExecAuditRecord) error {
	if !isVMExecAuditID(record.RequestID) {
		return vmExecAuditInvalid("vm-exec audit request ID must be exactly 32 lowercase hexadecimal characters")
	}
	if !isVMExecAuditID(record.SourceVMID) {
		return vmExecAuditInvalid("vm-exec audit source VM ID must be exactly 32 lowercase hexadecimal characters")
	}
	if !isVMExecAuditID(record.DestinationVMID) {
		return vmExecAuditInvalid("vm-exec audit destination VM ID must be exactly 32 lowercase hexadecimal characters")
	}
	if record.SourceVMID == record.DestinationVMID {
		return vmExecAuditInvalid("vm-exec audit source and destination VMs must differ")
	}
	if record.TargetUser == "root" || len(record.TargetUser) > 32 {
		return vmExecAuditInvalid("vm-exec audit target user must be a non-root SSH username of at most 32 bytes")
	}
	if err := validators.SSHUsername(record.TargetUser); err != nil {
		return vmExecAuditInvalid("vm-exec audit target user is invalid")
	}
	if record.StartedAt.IsZero() {
		return vmExecAuditInvalid("vm-exec audit start time is required")
	}
	if record.Duration < 0 {
		return vmExecAuditInvalid("vm-exec audit duration must not be negative")
	}
	if record.Duration > vmexecwire.MaxExecutionDuration {
		return vmExecAuditInvalid("vm-exec audit duration exceeds the fixed execution limit")
	}

	switch record.Result {
	case VMExecAuditSucceeded:
		if record.ExitCode == nil || *record.ExitCode != 0 {
			return vmExecAuditInvalid("successful vm-exec audit records require exit code zero")
		}
	case VMExecAuditCommandFailed:
		if record.ExitCode == nil || *record.ExitCode < 1 || *record.ExitCode > 255 {
			return vmExecAuditInvalid("failed vm-exec command audit records require exit code 1 through 255")
		}
	case VMExecAuditDenied, VMExecAuditCancelled, VMExecAuditTimedOut, VMExecAuditRelayFailed:
		if record.ExitCode != nil {
			return vmExecAuditInvalid("non-command vm-exec audit outcomes must not contain an exit code")
		}
	default:
		return vmExecAuditInvalid("vm-exec audit result is invalid")
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
