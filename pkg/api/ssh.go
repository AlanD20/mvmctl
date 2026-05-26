// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/ssh_operations.py exactly.
package api

import (
	"context"
	"database/sql"
	"fmt"

	"mvmctl/internal/core/key"
	"mvmctl/internal/core/ssh"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/pkg/api/inputs"
)

// SSHOperation orchestrates SSH connections.
// Matches Python's SSHOperation class exactly.
type SSHOperation struct {
	db       *sql.DB
	vmRepo   vm.Repository
	keyRepo  key.Repository
	cacheDir string
}

// NewSSHOperation creates an SSHOperation.
func NewSSHOperation(db *sql.DB, vmRepo vm.Repository, keyRepo key.Repository, cacheDir string) *SSHOperation {
	return &SSHOperation{
		db:       db,
		vmRepo:   vmRepo,
		keyRepo:  keyRepo,
		cacheDir: cacheDir,
	}
}

// Connect opens SSH session or executes command on a VM.
// Matches Python's SSHOperation.connect() exactly.
// Python: returns OperationResult[int]; only MVMError is caught and wrapped
// in OperationResult; other exceptions propagate.
// Go: returns *OperationResult. Non-MVMError errors are wrapped with code
// "ssh.failed" as well (Go has no exceptions, so all errors are returned).
func (o *SSHOperation) Connect(ctx context.Context, input *inputs.SSHInput) *errs.OperationResult {
	// Python: try:
	//   request = SSHRequest(inputs, db); resolved = request.resolve()
	//   ...
	// except MVMError as e:
	//   return OperationResult(status="error", code="ssh.failed", ...)
	request := inputs.NewSSHRequest(*input, o.db)
	resolved, err := request.Resolve(ctx, o.vmRepo, o.keyRepo)
	if err != nil {
		// Python: except MVMError — only MVMError is caught.
		// Other exceptions propagate. In Go, we return the result directly.
		return newSSHError(err)
	}

	// Audit log (matches Python: AuditLog.log("vm.ssh", changes={"ip": ..., "user": ...}))
	auditLog := logging.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("vm.ssh", map[string]interface{}{
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

	if exitCode == 0 {
		return &errs.OperationResult{
			Status:  "success",
			Code:    "ssh.connected",
			Message: "SSH connection successful",
			Item:    exitCode,
		}
	}
	return &errs.OperationResult{
		Status:  "error",
		Code:    "ssh.failed",
		Message: fmt.Sprintf("SSH command failed with exit code %d", exitCode),
		Item:    exitCode,
	}
}

// newSSHError wraps any error as an OperationResult with "ssh.failed" code.
// Matches Python's except MVMError: return OperationResult(status="error",
// code="ssh.failed", message=str(e), exception=e).
func newSSHError(err error) *errs.OperationResult {
	return &errs.OperationResult{
		Status:    "error",
		Code:      "ssh.failed",
		Message:   err.Error(),
		Exception: err,
	}
}
