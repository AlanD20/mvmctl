// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/ssh_operations.py exactly.
package api

import (
	"context"
	"fmt"

	"mvmctl/internal/core/ssh"
	"mvmctl/internal/infra/errs"
	"mvmctl/pkg/api/inputs"
)

// SSHConnect opens SSH session or executes command on a VM.
// Matches Python's SSHOperation.connect() exactly.
// Python: returns OperationResult[int]; only MVMError is caught and wrapped
// in OperationResult; other exceptions propagate.
// Go: returns error. Non-MVMError errors are wrapped with code
// "ssh.failed" as well (Go has no exceptions, so all errors are returned).
func (op *Operation) SSHConnect(ctx context.Context, input *inputs.SSHInput) error {
	// Python: try:
	//   request = SSHRequest(inputs, db); resolved = request.resolve()
	//   ...
	// except MVMError as e:
	//   return OperationResult(status="error", code="ssh.failed", ...)
	request := inputs.NewSSHRequest(*input, op.Services.Config)
	resolved, err := request.Resolve(ctx, op.Repos.VM, op.Repos.Key)
	if err != nil {
		return newSSHError(err)
	}

	// Audit log (matches Python: AuditLog.log("vm.ssh", changes={"ip": ..., "user": ...}))
	op.AuditLog.LogOperation("vm.ssh", map[string]interface{}{
		"ip":   resolved.TargetIP,
		"user": resolved.User,
	}, "")

	// Create SSH service (matches Python: SSHService(ip=..., user=..., key_path=..., timeout=...))
	keyPath := ""
	if resolved.Key != nil {
		keyPath = *resolved.Key
	}
	svc, err := ssh.NewService(resolved.TargetIP, resolved.User, keyPath, resolved.Timeout)
	if err != nil {
		return newSSHError(err)
	}

	// Connect (matches Python: service.connect(command=..., exec_mode=resolved.cmd is None))
	command := ""
	if resolved.Cmd != nil {
		command = *resolved.Cmd
	}
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
	return &errs.DomainError{
		Code:    "ssh.failed",
		Op:      "ssh",
		Message: err.Error(),
		Err:     err,
		Class:   errs.ClassInternal,
	}
}
