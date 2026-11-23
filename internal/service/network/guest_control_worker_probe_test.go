package network

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCurrentGuestControlDelegationStartupHintRejectsRetiredAndReplacedIncarnations(t *testing.T) {
	_, principal, record := delegationFixture(t)
	incarnation := FloatingIPVMIncarnation{
		BootID:           principal.bootID,
		LaunchGeneration: principal.launchGeneration,
		ExecutionUID:     principal.executionUID,
	}
	require.True(
		t,
		currentGuestControlDelegationExists(
			[]floatingIPDelegationRecord{record},
			principal.ownerUID,
			principal.vmID,
			incarnation,
		),
	)
	for name, mutate := range map[string]func(*floatingIPDelegationRecord){
		"revoked":                 func(r *floatingIPDelegationRecord) { r.State = floatingIPDelegationRevoked; r.Revision = 3 },
		"revoking":                func(r *floatingIPDelegationRecord) { r.State = floatingIPDelegationRevoking; r.Revision = 2 },
		"different VM":            func(r *floatingIPDelegationRecord) { r.VMID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" },
		"different owner":         func(r *floatingIPDelegationRecord) { r.OwnerUID++ },
		"different boot":          func(r *floatingIPDelegationRecord) { r.BootID = "22222222-2222-4222-8222-222222222222" },
		"different launch":        func(r *floatingIPDelegationRecord) { r.LaunchGeneration++ },
		"different execution UID": func(r *floatingIPDelegationRecord) { r.ExecutionUID++ },
	} {
		t.Run(name, func(t *testing.T) {
			r := record
			mutate(&r)
			require.False(
				t,
				currentGuestControlDelegationExists(
					[]floatingIPDelegationRecord{r},
					principal.ownerUID,
					principal.vmID,
					incarnation,
				),
			)
		})
	}
	require.False(t, currentGuestControlDelegationExists(nil, principal.ownerUID, principal.vmID, incarnation))
}
