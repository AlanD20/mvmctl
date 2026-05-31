package network

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/firewall"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// Service manages network interfaces, bridges, TAP devices, and NAT/firewall rules.
// Matches src/mvmctl/core/network/_service.py: Service
type Service struct {
	repo            Repository
	firewallTracker *firewall.FirewallTracker
}

// NewService creates a Service. The tracker parameter is the firewall tracker
// to use for firewall operations. If nil, all firewall operations are skipped
// (used for cleanup contexts). Callers can replace the tracker later via
// SetFirewallTracker (e.g., after HostInit resolves the firewall backend).
func NewService(repo Repository, tracker *firewall.FirewallTracker) *Service {
	return &Service{repo: repo, firewallTracker: tracker}
}

// SetFirewallTracker replaces the firewall tracker.
// Used by the API layer to inject the configured tracker after HostInit
// resolves firewall_backend and iptables_xtcomment settings.
func (s *Service) SetFirewallTracker(tracker *firewall.FirewallTracker) {
	s.firewallTracker = tracker
}

// WithBatch runs a function inside a firewall batch context, flushing
// all queued rule operations atomically on return. This matches Python's:
// with self._tracker.batch():
//
//	...
func (s *Service) WithBatch(ctx context.Context, fn func()) {
	if s.firewallTracker == nil {
		fn()
		return
	}
	s.firewallTracker.WithBatch(ctx, fn)
}

// ── List ──

func (s *Service) ListAll(ctx context.Context, verify bool) ([]*model.Network, error) {
	networks, err := s.repo.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	if !verify {
		return networks, nil
	}

	var missingIDs []string
	for _, network := range networks {
		if !BridgeExists(ctx, network.Bridge) {
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
		s.firewallTracker.Initialize(ctx)
	}
	return nil
}

func (s *Service) Initialize(ctx context.Context) error {
	if s.firewallTracker != nil {
		s.firewallTracker.Initialize(ctx)
	}
	return nil
}

func (s *Service) Teardown(ctx context.Context) error {
	if s.firewallTracker == nil {
		return nil
	}
	s.firewallTracker.Teardown(ctx)
	return nil
}

// ── Bridge management ──

func (s *Service) EnsureBridge(ctx context.Context, bridge, bridgeAddress string) error {
	if BridgeExists(ctx, bridge) {
		slog.Debug("Bridge already exists, reconciling state", "bridge", bridge)
		var reconcileCmds []string
		if !bridgeHasSubnet(ctx, bridge, bridgeAddress) {
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

	slog.Info("Bridge created with address", "bridge", bridge, "address", bridgeAddress)
	return nil
}

func (s *Service) RemoveBridge(ctx context.Context, bridge string, networkID string) error {
	attachedTaps := GetBridgeTaps(ctx, bridge)
	for _, tap := range attachedTaps {
		slog.Debug("Removing attached TAP from bridge", "tap", tap, "bridge", bridge)
		s.RemoveTap(ctx, tap, bridge, networkID)
	}
	if err := RemoveRawBridge(ctx, bridge); err != nil {
		return errs.WrapMsg(errs.CodeNetworkBridgeFailed,
			fmt.Sprintf("Failed to teardown bridge %s", bridge), err)
	}
	slog.Info("Bridge removed", "bridge", bridge)
	return nil
}

// ── NAT ──

func (s *Service) EnsureNAT(
	ctx context.Context,
	bridge string,
	natGateways []string,
	subnet string,
	networkID string,
) error {
	// Initialize firewall chains
	s.Initialize(ctx)

	for _, gatewayIface := range natGateways {
		masqRule := &model.FirewallRule{
			TableName:    model.FirewallTableNat,
			ChainName:    FirewallChainMVMPostrouting,
			RuleType:     model.FirewallRuleTypeMasquerade,
			Target:       model.FirewallTargetMasquerade,
			NetworkID:    networkID,
			Protocol:     model.FirewallProtocolAll,
			Source:       subnet,
			Destination:  string(model.FirewallWildcardAnyCIDR),
			InInterface:  string(model.FirewallWildcardAnyInterface),
			OutInterface: gatewayIface,
			SPort:        int(model.FirewallPortAny),
			DPort:        int(model.FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		}
		fwdOutRule := &model.FirewallRule{
			TableName:    model.FirewallTableFilter,
			ChainName:    FirewallChainMVMForward,
			RuleType:     model.FirewallRuleTypeForwardOut,
			Target:       model.FirewallTargetAccept,
			NetworkID:    networkID,
			Protocol:     model.FirewallProtocolAll,
			Source:       subnet,
			Destination:  string(model.FirewallWildcardAnyCIDR),
			InInterface:  bridge,
			OutInterface: gatewayIface,
			SPort:        int(model.FirewallPortAny),
			DPort:        int(model.FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		}
		fwdInRule := &model.FirewallRule{
			TableName:    model.FirewallTableFilter,
			ChainName:    FirewallChainMVMForward,
			RuleType:     model.FirewallRuleTypeForwardIn,
			Target:       model.FirewallTargetAccept,
			NetworkID:    networkID,
			Protocol:     model.FirewallProtocolAll,
			Source:       string(model.FirewallWildcardAnyCIDR),
			Destination:  subnet,
			InInterface:  gatewayIface,
			OutInterface: bridge,
			SPort:        int(model.FirewallPortAny),
			DPort:        int(model.FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		}

		if s.firewallTracker != nil {
			context := fmt.Sprintf("%s:%s", bridge, gatewayIface)
			result := s.firewallTracker.EnsureRule(ctx, *masqRule, context)
			if !result.Success {
				errMsg := infra.DerefOrZero(result.ErrorMessage)
				return errs.Wrap(errs.CodeNetworkNATFailed,
					fmt.Errorf("Failed to add MASQUERADE rule for %s via %s: %s", bridge, gatewayIface, errMsg))
			}
			result = s.firewallTracker.EnsureRule(ctx, *fwdOutRule, context)
			if !result.Success {
				errMsg := infra.DerefOrZero(result.ErrorMessage)
				return errs.Wrap(errs.CodeNetworkNATFailed,
					fmt.Errorf("Failed to add FORWARD out rule for %s via %s: %s", bridge, gatewayIface, errMsg))
			}
			result = s.firewallTracker.EnsureRule(ctx, *fwdInRule, context)
			if !result.Success {
				errMsg := infra.DerefOrZero(result.ErrorMessage)
				return errs.Wrap(errs.CodeNetworkNATFailed,
					fmt.Errorf("Failed to add FORWARD in rule for %s via %s: %s", bridge, gatewayIface, errMsg))
			}
		}
	}

	slog.Info("NAT rules configured for bridge",
		"bridge", bridge,
		"gateways", strings.Join(natGateways, ", "),
		"subnet", subnet)
	return nil
}

func (s *Service) RemoveNAT(
	ctx context.Context,
	bridge string,
	natGateways []string,
	subnet, networkID string,
	force bool,
) error {
	effectiveGateways := natGateways
	effectiveSubnet := subnet

	// Matches Python: tries resolver.by_name(bridge), catching ALL exceptions silently
	if effectiveGateways == nil || effectiveSubnet == "" {
		resolver := NewResolver(s.repo, nil)
		network, err := resolver.ByName(ctx, bridge)
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
		return errs.Wrap(
			errs.CodeNetworkNATFailed,
			fmt.Errorf(
				"Could not determine NAT gateways for bridge %s. Provide nat_gateways explicitly or ensure network exists in database.",
				bridge,
			),
		)
	}
	if effectiveSubnet == "" {
		return errs.Wrap(
			errs.CodeNetworkNATFailed,
			fmt.Errorf(
				"Could not determine subnet for bridge %s. Provide subnet explicitly or ensure network exists in database.",
				bridge,
			),
		)
	}

	// Check for attached TAPs — matches Python's NetworkError
	attachedTaps := GetBridgeTaps(ctx, bridge)
	if len(attachedTaps) > 0 {
		if !force {
			return errs.NetworkError(
				fmt.Sprintf(
					"Cannot remove NAT: %d TAP(s) still attached on bridge %s. Use --force to override.",
					len(attachedTaps),
					bridge,
				),
			)
		}
		slog.Warn("Removing NAT for bridge but TAPs still attached",
			"bridge", bridge,
			"count", len(attachedTaps),
			"taps", strings.Join(attachedTaps, ", "))
	}

	// Build rules to remove
	var rulesToRemove []model.FirewallRule
	for _, gwIface := range effectiveGateways {
		rulesToRemove = append(rulesToRemove, model.FirewallRule{
			TableName:    model.FirewallTableNat,
			ChainName:    FirewallChainMVMPostrouting,
			RuleType:     model.FirewallRuleTypeMasquerade,
			Target:       model.FirewallTargetMasquerade,
			NetworkID:    networkID,
			Protocol:     model.FirewallProtocolAll,
			Source:       effectiveSubnet,
			Destination:  string(model.FirewallWildcardAnyCIDR),
			InInterface:  string(model.FirewallWildcardAnyInterface),
			OutInterface: gwIface,
			SPort:        int(model.FirewallPortAny),
			DPort:        int(model.FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		})
		rulesToRemove = append(rulesToRemove, model.FirewallRule{
			TableName:    model.FirewallTableFilter,
			ChainName:    FirewallChainMVMForward,
			RuleType:     model.FirewallRuleTypeForwardOut,
			Target:       model.FirewallTargetAccept,
			NetworkID:    networkID,
			Protocol:     model.FirewallProtocolAll,
			Source:       effectiveSubnet,
			Destination:  string(model.FirewallWildcardAnyCIDR),
			InInterface:  bridge,
			OutInterface: gwIface,
			SPort:        int(model.FirewallPortAny),
			DPort:        int(model.FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		})
		rulesToRemove = append(rulesToRemove, model.FirewallRule{
			TableName:    model.FirewallTableFilter,
			ChainName:    FirewallChainMVMForward,
			RuleType:     model.FirewallRuleTypeForwardIn,
			Target:       model.FirewallTargetAccept,
			NetworkID:    networkID,
			Protocol:     model.FirewallProtocolAll,
			Source:       string(model.FirewallWildcardAnyCIDR),
			Destination:  effectiveSubnet,
			InInterface:  gwIface,
			OutInterface: bridge,
			SPort:        int(model.FirewallPortAny),
			DPort:        int(model.FirewallPortAny),
			IsActive:     true,
			NetworkName:  &bridge,
		})
	}

	// Batch remove all rules (non-fatal on failure — matches Python's behavior)
	if s.firewallTracker != nil {
		fwRules := make([]model.FirewallRule, len(rulesToRemove))
		for i := range rulesToRemove {
			fwRules[i] = rulesToRemove[i]
		}
		res := s.firewallTracker.BatchRemoveRules(ctx, fwRules)
		if !res.Success {
			msg := infra.DerefOrZero(res.ErrorMessage)
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
	if TapExists(ctx, tap) {
		currentBridge := GetTapBridge(ctx, tap)
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

	// Matches Python ensure_tap exactly:
	//   forward_bridge_to_tap: source=subnet or ANY_CIDR, destination=ANY_CIDR
	//   forward_tap_to_bridge: source=ANY_CIDR,           destination=subnet or ANY_CIDR
	wildcard := string(model.FirewallWildcardAnyCIDR)

	fwdBridgeToTap := &model.FirewallRule{
		TableName:    model.FirewallTableFilter,
		ChainName:    FirewallChainMVMForward,
		RuleType:     model.FirewallRuleTypeForwardOut,
		Target:       model.FirewallTargetAccept,
		NetworkID:    networkID,
		Protocol:     model.FirewallProtocolAll,
		Source:       wildcard, // overwritten below if subnet != ""
		Destination:  wildcard, // ALWAYS wildcard — matches Python
		InInterface:  bridge,
		OutInterface: tap,
		SPort:        int(model.FirewallPortAny),
		DPort:        int(model.FirewallPortAny),
		IsActive:     true,
		NetworkName:  &bridge,
	}
	fwdTapToBridge := &model.FirewallRule{
		TableName:    model.FirewallTableFilter,
		ChainName:    FirewallChainMVMForward,
		RuleType:     model.FirewallRuleTypeForwardIn,
		Target:       model.FirewallTargetAccept,
		NetworkID:    networkID,
		Protocol:     model.FirewallProtocolAll,
		Source:       wildcard, // ALWAYS wildcard — matches Python
		Destination:  wildcard, // overwritten below if subnet != ""
		InInterface:  tap,
		OutInterface: bridge,
		SPort:        int(model.FirewallPortAny),
		DPort:        int(model.FirewallPortAny),
		IsActive:     true,
		NetworkName:  &bridge,
	}

	if subnet != "" {
		fwdBridgeToTap.Source = subnet
		fwdTapToBridge.Destination = subnet
	}

	if s.firewallTracker != nil {
		result := s.firewallTracker.EnsureRule(ctx, *fwdBridgeToTap, fmt.Sprintf("tap:%s", tap))
		if !result.Success {
			errMsg := infra.DerefOrZero(result.ErrorMessage)
			return errs.Wrap(errs.CodeNetworkFirewallFailed,
				fmt.Errorf("Failed to add FORWARD rule for bridge %s to TAP %s: %s", bridge, tap, errMsg))
		}
		result = s.firewallTracker.EnsureRule(ctx, *fwdTapToBridge, fmt.Sprintf("tap:%s", tap))
		if !result.Success {
			s.firewallTracker.RemoveRule(ctx, *fwdBridgeToTap)
			errMsg := infra.DerefOrZero(result.ErrorMessage)
			return errs.Wrap(errs.CodeNetworkFirewallFailed,
				fmt.Errorf("Failed to add FORWARD rule for TAP %s to bridge %s: %s", tap, bridge, errMsg))
		}
	}

	return nil
}

func (s *Service) RemoveTap(ctx context.Context, tap, bridge string, networkID string) error {
	if !TapExists(ctx, tap) {
		slog.Debug("TAP device does not exist, skipping removal", "tap", tap)
		return nil
	}

	effectiveBridge := bridge
	if effectiveBridge == "" {
		effectiveBridge = GetTapBridge(ctx, tap)
	}

	if effectiveBridge != "" {
		dbRules, err := s.firewallTracker.GetByNetworkIDAndInterface(ctx, networkID, tap, false)
		if err == nil && len(dbRules) > 0 {
			valRules := make([]model.FirewallRule, len(dbRules))
			for i, r := range dbRules {
				valRules[i] = *r
			}
			res := s.firewallTracker.BatchRemoveRules(ctx, valRules)
			if !res.Success {
				msg := infra.DerefOrZero(res.ErrorMessage)
				slog.Warn("Failed to remove FORWARD rules for TAP",
					"tap", tap,
					"error", msg)
			}
		}
	} else {
		slog.Warn("Could not determine bridge for TAP, skipping rule cleanup", "tap", tap)
	}

	if err := RemoveRawTap(ctx, tap); err != nil {
		return err
	}
	slog.Info("TAP device removed", "tap", tap)
	return nil
}

// ── model.Network removal ──

func (s *Service) Remove(ctx context.Context, network *model.Network, force bool) error {
	// 1. Tear down NAT — only catch NetworkError, matching Python's behavior
	if network.NATEnabled {
		if err := s.RemoveNAT(
			ctx,
			network.Bridge,
			NatGatewaysList(network),
			network.Subnet,
			network.ID,
			force,
		); err != nil {
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
			fmt.Sprintf("model.Network referenced by VMs: %s", strings.Join(vmNames, ", ")))
	}

	if hasVMs {
		return s.repo.SoftDelete(ctx, network.ID)
	}
	return s.repo.Delete(ctx, network.ID)
}

func (s *Service) RemoveMany(ctx context.Context, networks []*model.Network, force bool) error {
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
func (s *Service) SyncIPTablesRules(ctx context.Context, network *model.Network) (*SyncResult, error) {
	// 1. Get active DB rules for the network through the tracker.
	var dbRules []*model.FirewallRule
	if s.firewallTracker != nil {
		var err error
		dbRules, err = s.firewallTracker.GetByNetworkID(ctx, network.ID, true)
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
		s.WithBatch(ctx, func() {
			for _, rule := range dbRules {
				result := s.firewallTracker.EnsureRule(ctx, *rule, "")
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
		orphaned = s.firewallTracker.CountOrphanedRules(ctx, network)
	}

	return &SyncResult{
		Added:    added,
		Verified: verified,
		Orphaned: orphaned,
	}, nil
}

// ── Orphan cleanup ──
// Matches Python: Service.cleanup_orphaned_bridges(db_networks) exactly.
// Python: @staticmethod cleanup_orphaned_bridges(db_networks: list[NetworkItem]) -> int

func (s *Service) CleanupOrphanedBridges(ctx context.Context, dbNetworks []*model.Network) int {
	dbBridgeNames := make(map[string]bool)
	for _, n := range dbNetworks {
		dbBridgeNames[n.Bridge] = true
	}

	hostBridges := GetSystemBridges(ctx)
	count := 0
	for _, bridge := range hostBridges {
		if !strings.HasPrefix(bridge, "mvm-") {
			continue
		}
		if dbBridgeNames[bridge] {
			continue
		}
		err := func() error {
			for _, slave := range GetBridgeSlaves(ctx, bridge) {
				if err := RemoveRawTap(ctx, slave); err != nil {
					return err
				}
			}
			return RemoveRawBridge(ctx, bridge)
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

func (s *Service) RemoveStaleInterfaces(ctx context.Context, prefix string) []string {
	var summary []string
	bridges := GetSystemBridges(ctx)
	for _, bridge := range bridges {
		if !strings.HasPrefix(bridge, prefix) {
			continue
		}
		for _, slave := range GetBridgeSlaves(ctx, bridge) {
			if err := RemoveRawTap(ctx, slave); err != nil {
				summary = append(summary, fmt.Sprintf("Warning: failed to remove interface '%s': %s", slave, err))
			} else {
				summary = append(summary, fmt.Sprintf("Removed interface '%s'", slave))
			}
		}
	}
	return summary
}

// RemoveRawTap removes a TAP device by name.
// Matches Python's Service.remove_raw_tap() @staticmethod.
func (s *Service) RemoveRawTap(ctx context.Context, tap string) error {
	return RemoveRawTap(ctx, tap)
}

// RemoveRawBridge removes a bridge interface by name.
// Matches Python's Service.remove_raw_bridge() @staticmethod.
func (s *Service) RemoveRawBridge(ctx context.Context, bridge string) error {
	return RemoveRawBridge(ctx, bridge)
}

// ── nftables availability check ──
// Matches Python Service.check_nftables_available()

func (s *Service) CheckNFTablesAvailable(ctx context.Context) bool {
	result := system.RunCmdCompat(ctx, []string{"nft", "--version"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result == nil || !result.Success {
		slog.Debug("nftables not available: nft --version failed")
		return false
	}

	system.RunCmdCompat(ctx, []string{"modprobe", "nft_chain_nat"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})

	test := "add table inet __mvm_nft_test\n" +
		"add chain inet __mvm_nft_test test_post { type nat hook postrouting priority srcnat; policy accept; }\n" +
		"add rule inet __mvm_nft_test test_post masquerade\n"
	testResult := system.RunCmdCompat(ctx, []string{"nft", "-f", "-"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Input: test})

	system.RunCmdCompat(ctx, []string{"nft", "delete", "table", "inet", "__mvm_nft_test"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})

	if testResult == nil || !testResult.Success {
		slog.Debug("nftables MASQUERADE not available (kernel module nft_chain_nat may be missing)")
	}
	return testResult != nil && testResult.Success
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
