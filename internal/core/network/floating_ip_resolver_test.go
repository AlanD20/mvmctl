package network_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
)

// This read-only inventory is the resolver's domain input, not a privileged transport mock.
type floatingIPInventoryFixture []*model.FloatingIPItem

func (items floatingIPInventoryFixture) ListFloatingIPs(
	_ context.Context,
	networkID string,
) ([]*model.FloatingIPItem, error) {
	var selected []*model.FloatingIPItem
	for _, item := range items {
		if item.NetworkID == networkID {
			selected = append(selected, item)
		}
	}
	return selected, nil
}

func TestFloatingIPResolverFullIDCannotBeShadowedByResourceName(t *testing.T) {
	a, b, netID := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("e", 64)
	items := floatingIPInventoryFixture{
		{ID: a, NetworkID: netID, Name: b, IPv4: "10.0.0.2", Generation: 1},
		{ID: b, NetworkID: netID, Name: "target", IPv4: "10.0.0.3", Generation: 1},
	}
	got, err := network.NewFloatingIPResolver(items).Resolve(t.Context(), netID, b)
	require.NoError(t, err)
	want := &model.FloatingIPItem{ID: b, NetworkID: netID, Name: "target", IPv4: "10.0.0.3", Generation: 1}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("full ID selected a differently named resource (-want +got):\n%s", diff)
	}
}

func TestFloatingIPResolverRejectsAmbiguousPrefixAndKeepsNetworkScope(t *testing.T) {
	netID, other := strings.Repeat("e", 64), strings.Repeat("f", 64)
	items := floatingIPInventoryFixture{
		{ID: "aa" + strings.Repeat("1", 30), NetworkID: netID, Name: "first", IPv4: "10.0.0.2", Generation: 1},
		{ID: "aa" + strings.Repeat("2", 30), NetworkID: netID, Name: "second", IPv4: "10.0.0.3", Generation: 1},
		{ID: strings.Repeat("b", 32), NetworkID: other, Name: "foreign", IPv4: "10.1.0.2", Generation: 1},
	}
	resolver := network.NewFloatingIPResolver(items)
	_, err := resolver.Resolve(t.Context(), netID, "aa")
	require.Error(t, err)
	_, err = resolver.Resolve(t.Context(), netID, "foreign")
	require.Error(t, err)
	got, err := resolver.Resolve(t.Context(), netID, "10.0.0.3")
	require.NoError(t, err)
	if diff := cmp.Diff(items[1], got); diff != "" {
		t.Fatalf("address resolution (-want +got):\n%s", diff)
	}
}
