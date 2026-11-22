package firewall

import (
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
	vmID := strings.Repeat("a", 32)
	change := vmLabelMetadataChange{labels: model.VMLabels{"role": "worker"}}
	publish := func(owner uint32, change vmLabelMetadataChange) (uint64, error) {
		return publishVMLabelMetadata(ctx, store, kernel, record, binding, owner, vmID, change)
	}

	revision, err := publish(1000, change)
	require.NoError(t, err)
	assert.EqualValues(t, 1, revision)
	_, err = publish(1001, vmLabelMetadataChange{expected: 1, labels: model.VMLabels{}})
	require.Error(t, err)
	revision, err = publish(1000, vmLabelMetadataChange{expected: 1, labels: model.VMLabels{"role": "controller"}})
	require.NoError(t, err)
	assert.EqualValues(t, 2, revision)
	_, err = publish(1000, vmLabelMetadataChange{expected: 1, labels: model.VMLabels{}})
	require.Error(t, err)
	for range 2 {
		revision, err = publish(1000, vmLabelMetadataChange{retire: true})
		require.NoError(t, err)
		assert.EqualValues(t, 3, revision)
	}
	_, err = publish(1000, change)
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
