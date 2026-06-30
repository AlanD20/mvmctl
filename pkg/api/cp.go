// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"mvmctl/internal/core/vsock"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
	"strings"
	"time"
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
func (op *Operation) CPCopy(
	ctx context.Context,
	input inputs.CPInput,
	onProgress event.OnDownloadCallback,
) (*results.CPCopyResult, error) {
	// Resolve input.
	resolved, err := input.Resolve(ctx, op.Services.Config, op.Repos.VM, op.Repos.Vsock)
	if err != nil {
		return nil, err
	}
	// -- Audit log.
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
		client, err := op.newVsockClient(ctx, resolved.DstInfo.Vsock, probeTimeout, resolved.DstInfo.Identifier)
		if err != nil {
			return nil, err
		}
		ftResult, ftErr := client.FTCopyToVM(ctx, resolved.LocalPaths, resolved.DstInfo.RemotePath,
			resolved.Force, resolved.NoSync, wrapProgress)
		if ftErr != nil {
			return nil, ftErr
		}
		if ftResult.Errors > 0 {
			return nil, errs.New(errs.CodeCPError,
				fmt.Sprintf("copy failed: %d error(s) — destination exists? use --force to overwrite",
					ftResult.Errors))
		}
		msg := fmt.Sprintf("Copied %d file(s) (%s)", ftResult.Files, formatBytes(ftResult.Bytes))
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
		client, err := op.newVsockClient(ctx, resolved.SrcInfo.Vsock, probeTimeout, resolved.SrcInfo.Identifier)
		if err != nil {
			return nil, err
		}
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
		srcClient, err := op.newVsockClient(ctx, resolved.SrcInfo.Vsock, probeTimeout, resolved.SrcInfo.Identifier)
		if err != nil {
			return nil, err
		}
		dstClient, err := op.newVsockClient(ctx, resolved.DstInfo.Vsock, probeTimeout, resolved.DstInfo.Identifier)
		if err != nil {
			return nil, err
		}
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

// newVsockClient creates a vsock client with upgrade lifecycle callbacks.
// It checks for in-progress upgrades before connecting and wires up
// OnUpgradeStarted/OnUpgradeCompleted callbacks that manage the DB upgrade lock.
func (op *Operation) newVsockClient(
	ctx context.Context,
	cfg *model.VsockConfigItem,
	probeTimeout time.Duration,
	vmName string,
) (*vsock.Client, error) {
	// Early DB lock check — reject before connecting if a recent upgrade is in progress.
	if cfg.Upgrading {
		if cfg.UpgradeStartedAt != nil && time.Since(*cfg.UpgradeStartedAt) < 60*time.Second {
			return nil, errs.New(errs.CodeVsockUpgradeInProgress,
				fmt.Sprintf("agent upgrade already in progress for VM '%s'", vmName))
		}
		// Stale lock — clear it
		if err := op.Repos.Vsock.ClearUpgradeLock(ctx, cfg.VmID); err != nil {
			slog.Warn("failed to clear stale upgrade lock", "vm", vmName, "error", err)
		}
	}
	client := vsock.NewClient(cfg, probeTimeout)
	client.VmName = vmName
	client.OnUpgradeStarted = func(ctx context.Context, fromVersion, toVersion string) {
		slog.Info("upgrading vsock agent", "vm", vmName, "from", fromVersion, "to", toVersion)
		if err := op.Repos.Vsock.SetUpgradeLock(ctx, cfg.VmID); err != nil {
			slog.Warn("failed to set upgrade lock", "vm", vmName, "error", err)
		}
	}
	client.OnUpgradeCompleted = func(ctx context.Context, newVersion string) {
		slog.Info("vsock agent upgrade complete", "vm", vmName, "version", newVersion)
		if err := op.Repos.Vsock.ClearUpgradeLock(ctx, cfg.VmID); err != nil {
			slog.Warn("failed to clear upgrade lock", "vm", vmName, "error", err)
		}
		if err := op.Repos.Vsock.UpdateAgentVersion(ctx, cfg.VmID, newVersion); err != nil {
			slog.Warn("failed to persist agent version", "vm", vmName, "error", err)
		}
	}
	client.OnVersionKnown = func(ctx context.Context, version string) {
		if version != cfg.AgentVersion {
			if err := op.Repos.Vsock.UpdateAgentVersion(ctx, cfg.VmID, version); err != nil {
				slog.Warn("failed to persist agent version", "vm", vmName, "error", err)
			}
		}
	}
	return client, nil
}
