// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/network_operations.py exactly.
package api

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"mvmctl/internal/core/network"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	infranet "mvmctl/internal/infra/network"
	"mvmctl/internal/infra/system"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"
)

// NetworkCreate creates a new network.
// Matches Python's NetworkOperation.create() exactly.
func (op *Operation) NetworkCreate(ctx context.Context, input *inputs.NetworkCreateInput) *errs.OperationResult {
	request := inputs.NewNetworkCreateRequest(*input, op.Connection.DB(), op.Repos.Network)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "network.create_failed",
			Message:   err.Error(),
			Exception: err,
		}
	}

	createdAt := time.Now().Format(time.RFC3339)
	hashInput := fmt.Sprintf("%s:%s:%s", resolved.Name, resolved.Subnet, createdAt)
	networkID := fmt.Sprintf("%x", sha256.Sum256([]byte(hashInput)))

	networkItem := &model.Network{
		ID:           networkID,
		Name:         resolved.Name,
		Subnet:       resolved.Subnet,
		Bridge:       resolved.Bridge,
		IPv4Gateway:  resolved.IPv4Gateway,
		BridgeActive: false,
		NATEnabled:   resolved.NATEnabled,
		IsDefault:    false,
		IsPresent:    true,
		CreatedAt:    createdAt,
		UpdatedAt:    createdAt,
	}
	if len(resolved.NATGateways) > 0 {
		joined := strings.Join(resolved.NATGateways, ",")
		networkItem.NATGateways = &joined
	}

	if err := op.Repos.Network.Upsert(ctx, networkItem); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeDatabaseError),
			Message:   "Failed to persist network: " + err.Error(),
			Exception: err,
		}
	}

	bridgeAddr, bridgeErr := network.ComputeBridgeAddress(resolved.IPv4Gateway, resolved.Subnet)
	if bridgeErr != nil {
		_ = op.Repos.Network.Delete(ctx, networkID)
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeNetworkBridgeFailed),
			Message:   fmt.Sprintf("Failed to compute bridge address: %v", bridgeErr),
			Exception: bridgeErr,
		}
	}
	if err := op.Services.Network.EnsureBridge(ctx, resolved.Bridge, bridgeAddr); err != nil {
		_ = op.Repos.Network.Delete(ctx, networkID)
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeNetworkBridgeFailed),
			Message:   fmt.Sprintf("Failed to create network '%s': %v", resolved.Name, err),
			Exception: err,
		}
	}

	if resolved.NATEnabled {
		if err := op.Services.Network.EnsureNAT(ctx, resolved.Bridge, resolved.NATGateways, resolved.Subnet, networkID); err != nil {
			_ = op.Repos.Network.Delete(ctx, networkID)
			return &errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeNetworkNATFailed),
				Message:   fmt.Sprintf("Failed to create network '%s': %v", resolved.Name, err),
				Exception: err,
			}
		}
	}

	// Update bridge_active
	bridgeActive := infranet.BridgeExists(resolved.Bridge)
	_ = op.Repos.Network.UpdateBridgeActive(ctx, networkID, bridgeActive)

	// Re-fetch
	updated, err := op.Repos.Network.GetByName(ctx, resolved.Name)
	if err != nil || updated == nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeNetworkNotFound),
			Message: fmt.Sprintf("Failed to fetch created network '%s'", resolved.Name),
		}
	}

	if input.SetDefault {
		if err := op.Repos.Network.SetDefault(ctx, updated.ID); err != nil {
			slog.Warn("Failed to set network as default", "name", input.Name, "error", err)
		}
	}

	// Audit log
	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("network.create", map[string]interface{}{"name": resolved.Name}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "network.created",
		Item:    updated,
		Message: fmt.Sprintf("Network '%s' created", resolved.Name),
	}
}

// NetworkRemove removes one or more networks.
// Matches Python's NetworkOperation.remove() exactly — uses NetworkRequest for resolution,
// enriches with VM references, checks "in use".
func (op *Operation) NetworkRemove(ctx context.Context, input *inputs.NetworkInput, force bool) *errs.OperationResult {
	request := inputs.NewNetworkRequest(*input, op.Connection.DB(), op.Repos.Network)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "network.remove_failed",
			Message:   err.Error(),
			Exception: err,
		}
	}

	// Batch-enrich with VM references for VM reference check
	// (matches Python: Resolver(repo, include=["vm"]).enrich(resolved.networks))
	if op.Enr != nil {
		_ = op.Enr.EnrichNetwork(ctx, resolved.Networks, "vm")
	}

	// Match Python: service.remove(network, force=force) raises NetworkError on failure.
	// Python catches the first error and returns it immediately — we match by iterating
	// once and returning the first error encountered.
	var results []string
	for _, net := range resolved.Networks {
		if err := op.Services.Network.Remove(ctx, net, force); err != nil {
			errorMsg := err.Error()
			code := "network.remove_failed"
			if strings.Contains(strings.ToLower(errorMsg), "in use") {
				code = "network.in_use"
			}
			return &errs.OperationResult{
				Status:    "error",
				Code:      code,
				Message:   errorMsg,
				Exception: err,
			}
		}

		auditLog := logging.NewAuditLog(op.CacheDir)
		_ = auditLog.LogOperation("network.remove", map[string]interface{}{"id": net.ID, "name": net.Name}, "")
		results = append(results, net.Name)
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "network.removed",
		Message: fmt.Sprintf("Network(s) '%s' removed", strings.Join(results, ", ")),
	}
}

// NetworkListAll returns all networks with lease enrichment.
// Matches Python's NetworkOperation.list_all() exactly.
func (op *Operation) NetworkListAll(ctx context.Context) ([]*model.Network, error) {
	networks, err := op.Services.Network.ListAll(ctx, true)
	if err != nil {
		return nil, err
	}
	if len(networks) > 0 {
		_ = op.networkEnrichWithLeases(ctx, networks)
	}
	return networks, nil
}

// NetworkGet returns a single network by Input/Request resolution pipeline.
// Matches Python's NetworkOperation.get() exactly — uses NetworkInput/NetworkRequest
// to resolve identifiers (by name or ID) and supports multi-identifier resolution.
func (op *Operation) NetworkGet(ctx context.Context, input *inputs.NetworkInput) (*model.Network, error) {
	request := inputs.NewNetworkRequest(*input, op.Connection.DB(), op.Repos.Network)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("network not found: %v", err)
	}
	if len(resolved.Networks) != 1 {
		return nil, fmt.Errorf("expected exactly one network, got %d", len(resolved.Networks))
	}
	return resolved.Networks[0], nil
}

// NetworkToJSON converts networks to JSON-serializable dicts.
// Matches Python's NetworkOperation.to_json() exactly — delegates to model's to_dict().
func (op *Operation) NetworkToJSON(networks []*model.Network) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(networks))
	for _, n := range networks {
		result = append(result, map[string]interface{}{
			"id":            n.ID,
			"name":          n.Name,
			"subnet":        n.Subnet,
			"bridge":        n.Bridge,
			"ipv4_gateway":  n.IPv4Gateway,
			"bridge_active": n.BridgeActive,
			"nat_enabled":   n.NATEnabled,
			"is_default":    n.IsDefault,
			"is_present":    n.IsPresent,
			"created_at":    n.CreatedAt,
			"updated_at":    n.UpdatedAt,
			"nat_gateways":  network.NatGatewaysList(n),
		})
	}
	return result
}

// NetworkInspect returns detailed network info via Input/Request resolution pipeline.
// Matches Python's NetworkOperation.inspect() exactly — uses NetworkInput/NetworkRequest
// to resolve identifiers (by name or ID) with lease enrichment.
func (op *Operation) NetworkInspect(ctx context.Context, input *inputs.NetworkInput) (*responses.NetworkInspect, error) {
	request := inputs.NewNetworkRequest(*input, op.Connection.DB(), op.Repos.Network)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("network not found: %v", err)
	}
	if len(resolved.Networks) != 1 {
		return nil, fmt.Errorf("expected exactly one network, got %d", len(resolved.Networks))
	}

	net := resolved.Networks[0]

	bridgeActive := infranet.BridgeExists(net.Bridge)
	if bridgeActive != net.BridgeActive {
		_ = op.Repos.Network.UpdateBridgeActive(ctx, net.ID, bridgeActive)
		net.BridgeActive = bridgeActive
	}

	updated, err := op.Repos.Network.GetByName(ctx, net.Name)
	if err != nil || updated == nil {
		return nil, fmt.Errorf("network '%s' not found after update", net.Name)
	}

	// Load leases
	leases, err := op.Repos.Lease.ListAll(ctx, updated.ID)
	leaseList := make([]responses.NetworkLease, 0)
	if err == nil {
		leaseList = make([]responses.NetworkLease, 0, len(leases))
		for _, lease := range leases {
			l := responses.NetworkLease{
				IPv4:     lease.IPv4,
				LeasedAt: lease.LeasedAt,
			}
			if lease.ID != nil {
				l.ID = *lease.ID
			}
			if lease.VMID != nil {
				l.VMID = *lease.VMID
			}
			if lease.ExpiresAt != nil {
				l.ExpiresAt = *lease.ExpiresAt
			}
			leaseList = append(leaseList, l)
		}
	}

	return &responses.NetworkInspect{
		Network: responses.NetworkItemInfo{
			ID: updated.ID, Name: updated.Name, Subnet: updated.Subnet,
			Bridge: updated.Bridge, IPv4Gateway: updated.IPv4Gateway,
			IsDefault: updated.IsDefault, IsPresent: updated.IsPresent,
			CreatedAt: updated.CreatedAt, UpdatedAt: updated.UpdatedAt,
		},
		Status: responses.NetworkStatusInfo{
			BridgeActive: updated.BridgeActive,
			IsPresent:    updated.IsPresent,
			IsDefault:    updated.IsDefault,
		},
		NAT: responses.NetworkNATInfo{
			NATEnabled:  updated.NATEnabled,
			NATGateways: network.NatGatewaysList(updated),
		},
		Leases: leaseList,
	}, nil
}

// NetworkSetDefault sets a network as default.
// Matches Python's NetworkOperation.set_default() exactly — goes through Controller
// and uses NetworkInput/NetworkRequest to resolve identifiers.
func (op *Operation) NetworkSetDefault(ctx context.Context, input *inputs.NetworkInput) *errs.OperationResult {
	request := inputs.NewNetworkRequest(*input, op.Connection.DB(), op.Repos.Network)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "network.default_set_failed",
			Message:   fmt.Sprintf("Failed to resolve network: %v", err),
			Exception: err,
		}
	}
	if len(resolved.Networks) != 1 {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "network.default_set_failed",
			Message: fmt.Sprintf("Expected exactly one network, got %d", len(resolved.Networks)),
		}
	}

	net := resolved.Networks[0]
	controller, err := network.NewController(net, op.Repos.Network)
	if err != nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "network.default_set_failed",
			Message: fmt.Sprintf("Failed to create network controller: %v", err),
		}
	}
	if err := controller.SetDefault(ctx); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "network.default_set_failed",
			Message:   fmt.Sprintf("Failed to set network '%s' as default: %v", net.Name, err),
			Exception: err,
		}
	}

	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("network.set_default", map[string]interface{}{"name": net.Name}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "network.default_set",
		Item:    net,
		Message: fmt.Sprintf("Network '%s' set as default", net.Name),
	}
}

// NetworkSync syncs firewall rules for a network.
// Matches Python's NetworkOperation.sync() exactly.
func (op *Operation) NetworkSync(ctx context.Context, networkID string) *errs.OperationResult {
	var networks []*model.Network
	var err error

	if networkID != "" {
		net, err2 := op.Repos.Network.Get(ctx, networkID)
		if err2 != nil || net == nil {
			return &errs.OperationResult{
				Status:  "error",
				Code:    string(errs.CodeNetworkNotFound),
				Message: fmt.Sprintf("Network '%s' not found", networkID),
			}
		}
		networks = []*model.Network{net}
	} else {
		networks, err = op.Repos.Network.ListAll(ctx)
		if err != nil {
			return &errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeDatabaseError),
				Message:   fmt.Sprintf("Failed to list networks: %v", err),
				Exception: err,
			}
		}
	}

	bridgesReconciled := 0
	orphanedBridgesRemoved := 0
	results := make(map[string]map[string]int)

	// Wrap the core sync logic in a NetworkError catch (matches Python's try/except NetworkError)
	syncErr := func() error {
		// Step 1: Restore missing bridges (post-reboot recovery)
		for _, net := range networks {
			if !infranet.BridgeExists(net.Bridge) {
				bridgeAddr, calcErr := network.ComputeBridgeAddress(net.IPv4Gateway, net.Subnet)
				if calcErr != nil {
					return fmt.Errorf("compute bridge address: %w", calcErr)
				}
				if err := op.Services.Network.EnsureBridge(ctx, net.Bridge, bridgeAddr); err != nil {
					return fmt.Errorf("ensure bridge: %w", err)
				}
				if net.NATEnabled {
					if err := op.Services.Network.EnsureNAT(ctx, net.Bridge, network.NatGatewaysList(net), net.Subnet, net.ID); err != nil {
						return fmt.Errorf("ensure NAT: %w", err)
					}
				}
			}
		}

		// Step 2: Reconcile bridge state (DB vs kernel)
		for _, net := range networks {
			bridgeActive := infranet.BridgeExists(net.Bridge)
			if bridgeActive != net.BridgeActive {
				_ = op.Repos.Network.UpdateBridgeActive(ctx, net.ID, bridgeActive)
				bridgesReconciled++
			}
		}

		// Step 3: Sync firewall rules
		for _, net := range networks {
			r, err := op.Services.Network.SyncIPTablesRules(ctx, net)
			if err != nil {
				return fmt.Errorf("sync rules for network '%s': %w", net.Name, err)
			}
			result := map[string]int{"added": 0, "verified": 0, "orphaned": 0}
			if r != nil {
				result["added"] = r.Added
				result["verified"] = r.Verified
				result["orphaned"] = r.Orphaned
			}
			results[net.ID] = result
		}

		// Step 4: Clean up orphaned bridges (matches Python's service.cleanup_orphaned_bridges())
		orphanedBridgesRemoved = op.Services.Network.CleanupOrphanedBridges(networks)
		return nil
	}()

	if syncErr != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeNetworkBridgeFailed),
			Message:   fmt.Sprintf("Network sync failed: %v", syncErr),
			Exception: syncErr,
		}
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "network.synced",
		Item:    results,
		Message: "Network synced",
		Metadata: map[string]interface{}{
			"network_count":            len(results),
			"bridges_reconciled":       bridgesReconciled,
			"orphaned_bridges_removed": orphanedBridgesRemoved,
		},
	}
}

// NetworkPrune prunes unused networks.
// Matches Python's NetworkOperation.prune() exactly.
func (op *Operation) NetworkPrune(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	// Python: HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune networks")
	if err := system.CheckPrivileges("/usr/sbin/ip", "prune networks"); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodePrivilegeRequired),
			Message:   err.Error(),
			Exception: err,
		}
	}

	networks, err := op.Repos.Network.ListAll(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeDatabaseError),
			Message:   fmt.Sprintf("Failed to list networks: %v", err),
			Exception: err,
		}
	}

	// Get referenced network IDs from VMs
	allVMs, _ := op.Repos.VM.ListAll(ctx)
	referencedIDs := make(map[string]bool)
	for _, vm := range allVMs {
		if vm.NetworkID != "" {
			referencedIDs[vm.NetworkID] = true
		}
	}

	defaultNetNameRaw, _ := op.ConfigGet(ctx, "defaults.network", "name")
	defaultNetName := "net"
	if s, ok := defaultNetNameRaw.(string); ok {
		defaultNetName = s
	}

	var removed []string
	for _, network := range networks {
		if !includeAll {
			if network.Name == defaultNetName {
				continue
			}
			if referencedIDs[network.ID] {
				continue
			}
			leases, _ := op.Repos.Lease.ListAll(ctx, network.ID)
			if len(leases) > 0 {
				continue
			}
		}

		if !dryRun {
			if !network.IsPresent {
				_ = op.Repos.Network.Delete(ctx, network.ID)
			} else {
				result := op.NetworkRemove(ctx, &inputs.NetworkInput{Name: []string{network.Name}}, includeAll)
				if result.IsError() {
					slog.Warn("Failed to remove network", "name", network.Name, "error", result.Message)
					continue
				}
			}
		}
		removed = append(removed, network.Name)
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "cache.pruned",
		Message: fmt.Sprintf("Pruned %d network(s)", len(removed)),
		Item:    removed,
	}
}

// NetworkCreateDefaultNetwork creates the default network if it doesn't exist.
// Matches Python's NetworkOperation.create_default_network() exactly.
// Updates Repository component tracking after creation.
func (op *Operation) NetworkCreateDefaultNetwork(ctx context.Context) *errs.OperationResult {
	defaultNameRaw, _ := op.ConfigGet(ctx, "defaults.network", "name")
	defaultName := "net"
	if s, ok := defaultNameRaw.(string); ok {
		defaultName = s
	}

	defaultSubnetRaw, _ := op.ConfigGet(ctx, "defaults.network", "subnet")
	defaultSubnet := "172.27.0.0/24"
	if s, ok := defaultSubnetRaw.(string); ok {
		defaultSubnet = s
	}

	defaultNATEnabledRaw, _ := op.ConfigGet(ctx, "defaults.network", "nat_enabled")
	defaultNATEnabled := true
	if b, ok := defaultNATEnabledRaw.(bool); ok {
		defaultNATEnabled = b
	}

	// Check existing
	internalNetwork, _ := op.Repos.Network.GetByName(ctx, defaultName)
	if internalNetwork == nil {
		outboundIf := infranet.DetectOutboundInterface()
		var natGateways []string
		if outboundIf != "" {
			natGateways = []string{outboundIf}
		}

		createInput := &inputs.NetworkCreateInput{
			Name:        defaultName,
			Subnet:      defaultSubnet,
			NATEnabled:  defaultNATEnabled && len(natGateways) > 0,
			NATGateways: natGateways,
		}
		createResult := op.NetworkCreate(ctx, createInput)
		// NeedsInteraction is not expected during default network creation
		// (defensive, matching Python: isinstance(create_result, NeedsInteraction))
		if createResult.Exception != nil && errs.IsNeedsInteraction(createResult.Exception) {
			return &errs.OperationResult{
				Status:  "error",
				Code:    "network.default_created_failed",
				Message: createResult.Exception.Error(),
			}
		}
		if createResult.IsError() {
			return createResult
		}
		internalNetwork, _ = op.Repos.Network.GetByName(ctx, defaultName)
		if internalNetwork != nil && op.Repos.Host != nil {
			_ = op.Repos.Host.UpdateComponent(ctx, "default_network_created", true)
		}
	}

	// Ensure one network is default
	defaultNetwork, _ := op.Repos.Network.GetDefault(ctx)
	if defaultNetwork == nil && internalNetwork != nil {
		_ = op.Repos.Network.SetDefault(ctx, internalNetwork.ID)
		defaultNetwork = internalNetwork
	}

	if defaultNetwork == nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "network.default_created_failed",
			Message: "Failed to create or locate default network",
		}
	}

	// Materialize bridge and NAT
	bridgeAddr, calcErr := network.ComputeBridgeAddress(defaultNetwork.IPv4Gateway, defaultNetwork.Subnet)
	if calcErr != nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "network.default_created_failed",
			Message: fmt.Sprintf("Failed to compute bridge address: %v", calcErr),
		}
	}
	_ = op.Services.Network.EnsureBridge(ctx, defaultNetwork.Bridge, bridgeAddr)
	if defaultNetwork.NATEnabled {
		_ = op.Services.Network.EnsureNAT(ctx, defaultNetwork.Bridge, network.NatGatewaysList(defaultNetwork), defaultNetwork.Subnet, defaultNetwork.ID)
	}

	bridgeActive := infranet.BridgeExists(defaultNetwork.Bridge)
	_ = op.Repos.Network.UpdateBridgeActive(ctx, defaultNetwork.ID, bridgeActive)

	return &errs.OperationResult{
		Status:  "success",
		Code:    "network.default_created",
		Item:    defaultNetwork,
		Message: fmt.Sprintf("Default network '%s' ready", defaultNetwork.Name),
	}
}

func (op *Operation) networkEnrichWithLeases(ctx context.Context, networks []*model.Network) error {
	ids := make([]string, len(networks))
	for i, n := range networks {
		ids[i] = n.ID
	}
	leases, err := op.Repos.Lease.ListAllBatch(ctx, ids)
	if err != nil {
		return errs.Wrap(errs.CodeDatabaseError, fmt.Errorf("batch load leases: %w", err))
	}
	leaseMap := make(map[string][]*model.NetworkLeaseItem)
	for _, lease := range leases {
		leaseMap[lease.NetworkID] = append(leaseMap[lease.NetworkID], lease)
	}
	for _, n := range networks {
		n.Leases = leaseMap[n.ID]
		if n.Leases == nil {
			n.Leases = []*model.NetworkLeaseItem{}
		}
	}
	return nil
}
