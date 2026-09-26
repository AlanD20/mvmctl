package jailer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/service/network"
)

func TestNetworklessCleaningRetryRetainsExactLease(t *testing.T) {
	record := testActiveInstanceRecord(false)
	record.networkPlacement = model.VMNetworkPlacementNetworkless
	record.launch.networkPlacement = model.VMNetworkPlacementNetworkless
	record.lifecycle = instanceLifecycleCleaning
	record.cleanupGeneration = record.launchGeneration
	state := &cleanupLeaseState{
		record:      record,
		networkless: &network.NetworklessNamespaceLease{},
		recoveredProcesses: &reopenedRecordedProcesses{state: &reopenedRecordedProcessesState{
			retained: []recoveredRecordedProcess{{role: "Firecracker", reaped: true}},
		}},
	}

	retained, err := retainedNetworkPlacementRecovery(state)
	require.NoError(t, err)
	assert.True(t, retained, "retry must reuse the retained namespace cleanup lease")
	assert.NoError(t, verifyRecoveredNetworklessProcessNamespace(context.Background(), state),
		"reaped Firecracker must not require an active namespace handoff")
}

func TestNetworklessCleanedJournalConvergesAfterRootWriteFailure(t *testing.T) {
	record := testActiveInstanceRecord(false)
	record.networkPlacement = model.VMNetworkPlacementNetworkless
	record.launch.networkPlacement = model.VMNetworkPlacementNetworkless
	record.lifecycle = instanceLifecycleCleaning
	record.cleanupGeneration = record.launchGeneration

	cleaned, err := admitNetworklessRecoveryOutcome(record, nil, network.NetworklessRecoveryCleaned)
	require.NoError(t, err)
	assert.True(t, cleaned, "same-generation cleaned journal must retain retry authority")

	state := &cleanupLeaseState{record: record, networklessCleaned: cleaned}
	retained, err := retainedNetworkPlacementRecovery(state)
	require.NoError(t, err)
	assert.True(t, retained, "root record retry must not re-open an already cleaned namespace")

	_, err = admitNetworklessRecoveryOutcome(record, nil, network.NetworklessRecoveryAbsent)
	require.Error(t, err, "missing journal cannot prove active namespace cleanup")
}
