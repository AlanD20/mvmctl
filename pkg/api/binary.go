package api

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// BinaryPrune prunes unused binaries.
// Matches Python's BinaryOperation.prune() exactly.
func (op *Operation) BinaryPrune(ctx context.Context, dryRun bool, force bool) ([]string, error) {
	allBinaries, err := op.Repos.Binary.ListAll(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, fmt.Sprintf("Failed to list binaries: %v", err), err)
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
			removeResult := op.BinaryRemove(ctx, inputs.BinaryInput{Identifiers: []string{bin.ID}}, force)
			if removeResult.HasErrors() {
				slog.Warn("Failed to remove binary",
					"name", bin.Name, "version", bin.Version, "error", removeResult.Errors()[0].Message)
				continue
			}
		}
		removed = append(removed, fmt.Sprintf("%s:%s", bin.Name, bin.Version))
	}

	return removed, nil
}

// BinaryPull downloads or builds a binary.
// Matches Python's BinaryOperation.pull() exactly — uses BinaryPullRequest
// resolution pipeline and wraps all BinaryErrors in code="binary.pull_failed".
func (op *Operation) BinaryPull(ctx context.Context, input inputs.BinaryPullInput,
	onProgress event.OnProgressCallback) ([]*model.BinaryItem, error) {

	// Python: request = BinaryPullRequest(inputs=inputs, db=db); resolved = request.resolve()
	request := inputs.NewBinaryPullRequest(input, op.Connection.DB())
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryPullFailed, err.Error(), err)
	}

	// ---- Git build path (parallel to release download) ----
	if resolved.GitRef != nil && *resolved.GitRef != "" {
		emitProgress(onProgress, "build", "running", "Building Firecracker from source...")

		binaries, err := op.Services.Binary.BuildFromSource(ctx, *resolved.GitRef)
		if err != nil {
			return nil, errs.WrapMsg(errs.CodeBinaryPullFailed, fmt.Sprintf("Build failed: %v", err), err)
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

		op.AuditLog.LogOperation("binary.pull", map[string]interface{}{
			"git_ref": *resolved.GitRef,
			"version": versionStr,
		}, "")

		emitProgress(onProgress, "complete", "complete", "Firecracker built successfully")

		return binaries, nil
	}

	// ---- Release download path ----
	resolvedVersion, err := op.Services.Binary.ResolveVersion(ctx, resolved.Name, resolved.Version)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryPullFailed, err.Error(), err)
	}

	normalized := binary.NormalizeVersion(resolvedVersion)
	fcExists, _ := op.Repos.Binary.GetByNameAndVersion(ctx, "firecracker", normalized)
	jlExists, _ := op.Repos.Binary.GetByNameAndVersion(ctx, "jailer", normalized)
	versionExists := fcExists != nil && jlExists != nil

	if versionExists && !resolved.DownloadOverride {
		return nil, errs.AlreadyExists(
			errs.CodeBinaryAlreadyExists,
			fmt.Sprintf("Firecracker v%s already exists. Use --force to re-download.", normalized),
		)
	}

	noDefault, _ := op.Repos.Binary.GetDefault(ctx, "firecracker")
	shouldSetDefault := resolved.SetDefault || noDefault == nil

	// arch maps the current architecture to Firecracker's naming convention.
	arch := system.RuntimeArch()

	// Bridge byte-level download progress to phase-level ProgressEvent
	emitProgress(onProgress, "download", "running", "Downloading Firecracker...")
	progressBridge := event.FormatProgress(onProgress)

	binaries, err := op.Services.Binary.DownloadFirecracker(ctx, resolvedVersion, arch, progressBridge)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryPullFailed, fmt.Sprintf("Download failed: %v", err), err)
	}

	for _, b := range binaries {
		_ = op.Repos.Binary.Upsert(ctx, b)
		if shouldSetDefault {
			_ = op.Repos.Binary.SetDefault(ctx, b.Name, b.Version, b.Path)
		}
	}

	op.AuditLog.LogOperation("binary.pull", map[string]interface{}{"version": resolvedVersion}, "")

	emitProgress(onProgress, "complete", "complete", "Firecracker downloaded successfully")

	return binaries, nil
}

// BinaryRemove removes binaries by identifiers.
// Matches Python's BinaryOperation.remove() exactly — resolves via BinaryRequest
// then enriches with VM references. Each binary removal is wrapped in per-binary
// error handling (matching Python's try/except (BinaryError, BinaryNotFoundError)).
func (op *Operation) BinaryRemove(ctx context.Context, input inputs.BinaryInput, force bool) *errs.BatchResult {
	request := inputs.NewBinaryRequest(input, op.Repos.Binary)
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
	op.Enr.EnrichBinary(ctx, enriched, "vm")

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

		op.AuditLog.LogOperation("binary.remove", map[string]interface{}{
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
func (op *Operation) BinaryRemoveByVersion(ctx context.Context, version string, force bool) error {
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
		return errs.New(errs.CodeBinaryNotFound, fmt.Sprintf("No binaries found for version %s", normalized))
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
			return errs.WrapMsg(errs.CodeBinaryRemoveFailed, fmt.Sprintf("Failed to remove %s: %v", bin.Name, err), err)
		}

		op.AuditLog.LogOperation("binary.remove", map[string]interface{}{
			"id":      bin.ID,
			"name":    bin.Name,
			"version": normalized,
		}, "")
	}

	return nil
}

// BinaryList returns local binaries or remote versions.
// Matches Python's BinaryOperation.list_all(remote=bool, limit=int|None) exactly.
// When remote=false, returns ([]*model.BinaryItem, nil, error).
// When remote=true, returns (nil, []string, error) with remote version strings.
// When limit is nil and remote=true, reads default from settings like Python:
//
//	SettingsService.resolve(Database(), "defaults.binary", "remote_version_limit")
func (op *Operation) BinaryList(
	ctx context.Context,
	remote bool,
	limit *int,
	onProgress event.OnProgressCallback,
) ([]*model.BinaryItem, []model.VersionInfo, error) {
	if !remote {
		items, err := op.Services.Binary.ListAll(ctx, false, true)
		return items, nil, err
	}

	emitProgress(onProgress, "listing", "running", "Fetching remote versions...")

	lmt := 0
	if limit != nil {
		lmt = *limit
	}
	if lmt <= 0 {
		lmt, _ = op.Services.Config.GetInt(ctx, "defaults.binary", "remote_version_limit")
	}
	versions, err := op.Services.Binary.ListRemote(ctx, lmt)
	if err != nil {
		return nil, nil, err
	}
	// Mark locally cached versions
	local, _ := op.Repos.Binary.ListAll(ctx)
	localSet := make(map[string]bool, len(local))
	for _, l := range local {
		localSet[l.Version] = true
	}
	for i := range versions {
		if localSet[versions[i].Version] {
			versions[i].IsPresent = true
		}
	}
	emitProgress(onProgress, "listing", "complete", fmt.Sprintf("Found %d remote version(s)", len(versions)))
	return nil, versions, nil
}

// BinaryGet returns binaries by identifier.
// Matches Python's BinaryOperation.get() exactly — resolves via BinaryRequest
// with multi-identifier resolution.
func (op *Operation) BinaryGet(ctx context.Context, input inputs.BinaryInput) ([]*model.BinaryItem, error) {
	request := inputs.NewBinaryRequest(input, op.Repos.Binary)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return resolved.Binaries, nil
}

// BinarySetDefault sets a binary as default.
// Matches Python's BinaryOperation.set_default() exactly — resolves via BinaryRequest,
// checks for ambiguous results, then delegates to BinaryController.
func (op *Operation) BinarySetDefault(ctx context.Context, input inputs.BinaryInput) (*model.BinaryItem, error) {
	request := inputs.NewBinaryRequest(input, op.Repos.Binary)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryDefaultSetFailed, fmt.Sprintf("Binary not found: %v", err), err)
	}

	// Match Python's: if len(resolved.binaries) > 1: raise BinaryError("Ambiguous ID to set to default")
	if len(resolved.Binaries) > 1 {
		return nil, errs.New(errs.CodeBinaryDefaultSetFailed, "Ambiguous ID to set to default")
	}

	bin := resolved.Binaries[0]

	// Use BinaryController for the default-setting operation, matching Python:
	// controller = BinaryController(entity=binary, repo=repo); controller.set_default()
	ctrl, err := binary.NewController(ctx, bin, op.Repos.Binary)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryDefaultSetFailed, fmt.Sprintf("Failed to set default: %v", err), err)
	}

	if err := ctrl.SetDefault(ctx); err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryDefaultSetFailed, fmt.Sprintf("Failed to set default: %v", err), err)
	}

	op.AuditLog.LogOperation("binary.set_default", map[string]interface{}{
		"id":      bin.ID,
		"name":    bin.Name,
		"version": bin.Version,
	}, "")

	return bin, nil
}

// BinaryEnsureDefault ensures a default Firecracker binary exists.
// Matches Python's BinaryOperation.ensure_default() exactly — wraps the entire
// flow in try/except BinaryError and uses PEP 440 version sorting via
// packaging.version.Version (replicated as a PEP 440-compatible sort).
func (op *Operation) BinaryEnsureDefault(ctx context.Context) (*model.BinaryItem, error) {
	local, err := op.Services.Binary.ListAll(ctx, true, true)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeBinaryEnsureDefaultFailed,
			fmt.Sprintf("Failed to list binaries: %v", err),
			err,
		)
	}
	if len(local) == 0 {
		return nil, nil
	}

	default_, _ := op.Services.Binary.GetDefaultFirecracker(ctx)
	if default_ != nil {
		return default_, nil
	}

	firecrackerBins := make([]*model.BinaryItem, 0)
	for _, b := range local {
		if b.Name == "firecracker" {
			firecrackerBins = append(firecrackerBins, b)
		}
	}
	if len(firecrackerBins) == 0 {
		return nil, nil
	}

	// PEP 440 version sorting: newest first (matching Python's:
	//   sorted(firecracker_bins, key=lambda b: Version(b.version), reverse=True)
	sort.Slice(firecrackerBins, func(i, j int) bool {
		return version.CompareVersions(firecrackerBins[i].Version, firecrackerBins[j].Version) > 0
	})
	latest := firecrackerBins[0]

	ctrl, err := binary.NewController(ctx, latest, op.Repos.Binary)
	if err != nil {
		return nil, errs.WrapMsg(
			errs.CodeBinaryEnsureDefaultFailed,
			fmt.Sprintf("Failed to set default binary: %v", err),
			err,
		)
	}

	if err := ctrl.SetDefault(ctx); err != nil {
		return nil, errs.WrapMsg(
			errs.CodeBinaryEnsureDefaultFailed,
			fmt.Sprintf("Failed to set default binary: %v", err),
			err,
		)
	}

	op.AuditLog.LogOperation("binary.ensure_default", map[string]interface{}{
		"id":      latest.ID,
		"name":    latest.Name,
		"version": latest.Version,
	}, "")

	return latest, nil
}

// Compile-time check
