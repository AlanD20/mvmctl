package jailer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuestControlWorkerResetRefusesUnfinishedAuthorityAndAcceptsRetirement(t *testing.T) {
	store := guestControlWorkerStoreFixture(t)
	record := guestControlWorkerTestRecord()
	require.NoError(t, store.persist(t.Context(), record))
	guard := &HostResetGuard{state: &hostResetGuardState{roots: &instanceAuthorityRoots{
		deps: store.directories.deps, policy: store.directories.policy, retained: store.retained,
	}}}
	require.Error(t, guard.requireRetiredGuestControlWorkers(t.Context()))
	record.Revision++
	record.Phase = "stopping"
	require.NoError(t, store.persist(t.Context(), record))
	require.Error(t, guard.requireRetiredGuestControlWorkers(t.Context()))
	record.Revision++
	record.Phase = "stopped"
	require.NoError(t, store.persist(t.Context(), record))
	require.NoError(t, guard.requireRetiredGuestControlWorkers(t.Context()))
}
