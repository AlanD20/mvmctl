package network

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFloatingIPDelegationAdministrationPersistsImmutableGrantAndExactRetry(t *testing.T) {
	fixture, resources := floatingIPStoreFixture(t)
	store, err := openFloatingIPDelegationStore(t.Context(), fixture.uid, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.release(context.Background())) })
	resource, principal, seed := delegationFixture(t)
	require.NoError(t, resources.persist(t.Context(), resource, nil))
	created, err := ensureFloatingIPDelegation(t.Context(), store, seed.ID, resource, principal,
		floatingIPDelegationAssociate, 5*time.Second)
	require.NoError(t, err)
	read, _, _, found, err := store.read(t.Context(), created.ID)
	require.NoError(t, err)
	require.True(t, found)
	if diff := cmp.Diff(created, read); diff != "" {
		t.Errorf("persisted grant (-want +got):\n%s", diff)
	}
	retry, err := ensureFloatingIPDelegation(t.Context(), store, seed.ID, resource, principal,
		floatingIPDelegationAssociate, 5*time.Second)
	require.NoError(t, err)
	assert.Equal(t, created.ID, retry.ID)
	_, err = ensureFloatingIPDelegation(t.Context(), store, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", resource,
		principal, floatingIPDelegationAssociate, 5*time.Second)
	require.Error(t, err, "different-ID tuple must not adopt existing authority")
	_, err = ensureFloatingIPDelegation(t.Context(), store, seed.ID, resource, principal,
		floatingIPDelegationAssociate, 6*time.Second)
	require.Error(t, err)
	changed := principal
	changed.launchGeneration++
	_, err = ensureFloatingIPDelegation(t.Context(), store, seed.ID, resource, changed,
		floatingIPDelegationAssociate, 5*time.Second)
	require.Error(t, err)
	foreign := principal
	foreign.ownerUID++
	_, err = ensureFloatingIPDelegation(t.Context(), store, seed.ID, resource, foreign, floatingIPDelegationInspect, 0)
	require.Error(t, err)
	request := FloatingIPDelegationRevokeRequest{NetworkID: resource.NetworkID, ID: created.ID, ExpectedRevision: 2}
	tx := &managedNetworkTransaction{store: fixture.uid}
	_, err = revokeStoredFloatingIPDelegation(t.Context(), tx, store, request)
	require.Error(t, err)
	read, _, _, _, err = store.read(t.Context(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, floatingIPDelegationActive, read.State, "stale revoke must publish nothing")
	request.ExpectedRevision = 1
	revoked, err := revokeStoredFloatingIPDelegation(t.Context(), tx, store, request)
	require.NoError(t, err)
	assert.Equal(t, "revoked", revoked.State)
	assert.Equal(t, uint64(3), revoked.Revision)
	// No VM admission is present in tx: revoking a stopped incarnation remains possible.
	request.ExpectedRevision = 3
	_, err = revokeStoredFloatingIPDelegation(t.Context(), tx, store, request)
	require.NoError(t, err)
	_, err = ensureFloatingIPDelegation(t.Context(), store, seed.ID, resource, principal,
		floatingIPDelegationAssociate, 5*time.Second)
	require.Error(t, err, "revoked identity cannot reactivate")
	fresh, err := ensureFloatingIPDelegation(t.Context(), store, "dddddddddddddddddddddddddddddddd", resource,
		changed, floatingIPDelegationAssociate, 5*time.Second)
	require.NoError(t, err)
	assert.NotEqual(t, created.ID, fresh.ID)
	old, _, _, _, err := store.read(t.Context(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, floatingIPDelegationRevoked, old.State, "regrant cannot revive a retired identity")
}

func TestFloatingIPDelegationAdministrationPagesAndRefusesReboundReplies(t *testing.T) {
	resource, _, grant := delegationFixture(t)
	records := make([]floatingIPDelegationRecord, 0, floatingIPPageSize+1)
	for index := 0; index < floatingIPPageSize+1; index++ {
		item := grant
		item.ID = fmt.Sprintf("%032x", index+1)
		records = append(records, item)
	}
	request := FloatingIPDelegationListRequest{NetworkID: resource.NetworkID, ResourceID: resource.ID}
	first := floatingIPDelegationRecordPage(records, request)
	require.NoError(t, validateFloatingIPDelegationPage(first, request))
	assert.Equal(t, fmt.Sprintf("%032x", floatingIPPageSize), first.NextID)
	request.AfterID = first.NextID
	last := floatingIPDelegationRecordPage(records, request)
	require.NoError(t, validateFloatingIPDelegationPage(last, request))
	require.Len(t, last.Items, 1)
	assert.Equal(t, fmt.Sprintf("%032x", floatingIPPageSize+1), last.Items[0].ID)
	assert.Empty(t, last.NextID)
	last.Items[0].NetworkID = string(make([]byte, 64))
	require.Error(t, validateFloatingIPDelegationPage(last, request))
}

func TestFloatingIPDelegationGrantWireRequiresExplicitLeaseAndNoPrincipal(t *testing.T) {
	resource, principal, _ := delegationFixture(t)
	for name, request := range map[string]FloatingIPDelegationGrantRequest{
		"associate missing": {Method: "associate"}, "renew missing": {Method: "renew"},
		"subsecond":     {Method: "associate", LeaseDuration: time.Millisecond},
		"inspect lease": {Method: "inspect", LeaseDuration: time.Second},
		"release lease": {Method: "release", LeaseDuration: time.Second}, "unknown": {Method: "any"},
		"inspect": {Method: "inspect"}, "release": {Method: "release"},
		"associate": {Method: "associate", LeaseDuration: time.Second}, "renew": {Method: "renew",
			LeaseDuration: time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			request.ID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			request.NetworkID = resource.NetworkID
			request.ResourceID = resource.ID
			request.VMID = principal.vmID
			err := request.Validate()
			if name == "inspect" || name == "release" || name == "associate" || name == "renew" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestFloatingIPDelegationOwnedCleanupComparesReceiptAndResumesRevoking(t *testing.T) {
	fixture, resources := floatingIPStoreFixture(t)
	store, err := openFloatingIPDelegationStore(t.Context(), fixture.uid, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.release(context.Background())) })
	resource, principal, seed := delegationFixture(t)
	require.NoError(t, resources.persist(t.Context(), resource, nil))
	grant, err := ensureFloatingIPDelegation(t.Context(), store, seed.ID, resource, principal,
		floatingIPDelegationRenew, 5*time.Second)
	require.NoError(t, err)
	receipt := floatingIPDelegationProjection(grant)
	tx := &managedNetworkTransaction{store: fixture.uid}
	bad := receipt
	bad.LaunchGeneration++
	_, err = removeOwnedStoredFloatingIPDelegation(t.Context(), tx, store,
		FloatingIPDelegationRemoveOwnedRequest{Grant: bad})
	require.Error(t, err)
	current, _, _, _, err := store.read(t.Context(), grant.ID)
	require.NoError(t, err)
	assert.Equal(t, floatingIPDelegationActive, current.State, "rebound receipt must not revoke")
	revoking := grant
	revoking.State = floatingIPDelegationRevoking
	revoking.Revision++
	require.NoError(t, store.persist(t.Context(), revoking, &grant))
	removed, err := removeOwnedStoredFloatingIPDelegation(t.Context(), tx, store,
		FloatingIPDelegationRemoveOwnedRequest{Grant: receipt})
	require.NoError(t, err)
	assert.Equal(t, "revoked", removed.State)
	assert.Equal(t, uint64(3), removed.Revision)
	_, err = removeOwnedStoredFloatingIPDelegation(t.Context(), tx, store,
		FloatingIPDelegationRemoveOwnedRequest{Grant: receipt})
	require.NoError(t, err, "same immutable active receipt may acknowledge completed cleanup")
}
