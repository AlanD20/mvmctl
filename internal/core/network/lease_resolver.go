package network

import (
	"context"

	"mvmctl/internal/lib/model"
)

// LeaseResolver resolves network IP leases.
type LeaseResolver struct {
	repo    LeaseRepository
	include []string
}

// NewLeaseResolver creates a resolver with optional enrichment relations.
// include controls cross-domain enrichment (passed by the enricher).
func NewLeaseResolver(repo LeaseRepository, include []string) *LeaseResolver {
	return &LeaseResolver{repo: repo, include: include}
}

// enrich enriches leases with relations if include is set.
// Enrichment of cross-domain relations is handled by the enricher package
// (internal/enricher/) which is the only package authorised to import
// across core/* domain boundaries. This method provides the extension point
// for the enricher to call back into.
func (r *LeaseResolver) enrich(leases []*model.NetworkLeaseItem) []*model.NetworkLeaseItem {
	if r.include == nil || len(r.include) == 0 || len(leases) == 0 {
		return leases
	}
	// RelationEnricher enrichment goes here when relations are configured.
	return leases
}

// ListByNetworkID lists all leases for a network.
func (r *LeaseResolver) ListByNetworkID(ctx context.Context, networkID string) ([]*model.NetworkLeaseItem, error) {
	leases, err := r.repo.ListAll(ctx, networkID)
	if err != nil {
		return nil, err
	}
	return r.enrich(leases), nil
}

// ListByNetworkIDBatch batch-resolves leases by network IDs.
func (r *LeaseResolver) ListByNetworkIDBatch(
	ctx context.Context,
	networkIDs []string,
) (map[string][]*model.NetworkLeaseItem, error) {
	leases, err := r.repo.ListAllBatch(ctx, networkIDs)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]*model.NetworkLeaseItem)
	for _, nid := range networkIDs {
		result[nid] = []*model.NetworkLeaseItem{}
	}
	for _, lease := range leases {
		if _, ok := result[lease.NetworkID]; ok {
			result[lease.NetworkID] = append(result[lease.NetworkID], lease)
		}
	}
	return result, nil
}

// Get returns a specific lease by network_id + ipv4.
func (r *LeaseResolver) Get(ctx context.Context, networkID, ipv4 string) (*model.NetworkLeaseItem, error) {
	lease, err := r.repo.Get(ctx, networkID, ipv4)
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return nil, nil
	}
	return r.enrich([]*model.NetworkLeaseItem{lease})[0], nil
}

// ListByVM lists all leases for a VM on a specific network.
func (r *LeaseResolver) ListByVM(ctx context.Context, networkID, vmID string) ([]*model.NetworkLeaseItem, error) {
	leases, err := r.repo.ListByVM(ctx, networkID, vmID)
	if err != nil {
		return nil, err
	}
	return r.enrich(leases), nil
}
