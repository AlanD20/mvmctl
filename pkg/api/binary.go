package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/internal/service/jailer"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// BinaryAPI defines the public interface for binary operations.
type BinaryAPI interface {
	BinaryPrune(ctx context.Context, dryRun bool, force bool) ([]string, error)
	BinaryPull(
		ctx context.Context,
		input inputs.BinaryPullInput,
		onProgress event.OnProgressCallback,
	) ([]*model.BinaryItem, error)
	BinaryRemove(ctx context.Context, input inputs.BinaryRemoveInput, force bool) *errs.BatchResult
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
	architecture := system.RuntimeArch()
	allBinaries, err := op.Repos.Binary.ListAll(ctx, architecture)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeDatabaseError, fmt.Sprintf("Failed to list binaries: %v", err), err)
	}
	defaultBinary, err := op.Repos.Binary.GetDefault(ctx, "firecracker", architecture)
	if err != nil {
		return nil, err
	}
	var defaultVersion string
	if defaultBinary != nil {
		defaultVersion = defaultBinary.Version
	}
	versions := make([]string, 0, len(allBinaries)/2)
	seen := make(map[string]struct{}, len(allBinaries)/2)
	for _, bin := range allBinaries {
		if bin.Version == defaultVersion {
			continue
		}
		if _, duplicate := seen[bin.Version]; duplicate {
			continue
		}
		seen[bin.Version] = struct{}{}
		versions = append(versions, bin.Version)
	}
	var removed []string
	for _, releaseVersion := range versions {
		if !dryRun {
			removeResult := op.BinaryRemove(
				ctx,
				inputs.BinaryRemoveInput{Versions: []string{releaseVersion}},
				force,
			)
			if removeResult.HasErrors() {
				failure := removeResult.Errors()[0]
				if failure.Exception != nil {
					return removed, failure.Exception
				}
				return removed, failure.ToError()
			}
		}
		removed = append(removed, releaseVersion)
	}
	return removed, nil
}

// BinaryPull installs an exact release through the root-owned trusted-release authority.
func (op *Operation) BinaryPull(ctx context.Context, input inputs.BinaryPullInput,
	onProgress event.OnProgressCallback) ([]*model.BinaryItem, error) {
	architecture := system.RuntimeArch()
	resolved, err := input.Resolve()
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryPullFailed, err.Error(), err)
	}
	resolvedVersion, err := op.Services.Binary.ResolveVersion(ctx, resolved.Type, resolved.Version)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeBinaryPullFailed, err.Error(), err)
	}
	normalized := binary.NormalizeVersion(resolvedVersion)
	payload, payloadLength, err := openTrustedReleaseMirrorArchive(normalized, architecture)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		defer func() { _ = payload.Close() }()
		slog.Info(
			"Using local mirror for trusted release",
			"version", normalized,
			"architecture", architecture,
			"bytes", payloadLength,
		)
		emitProgress(onProgress, "mirror", "complete", "Using exact local asset mirror archive")
	}
	if op.trustedReleaseClient == nil {
		return nil, errs.New(errs.CodeInternal, "trusted release client is not configured")
	}
	emitProgress(onProgress, "install", "running", "Installing trusted Firecracker release...")
	rootResult, err := op.trustedReleaseClient.Install(ctx, jailer.TrustedReleaseInstallRequest{
		Slot:             jailer.ReleaseSlot{Version: normalized, Architecture: architecture},
		AllowReplacement: resolved.DownloadOverride,
	}, payload, payloadLength)
	if err != nil {
		return nil, err
	}
	pair := binaryPairFromTrustedRelease(rootResult)
	defaultBinary, err := op.Repos.Binary.GetDefault(ctx, "firecracker", architecture)
	if err != nil {
		return []*model.BinaryItem{pair.Firecracker, pair.Jailer}, releaseProjectionFailure(
			"trusted release install",
			rootResult.Outcome,
			err,
		)
	}
	setDefault := resolved.SetDefault || defaultBinary == nil || defaultBinary.Version == normalized
	pair.Firecracker.IsDefault = setDefault
	pair.Jailer.IsDefault = setDefault
	if err := op.Repos.Binary.ReplaceReleaseProjection(ctx, architecture, normalized, pair, setDefault); err != nil {
		return []*model.BinaryItem{pair.Firecracker, pair.Jailer}, releaseProjectionFailure(
			"trusted release install",
			rootResult.Outcome,
			err,
		)
	}
	if op.AuditLog != nil {
		_ = op.AuditLog.LogOperation("binary.pull", map[string]any{
			"version": normalized,
			"outcome": rootResult.Outcome,
		}, "")
	}
	emitProgress(onProgress, "complete", "complete", "Trusted Firecracker release installed")
	return []*model.BinaryItem{pair.Firecracker, pair.Jailer}, nil
}

// BinaryRemove removes exact release slots through the root-owned trusted-release authority.
func (op *Operation) BinaryRemove(ctx context.Context, input inputs.BinaryRemoveInput, force bool) *errs.BatchResult {
	architecture := system.RuntimeArch()
	versions, err := input.Resolve()
	if err != nil {
		return &errs.BatchResult{Items: []errs.OperationResult{binaryRemoveError("", err)}}
	}
	items := make([]errs.OperationResult, 0, len(versions))
	for _, releaseVersion := range versions {
		var warnings []string
		if !force {
			warnings = op.binaryReleaseAdvisoryWarnings(ctx, architecture, releaseVersion)
		}
		if op.trustedReleaseClient == nil {
			items = append(items, binaryRemoveError(
				releaseVersion,
				errs.New(errs.CodeInternal, "trusted release client is not configured"),
			))
			continue
		}
		rootResult, err := op.trustedReleaseClient.Remove(ctx, jailer.TrustedReleaseRemoveRequest{
			Slot: jailer.ReleaseSlot{Version: releaseVersion, Architecture: architecture},
		})
		if err != nil {
			item := binaryRemoveError(releaseVersion, err)
			item.Warnings = warnings
			items = append(items, item)
			continue
		}
		if err := op.Repos.Binary.RemoveReleaseProjection(ctx, architecture, releaseVersion); err != nil {
			projectionErr := releaseProjectionFailure("trusted release removal", rootResult.Removed, err)
			item := binaryRemoveError(releaseVersion, projectionErr)
			item.Warnings = warnings
			items = append(items, item)
			continue
		}
		if op.AuditLog != nil {
			_ = op.AuditLog.LogOperation("binary.remove", map[string]any{
				"version": releaseVersion,
				"removed": rootResult.Removed,
			}, "")
		}
		status, code, message := "success", "binary.removed", fmt.Sprintf("Removed release v%s", releaseVersion)
		if !rootResult.Removed {
			status, code, message = "skipped", "binary.remove_absent", fmt.Sprintf(
				"Release v%s was already absent",
				releaseVersion,
			)
		}
		items = append(items, errs.OperationResult{
			Status: status, Code: code, Message: message, Item: releaseVersion, Warnings: warnings,
		})
	}
	return &errs.BatchResult{Items: items}
}

func binaryRemoveError(version string, err error) errs.OperationResult {
	code := errs.CodeBinaryRemoveFailed
	var domainErr *errs.DomainError
	if errors.As(err, &domainErr) {
		code = domainErr.Code
	}
	return errs.OperationResult{
		Status: "error", Code: string(code), Message: err.Error(), Item: version, Exception: err,
	}
}

func (op *Operation) binaryReleaseAdvisoryWarnings(
	ctx context.Context,
	architecture string,
	version string,
) []string {
	pair := make([]*model.BinaryItem, 0, 2)
	for _, typ := range []string{"firecracker", "jailer"} {
		member, err := op.Repos.Binary.GetByTypeAndVersion(ctx, typ, architecture, version)
		if err == nil && member != nil {
			pair = append(pair, member)
		}
	}
	if len(pair) == 0 {
		return nil
	}
	if op.Enr != nil {
		_ = op.Enr.EnrichBinary(ctx, pair, "vm", "snapshots")
	}
	for _, member := range pair {
		if len(member.VMs) > 0 || len(member.Snapshots) > 0 {
			return []string{fmt.Sprintf(
				"local state references release v%s; root authority will make the final removal decision",
				version,
			)}
		}
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
		items, err := op.Services.Binary.ListAll(ctx)
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
	local, _ := op.Repos.Binary.ListAll(ctx, system.RuntimeArch())
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
	binaries, err := input.Resolve(ctx, op.Repos.Binary, system.RuntimeArch())
	if err != nil {
		return nil, err
	}
	return binaries, nil
}

// BinarySetDefault sets a binary as default.
// Checks for ambiguous results, then delegates to BinaryController.
func (op *Operation) BinarySetDefault(ctx context.Context, input inputs.BinaryInput) (*model.BinaryItem, error) {
	binaries, err := input.Resolve(ctx, op.Repos.Binary, system.RuntimeArch())
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
	local, err := op.Services.Binary.ListAll(ctx)
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
