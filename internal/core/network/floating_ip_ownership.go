package network

import (
	"context"
	"log/slog"

	"mvmctl/internal/lib/model"
	networkservice "mvmctl/internal/service/network"
)

// AssociateFloatingIP requests persistent operator authority, never a delegated guest lease.
func (service *Service) AssociateFloatingIP(
	ctx context.Context,
	item *model.FloatingIPItem,
	vmID string,
	generation uint64,
) (*model.FloatingIPItem, error) {
	result, err := service.floating.AssociateFloatingIP(
		ctx,
		networkservice.FloatingIPAssociateRequest{
			ID:                 item.ID,
			NetworkID:          item.NetworkID,
			VMID:               vmID,
			ExpectedGeneration: generation,
		},
	)
	if err != nil {
		slog.Error("failed to associate floating IP", "id", item.ID, "vm_id", vmID, "error", err)
		return nil, err
	}
	return floatingIPProjection(result), nil
}

// TransferFloatingIP explicitly replaces the exact observed ownership generation.
func (service *Service) TransferFloatingIP(
	ctx context.Context,
	item *model.FloatingIPItem,
	vmID string,
	generation uint64,
) (*model.FloatingIPItem, error) {
	result, err := service.floating.TransferFloatingIP(
		ctx,
		networkservice.FloatingIPTransferRequest{
			ID:                 item.ID,
			NetworkID:          item.NetworkID,
			VMID:               vmID,
			ExpectedGeneration: generation,
		},
	)
	if err != nil {
		slog.Error("failed to transfer floating IP", "id", item.ID, "vm_id", vmID, "error", err)
		return nil, err
	}
	return floatingIPProjection(result), nil
}

// ReleaseFloatingIP fences exact ownership even if the old target is stopped.
func (service *Service) ReleaseFloatingIP(
	ctx context.Context,
	item *model.FloatingIPItem,
	generation uint64,
) (*model.FloatingIPItem, error) {
	result, err := service.floating.ReleaseFloatingIP(
		ctx,
		networkservice.FloatingIPReleaseRequest{ID: item.ID, NetworkID: item.NetworkID, ExpectedGeneration: generation},
	)
	if err != nil {
		slog.Error("failed to release floating IP", "id", item.ID, "error", err)
		return nil, err
	}
	return floatingIPProjection(result), nil
}

// InspectFloatingIP observes current root and kernel authority without extending ownership.
func (service *Service) InspectFloatingIP(
	ctx context.Context,
	item *model.FloatingIPItem,
) (*model.FloatingIPItem, error) {
	result, err := service.floating.GetFloatingIP(
		ctx,
		networkservice.FloatingIPGetRequest{ID: item.ID, NetworkID: item.NetworkID},
	)
	if err != nil {
		slog.Error("failed to inspect floating-IP ownership", "id", item.ID, "error", err)
		return nil, err
	}
	return floatingIPProjection(result), nil
}
