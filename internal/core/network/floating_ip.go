package network

import (
	"context"
	"log/slog"

	"mvmctl/internal/lib/model"
	networkservice "mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// CreateFloatingIP reserves root authority before returning an operator-visible projection.
func (service *Service) CreateFloatingIP(
	ctx context.Context,
	id, networkID, name, address string,
) (*model.FloatingIPItem, error) {
	result, err := service.floating.CreateFloatingIP(ctx, networkservice.FloatingIPCreateRequest{
		ID: id, NetworkID: networkID, Name: name, IPv4: address,
	})
	if err != nil {
		slog.Error("failed to reserve floating IP", "network_id", networkID, "error", err)
		return nil, err
	}
	return floatingIPProjection(result), nil
}

// ListFloatingIPs follows bounded root inventory pages. Root data remains the authority.
func (service *Service) ListFloatingIPs(ctx context.Context, networkID string) ([]*model.FloatingIPItem, error) {
	items := make([]*model.FloatingIPItem, 0)
	request := networkservice.FloatingIPListRequest{NetworkID: networkID}
	names := make(map[string]struct{})
	addresses := make(map[string]struct{})
	for {
		page, err := service.floating.ListFloatingIPs(ctx, request)
		if err != nil {
			slog.Error("failed to read floating-IP inventory", "network_id", networkID, "error", err)
			return nil, err
		}
		for _, item := range page.Items {
			_, duplicateName := names[item.Name]
			_, duplicateAddress := addresses[item.IPv4]
			if duplicateName || duplicateAddress {
				err := errs.New(
					errs.CodeNetworkError,
					"floating-IP inventory changed during pagination; retry listing",
				)
				slog.Error("floating-IP inventory changed during pagination", "network_id", networkID, "error", err)
				return nil, err
			}
			names[item.Name], addresses[item.IPv4] = struct{}{}, struct{}{}
			items = append(items, floatingIPProjection(item))
		}
		if page.NextID == "" {
			return items, nil
		}
		request.AfterID = page.NextID
	}
}

// HasFloatingIPs reads only the first root inventory page when deciding whether a network is unused.
func (service *Service) HasFloatingIPs(ctx context.Context, networkID string) (bool, error) {
	page, err := service.floating.ListFloatingIPs(ctx, networkservice.FloatingIPListRequest{NetworkID: networkID})
	if err != nil {
		slog.Error("failed to inspect floating-IP references", "network_id", networkID, "error", err)
		return false, err
	}
	return len(page.Items) != 0, nil
}

// RemoveFloatingIP retires the exact generation the caller observed.
func (service *Service) RemoveFloatingIP(
	ctx context.Context,
	item *model.FloatingIPItem,
) (*model.FloatingIPItem, error) {
	result, err := service.floating.RemoveFloatingIP(ctx, networkservice.FloatingIPRemoveRequest{
		ID: item.ID, NetworkID: item.NetworkID, ExpectedGeneration: item.Generation,
	})
	if err != nil {
		slog.Error("failed to retire floating IP", "id", item.ID, "network_id", item.NetworkID, "error", err)
		return nil, err
	}
	if err := service.trafficRepo.InvalidateByFloatingIP(ctx, item.ID); err != nil {
		return nil, errs.WrapMsg(
			errs.CodeDatabaseError,
			"floating IP retired but local policy projection cleanup failed; retry policy removal", err,
			errs.WithDetails(map[string]any{"root_resource_retired": true, "projection_failed": true}))
	}
	return floatingIPProjection(result), nil
}

func floatingIPProjection(result networkservice.FloatingIPResult) *model.FloatingIPItem {
	return &model.FloatingIPItem{
		ID: result.ID, NetworkID: result.NetworkID, Name: result.Name, IPv4: result.IPv4,
		Generation: result.Generation, Retired: result.Retired,
		Phase: result.Phase, OwnerVMID: result.OwnerVMID, OwnerOrigin: result.OwnerOrigin, Lifetime: result.Lifetime,
		OwnerBootID: result.OwnerBootID, OwnerLaunchGeneration: result.OwnerLaunchGeneration,
		AuthorityState: result.AuthorityState, LeaseRemainingMillis: result.LeaseRemainingMillis, AliasSynchronization: result.AliasSynchronization,
	}
}
