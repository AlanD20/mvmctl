package network

import (
	"context"
	"testing"
	"time"

	"mvmctl/pkg/errs"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/service/firewall"
)

func TestFloatingIPGuestMutationRefusesHealthyOwnerAndRequiresExactSelf(t *testing.T) {
	resource, principal, grant := delegationFixture(t)
	owner := floatingIPTransitionOwner()
	owner.VMID = "cccccccccccccccccccccccccccccccc"
	resource.Phase = floatingIPOwned
	resource.Owner = owner
	observed := firewall.FloatingIPLeaseObservation{
		Binding: floatingIPFirewallBinding(resource, owner), Present: true, Remaining: time.Second,
	}
	require.Error(t, admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationAssociate, observed))
	observed.Present = false
	observed.Remaining = 0
	require.NoError(
		t,
		admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationAssociate, observed),
	)
	require.Error(t, admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationRelease, observed))
	resource.Owner = principal.owner(grant.LeaseDuration, grant.ID, grant.Revision)
	require.Error(t, admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationAssociate, observed))
	require.NoError(t, admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationRenew, observed))
	grant.LeaseDuration += time.Second
	require.Error(t, admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationRenew, observed))
}

func TestFloatingIPGuestResultDistinguishesExpiryAndRejectsReboundBinding(t *testing.T) {
	resource, principal, grant := delegationFixture(t)
	resource.Phase = floatingIPOwned
	resource.Owner = principal.owner(grant.LeaseDuration, grant.ID, grant.Revision)
	observed := firewall.FloatingIPLeaseObservation{
		Binding: floatingIPFirewallBinding(resource, resource.Owner), Present: true, Remaining: 2 * time.Second,
	}
	result, err := floatingIPGuestResult(resource, principal, observed)
	require.NoError(t, err)
	require.Equal(t, FloatingIPGuestSelf, result.Ownership)
	require.Equal(t, uint64(2000), result.LeaseRemainingMillis)
	observed.Present = false
	observed.Remaining = 0
	result, err = floatingIPGuestResult(resource, principal, observed)
	require.NoError(t, err)
	require.Equal(t, FloatingIPGuestUnassociated, result.Ownership)
	observed.Binding.Generation++
	_, err = floatingIPGuestResult(resource, principal, observed)
	require.Error(t, err)
}

func TestFloatingIPGuestMethodsRefuseMissingRetainedAuthority(t *testing.T) {
	for _, lease := range []*FloatingIPVMLease{
		nil, {}, {state: &floatingIPVMLeaseState{}}, {state: &floatingIPVMLeaseState{released: true}},
	} {
		_, possible, err := lease.GuestInspect(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1)
		require.Error(t, err)
		require.False(t, possible, "missing retained admission cannot attempt mutation")
		_, possible, err = lease.GuestAssociate(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1)
		require.Error(t, err)
		require.False(t, possible, "missing retained admission cannot attempt mutation")
		_, possible, err = lease.GuestRenew(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1)
		require.Error(t, err)
		require.False(t, possible, "missing retained admission cannot attempt mutation")
		_, possible, err = lease.GuestRelease(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1)
		require.Error(t, err)
		require.False(t, possible, "missing retained admission cannot attempt mutation")
	}
}

func TestFloatingIPDelegatedRenewalPreservesCleanupProvenanceOnInterruptedWrite(t *testing.T) {
	_, store := floatingIPStoreFixture(t)
	records := floatingIPAssociationRecords()
	for i, record := range records {
		var previous *floatingIPRecord
		if i > 0 {
			previous = &records[i-1]
		}
		require.NoError(t, store.persist(t.Context(), record, previous))
	}
	owned := records[4]
	renewing := owned
	renewing.Phase = floatingIPRenewing
	renewing.Operation = floatingIPRenew
	renewing.Revision++
	renewing.Owner = floatingIPOwner{}
	renewing.BeforeOwner = owned.Owner
	renewing.TargetOwner = owned.Owner
	renewing.TargetOwner.Origin = floatingIPDelegatedOrigin
	renewing.TargetOwner.GrantID = "cccccccccccccccccccccccccccccccc"
	renewing.TargetOwner.GrantRevision = floatingIPDelegationActiveRevision
	require.NoError(t, store.persist(t.Context(), renewing, &owned))
	recovered, _, _, found, err := store.read(t.Context(), owned.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, owned.Owner, recovered.BeforeOwner)
	require.Equal(t, renewing.TargetOwner, recovered.TargetOwner)
	recovery := recovered
	recovery.Revision++
	recovery.Generation++
	recovery.Phase = floatingIPFencing
	recovery.Operation = floatingIPRecover
	require.NoError(t, store.persist(t.Context(), recovery, &recovered))
	_, err = commitFloatingIPOwned(t.Context(), store, recovery)
	require.Error(t, err, "root recovery cannot acknowledge or replay interrupted renewal")
}

func TestFloatingIPGuestAssociationRefusesPresentZeroRemainingOwner(t *testing.T) {
	resource, principal, grant := delegationFixture(t)
	owner := floatingIPTransitionOwner()
	owner.VMID = "cccccccccccccccccccccccccccccccc"
	resource.Phase, resource.Owner = floatingIPOwned, owner
	observed := firewall.FloatingIPLeaseObservation{
		Binding: floatingIPFirewallBinding(resource, owner), Present: true, Remaining: 0,
	}
	require.Error(t, admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationAssociate, observed),
		"only absence proves revoked authority; zero remaining is still a present key")
}

func TestFloatingIPGuestAdmissionDeniesMissingMethodAndConflictsOnStaleGeneration(t *testing.T) {
	record, principal, _ := delegationFixture(t)
	_, err := findFloatingIPGuestGrant(t.Context(), nil, principal, record, floatingIPDelegationRenew)
	require.Error(t, err)
	require.Equal(t, errs.ClassValidation, errs.AsDomainError(err).Class)
	err = admitFloatingIPGuestGeneration(record, record.Generation+1, floatingIPDelegationRenew)
	require.Error(t, err)
	require.Equal(t, errs.ClassConflict, errs.AsDomainError(err).Class)
	require.NoError(t, admitFloatingIPGuestGeneration(record, record.Generation, floatingIPDelegationRenew))
	require.NoError(t, admitFloatingIPGuestGeneration(record, 0, floatingIPDelegationInspect))
}

func TestFloatingIPGuestPublicationFaultsRemainPossibleMutations(t *testing.T) {
	for _, afterPublication := range []bool{false, true} {
		name := "before-write"
		if afterPublication {
			name = "after-rename"
		}
		t.Run(name, func(t *testing.T) {
			fixture, store := floatingIPStoreFixture(t)
			before := floatingIPAssociationRecords()[0]
			target := floatingIPAssociationRecords()[1]
			require.NoError(t, store.persist(t.Context(), before, nil))
			if afterPublication {
				realSync := store.owner.deps.fsync
				store.owner.deps.fsync = func(ctx context.Context, fd int) error {
					if fd == store.owner.stateFD {
						return unix.EIO
					}
					return realSync(ctx, fd)
				}
			} else {
				store.owner.deps.write = func(context.Context, int, []byte) (int, error) { return 0, unix.EIO }
			}
			possible, err := publishGuestFloatingIPIntent(t.Context(), store, target, before)
			require.Error(t, err)
			require.True(t, possible, "publication entry must never prove a pre-effect refusal")
			reopened, err := openFloatingIPStore(t.Context(), fixture.uid, false)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.release(context.Background())) }()
			observed, _, _, found, err := reopened.read(t.Context(), before.ID)
			require.NoError(t, err)
			require.True(t, found)
			expected := before
			if afterPublication {
				expected = target
			}
			require.Equal(t, expected, observed)
		})
	}
}
