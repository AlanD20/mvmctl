package network

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"mvmctl/internal/infra"
)

// SyncResult holds the result of a SyncIPTablesRules operation.
type SyncResult struct {
	Added    int // Rules added to host iptables
	Verified int // Rules already present in host iptables
	Orphaned int // Host rules not tracked in DB
}

// --- Network utilities ---

// ComputeBridgeAddress returns gateway IP with subnet prefix.
func ComputeBridgeAddress(gateway, subnet string) (string, error) {
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return "", fmt.Errorf("invalid subnet: %s", subnet)
	}
	ones, _ := ipnet.Mask.Size()
	return fmt.Sprintf("%s/%d", gateway, ones), nil
}

// ComputeBridgeName generates a 15-char bridge name from the network name.
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
func GenerateTAPName(networkName, vmName string) string {
	raw := fmt.Sprintf("%s-%s", networkName, vmName)
	hash := sha256Hex(raw)[:11]
	return fmt.Sprintf("%s-%s", infra.CLIName, hash)
}
