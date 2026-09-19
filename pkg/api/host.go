// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"mvmctl/internal/core/host"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
	"os"
	"os/user"
)

// HostAPI defines the public interface for host operations.
type HostAPI interface {
	HostInstallSystemBinary(ctx context.Context) (bool, error)
	HostInit(ctx context.Context, onProgress event.OnProgressCallback) (*results.HostInit, error)
	HostGetState(ctx context.Context) (*model.HostStateItem, error)
	HostDetectResources(ctx context.Context) (*model.HostResources, error)
	HostNetworkSetup(ctx context.Context) error
	HostInfo(ctx context.Context) (*results.HostInfo, error)
	HostRefreshCapacity(ctx context.Context) (*results.HostInfo, error)
	HostCheckKVMAccess() bool
	HostCheckRequiredBinaries() []string
	HostGetIPForwardStatus(ctx context.Context) (string, error)
	HostStatusCheck(ctx context.Context) *results.HostStatusCheck
	HostClean(ctx context.Context) ([]string, error)
	HostReset(ctx context.Context) ([]string, error)
	HostGetRunningVMs(ctx context.Context) ([]*model.VMItem, error)
	HostIsInitialized(ctx context.Context) bool
	HostCheckReadiness(ctx context.Context) *model.ProbeResult
}

// HostInstallSystemBinary explicitly installs or upgrades the administrator-owned
// system image. It intentionally performs no host database or configuration work.
func (op *Operation) HostInstallSystemBinary(ctx context.Context) (bool, error) {
	return op.hostSystemBinaryInstaller.InstallSystemBinary(ctx)
}

// HostInit requests the explicit administrator setup route. The normal API never
// uses caller-owned configuration or SQLite as authority for host mutations.
func (op *Operation) HostInit(ctx context.Context, onProgress event.OnProgressCallback) (*results.HostInit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, &errs.NeedsInteraction{
		Code:      "privilege.sudo_required",
		Message:   "run sudo /usr/local/bin/mvm host init; direct root must specify --operator <non-root-user>",
		InputType: "sudo",
		Context:   map[string]any{"command": "sudo /usr/local/bin/mvm host init", "operation": "initialize host"},
	}
}

// HostGetState returns the current host state snapshot.
func (op *Operation) HostGetState(ctx context.Context) (*model.HostStateItem, error) {
	return op.Repos.Host.GetState(ctx)
}

// HostDetectResources detects live host resources.
func (op *Operation) HostDetectResources(ctx context.Context) (*model.HostResources, error) {
	state, err := op.Repos.Host.GetState(ctx)
	if err != nil {
		return nil, err
	}
	var hardware *model.HostHardware
	var limits *model.HostLimits
	if state != nil && state.CPUModel != nil {
		hardware = host.HardwareFromState(state)
		limits = host.LimitsFromState(state)
	} else {
		var detErr error
		hardware, detErr = host.DetectHardware()
		if detErr != nil {
			return nil, detErr
		}
		limits = host.DetectLimits()
	}
	if hardware == nil || limits == nil {
		return nil, nil
	}
	res, err := host.DetectResources(ctx, hardware, limits, op.CacheDir)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// HostNetworkSetup sets up the default network.
// static call to
func (op *Operation) HostNetworkSetup(ctx context.Context) error {
	results, syncErr := op.NetworkSync(ctx, inputs.NetworkInput{})
	if syncErr == nil {
		if len(results) == 0 {
			_, err := op.NetworkCreateDefaultNetwork(ctx)
			if err != nil {
				slog.Warn("Could not create default network", "error", err)
				return err
			}
		}
	} else {
		return syncErr
	}
	return nil
}

// HostInfo returns host info with capacity analysis.
// uses HostInfo serialization to map.
func (op *Operation) HostInfo(ctx context.Context) (*results.HostInfo, error) {
	state, err := op.Repos.Host.GetState(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeHostInfoFailed, fmt.Sprintf("Failed to get host state: %v", err), err)
	}
	if state == nil {
		return nil, errs.New(
			errs.CodeHostInfoFailed,
			"Host not yet detected. Run 'mvm host init' first.",
			errs.WithClass(errs.ClassValidation),
		)
	}
	hardware := host.HardwareFromState(state)
	limits := host.LimitsFromState(state)
	if hardware == nil || limits == nil {
		// Auto-detect if this is the first time
		hardware, limits, err = op.Services.Host.DetectAndSaveCapacity(ctx)
		if err != nil {
			return nil, errs.WrapMsg(
				errs.CodeHostInfoFailed,
				fmt.Sprintf("Failed to detect host capacity: %v", err),
				err,
			)
		}
		state, err = op.Repos.Host.GetState(ctx)
		if err != nil || state == nil {
			return nil, errs.New(
				errs.CodeHostInfoFailed,
				"Failed to retrieve host state after detection.",
				errs.WithClass(errs.ClassInternal),
			)
		}
	}
	// Detect resources
	resources, err := host.DetectResources(ctx, hardware, limits, op.CacheDir)
	if err != nil {
		return nil, errs.New(errs.CodeHostInfoFailed, fmt.Sprintf("Failed to detect resources: %v", err))
	}
	// Build HostInfo from model data
	info := &model.HostInfo{
		State:     *state,
		Resources: *resources,
		Limits:    *limits,
		Hardware:  *hardware,
	}
	return results.BuildHostInfo(info), nil
}

// HostRefreshCapacity re-detects host capacity.
func (op *Operation) HostRefreshCapacity(ctx context.Context) (*results.HostInfo, error) {
	hardware, limits, err := op.Services.Host.DetectAndSaveCapacity(ctx)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeHostCapacityFailed,
			fmt.Sprintf("Failed to detect host capacity: %v", err),
			err,
		)
	}
	state, err := op.Repos.Host.GetState(ctx)
	if err != nil || state == nil {
		return nil, errs.New(
			errs.CodeHostInfoFailed,
			"Failed to retrieve host state after detection.",
			errs.WithClass(errs.ClassInternal),
		)
	}
	resources, err := host.DetectResources(ctx, hardware, limits, op.CacheDir)
	if err != nil {
		return nil, errs.New(errs.CodeHostCapacityFailed, fmt.Sprintf("Failed to detect resources: %v", err))
	}
	// Build HostInfo from model data
	info := &model.HostInfo{
		State:     *state,
		Resources: *resources,
		Limits:    *limits,
		Hardware:  *hardware,
	}
	return results.BuildHostInfo(info), nil
}

// HostCheckKVMAccess checks /dev/kvm accessibility.
func (op *Operation) HostCheckKVMAccess() bool {
	return host.CheckKVMAccess()
}

// HostCheckRequiredBinaries checks for missing required binaries.
func (op *Operation) HostCheckRequiredBinaries() []string {
	return host.CheckRequiredBinaries()
}

// HostGetIPForwardStatus returns IP forwarding status.
func (op *Operation) HostGetIPForwardStatus(ctx context.Context) (string, error) {
	return host.GetIPForwardStatus(ctx)
}

// HostStatusCheck returns a consolidated host status with all checks.
func (op *Operation) HostStatusCheck(ctx context.Context) *results.HostStatusCheck {
	kvmOK := op.HostCheckKVMAccess()
	missing := op.HostCheckRequiredBinaries()
	ipFwd, _ := op.HostGetIPForwardStatus(ctx)
	fwdOK := ipFwd == "1"
	var setup *results.HostSetupInfo
	state, _ := op.HostGetState(ctx)
	if state != nil {
		setup = &results.HostSetupInfo{
			Initialized:   state.Initialized,
			InitializedAt: state.InitializedAt,
		}
	}
	resources, _ := op.HostDetectResources(ctx)
	// Check group and sudoers state from live system
	groupExists := system.GroupExists(infra.MVMUnixGroup)
	currentUser, _ := user.Current()
	userInGroup := currentUser != nil && system.UserInGroup(ctx, currentUser.Username, infra.MVMUnixGroup)
	sudoersExists := false
	if _, err := os.Stat(fmt.Sprintf("/etc/sudoers.d/%s", infra.MVMUnixGroup)); err == nil {
		sudoersExists = true
	}
	return &results.HostStatusCheck{
		KVMOK:           kvmOK,
		MissingBinaries: missing,
		IPForward:       ipFwd,
		IPForwardOK:     fwdOK,
		GroupExists:     groupExists,
		SudoersExists:   sudoersExists,
		UserInGroup:     userInGroup,
		State:           setup,
		Resources:       resources,
	}
}

// HostClean removes the caller's registered networks through the named API.
func (op *Operation) HostClean(ctx context.Context) ([]string, error) {
	running, err := op.HostGetRunningVMs(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeHostCleanFailed, "discover running VMs before host cleanup", err)
	}
	if len(running) != 0 {
		return nil, errs.New(errs.CodeHostCleanFailed, "stop all running VMs before host cleanup")
	}
	networks, err := op.Repos.Network.ListAll(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeHostCleanFailed, "discover networks before host cleanup", err)
	}
	summary := make([]string, 0, len(networks))
	for _, network := range networks {
		if err := op.NetworkRemove(ctx, inputs.NetworkInput{Identifiers: []string{network.ID}}, false); err != nil {
			return summary, errs.WrapMsg(errs.CodeHostCleanFailed, "remove registered network", err,
				errs.WithDetails(map[string]any{"completed": summary, "network_id": network.ID}))
		}
		summary = append(summary, fmt.Sprintf("Removed network '%s'", network.Name))
	}
	op.AuditLog.LogOperation("host.clean", map[string]any{"actions": len(summary)}, "")
	return summary, nil
}

// HostReset requests the administrator route; user SQLite is never rollback authority.
func (op *Operation) HostReset(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, &errs.NeedsInteraction{
		Code:      "privilege.sudo_required",
		Message:   "run sudo /usr/local/bin/mvm host reset --force; direct root must specify --operator <non-root-user>",
		InputType: "sudo",
		Context:   map[string]any{"command": "sudo /usr/local/bin/mvm host reset --force", "operation": "reset host"},
	}
}

// HostGetRunningVMs returns the caller's root-observed running VMs, including records absent from SQLite.
// This preflight is not host-wide quiescence authority; each network removal independently checks root references.
func (op *Operation) HostGetRunningVMs(ctx context.Context) ([]*model.VMItem, error) {
	inventory, err := jailer.NewRuntimeOwnerInventoryClient().Observe(ctx)
	if err != nil {
		return nil, err
	}
	localVMs, err := op.Repos.VM.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*model.VMItem, len(localVMs))
	for _, item := range localVMs {
		byID[item.ID] = item
	}
	running := make([]*model.VMItem, 0)
	for _, observed := range inventory.VMs {
		if observed.Status != jailer.RuntimeStatusRunning {
			continue
		}
		item := byID[observed.VMID]
		if item == nil {
			item = &model.VMItem{ID: observed.VMID, Name: observed.VMID}
		}
		item.Status = model.VMStatusRunning
		running = append(running, item)
	}
	return running, nil
}

// HostIsInitialized checks if host is initialized.
func (op *Operation) HostIsInitialized(ctx context.Context) bool {
	state, err := op.Repos.Host.GetState(ctx)
	return err == nil && state != nil && state.Initialized
}

// HostCheckReadiness runs pre-flight checks.
func (op *Operation) HostCheckReadiness(ctx context.Context) *model.ProbeResult {
	hardware, _ := host.DetectHardware()
	limits := host.DetectLimits()
	resources, _ := host.DetectResources(ctx, hardware, limits, op.CacheDir)
	probe := &host.Probe{}
	return probe.RunAll(ctx, hardware, limits, resources)
}
