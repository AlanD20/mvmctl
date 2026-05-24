package network

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/firewall"
	"mvmctl/internal/infra/system"
)

// Service manages network interfaces, bridges, TAP devices, and NAT/firewall rules.
// Matches src/mvmctl/core/network/_service.py: Service
type Service struct {
	repo            Repository
	firewallTracker *firewall.FirewallTracker
}

// NewService creates a Service. The db parameter is used to create
// the firewall tracker internally (matching Python's FirewallTracker(repo.db)).
// If db is nil, firewall operations are skipped (used for cleanup contexts).
func NewService(repo Repository, db *sql.DB) *Service {
	var tracker *firewall.FirewallTracker
	if db != nil {
		t, err := firewall.NewFirewallTracker(db)
		if err == nil {
			tracker = t
		}
	}
	return &Service{repo: repo, firewallTracker: tracker}
}

// ── Batch context ──

// SyncResult holds the result of a SyncIPTablesRules operation.
// Matches the dict returned by Python's sync_iptables_rules().
type SyncResult struct {
	Added    int // Rules added to host iptables
	Verified int // Rules already present in host iptables
	Orphaned int // Host rules not tracked in DB
}

// WithBatch runs a function inside a firewall batch context, flushing
// all queued rule operations atomically on return. This matches Python's:
// with self._tracker.batch():
//     ...
func (s *Service) WithBatch(fn func()) {
	if s.firewallTracker == nil {
		fn()
		return
	}
	batch := s.firewallTracker.Batch()
	defer batch.Close()
	fn()
}

// ── List ──

func (s *Service) ListAll(ctx context.Context, verify bool) ([]*Network, error) {
	networks, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if !verify {
		return networks, nil
	}

	var missingIDs []string
	for _, network := range networks {
		if !bridgeExists(network.Bridge) {
			missingIDs = append(missingIDs, network.ID)
		}
	}

	if len(missingIDs) > 0 {
		if err := s.repo.UpdateManyIsPresent(ctx, missingIDs, false); err != nil {
			return nil, err
		}
		return s.repo.ListAll(ctx)
	}
	return networks, nil
}

// ── Firewall chain management ──

func (s *Service) EnsureMVMChains(ctx context.Context) error {
	if s.firewallTracker != nil {
		s.firewallTracker.Initialize()
	}
	return nil
}

func (s *Service) Initialize(ctx context.Context) error {
	if s.firewallTracker != nil {
		s.firewallTracker.Initialize()
	}
	return nil
}

func (s *Service) Teardown(ctx context.Context) error {
	if s.firewallTracker == nil {
		return nil
	}
	s.firewallTracker.Teardown()
	return nil
}

// detect_iptables_backend_conflict detects mixed iptables backend conflict.
// Matches Python NetworkUtils.detect_iptables_backend_conflict().
func (s *Service) DetectIPTablesBackendConflict() (bool, string) {
	// Check current iptables backend version
	result := system.RunCmdCompat(context.Background(), []string{"iptables", "--version"}, system.RunCmdOpts{Capture: true, Check: false})
	currentBackend := "legacy"
	if result != nil && strings.Contains(result.Stderr, "nf_tables") {
		currentBackend = "nft"
	}

	legacyActive := false
	func() {
		legacyResult := system.RunCmdCompat(context.Background(), []string{"iptables-legacy", "-L", "-n", "-v"}, system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		if legacyResult != nil && legacyResult.Success {
			for _, line := range strings.Split(legacyResult.Stdout, "\n") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					var pkts int
					if _, err := fmt.Sscanf(parts[0], "%d", &pkts); err == nil && pkts > 0 {
						legacyActive = true
						break
					}
				}
			}
		}
	}()

	nftActive := false
	func() {
		nftResult := system.RunCmdCompat(context.Background(), []string{"iptables", "-L", "-n", "-v"}, system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		if nftResult != nil && nftResult.Success {
			for _, line := range strings.Split(nftResult.Stdout, "\n") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					var pkts int
					if _, err := fmt.Sscanf(parts[0], "%d", &pkts); err == nil && pkts > 0 {
						nftActive = true
						break
					}
				}
			}
		}
	}()

	hasConflict := legacyActive && nftActive
	diagnosis := fmt.Sprintf("iptables backend: %s, legacy active: %t, nft active: %t", currentBackend, legacyActive, nftActive)
	return hasConflict, diagnosis
}

// ── IP forwarding ──

func (s *Service) EnsureIPForwarding(ctx context.Context) error {
	return ensureIPForwarding()
}

// ── Bridge management ──

func (s *Service) EnsureBridge(ctx context.Context, bridge, bridgeAddress string) error {
	if bridgeExists(bridge) {
		slog.Debug("Bridge already exists, reconciling state", "bridge", bridge)
		reconcileCmds := make([]string, 0)
		if !bridgeHasSubnet(bridge, bridgeAddress) {
			reconcileCmds = append(reconcileCmds, fmt.Sprintf("addr add %s dev %s", bridgeAddress, bridge))
		}
		reconcileCmds = append(reconcileCmds, fmt.Sprintf("link set %s up", bridge))
		if err := runBatch(ctx, reconcileCmds); err != nil {
			return errs.WrapMsg(errs.CodeNetworkBridgeFailed,
				fmt.Sprintf("Failed to setup bridge %s", bridge), err)
		}
	} else {
		if err := runBatch(ctx, []string{
			fmt.Sprintf("link add name %s type bridge", bridge),
			fmt.Sprintf("addr add %s dev %s", bridgeAddress, bridge),
			fmt.Sprintf("link set %s up", bridge),
		}); err != nil {
			return errs.WrapMsg(errs.CodeNetworkBridgeFailed,
				fmt.Sprintf("Failed to setup bridge %s", bridge), err)
		}
	}

	// ip forwarding has to be enabled
	if err := s.EnsureIPForwarding(ctx); err != nil {
		return err
	}

	slog.Info("Bridge created with address", "bridge", bridge, "address", bridgeAddress)
	return nil
}

func (s *Service) RemoveBridge(ctx context.Context, bridge string, networkID string) error {
	attachedTaps := getBridgeTaps(bridge)
	for _, tap := range attachedTaps {
		slog.Debug("Removing attached TAP from bridge", "tap", tap, "bridge", bridge)
		s.RemoveTap(ctx, tap, bridge, networkID)
	}
	if err := removeRawBridge(bridge); err != nil {
		return errs.WrapMsg(errs.CodeNetworkBridgeFailed,
			fmt.Sprintf("Failed to teardown bridge %s", bridge), err)
	}
	slog.Info("Bridge removed", "bridge", bridge)
	return nil
}

// ── NAT ──

func (s *Service) EnsureNAT(ctx context.Context, bridge string, natGateways []string, subnet string, networkID string) error {
	// Initialize firewall chains
	s.Initialize(ctx)

	for _, gwIface := range natGateways {
		masqRule := &FirewallRule{
			TableName:    FirewallTableNat,
			ChainName:    FirewallChainMVMPostrouting,
			RuleType:     FirewallRuleMasquerade,
			Target:       FirewallTargetMasquerade,
			NetworkID:    networkID,
			Protocol:     FirewallProtocolAll,
			Source:       subnet,
			Destination:  string(FirewallWildcardAnyCIDR),
			InInterface:  string(FirewallWildcardAnyInterface),
			OutInterface: gwIface,
			SPort:        int(FirewallPortAny),
			DPort:        int(FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		}
		fwdOutRule := &FirewallRule{
			TableName:    FirewallTableFilter,
			ChainName:    FirewallChainMVMForward,
			RuleType:     FirewallRuleForwardOut,
			Target:       FirewallTargetAccept,
			NetworkID:    networkID,
			Protocol:     FirewallProtocolAll,
			Source:       subnet,
			Destination:  string(FirewallWildcardAnyCIDR),
			InInterface:  bridge,
			OutInterface: gwIface,
			SPort:        int(FirewallPortAny),
			DPort:        int(FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		}
		fwdInRule := &FirewallRule{
			TableName:    FirewallTableFilter,
			ChainName:    FirewallChainMVMForward,
			RuleType:     FirewallRuleForwardIn,
			Target:       FirewallTargetAccept,
			NetworkID:    networkID,
			Protocol:     FirewallProtocolAll,
			Source:       string(FirewallWildcardAnyCIDR),
			Destination:  subnet,
			InInterface:  gwIface,
			OutInterface: bridge,
			SPort:        int(FirewallPortAny),
			DPort:        int(FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		}

		if s.firewallTracker != nil {
			context := fmt.Sprintf("%s:%s", bridge, gwIface)
			result := s.firewallTracker.EnsureRule(toFWRule(masqRule), context)
			if !result.Success {
				errMsg := errorMessageString(result.ErrorMessage)
				return errs.Wrap(errs.CodeNetworkNATFailed,
					fmt.Errorf("Failed to add MASQUERADE rule for %s via %s: %s", bridge, gwIface, errMsg))
			}
			result = s.firewallTracker.EnsureRule(toFWRule(fwdOutRule), context)
			if !result.Success {
				errMsg := errorMessageString(result.ErrorMessage)
				return errs.Wrap(errs.CodeNetworkNATFailed,
					fmt.Errorf("Failed to add FORWARD out rule for %s via %s: %s", bridge, gwIface, errMsg))
			}
			result = s.firewallTracker.EnsureRule(toFWRule(fwdInRule), context)
			if !result.Success {
				errMsg := errorMessageString(result.ErrorMessage)
				return errs.Wrap(errs.CodeNetworkNATFailed,
					fmt.Errorf("Failed to add FORWARD in rule for %s via %s: %s", bridge, gwIface, errMsg))
			}
		}
	}

	if err := s.EnsureIPForwarding(ctx); err != nil {
		return err
	}

	slog.Info("NAT rules configured for bridge",
		"bridge", bridge,
		"gateways", strings.Join(natGateways, ", "),
		"subnet", subnet)
	return nil
}

func (s *Service) RemoveNAT(ctx context.Context, bridge string, natGateways []string, subnet, networkID string, force bool) error {
	effectiveGateways := natGateways
	effectiveSubnet := subnet

	// Python: tries resolver.by_name(bridge) to resolve missing values, catching ALL exceptions
	// (including DB errors) silently — any error is simply ignored.
	if effectiveGateways == nil || effectiveSubnet == "" {
		network, err := s.repo.GetByName(ctx, bridge)
		if err == nil && network != nil {
			if effectiveSubnet == "" {
				effectiveSubnet = network.Subnet
			}
			if effectiveGateways == nil {
				effectiveGateways = NatGatewaysList(network)
			}
		}
	}

	if effectiveGateways == nil {
		return errs.Wrap(errs.CodeNetworkNATFailed,
			fmt.Errorf("Could not determine NAT gateways for bridge %s. Provide nat_gateways explicitly or ensure network exists in database.", bridge))
	}
	if effectiveSubnet == "" {
		return errs.Wrap(errs.CodeNetworkNATFailed,
			fmt.Errorf("Could not determine subnet for bridge %s. Provide subnet explicitly or ensure network exists in database.", bridge))
	}

	// Check for attached TAPs — matches Python's NetworkError
	attachedTaps := getBridgeTaps(bridge)
	if len(attachedTaps) > 0 {
		if !force {
			return errs.NetworkError(
				fmt.Sprintf("Cannot remove NAT: %d TAP(s) still attached on bridge %s. Use --force to override.", len(attachedTaps), bridge))
		}
		slog.Warn("Removing NAT for bridge but TAPs still attached",
			"bridge", bridge,
			"count", len(attachedTaps),
			"taps", strings.Join(attachedTaps, ", "))
	}

	// Build rules to remove
	var rulesToRemove []FirewallRule
	for _, gwIface := range effectiveGateways {
		rulesToRemove = append(rulesToRemove, FirewallRule{
			TableName:    FirewallTableNat,
			ChainName:    FirewallChainMVMPostrouting,
			RuleType:     FirewallRuleMasquerade,
			Target:       FirewallTargetMasquerade,
			NetworkID:    networkID,
			Protocol:     FirewallProtocolAll,
			Source:       effectiveSubnet,
			Destination:  string(FirewallWildcardAnyCIDR),
			InInterface:  string(FirewallWildcardAnyInterface),
			OutInterface: gwIface,
			SPort:        int(FirewallPortAny),
			DPort:        int(FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		})
		rulesToRemove = append(rulesToRemove, FirewallRule{
			TableName:    FirewallTableFilter,
			ChainName:    FirewallChainMVMForward,
			RuleType:     FirewallRuleForwardOut,
			Target:       FirewallTargetAccept,
			NetworkID:    networkID,
			Protocol:     FirewallProtocolAll,
			Source:       effectiveSubnet,
			Destination:  string(FirewallWildcardAnyCIDR),
			InInterface:  bridge,
			OutInterface: gwIface,
			SPort:        int(FirewallPortAny),
			DPort:        int(FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		})
		rulesToRemove = append(rulesToRemove, FirewallRule{
			TableName:    FirewallTableFilter,
			ChainName:    FirewallChainMVMForward,
			RuleType:     FirewallRuleForwardIn,
			Target:       FirewallTargetAccept,
			NetworkID:    networkID,
			Protocol:     FirewallProtocolAll,
			Source:       string(FirewallWildcardAnyCIDR),
			Destination:  effectiveSubnet,
			InInterface:  gwIface,
			OutInterface: bridge,
			SPort:        int(FirewallPortAny),
			DPort:        int(FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		})
	}

	// Batch remove all rules (non-fatal on failure — matches Python's behavior)
	if s.firewallTracker != nil {
		fwRules := make([]firewall.FirewallRule, len(rulesToRemove))
		for i := range rulesToRemove {
			fwRules[i] = toFWRule(&rulesToRemove[i])
		}
		res := s.firewallTracker.BatchRemoveRules(fwRules)
		if !res.Success {
			msg := errorMessageString(res.ErrorMessage)
			slog.Warn("Failed to remove NAT rules",
				"bridge", bridge,
				"error", msg)
		}
	}

	slog.Info("NAT rules removed for bridge",
		"bridge", bridge,
		"gateways", strings.Join(effectiveGateways, ", "),
		"source", effectiveSubnet)
	return nil
}

// ── TAP management ──

func (s *Service) EnsureTap(ctx context.Context, tap, bridge, networkID, subnet string) error {
	if tapExists(tap) {
		currentBridge := getTapBridge(tap)
		if currentBridge == bridge {
			slog.Debug("TAP device already attached to bridge", "tap", tap, "bridge", bridge)
		} else if currentBridge != "" {
			slog.Warn("TAP device exists but attached to different bridge, reattaching",
				"tap", tap, "current_bridge", currentBridge, "target_bridge", bridge)
			if err := runBatch(ctx, []string{
				fmt.Sprintf("link set %s down", tap),
				fmt.Sprintf("link set %s master %s", tap, bridge),
				fmt.Sprintf("link set %s up", tap),
			}); err != nil {
				return errs.WrapMsg(errs.CodeNetworkBridgeFailed,
					fmt.Sprintf("Failed to reattach TAP %s to bridge %s", tap, bridge), err)
			}
			slog.Info("TAP device reattached to bridge", "tap", tap, "bridge", bridge)
		} else {
			if err := runBatch(ctx, []string{
				fmt.Sprintf("link set %s master %s", tap, bridge),
				fmt.Sprintf("link set %s up", tap),
			}); err != nil {
				return errs.WrapMsg(errs.CodeNetworkBridgeFailed,
					fmt.Sprintf("Failed to attach TAP %s to bridge %s", tap, bridge), err)
			}
			slog.Info("TAP device reattached to bridge", "tap", tap, "bridge", bridge)
		}
	} else {
		if err := runBatch(ctx, []string{
			fmt.Sprintf("tuntap add dev %s mode tap", tap),
			fmt.Sprintf("link set %s master %s", tap, bridge),
			fmt.Sprintf("link set %s up", tap),
		}); err != nil {
			return errs.WrapMsg(errs.CodeNetworkBridgeFailed,
				fmt.Sprintf("Failed to create TAP %s", tap), err)
		}
		slog.Info("TAP device created and attached to bridge", "tap", tap, "bridge", bridge)
	}

	s.Initialize(ctx)

	fwdBridgeToTap := &FirewallRule{
		TableName:    FirewallTableFilter,
		ChainName:    FirewallChainMVMForward,
		RuleType:     FirewallRuleForwardOut,
		Target:       FirewallTargetAccept,
		NetworkID:    networkID,
		Protocol:     FirewallProtocolAll,
		InInterface:  bridge,
		OutInterface: tap,
		SPort:        int(FirewallPortAny),
		DPort:        int(FirewallPortAny),
		IsActive:     true,
		NetworkName:  &bridge,
	}
	fwdTapToBridge := &FirewallRule{
		TableName:    FirewallTableFilter,
		ChainName:    FirewallChainMVMForward,
		RuleType:     FirewallRuleForwardIn,
		Target:       FirewallTargetAccept,
		NetworkID:    networkID,
		Protocol:     FirewallProtocolAll,
		InInterface:  tap,
		OutInterface: bridge,
		SPort:        int(FirewallPortAny),
		DPort:        int(FirewallPortAny),
		IsActive:     true,
		NetworkName:  &bridge,
	}

	if subnet != "" {
		fwdBridgeToTap.Source = subnet
		fwdTapToBridge.Destination = subnet
	} else {
		fwdBridgeToTap.Source = string(FirewallWildcardAnyCIDR)
		fwdTapToBridge.Destination = string(FirewallWildcardAnyCIDR)
	}

	if s.firewallTracker != nil {
		result := s.firewallTracker.EnsureRule(toFWRule(fwdBridgeToTap), fmt.Sprintf("tap:%s", tap))
		if !result.Success {
			errMsg := errorMessageString(result.ErrorMessage)
			return errs.Wrap(errs.CodeNetworkFirewallFailed,
				fmt.Errorf("Failed to add FORWARD rule for bridge %s to TAP %s: %s", bridge, tap, errMsg))
		}
		result = s.firewallTracker.EnsureRule(toFWRule(fwdTapToBridge), fmt.Sprintf("tap:%s", tap))
		if !result.Success {
			s.firewallTracker.RemoveRule(toFWRule(fwdBridgeToTap))
			errMsg := errorMessageString(result.ErrorMessage)
			return errs.Wrap(errs.CodeNetworkFirewallFailed,
				fmt.Errorf("Failed to add FORWARD rule for TAP %s to bridge %s: %s", tap, bridge, errMsg))
		}
	}

	return nil
}

func (s *Service) RemoveTap(ctx context.Context, tap, bridge string, networkID string) error {
	if !tapExists(tap) {
		slog.Debug("TAP device does not exist, skipping removal", "tap", tap)
		return nil
	}

	effectiveBridge := bridge
	if effectiveBridge == "" {
		effectiveBridge = getTapBridge(tap)
	}

	if effectiveBridge != "" && s.firewallTracker != nil {
		repo := s.firewallTracker.Repo()
		if fwRepo, ok := repo.(fwRuleByInterfaceLister); ok {
			dbRules, err := fwRepo.GetByNetworkIDAndInterface(networkID, tap, false)
			if err == nil && len(dbRules) > 0 {
				valRules := make([]firewall.FirewallRule, len(dbRules))
				for i, r := range dbRules {
					valRules[i] = *r
				}
				res := s.firewallTracker.BatchRemoveRules(valRules)
				if !res.Success {
					msg := errorMessageString(res.ErrorMessage)
					slog.Warn("Failed to remove FORWARD rules for TAP",
						"tap", tap,
						"error", msg)
				}
			}
		}
	} else if effectiveBridge == "" {
		slog.Warn("Could not determine bridge for TAP, skipping rule cleanup", "tap", tap)
	}

	if err := removeRawTap(tap); err != nil {
		return err
	}
	slog.Info("TAP device removed", "tap", tap)
	return nil
}

// ── Network removal ──

func (s *Service) Remove(ctx context.Context, network *Network, force bool) error {
	// 1. Tear down NAT — only catch NetworkError, matching Python's behavior
	if network.NATEnabled {
		if err := s.RemoveNAT(ctx, network.Bridge, NatGatewaysList(network), network.Subnet, network.ID, force); err != nil {
			if isNetworkError(err) {
				slog.Debug("NAT teardown", "bridge", network.Bridge, "error", err)
			} else {
				return err // propagate non-network errors
			}
		}
	}

	// 2. Remove bridge — only catch NetworkError
	if err := s.RemoveBridge(ctx, network.Bridge, network.ID); err != nil {
		if isNetworkError(err) {
			slog.Debug("Bridge teardown", "bridge", network.Bridge, "error", err)
		} else {
			return err // propagate non-network errors
		}
	}

	// 3. VM reference check + DB removal
	hasVMs := len(network.VMs) > 0
	if hasVMs && !force {
		vmNames := make([]string, 0, len(network.VMs))
		for _, vm := range network.VMs {
			if vm != nil {
				vmNames = append(vmNames, vm.Name)
			}
		}
		return errs.NetworkError(
			fmt.Sprintf("Network referenced by VMs: %s", strings.Join(vmNames, ", ")))
	}

	if hasVMs {
		return s.repo.SoftDelete(ctx, network.ID)
	}
	return s.repo.Delete(ctx, network.ID)
}

func (s *Service) RemoveMany(ctx context.Context, networks []*Network, force bool) error {
	for _, n := range networks {
		if err := s.Remove(ctx, n, force); err != nil {
			return err
		}
	}
	return nil
}

// ── Sync iptables rules ──
// Matches src/mvmctl/core/network/_service.py: Service.sync_iptables_rules() exactly.

// SyncIPTablesRules ensures all active DB firewall rules exist in host iptables for the given network.
// Returns counts of added, verified, and orphaned rules matching Python's behavior:
//   - added: rules that were created (command_executed was not None)
//   - verified: rules that already existed (command_executed is None)
//   - orphaned: host iptables rules referencing the network but absent from the DB
func (s *Service) SyncIPTablesRules(ctx context.Context, network *Network) (*SyncResult, error) {
	// 1. Get active DB rules for the network through the tracker's repo.
	var dbRules []*firewall.FirewallRule
	if s.firewallTracker != nil {
		repo := s.firewallTracker.Repo()
		fwRepo, ok := repo.(fwRuleLister)
		if !ok {
			return nil, fmt.Errorf("firewall tracker repo does not implement GetByNetworkID")
		}
		var err error
		dbRules, err = fwRepo.GetByNetworkID(network.ID, true)
		if err != nil {
			return nil, err
		}
	}

	added := 0
	verified := 0

	// 2. Use batch mode to queue ensure_rule calls and flush atomically.
	//    Matches Python: with self._tracker.batch():
	//                       for rule in db_rules: self._tracker.ensure_rule(rule)
	//    Python does NOT pass a context parameter to ensure_rule.
	if s.firewallTracker != nil && len(dbRules) > 0 {
		s.WithBatch(func() {
			for _, rule := range dbRules {
				result := s.firewallTracker.EnsureRule(*rule, "")
				if result.Success {
					// Python: if result.command_executed is None → verified, else → added
					if result.CommandExecuted == nil {
						verified++
					} else {
						added++
					}
				}
			}
		})
	}

	// 3. Count orphaned rules
	orphaned := 0
	if s.firewallTracker != nil {
		orphaned = s.firewallTracker.CountOrphanedRules(firewall.NetworkRef{ID: network.ID, Name: network.Name})
	}

	return &SyncResult{
		Added:    added,
		Verified: verified,
		Orphaned: orphaned,
	}, nil
}

// fwRuleLister is a local interface that matches the GetByNetworkID method
// shared by both IPTablesRuleRepository and NFTablesRuleRepository.
type fwRuleLister interface {
	GetByNetworkID(networkID string, activeOnly bool) ([]*firewall.FirewallRule, error)
}

// fwRuleByInterfaceLister extends fwRuleLister with interface-based filtering.
type fwRuleByInterfaceLister interface {
	GetByNetworkID(networkID string, activeOnly bool) ([]*firewall.FirewallRule, error)
	GetByNetworkIDAndInterface(networkID string, iface string, activeOnly bool) ([]*firewall.FirewallRule, error)
}

// ── Orphan cleanup ──
// Matches Python: Service.cleanup_orphaned_bridges(db_networks) exactly.
// Python: @staticmethod cleanup_orphaned_bridges(db_networks: list[NetworkItem]) -> int

func (s *Service) CleanupOrphanedBridges(dbNetworks []*Network) int {
	dbBridgeNames := make(map[string]bool)
	for _, n := range dbNetworks {
		dbBridgeNames[n.Bridge] = true
	}

	hostBridges := getSystemBridges()
	count := 0
	for _, bridge := range hostBridges {
		if !strings.HasPrefix(bridge, "mvm-") {
			continue
		}
		if dbBridgeNames[bridge] {
			continue
		}
		err := func() error {
			for _, slave := range getBridgeSlaves(bridge) {
				if err := removeRawTap(slave); err != nil {
					return err
				}
			}
			return removeRawBridge(bridge)
		}()
		if err != nil {
			slog.Warn("Failed to remove orphaned bridge", "bridge", bridge, "error", err)
		} else {
			count++
			slog.Info("Removed orphaned bridge", "bridge", bridge)
		}
	}
	return count
}

// ── Remove stale interfaces ──
// Matches Python: Service.remove_stale_interfaces()

func (s *Service) RemoveStaleInterfaces(prefix string) []string {
	var summary []string
	bridges := getSystemBridges()
	for _, bridge := range bridges {
		if !strings.HasPrefix(bridge, prefix) {
			continue
		}
		for _, slave := range getBridgeSlaves(bridge) {
			if err := removeRawTap(slave); err != nil {
				summary = append(summary, fmt.Sprintf("Warning: failed to remove interface '%s': %s", slave, err))
			} else {
				summary = append(summary, fmt.Sprintf("Removed interface '%s'", slave))
			}
		}
	}
	return summary
}

// ── Raw bridge/TAP operations (static helpers matching Python) ──

func removeRawTap(tap string) error {
	if !tapExists(tap) {
		return nil
	}

	// Bring down (best effort — may already be down)
	system.RunCmdCompat(context.Background(), []string{"ip", "link", "set", tap, "down"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})

	// Try standard link delete first
	result := system.RunCmdCompat(context.Background(), []string{"ip", "link", "delete", tap},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result != nil && result.Success {
		return nil
	}

	stderrFirst := ""
	if result != nil {
		stderrFirst = strings.TrimSpace(result.Stderr)
	}

	// Fallback for tuntap-type interfaces
	result = system.RunCmdCompat(context.Background(), []string{"ip", "tuntap", "del", "dev", tap, "mode", "tap"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result != nil && result.Success {
		return nil
	}

	details := ""
	if stderrFirst != "" {
		details = fmt.Sprintf(" (%s)", stderrFirst)
	}
	return errs.Wrap(errs.CodeNetworkBridgeFailed,
		fmt.Errorf("Failed to remove TAP device '%s'. Tried 'ip link delete'%s and 'ip tuntap del'.", tap, details))
}

func removeRawBridge(bridge string) error {
	if !bridgeExists(bridge) {
		return nil
	}

	// Remove slave interfaces first
	for _, slave := range getBridgeSlaves(bridge) {
		system.RunCmdCompat(context.Background(), []string{"ip", "link", "set", slave, "down"},
			system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		result := system.RunCmdCompat(context.Background(), []string{"ip", "link", "delete", slave},
			system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		if result == nil || !result.Success {
			// Try tuntap fallback for TAP slaves
			system.RunCmdCompat(context.Background(), []string{"ip", "tuntap", "del", "dev", slave, "mode", "tap"},
				system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		}
	}

	// Bring bridge down
	system.RunCmdCompat(context.Background(), []string{"ip", "link", "set", bridge, "down"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})

	// Delete bridge
	result := system.RunCmdCompat(context.Background(), []string{"ip", "link", "delete", bridge, "type", "bridge"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result != nil && result.Success {
		return nil
	}

	stderrFirst := ""
	if result != nil {
		stderrFirst = strings.TrimSpace(result.Stderr)
	}

	// Fallback: try without type specifier
	result = system.RunCmdCompat(context.Background(), []string{"ip", "link", "delete", bridge},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result != nil && result.Success {
		return nil
	}

	details := ""
	if stderrFirst != "" {
		details = fmt.Sprintf(" (%s)", stderrFirst)
	}
	return errs.Wrap(errs.CodeNetworkBridgeFailed,
		fmt.Errorf("Failed to remove bridge '%s'. Tried 'ip link delete' with type%s and without.", bridge, details))
}

// RemoveRawTap removes a TAP device by name.
// Matches Python's Service.remove_raw_tap() @staticmethod.
func (s *Service) RemoveRawTap(ctx context.Context, tap string) error {
	return removeRawTap(tap)
}

// RemoveRawBridge removes a bridge interface by name.
// Matches Python's Service.remove_raw_bridge() @staticmethod.
func (s *Service) RemoveRawBridge(ctx context.Context, bridge string) error {
	return removeRawBridge(bridge)
}

// ── IP forwarding ──

func ensureIPForwarding() error {
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0644); err != nil {
		result := system.RunCmdCompat(context.Background(), []string{"sysctl", "-w", "net.ipv4.ip_forward=1"},
			system.RunCmdOpts{Capture: true, Privileged: true, Check: true})
		if result != nil && result.Err != nil {
			slog.Debug("Failed to enable IP forwarding")
			return errs.NetworkError("Failed to enable IP forwarding")
		}
	}
	return nil
}

// ── Conversion helpers (network.FirewallRule → firewall.FirewallRule) ──

func toFWRule(r *FirewallRule) firewall.FirewallRule {
	fr := firewall.FirewallRule{
		TableName:    firewall.FirewallTable(r.TableName),
		ChainName:    firewall.FirewallChain(r.ChainName),
		RuleType:     firewall.FirewallRuleType(r.RuleType),
		Protocol:     firewall.FirewallProtocol(r.Protocol),
		Source:       r.Source,
		Destination:  r.Destination,
		InInterface:  r.InInterface,
		OutInterface: r.OutInterface,
		Target:       firewall.FirewallTarget(r.Target),
		SPort:        r.SPort,
		DPort:        r.DPort,
		NetworkID:    r.NetworkID,
		IsActive:     r.IsActive,
	}
	if r.ID != nil {
		v := *r.ID
		fr.ID = &v
	}
	if r.NetworkName != nil {
		fr.NetworkName = r.NetworkName
	}
	if r.CommentTag != nil {
		fr.CommentTag = r.CommentTag
	}
	if r.CommandString != nil {
		fr.CommandString = r.CommandString
	}
	if r.CreatedAt != nil {
		fr.CreatedAt = r.CreatedAt
	}
	if r.LastVerifiedAt != nil {
		fr.LastVerifiedAt = r.LastVerifiedAt
	}
	return fr
}

// ── System query helpers (non-privileged) ──

func bridgeExists(bridge string) bool {
	result := system.RunCmdCompat(context.Background(), []string{"ip", "link", "show", bridge},
		system.RunCmdOpts{Capture: true, Check: false})
	return result != nil && result.Success
}

func tapExists(tap string) bool {
	result := system.RunCmdCompat(context.Background(), []string{"ip", "link", "show", tap},
		system.RunCmdOpts{Capture: true, Check: false})
	return result != nil && result.Success
}

func bridgeHasSubnet(bridge, subnet string) bool {
	result := system.RunCmdCompat(context.Background(), []string{"ip", "-o", "addr", "show", bridge},
		system.RunCmdOpts{Capture: true, Check: false})
	if result == nil || !result.Success {
		return false
	}
	return strings.Contains(result.Stdout, subnet)
}

func getBridgeSlaves(bridge string) []string {
	result := system.RunCmdCompat(context.Background(), []string{"ip", "-o", "link", "show", "master", bridge},
		system.RunCmdOpts{Capture: true, Check: false})
	if result == nil || !result.Success {
		return nil
	}
	var slaves []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			slave := strings.TrimRight(parts[1], ":")
			slave = strings.SplitN(slave, "@", 2)[0]
			if slave != bridge {
				slaves = append(slaves, slave)
			}
		}
	}
	return slaves
}

func getBridgeTaps(bridge string) []string {
	result := system.RunCmdCompat(context.Background(), []string{"ip", "link", "show", "master", bridge},
		system.RunCmdOpts{Capture: true, Check: false})
	if result == nil || !result.Success {
		return nil
	}
	var taps []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		// Python: checks if parts[0].isdigit() to filter out header/error lines
		if len(parts) >= 2 && len(parts[0]) > 0 && parts[0][0] >= '0' && parts[0][0] <= '9' {
			iface := strings.TrimRight(parts[1], ":")
			taps = append(taps, iface)
		}
	}
	return taps
}

func getTapBridge(tap string) string {
	result := system.RunCmdCompat(context.Background(), []string{"ip", "link", "show", tap},
		system.RunCmdOpts{Capture: true, Check: false})
	if result == nil || !result.Success {
		return ""
	}
	for _, line := range strings.Split(result.Stdout, "\n") {
		if strings.Contains(line, "master") {
			parts := strings.Fields(line)
			for i, part := range parts {
				if part == "master" && i+1 < len(parts) {
					return parts[i+1]
				}
			}
		}
	}
	return ""
}

// ── Batch ip commands (privileged) ──
// Matches Python NetworkUtils._run_batch() exactly:
//   run_cmd(["ip", "-batch", "-"], privileged=True, input=batch)
// where batch = "\n".join(commands) + "\n"

func runBatch(ctx context.Context, commands []string) error {
	if len(commands) == 0 {
		return nil
	}
	batch := strings.Join(commands, "\n") + "\n"
	result := system.RunCmdCompat(ctx, []string{"ip", "-batch", "-"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: true, Input: batch})
	if result != nil && result.Err != nil {
		return result.Err
	}
	return nil
}

// ── nftables availability check ──
// Matches Python Service.check_nftables_available()

func (s *Service) CheckNFTablesAvailable() bool {
	result := system.RunCmdCompat(context.Background(), []string{"nft", "--version"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result == nil || !result.Success {
		slog.Debug("nftables not available: nft --version failed")
		return false
	}

	system.RunCmdCompat(context.Background(), []string{"modprobe", "nft_chain_nat"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})

	test := "add table inet __mvm_nft_test\n" +
		"add chain inet __mvm_nft_test test_post { type nat hook postrouting priority srcnat; policy accept; }\n" +
		"add rule inet __mvm_nft_test test_post masquerade\n"
	testResult := system.RunCmdCompat(context.Background(), []string{"nft", "-f", "-"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Input: test})

	system.RunCmdCompat(context.Background(), []string{"nft", "delete", "table", "inet", "__mvm_nft_test"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})

	if testResult == nil || !testResult.Success {
		slog.Debug("nftables MASQUERADE not available (kernel module nft_chain_nat may be missing)")
	}
	return testResult != nil && testResult.Success
}

// ── Flush ARP ──

func (s *Service) FlushARP(bridge string) {
	system.RunCmdCompat(context.Background(), []string{"ip", "neigh", "flush", "dev", bridge},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
}

// ── System bridge listing ──

func getSystemBridges() []string {
	result := system.RunCmdCompat(context.Background(), []string{"ip", "-o", "link", "show", "type", "bridge"},
		system.RunCmdOpts{Capture: true, Check: false})
	if result == nil || !result.Success {
		return nil
	}
	var bridges []string
	for _, line := range strings.Split(result.Stdout, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			bridges = append(bridges, strings.TrimRight(parts[1], ":"))
		}
	}
	return bridges
}

// ── Compute helpers ──

func ComputeSubnetMask(subnet string) string {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", ipnet.Mask[0], ipnet.Mask[1], ipnet.Mask[2], ipnet.Mask[3])
}

func ComputePrefixLength(subnet string) int {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return 0
	}
	ones, _ := ipnet.Mask.Size()
	return ones
}

// ComputeIPv4Gateway computes the default gateway IP from subnet (first usable host).
// Matches Python's compute_ipv4_gateway:
//   - For /31 (RFC 3021): both addresses are usable, uses the second (ip+1)
//   - For /32: the single address is the only host (returns ip)
//   - For all others: returns the first usable host (ip+1)
func ComputeIPv4Gateway(subnet string) (string, error) {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("invalid subnet: %s", subnet)
	}

	ip := ipnet.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("invalid subnet (not IPv4): %s", subnet)
	}

	ones, bits := ipnet.Mask.Size()
	total := 1 << (bits - ones)

	if total <= 2 {
		// RFC 3021: for /31 both addresses are usable, use the second (ip+1).
		// For /32, the single address is the only host (return ip unchanged).
		if total == 1 {
			// /32: Python's IPv4Network.hosts() returns [ip]
			return ip.String(), nil
		}
		// /31: Python's IPv4Network.hosts() returns [ip, ip+1], uses hosts[1]
		n := ipToUint32(ip) + 1
		return intToIP(n).String(), nil
	}

	// Normal subnets: first usable host = ip + 1
	n := ipToUint32(ip) + 1
	return intToIP(n).String(), nil
}

// ComputeBridgeAddress returns gateway IP with subnet prefix.
// Matches Python's compute_bridge_address which raises ValueError on invalid subnet.
func ComputeBridgeAddress(gateway, subnet string) (string, error) {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("invalid subnet: %s", subnet)
	}
	ones, _ := ipnet.Mask.Size()
	return fmt.Sprintf("%s/%d", gateway, ones), nil
}

func ComputeBridgeName(networkName string) string {
	cliName := "mvm"
	raw := fmt.Sprintf("%s-%s", cliName, networkName)
	if len(raw) <= 15 {
		return raw
	}

	hashLen := 8
	prefix := fmt.Sprintf("%s-", cliName)
	maxName := 15 - len(prefix) - hashLen - 1
	nameTruncated := networkName
	if maxName > 0 && len(networkName) > maxName {
		nameTruncated = networkName[:maxName]
	}
	shortHash := sha256Hex(networkName)[:hashLen]
	return fmt.Sprintf("%s%s-%s", prefix, nameTruncated, shortHash)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h)
}

// GenerateMAC generates a MAC address with the given prefix.
// Matches Python's generate_mac which uses 4 random bytes + uppercase.
func GenerateMAC(macPrefix string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		b = []byte{
			byte(time.Now().UnixNano()),
			byte(os.Getpid()),
			byte(os.Getppid()),
			0x00, // 4th byte for the additional random byte
		}
	}
	return strings.ToUpper(fmt.Sprintf("%s:%02x:%02x:%02x:%02x", macPrefix, b[0], b[1], b[2], b[3]))
}

func GenerateTAPName(networkName, vmName string) string {
	raw := fmt.Sprintf("%s-%s", networkName, vmName)
	hash := sha256Hex(raw)[:11]
	return fmt.Sprintf("mvm-%s", hash)
}

func getIPNet(subnet string) *net.IPNet {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil
	}
	return ipnet
}

// ── Error helpers ──

// errorMessageString returns the error message as a string, handling nil.
// Returns empty string for nil, which is Go's natural zero value.
func errorMessageString(msg *string) string {
	if msg == nil {
		return ""
	}
	return *msg
}

// isNetworkError checks if an error is a NetworkError-type error.
// Matches Python's "except NetworkError" which catches all network-related failures.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var de *errs.DomainError
	if errors.As(err, &de) {
		switch de.Code {
		case errs.CodeNetworkBridgeFailed, errs.CodeNetworkNATFailed,
			errs.CodeNetworkFirewallFailed, errs.CodeNetworkNotFound,
			errs.CodeNetworkLeaseFailed, errs.CodeNetworkLeaseExhausted,
			errs.CodeNetworkSubnetOverlap, errs.CodeNetworkAlreadyExists:
			return true
		}
		// Generic NetworkError from NetworkError() has Op "network" and CodeInternal
		if de.Op == "network" {
			return true
		}
	}
	return false
}

// ── IP conversion helpers (shared with lease_service.go) ──
// ipToUint32 and intToIP are defined in lease_service.go
// TODO(verdict#33): move ipToUint32, intToIP to infra/
