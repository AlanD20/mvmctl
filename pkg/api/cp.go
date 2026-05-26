// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/cp_operations.py exactly.
package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"mvmctl/internal/core/key"
	"mvmctl/internal/core/ssh"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api/inputs"
)

// CPOperation orchestrates file copy operations (host ↔ VM, VM ↔ VM).
// Matches Python's CPOperation exactly.
type CPOperation struct {
	cpSvc    *ssh.CPService
	vmRepo   vm.Repository
	db       *sql.DB
	cacheDir string
	keyRepo  key.Repository
}

// NewCPOperation creates a CPOperation.
func NewCPOperation(cpSvc *ssh.CPService, vmRepo vm.Repository, db *sql.DB, cacheDir string, keyRepo key.Repository) *CPOperation {
	return &CPOperation{
		cpSvc:    cpSvc,
		vmRepo:   vmRepo,
		db:       db,
		cacheDir: cacheDir,
		keyRepo:  keyRepo,
	}
}

// copyError converts a CPService error to OperationResult.
// Matches Python's except CPError as e: logger.debug("CP error: %s", e, exc_info=True)
// Returns *OperationResult so it can be used as a unified error return value
// from the Copy method's outer try/except pattern.
func (o *CPOperation) copyError(err error) *errs.OperationResult {
	slog.Debug("CP error", "error", err)
	errCode := "cp.failed"
	msg := err.Error()
	if de, ok := err.(*errs.DomainError); ok {
		if de.Code != "" {
			errCode = string(de.Code)
		}
		// Python: message=str(e) returns just the message, not the code-prefixed string
		if de.Message != "" {
			msg = de.Message
		}
	}
	return &errs.OperationResult{
		Status:    "error",
		Code:      errCode,
		Message:   msg,
		Exception: err,
	}
}

// Copy copies files between host and microVMs using tar-over-SSH.
// Matches Python's CPOperation.copy() exactly.
//
// Python: The entire method body is wrapped in "try: ... except CPError as e:"
// which catches all CPError exceptions (including CPDestinationNotDirectoryError)
// from resolution, validation, and copy operations. In Go, errors returned by
// the CopyToVM/CopyFromVM/CopyVMToVM methods are DomainError types that map
// to CPError. All CP-path errors (resolution, validation, copy) go through
// the unified copyError handler.
func (o *CPOperation) Copy(ctx context.Context, input *inputs.CPInput, onProgress func(int64)) *errs.OperationResult {
	// Python: try: ... except CPError as e: ...
	// Build CPRequest and resolve (matches Python: CPRequest(inputs, db).resolve())
	req := inputs.NewCPRequest(*input, o.db)
	resolved, err := req.Resolve(ctx, o.vmRepo, o.keyRepo)
	if err != nil {
		// Python: raises CPError during resolution → caught by except CPError.
		// Go: DomainError with CP error codes → unified error handler.
		return o.copyError(err)
	}

	// ── Validate destination is a directory for host_to_vm ──────────────
	// Python: Raises CPDestinationNotDirectoryError (subclass of CPError),
	// which is caught by the outer "except CPError". Validation happens
	// BEFORE audit log in the original Python order.
	if resolved.Direction == "host_to_vm" && resolved.DstInfo != nil {
		dstPath := resolved.DstInfo.RemotePath
		if dstPath != "" && !strings.HasSuffix(dstPath, "/") {
			// Python: raise CPDestinationNotDirectoryError(
			//   f"Destination path must be a directory (end with /). "
			//   f"Got: '{dst_path}'. "
			//   f"Use 'vm_name:/dest/dir/' to copy into a directory.",
			//   code="cp.destination_not_directory",
			// )
			// Go: create DomainError with the exact same message and code.
			return &errs.OperationResult{
				Status:  "error",
				Code:    "cp.destination_not_directory",
				Message: fmt.Sprintf("Destination path must be a directory (end with /). Got: '%s'. Use 'vm_name:/dest/dir/' to copy into a directory.", dstPath),
			}
		}
	}

	// Audit log (matches Python: AuditLog.log("cp.copy", changes={...}))
	// Python order: resolution → audit log → direction dispatch.
	// With destination validation moved before audit log.
	auditLog := logging.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("cp.copy", map[string]interface{}{
		"direction": resolved.Direction,
		"sources":   strings.Join(input.Sources, ", "),
		"dst":       input.Dst,
		"force":     input.Force,
	}, "")

	// Perform the copy (matches Python: CPService.copy_xxx(...) returns (total_bytes, message))
	var totalBytes int64
	var resultMessage string

	switch resolved.Direction {
	case "host_to_vm":
		if resolved.DstInfo == nil || resolved.LocalPaths == nil {
			return o.copyError(ssh.ErrCPFailed("Internal error: destination VM info not available"))
		}

		dstKeyPath := ""
		if resolved.DstInfo.KeyPath != nil {
			dstKeyPath = *resolved.DstInfo.KeyPath
		}
		totalBytes, resultMessage, err = o.cpSvc.CopyToVM(ctx, resolved.LocalPaths, resolved.DstInfo.RemotePath, model.ConnectionInfo{
			Host:    resolved.DstInfo.IP,
			User:    resolved.DstInfo.User,
			KeyPath: dstKeyPath,
		}, resolved.Force, func(bytes int64) {
			if onProgress != nil {
				onProgress(bytes)
			}
		})
		if err != nil {
			return o.copyError(err)
		}

	case "vm_to_host":
		if resolved.SrcInfo == nil || resolved.LocalPaths == nil {
			return o.copyError(ssh.ErrCPFailed("Internal error: source VM info not available"))
		}

		srcKeyPath := ""
		if resolved.SrcInfo.KeyPath != nil {
			srcKeyPath = *resolved.SrcInfo.KeyPath
		}
		totalBytes, resultMessage, err = o.cpSvc.CopyFromVM(ctx, resolved.SrcInfo.RemotePath, resolved.LocalPaths[0], model.ConnectionInfo{
			Host:    resolved.SrcInfo.IP,
			User:    resolved.SrcInfo.User,
			KeyPath: srcKeyPath,
		}, resolved.Force, func(bytes int64) {
			if onProgress != nil {
				onProgress(bytes)
			}
		})
		if err != nil {
			return o.copyError(err)
		}

	case "vm_to_vm":
		if resolved.SrcInfo == nil || resolved.DstInfo == nil {
			return o.copyError(ssh.ErrCPFailed("Internal error: source or destination VM info not available"))
		}

		srcKeyPath := ""
		if resolved.SrcInfo.KeyPath != nil {
			srcKeyPath = *resolved.SrcInfo.KeyPath
		}
		dstKeyPath2 := ""
		if resolved.DstInfo.KeyPath != nil {
			dstKeyPath2 = *resolved.DstInfo.KeyPath
		}
		totalBytes, resultMessage, err = o.cpSvc.CopyVMToVM(ctx,
			model.ConnectionInfo{
				Host:    resolved.SrcInfo.IP,
				User:    resolved.SrcInfo.User,
				KeyPath: srcKeyPath,
			},
			model.ConnectionInfo{
				Host:    resolved.DstInfo.IP,
				User:    resolved.DstInfo.User,
				KeyPath: dstKeyPath2,
			},
			resolved.SrcInfo.RemotePath,
			resolved.DstInfo.RemotePath,
			resolved.Force,
			func(bytes int64) {
				if onProgress != nil {
					onProgress(bytes)
				}
			},
		)
		if err != nil {
			return o.copyError(err)
		}

	default:
		return &errs.OperationResult{
			Status:  "error",
			Code:    "cp.failed",
			Message: fmt.Sprintf("Unknown copy direction: %s", resolved.Direction),
		}
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cp.success",
		Message: resultMessage,
		Item: map[string]interface{}{
			"bytes":   totalBytes,
			"message": resultMessage,
		},
	}
}

// Compile-time checks
var _ = slog.Default()
