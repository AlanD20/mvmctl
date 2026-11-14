package jailer

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

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

func TestNetworklessCleaningRefreshesExitedFirecracker(t *testing.T) {
	// Rationale: graceful stop can exit an admitted task before namespace cleanup.
	// Missing /proc state must converge only after its retained pidfd proves exit and reap succeeds.
	tests := map[string]struct {
		alive         bool
		reused        bool
		reapErr       error
		withNamespace bool
		wantErr       error
		wantMessage   string
	}{
		"failed_reap_retains_cleanup": {
			reapErr: unix.EIO, withNamespace: true, wantErr: unix.EIO,
		},
		"live_task_requires_namespace_authority": {
			alive: true, wantMessage: "live Firecracker has no networkless namespace authority",
		},
		"live_reused_pid_refuses_namespace_admission": {
			alive: true, reused: true, withNamespace: true, wantMessage: "lifecycle Firecracker PID was reused",
		},
		"exited_unreaped_task_skips_live_namespace_admission": {withNamespace: true},
		"exited_task_requires_no_live_namespace_authority":    {},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			record := testActiveInstanceRecord(false)
			record.networkPlacement = model.VMNetworkPlacementNetworkless
			record.launch.networkPlacement = model.VMNetworkPlacementNetworkless
			record.lifecycle = instanceLifecycleCleaning
			record.cleanupGeneration = record.launchGeneration
			processes := &reopenedRecordedProcessesState{
				vmID: record.vmID,
				retained: []recoveredRecordedProcess{{
					role: "Firecracker", locator: record.launch.process, pidfd: 10,
				}},
				deps: processRecoveryDeps{
					pidfdAlive: func(context.Context, int) (bool, error) { return tc.alive, nil },
					deriveLocator: func(context.Context, int) (instanceProcessLocator, error) {
						locator := record.launch.process
						if tc.reused {
							locator.startTimeTicks++
						}
						return locator, nil
					},
					waitReapPIDFD: func(context.Context, int) error { return tc.reapErr },
				},
			}
			state := &cleanupLeaseState{
				record: record, recoveredProcesses: &reopenedRecordedProcesses{state: processes},
			}
			if tc.withNamespace {
				state.networkless = &network.NetworklessNamespaceLease{}
			}
			// The empty lease cannot admit a live namespace; dead-task success must skip it.
			err := verifyRecoveredNetworklessProcessNamespace(t.Context(), state)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			if tc.wantMessage != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantMessage)
				return
			}
			require.NoError(t, err)
			assert.True(t, processes.retained[0].reaped,
				"namespace cleanup must retain exact pidfd exit and reap completion")
			assert.False(t, processes.retained[0].signalSent,
				"an already exited task needs no termination signal")
		})
	}
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
