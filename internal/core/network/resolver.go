package network

import (
	"context"
	"fmt"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// ResolveResult holds the result of multi-resolve.
type ResolveResult struct {
	Items    []*model.Network
	Errors   []string
	ExitCode int
}

// NetworkEnrichFunc is a callback for enriching networks with relations.
// Set by the API layer during wiring to avoid circular imports.
type NetworkEnrichFunc func(ctx context.Context, networks []*model.Network) ([]*model.Network, error)

// Resolver resolves network identifiers.
// Matches src/mvmctl/core/network/_resolver.py: Resolver
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
// Matches Python's Resolver.enrich() method which calls
// RelationEnricher().enrich(networks, self._include, self.RELATIONS).
func (r *Resolver) enrich(ctx context.Context, networks []*model.Network) []*model.Network {
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
// Matches Python's Resolver.enrich() used by RelationEnricher.
func (r *Resolver) EnrichWithRelations(ctx context.Context, networks []*model.Network) []*model.Network {
	return r.enrich(ctx, networks)
}

func (r *Resolver) ByID(ctx context.Context, networkID string) (*model.Network, error) {
	matches, err := r.repo.FindByPrefix(ctx, networkID)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, fmt.Sprintf("model.Network not found: %s", networkID))
	}
	if len(matches) > 1 {
		return nil, errs.NotFound(errs.CodeNetworkNotFound,
			fmt.Sprintf("model.Network ID is ambiguous: %s", networkID))
	}
	return r.enrich(ctx, matches)[0], nil
}

func (r *Resolver) ByName(ctx context.Context, name string) (*model.Network, error) {
	network, err := r.repo.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if network == nil {
		return nil, errs.NotFound(errs.CodeNetworkNotFound, fmt.Sprintf("model.Network not found: %s", name))
	}
	return r.enrich(ctx, []*model.Network{network})[0], nil
}

func (r *Resolver) GetDefault(ctx context.Context) (*model.Network, error) {
	network, err := r.repo.GetDefault(ctx)
	if err != nil {
		return nil, err
	}
	if network == nil {
		return nil, nil
	}
	return r.enrich(ctx, []*model.Network{network})[0], nil
}

func (r *Resolver) Resolve(ctx context.Context, value string) (*model.Network, error) {
	// Try by name first, then by ID prefix
	// Matches Python's resolve() which catches only NetworkNotFoundError
	// from by_name — any other error (DB error, etc.) propagates immediately.
	network, err := r.ByName(ctx, value)
	if err == nil {
		return network, nil
	}
	if !errs.IsNotFound(err) {
		return nil, err // propagate non-not-found errors
	}
	network, err2 := r.ByID(ctx, value)
	if err2 == nil {
		return network, nil
	}
	// Python: if by_id also raises, that exception (from by_id) propagates,
	// NOT the original by_name exception.
	return nil, err2
}

func (r *Resolver) ResolveMany(ctx context.Context, identifiers []string) (*ResolveResult, error) {
	uniqueIDs := infra.Dedup(identifiers)

	var items []*model.Network
	var errorsList []string
	resolvedIDs := make(map[string]bool)

	for _, identifier := range uniqueIDs {
		item, err := r.Resolve(ctx, identifier)
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
