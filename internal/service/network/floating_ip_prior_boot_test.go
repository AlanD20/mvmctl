package network

import (
	"context"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"mvmctl/internal/service/firewall"
)

const floatingIPNextBoot = "223e4567-e89b-42d3-a456-426614174000"

func expectedPriorBootBinding(
	record floatingIPRecord,
	owner floatingIPOwner,
	generation uint64,
) firewall.FloatingIPBinding {
	return firewall.FloatingIPBinding{OwnerUID: record.OwnerUID, NetworkID: record.NetworkID,
		ID: record.ID, IPv4: netip.MustParseAddr(record.IPv4), Generation: generation,
		OwnerVMID: owner.VMID, OwnerLaunchGeneration: owner.LaunchGeneration,
		Lifetime: owner.Lifetime, LeaseDuration: owner.LeaseDuration}
}

// Rationale: prior-boot recovery must match the exact historical sealed projection,
// including a retry after its root generation advanced but before fencing succeeded.
func TestPriorBootFloatingIPHistoryPreservesExactRecoveryPredecessor(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(map[bool]string{false: "finite", true: "persistent"}[persistent], func(t *testing.T) {
			fixture, store := floatingIPStoreFixture(t)
			records := floatingIPAssociationRecords()
			if persistent {
				for i := range records {
					if records[i].TargetOwner != (floatingIPOwner{}) {
						records[i].TargetOwner = persistentFloatingIPOwner()
					}
					if records[i].Owner != (floatingIPOwner{}) {
						records[i].Owner = persistentFloatingIPOwner()
					}
				}
			}
			for i, record := range records {
				var previous *floatingIPRecord
				if i > 0 {
					previous = &records[i-1]
				}
				require.NoError(t, store.persist(t.Context(), record, previous))
			}
			owned := records[4]
			history, fenced, err := priorBootFloatingIPBindings(owned, floatingIPNextBoot)
			require.NoError(t, err)
			require.Equal(t, []firewall.FloatingIPBinding{expectedPriorBootBinding(owned, owned.Owner, 2)}, history)
			require.Equal(t, expectedPriorBootBinding(owned, floatingIPOwner{}, 3), fenced)
			requireFloatingIPReadback(t, store, owned) // projection is read-only
			recovering, err := beginFloatingIPRecovery(t.Context(), store, owned)
			require.NoError(t, err)
			reopened, err := openFloatingIPStore(t.Context(), fixture.uid, false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.release(context.Background())) })
			retry, _, _, found, err := reopened.read(t.Context(), owned.ID)
			require.NoError(t, err)
			require.True(t, found)
			history, retryFence, err := priorBootFloatingIPBindings(retry, floatingIPNextBoot)
			require.NoError(t, err)
			require.Equal(t, []firewall.FloatingIPBinding{
				expectedPriorBootBinding(owned, owned.Owner, 2), expectedPriorBootBinding(owned, floatingIPOwner{}, 3),
			}, history)
			require.Equal(t, fenced, retryFence)
			retried, err := beginFloatingIPRecovery(t.Context(), reopened, retry)
			require.NoError(t, err)
			require.Equal(t, recovering, retried, "retry must not advance generation or revision again")
			complete, err := completeFloatingIPRecovery(t.Context(), reopened, retried)
			require.NoError(t, err)
			require.Equal(t, uint64(3), complete.Generation)
			require.Equal(t, floatingIPUnassociated, complete.Phase)
			require.Empty(t, complete.Owner)
			require.Empty(t, complete.BeforeOwner)
			require.Empty(t, complete.TargetOwner)
			requireFloatingIPReadback(t, reopened, complete)
		})
	}
}

func TestPriorBootFloatingIPHistoryUsesOnlyClosedTransitionBindings(t *testing.T) {
	records := floatingIPAssociationRecords()
	owner := records[4].Owner
	zero := floatingIPOwner{}
	renewing := records[4]
	renewing.Phase, renewing.Operation, renewing.Revision = floatingIPRenewing, floatingIPRenew, 6
	renewing.Owner, renewing.BeforeOwner, renewing.TargetOwner = zero, owner, owner
	recovering := renewing
	recovering.Phase, recovering.Operation = floatingIPFencing, floatingIPRecover
	recovering.Generation, recovering.Revision = 3, 7
	release := recovering
	release.Operation, release.TargetOwner = floatingIPRelease, zero
	grantRecovery := records[3]
	grantRecovery.Phase, grantRecovery.Operation = floatingIPFencing, floatingIPRecover
	grantRecovery.Revision++
	transferRecovery := recovering
	transferRecovery.TargetOwner.VMID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	transferRecovery.TargetOwner.ExecutionUID++
	fencedRecovery := recovering
	fencedRecovery.Phase = floatingIPFenced
	fencedRecovery.Revision++
	for _, tc := range []struct {
		name        string
		record      floatingIPRecord
		owners      []floatingIPOwner
		generations []uint64
		fence       uint64
	}{
		{"associate fencing", records[1], []floatingIPOwner{zero, zero}, []uint64{1, 2}, 2},
		{"associate fenced", records[2], []floatingIPOwner{zero}, []uint64{2}, 2},
		{"associate granting", records[3], []floatingIPOwner{owner, zero}, []uint64{2, 2}, 2},
		{"renewal", renewing, []floatingIPOwner{owner, zero}, []uint64{2, 2}, 3},
		{"release", release, []floatingIPOwner{owner, zero}, []uint64{2, 3}, 3},
		{"interrupted renewal recovery", recovering, []floatingIPOwner{owner, zero, zero}, []uint64{2, 2, 3}, 3},
		{"interrupted grant recovery", grantRecovery, []floatingIPOwner{zero, owner, zero}, []uint64{1, 2, 2}, 2},
		{"interrupted transfer recovery", transferRecovery, []floatingIPOwner{owner, transferRecovery.TargetOwner, zero}, []uint64{2, 3, 3}, 3},
		{"completed fence retry", fencedRecovery, []floatingIPOwner{zero}, []uint64{3}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history, fenced, err := priorBootFloatingIPBindings(tc.record, floatingIPNextBoot)
			require.NoError(t, err)
			var want []firewall.FloatingIPBinding
			for i, owner := range tc.owners {
				want = append(want, expectedPriorBootBinding(tc.record, owner, tc.generations[i]))
			}
			require.Equal(t, want, history)
			require.Equal(t, expectedPriorBootBinding(tc.record, zero, tc.fence), fenced)
			_, _, err = priorBootFloatingIPBindings(tc.record, owner.BootID)
			require.Error(t, err, "same-boot ownership and unfinished transitions must refuse")
		})
	}
	mixed := recovering
	mixed.TargetOwner.BootID = floatingIPNextBoot
	_, _, err := priorBootFloatingIPBindings(mixed, floatingIPNextBoot)
	require.Error(t, err, "one historical owner cannot admit a current-boot target")
}
