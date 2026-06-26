package network

import (
	"context"
	"net/netip"
	"time"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
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

// CheckIPTablesCommentAvailable checks if iptables supports comments.
// Uses the comment match module to verify availability.
func CheckIPTablesCommentAvailable(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, _ := system.DefaultRunner.Run(
		ctx,
		[]string{"iptables", "-m", "comment", "--comment", "test", "-L"},
		system.RunCmdOpts{Check: false},
	)
	return result.Success()
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
