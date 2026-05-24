// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/network_operations.py exactly.
package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"mvmctl/internal/core/host"
	"mvmctl/internal/core/network"
	"mvmctl/internal/core/vm"
	"mvmctl/internal/enricher"
	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api/inputs"
)

// NetworkOperation orchestrates network management.
// Matches Python's NetworkOperation exactly.
type NetworkOperation struct {
	svc       *network.Service
	repo      network.Repository
	leaseRepo network.LeaseRepository
	vmRepo    vm.Repository
	enr       *enricher.Enricher
	configOp  *ConfigOperation
	hostRepo  host.Repository
	cacheDir  string
	db        *sql.DB
}

// NewNetworkOperation creates a NetworkOperation.
func NewNetworkOperation(
	svc *network.Service,
	repo network.Repository,
	leaseRepo network.LeaseRepository,
	vmRepo vm.Repository,
	enr *enricher.Enricher,
	configOp *ConfigOperation,
	hostRepo host.Repository,
	cacheDir string,
	db *sql.DB,
) *NetworkOperation {
	return &NetworkOperation{
		svc:       svc,
		repo:      repo,
		leaseRepo: leaseRepo,
		vmRepo:    vmRepo,
		enr:       enr,
		configOp:  configOp,
		hostRepo:  hostRepo,
		cacheDir:  cacheDir,
		db:        db,
	}
}

// Create creates a new network.
// Matches Python's NetworkOperation.create() exactly.
func (o *NetworkOperation) Create(ctx context.Context, input *inputs.NetworkCreateInput) *errs.OperationResult {
	request := inputs.NewNetworkCreateRequest(*input, o.db, o.repo)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "network.create_failed",
			Message:   err.Error(),
			Exception: err,
		}
	}

	createdAt := time.Now().UTC().Format(time.RFC3339)
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

	if err := o.repo.Upsert(ctx, networkItem); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeDatabaseError),
			Message:   "Failed to persist network: " + err.Error(),
			Exception: err,
		}
	}

	bridgeAddr, bridgeErr := network.ComputeBridgeAddress(resolved.IPv4Gateway, resolved.Subnet)
	if bridgeErr != nil {
		_ = o.repo.Delete(ctx, networkID)
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeNetworkBridgeFailed),
			Message:   fmt.Sprintf("Failed to compute bridge address: %v", bridgeErr),
			Exception: bridgeErr,
		}
	}
	if err := o.svc.EnsureBridge(ctx, resolved.Bridge, bridgeAddr); err != nil {
		_ = o.repo.Delete(ctx, networkID)
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeNetworkBridgeFailed),
			Message:   fmt.Sprintf("Failed to create network '%s': %v", resolved.Name, err),
			Exception: err,
		}
	}

	if resolved.NATEnabled {
		if err := o.svc.EnsureNAT(ctx, resolved.Bridge, resolved.NATGateways, resolved.Subnet, networkID); err != nil {
			_ = o.repo.Delete(ctx, networkID)
			return &errs.OperationResult{
				Status:    "error",
				Code:      string(errs.CodeNetworkNATFailed),
				Message:   fmt.Sprintf("Failed to create network '%s': %v", resolved.Name, err),
				Exception: err,
			}
		}
	}

	// Update bridge_active
	bridgeActive := infra.BridgeExists(resolved.Bridge)
	_ = o.repo.UpdateBridgeActive(ctx, networkID, bridgeActive)

	// Re-fetch
	updated, err := o.repo.GetByName(ctx, resolved.Name)
	if err != nil || updated == nil {
		return &errs.OperationResult{
			Status:  "error",
			Code:    string(errs.CodeNetworkNotFound),
			Message: fmt.Sprintf("Failed to fetch created network '%s'", resolved.Name),
		}
	}

	if input.SetDefault {
		if err := o.repo.SetDefault(ctx, updated.ID); err != nil {
			slog.Warn("Failed to set network as default", "name", input.Name, "error", err)
		}
	}

	// Audit log
	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("network.create", map[string]interface{}{"name": resolved.Name}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "network.created",
		Item:    updated,
		Message: fmt.Sprintf("Network '%s' created", resolved.Name),
	}
}

// Remove removes one or more networks.
// Matches Python's NetworkOperation.remove() exactly — uses NetworkRequest for resolution,
// enriches with VM references, checks "in use".
func (o *NetworkOperation) Remove(ctx context.Context, input *inputs.NetworkInput, force bool) *errs.OperationResult {
	request := inputs.NewNetworkRequest(*input, o.db, o.repo)
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
	if o.enr != nil {
		_ = o.enr.EnrichNetwork(ctx, resolved.Networks, "vm")
	}

	// Match Python: service.remove(network, force=force) raises NetworkError on failure.
	// Python catches the first error and returns it immediately — we match by iterating
	// once and returning the first error encountered.
	results := make([]string, 0)
	for _, net := range resolved.Networks {
		if err := o.svc.Remove(ctx, net, force); err != nil {
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

		auditLog := infra.NewAuditLog(o.cacheDir)
		_ = auditLog.LogOperation("network.remove", map[string]interface{}{"id": net.ID, "name": net.Name}, "")
		results = append(results, net.Name)
	}

	return &errs.OperationResult{
		Status:  "success",
		Code:    "network.removed",
		Message: fmt.Sprintf("Network(s) '%s' removed", strings.Join(results, ", ")),
	}
}

// ListAll returns all networks with lease enrichment.
// Matches Python's NetworkOperation.list_all() exactly.
func (o *NetworkOperation) ListAll(ctx context.Context) ([]*model.Network, error) {
	networks, err := o.svc.ListAll(ctx, true)
	if err != nil {
		return nil, err
	}
	if len(networks) > 0 {
		_ = o.enrichWithLeases(ctx, networks)
	}
	return networks, nil
}

// Get returns a single network by Input/Request resolution pipeline.
// Matches Python's NetworkOperation.get() exactly — uses NetworkInput/NetworkRequest
// to resolve identifiers (by name or ID) and supports multi-identifier resolution.
func (o *NetworkOperation) Get(ctx context.Context, input *inputs.NetworkInput) (*model.Network, error) {
	request := inputs.NewNetworkRequest(*input, o.db, o.repo)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("network not found: %v", err)
	}
	if len(resolved.Networks) != 1 {
		return nil, fmt.Errorf("expected exactly one network, got %d", len(resolved.Networks))
	}
	return resolved.Networks[0], nil
}

// ToJSON converts networks to JSON-serializable dicts.
// Matches Python's NetworkOperation.to_json() exactly — delegates to model's to_dict().
func (o *NetworkOperation) ToJSON(networks []*model.Network) []map[string]interface{} {
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

// Inspect returns detailed network info via Input/Request resolution pipeline.
// Matches Python's NetworkOperation.inspect() exactly — uses NetworkInput/NetworkRequest
// to resolve identifiers (by name or ID) with lease enrichment.
func (o *NetworkOperation) Inspect(ctx context.Context, input *inputs.NetworkInput) (map[string]interface{}, error) {
	request := inputs.NewNetworkRequest(*input, o.db, o.repo)
	resolved, err := request.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("network not found: %v", err)
	}
	if len(resolved.Networks) != 1 {
		return nil, fmt.Errorf("expected exactly one network, got %d", len(resolved.Networks))
	}

	net := resolved.Networks[0]

	bridgeActive := infra.BridgeExists(net.Bridge)
	if bridgeActive != net.BridgeActive {
		_ = o.repo.UpdateBridgeActive(ctx, net.ID, bridgeActive)
		net.BridgeActive = bridgeActive
	}

	// Re-fetch with updated state (matching Python)
	updated, err := o.repo.GetByName(ctx, net.Name)
	if err != nil || updated == nil {
		return nil, fmt.Errorf("network '%s' not found after update", net.Name)
	}

	// Load leases — Python always includes ALL keys (with None for missing values).
	leaseList := make([]map[string]interface{}, 0)
	leases, err := o.leaseRepo.ListAll(ctx, updated.ID)
	if err == nil {
		for _, lease := range leases {
			var leaseID interface{} = nil
			if lease.ID != nil {
				leaseID = *lease.ID
			}
			var vmID interface{} = nil
			if lease.VMID != nil {
				vmID = *lease.VMID
			}
			var expiresAt interface{} = nil
			if lease.ExpiresAt != nil {
				expiresAt = *lease.ExpiresAt
			}
			entry := map[string]interface{}{
				"id":         leaseID,
				"vm_id":      vmID,
				"ipv4":       lease.IPv4,
				"leased_at":  lease.LeasedAt,
				"expires_at": expiresAt,
			}
			leaseList = append(leaseList, entry)
		}
	}

	return map[string]interface{}{
		"network": map[string]interface{}{
			"id":           updated.ID,
			"name":         updated.Name,
			"subnet":       updated.Subnet,
			"bridge":       updated.Bridge,
			"ipv4_gateway": updated.IPv4Gateway,
			"is_default":   updated.IsDefault,
			"is_present":   updated.IsPresent,
			"created_at":   updated.CreatedAt,
			"updated_at":   updated.UpdatedAt,
		},
		"status": map[string]interface{}{
			"bridge_active": updated.BridgeActive,
			"is_present":    updated.IsPresent,
			"is_default":    updated.IsDefault,
		},
		"nat": map[string]interface{}{
			"nat_enabled":  updated.NATEnabled,
			"nat_gateways": network.NatGatewaysList(updated),
		},
		"leases": leaseList,
	}, nil
}

// SetDefault sets a network as default.
// Matches Python's NetworkOperation.set_default() exactly — goes through Controller
// and uses NetworkInput/NetworkRequest to resolve identifiers.
func (o *NetworkOperation) SetDefault(ctx context.Context, input *inputs.NetworkInput) *errs.OperationResult {
	request := inputs.NewNetworkRequest(*input, o.db, o.repo)
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
	controller, err := network.NewController(net, o.repo)
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

	auditLog := infra.NewAuditLog(o.cacheDir)
	_ = auditLog.LogOperation("network.set_default", map[string]interface{}{"name": net.Name}, "")

	return &errs.OperationResult{
		Status:  "success",
		Code:    "network.default_set",
		Item:    net,
		Message: fmt.Sprintf("Network '%s' set as default", net.Name),
	}
}

// Sync syncs firewall rules for a network.
// Matches Python's NetworkOperation.sync() exactly.
func (o *NetworkOperation) Sync(ctx context.Context, networkID string) *errs.OperationResult {
	var networks []*model.Network
	var err error

	if networkID != "" {
		net, err2 := o.repo.Get(ctx, networkID)
		if err2 != nil || net == nil {
			return &errs.OperationResult{
				Status:  "error",
				Code:    string(errs.CodeNetworkNotFound),
				Message: fmt.Sprintf("Network '%s' not found", networkID),
			}
		}
		networks = []*model.Network{net}
	} else {
		networks, err = o.repo.ListAll(ctx)
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
			if !infra.BridgeExists(net.Bridge) {
				bridgeAddr, calcErr := network.ComputeBridgeAddress(net.IPv4Gateway, net.Subnet)
				if calcErr != nil {
					return fmt.Errorf("compute bridge address: %w", calcErr)
				}
				if err := o.svc.EnsureBridge(ctx, net.Bridge, bridgeAddr); err != nil {
					return fmt.Errorf("ensure bridge: %w", err)
				}
				if net.NATEnabled {
					if err := o.svc.EnsureNAT(ctx, net.Bridge, network.NatGatewaysList(net), net.Subnet, net.ID); err != nil {
						return fmt.Errorf("ensure NAT: %w", err)
					}
				}
			}
		}

		// Step 2: Reconcile bridge state (DB vs kernel)
		for _, net := range networks {
			bridgeActive := infra.BridgeExists(net.Bridge)
			if bridgeActive != net.BridgeActive {
				_ = o.repo.UpdateBridgeActive(ctx, net.ID, bridgeActive)
				bridgesReconciled++
			}
		}

		// Step 3: Sync firewall rules
		for _, net := range networks {
			r, err := o.svc.SyncIPTablesRules(ctx, net)
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
		orphanedBridgesRemoved = o.svc.CleanupOrphanedBridges(networks)
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

// Prune prunes unused networks.
// Matches Python's NetworkOperation.prune() exactly.
func (o *NetworkOperation) Prune(ctx context.Context, dryRun bool, includeAll bool) *errs.OperationResult {
	// Python: HostPrivilegeHelper.check_privileges("/usr/sbin/ip", "prune networks")
	privHelper := host.NewPrivilegeHelper()
	if err := privHelper.CheckPrivileges("/usr/sbin/ip", "prune networks"); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodePrivilegeRequired),
			Message:   err.Error(),
			Exception: err,
		}
	}

	networks, err := o.repo.ListAll(ctx)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      string(errs.CodeDatabaseError),
			Message:   fmt.Sprintf("Failed to list networks: %v", err),
			Exception: err,
		}
	}

	// Get referenced network IDs from VMs
	allVMs, _ := o.vmRepo.ListAll(ctx)
	referencedIDs := make(map[string]bool)
	for _, vm := range allVMs {
		if vm.NetworkID != "" {
			referencedIDs[vm.NetworkID] = true
		}
	}

	defaultNetNameRaw, _ := o.configOp.Get(ctx, "defaults.network", "name")
	defaultNetName := "net"
	if s, ok := defaultNetNameRaw.(string); ok {
		defaultNetName = s
	}

	removed := make([]string, 0)
	for _, network := range networks {
		if !includeAll {
			if network.Name == defaultNetName {
				continue
			}
			if referencedIDs[network.ID] {
				continue
			}
			leases, _ := o.leaseRepo.ListAll(ctx, network.ID)
			if len(leases) > 0 {
				continue
			}
		}

		if !dryRun {
			if !network.IsPresent {
				_ = o.repo.Delete(ctx, network.ID)
			} else {
				result := o.Remove(ctx, &inputs.NetworkInput{Name: []string{network.Name}}, includeAll)
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

// CreateDefaultNetwork creates the default network if it doesn't exist.
// Matches Python's NetworkOperation.create_default_network() exactly.
// Updates Repository component tracking after creation.
func (o *NetworkOperation) CreateDefaultNetwork(ctx context.Context) *errs.OperationResult {
	defaultNameRaw, _ := o.configOp.Get(ctx, "defaults.network", "name")
	defaultName := "net"
	if s, ok := defaultNameRaw.(string); ok {
		defaultName = s
	}

	defaultSubnetRaw, _ := o.configOp.Get(ctx, "defaults.network", "subnet")
	defaultSubnet := "172.27.0.0/24"
	if s, ok := defaultSubnetRaw.(string); ok {
		defaultSubnet = s
	}

	defaultNATEnabledRaw, _ := o.configOp.Get(ctx, "defaults.network", "nat_enabled")
	defaultNATEnabled := true
	if b, ok := defaultNATEnabledRaw.(bool); ok {
		defaultNATEnabled = b
	}

	// Check existing
	internalNetwork, _ := o.repo.GetByName(ctx, defaultName)
	if internalNetwork == nil {
		outboundIf := infra.DetectOutboundInterface()
		natGateways := make([]string, 0)
		if outboundIf != "" {
			natGateways = []string{outboundIf}
		}

		createInput := &inputs.NetworkCreateInput{
			Name:        defaultName,
			Subnet:      defaultSubnet,
			NATEnabled:  defaultNATEnabled && len(natGateways) > 0,
			NATGateways: natGateways,
		}
		createResult := o.Create(ctx, createInput)
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
		internalNetwork, _ = o.repo.GetByName(ctx, defaultName)
		if internalNetwork != nil && o.hostRepo != nil {
			_ = o.hostRepo.UpdateComponent(ctx, "default_network_created", true)
		}
	}

	// Ensure one network is default
	defaultNetwork, _ := o.repo.GetDefault(ctx)
	if defaultNetwork == nil && internalNetwork != nil {
		_ = o.repo.SetDefault(ctx, internalNetwork.ID)
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
	_ = o.svc.EnsureBridge(ctx, defaultNetwork.Bridge, bridgeAddr)
	if defaultNetwork.NATEnabled {
		_ = o.svc.EnsureNAT(ctx, defaultNetwork.Bridge, network.NatGatewaysList(defaultNetwork), defaultNetwork.Subnet, defaultNetwork.ID)
	}

	bridgeActive := infra.BridgeExists(defaultNetwork.Bridge)
	_ = o.repo.UpdateBridgeActive(ctx, defaultNetwork.ID, bridgeActive)

	return &errs.OperationResult{
		Status:  "success",
		Code:    "network.default_created",
		Item:    defaultNetwork,
		Message: fmt.Sprintf("Default network '%s' ready", defaultNetwork.Name),
	}
}

func (o *NetworkOperation) enrichWithLeases(ctx context.Context, networks []*model.Network) error {
	ids := make([]string, len(networks))
	for i, n := range networks {
		ids[i] = n.ID
	}
	leases, err := o.leaseRepo.ListAllBatch(ctx, ids)
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

func subnetsOverlap(a, b string) bool {
	_, anet, err := net.ParseCIDR(a)
	if err != nil {
		return false
	}
	_, bnet, err := net.ParseCIDR(b)
	if err != nil {
		return false
	}
	return anet.Contains(bnet.IP) || bnet.Contains(anet.IP)
}
