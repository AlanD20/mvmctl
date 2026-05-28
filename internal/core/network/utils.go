package network

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/firewall"
	"mvmctl/internal/infra/system"
)

// ── Batch context ──

// SyncResult holds the result of a SyncIPTablesRules operation.
// Matches the dict returned by Python's sync_iptables_rules().
type SyncResult struct {
	Added    int // Rules added to host iptables
	Verified int // Rules already present in host iptables
	Orphaned int // Host rules not tracked in DB
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

// ── Firewall rule list interfaces ──

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
