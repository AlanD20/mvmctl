package firewall

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

func TestPublishVMLabelMetadataPreservesRevisionAndRetirement(t *testing.T) {
	ctx := t.Context()
	store := openTestGenerationRecordStore(t, prepareGenerationRecordTestRoot(t))
	t.Cleanup(func() { require.NoError(t, store.Release(ctx)) })
	kernel := &fakeKernelGenerationApplication{observations: []kernelObservation{
		{empty: true}, {empty: true}, {empty: true}, {empty: true},
	}}
	compiled, err := compileGeneration(generationIntent{})
	require.NoError(t, err)
	binding := testGenerationRecordBinding()
	record := mustCommittedGenerationRecord(t, binding, generationRecordIdentityFromCompiled(compiled))
	recordEvidence, err := store.create(ctx, record)
	require.NoError(t, err)
	vmID := strings.Repeat("a", 32)
	labels, observedRevision, err := readCommittedVMLabels(ctx, store, 1000, vmID)
	require.NoError(t, err)
	assert.NotNil(t, labels)
	assert.Zero(t, observedRevision)
	change := vmLabelMetadataChange{labels: model.VMLabels{"role": "worker"}}
	publish := func(owner uint32, change vmLabelMetadataChange) (uint64, error) {
		lease := &persistentGenerationLease{state: &persistentGenerationLeaseState{
			store: store, record: record, evidence: recordEvidence, active: true,
		}}
		mutation, err := loadLogicalPolicyMutation(ctx, store, kernel, lease, record, binding)
		require.NoError(t, err)
		return mutation.publishVMLabels(ctx, owner, vmID, change)
	}

	revision, err := publish(1000, change)
	require.NoError(t, err)
	assert.EqualValues(t, 1, revision)
	_, err = publish(1001, vmLabelMetadataChange{expected: 1, labels: model.VMLabels{}})
	require.Error(t, err)
	revision, err = publish(1000, vmLabelMetadataChange{expected: 1, labels: model.VMLabels{"role": "controller"}})
	require.NoError(t, err)
	assert.EqualValues(t, 2, revision)
	labels, observedRevision, err = readCommittedVMLabels(ctx, store, 1000, vmID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, observedRevision)
	assert.Equal(t, model.VMLabels{"role": "controller"}, labels)
	_, _, err = readCommittedVMLabels(ctx, store, 1001, vmID)
	require.Error(t, err)
	_, err = publish(1000, vmLabelMetadataChange{expected: 1, labels: model.VMLabels{}})
	require.Error(t, err)
	for range 2 {
		revision, err = publish(1000, vmLabelMetadataChange{retire: true})
		require.NoError(t, err)
		assert.EqualValues(t, 3, revision)
	}
	_, err = publish(1000, change)
	require.Error(t, err)
	_, _, err = readCommittedVMLabels(ctx, store, 1000, vmID)
	require.Error(t, err)
	journal, _, _, found, err := store.readIntent(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, vmLabelRecord{OwnerUID: 1000, Revision: 3, Labels: model.VMLabels{}, Retired: true},
		journal.Target.VMLabels[vmID])
	for _, event := range kernel.events {
		assert.Equal(t, "observe", event)
	}
}

func TestPublishVMLabelsRevokesActiveTrafficGeneration(t *testing.T) {
	ctx := t.Context()
	store := openTestGenerationRecordStore(t, prepareGenerationRecordTestRoot(t))
	t.Cleanup(func() { require.NoError(t, store.Release(ctx)) })
	before, policy := labelTrafficTestIntent(t)
	before.LabelTrafficPolicies = []LabelTrafficPolicy{policy}
	beforeGeneration, err := before.compile()
	require.NoError(t, err)
	target := cloneIntent(before)
	destination := target.Endpoints[2]
	change := vmLabelMetadataChange{expected: 1, labels: model.VMLabels{"role": "worker"}}
	_, err = change.apply(target.VMLabels, destination.OwnerUID, destination.VMID)
	require.NoError(t, err)
	targetGeneration, err := target.compile()
	require.NoError(t, err)
	require.NotEqual(t, beforeGeneration.id, targetGeneration.id)
	require.Empty(t, targetGeneration.bridge.labelAddressSets)

	binding := testGenerationRecordBinding()
	record := mustCommittedGenerationRecord(t, binding, generationRecordIdentityFromCompiled(beforeGeneration))
	evidence, err := store.create(ctx, record)
	require.NoError(t, err)
	_, _, err = store.writeIntent(ctx, nil, generationRecordFileEvidence{}, intentJournal{
		SchemaVersion: intentJournalSchema, BootID: binding.bootID,
		HostNamespace: NamespaceIdentity{Device: binding.namespace.device, Inode: binding.namespace.inode},
		Before:        before, Target: before,
	})
	require.NoError(t, err)
	global := &fakeFirewallGlobalAuthority{}
	lease := &persistentGenerationLease{state: &persistentGenerationLeaseState{
		global: global, lock: &global.lock, store: store, record: record, evidence: evidence, active: true,
	}}
	kernel := &fakeKernelGenerationApplication{
		observations: []kernelObservation{
			kernelObservationFromCompiled(beforeGeneration), kernelObservationFromCompiled(beforeGeneration),
			kernelObservationFromCompiled(targetGeneration),
		},
		results: []kernelApplicationResult{{commitAttempted: true}},
	}
	transaction, err := loadLogicalPolicyMutation(ctx, store, kernel, lease, record, binding)
	require.NoError(t, err)
	revision, err := transaction.publishVMLabels(ctx, destination.OwnerUID, destination.VMID, change)
	require.NoError(t, err)
	assert.EqualValues(t, 2, revision)
	assert.Equal(t, []string{"observe", "observe", "apply", "observe"}, kernel.events)
	current, _, found, err := store.read(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, generationRecordIdentityFromCompiled(targetGeneration), current.committed)
	labels, revision, err := readCommittedVMLabels(ctx, store, destination.OwnerUID, destination.VMID)
	require.NoError(t, err)
	assert.Equal(t, change.labels, labels)
	assert.EqualValues(t, 2, revision)
}
func readCommittedVMLabels(
	ctx context.Context, store *generationRecordStore, ownerUID uint32, vmID string,
) (model.VMLabels, uint64, error) {
	intent, err := readCommittedLabelIntent(ctx, store)
	if err != nil {
		return nil, 0, err
	}
	return readVMLabels(intent, ownerUID, vmID)
}
