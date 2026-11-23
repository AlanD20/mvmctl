package firewall

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLabelAddressSetUpdatePreservesUnchangedAndTimedSets(t *testing.T) {
	intent := secondaryDurableIntent()
	generation, err := intent.compile()
	require.NoError(t, err)
	first := testLabelAddressKey("mvma0", [4]byte{10, 0, 0, 2})
	second := testLabelAddressKey("mvma0", [4]byte{10, 0, 0, 3})
	third := testLabelAddressKey("mvma0", [4]byte{10, 0, 0, 4})
	old := newLabelAddressSet([]labelAddressKey{first})
	retained := newLabelAddressSet([]labelAddressKey{second, second})
	require.Len(t, retained.keys, 1)
	added := newLabelAddressSet([]labelAddressKey{third})

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
	set := newLabelAddressSet([]labelAddressKey{testLabelAddressKey("mvma0", [4]byte{10, 0, 0, 2})})
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
