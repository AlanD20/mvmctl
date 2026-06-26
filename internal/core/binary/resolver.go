package binary

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/version"
	"mvmctl/pkg/errs"
)

// Enricher resolves VM relations for BinaryItems.
// Implemented by internal/enricher to avoid circular imports.
type Enricher interface {
	EnrichBinary(ctx context.Context, binaries []*model.BinaryItem) error
}

// ResolveResult holds the result of resolving multiple binary identifiers.
type ResolveResult struct {
	Items    []*model.BinaryItem
	Errors   []string
	ExitCode int
}

// RELATIONS defines the cross-domain relations for binary enrichment.
var RELATIONS = map[string]model.RelationSpec{
	"vm": {
		FKField:      "id",
		Resolver:     "vm",
		Method:       "find_by_binary_id",
		RelationName: "vms",
		IsReverse:    true,
		BatchMethod:  "by_binary_id_batch",
	},
}

// EnrichFunc is a function that enriches binaries in-place with relations.
// Set by the API layer during wiring to avoid circular imports.
type EnrichFunc func(ctx context.Context, binaries []*model.BinaryItem, include []string, relations map[string]model.RelationSpec)

// Resolver resolves binary identifiers (ID prefix, name, [name, version] pair)
// to BinaryItem instances.
type Resolver struct {
	repo       Repository
	include    []string
	enrichFunc EnrichFunc
}

// NewResolver creates a new Resolver without enrichment.
func NewResolver(repo Repository) *Resolver {
	return &Resolver{repo: repo}
}

// NewBinaryResolverWithEnrichFunc creates a new Resolver with enrichment support.
func NewBinaryResolverWithEnrichFunc(repo Repository, enrichFunc EnrichFunc, include []string) *Resolver {
	return &Resolver{
		repo:       repo,
		enrichFunc: enrichFunc,
		include:    include,
	}
}

// WithInclude sets the relations to enrich.
func (r *Resolver) WithInclude(include []string) *Resolver {
	r.include = include
	return r
}

// enrich resolves VM relations on BinaryItems if an enricher is configured.
func (r *Resolver) enrich(ctx context.Context, binaries []*model.BinaryItem) []*model.BinaryItem {
	if r.enrichFunc != nil && len(r.include) > 0 && len(binaries) > 0 {
		r.enrichFunc(ctx, binaries, r.include, RELATIONS)
	}
	return binaries
}

// ByID resolves a binary by ID prefix.
func (r *Resolver) ByID(ctx context.Context, binaryID string) (*model.BinaryItem, error) {
	matches, err := r.repo.FindByPrefix(ctx, binaryID)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errs.NotFound(errs.CodeBinaryNotFound,
			fmt.Sprintf("Binary not found: %s", binaryID))
	}
	if len(matches) > 1 {
		return nil, errs.New(errs.CodeBinaryNotFound,
			fmt.Sprintf("Binary ID is ambiguous: %s", binaryID))
	}
	enriched := r.enrich(ctx, matches)
	return enriched[0], nil
}

// ByTypeVersion resolves a binary by type and version (both required).
func (r *Resolver) ByTypeVersion(ctx context.Context, typ, version string) (*model.BinaryItem, error) {
	binary, err := r.repo.GetByTypeAndVersion(ctx, typ, version)
	if err != nil {
		return nil, err
	}
	if binary == nil {
		return nil, errs.NotFound(errs.CodeBinaryNotFound,
			fmt.Sprintf("Binary not found: type='%s', version='%s'", typ, version))
	}
	enriched := r.enrich(ctx, []*model.BinaryItem{binary})
	return enriched[0], nil
}

// ByTypeLatest resolves a binary by type — returns the highest local version.
func (r *Resolver) ByTypeLatest(ctx context.Context, typ string) (*model.BinaryItem, error) {
	matches, err := r.repo.ListByType(ctx, typ)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errs.NotFound(errs.CodeBinaryNotFound,
			fmt.Sprintf("Binary not found by type: %s", typ))
	}
	if len(matches) == 1 {
		enriched := r.enrich(ctx, matches)
		return enriched[0], nil
	}

	// Sort by semver descending (newest first)
	sort.Slice(matches, func(i, j int) bool {
		return version.Compare(matches[i].Version, matches[j].Version) > 0
	})
	enriched := r.enrich(ctx, matches)
	return enriched[0], nil
}

// GetDefault resolves the default binary for a given type, or nil if not set.
func (r *Resolver) GetDefault(ctx context.Context, typ string) (*model.BinaryItem, error) {
	binary, err := r.repo.GetDefault(ctx, typ)
	if err != nil {
		return nil, err
	}
	if binary == nil {
		return nil, nil
	}
	enriched := r.enrich(ctx, []*model.BinaryItem{binary})
	return enriched[0], nil
}

// Resolve resolves a binary by ID prefix or name (latest version).
func (r *Resolver) Resolve(ctx context.Context, value string) (*model.BinaryItem, error) {
	// Try "type:version" selector format first
	typ, ver := version.ParseSelector(value)
	if typ != "" && ver != "" {
		return r.ByTypeVersion(ctx, typ, ver)
	}

	// Try by ID first
	b, err := r.ByID(ctx, value)
	if err == nil {
		return b, nil
	}

	// Only fall through on BinaryNotFoundError.
	if domainErr, ok := err.(*errs.DomainError); ok && domainErr.Code == errs.CodeBinaryNotFound {
		return r.ByTypeLatest(ctx, value)
	}
	return nil, err
}

// ResolveMany resolves multiple binary identifiers.
func (r *Resolver) ResolveMany(ctx context.Context, identifiers []string) *ResolveResult {
	// Dedup input identifiers (e.g. duplicate CLI args) before processing.
	uniqueIDs := infra.Dedup(identifiers)

	var items []*model.BinaryItem
	var errsList []string
	resolvedIDs := make(map[string]bool)

	for _, identifier := range uniqueIDs {
		item, err := r.Resolve(ctx, identifier)
		if err != nil {
			var de *errs.DomainError
			if errors.As(err, &de) && de.Code == errs.CodeBinaryNotFound {
				errsList = append(errsList, err.Error())
			} else {
				return &ResolveResult{
					Items:  items,
					Errors: append(errsList, err.Error()),
				}
			}
		} else if item != nil && !resolvedIDs[item.ID] {
			// Dedup by DB record ID — different input identifiers may
			// resolve to the same DB record (e.g. "firecracker" and
			// "firecracker:1.15" when only v1.15 exists).
			resolvedIDs[item.ID] = true
			items = append(items, item)
		}
	}

	items = r.enrich(ctx, items)

	exitCode := 0
	if len(errsList) > 0 && len(items) == 0 {
		exitCode = 1
	}
	return &ResolveResult{
		Items:    items,
		Errors:   errsList,
		ExitCode: exitCode,
	}
}
