package jailer

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

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
