package validators

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/network"
	"mvmctl/pkg/errs"
)

// Pre-compiled regexes — Python caches them at module level, so Go should too.
// (Deviation fix: adds sync.Once compilation caching for all regex operations.)
var (
	// Valid boot arg component pattern (Python: re.search(r"[\s;|&$`\\\"']", value))
	// Uses raw string with concatenation for the backtick, then adds \"'] literally.
	// In raw strings `...`, \" is \ (write once) followed by " — correct for regex char class.
	validBootArgComponentRegex = regexp.MustCompile(`[\s;|&$` + "`" + `\\\"']`)
	// Valid UUID pattern (Python: uuid_pattern.match(...)) — anchored at START only.
	// Python uses re.match() which anchors at the start, but does NOT anchor at the end.
	// This means "valid-uuid-here-extra-chars" passes Python but would fail ^...$.
	// We match Python's behavior exactly: start-only anchor.
	validUUIDRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// Valid interface/bridge name pattern (Python: re.match(r"^[a-z0-9_-]+$", ...))
	validInterfaceNameRegex = regexp.MustCompile(`^[a-z0-9_-]+$`)
)

// ── Valid SSH username regex (Python: ^[a-z_][a-z0-9_-]*$) ──
var validSSHUsernameRegex = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// ── MAC address strict regex ──
var validMACRegex = regexp.MustCompile(`^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$`)

// ValidSemverRegex matches semver-like version strings (e.g. "1.15" or "1.15.0").
// Used for version format validation across domains.
var ValidSemverRegex = regexp.MustCompile(`^\d+\.\d+(\.\d+)?$`)

// Linux IFNAMSIZ limit for interface names
const ifnamSiz = 15

// ── Reserved interface names ──
var ReservedInterfaces = map[string]bool{
	"lo": true, "eth0": true, "eth1": true,
	"wlan0": true, "virbr0": true, "docker0": true,
}

// ══════════════════════════════════════════════════════════════════════════════
// Name validation (Python: CommonUtils.validate_entity_name)
// ══════════════════════════════════════════════════════════════════════════════

// validNameRegex matches Python's ^[a-z0-9][a-z0-9._-]{0,62}$
var validNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// EntityName validates any entity name (VM, network, image, kernel, key, binary).
// Matches Python's CommonUtils.validate_entity_name().
// Returns *errs.DomainError matching Python's MVMError pattern.
func EntityName(name, entityType string, maxLength int) error {
	if maxLength <= 0 {
		maxLength = 63
	}
	if name == "" {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid %s name: cannot be empty", entityType))
	}
	if len(name) > maxLength {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf(
			"Invalid %s name '%s': exceeds maximum length of %d characters",
			entityType,
			name,
			maxLength,
		))
	}
	if strings.HasPrefix(name, "-") {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid %s name '%s': cannot start with a hyphen", entityType, name),
		)
	}
	if infra.IsReservedName(name) {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid %s name '%s': '%s' is a reserved name", entityType, name, name),
		)
	}
	if infra.ContainsDangerousChars(name) {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf(
			"Invalid %s name '%s': contains forbidden characters (shell metacharacters, path traversal, or control characters)",
			entityType,
			name,
		))
	}
	// Check if name is an IP address
	if IsIPAddress(name) {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid %s name '%s': cannot be an IP address", entityType, name),
		)
	}
	if !validNameRegex.MatchString(name) {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid %s name '%s': must match ^[a-z0-9][a-z0-9._-]{0,62}$", entityType, name),
		)
	}
	return nil
}

// ValidateName validates a general entity name (1-63 chars, alphanumeric, dot, underscore, hyphen).
// Wrapper around EntityName with default type and max length.
// Returns *errs.DomainError matching Python's MVMError pattern.
func Name(name string) error {
	return EntityName(name, "name", 63)
}

// ══════════════════════════════════════════════════════════════════════════════
// CIDR / IP / Port validation
// ══════════════════════════════════════════════════════════════════════════════

func CIDR(cidr string) error {
	if cidr == "" {
		return errs.New(errs.CodeValidationFailed, "Invalid subnet: cannot be empty")
	}
	if strings.Contains(cidr, " ") {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid subnet: '%s' cannot contain spaces", cidr))
	}
	// Enforce IPv4 only, matching Python's ipaddress.IPv4Network
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil || ipnet.IP.To4() == nil {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid subnet: '%s' is not a valid IPv4 CIDR: %v", cidr, err),
		)
	}
	return nil
}

func IP(ip string) error {
	if net.ParseIP(ip) == nil {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid IP address: %s", ip))
	}
	return nil
}

func Port(port int) error {
	if port < 1 || port > 65535 {
		return errs.New(errs.CodeValidationFailed, "Port must be 1-65535")
	}
	return nil
}

func IsIPAddress(s string) bool {
	return net.ParseIP(s) != nil
}

// ══════════════════════════════════════════════════════════════════════════════
// IPv4 address validation (matching Python's NetworkValidator.validate_ipv4_address)
// ══════════════════════════════════════════════════════════════════════════════

func IPv4Address(ip string, fieldName string, requirePrivate bool, subnet string, gateway string) error {
	if fieldName == "" {
		fieldName = "IP address"
	}
	if ip == "" {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid %s: cannot be empty", fieldName))
	}
	if strings.Contains(ip, " ") {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid %s: '%s' cannot contain spaces", fieldName, ip))
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid %s: '%s' is not a valid IPv4 address", fieldName, ip),
		)
	}
	if requirePrivate && !parsed.IsPrivate() {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid %s: '%s' must be a private/internal address", fieldName, ip),
		)
	}
	if subnet != "" {
		_, ipnet, err := net.ParseCIDR(subnet)
		if err != nil {
			return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid subnet: %v", err))
		}
		if !ipnet.Contains(parsed) {
			return errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("Invalid %s: '%s' is not within subnet %s", fieldName, ip, subnet),
			)
		}
		if parsed.Equal(ipnet.IP) {
			return errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("Invalid %s: '%s' is the network address of %s", fieldName, ip, subnet),
			)
		}
	}
	if gateway != "" {
		gw := net.ParseIP(gateway)
		if gw != nil && parsed.Equal(gw) {
			return errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("Invalid %s: '%s' is the gateway address", fieldName, ip),
			)
		}
	}
	return nil
}

// ══════════════════════════════════════════════════════════════════════════════
// KeyValidator (Python: KeyValidator)
// ══════════════════════════════════════════════════════════════════════════════

func KeyName(name string) error {
	return EntityName(name, "key", 63)
}

// ══════════════════════════════════════════════════════════════════════════════
// VolumeValidator (Python: VolumeValidator)
// ══════════════════════════════════════════════════════════════════════════════

func VolumeName(name string) error {
	return EntityName(name, "volume", 63)
}

// ══════════════════════════════════════════════════════════════════════════════
// CoerceBool helper
// ══════════════════════════════════════════════════════════════════════════════

func CoerceBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

// ══════════════════════════════════════════════════════════════════════════════
// Network validation (Python: NetworkValidator)
// ══════════════════════════════════════════════════════════════════════════════

func NetworkName(name string) error {
	// Apply common entity name validation first (uses max_length=31 for networks)
	if err := EntityName(name, "network", 31); err != nil {
		return err
	}
	// Network names must not contain dots
	if strings.Contains(name, ".") {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid network name '%s': cannot contain dots", name))
	}
	// Network names must not be reserved interface names
	if ReservedInterfaces[strings.ToLower(name)] {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid network name '%s': '%s' is a reserved interface name", name, name),
		)
	}
	// Network names must not start with CLI_NAME- prefix (reserved for bridges)
	if strings.HasPrefix(name, infra.CLIName+"-") {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf(
			"Invalid network name '%s': cannot start with '%s-' (reserved for bridge names)",
			name,
			infra.CLIName,
		))
	}
	return nil
}

func MAC(mac string) error {
	if !validMACRegex.MatchString(mac) {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid MAC address format: %s", mac))
	}
	return nil
}

// IsMAC checks if a string looks like a MAC address (6 colon-separated hex pairs).
func IsMAC(identifier string) bool {
	parts := strings.Split(identifier, ":")
	if len(parts) != 6 {
		return false
	}
	for _, p := range parts {
		if len(p) != 2 {
			return false
		}
		for _, c := range p {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

func Subnet(subnet string) (string, error) {
	if subnet == "" {
		return "", errs.New(errs.CodeValidationFailed, "Invalid subnet: cannot be empty")
	}
	if strings.Contains(subnet, " ") {
		return "", errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid subnet: '%s' cannot contain spaces", subnet),
		)
	}
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid subnet: '%s' is not a valid IPv4 CIDR: %v", subnet, err),
		)
	}
	if ipnet.IP.To4() == nil {
		return "", errs.New(errs.CodeValidationFailed, fmt.Sprintf(
			"Invalid subnet: '%s' is not a valid IPv4 CIDR: '%s' does not appear to be an IPv4 network",
			subnet,
			subnet,
		))
	}
	// Python uses strict=False in validate_subnet, so host bits are accepted.
	// net.ParseCIDR already strips/zeroes host bits, so we just return the
	// normalized network address (e.g. "10.0.0.0/24" for "10.0.0.1/24").
	return ipnet.String(), nil
}

func IPv4Gateway(gateway string, subnet string) (string, error) {
	if gateway == "" {
		return "", errs.New(errs.CodeValidationFailed, "Invalid gateway: cannot be empty")
	}
	if strings.Contains(gateway, " ") {
		return "", errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid gateway: '%s' cannot contain spaces", gateway),
		)
	}
	parsed := net.ParseIP(gateway)
	if parsed == nil || parsed.To4() == nil {
		return "", errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid gateway: '%s' is not a valid IPv4 address", gateway),
		)
	}
	if !parsed.IsPrivate() {
		return "", errs.New(errs.CodeValidationFailed, fmt.Sprintf(
			"Invalid gateway: '%s' must be a private/internal address. Use a subnet from RFC1918 ranges: 10.0.0.0/8, 172.16.0.0/12, or 192.168.0.0/16",
			gateway,
		))
	}
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid subnet: %v", err))
	}
	if !ipnet.Contains(parsed) {
		return "", errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid gateway: '%s' is not within subnet %s", gateway, subnet),
		)
	}
	if parsed.Equal(ipnet.IP) {
		return "", errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid gateway: '%s' is the network address of %s", gateway, subnet),
		)
	}
	return parsed.String(), nil
}

func BridgeName(ctx context.Context, bridge string) error {
	if bridge == "" {
		return errs.New(errs.CodeValidationFailed, "Invalid bridge name: cannot be empty")
	}
	if len(bridge) > ifnamSiz {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid bridge name: '%s' exceeds maximum length of %d", bridge, ifnamSiz),
		)
	}
	if strings.HasPrefix(bridge, "-") {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid bridge name: '%s' cannot start with a hyphen", bridge),
		)
	}
	if infra.ContainsDangerousChars(bridge) {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf(
			"Invalid bridge name: '%s' contains forbidden characters (shell metacharacters, path traversal, or control characters)",
			bridge,
		))
	}
	if !validInterfaceNameRegex.MatchString(bridge) {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf(
			"Invalid bridge name: '%s' must contain only lowercase alphanumeric, hyphen, and underscore characters",
			bridge,
		))
	}
	// Check if bridge already exists on host (non-mvm interface)
	if network.BridgeExists(ctx, bridge) {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Bridge '%s' already exists on this host", bridge))
	}
	return nil
}

func NATGateways(ctx context.Context, gateways []string) ([]string, error) {
	if len(gateways) == 0 {
		return nil, errs.New(errs.CodeValidationFailed, "NAT gateways cannot be empty")
	}
	var validated []string
	for _, iface := range gateways {
		iface = strings.TrimSpace(iface)
		if iface == "" {
			return nil, errs.New(errs.CodeValidationFailed, "NAT gateway interface name cannot be empty")
		}
		if len(iface) > ifnamSiz {
			return nil, errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("Invalid NAT gateway '%s': exceeds maximum length of %d", iface, ifnamSiz),
			)
		}
		if infra.ContainsDangerousChars(iface) {
			return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf(
				"Invalid NAT gateway '%s': contains forbidden characters (shell metacharacters, path traversal, or control characters)",
				iface,
			))
		}
		if !validInterfaceNameRegex.MatchString(iface) {
			return nil, errs.New(errs.CodeValidationFailed, fmt.Sprintf(
				"Invalid NAT gateway '%s': must contain only lowercase alphanumeric, hyphen, and underscore characters",
				iface,
			))
		}
		// Check that interface actually exists on the host
		if err := network.EnsureInterfaceReady(ctx, iface); err != nil {
			return nil, errs.New(
				errs.CodeValidationFailed,
				fmt.Sprintf("NAT gateway '%s': interface does not exist on this host", iface),
			)
		}
		validated = append(validated, iface)
	}
	return validated, nil
}

// networkRange returns the first and last IP addresses in the given network.
func networkRange(ipnet *net.IPNet) (net.IP, net.IP) {
	first := ipnet.IP.To4()
	if first == nil {
		return nil, nil
	}
	mask := ipnet.Mask
	last := make(net.IP, 4)
	for i := range 4 {
		last[i] = first[i] | ^mask[i]
	}
	return first, last
}

// ipCmp compares two IPv4 addresses, returning -1 if a<b, 0 if a==b, 1 if a>b.
func ipCmp(a, b net.IP) int {
	for i := range 4 {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// cidrsOverlap returns true if two IPv4 CIDR ranges overlap.
// Python's ipaddress.IPv4Network.overlaps() uses proper range comparison.
func cidrsOverlap(a, b *net.IPNet) bool {
	aFirst, aLast := networkRange(a)
	bFirst, bLast := networkRange(b)
	if aFirst == nil || bFirst == nil {
		return false
	}
	return ipCmp(aFirst, bLast) <= 0 && ipCmp(bFirst, aLast) <= 0
}

func SubnetNoOverlap(subnet string, existing []any, excludeName string) error {
	_, newNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf("Invalid subnet: %v", err))
	}
	// Python uses strict=True in subnet overlap check, which rejects host bits.
	// net.ParseCIDR strips host bits, so we must check if the input had host bits set.
	// Parse the IP portion separately to compare with normalized network address.
	parts := strings.SplitN(subnet, "/", 2)
	inputIP := net.ParseIP(parts[0])
	if inputIP != nil && !inputIP.Equal(newNet.IP) {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid subnet: '%s' has host bits set (use strict=True)", subnet),
		)
	}
	for _, item := range existing {
		name := duckTypedName(item)
		itemSubnet := duckTypedSubnet(item)
		if name == "" || itemSubnet == "" {
			continue
		}
		if name == excludeName {
			continue
		}
		_, existingNet, err := net.ParseCIDR(itemSubnet)
		if err != nil {
			continue
		}
		if cidrsOverlap(newNet, existingNet) {
			return errs.New(
				errs.CodeNetworkSubnetOverlap,
				fmt.Sprintf("Subnet %s overlaps with network '%s' (%s)", subnet, name, itemSubnet),
			)
		}
	}
	return nil
}

// Namable is implemented by types that have a Name field (matching Python's
// duck-typed item.name access pattern).
type Namable interface {
	GetName() string
}

// SubnetProvider is implemented by types that have a Subnet field (matching
// Python's duck-typed item.subnet access pattern).
type SubnetProvider interface {
	GetSubnet() string
}

// duckTypedName returns the Name from an item implementing Namable.
func duckTypedName(v any) string {
	if n, ok := v.(Namable); ok {
		return n.GetName()
	}
	return ""
}

// duckTypedSubnet returns the Subnet from an item implementing SubnetProvider.
func duckTypedSubnet(v any) string {
	if s, ok := v.(SubnetProvider); ok {
		return s.GetSubnet()
	}
	return ""
}

// ══════════════════════════════════════════════════════════════════════════════
// VMValidator (Python: VMValidator)
// ══════════════════════════════════════════════════════════════════════════════

func VMName(name string) error {
	return EntityName(name, "VM", 63)
}

func BootArgComponent(value, componentName string) error {
	if componentName == "" {
		componentName = "boot arg"
	}
	if value == "" {
		return nil
	}
	if validBootArgComponentRegex.MatchString(value) {
		return errs.New(errs.CodeValidationFailed, fmt.Sprintf(
			"Invalid %s '%s': must not contain spaces or shell metacharacters",
			componentName,
			value,
		))
	}
	return nil
}

func SSHUsername(user string) error {
	if !validSSHUsernameRegex.MatchString(user) {
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("Invalid SSH username '%s': must match ^[a-z_][a-z0-9_-]*$", user),
		)
	}
	return nil
}

func BootArgs(bootArgs, rootUUID, guestIP string) []string {
	var errors []string
	if rootUUID == "" {
		errors = append(errors, "root UUID is required")
	}
	if guestIP == "" {
		errors = append(errors, "guest IP is required")
	}
	if bootArgs != "" {
		for _, arg := range strings.Fields(bootArgs) {
			if strings.Contains(arg, "=") {
				parts := strings.SplitN(arg, "=", 2)
				key, value := parts[0], parts[1]
				if err := BootArgComponent(value, key); err != nil {
					errors = append(errors, err.Error())
				}
			} else {
				if err := BootArgComponent(arg, "boot arg"); err != nil {
					errors = append(errors, err.Error())
				}
			}
		}
		// Also check root UUID format if present
		// Python: uuid_pattern.match(root_uuid) — anchored at start only (no $).
		if strings.Contains(bootArgs, "root_uuid") && rootUUID != "" {
			if !validUUIDRegex.MatchString(rootUUID) {
				errors = append(errors, fmt.Sprintf("Invalid root UUID format: %s", rootUUID))
			}
		}
	}
	return errors
}

// ══════════════════════════════════════════════════════════════════════════════
// Port range parsing (migrated from host domain — verdict #33)
// ══════════════════════════════════════════════════════════════════════════════

// ParsePortRange parses an "low,high" port range string.
// On parse failure, returns a default range silently (matching Python's try/except pass).
// NOTE: There's a duplicate copy in pkg/api/host.go:1106 — if modifying, update both.
func ParsePortRange(s string) [2]int {
	var low, high int
	n, _ := fmt.Sscanf(s, "%d,%d", &low, &high)
	if n != 2 {
		return infra.DefaultIPLocalPortRange
	}
	return [2]int{low, high}
}

// ToInt safely extracts an int from any numeric type.
// IsDigits returns true if the string contains only ASCII digits (0-9) and is non-empty.
// Intended for PID directory validation in /proc — NOT unicode.IsDigit which matches
// non-ASCII digits (Arabic-Indic, etc.) that would not be valid PID directory names.
func IsDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}

// ToInt coerces a value to int, matching Python's int() behavior.
// Returns an error if the value is not a numeric type or is nil.
func ToInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	case uint64:
		return int(n), nil
	case nil:
		return 0, fmt.Errorf("expected numeric value, got nil")
	default:
		return 0, fmt.Errorf("expected numeric value, got %T", v)
	}
}
