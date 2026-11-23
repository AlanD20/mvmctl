package network

import (
	"context"
	"errors"
)

// ListFloatingIPDelegations reads stored permission, including revoked lifetimes, without recovery or worker startup.
func (server *FloatingIPServer) ListFloatingIPDelegations(
	ctx context.Context, ownerUID uint32, request FloatingIPDelegationListRequest,
) (FloatingIPDelegationPage, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationPage{}, err
	}
	records, err := readFloatingIPDelegations(ctx, ownerUID, request.NetworkID)
	if err != nil {
		return FloatingIPDelegationPage{}, err
	}
	return floatingIPDelegationRecordPage(records, request), nil
}

func floatingIPDelegationRecordPage(
	records []floatingIPDelegationRecord, request FloatingIPDelegationListRequest,
) FloatingIPDelegationPage {
	page := FloatingIPDelegationPage{Items: make([]FloatingIPDelegationResult, 0)}
	for _, record := range records {
		if record.ResourceID != request.ResourceID || record.ID <= request.AfterID {
			continue
		}
		if len(page.Items) == floatingIPPageSize {
			page.NextID = page.Items[len(page.Items)-1].ID
			break
		}
		page.Items = append(page.Items, floatingIPDelegationProjection(record))
	}
	return page
}

// GetFloatingIPDelegation returns an exact stored grant without trusting caller-owned projections.
func (server *FloatingIPServer) GetFloatingIPDelegation(
	ctx context.Context, ownerUID uint32, request FloatingIPDelegationGetRequest,
) (_ FloatingIPDelegationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(request.NetworkID))
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	store, err := openFloatingIPDelegationStore(ctx, tx.store, false)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	record, err := readSelectedFloatingIPDelegation(ctx, store, request.NetworkID, request.ID)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	return floatingIPDelegationProjection(record), nil
}

func readFloatingIPDelegations(
	ctx context.Context, ownerUID uint32, networkID string,
) (_ []floatingIPDelegationRecord, returnErr error) {
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(networkID))
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	store, err := openFloatingIPDelegationStore(ctx, tx.store, false)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	records, err := store.list(ctx)
	if err != nil {
		return nil, err
	}
	selected := make([]floatingIPDelegationRecord, 0)
	for _, record := range records {
		if record.NetworkID == networkID {
			selected = append(selected, record)
		}
	}
	return selected, nil
}
