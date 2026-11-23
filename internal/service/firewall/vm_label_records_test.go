package firewall

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

func TestVMLabelRecordsRevisionAndHistoricalRetirement(t *testing.T) {
	vmID := strings.Repeat("a", 32)
	records := make(vmLabelRecords)
	input := model.VMLabels{"role": "controller"}
	initial, err := records.enroll(1000, vmID, input)
	require.NoError(t, err)
	assert.EqualValues(t, 1, initial.Revision)
	input["role"] = "mutated"
	initial.Labels["role"] = "mutated"
	assert.Equal(t, "controller", records[vmID].Labels["role"])

	existing, err := records.enroll(1000, vmID, model.VMLabels{"role": "controller"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, existing.Revision)
	_, err = records.replace(1001, vmID, 1, model.VMLabels{})
	require.Error(t, err)
	_, err = records.replace(1000, vmID, 1, nil)
	require.Error(t, err)

	cleared, err := records.replace(1000, vmID, 1, model.VMLabels{})
	require.NoError(t, err)
	assert.EqualValues(t, 2, cleared.Revision)
	assert.Empty(t, cleared.Labels)
	_, err = records.replace(1000, vmID, 1, model.VMLabels{"role": "controller"})
	require.Error(t, err)
	records[vmID] = vmLabelRecord{OwnerUID: 1000, Revision: 3, Labels: model.VMLabels{}, Retired: true}
	require.NoError(t, records.validate())
	_, err = records.replace(1000, vmID, 3, model.VMLabels{})
	require.Error(t, err)
	_, err = records.enroll(1000, vmID, model.VMLabels{"role": "controller"})
	require.Error(t, err)
	_, err = records.enroll(1001, vmID, nil)
	require.Error(t, err)
}

func TestVMLabelJournalRecoversLatestMetadataWithoutKernelChange(t *testing.T) {
	vmID := strings.Repeat("a", 32)
	before := Intent{VMLabels: make(vmLabelRecords)}
	_, err := before.VMLabels.enroll(1000, vmID, model.VMLabels{"role": "worker"})
	require.NoError(t, err)
	target := cloneIntent(before)
	_, err = target.VMLabels.replace(1000, vmID, 1, model.VMLabels{"role": "controller"})
	require.NoError(t, err)
	assert.Equal(t, "worker", before.VMLabels[vmID].Labels["role"])

	compiled, err := before.compile()
	require.NoError(t, err)
	targetCompiled, err := target.compile()
	require.NoError(t, err)
	id := generationRecordIdentityFromCompiled(compiled)
	require.Equal(t, id, generationRecordIdentityFromCompiled(targetCompiled))
	binding := testGenerationRecordBinding()
	journal := intentJournal{
		SchemaVersion: intentJournalSchema,
		BootID:        binding.bootID,
		HostNamespace: NamespaceIdentity{Device: binding.namespace.device, Inode: binding.namespace.inode},
		Before:        before,
		Target:        target,
	}
	raw, err := json.Marshal(journal)
	require.NoError(t, err)
	reopened, err := decodeIntentJournal(raw)
	require.NoError(t, err)

	record := mustCommittedGenerationRecord(t, binding, id)
	recovered, err := selectPriorBootIntent(record, reopened.Before, reopened.Target)
	require.NoError(t, err)
	assert.EqualValues(t, 2, recovered.VMLabels[vmID].Revision)
	assert.Equal(t, "controller", recovered.VMLabels[vmID].Labels["role"])
	assert.False(t, hostResetIntentEmpty(recovered), "reset must not discard active logical label records")
	recovered.VMLabels[vmID] = vmLabelRecord{OwnerUID: 1000, Revision: 3, Labels: model.VMLabels{}, Retired: true}
	assert.True(t, hostResetIntentEmpty(recovered), "retired IDs carry history without live label authority")
}
