package jailer

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

// Internal tests exercise opaque lease lifetime and independently varied process evidence.
func floatingIPTestAdmission(
	t *testing.T,
) (*FloatingIPAdmissionLease, *vmExecPeerAdmissionFixture, *fakeProcessIdentityDeps) {
	t.Helper()
	record, identity := activeRecoveryTestRecord(t)
	peer := newVMExecPeerAdmissionFixture(record)
	peer.locators = slices.Repeat([]instanceProcessLocator{record.launch.process}, 64)
	peer.liveness = slices.Repeat([]bool{true}, 64)
	lease := &FloatingIPAdmissionLease{state: &floatingIPAdmissionState{
		active: &activeLease{state: &activeLeaseState{record: record}}, record: record,
		processes: testVMExecRetainedProcesses(record), directory: -1, peerPIDFD: 900,
		identityDeps: identity.deps(), peerDeps: peer.deps(),
	}}
	return lease, peer, identity
}

func TestFloatingIPAdmissionIdentityComesFromRetainedRecord(t *testing.T) {
	lease, _, _ := floatingIPTestAdmission(t)
	got, err := lease.Identity(t.Context())
	require.NoError(t, err)
	// INPUT: the independently built active-record fixture binds these exact identities.
	want := FloatingIPVMIdentity{OwnerUID: testAuthorityUID, VMID: testVMID,
		BootID: processTestHostBootID, LaunchGeneration: 1, ExecutionUID: testExecutionUID}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("identity (-want +got):\n%s", diff)
	}
}

func TestFloatingIPAdmissionRecheckRefusesChangedAuthority(t *testing.T) {
	for _, name := range []string{"peer_exit", "locator_drift", "boot_change", "process_authority_released"} {
		t.Run(name, func(t *testing.T) {
			lease, peer, identity := floatingIPTestAdmission(t)
			switch name {
			case "peer_exit":
				peer.liveness[0] = false
			case "locator_drift":
				peer.locators[1].startTimeTicks++
			case "boot_change":
				identity.bootIDs[0] = processOtherBootID
			case "process_authority_released":
				require.NoError(t, lease.state.processes.Release(t.Context()))
			}
			observed, err := lease.Identity(t.Context())
			require.Error(t, err)
			require.Equal(t, FloatingIPVMIdentity{}, observed)
		})
	}
}

func TestFloatingIPAdmissionRejectsCancellationAndIncompleteIdentity(t *testing.T) {
	lease, peer, _ := floatingIPTestAdmission(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, lease.Recheck(ctx), context.Canceled)
	_, err := lease.RetainTopologyHold(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, peer.locatorIndex)
	record := lease.state.record
	for _, name := range []string{"no_launch", "no_active", "root_UID", "owner_UID", "root_GID", "owner_GID"} {
		t.Run(name, func(t *testing.T) {
			candidate := cloneInstanceRecord(record)
			switch name {
			case "no_launch":
				candidate.launch = nil
			case "no_active":
				candidate.active = nil
			case "root_UID":
				candidate.active.userIDs.effective = 0
			case "owner_UID":
				candidate.active.userIDs.effective = candidate.ownerUID
			case "root_GID":
				candidate.active.groupIDs.effective = 0
			case "owner_GID":
				candidate.active.groupIDs.effective = candidate.launch.ownerGID
			}
			require.Error(t, requireFloatingIPExecutionIdentity(candidate))
		})
	}
}

func TestFloatingIPAdmissionOpenRefusesInvalidSelectorBeforeAuthority(t *testing.T) {
	server := &FloatingIPAdmissionServer{}
	lease, err := server.Open(t.Context(), testAuthorityUID, testAuthorityGID, FloatingIPAdmissionRequest{VMID: "../vm"})
	require.Error(t, err)
	require.Nil(t, lease)
	lease, err = server.Open(nil, testAuthorityUID, testAuthorityGID, FloatingIPAdmissionRequest{VMID: testVMID})
	require.Error(t, err)
	require.Nil(t, lease)
}

func TestFloatingIPAdmissionReleaseSerializesWithRecheck(t *testing.T) {
	lease, _, _ := floatingIPTestAdmission(t)
	fd, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	lease.state.peerPIDFD = fd
	t.Cleanup(func() { _ = lease.Release(context.Background()) })
	entered := make(chan struct{})
	resume := make(chan struct{})
	derive := lease.state.peerDeps.deriveLocator
	first := true
	lease.state.peerDeps.deriveLocator = func(ctx context.Context, fd int) (instanceProcessLocator, error) {
		if first {
			first = false
			close(entered)
			<-resume
		}
		return derive(ctx, fd)
	}
	rechecked := make(chan error, 1)
	go func() { rechecked <- lease.Recheck(t.Context()) }()
	<-entered
	released := make(chan error, 1)
	go func() { released <- lease.Release(t.Context()) }()
	// Recheck owns the proof until its complete identity observation finishes.
	_, err = unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	close(resume)
	require.NoError(t, <-rechecked)
	require.NoError(t, <-released)
	require.Error(t, lease.Recheck(t.Context()))
	_, err = unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.ErrorIs(t, err, unix.EBADF)
}

// This test needs the real root-owned authorityfs lock, not a tuple-backed fake hold.
func TestFloatingIPAdmissionGenuineChildHoldBlocksReleaseUntilConsumed(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires disposable root-test VM")
	}
	lease, _, _ := floatingIPTestAdmission(t)
	ctx := t.Context()
	root, err := authorityfs.OpenJailer(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Release(context.Background())) })
	uid, err := root.OpenUID(ctx, testAuthorityUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uid.Release(context.Background())) })
	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	vm, err := uid.AcquireVM(ctx, key)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, vm.Release(context.Background())) })
	lease.state.active.state.vmLock = &vmAuthorityLease{state: &vmAuthorityLeaseState{
		value: &systemVMAuthority{value: vm}, ownerUID: testAuthorityUID, key: key, active: true,
	}}
	// Use a genuine owned descriptor to catch accidental proof cleanup on rejected release.
	lease.state.peerPIDFD, err = unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	proofFD := lease.state.peerPIDFD
	t.Cleanup(func() {
		if !lease.state.released {
			_ = lease.Release(context.Background())
		}
	})
	hold, err := lease.RetainTopologyHold(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, hold.Release(context.Background())) })
	owner, bound, err := hold.Binding()
	require.NoError(t, err)
	require.Equal(t, testAuthorityUID, owner)
	require.Equal(t, key, bound)
	copyOfLease := *lease
	require.ErrorIs(t, copyOfLease.Release(ctx), authorityfs.ErrTopologyRetained)
	require.False(t, lease.state.released)
	require.False(t, lease.state.active.state.released)
	_, err = unix.FcntlInt(uintptr(proofFD), unix.F_GETFD, 0)
	require.NoError(t, err)
	require.NoError(t, lease.Recheck(ctx))
	transferred, err := hold.Transfer(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, transferred.Release(context.Background())) })
	require.ErrorIs(t, lease.Release(ctx), authorityfs.ErrTopologyRetained)
	require.NoError(t, transferred.Release(ctx))
	// Concurrent copied callers close one proof; no recheck/transfer can pass after release.
	results := make(chan error, 8)
	for range 8 {
		go func() { results <- copyOfLease.Release(ctx) }()
	}
	for range 8 {
		require.NoError(t, <-results)
	}
	_, err = unix.FcntlInt(uintptr(proofFD), unix.F_GETFD, 0)
	require.ErrorIs(t, err, unix.EBADF)
	require.Error(t, lease.Recheck(ctx))
	_, err = lease.RetainTopologyHold(ctx)
	require.Error(t, err)
	_, _, err = hold.Binding()
	require.Error(t, err)
	_, _, err = transferred.Binding()
	require.Error(t, err)
}
