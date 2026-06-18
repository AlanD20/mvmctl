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

// --- Private helpers ---

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

// --- Subnet Math & Computation ---

// ComputeSubnetMask returns netmask from CIDR subnet.
func ComputeSubnetMask(subnet string) string {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d.%d", ipnet.Mask[0], ipnet.Mask[1], ipnet.Mask[2], ipnet.Mask[3])
}

// ComputePrefixLength returns prefix length from CIDR subnet.
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
	if ipnet == nil {
		return 0
	}
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
func ComputeBridgeAddress(gateway, subnet string) string {
	prefix := ComputePrefixLength(subnet)
	return fmt.Sprintf("%s/%d", gateway, prefix)
}

// ComputeBridgeName computes bridge name from network name.
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

// --- Naming & Generation ---

// GenerateMAC generates a MAC address with the given prefix.
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
	return strings.ToUpper(mac)
}

// GenerateTAPName generates a unique TAP device name (max 16 chars for IFNAMSIZ).
func GenerateTAPName(cliName, networkName, vmName string) string {
	raw := fmt.Sprintf("%s-%s", networkName, vmName)
	tapHash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))[:11]
	return fmt.Sprintf("%s-%s", cliName, tapHash)
}

// --- IP Allocation ---

// AllocateNextIP allocates the next available IP in a subnet.
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

// --- System Queries (Host State) ---

var virtualInterfacePrefixes = []string{"mvm-", "tap", "br-", "virbr", "docker", "veth"}
var excludedInterfaces = []string{"lo"}

// GetPhysicalInterfaces returns available physical network interfaces.
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
	slices.Sort(interfaces)
	return interfaces, nil
}

// DetectOutboundInterface returns the outbound (default route) network interface.
func DetectOutboundInterface(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := system.DefaultRunner.Run(
		ctx,
		[]string{"ip", "route", "show", "default"},
		system.RunCmdOpts{Check: false, Capture: true},
	)
	if err != nil || !result.Success() {
		slog.Debug("Failed to detect outbound network interface", "error", err)
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

// GetTunTapDevices lists all TUN/TAP devices.
func GetTunTapDevices(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(
		ctx,
		[]string{"ip", "-o", "link", "show", "type", "tuntap"},
		system.RunCmdOpts{Check: false, Capture: true},
	)
	if !result.Success() {
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
func GetBridges(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(
		ctx,
		[]string{"ip", "-o", "link", "show", "type", "bridge"},
		system.RunCmdOpts{Check: false, Capture: true},
	)
	if !result.Success() {
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

// EnsureInterfaceReady ensures a network interface exists and is usable for NAT.
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

	result, err := system.DefaultRunner.Run(ctx, []string{"ip", "-o", "-4", "addr", "show", iface}, system.RunCmdOpts{
		Check:   false,
		Capture: true,
	})
	if err != nil {
		return errs.New(errs.CodeNetworkError, "'ip' command not found — install iproute2")
	}

	if !result.Success() || strings.TrimSpace(result.Stdout) == "" {
		return errs.New(errs.CodeNetworkError,
			fmt.Sprintf(
				"Interface '%s' has no IPv4 address assigned. NAT requires an interface with a valid IP address.",
				iface,
			),
		)
	}

	return nil
}

// --- Internal Helpers ---

// StripTapRules strips TAP-related rules from iptables rules text.
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
type BackendConflictResult struct {
	HasConflict bool
	Diagnosis   string
}

func DetectIPTablesBackendConflict(ctx context.Context) BackendConflictResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	versionResult, _ := system.DefaultRunner.Run(
		ctx,
		[]string{"iptables", "--version"},
		system.RunCmdOpts{Check: false, Capture: true},
	)
	currentBackend := "legacy"
	if versionResult.Success() && strings.Contains(versionResult.Stderr, "nf_tables") {
		currentBackend = "nft"
	}

	legacyActive := false
	legacyResult, _ := system.DefaultRunner.Run(ctx, []string{"iptables-legacy", "-L", "-n", "-v"}, system.RunCmdOpts{
		Check:      false,
		Capture:    true,
		Privileged: true,
	})
	if legacyResult.Success() {
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
	nftResult, _ := system.DefaultRunner.Run(ctx, []string{"iptables", "-L", "-n", "-v"}, system.RunCmdOpts{
		Check:      false,
		Capture:    true,
		Privileged: true,
	})
	if nftResult.Success() {
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

// FlushARP flushes the ARP cache for a bridge interface.
func FlushARP(ctx context.Context, bridge string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	system.DefaultRunner.Run(ctx, []string{"ip", "neigh", "flush", "dev", bridge},
		system.RunCmdOpts{Capture: true, Privileged: true, Check: false})
}

// --- Internal helpers ---
