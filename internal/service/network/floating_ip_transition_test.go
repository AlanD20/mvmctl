package network

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
)

func floatingIPTransitionOwner() floatingIPOwner {
	return floatingIPOwner{
		Origin: floatingIPOperatorOrigin, BootID: "123e4567-e89b-42d3-a456-426614174000", VMID: strings.Repeat("a", 32),
		LaunchGeneration: 7, ExecutionUID: 2000, LeaseDuration: 5 * time.Second}
}

func floatingIPAssociationRecords() []floatingIPRecord {
	initial := floatingIPContractRecord()
	fencing := initial
	fencing.Generation, fencing.Revision = 2, 2
	fencing.Phase, fencing.Operation = floatingIPFencing, floatingIPAssociate
	fencing.TargetOwner = floatingIPTransitionOwner()
	fenced := fencing
	fenced.Phase, fenced.Revision = floatingIPFenced, 3
	granting := fenced
	granting.Phase, granting.Revision = floatingIPGranting, 4
	owned := initial
	owned.Generation, owned.Revision, owned.Phase, owned.Owner = 2, 5, floatingIPOwned, floatingIPTransitionOwner()
	return []floatingIPRecord{initial, fencing, fenced, granting, owned}
}

func requireFloatingIPReadback(t *testing.T, store *floatingIPStore, expected floatingIPRecord) {
	t.Helper()
	observed, _, _, found, err := store.read(t.Context(), expected.ID)
	require.NoError(t, err)
	require.True(t, found)
	if diff := cmp.Diff(expected, observed); diff != "" {
		t.Fatalf("durable transition (-want +got):\n%s", diff)
	}
}

func TestFloatingIPTransitionAssociateRenewReleaseAndRetire(t *testing.T) {
	fixture, store := floatingIPStoreFixture(t)
	records := floatingIPAssociationRecords()
	owned := records[4]
	renewing := owned
	renewing.Revision, renewing.Phase, renewing.Operation = 6, floatingIPRenewing, floatingIPRenew
	renewing.Owner, renewing.BeforeOwner, renewing.TargetOwner = floatingIPOwner{}, owned.Owner, owned.Owner
	renewed := owned
	renewed.Revision = 7 // CONTRACT: renewal advances revision, retaining ownership generation 2.
	release := renewed
	release.Generation, release.Revision, release.Phase, release.Operation = 3, 8, floatingIPFencing, floatingIPRelease
	release.Owner, release.BeforeOwner = floatingIPOwner{}, owned.Owner
	fenced := release
	fenced.Revision, fenced.Phase = 9, floatingIPFenced
	unassociated := floatingIPContractRecord()
	unassociated.Generation, unassociated.Revision = 3, 10
	retired := unassociated
	retired.Generation, retired.Revision, retired.Retired = 4, 11, true
	records = append(records, renewing, renewed, release, fenced, unassociated, retired)
	for index, record := range records {
		var previous *floatingIPRecord
		if index > 0 {
			previous = &records[index-1]
		}
		require.NoError(t, store.persist(t.Context(), record, previous))
		requireFloatingIPReadback(t, store, record)
	}
	reopened, err := openFloatingIPStore(t.Context(), fixture.uid, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.release(context.Background())) })
	requireFloatingIPReadback(t, reopened, retired)
	require.Error(t, reopened.persist(t.Context(), records[0], &retired), "terminal retirement cannot resurrect")
}

func TestFloatingIPTransitionInterruptedGrantRecoveryCannotReplayActivation(t *testing.T) {
	fixture, store := floatingIPStoreFixture(t)
	records := floatingIPAssociationRecords()[:4]
	for index, record := range records {
		var previous *floatingIPRecord
		if index > 0 {
			previous = &records[index-1]
		}
		require.NoError(t, store.persist(t.Context(), record, previous))
	}
	reopened, err := openFloatingIPStore(t.Context(), fixture.uid, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.release(context.Background())) })
	granting := records[3]
	requireFloatingIPReadback(t, reopened, granting)
	recovering := granting
	recovering.Phase, recovering.Operation, recovering.Revision = floatingIPFencing, floatingIPRecover, 5
	require.NoError(t, reopened.persist(t.Context(), recovering, &granting))
	requireFloatingIPReadback(t, reopened, recovering) // Target may have a live key; never discard it before cleanup.
	fenced := recovering
	fenced.Phase, fenced.Revision = floatingIPFenced, 6
	require.NoError(t, reopened.persist(t.Context(), fenced, &recovering))
	replay := fenced
	replay.Phase, replay.Revision = floatingIPGranting, 7
	require.Error(t, reopened.persist(t.Context(), replay, &fenced))
	requireFloatingIPReadback(t, reopened, fenced)
	unassociated := floatingIPContractRecord()
	unassociated.Generation, unassociated.Revision = 2, 7
	require.NoError(t, reopened.persist(t.Context(), unassociated, &fenced))
	requireFloatingIPReadback(t, reopened, unassociated)
}

func TestFloatingIPTransitionRenewalRecoveryAdvancesOwnershipGenerationOnce(t *testing.T) {
	owned := floatingIPAssociationRecords()[4]
	renewing := owned
	renewing.Owner, renewing.BeforeOwner, renewing.TargetOwner = floatingIPOwner{}, owned.Owner, owned.Owner
	renewing.Revision, renewing.Phase, renewing.Operation = 6, floatingIPRenewing, floatingIPRenew
	require.NoError(t, validateFloatingIPReplacement(renewing, &owned))
	recovering := renewing
	recovering.Generation, recovering.Revision = 3, 7
	recovering.Phase, recovering.Operation = floatingIPFencing, floatingIPRecover
	require.NoError(t, validateFloatingIPReplacement(recovering, &renewing))
	fenced := recovering
	fenced.Phase, fenced.Revision = floatingIPFenced, 8
	require.NoError(t, validateFloatingIPReplacement(fenced, &recovering))
	unassociated := floatingIPContractRecord()
	unassociated.Generation, unassociated.Revision = 3, 9
	require.NoError(t, validateFloatingIPReplacement(unassociated, &fenced))
	replay := renewing
	replay.Revision = 10
	require.Error(t, validateFloatingIPReplacement(replay, &unassociated))
}

func TestFloatingIPTransitionRejectsUnsafePhaseDeltas(t *testing.T) {
	records := floatingIPAssociationRecords()
	cases := map[string]struct{ before, next floatingIPRecord }{}
	add := func(name string, before, next floatingIPRecord) {
		next.Revision = before.Revision + 1
		cases[name] = struct{ before, next floatingIPRecord }{before, next}
	}
	add("skip_fencing", records[0], records[4])
	add("skip_cleanup", records[1], records[3])
	changed := records[2]
	changed.TargetOwner.LaunchGeneration++
	add("replace_target_mid_transition", records[1], changed)
	changed = records[4]
	changed.Owner.ExecutionUID++
	add("commit_another_owner", records[3], changed)
	add("drop_owner_without_fencing", records[4], records[0])
	changed = records[0]
	changed.Retired = true
	add("retire_owned", records[4], changed)
	changed = records[3]
	changed.Operation = floatingIPRecover
	add("recovery_cannot_grant", records[2], changed)
	changed = records[1]
	changed.Generation = 1
	add("association_without_generation_change", records[0], changed)
	for name, test := range cases {
		t.Run(name, func(t *testing.T) { require.Error(t, validateFloatingIPReplacement(test.next, &test.before)) })
	}
}

func TestFloatingIPTransitionCASRejectsStaleRevisionAtSameGeneration(t *testing.T) {
	_, store := floatingIPStoreFixture(t)
	records := floatingIPAssociationRecords()
	for index, record := range records {
		var previous *floatingIPRecord
		if index > 0 {
			previous = &records[index-1]
		}
		require.NoError(t, store.persist(t.Context(), record, previous))
	}
	owned := records[4]
	renewing := owned
	renewing.Revision, renewing.Phase, renewing.Operation = 6, floatingIPRenewing, floatingIPRenew
	renewing.Owner, renewing.BeforeOwner, renewing.TargetOwner = floatingIPOwner{}, owned.Owner, owned.Owner
	require.NoError(t, store.persist(t.Context(), renewing, &owned))
	renewed := owned
	renewed.Revision = 7
	require.NoError(t, store.persist(t.Context(), renewed, &renewing))
	// Same generation and owner do not permit a stale snapshot to replace the newer renewal revision.
	require.Error(t, store.persist(t.Context(), renewing, &owned))
	requireFloatingIPReadback(t, store, renewed)
}

func TestFloatingIPTransitionRefusesRevisionAndGenerationOverflow(t *testing.T) {
	initial := floatingIPContractRecord()
	max := ^uint64(0)
	initial.Generation, initial.Revision = max, max
	next := initial
	next.Revision = 0
	next.Retired = true
	require.Error(t, validateFloatingIPReplacement(next, &initial))
	// A phase can consume the final available revision; the following phase must refuse overflow.
	fencing := floatingIPAssociationRecords()[1]
	fencing.Generation, fencing.Revision = max-1, max-1
	fenced := fencing
	fenced.Revision, fenced.Phase = max, floatingIPFenced
	require.NoError(t, validateFloatingIPReplacement(fenced, &fencing))
	next = fenced
	next.Revision, next.Phase = 0, floatingIPGranting
	require.Error(t, validateFloatingIPReplacement(next, &fenced))
}

func TestFloatingIPTransitionCanonicalCodecRefusesInvalidOwnership(t *testing.T) {
	owned := floatingIPAssociationRecords()[4]
	cases := map[string]func(*floatingIPRecord){
		"schema1":                 func(r *floatingIPRecord) { r.Schema = 1 },
		"zero_revision":           func(r *floatingIPRecord) { r.Revision = 0 },
		"unknown_phase":           func(r *floatingIPRecord) { r.Phase = "pending" },
		"zero_lease":              func(r *floatingIPRecord) { r.Owner.LeaseDuration = 0 },
		"fractional_lease":        func(r *floatingIPRecord) { r.Owner.LeaseDuration += time.Millisecond },
		"root_execution":          func(r *floatingIPRecord) { r.Owner.ExecutionUID = 0 },
		"operator_execution":      func(r *floatingIPRecord) { r.Owner.ExecutionUID = r.OwnerUID },
		"missing_boot":            func(r *floatingIPRecord) { r.Owner.BootID = "" },
		"missing_VM":              func(r *floatingIPRecord) { r.Owner.VMID = "" },
		"zero_incarnation":        func(r *floatingIPRecord) { r.Owner.LaunchGeneration = 0 },
		"unassociated_with_owner": func(r *floatingIPRecord) { r.Phase = floatingIPUnassociated },
		"retired_with_owner":      func(r *floatingIPRecord) { r.Retired = true },
		"stable_with_history":     func(r *floatingIPRecord) { r.BeforeOwner = r.Owner },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			record := owned
			mutate(&record)
			_, err := encodeFloatingIPRecord(record)
			require.Error(t, err)
		})
	}
	encoded, err := encodeFloatingIPRecord(owned)
	require.NoError(t, err)
	for name, raw := range map[string][]byte{
		"duplicate_nested_VM":  []byte(strings.Replace(string(encoded), `"vm_id":`, `"vm_id":"","vm_id":`, 1)),
		"unknown_nested_field": []byte(strings.Replace(string(encoded), `"owner":{`, `"owner":{"path":"/etc/passwd",`, 1)),
		"missing_owner":        []byte(strings.Replace(string(encoded), `"owner":`, `"Owner":`, 1)),
	} {
		t.Run(name, func(t *testing.T) { _, err := decodeFloatingIPRecord(raw); require.Error(t, err) })
	}
}

func TestFloatingIPTransitionRenewalMayChangeOnlyProvenance(t *testing.T) {
	owned := floatingIPAssociationRecords()[4]
	renewing := owned
	renewing.Revision++
	renewing.Phase = floatingIPRenewing
	renewing.Operation = floatingIPRenew
	renewing.Owner = floatingIPOwner{}
	renewing.BeforeOwner = owned.Owner
	renewing.TargetOwner = owned.Owner
	renewing.TargetOwner.Origin = floatingIPDelegatedOrigin
	renewing.TargetOwner.GrantID = strings.Repeat("c", 32)
	renewing.TargetOwner.GrantRevision = 1
	require.NoError(t, validateFloatingIPReplacement(renewing, &owned))
	require.Equal(t, owned.Owner, renewing.BeforeOwner)
	changed := renewing
	changed.TargetOwner.LeaseDuration += time.Second
	require.Error(t, validateFloatingIPReplacement(changed, &owned))
	changed = renewing
	changed.TargetOwner.LaunchGeneration++
	require.Error(t, validateFloatingIPReplacement(changed, &owned))
	legacy := owned
	legacy.Owner.Origin = ""
	require.Error(t, validateFloatingIPRecord(legacy))
}
