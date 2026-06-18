// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"fmt"
	"mvmctl/internal/core/ssh"
	"mvmctl/internal/infra/event"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
	"time"
)

// SSHAPI defines the public interface for SSH operations.
type SSHAPI interface {
	SSHConnect(ctx context.Context, input inputs.SSHInput, onProgress event.OnProgressCallback) error
}

// SSHConnect opens an SSH session or executes a command on a VM.
// Returns an error. Non-DomainError errors are wrapped with code
// "ssh.failed".
// When onProgress is non-nil and a command is provided, SSH output is
// streamed line by line through the callback instead of being printed
// directly to the terminal. This allows the CLI layer to control display.
func (op *Operation) SSHConnect(ctx context.Context, input inputs.SSHInput, onProgress event.OnProgressCallback) error {
	request := inputs.NewSSHRequest(input, op.Services.Config)
	resolved, err := request.Resolve(ctx, op.Repos.VM, op.Repos.Key)
	if err != nil {
		return newSSHError(err)
	}
	// Audit log.
	op.AuditLog.LogOperation("vm.ssh", map[string]any{
		"ip":   resolved.TargetIP,
		"user": resolved.User,
	}, "")
	// Create SSH service.
	keyPath := ""
	if resolved.Key != nil {
		keyPath = *resolved.Key
	}
	timeout, _ := op.Services.Config.GetDuration(ctx, "settings.vm", "ssh_timeout_sec")
	if resolved.Timeout != nil && *resolved.Timeout > 0 {
		timeout = time.Duration(*resolved.Timeout) * time.Second
	}
	svc := ssh.NewService(resolved.TargetIP, resolved.User, keyPath, timeout)
	command := ""
	if resolved.Cmd != nil {
		command = *resolved.Cmd
	}
	// If onProgress is provided and we have a command, stream output line by line.
	// Otherwise fall back to Connect (direct terminal pipe).
	if onProgress != nil && command != "" {
		ch, streamErr := svc.StreamCommand(ctx, command)
		if streamErr != nil {
			return newSSHError(streamErr)
		}
		for line := range ch {
			if line.Err != nil {
				return newSSHError(line.Err)
			}
			onProgress(event.Progress{
				Phase:   "ssh",
				Status:  "running",
				Message: line.Line,
			})
		}
		return nil
	}
	// Connect.
	exitCode, err := svc.Connect(ctx, command, resolved.Cmd == nil)
	if err != nil {
		return newSSHError(err)
	}
	if exitCode != 0 {
		return newSSHError(fmt.Errorf("SSH command failed with exit code %d", exitCode))
	}
	return nil
}

// newSSHError wraps any error as a DomainError with "ssh.failed" code.
func newSSHError(err error) error {
	return errs.WrapMsg(errs.CodeSSHError, err.Error(), err, errs.WithClass(errs.ClassInternal))
}
