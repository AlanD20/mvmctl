package firewall

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

func TestPublishVMLabelMetadataPreservesRevisionAndForgetsAfterRetirement(t *testing.T) {
	ctx := t.Context()
	store := openTestGenerationRecordStore(t, prepareGenerationRecordTestRoot(t))
	t.Cleanup(func() { require.NoError(t, store.Release(ctx)) })
	kernel := &fakeKernelGenerationApplication{observations: []kernelObservation{
		{empty: true}, {empty: true}, {empty: true},
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
	load := func() *logicalPolicyMutation {
		lease := &persistentGenerationLease{state: &persistentGenerationLeaseState{
			store: store, record: record, evidence: recordEvidence, active: true,
		}}
		mutation, err := loadLogicalPolicyMutation(ctx, store, kernel, lease, record, binding)
		require.NoError(t, err)
		return mutation
	}

	revision, err := load().publishVMLabels(ctx, 1000, vmID, 0, model.VMLabels{"role": "worker"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, revision)
	_, err = load().publishVMLabels(ctx, 1001, vmID, 1, model.VMLabels{})
	require.Error(t, err)
	revision, err = load().publishVMLabels(ctx, 1000, vmID, 1, model.VMLabels{"role": "controller"})
	require.NoError(t, err)
	assert.EqualValues(t, 2, revision)
	labels, observedRevision, err = readCommittedVMLabels(ctx, store, 1000, vmID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, observedRevision)
	assert.Equal(t, model.VMLabels{"role": "controller"}, labels)
	_, _, err = readCommittedVMLabels(ctx, store, 1001, vmID)
	require.Error(t, err)
	_, err = load().publishVMLabels(ctx, 1000, vmID, 1, model.VMLabels{})
	require.Error(t, err)
	require.Error(t, load().forgetVMLabels(ctx, 1001, vmID))
	require.NoError(t, load().forgetVMLabels(ctx, 1000, vmID))
	require.NoError(t, load().forgetVMLabels(ctx, 1000, vmID))
	require.NoError(t, load().forgetVMLabels(ctx, 1000, strings.Repeat("b", 32)))

	journal, _, _, found, err := store.readIntent(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, journal.Before.VMLabels)
	assert.Empty(t, journal.Target.VMLabels)
	assert.Equal(t, []string{"observe", "observe", "observe"}, kernel.events)
}

func TestForgetVMLabelsRemovesExactExecGrantsForUnlabeledVM(t *testing.T) {
	ctx := t.Context()
	store := openTestGenerationRecordStore(t, prepareGenerationRecordTestRoot(t))
	t.Cleanup(func() { require.NoError(t, store.Release(ctx)) })
	const source = "11111111111111111111111111111111"
	const destination = "22222222222222222222222222222222"
	before := Intent{VMLabels: vmLabelRecords{
		source:      {OwnerUID: 1000, Revision: 1, Labels: model.VMLabels{"role": "worker"}},
		destination: {OwnerUID: 1000, Revision: 1, Labels: model.VMLabels{}},
	}}
	policy := ExecPolicyIntent{
		OwnerUID: 1000, Source: VMPolicyEndpoint{VMID: source}, Destination: VMPolicyEndpoint{VMID: destination},
		TargetUser: "alice", Incarnation: "11111111-1111-4111-8111-111111111111",
	}
	var err error
	policy.ID, err = policy.identity()
	require.NoError(t, err)
	before.ExecPolicies = []ExecPolicyIntent{policy}
	compiled, err := before.compile()
	require.NoError(t, err)
	binding := testGenerationRecordBinding()
	_, err = store.create(
		ctx,
		mustCommittedGenerationRecord(t, binding, generationRecordIdentityFromCompiled(compiled)),
	)
	require.NoError(t, err)
	projection, err := before.projectExecPolicies(1000)
	require.NoError(t, err)
	require.NoError(t, store.writeExecPolicyIndex(ctx, 1000, projection, true))
	allowed, err := store.execPolicyAllowed(ctx, 1000, source, destination, "alice")
	require.NoError(t, err)
	require.True(t, allowed)
	kernel := &fakeKernelGenerationApplication{observations: []kernelObservation{{empty: true}}}
	mutation := &logicalPolicyMutation{
		store:    store,
		kernel:   kernel,
		before:   before,
		compiled: compiled,
		binding:  binding,
	}

	require.NoError(t, mutation.forgetVMLabels(ctx, 1000, destination))

	recovered, err := readCommittedLabelIntent(ctx, store)
	require.NoError(t, err)
	assert.NotContains(t, recovered.VMLabels, destination)
	assert.Contains(t, recovered.VMLabels, source)
	assert.Empty(t, recovered.ExecPolicies)
	allowed, err = store.execPolicyAllowed(ctx, 1000, source, destination, "alice")
	require.NoError(t, err)
	assert.False(t, allowed)
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
	labelsAfter := model.VMLabels{"role": "worker"}
	_, err = target.VMLabels.replace(destination.OwnerUID, destination.VMID, 1, labelsAfter)
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
	revision, err := transaction.publishVMLabels(ctx, destination.OwnerUID, destination.VMID, 1, labelsAfter)
	require.NoError(t, err)
	assert.EqualValues(t, 2, revision)
	assert.Equal(t, []string{"observe", "observe", "apply", "observe"}, kernel.events)
	current, _, found, err := store.read(ctx)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, generationRecordIdentityFromCompiled(targetGeneration), current.committed)
	labels, revision, err := readCommittedVMLabels(ctx, store, destination.OwnerUID, destination.VMID)
	require.NoError(t, err)
	assert.Equal(t, labelsAfter, labels)
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
