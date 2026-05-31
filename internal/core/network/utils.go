package network

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"
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

func RemoveRawTap(ctx context.Context, tap string) error {
	if !TapExists(ctx, tap) {
		return nil
	}

	// Bring down (best effort — may already be down)
	system.RunCmdCompat(ctx, []string{"ip", "link", "set", tap, "down"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})

	// Try standard link delete first
	result := system.RunCmdCompat(ctx, []string{"ip", "link", "delete", tap},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result != nil && result.Success {
		return nil
	}

	stderrFirst := ""
	if result != nil {
		stderrFirst = strings.TrimSpace(result.Stderr)
	}

	// Fallback for tuntap-type interfaces
	result = system.RunCmdCompat(ctx, []string{"ip", "tuntap", "del", "dev", tap, "mode", "tap"},
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

func RemoveRawBridge(ctx context.Context, bridge string) error {
	if !BridgeExists(ctx, bridge) {
		return nil
	}

	// Remove slave interfaces first
	for _, slave := range GetBridgeSlaves(ctx, bridge) {
		system.RunCmdCompat(ctx, []string{"ip", "link", "set", slave, "down"},
			system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		result := system.RunCmdCompat(ctx, []string{"ip", "link", "delete", slave},
			system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		if result == nil || !result.Success {
			// Try tuntap fallback for TAP slaves
			system.RunCmdCompat(ctx, []string{"ip", "tuntap", "del", "dev", slave, "mode", "tap"},
				system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
		}
	}

	// Bring bridge down
	system.RunCmdCompat(ctx, []string{"ip", "link", "set", bridge, "down"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})

	// Delete bridge
	result := system.RunCmdCompat(ctx, []string{"ip", "link", "delete", bridge, "type", "bridge"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
	if result != nil && result.Success {
		return nil
	}

	stderrFirst := ""
	if result != nil {
		stderrFirst = strings.TrimSpace(result.Stderr)
	}

	// Fallback: try without type specifier
	result = system.RunCmdCompat(ctx, []string{"ip", "link", "delete", bridge},
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

// ── System query helpers (non-privileged) ──

func BridgeExists(ctx context.Context, bridge string) bool {
	result := system.RunCmdCompat(ctx, []string{"ip", "link", "show", bridge},
		system.RunCmdOpts{Capture: true, Check: false})
	return result != nil && result.Success
}

func TapExists(ctx context.Context, tap string) bool {
	result := system.RunCmdCompat(ctx, []string{"ip", "link", "show", tap},
		system.RunCmdOpts{Capture: true, Check: false})
	return result != nil && result.Success
}

func bridgeHasSubnet(ctx context.Context, bridge, subnet string) bool {
	result := system.RunCmdCompat(ctx, []string{"ip", "-o", "addr", "show", bridge},
		system.RunCmdOpts{Capture: true, Check: false})
	if result == nil || !result.Success {
		return false
	}
	return strings.Contains(result.Stdout, subnet)
}

func GetBridgeSlaves(ctx context.Context, bridge string) []string {
	result := system.RunCmdCompat(ctx, []string{"ip", "-o", "link", "show", "master", bridge},
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

func GetBridgeTaps(ctx context.Context, bridge string) []string {
	result := system.RunCmdCompat(ctx, []string{"ip", "link", "show", "master", bridge},
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

func GetTapBridge(ctx context.Context, tap string) string {
	result := system.RunCmdCompat(ctx, []string{"ip", "link", "show", tap},
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

func GetSystemBridges(ctx context.Context) []string {
	result := system.RunCmdCompat(ctx, []string{"ip", "-o", "link", "show", "type", "bridge"},
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
		n := IPToUint32(ip) + 1
		return IntToIP(n).String(), nil
	}

	// Normal subnets: first usable host = ip + 1
	n := IPToUint32(ip) + 1
	return IntToIP(n).String(), nil
}

// allocateNextIP finds the next available IP in a subnet, skipping gateway.
// Matches Python's NetworkUtils.allocate_next_ip exactly.
func AllocateNextIP(existingIPs []string, subnet, gateway string) (string, error) {
	network := &net.IPNet{}
	if _, ipnet, err := net.ParseCIDR(subnet); err == nil {
		network = ipnet
	} else {
		return "", fmt.Errorf("invalid subnet: %s", subnet)
	}

	existingSet := make(map[string]bool)
	for _, ip := range existingIPs {
		existingSet[ip] = true
	}

	ip := network.IP.To4()
	mask := network.Mask
	ones, bits := mask.Size()
	total := 1 << (bits - ones)

	// Matches Python's ipaddress.IPv4Network(subnet, strict=False).hosts():
	// For /31 (RFC 3021): both addresses are usable.
	// For /32: the single address is usable.
	start := 1
	end := total - 1
	if total <= 2 {
		start = 0
		end = total
	}

	for i := start; i < end; i++ {
		n := IPToUint32(ip) + uint32(i)
		candidate := IntToIP(n).String()

		if gateway != "" && candidate == gateway {
			continue
		}
		if !existingSet[candidate] {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("no available IPs in subnet %s", subnet)
}

// ipToUint32 converts an IPv4 address to a uint32.
func IPToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

// intToIP converts a uint32 to an IPv4 address.
func IntToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

// ── Network utilities ──

// FlushARP flushes the ARP cache for a bridge interface.
// Matches Python's Service.flush_arp().
func FlushARP(ctx context.Context, bridge string) {
	system.RunCmdCompat(ctx, []string{"ip", "neigh", "flush", "dev", bridge},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
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

// ComputeBridgeName generates a 15-char bridge name from the network name.
// Matches Python's NetworkUtils.compute_bridge_name().
func ComputeBridgeName(networkName string) string {
	raw := fmt.Sprintf("%s-%s", infra.CLIName, networkName)
	if len(raw) <= 15 {
		return raw
	}

	hashLen := 8
	prefix := fmt.Sprintf("%s-", infra.CLIName)
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
			0x00,
		}
	}
	return strings.ToUpper(fmt.Sprintf("%s:%02x:%02x:%02x:%02x", macPrefix, b[0], b[1], b[2], b[3]))
}

// GenerateTAPName generates a TAP device name from network and VM names.
// Matches Python's NetworkUtils.generate_tap_name().
func GenerateTAPName(networkName, vmName string) string {
	raw := fmt.Sprintf("%s-%s", networkName, vmName)
	hash := sha256Hex(raw)[:11]
	return fmt.Sprintf("%s-%s", infra.CLIName, hash)
}
