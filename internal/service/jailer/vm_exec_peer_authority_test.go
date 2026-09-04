package jailer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type vmExecPeerAdmissionFixture struct {
	credentials   unix.Ucred
	flags         int
	locators      []instanceProcessLocator
	locatorIndex  int
	liveness      []bool
	livenessIndex int
	livenessError map[int]error
	closeCalls    int
}

// Rationale: every peer descriptor property is independently attacker-controlled at the AF_UNIX admission boundary.
// The table proves that no single mismatched credential, descriptor, locator, or liveness signal is accepted.
func TestVMExecFirecrackerPeerRejectsInexactPeerEvidence(t *testing.T) {
	tests := map[string]struct {
		mutate      func(*vmExecPeerAdmissionFixture)
		wantMessage string
	}{
		"missing_close_on_exec": {
			mutate:      func(fixture *vmExecPeerAdmissionFixture) { fixture.flags = 0 },
			wantMessage: "close-on-exec",
		},
		"PID_mismatch": {
			mutate:      func(fixture *vmExecPeerAdmissionFixture) { fixture.credentials.Pid++ },
			wantMessage: "PID does not match",
		},
		"UID_mismatch": {
			mutate:      func(fixture *vmExecPeerAdmissionFixture) { fixture.credentials.Uid++ },
			wantMessage: "credentials do not match",
		},
		"GID_mismatch": {
			mutate:      func(fixture *vmExecPeerAdmissionFixture) { fixture.credentials.Gid++ },
			wantMessage: "credentials do not match",
		},
		"initial_locator_mismatch": {
			mutate: func(fixture *vmExecPeerAdmissionFixture) {
				fixture.locators[0].startTimeTicks++
			},
			wantMessage: "pidfd does not match Firecracker locator",
		},
		"locator_drift": {
			mutate: func(fixture *vmExecPeerAdmissionFixture) {
				fixture.locators[1].startTimeTicks++
			},
			wantMessage: "locator changed during admission",
		},
		"peer_exit": {
			mutate:      func(fixture *vmExecPeerAdmissionFixture) { fixture.liveness[0] = false },
			wantMessage: "peer is not live",
		},
		"liveness_error": {
			mutate: func(fixture *vmExecPeerAdmissionFixture) {
				fixture.livenessError[0] = errors.New("injected peer liveness failure")
			},
			wantMessage: "inspect connection peer liveness",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			record, identity := activeRecoveryTestRecord(t)
			fixture := newVMExecPeerAdmissionFixture(record)
			test.mutate(fixture)

			peer, err := admitVMExecFirecrackerPeer(
				t.Context(),
				fixture.deps(),
				identity.deps(),
				record,
				testVMExecRetainedProcesses(record),
				50,
				"source",
			)

			assert.Nil(t, peer)
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantMessage)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, record.vmID, domainErr.Entity)
			assert.Equal(t, 1, fixture.closeCalls)
		})
	}
}

func TestVMExecFirecrackerPeerAdmissionAndRecheckHonorCancellation(t *testing.T) {
	t.Run("admission", func(t *testing.T) {
		record, identity := activeRecoveryTestRecord(t)
		fixture := newVMExecPeerAdmissionFixture(record)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		peer, err := admitVMExecFirecrackerPeer(
			ctx,
			fixture.deps(),
			identity.deps(),
			record,
			testVMExecRetainedProcesses(record),
			50,
			"source",
		)

		assert.Nil(t, peer)
		require.ErrorIs(t, err, context.Canceled)
		assert.Zero(t, fixture.closeCalls)
	})

	t.Run("recheck", func(t *testing.T) {
		record, identity := activeRecoveryTestRecord(t)
		fixture := newVMExecPeerAdmissionFixture(record)
		processes := testVMExecRetainedProcesses(record)
		peer, err := admitVMExecFirecrackerPeer(
			t.Context(), fixture.deps(), identity.deps(), record, processes, 50, "source",
		)
		require.NoError(t, err)
		require.NotNil(t, peer)
		t.Cleanup(func() { require.NoError(t, peer.Release(context.Background())) })
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		err = peer.recheck(ctx, identity.deps(), record, processes)

		require.ErrorIs(t, err, context.Canceled)
	})
}

// Rationale: SO_PEERPIDFD is the stable kernel binding between a retained socket and its Firecracker peer. Numeric
// credentials alone are replayable after PID reuse, so kernels without peer-pidfd support must fail closed.
func TestVMExecFirecrackerPeerRequiresPeerPIDFD(t *testing.T) {
	record, fake := activeRecoveryTestRecord(t)
	closeCalls := 0
	deps := vmExecPeerAuthorityDeps{
		peerCredentials: func(context.Context, int) (*unix.Ucred, error) {
			return &unix.Ucred{
				Pid: int32(record.launch.process.pid),
				Uid: record.active.userIDs.effective,
				Gid: record.active.groupIDs.effective,
			}, nil
		},
		peerPIDFD: func(context.Context, int) (int, error) {
			return -1, unix.ENOPROTOOPT
		},
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		deriveLocator: func(context.Context, int) (instanceProcessLocator, error) {
			return record.launch.process, nil
		},
		pidfdAlive: func(context.Context, int) (bool, error) { return true, nil },
		close: func(context.Context, int) error {
			closeCalls++
			return nil
		},
	}

	peer, err := admitVMExecFirecrackerPeer(
		t.Context(),
		deps,
		fake.deps(),
		record,
		testVMExecRetainedProcesses(record),
		50,
		"source",
	)

	assert.Nil(t, peer)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.ErrorIs(t, err, unix.ENOPROTOOPT)
	assert.Zero(t, closeCalls)
}

func TestVMExecFirecrackerPeerRejectionPreservesPIDFDCloseFailure(t *testing.T) {
	record, fake := activeRecoveryTestRecord(t)
	closeErr := errors.New("injected peer pidfd close failure")
	deps := vmExecPeerAuthorityDeps{
		peerCredentials: func(context.Context, int) (*unix.Ucred, error) {
			return &unix.Ucred{
				Pid: int32(record.launch.process.pid),
				Uid: record.active.userIDs.effective + 1,
				Gid: record.active.groupIDs.effective,
			}, nil
		},
		peerPIDFD:       func(context.Context, int) (int, error) { return 900, nil },
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		deriveLocator: func(context.Context, int) (instanceProcessLocator, error) {
			return record.launch.process, nil
		},
		pidfdAlive: func(context.Context, int) (bool, error) { return true, nil },
		close:      func(context.Context, int) error { return closeErr },
	}

	peer, err := admitVMExecFirecrackerPeer(
		t.Context(),
		deps,
		fake.deps(),
		record,
		testVMExecRetainedProcesses(record),
		50,
		"source",
	)

	assert.Nil(t, peer)
	require.Error(t, err)
	assert.ErrorIs(t, err, closeErr)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["vm_exec_session_cleanup_failed"])
}

// Rationale: a same-VM shell capability with no retained Firecracker pidfd would reduce the peer proof to the new
// socket pidfd alone. Admission must require the already-proven process set retained by active-pair admission.
func TestVMExecFirecrackerPeerRejectsIncompleteRetainedProcessAuthority(t *testing.T) {
	record, fake := activeRecoveryTestRecord(t)
	closeCalls := 0
	deps := vmExecPeerAuthorityDeps{
		peerCredentials: func(context.Context, int) (*unix.Ucred, error) {
			return &unix.Ucred{
				Pid: int32(record.launch.process.pid),
				Uid: record.active.userIDs.effective,
				Gid: record.active.groupIDs.effective,
			}, nil
		},
		peerPIDFD:       func(context.Context, int) (int, error) { return 900, nil },
		descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
		deriveLocator: func(context.Context, int) (instanceProcessLocator, error) {
			return record.launch.process, nil
		},
		pidfdAlive: func(context.Context, int) (bool, error) { return true, nil },
		close: func(context.Context, int) error {
			closeCalls++
			return nil
		},
	}

	peer, err := admitVMExecFirecrackerPeer(
		t.Context(),
		deps,
		fake.deps(),
		record,
		&reopenedRecordedProcesses{state: &reopenedRecordedProcessesState{vmID: record.vmID}},
		50,
		"source",
	)
	if peer != nil {
		t.Cleanup(func() { require.NoError(t, peer.Release(context.Background())) })
	}

	assert.Nil(t, peer)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Equal(t, record.vmID, domainErr.Entity)
	assert.Zero(t, closeCalls)
}

// newVMExecPeerAdmissionFixture starts from a fully exact, live peer proof; each rejection case mutates one signal.
func newVMExecPeerAdmissionFixture(record instanceRecord) *vmExecPeerAdmissionFixture {
	return &vmExecPeerAdmissionFixture{
		credentials: unix.Ucred{
			Pid: int32(record.launch.process.pid),
			Uid: record.active.userIDs.effective,
			Gid: record.active.groupIDs.effective,
		},
		flags:         unix.FD_CLOEXEC,
		locators:      []instanceProcessLocator{record.launch.process, record.launch.process},
		liveness:      []bool{true, true},
		livenessError: make(map[int]error),
	}
}

// deps returns deterministic peer observations in call order, allowing the test to distinguish initial mismatch from
// drift discovered after the expensive complete-identity verification.
func (fixture *vmExecPeerAdmissionFixture) deps() vmExecPeerAuthorityDeps {
	return vmExecPeerAuthorityDeps{
		peerCredentials: func(context.Context, int) (*unix.Ucred, error) {
			credentials := fixture.credentials
			return &credentials, nil
		},
		peerPIDFD:       func(context.Context, int) (int, error) { return 900, nil },
		descriptorFlags: func(context.Context, int) (int, error) { return fixture.flags, nil },
		deriveLocator: func(context.Context, int) (instanceProcessLocator, error) {
			index := fixture.locatorIndex
			fixture.locatorIndex++
			if index >= len(fixture.locators) {
				return instanceProcessLocator{}, fmt.Errorf("unexpected peer locator observation %d", index)
			}
			return fixture.locators[index], nil
		},
		pidfdAlive: func(context.Context, int) (bool, error) {
			index := fixture.livenessIndex
			fixture.livenessIndex++
			if err := fixture.livenessError[index]; err != nil {
				return false, err
			}
			if index >= len(fixture.liveness) {
				return false, fmt.Errorf("unexpected peer liveness observation %d", index)
			}
			return fixture.liveness[index], nil
		},
		close: func(context.Context, int) error {
			fixture.closeCalls++
			return nil
		},
	}
}

// testVMExecRetainedProcesses constructs the complete retained process set that peer admission requires. Its effects
// are inert and exact so peer tests isolate AF_UNIX evidence instead of repeating process-recovery scenarios.
func testVMExecRetainedProcesses(record instanceRecord) *reopenedRecordedProcesses {
	retained := []recoveredRecordedProcess{{
		role: "Firecracker", locator: record.launch.process, pidfd: 700,
	}}
	if record.launch.relay.kind == instanceRelayPresent {
		retained = append(retained, recoveredRecordedProcess{
			role: "relay", locator: record.launch.relay.locator, pidfd: 701,
		})
	}
	return &reopenedRecordedProcesses{state: &reopenedRecordedProcessesState{
		deps: processRecoveryDeps{
			pidfdOpen:       func(context.Context, int) (int, error) { return -1, nil },
			pidfdAlive:      func(context.Context, int) (bool, error) { return true, nil },
			descriptorFlags: func(context.Context, int) (int, error) { return unix.FD_CLOEXEC, nil },
			deriveLocator: func(_ context.Context, fd int) (instanceProcessLocator, error) {
				for _, process := range retained {
					if process.pidfd == fd {
						return process.locator, nil
					}
				}
				return instanceProcessLocator{}, errors.New("unexpected retained test pidfd")
			},
			terminatePIDFD: func(context.Context, int) error { return nil },
			waitReapPIDFD:  func(context.Context, int) error { return nil },
			close:          func(context.Context, int) error { return nil },
		},
		vmID:     record.vmID,
		retained: retained,
	}}
}
