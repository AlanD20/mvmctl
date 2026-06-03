// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/host_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"slices"
	"strings"
	"time"

	"mvmctl/internal/core/host"
	"mvmctl/internal/core/network"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/crypto"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/firewall"
	"mvmctl/internal/infra/model"
	infranet "mvmctl/internal/infra/network"
	"mvmctl/internal/infra/system"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"
)

// HostInit initializes host configuration.
// Matches Python's HostOperation.init() exactly — returns NeedsInteraction directly
// (not wrapped in OperationResult.Item) when elevated privileges are required.
// Accepts optional onProgress callback matching Python's:
//
//	on_progress: Callable[[ProgressEvent], None] | None = None
//
// Returns (any, error) where any is:
//   - *errs.NeedsInteraction when sudo required
//   - map[string]any with changes/user_added_to_group on success
//   - nil when skipped (no changes needed)
func (op *Operation) HostInit(ctx context.Context, onProgress func(errs.ProgressEvent)) (any, error) {
	// Resolve the actual binary path so sudo invokes the correct binary.
	mvmPath, _ := os.Executable()
	if mvmPath == "" {
		mvmPath = infra.CLIName
	}
	sudoCmd := fmt.Sprintf("sudo %s host init", mvmPath)

	// Check for privileges — returns NeedsInteraction if not available
	if err := system.CheckPrivileges("/usr/sbin/ip", "initialize host"); err != nil {
		hasGroup := system.SessionHasGroup()
		return &errs.NeedsInteraction{
			Code:      "privilege.sudo_required",
			Message:   "Elevated privileges required for host initialization",
			InputType: "sudo",
			Context: map[string]any{
				"command":           sudoCmd,
				"operation":         "initialize host",
				"session_has_group": hasGroup,
			},
		}, nil
	}

	// Ensure DB schema exists before any DB writes, matching Python:
	//   # Ensure DB schema exists before any DB writes.
	//   Database().migrate()
	if op.Connection != nil {
		_, _ = op.Connection.RunMigrationsCtx(ctx)
	}

	if !system.IsRoot() {
		hasGroup := system.SessionHasGroup()
		return &errs.NeedsInteraction{
			Code:      "privilege.sudo_required",
			Message:   "Root privileges required for host initialization",
			InputType: "sudo",
			Context: map[string]any{
				"command":           sudoCmd,
				"operation":         "initialize host",
				"session_has_group": hasGroup,
			},
		}, nil
	}

	// --- Pre-flight probes ---
	// Run detection first, then probe against the detected state (verdict #53).
	hardware, detErr := host.DetectHardware()
	if detErr != nil {
		return nil, &errs.DomainError{
			Code:    "host.init.detect_failed",
			Op:      "host",
			Message: fmt.Sprintf("Hardware detection failed: %v", detErr),
			Err:     detErr,
			Class:   errs.ClassInternal,
		}
	}
	limits := host.DetectLimits()
	resources, _ := host.DetectResources(ctx, hardware, limits, op.CacheDir)
	probe := &host.Probe{}
	probeResult := probe.RunAll(ctx, hardware, limits, resources)
	if len(probeResult.Critical) > 0 {
		criticalNames := make([]string, len(probeResult.Critical))
		for i, c := range probeResult.Critical {
			criticalNames[i] = c.Name
		}
		return nil, &errs.DomainError{
			Code:    "host.init.probe_failed",
			Op:      "host",
			Message: fmt.Sprintf("Probe failures: %s", strings.Join(criticalNames, ", ")),
			Class:   errs.ClassValidation,
		}
	}

	// Resolve firewall backend once (verdict #44).
	fwBackendRaw, _ := op.Services.Config.Get(ctx, "settings", "firewall_backend")
	fwBackend := "nftables"
	if s, ok := fwBackendRaw.(string); ok {
		fwBackend = s
	}

	// --- iptables comment module check ---
	xtcommentAvail := true
	if fwBackend == "iptables" {
		if !infranet.CheckIPTablesCommentAvailable(ctx) {
			slog.Info("iptables comment module (xt_comment) not available; rule comments will be skipped")
			_ = op.Services.Config.Set(ctx, "settings.firewall", "iptables_xtcomment", false)
			xtcommentAvail = false
		}
	}

	// Replace the default firewall tracker with the configured one.
	fwBackendType := model.FirewallBackendNFTables
	if fwBackend == "iptables" {
		fwBackendType = model.FirewallBackendIPTables
	}
	sqlDB := op.Connection.DB()
	fwTracker := firewall.NewFirewallTracker(fwBackendType, xtcommentAvail, sqlDB)
	op.Services.Network.SetFirewallTracker(fwTracker)

	// --- Initialize host state ---
	sessionID := crypto.UUIDV4()
	hostCtrl := host.NewController(op.Repos.Host)
	_, _ = op.Repos.Host.InitializeState(ctx)

	// --- Setup host environment ---
	allChanges, err := op.hostInitSetupEnvironment(ctx, sessionID, hostCtrl, fwBackend)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    "host.init.failed",
			Op:      "host",
			Message: err.Error(),
			Class:   errs.ClassInternal,
		}
	}

	// --- Finalize ---
	// Python: try: controller.mark_initialized(now) except Exception as e: logger.warning(...)
	now := time.Now().Format(time.RFC3339)
	if err := hostCtrl.MarkInitialized(ctx, now); err != nil {
		slog.Warn("Could not mark host as initialized", "error", err)
	}

	infra.ChownToRealUser(op.CacheDir)

	// Audit log

	op.AuditLog.LogOperation("host.init", map[string]any{"changes": len(allChanges)}, "")

	wasUserAdded := false
	for _, c := range allChanges {
		if c.Mechanism == "usermod" {
			wasUserAdded = true
			break
		}
	}

	if len(allChanges) == 0 {
		return nil, nil
	}

	return map[string]any{
		"changes":             allChanges,
		"user_added_to_group": wasUserAdded,
		"session_has_group":   system.SessionHasGroup(),
	}, nil
}

func (op *Operation) hostInitSetupEnvironment(
	ctx context.Context,
	sessionID string,
	hostCtrl *host.Controller,
	fwBackend string,
) ([]*model.HostStateChangeItem, error) {
	allChanges := make([]*model.HostStateChangeItem, 0)
	dbChanges := make([]*model.HostStateChangeItem, 0)

	// --- Group setup ---
	groupCreated, _ := host.CreateGroup(ctx, infra.MVMUnixGroup)
	if groupCreated {
		change := &model.HostStateChangeItem{
			SessionID: "", Setting: fmt.Sprintf("group:%s", infra.MVMUnixGroup),
			Mechanism: "groupadd", AppliedValue: infra.MVMUnixGroup,
			InitTimestamp: "", OriginalValue: nil, Reverted: false, ChangeOrder: 0, CreatedAt: "",
		}
		dbChanges = append(dbChanges, change)
		allChanges = append(allChanges, change)
	}

	username, err := system.CurrentUsername()
	if err != nil {
		return allChanges, err
	}
	userAdded, _ := host.AddUserToGroup(ctx, username, infra.MVMUnixGroup)
	if userAdded {
		change := &model.HostStateChangeItem{
			SessionID: "", Setting: fmt.Sprintf("group_member:%s", username),
			Mechanism: "usermod", AppliedValue: fmt.Sprintf("%s:%s", username, infra.MVMUnixGroup),
			InitTimestamp: "", OriginalValue: nil, Reverted: false, ChangeOrder: 0, CreatedAt: "",
		}
		dbChanges = append(dbChanges, change)
		allChanges = append(allChanges, change)
	}

	// --- Sudoers setup ---
	sudoersPath := infra.SudoersDropInPath()
	sudoersContent := host.GenerateSudoersContent(infra.MVMUnixGroup)
	sudoersStale := true
	if data, err := os.ReadFile(sudoersPath); err == nil {
		sudoersStale = string(data) != sudoersContent
	}
	if sudoersStale {
		_ = host.WriteSudoers(ctx, sudoersPath, sudoersContent)
		change := &model.HostStateChangeItem{
			SessionID: "", Setting: "sudoers_dropin",
			Mechanism: "file_create", AppliedValue: sudoersPath,
			InitTimestamp: "", OriginalValue: nil, Reverted: false, ChangeOrder: 0, CreatedAt: "",
		}
		dbChanges = append(dbChanges, change)
		allChanges = append(allChanges, change)
	}

	// --- IP forwarding ---
	fwdChange, _ := host.EnableIPForward(ctx)
	if fwdChange != nil {
		dbChanges = append(dbChanges, fwdChange)
		allChanges = append(allChanges, fwdChange)
	}

	// --- Persist sysctl ---
	sysctlChange, _ := host.PersistSysctl(ctx)
	if sysctlChange != nil {
		dbChanges = append(dbChanges, sysctlChange)
		allChanges = append(allChanges, sysctlChange)
	}

	// --- KVM modules ---
	moduleChanges, nextOrder, _ := op.Services.Host.EnsureKVMModules(ctx, sessionID, 0)
	allChanges = append(allChanges, moduleChanges...)

	// --- Firewall chains ---
	_ = op.Services.Network.EnsureMVMChains(ctx)

	chainChange := &model.HostStateChangeItem{
		SessionID: "", Setting: fmt.Sprintf("%s_chains", fwBackend),
		Mechanism: fwBackend, AppliedValue: "MVM chains ensured",
		InitTimestamp: "", OriginalValue: nil, Reverted: false, ChangeOrder: 0, CreatedAt: "",
	}
	dbChanges = append(dbChanges, chainChange)
	allChanges = append(allChanges, chainChange)

	// --- Persist state ---
	// Python: try: controller.record_changes(...) except Exception as e: logger.warning(...)
	if _, err := hostCtrl.RecordChanges(ctx, dbChanges, &sessionID, nextOrder); err != nil {
		slog.Warn("Could not record host changes to DB", "error", err)
	}

	// Python: try: repo.update_component(...) except Exception as e: logger.warning(...)
	if groupCreated {
		if err := op.Repos.Host.UpdateComponent(ctx, "mvm_group_created", true); err != nil {
			slog.Warn("Could not update host state", "error", err)
		}
	}
	if sudoersStale {
		if err := op.Repos.Host.UpdateComponent(ctx, "sudoers_configured", true); err != nil {
			slog.Warn("Could not update host state", "error", err)
		}
	}

	return allChanges, nil
}

// HostGetState returns the current host state snapshot.
// Matches Python's HostOperation.get_state().
func (op *Operation) HostGetState(ctx context.Context) (*model.HostStateItem, error) {
	return op.Repos.Host.GetState(ctx)
}

// HostDetectResources detects live host resources.
// Matches Python's HostOperation.detect_resources().
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
// Matches Python's HostOperation.network_setup() exactly — static call to
func (op *Operation) HostNetworkSetup(ctx context.Context) error {
	results, syncErr := op.NetworkSync(ctx, nil)
	if syncErr == nil {
		if len(results) == 0 {
			_, err := op.NetworkCreateDefaultNetwork(ctx)
			if err != nil {
				slog.Warn("Could not create default network", "error", err)
				return err
			}
		}
	} else {
		slog.Warn("Could not sync networks", "error", syncErr)
	}

	return nil
}

// HostInfo returns host info with capacity analysis.
// Matches Python's HostOperation.info() exactly — uses HostInfo.to_dict().
func (op *Operation) HostInfo(ctx context.Context) (*responses.HostInfo, error) {
	state, err := op.Repos.Host.GetState(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    "host.info.no_state",
			Op:      "host",
			Message: fmt.Sprintf("Failed to get host state: %v", err),
			Err:     err,
			Class:   errs.ClassInternal,
		}
	}
	if state == nil {
		return nil, &errs.DomainError{
			Code:    "host.info.no_state",
			Op:      "host",
			Message: "Host not yet detected. Run 'mvm host init' first.",
			Class:   errs.ClassValidation,
		}
	}

	hardware := host.HardwareFromState(state)
	limits := host.LimitsFromState(state)

	if hardware == nil || limits == nil {
		// Auto-detect if this is the first time
		hardware, limits, err = op.Services.Host.DetectAndSaveCapacity(ctx)
		if err != nil {
			return nil, &errs.DomainError{
				Code:    "host.info.detect_failed",
				Op:      "host",
				Message: fmt.Sprintf("Failed to detect host capacity: %v", err),
				Err:     err,
				Class:   errs.ClassInternal,
			}
		}
		state, err = op.Repos.Host.GetState(ctx)
		if err != nil || state == nil {
			return nil, &errs.DomainError{
				Code:    "host.info.no_state",
				Op:      "host",
				Message: "Failed to retrieve host state after detection.",
				Class:   errs.ClassInternal,
			}
		}
	}

	// Detect resources
	resources, err := host.DetectResources(ctx, hardware, limits, op.CacheDir)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    "host.info_failed",
			Op:      "host",
			Message: fmt.Sprintf("Failed to detect resources: %v", err),
			Class:   errs.ClassInternal,
		}
	}

	// Use HostInfo.to_dict() matching Python exactly
	info := &model.HostInfo{
		State:     *state,
		Resources: *resources,
		Limits:    *limits,
		Hardware:  *hardware,
	}
	return responses.BuildHostInfo(info), nil
}

// HostRefreshCapacity re-detects host capacity.
// Matches Python's HostOperation.refresh_capacity() exactly.
func (op *Operation) HostRefreshCapacity(ctx context.Context) (*responses.HostInfo, error) {
	hardware, limits, err := op.Services.Host.DetectAndSaveCapacity(ctx)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    "host.capacity.detect_failed",
			Op:      "host",
			Message: fmt.Sprintf("Failed to detect host capacity: %v", err),
			Err:     err,
			Class:   errs.ClassInternal,
		}
	}

	state, err := op.Repos.Host.GetState(ctx)
	if err != nil || state == nil {
		return nil, &errs.DomainError{
			Code:    "host.info.no_state",
			Op:      "host",
			Message: "Failed to retrieve host state after detection.",
			Class:   errs.ClassInternal,
		}
	}

	resources, err := host.DetectResources(ctx, hardware, limits, op.CacheDir)
	if err != nil {
		return nil, &errs.DomainError{
			Code:    "host.capacity_failed",
			Op:      "host",
			Message: fmt.Sprintf("Failed to detect resources: %v", err),
			Class:   errs.ClassInternal,
		}
	}

	// Use HostInfo.to_dict() matching Python exactly
	info := &model.HostInfo{
		State:     *state,
		Resources: *resources,
		Limits:    *limits,
		Hardware:  *hardware,
	}
	return responses.BuildHostInfo(info), nil
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
func (op *Operation) HostStatusCheck(ctx context.Context) *responses.HostStatusCheck {
	kvmOK := op.HostCheckKVMAccess()
	missing := op.HostCheckRequiredBinaries()
	ipFwd, _ := op.HostGetIPForwardStatus(ctx)
	fwdOK := ipFwd == "1"

	var setup *responses.HostSetupInfo
	state, _ := op.HostGetState(ctx)
	if state != nil {
		setup = &responses.HostSetupInfo{
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

	return &responses.HostStatusCheck{
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

// HostClean cleans host networking configuration.
// Matches Python's HostOperation.clean() exactly — wraps errors in HostError/NetworkError pattern.
func (op *Operation) HostClean(ctx context.Context) ([]string, error) {

	if err := system.CheckPrivileges("/usr/sbin/ip", "clean host"); err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodePrivilegeRequired,
			Op:      "host",
			Message: fmt.Sprintf("Privilege check failed: %v", err),
			Err:     err,
			Class:   errs.ClassNeedsInteraction,
		}
	}

	var summary []string

	// Remove TAP devices
	tapNames := infranet.GetTunTapDevices(ctx)
	var fallbackTaps []string
	for _, tap := range tapNames {
		if strings.HasPrefix(tap, fmt.Sprintf("%s-", infra.CLIName)) {
			fallbackTaps = append(fallbackTaps, tap)
		}
	}
	slices.Sort(fallbackTaps)
	for _, tapName := range fallbackTaps {
		if err := op.Services.Network.RemoveRawTap(ctx, tapName); err != nil {
			summary = append(summary, fmt.Sprintf("Warning: failed to remove TAP '%s': %v", tapName, err))
		} else {
			summary = append(summary, fmt.Sprintf("Removed TAP device '%s'", tapName))
		}
	}

	networks, _ := op.Repos.Network.ListAll(ctx)
	staleSummary := op.Services.Network.RemoveStaleInterfaces(ctx, fmt.Sprintf("%s-", infra.CLIName))
	summary = append(summary, staleSummary...)

	metadataBridges := make(map[string]bool)
	for _, net := range networks {
		metadataBridges[net.Bridge] = true
	}

	for _, net := range networks {
		if net.NATEnabled {
			// Python: try: service.remove_nat(...) except NetworkError: pass
			_ = op.Services.Network.RemoveNAT(ctx, net.Bridge, network.NatGatewaysList(net), net.Subnet, net.ID, false)
		}
		if err := op.Services.Network.RemoveBridge(ctx, net.Bridge, net.ID); err != nil {
			summary = append(summary, fmt.Sprintf("Warning: failed to remove network '%s': %v", net.Name, err))
		} else {
			summary = append(summary, fmt.Sprintf("Removed network '%s' (bridge: %s)", net.Name, net.Bridge))
		}
	}

	defaultNetNameRaw, _ := op.Services.Config.Get(ctx, "defaults.network", "name")
	defaultNetNameStr := "net"
	if s, ok := defaultNetNameRaw.(string); ok {
		defaultNetNameStr = s
	}
	defaultBridge := fmt.Sprintf("%s-%s", infra.CLIName, system.TruncateString(defaultNetNameStr, 10))
	if infranet.BridgeExists(ctx, defaultBridge) {
		if err := op.Services.Network.RemoveRawBridge(ctx, defaultBridge); err != nil {
			summary = append(
				summary,
				fmt.Sprintf("Warning: failed to remove orphan bridge '%s': %v", defaultBridge, err),
			)
		} else {
			summary = append(summary, fmt.Sprintf("Removed orphan bridge '%s'", defaultBridge))
		}
	}

	for _, bridge := range infranet.GetBridges(ctx) {
		if !strings.HasPrefix(bridge, fmt.Sprintf("%s-", infra.CLIName)) {
			continue
		}
		if bridge == defaultBridge || metadataBridges[bridge] {
			continue
		}
		if err := op.Services.Network.RemoveRawBridge(ctx, bridge); err != nil {
			summary = append(summary, fmt.Sprintf("Warning: failed to remove orphan bridge '%s': %v", bridge, err))
		} else {
			summary = append(summary, fmt.Sprintf("Removed orphan bridge '%s'", bridge))
		}
	}

	// Remove default network from database (matching Python)
	defaultNet := infranet.FindNetworkByName(networks, defaultNetNameStr)
	if defaultNet != nil {
		removeErr := op.NetworkRemove(ctx, &inputs.NetworkInput{Identifiers: []string{defaultNetNameStr}}, true)
		if removeErr != nil {
			summary = append(
				summary,
				fmt.Sprintf("Warning: failed to remove default network: %s", removeErr.Error()),
			)
		} else {
			summary = append(summary, fmt.Sprintf("Removed default network '%s'", defaultNetNameStr))
		}
	}

	// Remove MVM chains
	_ = op.Services.Network.Teardown(ctx)
	summary = append(summary, "Removed MVM firewall chains")

	if len(summary) == 0 {
		summary = append(summary, "Warning: skipped host networking cleanup (already clean)")
	}

	op.AuditLog.LogOperation("host.clean", map[string]any{"actions": len(summary)}, "")

	return summary, nil
}

// HostReset resets host to pre-init state.
// Matches Python's HostOperation.reset() exactly — usermod processing order matches.
func (op *Operation) HostReset(ctx context.Context) ([]string, error) {

	if err := system.CheckPrivileges("/usr/sbin/ip", "reset host"); err != nil {
		return nil, &errs.DomainError{
			Code:    errs.CodePrivilegeRequired,
			Op:      "host",
			Message: fmt.Sprintf("Privilege check failed: %v", err),
			Err:     err,
			Class:   errs.ClassNeedsInteraction,
		}
	}

	cleanSummary, cleanErr := op.HostClean(ctx)
	if cleanErr != nil {
		return nil, cleanErr
	}
	summary := cleanSummary

	reverted, err := op.Services.Host.RestoreState(ctx)
	if err != nil {
		slog.Warn("No saved host state to restore", "error", err)
	} else {
		for _, change := range reverted {
			summary = append(summary, fmt.Sprintf("Reverted %s", change.Setting))
		}
	}

	// Single query for all host state changes (verdict #44).
	allHostChanges, _ := op.Repos.Host.ListChanges(ctx, nil, false)

	// Notify about kernel modules that were left loaded
	var activeModules []string
	for _, c := range allHostChanges {
		if c.Setting == "kernel_module_load" {
			activeModules = append(activeModules, c.AppliedValue)
		}
	}
	if len(activeModules) > 0 {
		summary = append(
			summary,
			fmt.Sprintf(
				"Modules loaded by mvm: %s. These were left loaded. Unload manually with 'modprobe -r <module>' if desired.",
				strings.Join(activeModules, ", "),
			),
		)
	}

	sudoersPath := infra.SudoersDropInPath()
	if removed, err := host.RemoveSudoers(ctx, sudoersPath); err != nil {
		summary = append(summary, fmt.Sprintf("Warning: %v", err))
	} else if removed {
		summary = append(summary, fmt.Sprintf("Removed sudoers file %s", sudoersPath))
	}

	// Python: Remove user from group FIRST, then remove group (matches Python order)
	// Python only processes the LAST usermod change (usermod_changes[-1].applied_value).
	var lastUsermod *model.HostStateChangeItem
	for _, c := range allHostChanges {
		if c.Mechanism == "usermod" {
			lastUsermod = c
		}
	}
	if lastUsermod != nil {
		applied := lastUsermod.AppliedValue
		username := applied
		if u, _, found := strings.Cut(applied, ":"); found {
			username = u
		}
		if removed, err := host.RemoveUserFromGroup(ctx, username, infra.MVMUnixGroup); err != nil {
			summary = append(summary, fmt.Sprintf("Warning: %v", err))
		} else if removed {
			summary = append(summary, fmt.Sprintf("Removed user '%s' from group '%s'", username, infra.MVMUnixGroup))
		}
	}

	if removed, err := host.RemoveGroup(ctx, infra.MVMUnixGroup); err != nil {
		summary = append(summary, fmt.Sprintf("Warning: %v", err))
	} else if removed {
		summary = append(summary, fmt.Sprintf("Removed group '%s'", infra.MVMUnixGroup))
	}

	_ = op.Repos.Host.ResetState(ctx)

	op.AuditLog.LogOperation("host.reset", map[string]any{"actions": len(summary)}, "")

	return summary, nil
}

// HostGetRunningVMs returns running VMs.
// Matches Python's HostOperation.get_running_vms().
func (op *Operation) HostGetRunningVMs(ctx context.Context) ([]*model.VM, error) {
	return op.Repos.VM.ListByStatus(ctx, string(model.StatusRunning), string(model.StatusStarting))
}

// HostIsInitialized checks if host is initialized.
// Matches Python's HostOperation.is_initialized().
func (op *Operation) HostIsInitialized(ctx context.Context) bool {
	state, err := op.Repos.Host.GetState(ctx)
	return err == nil && state != nil && state.Initialized
}

// HostCheckReadiness runs pre-flight checks.
// Matches Python's HostOperation.check_readiness().
func (op *Operation) HostCheckReadiness(ctx context.Context) *model.ProbeResult {
	hardware, _ := host.DetectHardware()
	limits := host.DetectLimits()
	resources, _ := host.DetectResources(ctx, hardware, limits, op.CacheDir)
	probe := &host.Probe{}
	return probe.RunAll(ctx, hardware, limits, resources)
}

// ── Host helpers inlined from internal/core/host/_host_info.go ──
// (Go ignores files starting with _, so these were never compiled into the host package.)
