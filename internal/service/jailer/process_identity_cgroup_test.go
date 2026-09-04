package jailer

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
)

// Rationale: The cgroup lease supplies private values, but the recovered verifier must still reject a locator, VM,
// or observation path that differs from the exact process authority captured when it was constructed.
func TestRetainedRecoveredCgroupProcessVerifierRejectsForeignAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		locator     instanceProcessLocator
		observation processObservation
		vmID        string
		want        string
	}{
		"locator": {
			locator:     instanceProcessLocator{pid: 9999, startTimeTicks: 1},
			observation: launchingRecoveryProcessObservation(),
			vmID:        testVMID,
			want:        "locator does not match",
		},
		"observation start tick": {
			locator: processTestLocator(),
			observation: processObservation{
				startTimeTicks: processTestFirecrackerTicks + 1,
				cgroupPath:     infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + testVMID,
			},
			vmID: testVMID,
			want: "observation does not match",
		},
		"observation VM path": {
			locator: processTestLocator(),
			observation: processObservation{
				startTimeTicks: processTestFirecrackerTicks,
				cgroupPath:     infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/other-vm",
			},
			vmID: testVMID,
			want: "observation does not match",
		},
		"VM": {
			locator: processTestLocator(),
			observation: processObservation{
				startTimeTicks: processTestFirecrackerTicks,
				cgroupPath:     infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/other-vm",
			},
			vmID: "other-vm",
			want: "does not match recovered authority",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			processes, probe := newLaunchingRecoveryProcessTestAuthority()
			verifier := retainedRecoveredCgroupProcessVerifier{
				processes: processes,
				locator:   processTestLocator(),
				vmID:      tc.vmID,
			}

			err := verifier.verifyBeforeCgroupMembership(t.Context(), tc.locator, tc.observation)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, probe.aliveFDs, processTestRelayPIDFD)
			assert.NotContains(t, probe.locatorFDs, processTestRelayPIDFD)
			require.NoError(t, processes.Release(t.Context()))
		})
	}
}

func TestRetainedRecoveredCgroupProcessVerifierReprovesOnlyFirecrackerBeforeAndAfter(t *testing.T) {
	t.Parallel()

	processes, probe := newLaunchingRecoveryProcessTestAuthority()
	verifier := retainedRecoveredCgroupProcessVerifier{
		processes: processes,
		locator:   processTestLocator(),
		vmID:      testVMID,
	}

	require.NoError(t, verifier.verifyBeforeCgroupMembership(
		t.Context(),
		processTestLocator(),
		launchingRecoveryProcessObservation(),
	))
	require.NoError(t, verifier.verifyAfterCgroupMembership(
		t.Context(),
		processTestLocator(),
		launchingRecoveryProcessObservation(),
	))
	assert.Equal(t, []int{processTestFirecrackerPIDFD, processTestFirecrackerPIDFD}, probe.aliveFDs)
	assert.Equal(t, []int{processTestFirecrackerPIDFD, processTestFirecrackerPIDFD}, probe.locatorFDs)
	assert.NotContains(t, probe.aliveFDs, processTestRelayPIDFD)
	assert.NotContains(t, probe.locatorFDs, processTestRelayPIDFD)
	require.NoError(t, processes.Release(t.Context()))
}

// Rationale: Membership text alone is numeric PID evidence. Recovery must bracket it with the same retained pidfd
// locator and liveness proof so both pre-read substitution and post-read drift fail closed.
func TestRetainedRecoveredCgroupProcessVerifierRejectsPidfdDriftBeforeAndAfterMembership(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure func(*launchingRecoveryProcessProbe)
		beforeErr bool
		want      string
	}{
		"death before membership": {
			configure: func(probe *launchingRecoveryProcessProbe) {
				probe.alive = []bool{false}
			},
			beforeErr: true,
			want:      "exited during identity verification",
		},
		"locator drift before membership": {
			configure: func(probe *launchingRecoveryProcessProbe) {
				probe.locators = []instanceProcessLocator{{pid: 9999, startTimeTicks: 1}}
			},
			beforeErr: true,
			want:      "pidfd locator does not match",
		},
		"death after membership": {
			configure: func(probe *launchingRecoveryProcessProbe) {
				probe.alive = []bool{true, false}
			},
			want: "exited during identity verification",
		},
		"locator drift after membership": {
			configure: func(probe *launchingRecoveryProcessProbe) {
				probe.locators = []instanceProcessLocator{
					processTestLocator(),
					{pid: 9999, startTimeTicks: 1},
				}
			},
			want: "pidfd locator does not match",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			processes, probe := newLaunchingRecoveryProcessTestAuthority()
			tc.configure(probe)
			verifier := retainedRecoveredCgroupProcessVerifier{
				processes: processes,
				locator:   processTestLocator(),
				vmID:      testVMID,
			}
			observation := launchingRecoveryProcessObservation()
			beforeErr := verifier.verifyBeforeCgroupMembership(
				t.Context(), processTestLocator(), observation,
			)
			if tc.beforeErr {
				require.Error(t, beforeErr)
				assert.Contains(t, beforeErr.Error(), tc.want)
				assert.NotContains(t, probe.aliveFDs, processTestRelayPIDFD)
				assert.NotContains(t, probe.locatorFDs, processTestRelayPIDFD)
				require.NoError(t, processes.Release(t.Context()))
				return
			}
			require.NoError(t, beforeErr)
			err := verifier.verifyAfterCgroupMembership(t.Context(), processTestLocator(), observation)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.NotContains(t, probe.aliveFDs, processTestRelayPIDFD)
			assert.NotContains(t, probe.locatorFDs, processTestRelayPIDFD)
			require.NoError(t, processes.Release(t.Context()))
		})
	}
}
