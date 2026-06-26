// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"mvmctl/internal/core/ssh"
	"mvmctl/internal/infra/event"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
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
	resolved, err := input.Resolve(ctx, op.Services.Config, op.Repos.VM, op.Repos.Key)
	if err != nil {
		return errs.WrapMsg(errs.CodeSSHError, err.Error(), err, errs.WithClass(errs.ClassInternal))
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
	// Prepend environment variables to the command using POSIX env utility.
	if len(input.Env) > 0 {
		var parts []string
		keys := make([]string, 0, len(input.Env))
		for k := range input.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, input.Env[k]))
		}
		command = fmt.Sprintf("env %s %s", strings.Join(parts, " "), command)
	}
	// If onProgress is provided and we have a command, stream output line by line.
	// Otherwise fall back to Connect (direct terminal pipe).
	if onProgress != nil && command != "" {
		ch, streamErr := svc.StreamCommand(ctx, command)
		if streamErr != nil {
			return errs.WrapMsg(errs.CodeSSHError, streamErr.Error(), streamErr, errs.WithClass(errs.ClassInternal))
		}
		for line := range ch {
			if line.Err != nil {
				return errs.WrapMsg(errs.CodeSSHError, line.Err.Error(), line.Err, errs.WithClass(errs.ClassInternal))
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
		return errs.WrapMsg(errs.CodeSSHError, err.Error(), err, errs.WithClass(errs.ClassInternal))
	}
	if exitCode != 0 {
		exitErr := fmt.Errorf("SSH command failed with exit code %d", exitCode)
		return errs.WrapMsg(errs.CodeSSHError, exitErr.Error(), exitErr, errs.WithClass(errs.ClassInternal))
	}
	return nil
}
