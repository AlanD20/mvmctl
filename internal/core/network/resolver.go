package network

import (
	"context"
	"fmt"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// ResolveResult holds the result of multi-resolve.
type ResolveResult struct {
	Items    []*model.NetworkItem
	Errors   []string
	ExitCode int
}

// NetworkEnrichFunc is a callback for enriching networks with relations.
// Set by the API layer during wiring to avoid circular imports.
type NetworkEnrichFunc func(ctx context.Context, networks []*model.NetworkItem) ([]*model.NetworkItem, error)

// Resolver resolves network identifiers.
type Resolver struct {
	repo     Repository
	include  []string
	enrichFn NetworkEnrichFunc
}

func NewResolver(repo Repository, include []string) *Resolver {
	return &Resolver{repo: repo, include: include}
}

// SetEnrichFunc sets the enrichment function called after each resolution.
// Must be set for cross-domain relation enrichment (e.g., populating leases,
// firewall rules, and VMs).
func (r *Resolver) SetEnrichFunc(fn NetworkEnrichFunc) {
	r.enrichFn = fn
}

// Enrich enriches networks with relations if include is set.
func (r *Resolver) enrich(ctx context.Context, networks []*model.NetworkItem) []*model.NetworkItem {
	if r.include == nil || len(r.include) == 0 || len(networks) == 0 {
		return networks
	}
	if r.enrichFn != nil {
		enriched, err := r.enrichFn(ctx, networks)
		if err == nil {
			return enriched
		}
	}
	return networks
}

// EnrichWithRelations loads relations for a resolved network.
// This is the public entry point for the enricher package to call.
func (r *Resolver) EnrichWithRelations(ctx context.Context, networks []*model.NetworkItem) []*model.NetworkItem {
	return r.enrich(ctx, networks)
}

func (r *Resolver) ByID(ctx context.Context, networkID string, includeDeleted ...bool) (*model.NetworkItem, error) {
	matches, err := r.repo.FindByPrefix(ctx, networkID, includeDeleted...)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, fmt.Sprintf("model.NetworkItem not found: %s", networkID))
	}
	if len(matches) > 1 {
		return nil, errs.NotFound(errs.CodeNetworkNotFound,
			fmt.Sprintf("model.NetworkItem ID is ambiguous: %s", networkID))
	}
	return r.enrich(ctx, matches)[0], nil
}

func (r *Resolver) ByName(ctx context.Context, name string, includeDeleted ...bool) (*model.NetworkItem, error) {
	network, err := r.repo.GetByName(ctx, name, includeDeleted...)
	if err != nil {
		return nil, err
	}
	if network == nil {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, fmt.Sprintf("model.NetworkItem not found: %s", name))
	}
	return r.enrich(ctx, []*model.NetworkItem{network})[0], nil
}

func (r *Resolver) GetDefault(ctx context.Context) (*model.NetworkItem, error) {
	network, err := r.repo.GetDefault(ctx)
	if err != nil {
		return nil, err
	}
	if network == nil {
		return nil, nil
	}
	return r.enrich(ctx, []*model.NetworkItem{network})[0], nil
}

func (r *Resolver) Resolve(ctx context.Context, value string, includeDeleted ...bool) (*model.NetworkItem, error) {
	// Try by name first, then by ID prefix
	// from by_name — any other error (DB error, etc.) propagates immediately.
	network, err := r.ByName(ctx, value, includeDeleted...)
	if err == nil {
		return network, nil
	}
	if !errs.IsNotFound(err) {
		return nil, err // propagate non-not-found errors
	}
	network, err2 := r.ByID(ctx, value, includeDeleted...)
	if err2 == nil {
		return network, nil
	}
	// NOT the original by_name exception.
	return nil, err2
}

func (r *Resolver) ResolveMany(
	ctx context.Context,
	identifiers []string,
	includeDeleted ...bool,
) (*ResolveResult, error) {
	uniqueIDs := infra.Dedup(identifiers)

	var items []*model.NetworkItem
	var errorsList []string
	resolvedIDs := make(map[string]bool)

	for _, identifier := range uniqueIDs {
		item, err := r.Resolve(ctx, identifier, includeDeleted...)
		if err != nil {
			errorsList = append(errorsList, err.Error())
			continue
		}
		if !resolvedIDs[item.ID] {
			resolvedIDs[item.ID] = true
			items = append(items, item)
		}
	}

	items = r.enrich(ctx, items)

	exitCode := 0
	if len(errorsList) > 0 && len(items) == 0 {
		exitCode = 1
	}

	return &ResolveResult{
		Items:    items,
		Errors:   errorsList,
		ExitCode: exitCode,
	}, nil
}
