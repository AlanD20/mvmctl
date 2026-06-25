package api

import (
	"context"
	"fmt"
	"log/slog"
	"mvmctl/internal/core/binary"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
	"sort"
)

// BinaryAPI defines the public interface for binary operations.
type BinaryAPI interface {
	BinaryPrune(ctx context.Context, dryRun bool, force bool) ([]string, error)
	BinaryPull(
		ctx context.Context,
		input inputs.BinaryPullInput,
		onProgress event.OnProgressCallback,
	) ([]*model.BinaryItem, error)
	BinaryRemove(ctx context.Context, input inputs.BinaryInput, force bool) *errs.BatchResult
	BinaryRemoveByVersion(ctx context.Context, version string, force bool) error
	BinaryList(
		ctx context.Context,
		remote bool,
		limit *int,
		onProgress event.OnProgressCallback,
	) ([]*model.BinaryItem, []model.VersionInfo, error)
	BinaryGet(ctx context.Context, input inputs.BinaryInput) ([]*model.BinaryItem, error)
	BinarySetDefault(ctx context.Context, input inputs.BinaryInput) (*model.BinaryItem, error)
	BinaryEnsureDefault(ctx context.Context) (*model.BinaryItem, error)
}

// BinaryPrune prunes unused binaries.
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
					"type", bin.Type, "version", bin.Version, "error", removeResult.Errors()[0].Message)
				continue
			}
		}
		removed = append(removed, fmt.Sprintf("%s:%s", bin.Type, bin.Version))
	}
	return removed, nil
}

// BinaryPull downloads or builds a binary.
func (op *Operation) BinaryPull(ctx context.Context, input inputs.BinaryPullInput,
	onProgress event.OnProgressCallback) ([]*model.BinaryItem, error) {
	resolved, err := input.Resolve()
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryPullFailed, err.Error(), err)
	}
	// --- Git build path (parallel to release download) ---
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
				_ = op.Repos.Binary.SetDefault(ctx, b.Type, b.ID)
			}
		}
		versionStr := ""
		if len(binaries) > 0 {
			versionStr = binaries[0].Version
		}
		op.AuditLog.LogOperation("binary.pull", map[string]any{
			"git_ref": *resolved.GitRef,
			"version": versionStr,
		}, "")
		emitProgress(onProgress, "complete", "complete", "Firecracker built successfully")
		return binaries, nil
	}
	// --- Release download path ---
	resolvedVersion, err := op.Services.Binary.ResolveVersion(ctx, resolved.Type, resolved.Version)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryPullFailed, err.Error(), err)
	}
	normalized := binary.NormalizeVersion(resolvedVersion)
	fcExists, _ := op.Repos.Binary.GetByTypeAndVersion(ctx, "firecracker", normalized)
	jlExists, _ := op.Repos.Binary.GetByTypeAndVersion(ctx, "jailer", normalized)
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
			_ = op.Repos.Binary.SetDefault(ctx, b.Type, b.ID)
		}
	}
	op.AuditLog.LogOperation("binary.pull", map[string]any{"version": resolvedVersion}, "")
	emitProgress(onProgress, "complete", "complete", "Firecracker downloaded successfully")
	return binaries, nil
}

// BinaryRemove removes binaries by identifiers.
// Enriches with VM references. Each binary removal is wrapped in per-binary
// error handling.
func (op *Operation) BinaryRemove(ctx context.Context, input inputs.BinaryInput, force bool) *errs.BatchResult {
	binaries, err := input.Resolve(ctx, op.Repos.Binary)
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
	// Enrich binaries with VM and snapshot references.
	enriched := binaries
	op.Enr.EnrichBinary(ctx, enriched, "vm", "snapshots")
	items := make([]errs.OperationResult, 0)
	for _, bin := range enriched {
		// svc.Remove returns (*BinaryItem, error)
		if _, err := op.Services.Binary.Remove(ctx, bin, force); err != nil {
			items = append(items, errs.OperationResult{
				Status:    "error",
				Code:      "binary.remove_failed",
				Message:   fmt.Sprintf("Failed to remove %s v%s: %v", bin.Type, bin.FullVersion, err),
				Exception: err,
				Item:      bin,
			})
			continue
		}
		op.AuditLog.LogOperation("binary.remove", map[string]any{
			"id":      bin.ID,
			"type":    bin.Type,
			"version": bin.FullVersion,
		}, "")
		items = append(items, errs.OperationResult{
			Status:  "success",
			Code:    "binary.removed",
			Message: fmt.Sprintf("Removed %s v%s", bin.Type, bin.FullVersion),
			Item:    bin,
		})
	}
	return &errs.BatchResult{Items: items}
}

// BinaryRemoveByVersion removes both firecracker and jailer for a version.
// Errors propagate through the DomainError pipeline.
func (op *Operation) BinaryRemoveByVersion(ctx context.Context, version string, force bool) error {
	resolver := binary.NewResolver(op.Repos.Binary)
	normalized := binary.NormalizeVersion(version)
	binariesToRemove := make([]*model.BinaryItem, 0)
	for _, name := range []string{"firecracker", "jailer"} {
		bin, err := resolver.ByTypeVersion(ctx, name, normalized)
		if err != nil {
			slog.Debug("Binary not found in DB, skipping",
				"name", name, "version", normalized, "error", err)
			continue
		}
		binariesToRemove = append(binariesToRemove, bin)
	}
	if len(binariesToRemove) == 0 {
		return errs.New(errs.CodeBinaryNotFound, fmt.Sprintf("No binaries found for version %s", normalized))
	}
	// Batch-enrich with VM references.
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
		if _, err := op.Services.Binary.Remove(ctx, bin, force); err != nil {
			return errs.WrapMsg(errs.CodeBinaryRemoveFailed, fmt.Sprintf("Failed to remove %s: %v", bin.Type, err), err)
		}
		op.AuditLog.LogOperation("binary.remove", map[string]any{
			"id":      bin.ID,
			"type":    bin.Type,
			"version": normalized,
		}, "")
	}
	return nil
}

// BinaryList returns local binaries or remote versions.
// When remote=false, returns ([]*model.BinaryItem, nil, error).
// When remote=true, returns (nil, []string, error) with remote version strings.
// When limit is nil and remote=true, reads default from the config setting
// "defaults.binary.remote_version_limit".
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
func (op *Operation) BinaryGet(ctx context.Context, input inputs.BinaryInput) ([]*model.BinaryItem, error) {
	binaries, err := input.Resolve(ctx, op.Repos.Binary)
	if err != nil {
		return nil, err
	}
	return binaries, nil
}

// BinarySetDefault sets a binary as default.
// Checks for ambiguous results, then delegates to BinaryController.
func (op *Operation) BinarySetDefault(ctx context.Context, input inputs.BinaryInput) (*model.BinaryItem, error) {
	binaries, err := input.Resolve(ctx, op.Repos.Binary)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryDefaultSetFailed, fmt.Sprintf("Binary not found: %v", err), err)
	}
	// Reject ambiguous identifier matches
	if len(binaries) > 1 {
		return nil, errs.New(errs.CodeBinaryDefaultSetFailed, "Ambiguous ID to set to default")
	}
	bin := binaries[0]
	// Use BinaryController for the default-setting operation.
	ctrl := binary.NewController(bin, op.Repos.Binary)
	if err := ctrl.SetDefault(ctx); err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryDefaultSetFailed, fmt.Sprintf("Failed to set default: %v", err), err)
	}
	op.AuditLog.LogOperation("binary.set_default", map[string]any{
		"id":      bin.ID,
		"type":    bin.Type,
		"version": bin.Version,
	}, "")
	return bin, nil
}

// BinaryEnsureDefault ensures a default Firecracker binary exists.
// Uses PEP 440-compatible version sorting to pick the newest binary.
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
		if b.Type == "firecracker" {
			firecrackerBins = append(firecrackerBins, b)
		}
	}
	if len(firecrackerBins) == 0 {
		return nil, nil
	}
	// PEP 440 version sorting: newest first
	sort.Slice(firecrackerBins, func(i, j int) bool {
		return version.Compare(firecrackerBins[i].Version, firecrackerBins[j].Version) > 0
	})
	latest := firecrackerBins[0]
	ctrl := binary.NewController(latest, op.Repos.Binary)
	if err := ctrl.SetDefault(ctx); err != nil {
		return nil, errs.WrapMsg(
			errs.CodeBinaryEnsureDefaultFailed,
			fmt.Sprintf("Failed to set default binary: %v", err),
			err,
		)
	}
	op.AuditLog.LogOperation("binary.ensure_default", map[string]any{
		"id":      latest.ID,
		"type":    latest.Type,
		"version": latest.Version,
	}, "")
	return latest, nil
}
