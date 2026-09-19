package api

import (
	"context"
	"fmt"
	"time"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/crypto"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
)

// NetworkAPI defines logical network operations backed by root-owned network authority.
type NetworkAPI interface {
	NetworkCreate(context.Context, inputs.NetworkCreateInput) (*model.NetworkItem, error)
	NetworkRemove(context.Context, inputs.NetworkInput, bool) error
	NetworkListAll(context.Context) ([]*model.NetworkItem, error)
	NetworkGet(context.Context, inputs.NetworkInput) (*model.NetworkItem, error)
	NetworkToJSON([]*model.NetworkItem) []map[string]any
	NetworkInspect(context.Context, inputs.NetworkInput) (*results.NetworkInspect, error)
	NetworkSetDefault(context.Context, inputs.NetworkInput) error
	NetworkSync(context.Context, inputs.NetworkInput) (map[string]map[string]int, error)
	NetworkPrune(context.Context, bool, bool) ([]string, error)
	NetworkCreateDefaultNetwork(context.Context) (*model.NetworkItem, error)
}

// NetworkCreate preserves a pending logical identity so an interrupted root transaction can be retried exactly.
func (op *Operation) NetworkCreate(ctx context.Context, input inputs.NetworkCreateInput) (*model.NetworkItem, error) {
	resolved, err := input.Resolve(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	item := resolved.Pending
	if item == nil {
		createdAt := time.Now().UTC().Format(time.RFC3339)
		item = &model.NetworkItem{ID: crypto.NetworkID(resolved.Name, resolved.Subnet, createdAt),
			Name: resolved.Name, Subnet: resolved.Subnet, IPv4Gateway: resolved.IPv4Gateway,
			NATEnabled: resolved.NATEnabled, CreatedAt: createdAt, UpdatedAt: createdAt,
			Bridge: "", BridgeActive: false, IsPresent: false}
		// This row is desired state only. Root never reads it and acknowledgement alone publishes observed state.
		if err := op.Repos.Network.Upsert(ctx, item); err != nil {
			return nil, errs.WrapMsg(errs.CodeDatabaseError, "persist pending network intent", err)
		}
	}
	if err := op.Services.Network.EnsureManagedNetwork(ctx, item); err != nil {
		return item, err
	}
	if input.SetDefault {
		if err := op.Repos.Network.SetDefault(ctx, item.ID); err != nil {
			return item, err
		}
		item.IsDefault = true
	}
	op.AuditLog.LogOperation("network.create", map[string]any{"id": item.ID, "name": item.Name}, "")
	return item, nil
}

// NetworkRemove performs each root transaction before its local projection is removed.
func (op *Operation) NetworkRemove(ctx context.Context, input inputs.NetworkInput, force bool) error {
	networks, err := input.Resolve(ctx, op.Repos.Network)
	if err != nil {
		return err
	}
	if err := op.Enr.EnrichNetwork(ctx, networks, "vm", "snapshots"); err != nil {
		return err
	}
	for _, item := range networks {
		if err := op.Services.Network.Remove(ctx, item, force); err != nil {
			return err
		}
		op.AuditLog.LogOperation("network.remove", map[string]any{"id": item.ID, "name": item.Name}, "")
	}
	return nil
}

// NetworkListAll returns networks with observed link presence and desired guest leases.
func (op *Operation) NetworkListAll(ctx context.Context) ([]*model.NetworkItem, error) {
	networks, err := op.Services.Network.ListAll(ctx, true)
	if err != nil {
		return nil, err
	}
	if len(networks) > 0 {
		if err := op.Services.Network.EnrichWithLeases(ctx, networks, op.Repos.Lease); err != nil {
			return nil, err
		}
	}
	return networks, nil
}

// NetworkGet resolves one logical network.
func (op *Operation) NetworkGet(ctx context.Context, input inputs.NetworkInput) (*model.NetworkItem, error) {
	networks, err := input.Resolve(ctx, op.Repos.Network)
	if err != nil {
		return nil, err
	}
	if len(networks) != 1 {
		return nil, errs.New(errs.CodeValidationFailed, "exactly one network is required")
	}
	return networks[0], nil
}

// NetworkToJSON exposes logical NAT intent without an uplink selector.
func (op *Operation) NetworkToJSON(networks []*model.NetworkItem) []map[string]any {
	items := make([]map[string]any, 0, len(networks))
	for _, item := range networks {
		items = append(items, map[string]any{"id": item.ID, "name": item.Name, "subnet": item.Subnet,
			"bridge": item.Bridge, "ipv4_gateway": item.IPv4Gateway, "bridge_active": item.BridgeActive,
			"nat_enabled": item.NATEnabled, "is_default": item.IsDefault, "is_present": item.IsPresent,
			"created_at": item.CreatedAt, "updated_at": item.UpdatedAt})
	}
	return items
}

// NetworkInspect returns desired configuration, observed presence and allocations.
func (op *Operation) NetworkInspect(ctx context.Context, input inputs.NetworkInput) (*results.NetworkInspect, error) {
	item, err := op.NetworkGet(ctx, input)
	if err != nil {
		return nil, err
	}
	observed, err := op.Services.Network.ListAll(ctx, true)
	if err != nil {
		return nil, err
	}
	for _, candidate := range observed {
		if candidate.ID == item.ID {
			item = candidate
			break
		}
	}
	leases, err := op.Repos.Lease.ListAll(ctx, item.ID)
	if err != nil {
		return nil, err
	}
	leaseList := make([]results.NetworkLease, 0, len(leases))
	for _, lease := range leases {
		value := results.NetworkLease{IPv4: lease.IPv4, LeasedAt: lease.LeasedAt}
		if lease.ID != nil {
			value.ID = *lease.ID
		}
		if lease.VMID != nil {
			value.VMID = *lease.VMID
		}
		if lease.ExpiresAt != nil {
			value.ExpiresAt = *lease.ExpiresAt
		}
		leaseList = append(leaseList, value)
	}
	return &results.NetworkInspect{
		Network: results.NetworkItemInfo{ID: item.ID, Name: item.Name, Subnet: item.Subnet,
			Bridge: item.Bridge, IPv4Gateway: item.IPv4Gateway, IsDefault: item.IsDefault, IsPresent: item.IsPresent,
			CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt},
		Status: results.NetworkStatusInfo{
			BridgeActive: item.BridgeActive,
			IsPresent:    item.IsPresent,
			IsDefault:    item.IsDefault,
		},
		NAT:    results.NetworkNATInfo{NATEnabled: item.NATEnabled},
		Leases: leaseList,
	}, nil
}

// NetworkSetDefault changes only desired default selection.
func (op *Operation) NetworkSetDefault(ctx context.Context, input inputs.NetworkInput) error {
	item, err := op.NetworkGet(ctx, input)
	if err != nil {
		return err
	}
	if err := network.NewController(item, op.Repos.Network).SetDefault(ctx); err != nil {
		return err
	}
	op.AuditLog.LogOperation("network.set_default", map[string]any{"name": item.Name}, "")
	return nil
}

// NetworkSync readmits exact root state and retries pending creation without scanning or deleting unrelated links.
func (op *Operation) NetworkSync(ctx context.Context, input inputs.NetworkInput) (map[string]map[string]int, error) {
	var items []*model.NetworkItem
	var err error
	if len(input.Identifiers) == 0 {
		items, err = op.Repos.Network.ListAll(ctx)
	} else {
		items, err = input.Resolve(ctx, op.Repos.Network)
	}
	if err != nil {
		return nil, err
	}
	completed := make(map[string]map[string]int, len(items))
	for _, item := range items {
		if item.DeletedAt != nil {
			continue
		}
		if err := op.Services.Network.EnsureManagedNetwork(ctx, item); err != nil {
			return completed, err
		}
		completed[item.ID] = map[string]int{"verified": 1, "added": 0, "orphaned": 0}
	}
	return completed, nil
}

// NetworkPrune removes selected unused networks through the same exact root removal path.
func (op *Operation) NetworkPrune(ctx context.Context, dryRun bool, includeAll bool) ([]string, error) {
	networks, err := op.Repos.Network.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if err := op.Enr.EnrichNetwork(ctx, networks, "vm", "snapshots"); err != nil {
		return nil, err
	}
	removed := make([]string, 0)
	for _, item := range networks {
		if !includeAll && (item.IsDefault || len(item.VMs) > 0 || len(item.Snapshots) > 0) {
			continue
		}
		leases, err := op.Repos.Lease.ListAll(ctx, item.ID)
		if err != nil {
			return removed, err
		}
		if !includeAll && len(leases) > 0 {
			continue
		}
		if !dryRun {
			if err := op.Services.Network.Remove(ctx, item, includeAll); err != nil {
				return removed, err
			}
		}
		removed = append(removed, item.Name)
	}
	return removed, nil
}

// NetworkCreateDefaultNetwork creates or readmits the configured default through root-owned authority.
func (op *Operation) NetworkCreateDefaultNetwork(ctx context.Context) (*model.NetworkItem, error) {
	nameValue, err := op.ConfigGet(ctx, "defaults.network", "name")
	if err != nil {
		return nil, err
	}
	name, ok := nameValue.(string)
	if !ok {
		return nil, errs.New(errs.CodeValidationFailed, "default network name must be text")
	}
	item, err := op.Repos.Network.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if item == nil {
		subnetValue, err := op.ConfigGet(ctx, "defaults.network", "subnet")
		if err != nil {
			return nil, err
		}
		subnet, ok := subnetValue.(string)
		if !ok {
			return nil, errs.New(errs.CodeValidationFailed, "default network subnet must be text")
		}
		natValue, err := op.ConfigGet(ctx, "defaults.network", "nat_enabled")
		if err != nil {
			return nil, err
		}
		nat, ok := natValue.(bool)
		if !ok {
			return nil, errs.New(errs.CodeValidationFailed, "default network NAT setting must be boolean")
		}
		item, err = op.NetworkCreate(ctx, inputs.NetworkCreateInput{Name: name, Subnet: subnet, NATEnabled: nat})
		if err != nil {
			return item, err
		}
	} else if err := op.Services.Network.EnsureManagedNetwork(ctx, item); err != nil {
		return item, err
	}
	selected, err := op.Repos.Network.GetDefault(ctx)
	if err != nil {
		return item, err
	}
	if selected == nil {
		if err := op.Repos.Network.SetDefault(ctx, item.ID); err != nil {
			return item, err
		}
		item.IsDefault = true
		selected = item
	}
	if op.Repos.Host != nil {
		if err := op.Repos.Host.UpdateComponent(ctx, "default_network_created", true); err != nil {
			return selected, errs.WrapMsg(
				errs.CodeNetworkDefaultCreateFailed,
				fmt.Sprintf("network %s configured but host projection failed", selected.Name),
				err,
			)
		}
	}
	return selected, nil
}
