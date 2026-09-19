package network

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"mvmctl/internal/lib/firewall"
	"mvmctl/internal/lib/model"
	networkservice "mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// Service coordinates logical network projections with the root-owned managed-network service.
type Service struct {
	repo        Repository
	trafficRepo TrafficPolicyRepository
	managed     *networkservice.ManagedNetworkClient
	// The existing NoCloud consumer retains this dependency until its typed host-service cutover.
	noCloudTracker *firewall.FirewallTracker
}

// NewService binds desired-state repositories to the fixed privileged network client.
func NewService(repo Repository, trafficRepo TrafficPolicyRepository, noCloudTracker *firewall.FirewallTracker) *Service {
	return &Service{repo: repo, trafficRepo: trafficRepo, managed: networkservice.NewManagedNetworkClient(), noCloudTracker: noCloudTracker}
}

// FirewallTracker supplies only the existing NoCloud provisioner during its separately owned cutover.
func (service *Service) FirewallTracker() *firewall.FirewallTracker { return service.noCloudTracker }

// EnsureManagedNetwork applies canonical intent and updates observed state only after root acknowledgement.
func (service *Service) EnsureManagedNetwork(ctx context.Context, network *model.NetworkItem) error {
	result, err := service.managed.CreateManagedNetwork(ctx, networkservice.ManagedNetworkCreateRequest{
		NetworkID: network.ID, Prefix: network.Subnet, Gateway: network.IPv4Gateway, NAT: network.NATEnabled,
	})
	if err != nil {
		slog.Error("failed to configure root-owned managed network", "network_id", network.ID, "error", err)
		return err
	}
	network.Bridge, network.BridgeActive, network.IsPresent = result.Bridge, true, true
	if err := service.repo.Upsert(ctx, network); err != nil {
		slog.Error("failed to project configured managed network", "network_id", network.ID, "error", err)
		return errs.WrapMsg(errs.CodeDatabaseError, "root network is configured but local projection failed; retry network sync", err,
			errs.WithDetails(map[string]any{"network_id": network.ID, "root_applied": true}))
	}
	return nil
}

// ListAll reads desired networks and optionally observes interface presence without granting mutation authority.
func (service *Service) ListAll(ctx context.Context, verify bool) ([]*model.NetworkItem, error) {
	networks, err := service.repo.ListAll(ctx)
	if err != nil || !verify {
		return networks, err
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(errs.CodeNetworkError, err)
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeNetworkError, "observe local network interfaces", err)
	}
	byName := make(map[string]net.Interface, len(interfaces))
	for _, iface := range interfaces {
		byName[iface.Name] = iface
	}
	for _, network := range networks {
		iface, present := byName[network.Bridge]
		network.BridgeActive = present && iface.Flags&net.FlagUp != 0
		// Presence is display-only. Root admits exact ifindex, namespace, alias and L3 state for every mutation.
		network.IsPresent = present
	}
	return networks, nil
}

// Remove revokes root network state before invalidating desired policies and deleting the local projection.
func (service *Service) Remove(ctx context.Context, network *model.NetworkItem, force bool) error {
	hasReferences := len(network.VMs) > 0 || len(network.Snapshots) > 0
	if hasReferences && !force {
		return errs.New(errs.CodeNetworkRemoveFailed, "network is referenced by VMs or snapshots", errs.WithClass(errs.ClassConflict))
	}
	if _, err := service.managed.RemoveManagedNetwork(ctx, networkservice.ManagedNetworkRemoveRequest{NetworkID: network.ID}); err != nil {
		slog.Error("failed to remove root-owned network", "network_id", network.ID, "error", err)
		return err
	}
	if err := service.trafficRepo.InvalidateByNetwork(ctx, network.ID); err != nil {
		slog.Error("failed to invalidate removed network policy projection", "network_id", network.ID, "error", err)
		return errs.WrapMsg(errs.CodePolicyRemoveFailed, "root network removed but policy projection cleanup failed", err)
	}
	if hasReferences {
		return service.repo.SoftDelete(ctx, network.ID)
	}
	return service.repo.Delete(ctx, network.ID)
}

// RemoveMany stops at the first failed exact network transaction.
func (service *Service) RemoveMany(ctx context.Context, networks []*model.NetworkItem, force bool) error {
	for _, network := range networks {
		if err := service.Remove(ctx, network, force); err != nil {
			return err
		}
	}
	return nil
}

// EnrichWithLeases batch-loads desired guest allocations for display.
func (service *Service) EnrichWithLeases(ctx context.Context, networks []*model.NetworkItem, leaseRepo LeaseRepository) error {
	ids := make([]string, len(networks))
	for index, network := range networks {
		ids[index] = network.ID
	}
	leases, err := leaseRepo.ListAllBatch(ctx, ids)
	if err != nil {
		return errs.Wrap(errs.CodeDatabaseError, fmt.Errorf("batch load leases: %w", err))
	}
	byNetwork := make(map[string][]*model.NetworkLeaseItem)
	for _, lease := range leases {
		byNetwork[lease.NetworkID] = append(byNetwork[lease.NetworkID], lease)
	}
	for _, network := range networks {
		network.Leases = byNetwork[network.ID]
		if network.Leases == nil {
			network.Leases = []*model.NetworkLeaseItem{}
		}
	}
	return nil
}
