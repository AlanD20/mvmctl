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

type ResolveResult struct {
	Items    []*model.KernelItem
	Errors   []string
	ExitCode int
}

type Resolver struct {
	repo Repository
}

// NewResolver creates a new Resolver.
func NewResolver(repo Repository) *Resolver {
	return &Resolver{
		repo: repo,
	}
}

// ByID resolves a kernel by ID prefix.
func (r *Resolver) ByID(ctx context.Context, kernelID string, includeDeleted ...bool) (*model.KernelItem, error) {
	matches, err := r.repo.FindByPrefix(ctx, kernelID, includeDeleted...)
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
	return matches[0], nil
}

// ByVersionType resolves by version and type (both required).
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
	return k, nil
}

// ByNameVersion resolves a kernel by name and version.
// The name is the kernel type (e.g., "official", "kvib") — delegates to ByVersionType.
// Matches binary resolver's ByNameVersion for uniform Selector handling.
func (r *Resolver) ByNameVersion(ctx context.Context, name, ver string) (*model.KernelItem, error) {
	return r.ByVersionType(ctx, ver, name)
}

// ByType resolves by kernel type name.
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
	return k, nil
}

// GetDefault resolves the default kernel, or nil if not set.
func (r *Resolver) GetDefault(ctx context.Context) (*model.KernelItem, error) {
	k, err := r.repo.GetDefault(ctx)
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, nil
	}
	return k, nil
}

// Resolve resolves a kernel by ID prefix, "[type:]version" selector, or file path.
func (r *Resolver) Resolve(ctx context.Context, value string, includeDeleted ...bool) (*model.KernelItem, error) {
	// Try "name:version" selector format first (matches binary resolver pattern)
	name, ver := version.ParseSelector(value)
	if name != "" && ver != "" {
		return r.ByNameVersion(ctx, name, ver)
	}

	// Fast-path: absolute path -> skip DB queries entirely.
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

	// Try by ID prefix
	k, err := r.ByID(ctx, value, includeDeleted...)
	if err == nil {
		return k, nil
	}
	if !errs.IsNotFound(err) {
		return nil, err
	}

	// Try by type
	k, err = r.repo.GetByType(ctx, value)
	if err == nil && k != nil {
		return k, nil
	}

	// Fallback: treat value as a filesystem path to a vmlinux binary.
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
func (r *Resolver) ResolveMany(ctx context.Context, identifiers []string, includeDeleted ...bool) *ResolveResult {
	// Dedup input identifiers (e.g. duplicate CLI args) before processing.
	uniqueIDs := infra.Dedup(identifiers)

	var items []*model.KernelItem
	var errors []string
	resolvedIDs := make(map[string]bool)

	for _, identifier := range uniqueIDs {
		item, err := r.Resolve(ctx, identifier, includeDeleted...)
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
func (r *Resolver) ItemFromPath(path string) *model.KernelItem {
	// Expand ~ AND resolve symlinks
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
