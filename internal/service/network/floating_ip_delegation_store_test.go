package network

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestFloatingIPDelegationStoreCASRevocationRegrant(t *testing.T) {
	fixture, _ := floatingIPStoreFixture(t)
	store, err := openFloatingIPDelegationStore(t.Context(), fixture.uid, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.release(context.Background())) })
	resource, principal, grant := delegationFixture(t)
	require.NoError(t, store.persist(t.Context(), grant, nil))
	second, err := newFloatingIPDelegation(resource, principal, grant.Method)
	require.NoError(t, err)
	require.NotEqual(t, grant.ID, second.ID)
	require.Error(t, store.persist(t.Context(), second, nil))
	revoking := grant
	revoking.State = floatingIPDelegationRevoking
	revoking.Revision++
	require.NoError(t, store.persist(t.Context(), revoking, &grant))
	require.Error(t, store.persist(t.Context(), second, nil))
	require.Error(t, store.persist(t.Context(), revoking, &grant), "stale CAS")
	revoked := revoking
	revoked.State = floatingIPDelegationRevoked
	revoked.Revision++
	require.NoError(t, store.persist(t.Context(), revoked, &revoking))
	require.NoError(t, store.persist(t.Context(), second, nil))
	reopened, err := openFloatingIPDelegationStore(t.Context(), fixture.uid, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.release(context.Background())) })
	items, err := reopened.list(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Error(t, reopened.persist(t.Context(), grant, &revoked))
	third, err := newFloatingIPDelegation(resource, principal, grant.Method)
	require.NoError(t, err)
	raw, err := encodeFloatingIPDelegationRecord(third)
	require.NoError(t, err)
	path := filepath.Join(filepath.Dir(fixture.recordPath), floatingIPDelegationDirectory, third.ID+".json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	_, err = reopened.list(t.Context())
	require.Error(t, err, "duplicate live tuple refuses inventory")
}
