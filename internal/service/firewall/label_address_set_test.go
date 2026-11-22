package firewall

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/google/nftables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLabelAddressSetUpdatePreservesUnchangedAndTimedSets(t *testing.T) {
	intent := secondaryDurableIntent()
	generation, err := intent.compile()
	require.NoError(t, err)
	endpoint := intent.Endpoints[0]
	old, err := newLabelAddressSet(nftables.TableFamilyBridge, []EndpointBinding{endpoint})
	require.NoError(t, err)
	endpoint.IPv4 = netip.MustParseAddr("10.0.0.3")
	retained, err := newLabelAddressSet(nftables.TableFamilyBridge, []EndpointBinding{endpoint, endpoint})
	require.NoError(t, err)
	require.Len(t, retained.keys, 1)
	endpoint.IPv4 = netip.MustParseAddr("10.0.0.4")
	added, err := newLabelAddressSet(nftables.TableFamilyBridge, []EndpointBinding{endpoint})
	require.NoError(t, err)

	before, target := generation.bridge, generation.bridge
	before.labelAddressSets = sortedCopy([]labelAddressSet{old, retained}, func(set labelAddressSet) string {
		return set.name
	})
	target.labelAddressSets = sortedCopy([]labelAddressSet{retained, added}, func(set labelAddressSet) string {
		return set.name
	})
	require.NoError(t, validateLabelAddressSets(target.labelAddressSets))

	connection := &recordingKernelApplicationConnection{}
	require.NoError(t, queueKernelApplicationTable(connection, target, before, nil))
	assert.Contains(t, connection.events, "delete label address set "+old.name)
	assert.Contains(t, connection.events, "add label address set "+added.name+" keys=1")
	assert.NotContains(t, connection.events, "delete label address set "+retained.name)
	assert.NotContains(t, connection.events, "add label address set "+retained.name+" keys=1")
	assert.NotContains(t, connection.events, "delete secondary set "+before.secondarySet.Name)
	assert.NotContains(t, connection.events, "add secondary set "+before.secondarySet.Name)
	removeAt := slices.Index(connection.events, "delete label address set "+old.name)
	createAt := slices.Index(connection.events, "add label address set "+added.name+" keys=1")
	assert.Greater(t, removeAt, 0, "flush existing references before removing the set")
	assert.Greater(t, createAt, removeAt)
}

func TestKernelComparisonRefusesChangedLabelSetMembership(t *testing.T) {
	intent := secondaryDurableIntent()
	generation, err := intent.compile()
	require.NoError(t, err)
	set, err := newLabelAddressSet(nftables.TableFamilyBridge, intent.Endpoints)
	require.NoError(t, err)
	generation.bridge.labelAddressSets = []labelAddressSet{set}
	observed := kernelObservationFromCompiled(generation).tables[0]
	observed.labelAddressSets = nil
	require.ErrorContains(
		t,
		compareKernelObservedTable(observed, generation.bridge, generation.id),
		"label address sets",
	)

	observed.labelAddressSets = []labelAddressSet{set}
	require.NoError(t, compareKernelObservedTable(observed, generation.bridge, generation.id))
	observed.labelAddressSets[0].keys = []labelAddressKey{testLabelAddressKey("foreign-veth", [4]byte{10, 0, 0, 2})}
	require.ErrorContains(
		t,
		compareKernelObservedTable(observed, generation.bridge, generation.id),
		"label address sets",
	)
}
