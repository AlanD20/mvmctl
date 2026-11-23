package image

import (
	"context"
	"fmt"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
)

// ResolveResult holds the result of a batch image resolution.
type ResolveResult struct {
	Items    []*model.ImageItem
	Errors   []string
	ExitCode int
}

// Resolver resolves images by ID, type:version, type, name, or ID prefix.
type Resolver struct {
	repo Repository
}

// NewResolver creates a new Resolver.
func NewResolver(repo Repository) *Resolver {
	return &Resolver{repo: repo}
}

// ByID resolves by full ID.
func (r *Resolver) ByID(ctx context.Context, imageID string, includeDeleted ...bool) (*model.ImageItem, error) {
	matches, err := r.repo.FindByPrefix(ctx, imageID, includeDeleted...)
	if err != nil {
		return nil, fmt.Errorf("resolve image by ID: %w", err)
	}
	if len(matches) == 0 {
		return nil, errs.NotFound(errs.CodeImageNotFound, fmt.Sprintf("Image not found: '%s'", imageID))
	}
	if len(matches) > 1 {
		return nil, errs.NotFound(errs.CodeImageNotFound, fmt.Sprintf("Image ID is ambiguous: '%s'", imageID))
	}
	return matches[0], nil
}

// ByVersionType resolves by version and type (both required).
func (r *Resolver) ByVersionType(ctx context.Context, version, imgType string) (*model.ImageItem, error) {
	dbImage, err := r.repo.GetByVersionAndType(ctx, version, imgType)
	if err != nil {
		return nil, err
	}
	if dbImage == nil {
		return nil, errs.NotFound(
			errs.CodeImageNotFound,
			fmt.Sprintf("Image not found: version='%s', type='%s'", version, imgType),
		)
	}
	return dbImage, nil
}

// ByType resolves by image type.
func (r *Resolver) ByType(ctx context.Context, imgType string) (*model.ImageItem, error) {
	dbImage, err := r.repo.GetByType(ctx, imgType)
	if err != nil {
		return nil, err
	}
	if dbImage == nil {
		return nil, errs.NotFound(errs.CodeImageNotFound, fmt.Sprintf("Image not found: '%s'", imgType))
	}
	return dbImage, nil
}

// GetDefault resolves the default image, or nil if not set.
func (r *Resolver) GetDefault(ctx context.Context) (*model.ImageItem, error) {
	image, err := r.repo.GetDefault(ctx)
	if err != nil {
		return nil, err
	}
	if image == nil {
		return nil, nil
	}
	return image, nil
}

// ByName resolves by display name.
func (r *Resolver) ByName(ctx context.Context, name string, includeDeleted ...bool) (*model.ImageItem, error) {
	dbImage, err := r.repo.GetByName(ctx, name, includeDeleted...)
	if err != nil {
		return nil, err
	}
	if dbImage == nil {
		return nil, errs.NotFound(errs.CodeImageNotFound, fmt.Sprintf("Image not found by name: '%s'", name))
	}
	return dbImage, nil
}

// isImageNotFoundError checks if the error is an ImageNotFoundError
// using direct type assertion — no unwrapping.
func isImageNotFoundError(err error) bool {
	de, ok := err.(*errs.DomainError)
	if !ok {
		return false
	}
	return de.Code == errs.CodeImageNotFound
}

// Resolve resolves image by type:version, type, display name, or ID prefix.
// Only ImageNotFoundError causes fallthrough to the next resolution method
// — all other errors propagate immediately.
func (r *Resolver) Resolve(ctx context.Context, value string, includeDeleted ...bool) (*model.ImageItem, error) {
	// Try "type:version" selector format first using the shared version parser.
	name, ver := version.ParseSelector(value)
	if name != "" && ver != "" {
		image, err := r.ByVersionType(ctx, ver, name)
		if err == nil {
			return image, nil
		}
		if !isImageNotFoundError(err) {
			return nil, err
		}
		// Fall through to type-only lookup with the type part
		value = name
	}

	image, err := r.ByType(ctx, value)
	if err == nil {
		return image, nil
	}
	if !isImageNotFoundError(err) {
		return nil, err
	}

	image, err = r.ByName(ctx, value, includeDeleted...)
	if err == nil {
		return image, nil
	}
	if !isImageNotFoundError(err) {
		return nil, err
	}

	return r.ByID(ctx, value, includeDeleted...)
}

// ResolveMany resolves multiple image identifiers.
func (r *Resolver) ResolveMany(ctx context.Context, identifiers []string, includeDeleted ...bool) *ResolveResult {
	// Dedup input identifiers (e.g. duplicate CLI args) before processing.
	uniqueIDs := infra.Dedup(identifiers)

	var items []*model.ImageItem
	var errorsList []string
	resolvedIDs := make(map[string]bool)

	for _, identifier := range uniqueIDs {
		item, err := r.Resolve(ctx, identifier, includeDeleted...)
		if err != nil {
			errorsList = append(errorsList, err.Error())
			continue
		}
		if !resolvedIDs[item.ID] {
			// Dedup by DB record ID — different input identifiers may
			// resolve to the same DB record (e.g. "image" and
			// "image:ubuntu" when only ubuntu exists).
			resolvedIDs[item.ID] = true
			items = append(items, item)
		}
	}

	exitCode := 0
	if len(errorsList) > 0 && len(items) == 0 {
		exitCode = 1
	}

	return &ResolveResult{
		Items:    items,
		Errors:   errorsList,
		ExitCode: exitCode,
	}
}
