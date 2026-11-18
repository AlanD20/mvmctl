package network

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/service/firewall"
	"mvmctl/pkg/errs"
)

func persistentFloatingIPOwner() floatingIPOwner {
	return floatingIPOwner{Origin: floatingIPOperatorOrigin, Lifetime: firewall.FloatingIPPersistent,
		BootID: "123e4567-e89b-42d3-a456-426614174000", VMID: strings.Repeat("a", 32), LaunchGeneration: 7, ExecutionUID: 2000}
}

func TestPersistentFloatingIPJournalReadbackAndRecoveryNeverRenew(t *testing.T) {
	fixture, store := floatingIPStoreFixture(t)
	records := floatingIPAssociationRecords()
	for i := range records {
		if records[i].TargetOwner != (floatingIPOwner{}) {
			records[i].TargetOwner = persistentFloatingIPOwner()
		}
		if records[i].Owner != (floatingIPOwner{}) {
			records[i].Owner = persistentFloatingIPOwner()
		}
		var previous *floatingIPRecord
		if i > 0 {
			previous = &records[i-1]
		}
		require.NoError(t, store.persist(t.Context(), records[i], previous))
	}
	reopened, err := openFloatingIPStore(t.Context(), fixture.uid, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.release(context.Background())) })
	owned := records[4]
	got, _, _, found, err := reopened.read(t.Context(), owned.ID)
	require.NoError(t, err)
	require.True(t, found)
	if diff := cmp.Diff(owned, got); diff != "" {
		t.Fatalf("persistent journal (-want +got):\n%s", diff)
	}
	renewing := owned
	renewing.Revision++
	renewing.Phase = floatingIPRenewing
	renewing.Operation = floatingIPRenew
	renewing.Owner = floatingIPOwner{}
	renewing.BeforeOwner = owned.Owner
	renewing.TargetOwner = owned.Owner
	require.Error(
		t,
		validateFloatingIPReplacement(renewing, &owned),
		"persistent authority must never enter finite renewal",
	)
	recovering, err := beginFloatingIPRecovery(t.Context(), reopened, owned)
	require.NoError(t, err)
	require.Equal(t, owned.Generation+1, recovering.Generation)
	require.Equal(t, owned.Owner, recovering.BeforeOwner, "cleanup preserves exact persistent incarnation")
	_, err = commitFloatingIPOwned(t.Context(), reopened, recovering)
	require.Error(t, err, "recovery cannot reactivate persistent packet authority")
}

func TestFloatingIPLifetimeOriginGrammar(t *testing.T) {
	for name, owner := range map[string]floatingIPOwner{
		"persistent delegated": {Origin: floatingIPDelegatedOrigin, Lifetime: firewall.FloatingIPPersistent},
		"finite operator":      {Origin: floatingIPOperatorOrigin, Lifetime: firewall.FloatingIPFinite, LeaseDuration: time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			base := persistentFloatingIPOwner()
			base.Origin, base.Lifetime, base.LeaseDuration = owner.Origin, owner.Lifetime, owner.LeaseDuration
			require.False(t, validFloatingIPOwner(base, testOwnerUID))
		})
	}
	require.True(t, validFloatingIPOwner(persistentFloatingIPOwner(), testOwnerUID))
	finite := floatingIPTransitionOwner()
	require.True(t, validFloatingIPOwner(finite, testOwnerUID))
	finite.LeaseDuration = 0
	require.False(t, validFloatingIPOwner(finite, testOwnerUID))
}

func TestGuestCannotRenewOrAcquirePersistentOperatorAuthorityButMayRelease(t *testing.T) {
	resource, principal, grant := delegationFixture(t)
	resource.Phase = floatingIPOwned
	resource.Owner = persistentFloatingIPOwner()
	observed := firewall.FloatingIPLeaseObservation{
		Binding:  floatingIPFirewallBinding(resource, resource.Owner),
		Present:  true,
		Lifetime: firewall.FloatingIPPersistent,
	}
	require.Error(t, admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationRenew, observed))
	require.Error(t, admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationAssociate, observed))
	// CONTRACT: the coordinator independently admits the exact current release grant before this owner check.
	require.NoError(t, admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationRelease, observed))
	observed.Present = false
	require.Error(
		t,
		admitFloatingIPGuestMutation(resource, principal, grant, floatingIPDelegationAssociate, observed),
		"missing kernel key never delegates explicit operator takeover",
	)
	observed.Present = true
	require.False(
		t,
		floatingIPRecordSustainedByDelegation(resource, grant),
		"unrelated grant revocation cannot fence operator ownership",
	)
	result, err := floatingIPGuestResult(resource, principal, observed)
	require.NoError(t, err)
	require.Equal(t, FloatingIPGuestSelf, result.Ownership)
	require.Equal(t, "persistent", result.Lifetime)
	require.Zero(t, result.LeaseRemainingMillis)
}

func TestOperatorFloatingIPGenerationAndRetainedAdmission(t *testing.T) {
	record := floatingIPContractRecord()
	require.NoError(t, admitFloatingIPOperatorGeneration(record, record.Generation))
	err := admitFloatingIPOperatorGeneration(record, record.Generation+1)
	require.Error(t, err)
	require.Equal(t, errs.ClassConflict, errs.AsDomainError(err).Class)
	record.Phase = floatingIPGranting
	require.Error(t, admitFloatingIPOperatorGeneration(record, record.Generation))
	request := FloatingIPAssociateRequest{
		ID:                 strings.Repeat("b", 32),
		NetworkID:          testNetworkID,
		VMID:               strings.Repeat("a", 32),
		ExpectedGeneration: 1,
	}
	require.NoError(t, request.Validate())
	for _, lease := range []*FloatingIPVMLease{nil, {}, {state: &floatingIPVMLeaseState{released: true}}} {
		_, err := lease.AssociateFloatingIP(t.Context(), request)
		require.Error(t, err, "caller identity alone cannot replace genuine retained VM authority")
	}
}

func TestOperatorFloatingIPReleaseFailureOutcomeTracksPublicationAndDeferredCleanup(t *testing.T) {
	// Rationale: root intent can be durable before fencing fails; an authoritative error frame must still
	// tell the operator to inspect rather than replay a release using its old generation.
	for _, failure := range []string{"stale generation", "write before rename", "directory fsync", "fence admission", "deferred close"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newNetworkStoreFixture(t)
			store, err := openFloatingIPStore(t.Context(), fixture.uid, true)
			require.NoError(t, err)
			t.Cleanup(func() {
				if !store.owner.released {
					require.NoError(t, store.release(context.Background()))
				}
			})
			records := floatingIPAssociationRecords()
			for index, record := range records {
				var before *floatingIPRecord
				if index > 0 {
					before = &records[index-1]
				}
				require.NoError(t, store.persist(t.Context(), record, before))
			}
			owned := records[4]
			release := owned
			release.Revision++
			release.Generation++
			release.Phase, release.Operation = floatingIPFencing, floatingIPRelease
			release.BeforeOwner, release.Owner = owned.Owner, floatingIPOwner{}
			if failure == "write before rename" {
				installNetworkStoreWriteFault(t, store.owner, "write", unix.EIO)
			}
			if failure == "directory fsync" {
				original := store.owner.deps.fsync
				store.owner.deps.fsync = func(ctx context.Context, fd int) error {
					if fd == store.owner.stateFD {
						return unix.EIO
					}
					return original(ctx, fd)
				}
			}
			if failure == "deferred close" {
				original, stateFD := store.owner.deps.close, store.owner.stateFD
				store.owner.deps.close = func(ctx context.Context, fd int) error {
					err := original(ctx, fd)
					if fd == stateFD {
						return errors.Join(err, unix.EIO)
					}
					return err
				}
			}
			// Use genuine record publication and descriptor cleanup. Missing effect authority refuses fencing
			// after publication without touching the host, while the directory-fsync case has already renamed.
			releaseErr := func() (returnErr error) {
				publicationAttempted := false
				defer func() { returnErr = operatorFloatingIPReleaseOutcome(owned.ID, publicationAttempted, returnErr) }()
				defer func() { returnErr = errors.Join(returnErr, store.release(context.Background())) }()
				if failure == "stale generation" {
					return admitFloatingIPOperatorGeneration(owned, owned.Generation+1)
				}
				publicationAttempted = true
				_, err := publishOperatorFloatingIPRelease(
					t.Context(),
					&managedNetworkTransaction{firewall: &firewall.MutationLease{}},
					store,
					owned,
					release,
				)
				return err
			}()
			require.Error(t, releaseErr)
			domain := errs.AsDomainError(releaseErr)
			require.NotNil(t, domain)
			if failure == "stale generation" {
				require.Equal(t, errs.ClassConflict, domain.Class)
				require.NotContains(t, domain.Details, "outcome_unknown")
			} else {
				require.Equal(t, true, domain.Details["outcome_unknown"])
				require.Equal(t, owned.ID, domain.Details["id"])
				require.NotContains(t, domain.Details, "root_committed", "a failed publication may not be durable")
			}
			if failure == "write before rename" || failure == "directory fsync" || failure == "deferred close" {
				require.ErrorIs(t, releaseErr, unix.EIO)
			}
			reopened, err := openFloatingIPStore(t.Context(), fixture.uid, false)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.release(context.Background())) }()
			got, _, _, found, err := reopened.read(t.Context(), owned.ID)
			require.NoError(t, err)
			require.True(t, found)
			expected := release
			if failure == "stale generation" || failure == "write before rename" {
				expected = owned
			}
			if diff := cmp.Diff(expected, got); diff != "" {
				t.Fatalf("release failure durable state (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOperatorFloatingIPReleaseValidationRemainsBeforeEffects(t *testing.T) {
	_, err := NewFloatingIPServer().ReleaseFloatingIP(t.Context(), testOwnerUID, FloatingIPReleaseRequest{})
	require.Error(t, err)
	domain := errs.AsDomainError(err)
	require.Equal(t, errs.ClassValidation, domain.Class)
	require.NotContains(t, domain.Details, "outcome_unknown")
	require.Nil(t, operatorFloatingIPReleaseOutcome("id", true, nil))
}
