// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"fmt"
	"time"

	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// ExecAPI defines the public interface for command execution inside VMs.
type ExecAPI interface {
	Exec(ctx context.Context, input inputs.ExecInput) (*results.ExecResult, error)
}

// Exec executes a command inside a VM via the vsock guest agent.
// If input.Command is empty, opens an interactive PTY shell session.
// For non-interactive execution, output is captured and returned as structured result.
// For interactive shell, I/O is connected directly to the terminal and no result is returned.
func (op *Operation) Exec(ctx context.Context, input inputs.ExecInput) (*results.ExecResult, error) {
	resolved, err := input.Resolve(ctx, op.Repos.VM, op.Repos.Vsock)
	if err != nil {
		return nil, err
	}
	// Read probe timeout from config (defaults.vm.vsock_probe_timeout in constants.go).
	// input.Timeout overrides the probe timeout when set (> 0). The command itself
	// runs with no absolute timeout — only context cancellation (e.g. Ctrl-C) stops it.
	probeTimeout, err := op.Services.Config.GetDuration(ctx, "defaults.vm", "vsock_probe_timeout")
	if err != nil || probeTimeout <= 0 {
		return nil, errs.New(
			errs.CodeInternal,
			"vsock_probe_timeout not configured — check defaults.vm.vsock_probe_timeout",
		)
	}
	if input.Timeout > 0 {
		probeTimeout = time.Duration(input.Timeout) * time.Second
	}
	client, err := op.newVsockClient(ctx, resolved.VsockItem, probeTimeout, resolved.VM.Name)
	if err != nil {
		return nil, err
	}
	// Interactive shell or captured exec
	if input.Command == "" {
		// Interactive shell session — no result returned since I/O is direct to terminal.
		if err := client.Shell(ctx, input.User); err != nil {
			return nil, errs.WrapMsg(
				errs.CodeVsockExecFailed,
				fmt.Sprintf("vsock shell session failed for vm '%s'", resolved.VM.Name),
				err,
			)
		}
		return nil, nil
	}
	user := resolved.User
	if user == "" {
		user, _ = op.Services.Config.GetString(ctx, "defaults.vm", "vsock_user")
	}
	// Pass 0 for command timeout — the probe timeout is handled by the client
	// during connect; the command itself runs with no absolute timeout.
	result, err := client.Exec(ctx, input.Command, user, 0, input.Env, input.NoSync)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeVsockExecFailed,
			fmt.Sprintf("vsock exec failed for vm '%s'", resolved.VM.Name),
			err,
		)
	}
	return &results.ExecResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	}, nil
}
