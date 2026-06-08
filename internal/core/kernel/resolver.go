package kernel

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
)

// RELATIONS defines the cross-domain relations for kernel enrichment.
// Matches Python's Resolver.RELATIONS.
var RELATIONS = map[string]model.RelationSpec{
	"vm": {
		FKField:      "id",
		Resolver:     "vm",
		Method:       "find_by_kernel_id",
		RelationName: "vms",
		IsReverse:    true,
		BatchMethod:  "by_kernel_id_batch",
	},
}

// ResolveResult matches Python's ResolveResult dataclass.
type ResolveResult struct {
	Items    []*model.KernelItem
	Errors   []string
	ExitCode int
}

// EnrichFunc is a function that enriches kernels in-place with relations.
// Set by the API layer during wiring to avoid circular imports.
type EnrichFunc func(ctx context.Context, kernels []*model.KernelItem, include []string, relations map[string]model.RelationSpec)

// Resolver matches Python's Resolver with all resolution methods.
type Resolver struct {
	repo       Repository
	include    []string
	enrichFunc EnrichFunc
}

// NewResolver creates a new Resolver.
// Matches Python's Resolver.__init__().
func NewResolver(repo Repository, include []string) *Resolver {
	return &Resolver{
		repo:    repo,
		include: include,
	}
}

// SetEnrichFunc sets the enrichment function called after each resolution.
// Must be set for cross-domain relation enrichment (e.g., populating VMs).
func (r *Resolver) SetEnrichFunc(fn EnrichFunc) {
	r.enrichFunc = fn
}

// SetInclude sets the relation names to include during resolution.
func (r *Resolver) SetInclude(include []string) {
	r.include = include
}

// Enrich enriches kernels with relations if include is set.
// Matches Python's Resolver.enrich().
func (r *Resolver) Enrich(ctx context.Context, kernels []*model.KernelItem) []*model.KernelItem {
	if r.include != nil && len(kernels) > 0 && r.enrichFunc != nil {
		r.enrichFunc(ctx, kernels, r.include, RELATIONS)
	}
	return kernels
}

// ByID resolves a kernel by ID prefix.
// Matches Python's Resolver.by_id().
func (r *Resolver) ByID(ctx context.Context, kernelID string) (*model.KernelItem, error) {
	matches, err := r.repo.FindByPrefix(ctx, kernelID)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errs.NotFound(
			errs.CodeKernelNotFound,
			"",
			errs.WithEntity(fmt.Sprintf("Kernel not found: '%s'", kernelID)),
		)
	}
	if len(matches) > 1 {
		return nil, errs.NotFound(
			errs.CodeKernelNotFound,
			"",
			errs.WithEntity(fmt.Sprintf("Kernel ID is ambiguous: '%s'", kernelID)),
		)
	}
	enriched := r.Enrich(ctx, matches)
	return enriched[0], nil
}

// ByVersionType resolves by version and type (both required).
// Matches Python's Resolver.by_version_type().
func (r *Resolver) ByVersionType(ctx context.Context, version, kernelType string) (*model.KernelItem, error) {
	k, err := r.repo.GetByVersionAndType(ctx, version, kernelType)
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, errs.NotFound(
			errs.CodeKernelNotFound,
			"",
			errs.WithEntity(fmt.Sprintf("Kernel not found: version='%s', type='%s'", version, kernelType)),
		)
	}
	enriched := r.Enrich(ctx, []*model.KernelItem{k})
	return enriched[0], nil
}

// ByNameVersion resolves a kernel by name and version.
// The name is the kernel type (e.g., "official", "kvib") — delegates to ByVersionType.
// Matches binary resolver's ByNameVersion for uniform Selector handling.
func (r *Resolver) ByNameVersion(ctx context.Context, name, ver string) (*model.KernelItem, error) {
	return r.ByVersionType(ctx, ver, name)
}

// ByType resolves by kernel type name.
// Matches Python's Resolver.by_type().
func (r *Resolver) ByType(ctx context.Context, typeStr string) (*model.KernelItem, error) {
	k, err := r.repo.GetByType(ctx, typeStr)
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, errs.NotFound(
			errs.CodeKernelNotFound,
			"",
			errs.WithEntity(fmt.Sprintf("Kernel not found: type='%s'", typeStr)),
		)
	}
	enriched := r.Enrich(ctx, []*model.KernelItem{k})
	return enriched[0], nil
}

// GetDefault resolves the default kernel, or nil if not set.
// Matches Python's Resolver.get_default().
func (r *Resolver) GetDefault(ctx context.Context) (*model.KernelItem, error) {
	k, err := r.repo.GetDefault(ctx)
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, nil
	}
	enriched := r.Enrich(ctx, []*model.KernelItem{k})
	return enriched[0], nil
}

// Resolve resolves a kernel by ID prefix, "[type:]version" selector, or file path.
// Matches Python's Resolver.resolve().
func (r *Resolver) Resolve(ctx context.Context, value string) (*model.KernelItem, error) {
	// Try "name:version" selector format first (matches binary resolver pattern)
	name, ver := version.ParseSelector(value)
	if name != "" && ver != "" {
		return r.ByNameVersion(ctx, name, ver)
	}

	// Fast-path: absolute path -> skip DB queries entirely.
	// Python: path = Path(value).expanduser() — expand ~ before checking existence.
	if strings.HasPrefix(value, "/") {
		path := system.ExpandTilde(value)
		if _, err := os.Stat(path); err == nil {
			return r.ItemFromPath(path), nil
		}
		return nil, errs.NotFound(
			errs.CodeKernelNotFound,
			"",
			errs.WithEntity(fmt.Sprintf("Kernel not found at path: '%s'", value)),
		)
	}

	// Try by ID prefix without enrichment (matching Python's resolve flow)
	k, err := r.byIDRaw(ctx, value)
	if err == nil && k != nil {
		return r.Enrich(ctx, []*model.KernelItem{k})[0], nil
	}

	// Try by type
	k, err = r.repo.GetByType(ctx, value)
	if err == nil && k != nil {
		return r.Enrich(ctx, []*model.KernelItem{k})[0], nil
	}

	// Fallback: treat value as a filesystem path to a vmlinux binary.
	// Python: path = Path(value); if path.exists() — does NOT expand ~ here.
	if _, err := os.Stat(value); err == nil {
		return r.ItemFromPath(value), nil
	}

	return nil, errs.NotFound(
		errs.CodeKernelNotFound,
		"",
		errs.WithEntity(fmt.Sprintf("Kernel not found: '%s'", value)),
	)
}

// ResolveMany resolves multiple kernel identifiers.
// Matches Python's Resolver.resolve_many().
func (r *Resolver) ResolveMany(ctx context.Context, identifiers []string) *ResolveResult {
	// Dedup input identifiers (e.g. duplicate CLI args) before processing.
	uniqueIDs := infra.Dedup(identifiers)

	var items []*model.KernelItem
	var errors []string
	resolvedIDs := make(map[string]bool)

	for _, identifier := range uniqueIDs {
		item, err := r.Resolve(ctx, identifier)
		if err != nil {
			errors = append(errors, err.Error())
		} else if !resolvedIDs[item.ID] {
			// Dedup by DB record ID — different input identifiers may
			// resolve to the same DB record (e.g. "kernel" and
			// "kernel:6.1" when only v6.1 exists).
			resolvedIDs[item.ID] = true
			items = append(items, item)
		}
	}

	items = r.Enrich(ctx, items)

	exitCode := 0
	if len(errors) > 0 && len(items) == 0 {
		exitCode = 1
	}
	return &ResolveResult{
		Items:    items,
		Errors:   errors,
		ExitCode: exitCode,
	}
}

// ItemFromPath constructs a KernelItem from an existing file path.
// Matches Python's Resolver._item_from_path().
// Python: pathlib.Path.expanduser().resolve() — expands ~ AND resolves symlinks.
func (r *Resolver) ItemFromPath(path string) *model.KernelItem {
	// Expand ~ AND resolve symlinks (matching Python's .expanduser().resolve())
	path = system.ExpandTilde(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if absPath, err := filepath.Abs(path); err == nil {
		path = absPath
	}
	name := filepath.Base(path)
	now := time.Now().Format(time.RFC3339)
	return &model.KernelItem{
		ID:        path,
		Name:      name,
		BaseName:  name,
		Version:   "unknown",
		Arch:      "unknown",
		Type:      "external",
		Path:      path,
		IsDefault: false,
		IsPresent: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// byIDRaw resolves by ID prefix without enrichment.
func (r *Resolver) byIDRaw(ctx context.Context, kernelID string) (*model.KernelItem, error) {
	matches, err := r.repo.FindByPrefix(ctx, kernelID)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) > 1 {
		return nil, errs.New(
			errs.CodeKernelNotFound,
			fmt.Sprintf("Kernel ID is ambiguous: '%s'", kernelID),
			errs.WithClass(errs.ClassInternal),
		)
	}
	return matches[0], nil
}
