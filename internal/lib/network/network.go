package network

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// ── Private helpers (used by Python-equivalent functions) ──

// ipToInt converts IPv4 address to uint32 for arithmetic.
// Used internally by ComputeIPv4Gateway and AllocateNextIP.
func ipToInt(ip net.IP) uint32 {
	ip = ip.To4()
	if ip == nil {
		return 0
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

// intToIP converts uint32 to IPv4 address.
func intToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

// sortStrings sorts a slice of strings (simple insertion sort, matching Python's sorted).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ── Subnet Math & Computation ──
// These directly map to Python's NetworkUtils methods.

// ComputeSubnetMask returns netmask from CIDR subnet.
// Python: compute_subnet_mask(subnet) -> str
func ComputeSubnetMask(subnet string) string {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", ipnet.Mask[0], ipnet.Mask[1], ipnet.Mask[2], ipnet.Mask[3])
}

// ComputePrefixLength returns prefix length from CIDR subnet.
// Python: compute_prefix_length(subnet) -> int
func ComputePrefixLength(subnet string) int {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return 0
	}
	ones, _ := ipnet.Mask.Size()
	return ones
}

// CountHosts returns the number of usable host addresses in a subnet.
// For standard subnets this is total - 2 (excludes network and broadcast).
// For /31 and /32 (RFC 3021) all addresses are usable.
func CountHosts(ipnet *net.IPNet) int {
	ip := ipnet.IP.To4()
	if ip == nil {
		return 0
	}
	ones, bits := ipnet.Mask.Size()
	total := 1 << (bits - ones)
	if total <= 2 {
		return total
	}
	return total - 2
}

// ComputeIPv4Gateway computes default gateway IP from subnet (first usable host).
// Python: compute_ipv4_gateway(subnet) -> str
// For /31 subnets (RFC 3021), both addresses are usable hosts, so we
// return the second address to avoid colliding with the network address.
func ComputeIPv4Gateway(subnet string) (string, error) {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", err
	}

	ip := ipnet.IP.To4()
	ones, _ := ipnet.Mask.Size()
	// For /31 (RFC 3021) both addresses are usable hosts; gateway is the second.
	if ones == 31 {
		n := ipToInt(ip) + 1
		return intToIP(n).String(), nil
	}

	// Standard: gateway is the first usable host (network address + 1).
	n := ipToInt(ip) + 1
	return intToIP(n).String(), nil
}

// ComputeBridgeAddress returns gateway IP with subnet prefix (e.g. '172.29.0.1/28').
// Python: compute_bridge_address(ipv4_gateway, subnet) -> str
func ComputeBridgeAddress(gateway, subnet string) string {
	prefix := ComputePrefixLength(subnet)
	return fmt.Sprintf("%s/%d", gateway, prefix)
}

// ComputeBridgeName computes bridge name from network name.
// Python: compute_bridge_name(network_name) -> str
// Ensures the bridge name never exceeds the Linux IFNAMSIZ limit (15 chars).
func ComputeBridgeName(cliName, networkName string) string {
	raw := fmt.Sprintf("%s-%s", cliName, networkName)
	if len(raw) <= 15 {
		return raw
	}
	hashLen := 8
	prefix := fmt.Sprintf("%s-", cliName)
	maxName := 15 - len(prefix) - hashLen - 1
	nameTruncated := networkName
	if len(networkName) > maxName {
		nameTruncated = networkName[:maxName]
	}
	shortHash := fmt.Sprintf("%x", sha256.Sum256([]byte(networkName)))[:hashLen]
	return fmt.Sprintf("%s%s-%s", prefix, nameTruncated, shortHash)
}

// ── Naming & Generation ──

// GenerateMAC generates a MAC address with the given prefix.
// Python: generate_mac(mac_prefix) -> str
func GenerateMAC(macPrefix string) string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		// Deterministic fallback using time + pid
		t := time.Now().UnixNano()
		b[0] = byte(t)
		b[1] = byte(t >> 8)
		b[2] = byte(os.Getpid())
		b[3] = byte(os.Getppid())
	}
	suffix := fmt.Sprintf("%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3])
	mac := fmt.Sprintf("%s:%s", macPrefix, suffix)
	// Python returns UPPERCASE (.upper())
	return strings.ToUpper(mac)
}

// GenerateTAPName generates a unique TAP device name (max 16 chars for IFNAMSIZ).
// Python: generate_tap_name(network_name, vm_name) -> str
func GenerateTAPName(cliName, networkName, vmName string) string {
	raw := fmt.Sprintf("%s-%s", networkName, vmName)
	tapHash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:11]
	return fmt.Sprintf("%s-%s", cliName, tapHash)
}

// ── IP Allocation ──

// AllocateNextIP allocates the next available IP in a subnet.
// Python: allocate_next_ip(existing_ips, subnet, gateway=None) -> str
// For /31 (RFC 3021): both addresses are usable.
// For /32: the single address is usable.
func AllocateNextIP(existingIPs []string, subnet, gateway string) (string, error) {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("invalid subnet %s: %w", subnet, err)
	}

	existing := make(map[string]bool)
	for _, ip := range existingIPs {
		existing[ip] = true
	}

	ip := ipnet.IP.To4()
	mask := ipnet.Mask
	ones, bits := mask.Size()
	total := 1 << (bits - ones)

	start := 1
	end := total - 1
	if total <= 2 {
		start = 0
		end = total
	}

	for i := start; i < end; i++ {
		n := ipToInt(ip) + uint32(i)
		candidate := intToIP(n).String()
		if gateway != "" && candidate == gateway {
			continue
		}
		if !existing[candidate] {
			return candidate, nil
		}
	}
	return "", errs.New(errs.CodeNetworkError, fmt.Sprintf("No available IPs in subnet %s", subnet))
}

// ── System Queries (Host State) ──

var virtualInterfacePrefixes = []string{"mvm-", "tap", "br-", "virbr", "docker", "veth"}
var excludedInterfaces = []string{"lo"}

// GetPhysicalInterfaces returns available physical network interfaces.
// Python: get_physical_interfaces() -> list[str]
func GetPhysicalInterfaces() ([]string, error) {
	netPath := "/sys/class/net"
	if _, err := os.Stat(netPath); os.IsNotExist(err) {
		return nil, errs.New(errs.CodeNetworkError, "Unable to access /sys/class/net")
	}
	entries, err := os.ReadDir(netPath)
	if err != nil {
		return nil, errs.New(errs.CodeNetworkError, "Failed to list network interfaces")
	}

	var interfaces []string
	for _, entry := range entries {
		name := entry.Name()
		if slices.Contains(excludedInterfaces, name) {
			continue
		}
		isVirtual := false
		for _, prefix := range virtualInterfacePrefixes {
			if strings.HasPrefix(name, prefix) {
				isVirtual = true
				break
			}
		}
		if isVirtual {
			continue
		}
		interfaces = append(interfaces, name)
	}
	sortStrings(interfaces)
	return interfaces, nil
}

// DetectOutboundInterface returns the outbound (default route) network interface.
// Python: detect_outbound_interface() -> str | None
func DetectOutboundInterface(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"ip", "route", "show", "default"},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	if result.Err != nil || result.ExitCode != 0 {
		slog.Debug("Failed to detect outbound network interface", "error", result.Err)
		return ""
	}
	for line := range strings.SplitSeq(strings.TrimSpace(result.Stdout), "\n") {
		parts := strings.Fields(line)
		for i, part := range parts {
			if part == "dev" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return ""
}

// BridgeExists checks if a bridge interface exists.
// Python: bridge_exists(bridge) -> bool
func BridgeExists(ctx context.Context, bridge string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"ip", "link", "show", bridge},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	return result.Success
}

// TapExists checks if a TAP interface exists.
// Python: tap_exists(tap) -> bool
func TapExists(ctx context.Context, tap string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"ip", "link", "show", tap},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	return result.Success
}

// ChainExists checks if an iptables chain exists.
// Python: chain_exists(chain, table="filter") -> bool
// NOTE: Go does not support default parameters; callers must pass table explicitly.
func ChainExists(ctx context.Context, chain, table string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"iptables", "-t", table, "-L", chain, "-n"},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	return result.Success
}

// GetTunTapDevices lists all TUN/TAP devices.
// Python: get_tuntap_devices() -> list[str]
func GetTunTapDevices(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"ip", "-o", "link", "show", "type", "tuntap"},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	if !result.Success {
		return nil
	}
	var devices []string
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			devices = append(devices, strings.TrimRight(parts[1], ":"))
		}
	}
	return devices
}

// GetBridges lists all bridge interfaces.
// Python: get_bridges() -> list[str]
func GetBridges(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"ip", "-o", "link", "show", "type", "bridge"},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	if !result.Success {
		return nil
	}
	var bridges []string
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			bridges = append(bridges, strings.TrimRight(parts[1], ":"))
		}
	}
	return bridges
}

// GetBridgeSlaves returns all interface names attached to a bridge.
// Python: get_bridge_slaves(bridge) -> list[str]
func GetBridgeSlaves(ctx context.Context, bridge string) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"ip", "-o", "link", "show", "master", bridge},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	if !result.Success {
		return nil
	}
	var slaves []string
	for line := range strings.SplitSeq(result.Stdout, "\n") {
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

// GetBridgeTaps lists all TAP devices currently attached to the bridge.
// Python: get_bridge_taps(bridge) -> list[str]
func GetBridgeTaps(ctx context.Context, bridge string) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"ip", "link", "show", "master", bridge},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	if !result.Success {
		return nil
	}
	var devices []string
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		parts := strings.Fields(line)
		if len(parts) > 0 && len(parts[0]) > 0 && parts[0][0] >= '0' && parts[0][0] <= '9' && len(parts) >= 2 {
			iface := strings.TrimRight(parts[1], ":")
			devices = append(devices, iface)
		}
	}
	return devices
}

// EnsureInterfaceReady ensures a network interface exists and is usable for NAT.
// Python: ensure_interface_ready(interface) -> bool
// Returns nil on success, error on failure.
func EnsureInterfaceReady(ctx context.Context, iface string) error {
	if iface == "lo" {
		return errs.New(errs.CodeNetworkError, "Loopback interface 'lo' cannot be used for NAT")
	}

	netPath := filepath.Join("/sys/class/net", iface)
	if _, err := os.Stat(netPath); os.IsNotExist(err) {
		return errs.New(errs.CodeNetworkError, fmt.Sprintf("Interface '%s' does not exist", iface))
	}

	operstate, err := os.ReadFile(filepath.Join(netPath, "operstate"))
	if err == nil && strings.TrimSpace(string(operstate)) == "down" {
		return errs.New(errs.CodeNetworkError,
			fmt.Sprintf("Interface '%s' is down. Bring it up with: ip link set %s up", iface, iface),
		)
	}

	result := system.RunCmdCompat(ctx, []string{"ip", "-o", "-4", "addr", "show", iface}, system.RunCmdOpts{
		Check:   false,
		Capture: true,
		Text:    true,
	})
	if result.Err != nil {
		return errs.New(errs.CodeNetworkError, "'ip' command not found — install iproute2")
	}

	if !result.Success || strings.TrimSpace(result.Stdout) == "" {
		return errs.New(errs.CodeNetworkError,
			fmt.Sprintf(
				"Interface '%s' has no IPv4 address assigned. NAT requires an interface with a valid IP address.",
				iface,
			),
		)
	}

	return nil
}

// BridgeHasSubnet checks if a bridge already has a given subnet assigned.
// Python: bridge_has_subnet(bridge, subnet) -> bool
func BridgeHasSubnet(ctx context.Context, bridge, subnet string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"ip", "-o", "addr", "show", bridge},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	if !result.Success {
		return false
	}
	return strings.Contains(result.Stdout, subnet)
}

// GetTapBridge returns the bridge that a TAP device is attached to.
// Python: get_tap_bridge(tap) -> str | None
func GetTapBridge(ctx context.Context, tap string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"ip", "link", "show", tap},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	if !result.Success {
		return ""
	}
	for line := range strings.SplitSeq(result.Stdout, "\n") {
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

// ── Internal Helpers ──

// StripTapRules strips TAP-related rules from iptables rules text.
// Python: strip_tap_rules(rules_text) -> str
func StripTapRules(ctx context.Context, rulesText string) string {
	tapNames := GetTunTapDevices(ctx)
	if len(tapNames) == 0 {
		return rulesText
	}
	lines := strings.SplitAfter(rulesText, "\n")
	var filtered []string
	for _, line := range lines {
		skip := false
		for _, tap := range tapNames {
			if strings.Contains(line, tap) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, line)
		}
	}
	return strings.Join(filtered, "")
}

// DetectIPTablesBackendConflict detects mixed iptables backend conflict.
// Python: detect_iptables_backend_conflict() -> tuple[bool, str]
type BackendConflictResult struct {
	HasConflict bool
	Diagnosis   string
}

func DetectIPTablesBackendConflict(ctx context.Context) BackendConflictResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	versionResult := system.RunCmdCompat(
		ctx,
		[]string{"iptables", "--version"},
		system.RunCmdOpts{Check: false, Capture: true, Text: true},
	)
	currentBackend := "legacy"
	if versionResult.Success && strings.Contains(versionResult.Stderr, "nf_tables") {
		currentBackend = "nft"
	}

	legacyActive := false
	legacyResult := system.RunCmdCompat(ctx, []string{"iptables-legacy", "-L", "-n", "-v"}, system.RunCmdOpts{
		Check:      false,
		Capture:    true,
		Text:       true,
		Privileged: true,
	})
	if legacyResult.Success {
		for line := range strings.SplitSeq(legacyResult.Stdout, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if pkts, err := strconv.Atoi(parts[0]); err == nil && pkts > 0 {
					legacyActive = true
					break
				}
			}
		}
	}

	nftActive := false
	nftResult := system.RunCmdCompat(ctx, []string{"iptables", "-L", "-n", "-v"}, system.RunCmdOpts{
		Check:      false,
		Capture:    true,
		Text:       true,
		Privileged: true,
	})
	if nftResult.Success {
		for line := range strings.SplitSeq(nftResult.Stdout, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if pkts, err := strconv.Atoi(parts[0]); err == nil && pkts > 0 {
					nftActive = true
					break
				}
			}
		}
	}

	hasConflict := legacyActive && nftActive
	diagnosis := fmt.Sprintf(
		"iptables backend: %s, legacy active: %v, nft active: %v",
		currentBackend, legacyActive, nftActive,
	)
	return BackendConflictResult{HasConflict: hasConflict, Diagnosis: diagnosis}
}

// RunBatch executes a batch of ip commands using ip -batch mode.
// Python: _run_batch(commands) -> None
func RunBatch(ctx context.Context, commands []string) error {
	batch := strings.Join(commands, "\n") + "\n"
	result := system.RunCmdCompat(ctx, []string{"ip", "-batch", "-"}, system.RunCmdOpts{
		Check:      true,
		Capture:    true,
		Text:       true,
		Input:      batch,
		Privileged: true,
	})
	if result.Err != nil {
		return fmt.Errorf("ip -batch failed: %w\n%s", result.Err, result.Stderr)
	}
	return nil
}

// RemoveRawTap removes a TAP device by name with fallback to tuntap del.
// Python: remove_raw_tap(tap, privileged=True) -> None
func RemoveRawTap(ctx context.Context, tap string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Try standard link delete first
	result := system.RunCmdCompat(ctx, []string{"ip", "link", "delete", tap},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Text: true})
	if result.Success {
		return nil
	}
	stderrFirst := strings.TrimSpace(result.Stderr)

	// Fallback for tuntap-type interfaces
	result = system.RunCmdCompat(ctx, []string{"ip", "tuntap", "del", "dev", tap, "mode", "tap"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Text: true})
	if result.Success {
		return nil
	}

	details := ""
	if stderrFirst != "" {
		details = fmt.Sprintf(" (%s)", stderrFirst)
	}
	return errs.Wrap(errs.CodeNetworkBridgeFailed,
		fmt.Errorf("failed to remove TAP device '%s': tried 'ip link delete'%s and 'ip tuntap del'", tap, details))
}

// RemoveRawBridge removes a bridge interface with slave cleanup.
// Python: remove_raw_bridge(bridge, privileged=True) -> None
func RemoveRawBridge(ctx context.Context, bridge string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Remove slave interfaces first
	for _, slave := range GetBridgeSlaves(ctx, bridge) {
		system.RunCmdCompat(ctx, []string{"ip", "link", "set", slave, "down"},
			system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Text: true})
		result := system.RunCmdCompat(ctx, []string{"ip", "link", "delete", slave},
			system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Text: true})
		if !result.Success {
			system.RunCmdCompat(ctx, []string{"ip", "tuntap", "del", "dev", slave, "mode", "tap"},
				system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Text: true})
		}
	}

	// Bring bridge down
	system.RunCmdCompat(ctx, []string{"ip", "link", "set", bridge, "down"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Text: true})

	// Delete bridge with type
	result := system.RunCmdCompat(ctx, []string{"ip", "link", "delete", bridge, "type", "bridge"},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Text: true})
	if result.Success {
		return nil
	}
	stderrFirst := strings.TrimSpace(result.Stderr)

	// Fallback: try without type specifier
	result = system.RunCmdCompat(ctx, []string{"ip", "link", "delete", bridge},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Text: true})
	if result.Success {
		return nil
	}

	details := ""
	if stderrFirst != "" {
		details = fmt.Sprintf(" (%s)", stderrFirst)
	}
	return errs.Wrap(errs.CodeNetworkBridgeFailed,
		fmt.Errorf("failed to remove bridge '%s': tried 'ip link delete' with type%s and without", bridge, details))
}

// GetSystemBridges returns all bridge interfaces on the host.
// Python: get_system_bridges() -> list[str]
func GetSystemBridges(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(ctx, []string{"ip", "-o", "link", "show", "type", "bridge"},
		system.RunCmdOpts{Capture: true, Check: false, Text: true})
	if !result.Success {
		return nil
	}
	var bridges []string
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			bridges = append(bridges, strings.TrimRight(parts[1], ":"))
		}
	}
	return bridges
}

// FlushARP flushes the ARP cache for a bridge interface.
// Python: flush_arp(bridge, privileged=True) -> None
func FlushARP(ctx context.Context, bridge string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	system.RunCmdCompat(ctx, []string{"ip", "neigh", "flush", "dev", bridge},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false, Text: true})
}

// IPToUint32 converts an IPv4 address to a uint32.
func IPToUint32(ip net.IP) uint32 {
	ip = ip.To4()
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3])
}

// IntToIP converts a uint32 to an IPv4 address.
func IntToIP(n uint32) net.IP {
	return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
}

// ── Internal helpers ──
