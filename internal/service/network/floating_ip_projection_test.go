package network

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFloatingIPReservationProjectionNeverRevivesOwnerAuthority(t *testing.T) {
	initial := floatingIPContractRecord()
	binding, project, err := floatingIPReservationProjection(
		initial, initial.OwnerUID, initial.NetworkID, floatingIPTransitionOwner().BootID,
	)
	require.NoError(t, err)
	require.True(t, project)
	require.Empty(t, binding.OwnerVMID)
	require.Zero(t, binding.LeaseDuration)
	records := floatingIPAssociationRecords()
	owned := records[4]
	binding, project, err = floatingIPReservationProjection(owned, owned.OwnerUID, owned.NetworkID, owned.Owner.BootID)
	require.NoError(t, err)
	require.False(t, project)
	require.Equal(t, owned.Owner.VMID, binding.OwnerVMID)
	for _, pending := range records[1:4] {
		_, _, err = floatingIPReservationProjection(
			pending, pending.OwnerUID, pending.NetworkID, floatingIPTransitionOwner().BootID,
		)
		require.Error(t, err)
	}
	_, _, err = floatingIPReservationProjection(
		owned, owned.OwnerUID, owned.NetworkID, "223e4567-e89b-42d3-a456-426614174000",
	)
	require.Error(t, err, "historical owned authority needs genuine lifecycle retirement")
	_, _, err = floatingIPReservationProjection(
		initial, initial.OwnerUID+1, initial.NetworkID, floatingIPTransitionOwner().BootID,
	)
	require.Error(t, err)
}
