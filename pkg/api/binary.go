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
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/version"
	"mvmctl/pkg/api/inputs"
)

// BinaryPrune prunes unused binaries.
// Matches Python's BinaryOperation.prune() exactly.
func (op *Operation) BinaryPrune(ctx context.Context, dryRun bool, force bool) *errs.OperationResult {
	allBinaries, err := op.Repos.Binary.ListAll(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeDatabaseError),
			Message:   fmt.Sprintf("Failed to list binaries: %v", err),
			Exception: err,
		}
	}

	defaultBinary, _ := op.Repos.Binary.GetDefault(ctx, "firecracker")
	var defaultVersion string
	if defaultBinary != nil {
		defaultVersion = defaultBinary.Version
	}

	var removed []string
	for _, bin := range allBinaries {
		if !force {
			if bin.Version == defaultVersion {
				continue
			}
		}

		if !dryRun {
			removeResult := op.BinaryRemove(ctx, &inputs.BinaryInput{Identifiers: []string{bin.ID}}, force)
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

// BinaryPull downloads or builds a binary.
// Matches Python's BinaryOperation.pull() exactly — uses BinaryPullRequest
// resolution pipeline and wraps all BinaryErrors in code="binary.pull_failed".
func (op *Operation) BinaryPull(ctx context.Context, input *inputs.BinaryPullInput) *errs.OperationResult {
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
		binaries, err := op.Services.Binary.BuildFromSource(ctx, *resolved.GitRef)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "binary.pull_failed",
				Message:   fmt.Sprintf("Build failed: %v", err),
				Exception: err,
			}
		}

		noDefault, _ := op.Repos.Binary.GetDefault(ctx, "firecracker")
		shouldSetDefault := resolved.SetDefault || noDefault == nil

		for _, b := range binaries {
			_ = op.Repos.Binary.Upsert(ctx, b)
			if shouldSetDefault {
				_ = op.Repos.Binary.SetDefault(ctx, b.Name, b.Version, b.Path)
			}
		}

		versionStr := ""
		if len(binaries) > 0 {
			versionStr = binaries[0].Version
		}

		auditLog := logging.NewAuditLog(op.CacheDir)
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
		remoteVersions, err := op.Services.Binary.ListRemote(ctx, 20)
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
	fcExists, _ := op.Repos.Binary.GetByNameAndVersion(ctx, "firecracker", normalized)
	jlExists, _ := op.Repos.Binary.GetByNameAndVersion(ctx, "jailer", normalized)
	versionExists := fcExists != nil && jlExists != nil

	if versionExists && !resolved.DownloadOverride {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "binary.pull_failed",
			Message: fmt.Sprintf("Firecracker v%s already exists. Use --force to re-download.", normalized),
		}
	}

	noDefault, _ := op.Repos.Binary.GetDefault(ctx, "firecracker")
	shouldSetDefault := resolved.SetDefault || noDefault == nil

	// arch maps the current architecture to Firecracker's naming convention.
	arch := system.RuntimeArch()

	// Python passes bin_dir=resolved.bin_dir to BinaryService.download_firecracker();
	// Go uses s.binDir from the Service struct (same value).
	binaries, err := op.Services.Binary.DownloadFirecracker(ctx, resolvedVersion, arch, nil)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "binary.pull_failed",
			Message:   fmt.Sprintf("Download failed: %v", err),
			Exception: err,
		}
	}

	for _, b := range binaries {
		_ = op.Repos.Binary.Upsert(ctx, b)
		if shouldSetDefault {
			_ = op.Repos.Binary.SetDefault(ctx, b.Name, b.Version, b.Path)
		}
	}

	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("binary.pull", map[string]interface{}{"version": resolvedVersion}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "binary.downloaded",
		Message: fmt.Sprintf("Downloaded Firecracker v%s", normalized),
		Item:    binaries,
	}
}

// BinaryRemove removes binaries by identifiers.
// Matches Python's BinaryOperation.remove() exactly — resolves via BinaryRequest
// then enriches with VM references. Each binary removal is wrapped in per-binary
// error handling (matching Python's try/except (BinaryError, BinaryNotFoundError)).
func (op *Operation) BinaryRemove(ctx context.Context, input *inputs.BinaryInput, force bool) *errs.BatchResult {
	request := inputs.NewBinaryRequest(*input, op.Repos.Binary)
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
	if op.Enr != nil {
		_ = op.Enr.EnrichBinary(ctx, enriched)
	}

	items := make([]errs.OperationResult, 0)

	for _, bin := range enriched {
		// Python: svc.remove(binary, force=force)
		// Go: svc.Remove returns (*BinaryItem, error)
		if _, err := op.Services.Binary.Remove(ctx, bin, force); err != nil {
			items = append(items, errs.OperationResult{
				Status:    "error",
				Code:      "binary.remove_failed",
				Message:   fmt.Sprintf("Failed to remove %s v%s: %v", bin.Name, bin.FullVersion, err),
				Exception: err,
				Item:      bin,
			})
			continue
		}

		auditLog := logging.NewAuditLog(op.CacheDir)
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

// BinaryRemoveByVersion removes both firecracker and jailer for a version.
// Matches Python's BinaryOperation.remove_by_version() exactly — wraps the
// entire flow in try/except (BinaryError, BinaryNotFoundError).
func (op *Operation) BinaryRemoveByVersion(ctx context.Context, version string, force bool) *errs.OperationResult {
	resolver := binary.NewResolver(op.Repos.Binary)

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
			vms, err := op.Repos.VM.FindByBinaryID(ctx, bin.ID)
			if err == nil && len(vms) > 0 {
				for _, vm := range vms {
					bin.VMs = append(bin.VMs, vm)
				}
			}
		}
	}

	for _, bin := range binariesToRemove {
		// Python: svc.remove(binary, force=force) — returns (*BinaryItem, error)
		if _, err := op.Services.Binary.Remove(ctx, bin, force); err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      "binary.remove_failed",
				Message:   fmt.Sprintf("Failed to remove %s: %v", bin.Name, err),
				Exception: err,
			}
		}
		auditLog := logging.NewAuditLog(op.CacheDir)
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

// BinaryList returns local binaries or remote versions.
// Matches Python's BinaryOperation.list_all(remote=bool, limit=int|None) exactly.
// When remote=false, returns ([]*model.BinaryItem, nil, error).
// When remote=true, returns (nil, []string, error) with remote version strings.
// When limit is nil and remote=true, reads default from settings like Python:
//
//	SettingsService.resolve(Database(), "defaults.binary", "remote_version_limit")
func (op *Operation) BinaryList(ctx context.Context, remote bool, limit *int) ([]*model.BinaryItem, []string, error) {
	if !remote {
		items, err := op.Services.Binary.ListAll(ctx, false, true)
		return items, nil, err
	}

	lmt := 0
	if limit != nil {
		lmt = *limit
	}
	if lmt <= 0 {
		if op.Services.Config != nil {
			rawLimit, _ := op.Services.Config.Get(ctx, "defaults.binary", "remote_version_limit")
			if rawLimit != nil {
				switch v := rawLimit.(type) {
				case int:
					lmt = v
				case float64:
					lmt = int(v)
				case string:
					lmt, _ = strconv.Atoi(v)
				}
			}
		}
		if lmt <= 0 {
			dflt, _ := infra.GetDefault("defaults.binary", "remote_version_limit")
			switch v := dflt.(type) {
			case int:
				lmt = v
			case float64:
				lmt = int(v)
			}
		}
		if lmt <= 0 {
			lmt = 20
		}
	}
	versions, err := op.Services.Binary.ListRemote(ctx, lmt)
	return nil, versions, err
}

// BinaryGet returns binaries by identifier.
// Matches Python's BinaryOperation.get() exactly — resolves via BinaryRequest
// with multi-identifier resolution.
func (op *Operation) BinaryGet(ctx context.Context, input *inputs.BinaryInput) ([]*model.BinaryItem, error) {
	request := inputs.NewBinaryRequest(*input, op.Repos.Binary)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return resolved.Binaries, nil
}

// BinarySetDefault sets a binary as default.
// Matches Python's BinaryOperation.set_default() exactly — resolves via BinaryRequest,
// checks for ambiguous results, then delegates to BinaryController.
func (op *Operation) BinarySetDefault(ctx context.Context, input *inputs.BinaryInput) *errs.OperationResult {
	request := inputs.NewBinaryRequest(*input, op.Repos.Binary)
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
	ctrl, err := binary.NewController(ctx, bin, op.Repos.Binary)
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

	auditLog := logging.NewAuditLog(op.CacheDir)
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

// BinaryEnsureDefault ensures a default Firecracker binary exists.
// Matches Python's BinaryOperation.ensure_default() exactly — wraps the entire
// flow in try/except BinaryError and uses PEP 440 version sorting via
// packaging.version.Version (replicated as a PEP 440-compatible sort).
func (op *Operation) BinaryEnsureDefault(ctx context.Context) *errs.OperationResult {
	local, err := op.Services.Binary.ListAll(ctx, true, true)
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

	default_, _ := op.Services.Binary.GetDefaultFirecracker(ctx)
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

	ctrl, err := binary.NewController(ctx, latest, op.Repos.Binary)
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

	auditLog := logging.NewAuditLog(op.CacheDir)
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
