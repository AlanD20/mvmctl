// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/cp_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mvmctl/internal/core/vsock"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// CPAPI defines the public interface for copy file operations.
type CPAPI interface {
	CPCopy(
		ctx context.Context,
		input inputs.CPInput,
		onProgress event.OnDownloadCallback,
	) (*results.CPCopyResult, error)
}

// CPCopy copies files between host and microVMs using vsock binary frame protocol.
// Matches Python's CPOperation.copy().
func (op *Operation) CPCopy(
	ctx context.Context,
	input inputs.CPInput,
	onProgress event.OnDownloadCallback,
) (*results.CPCopyResult, error) {
	// Build CPRequest and resolve.
	req := inputs.NewCPRequest(input, op.Services.Config)
	resolved, err := req.Resolve(ctx, op.Repos.VM, op.Repos.Vsock)
	if err != nil {
		return nil, err
	}

	// ── Audit log.
	op.AuditLog.LogOperation("cp.copy", map[string]any{
		"direction": resolved.Direction,
		"sources":   strings.Join(input.Sources, ", "),
		"dest":      input.Dest,
		"force":     input.Force,
	}, "")

	// Read vsock probe timeout from config.
	probeTimeout, err := op.Services.Config.GetDuration(ctx, "defaults.vm", "vsock_probe_timeout")
	if err != nil || probeTimeout <= 0 {
		probeTimeout = 5 * time.Second
	}

	// Wrap progress callback.
	wrapProgress := func(current, total int64) {
		if onProgress != nil {
			onProgress(current, total)
		}
	}

	// Perform the copy using vsock binary frame protocol.
	switch resolved.Direction {
	case infra.DirectionHostToVM:
		if resolved.DstInfo == nil || resolved.LocalPaths == nil {
			return nil, errs.New(errs.CodeCPError, "Internal error: destination VM info not available")
		}
		if resolved.DstInfo.Vsock == nil {
			return nil, errs.New(errs.CodeCPError,
				fmt.Sprintf("VM '%s' has no vsock configuration", resolved.DstInfo.Identifier))
		}

		client := vsock.NewClient(resolved.DstInfo.Vsock, probeTimeout)
		client.VmName = resolved.DstInfo.Identifier
		ftResult, err := client.FTCopyToVM(ctx, resolved.LocalPaths, resolved.DstInfo.RemotePath,
			resolved.Force, wrapProgress)
		if err != nil {
			return nil, err
		}

		msg := fmt.Sprintf("Copied %d file(s) (%s)", ftResult.Files, formatBytes(ftResult.Bytes))
		if ftResult.Errors > 0 {
			msg += fmt.Sprintf(" (%d errors)", ftResult.Errors)
		}

		return &results.CPCopyResult{
			Bytes:   ftResult.Bytes,
			Message: msg,
		}, nil

	case infra.DirectionVMToHost:
		if resolved.SrcInfo == nil || resolved.LocalPaths == nil {
			return nil, errs.New(errs.CodeCPError, "Internal error: source VM info not available")
		}
		if resolved.SrcInfo.Vsock == nil {
			return nil, errs.New(errs.CodeCPError,
				fmt.Sprintf("VM '%s' has no vsock configuration", resolved.SrcInfo.Identifier))
		}

		client := vsock.NewClient(resolved.SrcInfo.Vsock, probeTimeout)
		client.VmName = resolved.SrcInfo.Identifier
		ftResult, err := client.FTCopyFromVM(ctx, resolved.SrcInfo.RemotePath,
			resolved.LocalPaths[0], resolved.Force, wrapProgress)
		if err != nil {
			return nil, err
		}

		msg := fmt.Sprintf("Copied %d file(s) (%s)", ftResult.Files, formatBytes(ftResult.Bytes))

		return &results.CPCopyResult{
			Bytes:   ftResult.Bytes,
			Message: msg,
		}, nil

	case infra.DirectionVMToVM:
		if resolved.SrcInfo == nil || resolved.DstInfo == nil {
			return nil, errs.New(errs.CodeCPError, "Internal error: source or destination VM info not available")
		}
		if resolved.SrcInfo.Vsock == nil {
			return nil, errs.New(errs.CodeCPError,
				fmt.Sprintf("Source VM '%s' has no vsock configuration", resolved.SrcInfo.Identifier))
		}
		if resolved.DstInfo.Vsock == nil {
			return nil, errs.New(errs.CodeCPError,
				fmt.Sprintf("Destination VM '%s' has no vsock configuration", resolved.DstInfo.Identifier))
		}

		srcClient := vsock.NewClient(resolved.SrcInfo.Vsock, probeTimeout)
		srcClient.VmName = resolved.SrcInfo.Identifier
		dstClient := vsock.NewClient(resolved.DstInfo.Vsock, probeTimeout)
		dstClient.VmName = resolved.DstInfo.Identifier
		ftResult, err := srcClient.FTCopyVMToVM(ctx, resolved.SrcInfo.RemotePath,
			resolved.DstInfo.RemotePath, resolved.Force, wrapProgress, dstClient)
		if err != nil {
			return nil, err
		}

		msg := fmt.Sprintf("Copied %d file(s) (%s)", ftResult.Files, formatBytes(ftResult.Bytes))

		return &results.CPCopyResult{
			Bytes:   ftResult.Bytes,
			Message: msg,
		}, nil

	default:
		return nil, errs.New(errs.CodeCPError, fmt.Sprintf("Unknown copy direction: %s", resolved.Direction))
	}
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(b int64) string {
	if b < 1024 {
		return fmt.Sprintf("%d B", b)
	}
	if b < 1024*1024 {
		return fmt.Sprintf("%.1f KiB", float64(b)/1024)
	}
	if b < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MiB", float64(b)/(1024*1024))
	}
	return fmt.Sprintf("%.1f GiB", float64(b)/(1024*1024*1024))
}

// Compile-time checks ensure interfaces are satisfied.
var _ CPAPI = (*Operation)(nil)
