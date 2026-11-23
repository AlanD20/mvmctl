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

func TestGuestControlWorkerResetAdmitsOnlyRetiredAliasAckHistory(t *testing.T) {
	store := guestControlWorkerStoreFixture(t)
	record := guestControlWorkerTestRecord()
	require.NoError(t, store.persist(t.Context(), record))
	receipt := GuestControlAliasAcknowledgement{
		Schema:           1,
		Principal:        record.principal(),
		WorkerGeneration: record.Generation,
		SessionID:        "11111111-1111-4111-8111-111111111111",
		SnapshotID:       "22222222-2222-4222-8222-222222222222",
		Digest:           "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	require.NoError(t, store.writeAliasAcknowledgement(t.Context(), receipt))
	guard := &HostResetGuard{
		state: &hostResetGuardState{
			roots: &instanceAuthorityRoots{
				deps:     store.directories.deps,
				policy:   store.directories.policy,
				retained: store.retained,
			},
		},
	}
	require.Error(t, guard.requireRetiredGuestControlWorkers(t.Context()))
	record.Revision++
	record.Phase = "stopping"
	require.NoError(t, store.persist(t.Context(), record))
	record.Revision++
	record.Phase = "stopped"
	require.NoError(t, store.persist(t.Context(), record))
	require.NoError(t, guard.requireRetiredGuestControlWorkers(t.Context()))
}
