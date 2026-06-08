package network

import (
	"crypto/sha256"
	"fmt"
	"os"
	"time"
)

// VMGenerateMAC generates a MAC address from the given prefix using time+nano+pid.
// Matches Python's NetworkUtils.generate_mac() logic exactly.
func VMGenerateMAC(prefix string) string {
	if prefix == "" {
		prefix = "02:FC"
	}
	return prefix + fmt.Sprintf(":%02x:%02x:%02x:%02x",
		time.Now().UnixNano()&0xff,
		os.Getpid()&0xff,
		time.Now().UnixNano()>>8&0xff,
		time.Now().UnixNano()>>16&0xff)
}

// VMGenerateTAPName generates a TAP device name from network and VM names.
// Matches Python's VMUtils.generate_tap_name() logic exactly.
func VMGenerateTAPName(netName, vmName string) string {
	h := sha256.Sum256([]byte(netName + ":" + vmName))
	return fmt.Sprintf("tap-%x", h[:4])
}
