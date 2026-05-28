// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/init_operations.py exactly.
package api

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/host"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/db"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api/inputs"
)

// InitOperation orchestrates the init wizard.
// Matches Python's InitOperation exactly with all 6+ steps.
type InitOperation struct {
	hostOp    *HostOperation
	cacheOp   *CacheOperation
	binOp     *BinaryOperation
	netOp     *NetworkOperation
	configSvc *config.Service
	hostRepo  host.Repository
	binRepo   binary.Repository
	cacheDir  string
	db        *sql.DB
}

// NewInitOperation creates an InitOperation.
func NewInitOperation(
	hostOp *HostOperation,
	cacheOp *CacheOperation,
	binOp *BinaryOperation,
	netOp *NetworkOperation,
	configSvc *config.Service,
	hostRepo host.Repository,
	binRepo binary.Repository,
	cacheDir string,
	db *sql.DB,
) *InitOperation {
	return &InitOperation{
		hostOp:    hostOp,
		cacheOp:   cacheOp,
		binOp:     binOp,
		netOp:     netOp,
		configSvc: configSvc,
		hostRepo:  hostRepo,
		binRepo:   binRepo,
		cacheDir:  cacheDir,
		db:        db,
	}
}

// InitStepResult matches Python's InitStepResult dataclass.
type InitStepResult struct {
	Step    string `json:"step"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// InitResult matches Python's InitResult dataclass.
type InitResult struct {
	Steps            []InitStepResult       `json:"steps"`
	HostReady        bool                   `json:"host_ready"`
	NeedsInteraction *errs.NeedsInteraction `json:"needs_interaction,omitempty"`
}

// CheckReadiness runs pre-flight host readiness checks via the public API layer.
// Matches Python's HostOperation.check_readiness() called from CLI.
func (o *InitOperation) CheckReadiness() *model.ProbeResult {
	return o.hostOp.CheckReadiness()
}

// SetupHost sets up host configuration.
// Matches Python's InitOperation.setup_host() exactly.
func (o *InitOperation) SetupHost(ctx context.Context, cacheDir string) *errs.OperationResult {
	raw := o.hostOp.Init(ctx, cacheDir, nil)
	if result, ok := raw.(*errs.OperationResult); ok {
		return result
	}
	if _, ok := raw.(*errs.NeedsInteraction); ok {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "privilege.sudo_required",
			Message: "Root privileges required",
		}
	}
	return nil
}

// Run runs the init wizard steps in sequence.
// Matches Python's InitOperation.run() with backward-compatible signature.
// hostSetupMessage defaults to "", guestfsEnabled defaults to nil (auto-detect).
func (o *InitOperation) Run(
	ctx context.Context,
	skipHost bool,
	skipNetwork bool,
	nonInteractive bool,
	sudoCompleted bool,
	downloadVersion string,
	onProgress func(errs.ProgressEvent),
) *InitResult {
	return o.RunFull(ctx, skipHost, skipNetwork, nonInteractive, sudoCompleted, "", downloadVersion, nil, onProgress)
}

// RunFull runs the init wizard steps with full parameters matching Python's InitOperation.run().
func (o *InitOperation) RunFull(
	ctx context.Context,
	skipHost bool,
	skipNetwork bool,
	nonInteractive bool,
	sudoCompleted bool,
	hostSetupMessage string,
	downloadVersion string,
	guestfsEnabled *bool,
	onProgress func(errs.ProgressEvent),
) *InitResult {
	steps := make([]InitStepResult, 0)

	// ── Step 1: Local state ──
	steps = append(steps, o.initDatabase(ctx))

	// ── Step 3: Host ──
	hostResult, hostInteraction := o.stepHost(ctx, skipHost, sudoCompleted, hostSetupMessage, onProgress)
	steps = append(steps, hostResult)
	if hostInteraction != nil {
		return &InitResult{
			Steps:            steps,
			HostReady:        false,
			NeedsInteraction: hostInteraction,
		}
	}

	// ── Step 4: Guestfs ──
	guestfsResult, guestfsInteraction := o.stepGuestfs(ctx, guestfsEnabled)
	steps = append(steps, guestfsResult)
	if guestfsInteraction != nil {
		return &InitResult{
			Steps:            steps,
			HostReady:        false,
			NeedsInteraction: guestfsInteraction,
		}
	}

	// ── Step 5: Network setup ──
	if skipNetwork {
		steps = append(steps, InitStepResult{Step: "network_setup", Success: true, Message: "Skipped (--skip-network)"})
	} else {
		steps = append(steps, o.stepNetworkSetup(ctx))
	}

	// ── Step 6: Cache ──
	steps = append(steps, o.stepCache(ctx, onProgress))

	// ── Step 7: Binary ──
	binaryResult, binaryInteraction := o.stepBinary(ctx, nonInteractive, downloadVersion)
	steps = append(steps, binaryResult)
	if binaryInteraction != nil {
		return &InitResult{
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

	return &InitResult{
		Steps:     steps,
		HostReady: hostReady && binaryReady,
	}
}

func (o *InitOperation) initDatabase(ctx context.Context) InitStepResult {
	// Python: try: InitOperation.init_database() except Exception as e:
	//         return InitStepResult("local_state", False, f"Failed: {e}")
	// Python's init_database: db = Database(); db.migrate()
	// Go: run migrations via db.RunMigrationsCtx, wrapped in explicit error handling.
	if o.db != nil {
		if _, err := db.RunMigrationsCtx(ctx, o.db, filepath.Join(o.cacheDir, infra.MVMDBFilename)); err != nil {
			return InitStepResult{Step: "local_state", Success: false, Message: fmt.Sprintf("Failed: %v", err)}
		}
	}
	return InitStepResult{Step: "local_state", Success: true, Message: "Local state ready"}
}

func (o *InitOperation) stepHost(ctx context.Context, skip bool, sudoCompleted bool, setupMessage string, onProgress func(errs.ProgressEvent)) (InitStepResult, *errs.NeedsInteraction) {
	if skip {
		return InitStepResult{Step: "host", Success: true, Message: "Skipped (--skip-host)"}, nil
	}

	if sudoCompleted {
		msg := setupMessage
		if msg == "" {
			msg = "completed"
		}
		return InitStepResult{Step: "host", Success: true, Message: msg}, nil
	}

	initResult := o.hostOp.Init(ctx, o.cacheDir, onProgress)
	if initResult == nil {
		return InitStepResult{Step: "host", Success: false, Message: "Host init returned no result"}, nil
	}

	// Check for NeedsInteraction (Python: isinstance(result, NeedsInteraction))
	if interaction, ok := initResult.(*errs.NeedsInteraction); ok {
		return InitStepResult{Step: "host", Success: false, Message: "Root privileges required"}, interaction
	}

	// Type assertion to OperationResult
	hostResult, ok := initResult.(*errs.OperationResult)
	if !ok {
		return InitStepResult{Step: "host", Success: false, Message: "Unexpected result from host init"}, nil
	}

	if hostResult.IsOK() {
		if hostResult.Status == "skipped" {
			return InitStepResult{Step: "host", Success: true, Message: "Host already configured"}, nil
		}
		return InitStepResult{Step: "host", Success: true, Message: "Host initialized"}, nil
	}

	return InitStepResult{Step: "host", Success: false, Message: hostResult.Message}, nil
}

func (o *InitOperation) stepNetworkSetup(ctx context.Context) InitStepResult {
	result := o.hostOp.NetworkSetup(ctx)
	success := result.IsOK()
	msg := result.Message
	if msg == "" {
		if success {
			msg = "Default network ready"
		} else {
			msg = "Failed to create default network"
		}
	}
	return InitStepResult{Step: "network_setup", Success: success, Message: msg}
}

func (o *InitOperation) stepCache(ctx context.Context, onProgress func(errs.ProgressEvent)) InitStepResult {
	// Python: try: result = CacheOperation.init_all(...); except Exception as e:
	//         return InitStepResult("cache", False, f"Cache init failed: {e}")
	result := o.cacheOp.InitAll(ctx, onProgress)
	if result.IsError() {
		return InitStepResult{Step: "cache", Success: false, Message: result.Message}
	}
	// Python: checks cache_dict.get("guestfs_appliance")
	cacheDict := map[string]interface{}{}
	if result.Item != nil {
		if m, ok := result.Item.(map[string]interface{}); ok {
			cacheDict = m
		}
	}
	guestfsBuilt := false
	if a, ok := cacheDict["guestfs_appliance"]; ok && a != nil && a != "" {
		guestfsBuilt = true
	}
	msg := "Cache directories ready"
	if guestfsBuilt {
		msg = "Cache directories ready (libguestfs appliance built)"
	}
	return InitStepResult{Step: "cache", Success: true, Message: msg}
}

func (o *InitOperation) stepBinary(ctx context.Context, nonInteractive bool, downloadVersion string) (InitStepResult, *errs.NeedsInteraction) {
	// Python: local = cast(list[BinaryItem], BinaryOperation.list_all())
	local, err := o.binOp.ListAll(ctx)
	if err != nil {
		return InitStepResult{Step: "binary", Success: false, Message: "Failed to list binaries"}, nil
	}

	// Python: fc_binaries = [b for b in local if b.name in ("firecracker", "jailer")]
	fcBinaries := make([]*model.BinaryItem, 0)
	for _, b := range local {
		if b.Name == "firecracker" || b.Name == "jailer" {
			fcBinaries = append(fcBinaries, b)
		}
	}

	if len(fcBinaries) > 0 {
		// Python: active = [v for v in fc_binaries if v.is_default]
		active := make([]*model.BinaryItem, 0)
		for _, v := range fcBinaries {
			if v.IsDefault {
				active = append(active, v)
			}
		}
		if len(active) > 0 {
			return InitStepResult{Step: "binary", Success: true, Message: fmt.Sprintf("Binary available (v%s)", active[0].Version)}, nil
		}
		// Python: repaired = BinaryOperation.ensure_default()
		repaired := o.binOp.EnsureDefault(ctx)
		if !repaired.IsError() && repaired.Item != nil {
			if item, ok := repaired.Item.(*model.BinaryItem); ok {
				return InitStepResult{Step: "binary", Success: true, Message: fmt.Sprintf("Binary available (v%s) — set as default", item.Version)}, nil
			}
		}
		return InitStepResult{Step: "binary", Success: true, Message: fmt.Sprintf("Binary available (v%s)", fcBinaries[0].Version)}, nil
	}

	// No local binaries found
	if downloadVersion != "" {
		return o.downloadBinary(ctx, downloadVersion), nil
	}

	if nonInteractive {
		return o.downloadBinaryLatest(ctx), nil
	}

	// Needs interaction (Python: _binary_needs_interaction())
	return o.binaryNeedsInteraction(ctx)
}

func (o *InitOperation) downloadBinary(ctx context.Context, version string) InitStepResult {
	// Python: BinaryOperation.pull(BinaryPullInput(version=version, set_default=True))
	// Python: try: fetch_result = BinaryOperation.pull(...); if isinstance(fetch_result, NeedsInteraction): ...
	pullResult := o.binOp.Pull(ctx, &inputs.BinaryPullInput{Version: version, SetDefault: true})

	// Python: if isinstance(fetch_result, NeedsInteraction):
	// In Go, Pull returns *OperationResult. A NeedsInteraction code indicates
	// the operation requires user confirmation before proceeding.
	if isNeedsInteraction(pullResult) {
		return InitStepResult{Step: "binary", Success: false, Message: "Binary download requires interaction"}
	}

	if pullResult.IsError() {
		return InitStepResult{Step: "binary", Success: false, Message: fmt.Sprintf("Download failed: %s", pullResult.Message)}
	}
	// Python: get version from result item
	binaries := []*model.BinaryItem{}
	if pullResult.Item != nil {
		if items, ok := pullResult.Item.([]*model.BinaryItem); ok {
			binaries = items
		}
	}
	versionStr := version
	for _, b := range binaries {
		if b.Name == "firecracker" {
			versionStr = b.Version
			break
		}
	}
	return InitStepResult{Step: "binary", Success: true, Message: fmt.Sprintf("Downloaded v%s", versionStr)}
}

func (o *InitOperation) downloadBinaryLatest(ctx context.Context) InitStepResult {
	// Python: try: BinaryOperation.list_all(remote=True, limit=1); except BinaryError:
	//         return InitStepResult("binary", False, f"Download failed: {e}")
	// Go wraps the list and pull in an error-checking pattern.
	remote, err := o.binOp.ListRemote(ctx, 1)
	if err != nil || len(remote) == 0 {
		return InitStepResult{Step: "binary", Success: false, Message: "No remote versions found"}
	}

	version := remote[0]
	pullResult := o.binOp.Pull(ctx, &inputs.BinaryPullInput{Version: version, SetDefault: true})

	// Python: if isinstance(fetch_result, NeedsInteraction):
	if isNeedsInteraction(pullResult) {
		return InitStepResult{Step: "binary", Success: false, Message: "Binary download requires interaction"}
	}

	if pullResult.IsError() {
		return InitStepResult{Step: "binary", Success: false, Message: fmt.Sprintf("Download failed: %s", pullResult.Message)}
	}
	// Python: get version from result item
	binaries := []*model.BinaryItem{}
	if pullResult.Item != nil {
		if items, ok := pullResult.Item.([]*model.BinaryItem); ok {
			binaries = items
		}
	}
	versionStr := version
	for _, b := range binaries {
		if b.Name == "firecracker" {
			versionStr = b.Version
			break
		}
	}
	return InitStepResult{Step: "binary", Success: true, Message: fmt.Sprintf("Downloaded v%s", versionStr)}
}

// isNeedsInteraction checks if an OperationResult indicates that the operation
// requires user interaction before proceeding.
// Python: isinstance(result, NeedsInteraction) instead of OperationResult.
// In Go, this checks for a specific code or status that represents the
// needs-interaction state.
func isNeedsInteraction(result *errs.OperationResult) bool {
	if result == nil {
		return false
	}
	// Python returns OperationResult with status "interaction" or code
	// prefixed with "interaction." when NeedsInteraction is expected.
	return result.Code == "interaction_required" || result.Status == "interaction"
}

func (o *InitOperation) binaryNeedsInteraction(ctx context.Context) (InitStepResult, *errs.NeedsInteraction) {
	// Python: try: versions = BinaryOperation.list_all(remote=True, limit=5)
	//         except BinaryError: versions = []
	remote, err := o.binOp.ListRemote(ctx, 5)

	var versions []string
	if err == nil {
		versions = remote
	}

	if len(versions) == 0 {
		return InitStepResult{Step: "binary", Success: false, Message: "No remote versions available"},
			&errs.NeedsInteraction{
				Code:      "binary.confirm_download",
				Message:   "No remote versions available",
				InputType: "confirm",
				Context:   map[string]interface{}{},
			}
	}

	return InitStepResult{Step: "binary", Success: false, Message: "No Firecracker binary found in cache"},
		&errs.NeedsInteraction{
			Code:      "binary.confirm_download",
			Message:   "No Firecracker binary found in cache",
			InputType: "confirm",
			Context: map[string]interface{}{
				"latest_version":     versions[0],
				"available_versions": versions,
			},
		}
}

func (o *InitOperation) stepGuestfs(ctx context.Context, guestfsEnabled *bool) (InitStepResult, *errs.NeedsInteraction) {
	// Python: Matches InitOperation._step_guestfs(guestfs_enabled=guestfs_enabled)
	// When guestfs_enabled is provided (from a previous interaction round),
	// the decision is persisted directly.
	if guestfsEnabled != nil {
		if o.configSvc != nil {
			_ = o.configSvc.Set(ctx, "settings", "guestfs_enabled", *guestfsEnabled)
		}
		if *guestfsEnabled {
			return InitStepResult{Step: "guestfs", Success: true, Message: "enabled"}, nil
		}
		return InitStepResult{Step: "guestfs", Success: true, Message: "disabled"}, nil
	}

	// First pass — detect availability
	// Python: try: import guestfs; available = True except ImportError: available = False
	// Python checks whether the Python guestfs module can be imported (python3-guestfs
	// package). We match this by checking for the guestfish binary, which is the most
	// common tool from the libguestfs package and reliably indicates availability.
	// Go does NOT spawn a python3 subprocess — it checks for the guestfs tools directly.
	available := false
	if _, err := exec.LookPath("guestfish"); err == nil {
		available = true
	}

	if !available {
		// libguestfs not installed — no point prompting (Python: svc.set("settings", "guestfs_enabled", False))
		if o.configSvc != nil {
			_ = o.configSvc.Set(ctx, "settings", "guestfs_enabled", false)
		}
		return InitStepResult{Step: "guestfs", Success: true, Message: "not installed"}, nil
	}

	// Installed but user hasn't decided — prompt (Python: NeedsInteraction)
	return InitStepResult{Step: "guestfs", Success: false, Message: "available"},
		&errs.NeedsInteraction{
			Code:      "guestfs.confirm_enable",
			Message:   "libguestfs is available. Enable it as a fallback?",
			InputType: "confirm",
			Context:   map[string]interface{}{},
		}
}

// Compile-time check
var _ = infra.SHA256Hash
