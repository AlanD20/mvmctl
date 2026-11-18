package network

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func delegationFixture(t *testing.T) (floatingIPRecord, floatingIPGuestPrincipal, floatingIPDelegationRecord) {
	t.Helper()
	resource := floatingIPContractRecord()
	owner := floatingIPTransitionOwner()
	principal := floatingIPGuestPrincipal{
		ownerUID: resource.OwnerUID, networkID: resource.NetworkID, vmID: owner.VMID, bootID: owner.BootID,
		launchGeneration: owner.LaunchGeneration, executionUID: owner.ExecutionUID,
	}
	grant, err := newFloatingIPDelegation(resource, principal, floatingIPDelegationAssociate)
	require.NoError(t, err)
	return resource, principal, grant
}
func TestFloatingIPDelegationCanonicalAndClosedTransitions(t *testing.T) {
	_, _, grant := delegationFixture(t)
	raw, err := encodeFloatingIPDelegationRecord(grant)
	require.NoError(t, err)
	got, err := decodeFloatingIPDelegationRecord(raw)
	require.NoError(t, err)
	require.Equal(t, grant, got)
	_, err = decodeFloatingIPDelegationRecord(append(raw, ' '))
	require.Error(t, err)
	_, err = decodeFloatingIPDelegationRecord(bytes.Replace(raw, []byte(`"schema":1`), []byte(`"schema":1,"schema":1`), 1))
	require.Error(t, err)
	revoking := grant
	revoking.Revision++
	revoking.State = floatingIPDelegationRevoking
	require.NoError(t, validateFloatingIPDelegationReplacement(revoking, &grant))
	revoked := revoking
	revoked.Revision++
	revoked.State = floatingIPDelegationRevoked
	require.NoError(t, validateFloatingIPDelegationReplacement(revoked, &revoking))
	resurrect := grant
	resurrect.Revision = 4
	require.Error(t, validateFloatingIPDelegationReplacement(resurrect, &revoked))
	for name, mutate := range map[string]func(*floatingIPDelegationRecord){
		"resource":    func(g *floatingIPDelegationRecord) { g.ResourceID = "cccccccccccccccccccccccccccccccc" },
		"method":      func(g *floatingIPDelegationRecord) { g.Method = floatingIPDelegationRenew },
		"incarnation": func(g *floatingIPDelegationRecord) { g.LaunchGeneration++ },
		"skip":        func(g *floatingIPDelegationRecord) { g.State = floatingIPDelegationRevoked },
	} {
		t.Run(name, func(t *testing.T) {
			next := revoking
			mutate(&next)
			require.Error(t, validateFloatingIPDelegationReplacement(next, &grant))
		})
	}
}
func TestFloatingIPDelegationAdmissionAndExactProvenance(t *testing.T) {
	resource, principal, grant := delegationFixture(t)
	require.NoError(t, admitFloatingIPDelegation(principal, resource, grant, floatingIPDelegationAssociate))
	require.Error(t, admitFloatingIPDelegation(principal, resource, grant, floatingIPDelegationRenew))
	stale := principal
	stale.launchGeneration++
	require.Error(t, admitFloatingIPDelegation(stale, resource, grant, floatingIPDelegationAssociate))
	revoking := grant
	revoking.State = floatingIPDelegationRevoking
	revoking.Revision++
	require.Error(t, admitFloatingIPDelegation(principal, resource, revoking, floatingIPDelegationAssociate))
	owner := floatingIPTransitionOwner()
	require.False(t, floatingIPOwnerSustainedByDelegation(owner, grant))
	owner.Origin = floatingIPDelegatedOrigin
	owner.GrantID = grant.ID
	owner.GrantRevision = grant.Revision
	require.True(t, floatingIPOwnerSustainedByDelegation(owner, grant))
	other := grant
	other.ID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	require.False(t, floatingIPOwnerSustainedByDelegation(owner, other))
	require.True(t, floatingIPOwnerSustainedByDelegation(owner, revoking))
	resource.Phase = floatingIPFencing
	resource.Operation = floatingIPRelease
	resource.BeforeOwner = owner
	require.True(t, floatingIPRecordSustainedByDelegation(resource, revoking))
}
