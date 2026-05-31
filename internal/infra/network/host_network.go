package network

import (
	"context"
	"net/netip"
	"time"

	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// FindNetworkByName finds a network by name from a list.
func FindNetworkByName(networks []*model.Network, name string) *model.Network {
	for _, n := range networks {
		if n.Name == name {
			return n
		}
	}
	return nil
}

// CheckIPTablesCommentAvailable checks if iptables supports comments.
// Uses the comment match module to verify availability.
func CheckIPTablesCommentAvailable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := system.RunCmdCompat(
		ctx,
		[]string{"iptables", "-m", "comment", "--comment", "test", "-L"},
		system.RunCmdOptions{Check: false},
	)
	return result.Success
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
