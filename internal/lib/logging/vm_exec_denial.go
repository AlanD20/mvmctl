package logging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"mvmctl/internal/lib/validators"
	"mvmctl/internal/lib/vmexecwire"
)

// VMExecDenialReason is the closed pre-authorization identity rejection set.
type VMExecDenialReason string

const (
	// VMExecDestinationUnresolved means no exact destination VM could be resolved.
	VMExecDestinationUnresolved VMExecDenialReason = "destination_unresolved"
	// VMExecDestinationIsSource means resolution selected the source VM itself.
	VMExecDestinationIsSource VMExecDenialReason = "destination_is_source"
)

// VMExecDenialRecord records a rejected selector without inventing a destination identity.
// It is separate from VMExecAuditRecord so successful execution always requires two exact VM IDs.
type VMExecDenialRecord struct {
	RequestID           string
	SourceVMID          string
	DestinationSelector string
	TargetUser          string
	StartedAt           time.Time
	Duration            time.Duration
	Reason              VMExecDenialReason
}

// RecordDenial appends one closed pre-authorization denial to the same dedicated audit log.
func (sink *VMExecAuditSink) RecordDenial(ctx context.Context, record VMExecDenialRecord) error {
	if err := validateVMExecAuditContext(ctx); err != nil {
		return err
	}
	if err := sink.validateAvailable("write vm-exec denial record"); err != nil {
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
		return vmExecAuditFailure("write vm-exec denial record", os.ErrClosed)
	}
	if sink.lifecycle.poisonErr != nil {
		return sink.lifecycle.poisonErr
	}
	if err := validateVMExecDenialRecord(record); err != nil {
		return err
	}
	value, err := json.Marshal(struct {
		SchemaVersion       int                `json:"schema_version"`
		Operation           string             `json:"operation"`
		RequestID           string             `json:"request_id"`
		SourceVMID          string             `json:"source_vm_id"`
		DestinationSelector string             `json:"destination_selector"`
		TargetUser          string             `json:"target_user"`
		StartedAt           string             `json:"started_at"`
		DurationMS          int64              `json:"duration_ms"`
		Result              VMExecAuditResult  `json:"result"`
		Reason              VMExecDenialReason `json:"reason"`
	}{
		SchemaVersion: vmExecAuditSchemaVersion, Operation: "vm_exec_denial",
		RequestID: record.RequestID, SourceVMID: record.SourceVMID,
		DestinationSelector: record.DestinationSelector, TargetUser: record.TargetUser,
		StartedAt: record.StartedAt.UTC().Format(time.RFC3339), DurationMS: record.Duration.Milliseconds(),
		Result: VMExecAuditDenied, Reason: record.Reason,
	})
	if err != nil {
		return vmExecAuditFailure("encode vm-exec denial record", err)
	}
	value = append(value, '\n')
	written, writeErr := sink.writer.WriteContext(ctx, value)
	if written != len(value) {
		writeErr = errors.Join(writeErr, io.ErrShortWrite)
	}
	if writeErr != nil {
		sink.lifecycle.poisonErr = vmExecAuditFailure("write vm-exec denial record", writeErr)
		return sink.lifecycle.poisonErr
	}
	return nil
}

func validateVMExecDenialRecord(record VMExecDenialRecord) error {
	if !isVMExecAuditID(record.RequestID) || !isVMExecAuditID(record.SourceVMID) {
		return vmExecAuditInvalid("vm-exec denial requires exact request and source identities")
	}
	if record.Reason != VMExecDestinationUnresolved && record.Reason != VMExecDestinationIsSource {
		return vmExecAuditInvalid("vm-exec denial reason is invalid")
	}
	// Only selector syntax is observable. Arbitrary request text, commands, and tokens stay redacted.
	selector := record.DestinationSelector
	if len(selector) < 1 || len(selector) > 64 || strings.ContainsAny(selector, " \t\r\n/") {
		return vmExecAuditInvalid("vm-exec denial destination selector is invalid")
	}
	if !validators.IsIPAddress(selector) && !validators.ValidMACRegex.MatchString(selector) {
		if err := validators.EntityName(selector, "VM", 63); err != nil {
			return vmExecAuditInvalid("vm-exec denial destination selector is invalid")
		}
	}
	if record.TargetUser == "root" || len(record.TargetUser) > 32 {
		return vmExecAuditInvalid("vm-exec denial requires a non-root target user")
	}
	if err := validators.SSHUsername(record.TargetUser); err != nil {
		return vmExecAuditInvalid("vm-exec denial target user is invalid")
	}
	if record.StartedAt.IsZero() || record.Duration < 0 || record.Duration > vmexecwire.MaxExecutionDuration {
		return vmExecAuditInvalid("vm-exec denial timing is invalid")
	}
	return nil
}
