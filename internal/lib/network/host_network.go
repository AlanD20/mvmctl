package network

import (
	"net/netip"

	"mvmctl/internal/lib/model"
)

// FindNetworkByName finds a network by name from a list.
func FindNetworkByName(networks []*model.NetworkItem, name string) *model.NetworkItem {
	for _, n := range networks {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// SubnetsOverlap checks if two CIDR subnets overlap.
// Uses netip.Prefix.Overlaps for overlap detection.
func SubnetsOverlap(a, b string) bool {
	n1, err1 := netip.ParsePrefix(a)
	n2, err2 := netip.ParsePrefix(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return n1.Overlaps(n2)
}
