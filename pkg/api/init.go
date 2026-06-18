// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"fmt"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
	"os/exec"
)

// InitAPI defines the public interface for init/wizard operations.
type InitAPI interface {
	InitCheckReadiness(ctx context.Context) *model.ProbeResult
	InitSetupHost(ctx context.Context) error
	InitRun(
		ctx context.Context,
		skipHost bool,
		skipNetwork bool,
		nonInteractive bool,
		sudoCompleted bool,
		downloadVersion string,
		onProgress event.OnProgressCallback,
	) *results.InitResult
	InitRunFull(
		ctx context.Context,
		skipHost bool,
		skipNetwork bool,
		nonInteractive bool,
		sudoCompleted bool,
		hostSetupMessage string,
		downloadVersion string,
		guestfsEnabled *bool,
		onProgress event.OnProgressCallback,
	) *results.InitResult
}

// InitCheckReadiness runs pre-flight host readiness checks via the public API layer.
// called from CLI.
func (op *Operation) InitCheckReadiness(ctx context.Context) *model.ProbeResult {
	return op.HostCheckReadiness(ctx)
}

// InitSetupHost sets up host configuration.
func (op *Operation) InitSetupHost(ctx context.Context) error {
	raw, err := op.HostInit(ctx, nil)
	if err != nil {
		return err
	}
	if _, ok := raw.(*errs.NeedsInteraction); ok {
		return errs.New(errs.CodePrivilegeRequired, "Root privileges required")
	}
	return nil
}

// InitRun runs the init wizard steps in sequence.
// with backward-compatible signature.
// hostSetupMessage defaults to "", guestfsEnabled defaults to nil (auto-detect).
func (op *Operation) InitRun(
	ctx context.Context,
	skipHost bool,
	skipNetwork bool,
	nonInteractive bool,
	sudoCompleted bool,
	downloadVersion string,
	onProgress event.OnProgressCallback,
) *results.InitResult {
	return op.InitRunFull(
		ctx,
		skipHost,
		skipNetwork,
		nonInteractive,
		sudoCompleted,
		"",
		downloadVersion,
		nil,
		onProgress,
	)
}

// InitRunFull runs the init wizard steps with all parameters.
func (op *Operation) InitRunFull(
	ctx context.Context,
	skipHost bool,
	skipNetwork bool,
	nonInteractive bool,
	sudoCompleted bool,
	hostSetupMessage string,
	downloadVersion string,
	guestfsEnabled *bool,
	onProgress event.OnProgressCallback,
) *results.InitResult {
	steps := make([]results.InitStepResult, 0)
	// --- Step 1: Local state ---
	steps = append(steps, op.initInitDatabase(ctx))
	// --- Step 3: Host ---
	hostResult, hostInteraction := op.initStepHost(ctx, skipHost, sudoCompleted, hostSetupMessage, onProgress)
	steps = append(steps, hostResult)
	if hostInteraction != nil {
		return &results.InitResult{
			Steps:            steps,
			HostReady:        false,
			NeedsInteraction: hostInteraction,
		}
	}
	// --- Step 4: Guestfs ---
	guestfsResult, guestfsInteraction := op.initStepGuestfs(ctx, guestfsEnabled)
	steps = append(steps, guestfsResult)
	if guestfsInteraction != nil {
		return &results.InitResult{
			Steps:            steps,
			HostReady:        false,
			NeedsInteraction: guestfsInteraction,
		}
	}
	// --- Step 5: Network setup ---
	if skipNetwork {
		steps = append(
			steps,
			results.InitStepResult{Step: "network_setup", Success: true, Message: "Skipped (--skip-network)"},
		)
	} else {
		steps = append(steps, op.initStepNetworkSetup(ctx))
	}
	// --- Step 6: Cache ---
	steps = append(steps, op.initStepCache(ctx, onProgress))
	// --- Step 7: Binary ---
	binaryResult, binaryInteraction := op.initStepBinary(ctx, nonInteractive, downloadVersion)
	steps = append(steps, binaryResult)
	if binaryInteraction != nil {
		return &results.InitResult{
			Steps:            steps,
			HostReady:        false,
			NeedsInteraction: binaryInteraction,
		}
	}
	// Determine host_ready
	hostReady := false
	binaryReady := false
	for _, s := range steps {
		if s.Step == "host" && s.Success {
			hostReady = true
		}
		if s.Step == "binary" && s.Success {
			binaryReady = true
		}
	}
	return &results.InitResult{
		Steps:     steps,
		HostReady: hostReady && binaryReady,
	}
}
func (op *Operation) initInitDatabase(ctx context.Context) results.InitStepResult {
	// Run database migrations.
	if op.Connection != nil {
		if _, err := op.Connection.RunMigrationsCtx(ctx); err != nil {
			return results.InitStepResult{Step: "local_state", Success: false, Message: fmt.Sprintf("Failed: %v", err)}
		}
	}
	return results.InitStepResult{Step: "local_state", Success: true, Message: "Local state ready"}
}
func (op *Operation) initStepHost(
	ctx context.Context,
	skip bool,
	sudoCompleted bool,
	setupMessage string,
	onProgress event.OnProgressCallback,
) (results.InitStepResult, *errs.NeedsInteraction) {
	if skip {
		return results.InitStepResult{Step: "host", Success: true, Message: "Skipped (--skip-host)"}, nil
	}
	if sudoCompleted {
		msg := setupMessage
		if msg == "" {
			msg = "completed"
		}
		return results.InitStepResult{Step: "host", Success: true, Message: msg}, nil
	}
	initResult, initErr := op.HostInit(ctx, onProgress)
	if initErr != nil {
		return results.InitStepResult{Step: "host", Success: false, Message: initErr.Error()}, nil
	}
	// Check for NeedsInteraction.
	if interaction, ok := initResult.(*errs.NeedsInteraction); ok {
		return results.InitStepResult{Step: "host", Success: false, Message: "Root privileges required"}, interaction
	}
	if initResult == nil {
		return results.InitStepResult{Step: "host", Success: true, Message: "Host already configured"}, nil
	}
	return results.InitStepResult{Step: "host", Success: true, Message: "Host initialized"}, nil
}
func (op *Operation) initStepNetworkSetup(ctx context.Context) results.InitStepResult {
	err := op.HostNetworkSetup(ctx)
	success := err == nil
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg == "" {
		if success {
			msg = "Default network ready"
		} else {
			msg = "Failed to create default network"
		}
	}
	return results.InitStepResult{Step: "network_setup", Success: success, Message: msg}
}
func (op *Operation) initStepCache(ctx context.Context, onProgress event.OnProgressCallback) results.InitStepResult {
	cacheDict, err := op.CacheInitAll(ctx, onProgress)
	if err != nil {
		return results.InitStepResult{Step: "cache", Success: false, Message: fmt.Sprintf("Cache init failed: %v", err)}
	}
	guestfsBuilt := cacheDict.GuestfsAppliance != ""
	msg := "Cache directories ready"
	if guestfsBuilt {
		msg = "Cache directories ready (libguestfs appliance built)"
	}
	return results.InitStepResult{Step: "cache", Success: true, Message: msg}
}
func (op *Operation) initStepBinary(
	ctx context.Context,
	nonInteractive bool,
	downloadVersion string,
) (results.InitStepResult, *errs.NeedsInteraction) {
	local, _, err := op.BinaryList(ctx, false, nil, nil)
	if err != nil {
		return results.InitStepResult{Step: "binary", Success: false, Message: "Failed to list binaries"}, nil
	}
	fcBinaries := make([]*model.BinaryItem, 0)
	for _, b := range local {
		if b.Type == "firecracker" || b.Type == "jailer" {
			fcBinaries = append(fcBinaries, b)
		}
	}
	if len(fcBinaries) > 0 {
		active := make([]*model.BinaryItem, 0)
		for _, v := range fcBinaries {
			if v.IsDefault {
				active = append(active, v)
			}
		}
		if len(active) > 0 {
			return results.InitStepResult{
				Step:    "binary",
				Success: true,
				Message: fmt.Sprintf("Binary available (v%s)", active[0].Version),
			}, nil
		}
		repaired, err := op.BinaryEnsureDefault(ctx)
		if err == nil && repaired != nil {
			return results.InitStepResult{
				Step:    "binary",
				Success: true,
				Message: fmt.Sprintf("Binary available (v%s) — set as default", repaired.Version),
			}, nil
		}
		return results.InitStepResult{
			Step:    "binary",
			Success: true,
			Message: fmt.Sprintf("Binary available (v%s)", fcBinaries[0].Version),
		}, nil
	}
	// No local binaries found
	if downloadVersion != "" {
		return op.initDownloadBinary(ctx, downloadVersion), nil
	}
	if nonInteractive {
		return op.initDownloadBinaryLatest(ctx), nil
	}
	// Needs interaction.
	return op.initBinaryNeedsInteraction(ctx)
}
func (op *Operation) initDownloadBinary(ctx context.Context, version string) results.InitStepResult {
	binaries, err := op.BinaryPull(ctx, inputs.BinaryPullInput{Version: version, SetDefault: true}, nil)
	if err != nil {
		return results.InitStepResult{
			Step:    "binary",
			Success: false,
			Message: fmt.Sprintf("Download failed: %v", err),
		}
	}
	versionStr := version
	for _, b := range binaries {
		if b.Type == "firecracker" {
			versionStr = b.Version
			break
		}
	}
	return results.InitStepResult{Step: "binary", Success: true, Message: fmt.Sprintf("Downloaded v%s", versionStr)}
}
func (op *Operation) initDownloadBinaryLatest(ctx context.Context) results.InitStepResult {
	// List remote versions, then pull the latest.
	one := 1
	_, remote, err := op.BinaryList(ctx, true, &one, nil)
	if err != nil || len(remote) == 0 {
		return results.InitStepResult{Step: "binary", Success: false, Message: "No remote versions found"}
	}
	version := remote[0].Version
	binaries, err := op.BinaryPull(ctx, inputs.BinaryPullInput{Version: version, SetDefault: true}, nil)
	if err != nil {
		return results.InitStepResult{
			Step:    "binary",
			Success: false,
			Message: fmt.Sprintf("Download failed: %v", err),
		}
	}
	versionStr := version
	for _, b := range binaries {
		if b.Type == "firecracker" {
			versionStr = b.Version
			break
		}
	}
	return results.InitStepResult{Step: "binary", Success: true, Message: fmt.Sprintf("Downloaded v%s", versionStr)}
}
func (op *Operation) initBinaryNeedsInteraction(ctx context.Context) (results.InitStepResult, *errs.NeedsInteraction) {
	// Attempt to list remote versions.
	five := 5
	_, remote, err := op.BinaryList(ctx, true, &five, nil)
	var versions []string
	if err == nil {
		versions = make([]string, len(remote))
		for i := range remote {
			versions[i] = remote[i].Version
		}
	}
	if len(versions) == 0 {
		return results.InitStepResult{Step: "binary", Success: false, Message: "No remote versions available"},
			&errs.NeedsInteraction{
				Code:      "binary.confirm_download",
				Message:   "No remote versions available",
				InputType: "confirm",
				Context:   map[string]any{},
			}
	}
	return results.InitStepResult{Step: "binary", Success: false, Message: "No Firecracker binary found in cache"},
		&errs.NeedsInteraction{
			Code:      "binary.confirm_download",
			Message:   "No Firecracker binary found in cache",
			InputType: "confirm",
			Context: map[string]any{
				"latest_version":     versions[0],
				"available_versions": versions,
			},
		}
}
func (op *Operation) initStepGuestfs(
	ctx context.Context,
	guestfsEnabled *bool,
) (results.InitStepResult, *errs.NeedsInteraction) {
	// When guestfs_enabled is provided (from a previous interaction round),
	// the decision is persisted directly.
	if guestfsEnabled != nil {
		op.Services.Config.Set(ctx, "settings", "guestfs_enabled", *guestfsEnabled)
		if *guestfsEnabled {
			return results.InitStepResult{Step: "guestfs", Success: true, Message: "enabled"}, nil
		}
		return results.InitStepResult{Step: "guestfs", Success: true, Message: "disabled"}, nil
	}
	// First pass — detect availability
	// Check for the guestfish binary as a reliable indicator of libguestfs availability.
	available := false
	if _, err := exec.LookPath("guestfish"); err == nil {
		available = true
	}
	if !available {
		// libguestfs not installed — no point prompting.
		op.Services.Config.Set(ctx, "settings", "guestfs_enabled", false)
		return results.InitStepResult{Step: "guestfs", Success: true, Message: "not installed"}, nil
	}
	// Installed but user hasn't decided — prompt
	return results.InitStepResult{Step: "guestfs", Success: false, Message: "available"},
		&errs.NeedsInteraction{
			Code:      "guestfs.confirm_enable",
			Message:   "libguestfs is available. Enable it as a fallback?",
			InputType: "confirm",
			Context:   map[string]any{},
		}
}
