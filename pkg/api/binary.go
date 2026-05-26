// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/binary_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/core/config"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/enricher"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/version"
	"mvmctl/pkg/api/inputs"
)

// BinaryOperation orchestrates binary management.
// Matches Python's BinaryOperation exactly.
type BinaryOperation struct {
	svc         *binary.Service
	repo        binary.Repository
	vmRepo      vm.Repository
	settingsSvc *config.Service
	cacheDir    string
	enr         *enricher.Enricher
}

// NewBinaryOperation creates a BinaryOperation.
// Matches Python's BinaryOperation() which creates internal Database/repo/service.
func NewBinaryOperation(svc *binary.Service, vmRepo vm.Repository, cacheDir string, settingsSvc *config.Service, enr *enricher.Enricher) *BinaryOperation {
	return &BinaryOperation{
		svc:         svc,
		repo:        svc.Repo(),
		vmRepo:      vmRepo,
		cacheDir:    cacheDir,
		settingsSvc: settingsSvc,
		enr:         enr,
	}
}

// Prune prunes unused binaries.
// Matches Python's BinaryOperation.prune() exactly.
func (o *BinaryOperation) Prune(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	allBinaries, err := o.repo.ListAll(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeDatabaseError),
			Message:   fmt.Sprintf("Failed to list binaries: %v", err),
			Exception: err,
		}
	}

	defaultBinary, _ := o.repo.GetDefault(ctx, "firecracker")
	var defaultVersion string
	if defaultBinary != nil {
		defaultVersion = defaultBinary.Version
	}

	removed := make([]string, 0)
	for _, bin := range allBinaries {
		if !includeAll {
			if bin.Version == defaultVersion {
				continue
			}
		}

		if !dryRun {
			// Match Python: BinaryOperation.remove(BinaryInput(identifiers=[binary.id]), force=include_all)
			removeResult := o.Remove(ctx, &inputs.BinaryInput{Identifiers: []string{bin.ID}}, includeAll)
			if removeResult.HasErrors() {
				slog.Warn("Failed to remove binary",
					"name", bin.Name, "version", bin.Version, "error", removeResult.Errors()[0].Message)
				continue
			}
		}
		removed = append(removed, fmt.Sprintf("%s:%s", bin.Name, bin.Version))
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.pruned",
		Message: fmt.Sprintf("Pruned %d binary(ies)", len(removed)),
		Item:    removed,
	}
}

// Pull downloads or builds a binary.
// Matches Python's BinaryOperation.pull() exactly — uses BinaryPullRequest
// resolution pipeline and wraps all BinaryErrors in code="binary.pull_failed".
func (o *BinaryOperation) Pull(ctx context.Context, input *inputs.BinaryPullInput) *errs.OperationResult {
	// Python: request = BinaryPullRequest(inputs=inputs, db=db); resolved = request.resolve()
	request := inputs.NewBinaryPullRequest(*input, nil)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "binary.pull_failed",
			Message:   err.Error(),
			Exception: err,
		}
	}

	// ---- Git build path (parallel to release download) ----
	if resolved.GitRef != nil && *resolved.GitRef != "" {
		// Python passes bin_dir=resolved.bin_dir to BinaryService.build_from_source()
		binaries, err := o.svc.BuildFromSource(ctx, *resolved.GitRef, resolved.BinDir)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "binary.pull_failed",
				Message:   fmt.Sprintf("Build failed: %v", err),
				Exception: err,
			}
		}

		noDefault, _ := o.repo.GetDefault(ctx, "firecracker")
		shouldSetDefault := resolved.SetDefault || noDefault == nil

		for _, b := range binaries {
			_ = o.repo.Upsert(ctx, b)
			if shouldSetDefault {
				_ = o.repo.SetDefault(ctx, b.Name, b.Version, b.Path)
			}
		}

		versionStr := ""
		if len(binaries) > 0 {
			versionStr = binaries[0].Version
		}

		auditLog := logging.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("binary.pull", map[string]interface{}{
			"git_ref": *resolved.GitRef,
			"version": versionStr,
		}, "")

		return &errs.OperationResult{
			Status:  "success",
			Code:    "binary.built_from_source",
			Message: fmt.Sprintf("Built Firecracker %s from ref '%s'", versionStr, *resolved.GitRef),
			Item:    binaries,
		}
	}

	// ---- Release download path ----
	resolvedVersion := resolved.Version
	if resolvedVersion == "" {
		remoteVersions, err := o.svc.ListRemote(ctx, 20)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "binary.pull_failed",
				Message:   fmt.Sprintf("Failed to list remote versions: %v", err),
				Exception: err,
			}
		}
		if len(remoteVersions) == 0 {
			return &errs.OperationResult{
				Status:  "error",
				Code:    "binary.no_remote_versions",
				Message: "No remote Firecracker versions found",
			}
		}
		resolvedVersion = remoteVersions[0]
	}

	normalized := binary.NormalizeVersion(resolvedVersion)
	fcExists, _ := o.repo.GetByNameAndVersion(ctx, "firecracker", normalized)
	jlExists, _ := o.repo.GetByNameAndVersion(ctx, "jailer", normalized)
	versionExists := fcExists != nil && jlExists != nil

	if versionExists && !resolved.DownloadOverride {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "binary.pull_failed",
			Message: fmt.Sprintf("Firecracker v%s already exists. Use --force to re-download.", normalized),
		}
	}

	noDefault, _ := o.repo.GetDefault(ctx, "firecracker")
	shouldSetDefault := resolved.SetDefault || noDefault == nil

	// Python passes bin_dir=resolved.bin_dir to BinaryService.download_firecracker()
	binaries, err := o.svc.DownloadFirecracker(ctx, resolvedVersion, resolved.BinDir)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "binary.pull_failed",
			Message:   fmt.Sprintf("Download failed: %v", err),
			Exception: err,
		}
	}

	for _, b := range binaries {
		_ = o.repo.Upsert(ctx, b)
		if shouldSetDefault {
			_ = o.repo.SetDefault(ctx, b.Name, b.Version, b.Path)
		}
	}

	auditLog := logging.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("binary.pull", map[string]interface{}{"version": resolvedVersion}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "binary.downloaded",
		Message: fmt.Sprintf("Downloaded Firecracker v%s", normalized),
		Item:    binaries,
	}
}

// Remove removes binaries by identifiers.
// Matches Python's BinaryOperation.remove() exactly — resolves via BinaryRequest
// then enriches with VM references. Each binary removal is wrapped in per-binary
// error handling (matching Python's try/except (BinaryError, BinaryNotFoundError)).
func (o *BinaryOperation) Remove(ctx context.Context, input *inputs.BinaryInput, force bool) *errs.BatchResult {
	request := inputs.NewBinaryRequest(*input, o.repo)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.BatchResult{
			Items: []errs.OperationResult{
				{
					Status:    "error",
					Code:      "binary.remove_failed",
					Message:   err.Error(),
					Exception: err,
				},
			},
		}
	}

	// Enrich binaries with VM references (matching Python's:
	//   enriched = Resolver(repo, include=["vm"]).enrich(resolved.binaries)
	enriched := resolved.Binaries
	if o.enr != nil {
		_ = o.enr.EnrichBinary(ctx, enriched)
	}

	items := make([]errs.OperationResult, 0)

	for _, bin := range enriched {
		// Python: svc.remove(binary, force=force)
		// Go: svc.Remove returns (*BinaryItem, error)
		if _, err := o.svc.Remove(ctx, bin, force); err != nil {
			items = append(items, errs.OperationResult{
				Status:    "error",
				Code:      "binary.remove_failed",
				Message:   fmt.Sprintf("Failed to remove %s v%s: %v", bin.Name, bin.FullVersion, err),
				Exception: err,
				Item:      bin,
			})
			continue
		}

		auditLog := logging.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("binary.remove", map[string]interface{}{
			"id":      bin.ID,
			"name":    bin.Name,
			"version": bin.FullVersion,
		}, "")

		items = append(items, errs.OperationResult{
			Status:  "success",
			Code:    "binary.removed",
			Message: fmt.Sprintf("Removed %s v%s", bin.Name, bin.FullVersion),
			Item:    bin,
		})
	}

	return &errs.BatchResult{Items: items}
}

// RemoveByVersion removes both firecracker and jailer for a version.
// Matches Python's BinaryOperation.remove_by_version() exactly — wraps the
// entire flow in try/except (BinaryError, BinaryNotFoundError).
func (o *BinaryOperation) RemoveByVersion(ctx context.Context, version string, force bool) *errs.OperationResult {
	resolver := binary.NewResolver(o.repo)

	normalized := binary.NormalizeVersion(version)

	binariesToRemove := make([]*model.BinaryItem, 0)
	for _, name := range []string{"firecracker", "jailer"} {
		bin, err := resolver.ByNameVersion(ctx, name, normalized)
		if err != nil {
			// Python: logger.debug("Binary %s v%s not found in DB, skipping", name, normalized)
			slog.Debug("Binary not found in DB, skipping",
				"name", name, "version", normalized, "error", err)
			continue
		}
		binariesToRemove = append(binariesToRemove, bin)
	}

	if len(binariesToRemove) == 0 {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "binary.not_found",
			Message: fmt.Sprintf("No binaries found for version %s", normalized),
		}
	}

	// Batch-enrich with VM references (matching Python's:
	//   enriched = Resolver(repo, include=["vm"]).enrich(binaries_to_remove)
	for _, bin := range binariesToRemove {
		if bin.VMs == nil {
			vms, err := o.vmRepo.FindByBinaryID(ctx, bin.ID)
			if err == nil && len(vms) > 0 {
				for _, vm := range vms {
					bin.VMs = append(bin.VMs, vm)
				}
			}
		}
	}

	for _, bin := range binariesToRemove {
		// Python: svc.remove(binary, force=force) — returns (*BinaryItem, error)
		if _, err := o.svc.Remove(ctx, bin, force); err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "binary.remove_failed",
				Message:   fmt.Sprintf("Failed to remove %s: %v", bin.Name, err),
				Exception: err,
			}
		}
		auditLog := logging.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("binary.remove", map[string]interface{}{
			"id":      bin.ID,
			"name":    bin.Name,
			"version": normalized,
		}, "")
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "binary.removed",
		Message: fmt.Sprintf("Removed binaries for v%s", normalized),
	}
}

// ListAll returns all local binaries.
// Matches Python's BinaryOperation.list_all(remote=False) exactly.
func (o *BinaryOperation) ListAll(ctx context.Context) ([]*model.BinaryItem, error) {
	return o.svc.ListAll(ctx, false, true)
}

// ListRemote returns available remote versions.
// Matches Python's BinaryOperation.list_all(remote=True) exactly.
// When limit <= 0, reads the default from SettingsService like Python:
//
//	SettingsService.resolve(Database(), "defaults.binary", "remote_version_limit")
func (o *BinaryOperation) ListRemote(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		if o.settingsSvc != nil {
			rawLimit, _ := o.settingsSvc.Get(ctx, "defaults.binary", "remote_version_limit")
			if rawLimit != nil {
				switch v := rawLimit.(type) {
				case int:
					limit = v
				case float64:
					limit = int(v)
				case string:
					limit, _ = strconv.Atoi(v)
				}
			}
		}
		if limit <= 0 {
			dflt, _ := infra.GetDefault("defaults.binary", "remote_version_limit")
			switch v := dflt.(type) {
			case int:
				limit = v
			case float64:
				limit = int(v)
			}
		}
		if limit <= 0 {
			limit = 20
		}
	}
	return o.svc.ListRemote(ctx, limit)
}

// Get returns binaries by identifier.
// Matches Python's BinaryOperation.get() exactly — resolves via BinaryRequest
// with multi-identifier resolution.
func (o *BinaryOperation) Get(ctx context.Context, input *inputs.BinaryInput) ([]*model.BinaryItem, error) {
	request := inputs.NewBinaryRequest(*input, o.repo)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return resolved.Binaries, nil
}

// SetDefault sets a binary as default.
// Matches Python's BinaryOperation.set_default() exactly — resolves via BinaryRequest,
// checks for ambiguous results, then delegates to BinaryController.
func (o *BinaryOperation) SetDefault(ctx context.Context, input *inputs.BinaryInput) *errs.OperationResult {
	request := inputs.NewBinaryRequest(*input, o.repo)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "binary.default_set_failed",
			Message:   fmt.Sprintf("Binary not found: %v", err),
			Exception: err,
		}
	}

	// Match Python's: if len(resolved.binaries) > 1: raise BinaryError("Ambiguous ID to set to default")
	if len(resolved.Binaries) > 1 {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "binary.default_set_failed",
			Message: "Ambiguous ID to set to default",
		}
	}

	bin := resolved.Binaries[0]

	// Use BinaryController for the default-setting operation, matching Python:
	// controller = BinaryController(entity=binary, repo=repo); controller.set_default()
	ctrl, err := binary.NewController(ctx, bin, o.repo)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "binary.default_set_failed",
			Message:   fmt.Sprintf("Failed to set default: %v", err),
			Exception: err,
		}
	}

	if err := ctrl.SetDefault(ctx); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "binary.default_set_failed",
			Message:   fmt.Sprintf("Failed to set default: %v", err),
			Exception: err,
		}
	}

	auditLog := logging.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("binary.set_default", map[string]interface{}{
		"id":      bin.ID,
		"name":    bin.Name,
		"version": bin.Version,
	}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "binary.default_set",
		Message: fmt.Sprintf("Default binary set to %s v%s", bin.Name, bin.Version),
		Item:    bin,
	}
}

// EnsureDefault ensures a default Firecracker binary exists.
// Matches Python's BinaryOperation.ensure_default() exactly — wraps the entire
// flow in try/except BinaryError and uses PEP 440 version sorting via
// packaging.version.Version (replicated as a PEP 440-compatible sort).
func (o *BinaryOperation) EnsureDefault(ctx context.Context) *errs.OperationResult {
	local, err := o.svc.ListAll(ctx, true, true)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "binary.ensure_default_failed",
			Message:   fmt.Sprintf("Failed to list binaries: %v", err),
			Exception: err,
		}
	}
	if len(local) == 0 {
		return &errs.OperationResult{
			Status:  "success",
			Code:    "binary.default_unchanged",
			Message: "No local binaries found",
		}
	}

	default_, _ := o.svc.GetDefaultFirecracker(ctx)
	if default_ != nil {
		return &errs.OperationResult{
			Status:  "skipped",
			Code:    "binary.default_unchanged",
			Message: "Default already set",
			Item:    default_,
		}
	}

	firecrackerBins := make([]*model.BinaryItem, 0)
	for _, b := range local {
		if b.Name == "firecracker" {
			firecrackerBins = append(firecrackerBins, b)
		}
	}
	if len(firecrackerBins) == 0 {
		return &errs.OperationResult{
			Status:  "success",
			Code:    "binary.default_unchanged",
			Message: "No firecracker binaries found",
		}
	}

	// PEP 440 version sorting: newest first (matching Python's:
	//   sorted(firecracker_bins, key=lambda b: Version(b.version), reverse=True)
	sort.Slice(firecrackerBins, func(i, j int) bool {
		return version.CompareVersions(firecrackerBins[i].Version, firecrackerBins[j].Version) > 0
	})
	latest := firecrackerBins[0]

	ctrl, err := binary.NewController(ctx, latest, o.repo)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "binary.ensure_default_failed",
			Message:   fmt.Sprintf("Failed to set default binary: %v", err),
			Exception: err,
		}
	}

	if err := ctrl.SetDefault(ctx); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "binary.ensure_default_failed",
			Message:   fmt.Sprintf("Failed to set default binary: %v", err),
			Exception: err,
		}
	}

	auditLog := logging.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("binary.ensure_default", map[string]interface{}{
		"id":      latest.ID,
		"name":    latest.Name,
		"version": latest.Version,
	}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "binary.default_repaired",
		Message: fmt.Sprintf("Default set to %s v%s", latest.Name, latest.Version),
		Item:    latest,
	}
}

// Compile-time check
