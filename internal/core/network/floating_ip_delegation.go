package network

import (
	"context"
	"log/slog"
	"time"

	"mvmctl/internal/lib/model"
	networkservice "mvmctl/internal/service/network"
)

// GrantFloatingIPDelegation returns root-persisted permission, not Guest Control readiness.
func (service *Service) GrantFloatingIPDelegation(
	ctx context.Context, id, networkID, resourceID, vmID, method string, duration time.Duration,
) (*model.FloatingIPDelegationItem, error) {
	result, err := service.floating.GrantFloatingIPDelegation(ctx, networkservice.FloatingIPDelegationGrantRequest{
		ID: id, NetworkID: networkID, ResourceID: resourceID, VMID: vmID, Method: method, LeaseDuration: duration})
	if err != nil {
		slog.Error("failed to store floating-IP delegation", "error", err)
		return nil, err
	}
	return &result, nil
}

// ListFloatingIPDelegations follows validated root pages, including revoked immutable lifetimes.
func (service *Service) ListFloatingIPDelegations(
	ctx context.Context, networkID, resourceID string,
) ([]*model.FloatingIPDelegationItem, error) {
	request := networkservice.FloatingIPDelegationListRequest{NetworkID: networkID, ResourceID: resourceID}
	items := make([]*model.FloatingIPDelegationItem, 0)
	for {
		page, err := service.floating.ListFloatingIPDelegations(ctx, request)
		if err != nil {
			slog.Error("failed to list floating-IP delegations", "error", err)
			return nil, err
		}
		for _, item := range page.Items {
			items = append(items, &item)
		}
		if page.NextID == "" {
			return items, nil
		}
		request.AfterID = page.NextID
	}
}

// GetFloatingIPDelegation reads one exact root permission without a SQLite grant row.
func (service *Service) GetFloatingIPDelegation(
	ctx context.Context, networkID, id string,
) (*model.FloatingIPDelegationItem, error) {
	result, err := service.floating.GetFloatingIPDelegation(ctx,
		networkservice.FloatingIPDelegationGetRequest{NetworkID: networkID, ID: id})
	if err != nil {
		slog.Error("failed to inspect floating-IP delegation", "error", err)
		return nil, err
	}
	return &result, nil
}

// RevokeFloatingIPDelegation fences exact sustaining ownership before reporting root revocation.
func (service *Service) RevokeFloatingIPDelegation(
	ctx context.Context, item *model.FloatingIPDelegationItem,
) (*model.FloatingIPDelegationItem, error) {
	result, err := service.floating.RevokeFloatingIPDelegation(ctx, networkservice.FloatingIPDelegationRevokeRequest{
		NetworkID: item.NetworkID, ID: item.ID, ExpectedRevision: item.Revision})
	if err != nil {
		slog.Error("failed to revoke floating-IP delegation", "error", err)
		return nil, err
	}
	return &result, nil
}

// RemoveOwnedFloatingIPDelegation compares the immutable root receipt before retiring that grant lifetime.
func (service *Service) RemoveOwnedFloatingIPDelegation(
	ctx context.Context, grant model.FloatingIPDelegationItem,
) (*model.FloatingIPDelegationItem, error) {
	result, err := service.floating.RemoveOwnedFloatingIPDelegation(ctx,
		networkservice.FloatingIPDelegationRemoveOwnedRequest{Grant: grant})
	if err != nil {
		slog.Error("failed to remove owned floating-IP delegation", "error", err)
		return nil, err
	}
	return &result, nil
}
