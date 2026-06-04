// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/cp_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"mvmctl/internal/core/ssh"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"
)

// copyError converts a CPService error to a DomainError.
// Matches Python's except CPError as e: logger.debug("CP error: %s", e, exc_info=True)
func (op *Operation) copyError(err error) error {
	slog.Debug("CP error", "error", err)
	return err
}

// CPCopy copies files between host and microVMs using tar-over-SSH.
// Matches Python's CPOperation.copy() exactly.
//
// Python: The entire method body is wrapped in "try: ... except CPError as e:"
// which catches all CPError exceptions (including CPDestinationNotDirectoryError)
// from resolution, validation, and copy operations. In Go, errors returned by
// the CopyToVM/CopyFromVM/CopyVMToVM methods are DomainError types that map
// to CPError. All CP-path errors (resolution, validation, copy) go through
// the unified copyError handler.
func (op *Operation) CPCopy(
	ctx context.Context,
	input inputs.CPInput,
	onProgress func(int64),
) (*responses.CPCopyResult, error) {
	// Python: try: ... except CPError as e: ...
	// Build CPRequest and resolve (matches Python: CPRequest(inputs, db).resolve())
	req := inputs.NewCPRequest(input, op.Services.Config)
	resolved, err := req.Resolve(ctx, op.Repos.VM, op.Repos.Key)
	if err != nil {
		return nil, op.copyError(err)
	}

	// ── Validate destination is a directory for host_to_vm ──────────────
	// Python: Raises CPDestinationNotDirectoryError (subclass of CPError),
	// which is caught by the outer "except CPError". Validation happens
	// BEFORE audit log in the original Python order.
	if resolved.Direction == "host_to_vm" && resolved.DstInfo != nil {
		dstPath := resolved.DstInfo.RemotePath
		if dstPath != "" && !strings.HasSuffix(dstPath, "/") {
			return nil, &errs.DomainError{
				Code: errs.CodeCPDestinationNotDir,
				Op:   "cp",
				Message: fmt.Sprintf(
					"Destination path must be a directory (end with /). Got: '%s'. Use 'vm_name:/dest/dir/' to copy into a directory.",
					dstPath,
				),
				Class: errs.ClassValidation,
			}
		}
	}

	// Audit log (matches Python: AuditLog.log("cp.copy", changes={...}))
	// Python order: resolution → audit log → direction dispatch.
	// With destination validation moved before audit log.
	op.AuditLog.LogOperation("cp.copy", map[string]interface{}{
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
			return nil, op.copyError(ssh.ErrCPFailed("Internal error: destination VM info not available"))
		}

		dstKeyPath := ""
		if resolved.DstInfo.KeyPath != nil {
			dstKeyPath = *resolved.DstInfo.KeyPath
		}
		totalBytes, resultMessage, err = op.Services.CP.CopyToVM(
			ctx,
			resolved.LocalPaths,
			resolved.DstInfo.RemotePath,
			model.ConnectionInfo{
				Host:    resolved.DstInfo.IP,
				User:    resolved.DstInfo.User,
				KeyPath: dstKeyPath,
			},
			resolved.Force,
			func(bytes int64) {
				if onProgress != nil {
					onProgress(bytes)
				}
			},
		)
		if err != nil {
			return nil, op.copyError(err)
		}

	case "vm_to_host":
		if resolved.SrcInfo == nil || resolved.LocalPaths == nil {
			return nil, op.copyError(ssh.ErrCPFailed("Internal error: source VM info not available"))
		}

		srcKeyPath := ""
		if resolved.SrcInfo.KeyPath != nil {
			srcKeyPath = *resolved.SrcInfo.KeyPath
		}
		totalBytes, resultMessage, err = op.Services.CP.CopyFromVM(
			ctx,
			resolved.SrcInfo.RemotePath,
			resolved.LocalPaths[0],
			model.ConnectionInfo{
				Host:    resolved.SrcInfo.IP,
				User:    resolved.SrcInfo.User,
				KeyPath: srcKeyPath,
			},
			resolved.Force,
			func(bytes int64) {
				if onProgress != nil {
					onProgress(bytes)
				}
			},
		)
		if err != nil {
			return nil, op.copyError(err)
		}

	case "vm_to_vm":
		if resolved.SrcInfo == nil || resolved.DstInfo == nil {
			return nil, op.copyError(ssh.ErrCPFailed("Internal error: source or destination VM info not available"))
		}

		srcKeyPath := ""
		if resolved.SrcInfo.KeyPath != nil {
			srcKeyPath = *resolved.SrcInfo.KeyPath
		}
		dstKeyPath2 := ""
		if resolved.DstInfo.KeyPath != nil {
			dstKeyPath2 = *resolved.DstInfo.KeyPath
		}
		totalBytes, resultMessage, err = op.Services.CP.CopyVMToVM(ctx,
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
			return nil, op.copyError(err)
		}

	default:
		return nil, &errs.DomainError{
			Code:    "cp.failed",
			Op:      "cp",
			Message: fmt.Sprintf("Unknown copy direction: %s", resolved.Direction),
			Class:   errs.ClassInternal,
		}
	}

	return &responses.CPCopyResult{
		Bytes:   totalBytes,
		Message: resultMessage,
	}, nil
}

// Compile-time checks
