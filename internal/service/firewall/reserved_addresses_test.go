package firewall

import (
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/require"
)

func TestReservedNetworkIPv4IncludesStoppedPrimaryAndExcludesOtherNetwork(t *testing.T) {
	intent := Intent{
		// No active endpoint: these addresses survive stopped topology cleanup.
		Reservations: []EndpointReservation{
			{OwnerUID: 1000, NetworkID: "own", IPv4: netip.MustParseAddr("10.10.0.2")},
			{OwnerUID: 1001, NetworkID: "other", IPv4: netip.MustParseAddr("10.20.0.2")},
		},
	}
	got, err := reservedNetworkIPv4(intent, EndpointBinding{OwnerUID: 1000, NetworkID: "own"})
	require.NoError(t, err)
	want := []netip.Addr{netip.MustParseAddr("10.10.0.2")}
	if diff := cmp.Diff(want, got, cmpopts.EquateComparable(netip.Addr{})); diff != "" {
		t.Fatalf("durable primary reservations (-want +got):\n%s", diff)
	}
	intent.Reservations[0].OwnerUID = 1001
	_, err = reservedNetworkIPv4(intent, EndpointBinding{OwnerUID: 1000, NetworkID: "own"})
	require.Error(t, err, "a conflicting owner cannot be treated as a usable reservation index")
}
