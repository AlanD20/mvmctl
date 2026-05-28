// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/host_operations.py exactly.
package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"mvmctl/internal/core/host"
	"mvmctl/internal/core/network"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/db"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	infranet "mvmctl/internal/infra/network"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/validators"
	"mvmctl/pkg/api/inputs"
)

// WithDB sets the database connection for migration calls.
// Must be called before HostInit if DB migration is needed.
func (op *Operation) HostWithDB(database *sql.DB) {
	op.DB = database
}

// HostInit initializes host configuration.
// Matches Python's HostOperation.init() exactly — returns NeedsInteraction directly
// (not wrapped in OperationResult.Item) when elevated privileges are required.
// Accepts optional onProgress callback matching Python's:
//
//	on_progress: Callable[[ProgressEvent], None] | None = None
//
// Returns *errs.OperationResult (success/error/skipped) or
// *errs.NeedsInteraction (when sudo required).
// OperationResult.Item varies: nil (success/skipped) or []string (error details).
func (op *Operation) HostInit(ctx context.Context, cacheDir string, onProgress func(errs.ProgressEvent)) interface{} {
	ph := &host.PrivilegeHelper{}

	// Check for privileges — returns NeedsInteraction if not available
	if err := ph.CheckPrivileges("/usr/sbin/ip", "initialize host"); err != nil {
		hasGroup := ph.SessionHasGroup()
		return &errs.NeedsInteraction{
			Code:      "privilege.sudo_required",
			Message:   "Elevated privileges required for host initialization",
			InputType: "sudo",
			Context: map[string]interface{}{
				"command":           "sudo mvm host init",
				"operation":         "initialize host",
				"session_has_group": hasGroup,
			},
		}
	}

	// Ensure DB schema exists before any DB writes, matching Python:
	//   # Ensure DB schema exists before any DB writes.
	//   Database().migrate()
	if op.DB != nil {
		_, _ = db.RunMigrationsCtx(ctx, op.DB, filepath.Join(op.CacheDir, infra.MVMDBFilename))
	}

	if os.Geteuid() != 0 {
		hasGroup := ph.SessionHasGroup()
		return &errs.NeedsInteraction{
			Code:      "privilege.sudo_required",
			Message:   "Root privileges required for host initialization",
			InputType: "sudo",
			Context: map[string]interface{}{
				"command":           "sudo mvm host init",
				"operation":         "initialize host",
				"session_has_group": hasGroup,
			},
		}
	}

	// Chown cache directory to real user
	infra.ChownToRealUser(cacheDir)

	// --- Pre-flight probes ---
	probe := &host.Probe{}
	probeResult := probe.RunAll()
	if len(probeResult.Critical) > 0 {
		criticalNames := make([]string, len(probeResult.Critical))
		for i, c := range probeResult.Critical {
			criticalNames[i] = c.Name
		}
		return &errs.OperationResult{
			Status:  "error",
			Code:    "host.init.probe_failed",
			Message: fmt.Sprintf("Probe failures: %s", strings.Join(criticalNames, ", ")),
			Metadata: map[string]interface{}{
				"probe_result": probeResult,
			},
		}
	}

	// --- iptables comment module check ---
	fwBackendRaw, _ := op.Services.Config.Get(ctx, "settings", "firewall_backend")
	if fwBackend, ok := fwBackendRaw.(string); ok && fwBackend == "iptables" {
		if !infranet.CheckIPTablesCommentAvailable() {
			slog.Info("iptables comment module (xt_comment) not available; rule comments will be skipped")
			_ = op.Services.Config.Set(ctx, "settings.firewall", "iptables_xtcomment", false)
		}
	}

	// --- Initialize host state ---
	hostCtrl := host.NewController(op.Repos.Host)
	_, _ = op.Repos.Host.InitializeState(ctx)
	sessionID := infra.UUIDV4()

	// --- Setup host environment ---
	allChanges, err := op.hostSetupHostEnvironment(ctx, sessionID, hostCtrl)
	if err != nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "host.init.failed",
			Message: err.Error(),
		}
	}

	// --- Finalize ---
	// Python: try: controller.mark_initialized(now) except Exception as e: logger.warning(...)
	now := time.Now().Format(time.RFC3339)
	if err := hostCtrl.MarkInitialized(ctx, now); err != nil {
		slog.Warn("Could not mark host as initialized", "error", err)
	}

	infra.ChownToRealUser(cacheDir)

	// Audit log
	auditLog := logging.NewAuditLog(cacheDir)
	_ = auditLog.LogOperation("host.init", map[string]interface{}{"changes": len(allChanges)}, "")

	wasUserAdded := false
	for _, c := range allChanges {
		if c.Mechanism == "usermod" {
			wasUserAdded = true
			break
		}
	}

	if len(allChanges) == 0 {
		return &errs.OperationResult{
			Status:  "skipped",
			Code:    "host.init.noop",
			Message: "Host already configured — nothing to do.",
		}
	}

	ph2 := &host.PrivilegeHelper{}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "host.init.complete",
		Message: fmt.Sprintf("Host initialized (%d change(s) applied).", len(allChanges)),
		Metadata: map[string]interface{}{
			"changes":             allChanges,
			"user_added_to_group": wasUserAdded,
			"session_has_group":   ph2.SessionHasGroup(),
		},
	}
}

func (op *Operation) hostSetupHostEnvironment(ctx context.Context, sessionID string, hostCtrl *host.Controller) ([]*model.HostStateChangeItem, error) {
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
	// Python: validate group name BEFORE writing sudoers.
	//         if not re.fullmatch(r"[a-z][a-z0-9_-]{0,30}", MVM_UNIX_GROUP):
	//             raise HostError(f"Invalid group name: {MVM_UNIX_GROUP!r}")
	if !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,30}$`).MatchString(infra.MVMUnixGroup) {
		return allChanges, fmt.Errorf("Invalid group name: %q", infra.MVMUnixGroup)
	}

	sudoersPath := infra.SudoersDropInPath()
	sudoersStale := true
	if data, err := os.ReadFile(sudoersPath); err == nil {
		expected := host.GenerateSudoersContent(infra.MVMUnixGroup)
		sudoersStale = string(data) != expected
	}
	if sudoersStale {
		_ = host.WriteSudoers(ctx, sudoersPath, infra.MVMUnixGroup)
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
	moduleChanges, nextOrder, _ := host.EnsureKVMModules(ctx, op.Repos.Host, sessionID, 0)
	allChanges = append(allChanges, moduleChanges...)

	// --- Firewall chains ---
	_ = op.Services.Network.EnsureMVMChains(ctx)

	fwBackendRaw, _ := op.Services.Config.Get(ctx, "settings", "firewall_backend")
	fwBackend := "nftables"
	if s, ok := fwBackendRaw.(string); ok {
		fwBackend = s
	}
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
		hardware = hardwareFromState(state)
		limits = limitsFromState(state)
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
	res, err := host.DetectResources(hardware, limits, op.CacheDir)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// HostNetworkSetup sets up the default network.
// Matches Python's HostOperation.network_setup() exactly — static call to
// NetworkOperation.sync() with try/except wrapping.
func (op *Operation) HostNetworkSetup(ctx context.Context) *errs.OperationResult {
	// Python: restored_result = NetworkOperation.sync()
	//         if restored_result.is_ok and not restored_result.item:
	//             default_result = NetworkOperation.create_default_network()
	//             if default_result.is_error:
	//                 logger.warning(...)
	//                 return default_result
	//         return OperationResult(status="success", code="network.default_ready")
	syncResult := op.NetworkSync(ctx, "")
	if syncResult.IsOK() {
		// Python: if restored_result.is_ok and not restored_result.item:
		//         (Python checks "not restored_result.item" — nil/empty/None)
		itemEmpty := syncResult.Item == nil
		if !itemEmpty {
			// Check for empty collection types
			switch v := syncResult.Item.(type) {
			case []interface{}:
				itemEmpty = len(v) == 0
			case map[string]interface{}:
				itemEmpty = len(v) == 0
			}
		}
		if itemEmpty {
			result := op.NetworkCreateDefaultNetwork(ctx)
			if result.IsError() {
				slog.Warn("Could not create default network", "error", result.Message)
				return result
			}
		}
	}

	if syncResult.IsError() {
		slog.Warn("Could not sync networks", "error", syncResult.Message)
	}

	return &errs.OperationResult{
		Status: "success", Code: "network.default_ready",
	}
}

// HostInfo returns host info with capacity analysis.
// Matches Python's HostOperation.info() exactly — uses HostInfo.to_dict().
func (op *Operation) HostInfo(ctx context.Context) *errs.OperationResult {
	state, err := op.Repos.Host.GetState(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status: "error", Code: "host.info.no_state",
			Message:   fmt.Sprintf("Failed to get host state: %v", err),
			Exception: err,
		}
	}
	if state == nil {
		return &errs.OperationResult{
			Status: "error", Code: "host.info.no_state",
			Message: "Host not yet detected. Run 'mvm host init' first.",
		}
	}

	hardware := hardwareFromState(state)
	limits := limitsFromState(state)

	if hardware == nil || limits == nil {
		// Auto-detect if this is the first time
		hardware, limits, err = op.Services.Host.DetectAndSaveCapacity(ctx)
		if err != nil {
			return &errs.OperationResult{
				Status: "error", Code: "host.info.detect_failed",
				Message:   fmt.Sprintf("Failed to detect host capacity: %v", err),
				Exception: err,
			}
		}
		state, err = op.Repos.Host.GetState(ctx)
		if err != nil || state == nil {
			return &errs.OperationResult{
				Status: "error", Code: "host.info.no_state",
				Message: "Failed to retrieve host state after detection.",
			}
		}
	}

	// Detect resources
	resources, err := host.DetectResources(hardware, limits, op.CacheDir)
	if err != nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "host.info_failed",
			Message: fmt.Sprintf("Failed to detect resources: %v", err),
		}
	}

	// Use HostInfo.to_dict() matching Python exactly
	info := &model.HostInfo{
		State:     *state,
		Resources: *resources,
		Limits:    *limits,
		Hardware:  *hardware,
	}
	infoDict := hostInfoToDict(info)

	return &errs.OperationResult{
		Status: "success", Code: "host.info",
		Item: infoDict,
	}
}

// HostRefreshCapacity re-detects host capacity.
// Matches Python's HostOperation.refresh_capacity() exactly.
func (op *Operation) HostRefreshCapacity(ctx context.Context) *errs.OperationResult {
	hardware, limits, err := op.Services.Host.DetectAndSaveCapacity(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status: "error", Code: "host.capacity.detect_failed",
			Message:   fmt.Sprintf("Failed to detect host capacity: %v", err),
			Exception: err,
		}
	}

	state, err := op.Repos.Host.GetState(ctx)
	if err != nil || state == nil {
		return &errs.OperationResult{
			Status: "error", Code: "host.info.no_state",
			Message: "Failed to retrieve host state after detection.",
		}
	}

	resources, err := host.DetectResources(hardware, limits, op.CacheDir)
	if err != nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "host.capacity_failed",
			Message: fmt.Sprintf("Failed to detect resources: %v", err),
		}
	}

	// Use HostInfo.to_dict() matching Python exactly
	info := &model.HostInfo{
		State:     *state,
		Resources: *resources,
		Limits:    *limits,
		Hardware:  *hardware,
	}
	infoDict := hostInfoToDict(info)

	return &errs.OperationResult{
		Status: "success", Code: "host.capacity.refreshed",
		Item: infoDict,
	}
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
	result := system.RunCmdCompat(ctx, []string{"sysctl", "-n", "net.ipv4.ip_forward"}, system.RunCmdOptions{Capture: true})
	if result.Err != nil {
		return "", fmt.Errorf("failed to read net.ipv4.ip_forward: %w", result.Err)
	}
	return strings.TrimSpace(result.Stdout), nil
}

// HostClean cleans host networking configuration.
// Matches Python's HostOperation.clean() exactly — wraps errors in HostError/NetworkError pattern.
func (op *Operation) HostClean(ctx context.Context, cacheDir string) *errs.OperationResult {

	ph := &host.PrivilegeHelper{}
	if err := ph.CheckPrivileges("/usr/sbin/ip", "clean host"); err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodePrivilegeRequired),
			Message:   fmt.Sprintf("Privilege check failed: %v", err),
			Exception: err,
		}
	}

	var summary []string

	// Remove TAP devices
	tapNames := infranet.GetTunTapDevices()
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
	staleSummary := op.Services.Network.RemoveStaleInterfaces(fmt.Sprintf("%s-", infra.CLIName))
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
	if infranet.BridgeExists(defaultBridge) {
		if err := op.Services.Network.RemoveRawBridge(ctx, defaultBridge); err != nil {
			summary = append(summary, fmt.Sprintf("Warning: failed to remove orphan bridge '%s': %v", defaultBridge, err))
		} else {
			summary = append(summary, fmt.Sprintf("Removed orphan bridge '%s'", defaultBridge))
		}
	}

	for _, bridge := range infranet.GetBridges() {
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
		removeResult := op.NetworkRemove(ctx, &inputs.NetworkInput{Name: []string{defaultNetNameStr}}, true)
		if removeResult.IsError() {
			summary = append(summary, fmt.Sprintf("Warning: failed to remove default network: %s", removeResult.Message))
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

	auditLog := logging.NewAuditLog(cacheDir)
	_ = auditLog.LogOperation("host.clean", map[string]interface{}{"actions": len(summary)}, "")

	return &errs.OperationResult{
		Status: "success", Code: "host.cleaned",
		Message: fmt.Sprintf("Cleaned %d networking item(s)", len(summary)),
		Item:    summary,
	}
}

// HostReset resets host to pre-init state.
// Matches Python's HostOperation.reset() exactly — usermod processing order matches.
func (op *Operation) HostReset(ctx context.Context, cacheDir string) *errs.OperationResult {

	ph := &host.PrivilegeHelper{}
	if err := ph.CheckPrivileges("/usr/sbin/ip", "reset host"); err != nil {
		return &errs.OperationResult{
			Status: "error", Code: string(errs.CodePrivilegeRequired),
			Message:   fmt.Sprintf("Privilege check failed: %v", err),
			Exception: err,
		}
	}

	cleanResult := op.HostClean(ctx, cacheDir)
	// Python: if clean_result.is_error: return clean_result
	if cleanResult.IsError() {
		return cleanResult
	}
	var summary []string
	if cleanResult.IsOK() && cleanResult.Item != nil {
		if items, ok := cleanResult.Item.([]string); ok {
			summary = append(summary, items...)
		}
	}

	reverted, err := op.Services.Host.RestoreState(ctx)
	if err != nil {
		slog.Warn("No saved host state to restore", "error", err)
	} else {
		for _, change := range reverted {
			summary = append(summary, fmt.Sprintf("Reverted %s", change.Setting))
		}
	}

	// Notify about kernel modules that were left loaded
	moduleChanges, _ := op.Repos.Host.ListChanges(ctx, nil, false)
	var activeModules []string
	for _, c := range moduleChanges {
		if c.Setting == "kernel_module_load" {
			activeModules = append(activeModules, c.AppliedValue)
		}
	}
	if len(activeModules) > 0 {
		summary = append(summary,
			fmt.Sprintf("Modules loaded by mvm: %s. These were left loaded. Unload manually with 'modprobe -r <module>' if desired.",
				strings.Join(activeModules, ", ")))
	}

	sudoersPath := infra.SudoersDropInPath()
	if removed, err := host.RemoveSudoers(ctx, sudoersPath); err != nil {
		summary = append(summary, fmt.Sprintf("Warning: %v", err))
	} else if removed {
		summary = append(summary, fmt.Sprintf("Removed sudoers file %s", sudoersPath))
	}

	// Python: Remove user from group FIRST, then remove group (matches Python order)
	// Python only processes the LAST usermod change (usermod_changes[-1].applied_value).
	usermodChanges, _ := op.Repos.Host.ListChanges(ctx, nil, false)
	var lastUsermod *model.HostStateChangeItem
	for _, c := range usermodChanges {
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

	auditLog := logging.NewAuditLog(cacheDir)
	_ = auditLog.LogOperation("host.reset", map[string]interface{}{"actions": len(summary)}, "")

	return &errs.OperationResult{
		Status: "success", Code: "host.reset",
		Message: fmt.Sprintf("Reset %d item(s)", len(summary)),
		Item:    summary,
	}
}

// HostGetRunningVMs returns running VMs.
// Matches Python's HostOperation.get_running_vms().
func (op *Operation) HostGetRunningVMs(ctx context.Context) ([]*model.VM, error) {
	return op.Repos.VM.ListByStatus(ctx, string(model.StatusRunning))
}

// HostIsInitialized checks if host is initialized.
// Matches Python's HostOperation.is_initialized().
func (op *Operation) HostIsInitialized(ctx context.Context) bool {
	state, err := op.Repos.Host.GetState(ctx)
	return err == nil && state != nil && state.Initialized
}

// HostCheckReadiness runs pre-flight checks.
// Matches Python's HostOperation.check_readiness().
func (op *Operation) HostCheckReadiness() *model.ProbeResult {
	probe := &host.Probe{}
	return probe.RunAll()
}

// ── Host helpers inlined from internal/core/host/_host_info.go ──
// (Go ignores files starting with _, so these were never compiled into the host package.)

// hardwareFromState reconstructs HostHardware from stored state, or returns nil if not yet detected.
func hardwareFromState(state *model.HostStateItem) *model.HostHardware {
	if state.CPUModel == nil {
		return nil
	}
	h := &model.HostHardware{}
	if state.Hostname != nil {
		h.Hostname = *state.Hostname
	}
	if state.CPUModel != nil {
		h.CPUModel = *state.CPUModel
	}
	if state.CPUVendor != nil {
		h.CPUVendor = *state.CPUVendor
	}
	if state.CPUCores != nil {
		h.CPUCores = *state.CPUCores
	}
	if state.CPUArchitecture != nil {
		h.CPUArchitecture = *state.CPUArchitecture
	}
	if state.NumaNodes != nil && *state.NumaNodes != 0 {
		h.NumaNodes = *state.NumaNodes
	} else {
		h.NumaNodes = 1
	}
	if state.MemoryTotalMiB != nil {
		h.MemoryTotalMiB = *state.MemoryTotalMiB
	}
	if state.StorageTotalBytes != nil {
		h.StorageTotalBytes = *state.StorageTotalBytes
	}
	if state.KernelVersion != nil {
		h.KernelVersion = *state.KernelVersion
	}
	if state.OSRelease != nil {
		h.OSRelease = *state.OSRelease
	}
	if state.CPUHasVMX != nil {
		h.CPUHasVMX = *state.CPUHasVMX != 0
	}
	if state.CPUHypervisor != nil {
		h.CPUHypervisor = *state.CPUHypervisor != 0
	}
	return h
}

// limitsFromState reconstructs HostLimits from stored state, or returns nil if not yet detected.
func limitsFromState(state *model.HostStateItem) *model.HostLimits {
	if state.PIDMax == nil {
		return nil
	}
	var portRange [2]int
	if state.IPLocalPortRange != nil {
		portRange = validators.ParsePortRange(*state.IPLocalPortRange)
	} else {
		portRange = [2]int{32768, 60999}
	}
	l := &model.HostLimits{}
	if state.PIDMax != nil {
		l.PIDMax = *state.PIDMax
	}
	if state.FDMax != nil {
		l.FDMax = *state.FDMax
	}
	if state.ConntrackMax != nil {
		l.ConntrackMax = *state.ConntrackMax
	}
	if state.TAPDevicesMax != nil {
		l.TAPDevicesMax = *state.TAPDevicesMax
	}
	l.IPLocalPortRange = portRange
	if state.NestedVirtAvailable != nil {
		l.NestedVirtAvailable = *state.NestedVirtAvailable != 0
	}
	if state.EPTAvailable != nil {
		l.EPTAvailable = *state.EPTAvailable != 0
	}
	if state.HugepageCount2MB != nil {
		l.HugepageCount2MB = *state.HugepageCount2MB
	}
	if state.KSMDisabled != nil {
		l.KSMDisabled = *state.KSMDisabled != 0
	} else {
		l.KSMDisabled = true
	}
	if state.CgroupVersion != nil && *state.CgroupVersion != 0 {
		l.CgroupVersion = *state.CgroupVersion
	} else {
		l.CgroupVersion = 1
	}
	if state.SwapTotalMiB != nil {
		l.SwapTotalMiB = *state.SwapTotalMiB
	}
	if state.KernelMinimumMet != nil {
		l.KernelMinimumMet = *state.KernelMinimumMet != 0
	}
	return l
}

// hostInfoToDict builds the standardised info response dict from host info.
func hostInfoToDict(hi *model.HostInfo) map[string]interface{} {
	detectedAt := ""
	if hi.State.DetectedAt != nil {
		detectedAt = *hi.State.DetectedAt
	}

	modulesLoaded := make(map[string]bool)
	for k, v := range hi.Resources.ModulesLoaded {
		modulesLoaded[k] = v
	}

	return map[string]interface{}{
		"detected_at": detectedAt,
		"hostname":    hi.Hardware.Hostname,
		"os": map[string]interface{}{
			"kernel":  hi.Hardware.KernelVersion,
			"release": hi.Hardware.OSRelease,
		},
		"cpu": map[string]interface{}{
			"model":        hi.Hardware.CPUModel,
			"vendor":       hi.Hardware.CPUVendor,
			"cores":        hi.Hardware.CPUCores,
			"architecture": hi.Hardware.CPUArchitecture,
			"numa_nodes":   hi.Hardware.NumaNodes,
		},
		"virtualization": map[string]interface{}{
			"cpu_has_vmx":           hi.Hardware.CPUHasVMX,
			"nested_virt_available": hi.Limits.NestedVirtAvailable,
			"ept_available":         hi.Limits.EPTAvailable,
			"hypervisor":            hi.Hardware.CPUHypervisor,
			"smt_active":            hi.Resources.SMTActive,
			"modules":               modulesLoaded,
		},
		"hugepages": map[string]interface{}{
			"count_2mb": hi.Limits.HugepageCount2MB,
			"free_2mb":  hi.Resources.HugepagesFree2MB,
		},
		"dependencies": map[string]interface{}{
			"nftables_available":      hi.Resources.NftablesAvailable,
			"iptables_available":      hi.Resources.IptablesAvailable,
			"cloud_localds_available": hi.Resources.CloudLocaldsAvailable,
			"dev_net_tun":             hi.Resources.DevNetTUNAccessible,
		},
		"system": map[string]interface{}{
			"cgroup_version":    hi.Limits.CgroupVersion,
			"ksm_disabled":      hi.Limits.KSMDisabled,
			"dev_kvm_status":    hi.Resources.DevKVMStatus,
			"user_in_kvm_group": hi.Resources.UserInKVMGroup,
		},
		"memory": map[string]interface{}{
			"total_mib":      hi.Hardware.MemoryTotalMiB,
			"available_mib":  hi.Resources.MemoryAvailableMiB,
			"swap_total_mib": hi.Limits.SwapTotalMiB,
			"swap_used_mib":  hi.Resources.SwapUsedMiB,
		},
		"storage": map[string]interface{}{
			"total_bytes": hi.Hardware.StorageTotalBytes,
			"free_bytes":  hi.Resources.StorageFreeBytes,
		},
		"kernel": map[string]interface{}{
			"version":             hi.Hardware.KernelVersion,
			"minimum_version_met": hi.Limits.KernelMinimumMet,
		},
		"limits": map[string]interface{}{
			"pid_max":             hi.Limits.PIDMax,
			"fd_max":              hi.Limits.FDMax,
			"conntrack_max":       hi.Limits.ConntrackMax,
			"tap_devices_max":     hi.Limits.TAPDevicesMax,
			"ip_local_port_range": []int{hi.Limits.IPLocalPortRange[0], hi.Limits.IPLocalPortRange[1]},
		},
		"capacity": map[string]interface{}{
			"current": map[string]interface{}{
				"pids":        hi.Resources.PIDsCurrent,
				"fds":         hi.Resources.FDCurrent,
				"conntrack":   hi.Resources.ConntrackCurrent,
				"tap_devices": hi.Resources.TAPDevicesUsed,
				"arp_entries": hi.Resources.ARPCurrent,
			},
			"recommended_max_vms": hi.Resources.RecommendedMaxVMs,
			"limiting_resource":   hi.Resources.LimitingResource,
		},
		"setup": map[string]interface{}{
			"initialized":    hi.State.Initialized,
			"initialized_at": hi.State.InitializedAt,
		},
	}
}

// Compile-time checks
var _ = regexp.MustCompile
