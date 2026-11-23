package network

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rationale: A rejected post-create alias must not strand an unlabelled bridge that
// the next managed-network create cannot safely adopt or remove.
func TestUnpublishedUnlabeledBridgeRollbackIsExact(t *testing.T) {
	t.Parallel()

	newTransaction := func(t *testing.T) (*linkEffectFixture, *managedNetworkTransaction, linkIdentity) {
		t.Helper()
		fixture := newLinkEffectFixture(t)
		bridge := linkIdentity{name: fixture.names.managedBridge, ifIndex: 10, namespace: fixture.hostNamespace}
		fixture.route.put(fixture.hostFD, routeLinkObservation{
			name: bridge.name, kind: linkKindBridge, ifIndex: bridge.ifIndex,
		})
		return fixture, &managedNetworkTransaction{
			route: fixture.route,
			host:  fixture.lease(fixture.hostFD, fixture.hostNamespace),
		}, bridge
	}

	t.Run("remove exact unpublished bridge", func(t *testing.T) {
		fixture, transaction, bridge := newTransaction(t)
		cause := errors.New("alias mutation rejected")
		err := transaction.removeUnpublishedUnlabeledBridge(t.Context(), bridge, cause)
		require.ErrorIs(t, err, cause)
		assert.Nil(t, fixture.route.link(fixture.hostFD, bridge.name))
	})

	t.Run("refuse foreign alias", func(t *testing.T) {
		fixture, transaction, bridge := newTransaction(t)
		link := fixture.route.mustLink(t, fixture.hostFD, bridge.name)
		link.alias = "foreign"
		fixture.route.put(fixture.hostFD, link)
		err := transaction.removeUnpublishedUnlabeledBridge(t.Context(), bridge, errors.New("alias failed"))
		require.Error(t, err)
		assert.Equal(t, "foreign", fixture.route.mustLink(t, fixture.hostFD, bridge.name).alias)
		assert.Zero(t, fixture.route.deleteCount())
	})

	t.Run("refuse new child", func(t *testing.T) {
		fixture, transaction, bridge := newTransaction(t)
		fixture.route.put(fixture.hostFD, routeLinkObservation{
			name: "foreign0", kind: "dummy", ifIndex: 99, master: bridge.ifIndex,
		})
		err := transaction.removeUnpublishedUnlabeledBridge(t.Context(), bridge, errors.New("alias failed"))
		require.Error(t, err)
		assert.NotNil(t, fixture.route.link(fixture.hostFD, bridge.name))
		assert.Zero(t, fixture.route.deleteCount())
	})
}
