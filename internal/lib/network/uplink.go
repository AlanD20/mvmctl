package network

import (
	"net/netip"
	"regexp"
)

var uplinkNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)

// ValidUplinkName checks interface syntax before root resolves the link in its host namespace.
func ValidUplinkName(name string) bool {
	return name != "." && name != ".." && name != "auto" && name != "none" &&
		uplinkNamePattern.MatchString(name)
}

// ParseUplinkGateway admits a canonical IPv4 next hop. Reachability is checked by root.
func ParseUplinkGateway(value string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(value)
	return addr, err == nil && addr.Is4() && addr.IsGlobalUnicast() && value == addr.String()
}
