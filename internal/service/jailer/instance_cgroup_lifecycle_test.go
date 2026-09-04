package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func newTestPreparedJailDestinationForCgroupHandoff() *preparedJailDestination {
	const parentFD = 7200
	parent := jailDestinationBinding{
		kind:     jailDestinationFirecracker,
		fd:       parentFD,
		identity: instanceObjectIdentity{device: 8, inode: 900},
		mount:    jailDestinationMountIdentity{kind: managedCacheMountIdentityUnique, id: 77},
	}
	return &preparedJailDestination{state: &preparedJailDestinationState{
		deps: jailDestinationDeps{
			openAt: func(context.Context, int, string, int, uint32) (int, error) { return -1, unix.ENOENT },
			fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
				if fd != parentFD {
					return errors.New("unexpected jail destination descriptor")
				}
				*stat = unix.Stat_t{
					Dev: parent.identity.device, Ino: parent.identity.inode,
					Mode: unix.S_IFDIR | 0o700, Uid: 0, Gid: 0,
				}
				return nil
			},
			statx: func(_ context.Context, _ int, _ int, _ int, stat *unix.Statx_t) error {
				stat.Mask = unix.STATX_MNT_ID_UNIQUE
				stat.Mnt_id = parent.mount.id
				return nil
			},
			close: func(context.Context, int) error { return nil },
		},
		vmID: testVMID, parent: parent, status: preparedJailDestinationAbsent,
	}}
}

func readyTestLaunchForCgroupHandoff(
	t *testing.T,
	authority *instanceAuthority,
	relayPresent bool,
) *launchLease {
	t.Helper()
	prepared := prepareTestRelease(t, authority)
	launch, err := prepared.registerLaunching(
		t.Context(),
		testInstanceCaller(),
		testLaunchIntent(relayPresent),
		newTestBlockedLaunchProcesses(t, relayPresent),
	)
	require.NoError(t, err)
	launch.state.preparedJailDestination = newTestPreparedJailDestinationForCgroupHandoff()
	launch.state.networkNamespace = &topologySelectedNetworkNamespace{
		identity: instanceObjectIdentity{device: 4, inode: 4026533101},
	}
	return launch
}

func TestLaunchManagedVMCgroupPreparationIsExclusive(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	cgroup := newManagedVMCgroupTestFixture(t)
	authority.cgroupDeps = cgroup.launchDeps()
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)

	require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
	original := launch.state.preparedManagedVMCgroup
	assert.True(t, launch.snapshot().hasPreparedManagedVMCgroup)
	assert.False(t, launch.snapshot().hasManagedVMCgroup)
	require.Error(t, launch.Release(t.Context()))
	require.Error(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
	assert.Same(t, original, launch.state.preparedManagedVMCgroup)
	assert.True(t, launch.snapshot().hasPreparedManagedVMCgroup)
	assert.False(t, launch.snapshot().hasManagedVMCgroup)

	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	assert.Same(t, original, cleanup.state.preparedManagedVMCgroup)
	assert.False(t, launch.snapshot().hasPreparedManagedVMCgroup)
	assert.False(t, launch.snapshot().hasManagedVMCgroup)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestLaunchReleaseToJailerRequiresPreparedManagedVMCgroup(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	launch := readyTestLaunchForCgroupHandoff(t, authority, false)

	require.Error(t, launch.releaseToJailer(t.Context()))
	assert.Equal(t, blockedLaunchProcessesOpen, launch.state.processes.state.status)
	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestLaunchReleaseToJailerRechecksManagedVMCgroupAbsence(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	cgroup := newManagedVMCgroupTestFixture(t)
	cgroupDeps := cgroup.launchDeps()
	realOpenAt2 := cgroupDeps.openAt2
	realClose := cgroupDeps.close
	leafFD := -1
	leafOpens := 0
	leafCloses := 0
	cgroupDeps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		opened, err := realOpenAt2(ctx, fd, name, how)
		if err == nil && name == testVMID {
			leafFD = opened
			leafOpens++
		}
		return opened, err
	}
	cgroupDeps.close = func(ctx context.Context, fd int) error {
		if fd == leafFD {
			leafCloses++
		}
		return realClose(ctx, fd)
	}
	authority.cgroupDeps = cgroupDeps
	launch := readyTestLaunchForCgroupHandoff(t, authority, false)
	require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
	cgroup.addVMLeaf(testVMID)

	require.Error(t, launch.releaseToJailer(t.Context()))
	assert.Equal(t, 1, leafOpens)
	assert.Equal(t, 1, leafCloses)
	assert.Equal(t, blockedLaunchProcessesOpen, launch.state.processes.state.status)
	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	cgroup.setMembership(testVMID, "")
	require.Error(t, cleanup.Complete(t.Context()))
	assert.Equal(t, 2, leafOpens)
	assert.Equal(t, 2, leafCloses)
	assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
	assert.Nil(t, launch.state.preparedManagedVMCgroup)
	assert.NotNil(t, cleanup.state.preparedManagedVMCgroup)
	assert.True(t, cgroup.vmLeafPresent(testVMID))
	require.NoError(t, os.Remove(cgroup.vmLeafPath(testVMID)+"/cgroup.procs"))
	require.NoError(t, os.Remove(cgroup.vmLeafPath(testVMID)))
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
	assert.False(t, cgroup.vmLeafPresent(testVMID))
}

// Rationale: controller state can be removed after initial preparation. The irreversible Firecracker gate release
// must restore and read back root then parent readiness through the already-pinned cgroup authority.
func TestLaunchReleaseToJailerRechecksAndReenablesManagedVMCgroupControllers(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	cgroup := newManagedVMCgroupTestFixture(t)
	parentPath := createManagedVMCgroupControllerHierarchy(
		t,
		cgroup,
		"cpu memory pids\n",
		"cpu memory pids\n",
		"cpu memory pids\n",
		"cpu memory pids\n",
	)
	deps := cgroup.deps()
	rootOpens := 0
	realOpenRoot := deps.openRoot
	deps.openRoot = func(ctx context.Context) (int, error) {
		rootOpens++
		return realOpenRoot(ctx)
	}
	parentOpens := 0
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		if name == "mvmctl" {
			parentOpens++
		}
		return realOpenAt2(ctx, fd, name, how)
	}
	var writes []managedVMCgroupControllerWriteCall
	deps.write = simulatedManagedVMCgroupControllerWrite(t, cgroup.root, parentPath, &writes)
	authority.cgroupDeps = deps
	launch := readyTestLaunchForCgroupHandoff(t, authority, false)
	require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
	prepared := launch.state.preparedManagedVMCgroup
	require.NotNil(t, prepared)
	preparedRootOpens := rootOpens
	preparedParentOpens := parentOpens
	require.NoError(t, os.WriteFile(filepath.Join(cgroup.root, "cgroup.subtree_control"), []byte("cpu\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(parentPath, "cgroup.subtree_control"), []byte("memory\n"), 0o644))

	require.NoError(t, launch.releaseToJailer(t.Context()))
	assert.Same(t, prepared, launch.state.preparedManagedVMCgroup)
	assert.Equal(t, preparedRootOpens, rootOpens)
	assert.Equal(t, preparedParentOpens, parentOpens)
	wantWrites := []managedVMCgroupControllerWriteCall{
		{directory: "root", value: "+memory +pids\n"},
		{directory: "parent", value: "+cpu +pids\n"},
	}
	if diff := cmp.Diff(wantWrites, writes, cmp.AllowUnexported(managedVMCgroupControllerWriteCall{})); diff != "" {
		t.Errorf("final controller writes mismatch (-want +got):\n%s", diff)
	}
	assert.True(t, launch.state.processes.state.firecracker.gate.isReleased())
	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

// Rationale: a successful controller write without a successful readback cannot release Firecracker. The same pinned
// preparation must remain retryable, and convergence on retry must not repeat the additive write.
func TestLaunchFinalControllerReadbackFailureRetainsSameCgroupAuthorityForRetry(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	cgroup := newManagedVMCgroupTestFixture(t)
	parentPath := createManagedVMCgroupControllerHierarchy(
		t,
		cgroup,
		"cpu memory pids\n",
		"cpu memory pids\n",
		"cpu memory pids\n",
		"cpu memory pids\n",
	)
	authority.cgroupDeps = cgroup.deps()
	launch := readyTestLaunchForCgroupHandoff(t, authority, false)
	require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
	prepared := launch.state.preparedManagedVMCgroup
	require.NotNil(t, prepared)
	require.NoError(t, os.WriteFile(
		filepath.Join(parentPath, "cgroup.subtree_control"),
		[]byte("cpu memory\n"),
		0o644,
	))

	deps := prepared.state.deps
	readbackErr := errors.New("injected final parent subtree-control readback failure")
	parentWriteCompleted := false
	failedReadback := false
	realPread := deps.pread
	deps.pread = func(ctx context.Context, fd int, buffer []byte, offset int64) (int, error) {
		target, readlinkErr := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
		require.NoError(t, readlinkErr)
		if target == filepath.Join(parentPath, "cgroup.subtree_control") && parentWriteCompleted && !failedReadback {
			failedReadback = true
			return 0, readbackErr
		}
		return realPread(ctx, fd, buffer, offset)
	}
	var writes []managedVMCgroupControllerWriteCall
	controllerWrite := simulatedManagedVMCgroupControllerWrite(t, cgroup.root, parentPath, &writes)
	deps.write = func(ctx context.Context, fd int, value []byte) (int, error) {
		count, err := controllerWrite(ctx, fd, value)
		if err == nil {
			parentWriteCompleted = true
		}
		return count, err
	}
	prepared.state.deps = deps

	err := launch.releaseToJailer(t.Context())
	require.ErrorIs(t, err, readbackErr)
	assert.Same(t, prepared, launch.state.preparedManagedVMCgroup)
	assert.False(t, launch.state.processes.state.firecracker.gate.isReleased())
	require.NoError(t, launch.releaseToJailer(t.Context()))
	assert.Same(t, prepared, launch.state.preparedManagedVMCgroup)
	assert.True(t, launch.state.processes.state.firecracker.gate.isReleased())
	wantWrites := []managedVMCgroupControllerWriteCall{{directory: "parent", value: "+pids\n"}}
	if diff := cmp.Diff(wantWrites, writes, cmp.AllowUnexported(managedVMCgroupControllerWriteCall{})); diff != "" {
		t.Errorf("retry controller writes mismatch (-want +got):\n%s", diff)
	}
	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestLaunchFinalCgroupRecheckFailurePreservesRelayPartialHandoffForAbort(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	cgroup := newManagedVMCgroupTestFixture(t)
	authority.cgroupDeps = cgroup.launchDeps()
	launch := readyTestLaunchForCgroupHandoff(t, authority, true)
	require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
	processes := launch.state.processes
	require.NotNil(t, processes.state.relay)
	cgroup.addVMLeaf(testVMID)

	require.Error(t, launch.releaseToJailer(t.Context()))
	assert.True(t, processes.state.relay.gate.isReleased())
	assert.False(t, processes.state.firecracker.gate.isReleased())
	assert.Equal(t, blockedLaunchProcessesOpen, processes.state.status)
	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	require.Error(t, cleanup.Complete(t.Context()))
	assert.True(t, processes.state.relay.reaped)
	assert.True(t, processes.state.firecracker.reaped)
	require.NoError(t, os.Remove(cgroup.vmLeafPath(testVMID)+"/cgroup.procs"))
	require.NoError(t, os.Remove(cgroup.vmLeafPath(testVMID)))
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestAbortCleanupReapsBeforeReleasingAbsentManagedVMCgroup(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	cgroup := newManagedVMCgroupTestFixture(t)
	authority.cgroupDeps = cgroup.launchDeps()
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
	prepared := launch.state.preparedManagedVMCgroup
	reaped := false
	originalWait := launch.state.processes.state.deps.waitReapPIDFD
	launch.state.processes.state.deps.waitReapPIDFD = func(ctx context.Context, fd int) error {
		if err := originalWait(ctx, fd); err != nil {
			return err
		}
		reaped = true
		return nil
	}
	originalOpenAt2 := prepared.state.deps.openAt2
	prepared.state.deps.openAt2 = func(
		ctx context.Context,
		fd int,
		name string,
		how *unix.OpenHow,
	) (int, error) {
		if name == testVMID && !reaped {
			return -1, errors.New("managed VM cgroup inspected before process reap")
		}
		return originalOpenAt2(ctx, fd, name, how)
	}

	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	assert.True(t, launch.snapshot().released)
	assert.False(t, launch.snapshot().hasPreparedManagedVMCgroup)
	assert.Same(t, prepared, cleanup.state.preparedManagedVMCgroup)
	require.Error(t, cleanup.Release(t.Context()))
	assert.Same(t, prepared, cleanup.state.preparedManagedVMCgroup)

	require.NoError(t, cleanup.Complete(t.Context()))
	assert.True(t, reaped)
	assert.True(t, prepared.snapshot().released)
	assert.Nil(t, cleanup.state.preparedManagedVMCgroup)
	assert.Nil(t, cleanup.state.managedVMCgroup)
	assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestAbortCleanupReapsBeforeRemovingJailerManagedVMCgroup(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	cgroup := newManagedVMCgroupTestFixture(t)
	cgroupDeps := cgroup.launchDeps()
	events := make([]string, 0, 3)
	reaped := false
	originalUnlinkAt := cgroupDeps.unlinkAt
	cgroupDeps.unlinkAt = func(ctx context.Context, fd int, name string, flags int) error {
		if !reaped {
			return errors.New("managed VM cgroup removed before process reap")
		}
		events = append(events, "cgroup-remove")
		return originalUnlinkAt(ctx, fd, name, flags)
	}
	authority.cgroupDeps = cgroupDeps
	launch := readyTestLaunchForCgroupHandoff(t, authority, false)
	require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
	require.NoError(t, launch.releaseToJailer(t.Context()))
	cgroup.addVMLeaf(testVMID)
	originalWait := launch.state.processes.state.deps.waitReapPIDFD
	launch.state.processes.state.deps.waitReapPIDFD = func(ctx context.Context, fd int) error {
		if err := originalWait(ctx, fd); err != nil {
			return err
		}
		reaped = true
		cgroup.setMembership(testVMID, "")
		events = append(events, "reap")
		return nil
	}

	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	originalRenameAt := cleanup.state.directories.deps.renameAt
	cleanup.state.directories.deps.renameAt = func(
		ctx context.Context,
		oldFD int,
		oldName string,
		newFD int,
		newName string,
	) error {
		if cgroup.vmLeafPresent(testVMID) {
			return errors.New("durable cleaned record written before managed VM cgroup removal")
		}
		events = append(events, "durable-cleaned")
		return originalRenameAt(ctx, oldFD, oldName, newFD, newName)
	}

	require.NoError(t, cleanup.Complete(t.Context()))
	assert.Equal(t, []string{"reap", "cgroup-remove", "durable-cleaned"}, events)
	assert.False(t, cgroup.vmLeafPresent(testVMID))
	assert.Nil(t, cleanup.state.preparedManagedVMCgroup)
	assert.Nil(t, cleanup.state.managedVMCgroup)
	assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestAbortCleanupManagedVMCgroupRemovalFailureRetainsAuthorityForRetry(t *testing.T) {
	t.Parallel()

	t.Run("nonempty", func(t *testing.T) {
		authority, _ := newTestInstanceAuthority(t)
		cgroup := newManagedVMCgroupTestFixture(t)
		authority.cgroupDeps = cgroup.launchDeps()
		launch := readyTestLaunchForCgroupHandoff(t, authority, false)
		require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
		require.NoError(t, launch.releaseToJailer(t.Context()))
		cgroup.addVMLeaf(testVMID)
		cleanup, err := launch.Abort(t.Context())
		require.NoError(t, err)

		require.Error(t, cleanup.Complete(t.Context()))
		bound := cleanup.state.managedVMCgroup
		require.NotNil(t, bound)
		assert.Nil(t, cleanup.state.preparedManagedVMCgroup)
		assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
		assert.True(t, cgroup.vmLeafPresent(testVMID))
		require.Error(t, cleanup.Release(t.Context()))
		assert.Same(t, bound, cleanup.state.managedVMCgroup)

		cgroup.setMembership(testVMID, "")
		require.NoError(t, cleanup.Complete(t.Context()))
		assert.True(t, bound.snapshot().released)
		assert.Nil(t, cleanup.state.managedVMCgroup)
		assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
		require.NoError(t, cleanup.Release(t.Context()))
	})

	t.Run("busy", func(t *testing.T) {
		authority, _ := newTestInstanceAuthority(t)
		cgroup := newManagedVMCgroupTestFixture(t)
		cgroupDeps := cgroup.launchDeps()
		unlinkCalls := 0
		originalUnlinkAt := cgroupDeps.unlinkAt
		cgroupDeps.unlinkAt = func(ctx context.Context, fd int, name string, flags int) error {
			unlinkCalls++
			if unlinkCalls == 1 {
				return unix.EBUSY
			}
			return originalUnlinkAt(ctx, fd, name, flags)
		}
		authority.cgroupDeps = cgroupDeps
		launch := readyTestLaunchForCgroupHandoff(t, authority, false)
		require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
		require.NoError(t, launch.releaseToJailer(t.Context()))
		cgroup.addVMLeaf(testVMID)
		originalWait := launch.state.processes.state.deps.waitReapPIDFD
		launch.state.processes.state.deps.waitReapPIDFD = func(ctx context.Context, fd int) error {
			if err := originalWait(ctx, fd); err != nil {
				return err
			}
			cgroup.setMembership(testVMID, "")
			return nil
		}
		cleanup, err := launch.Abort(t.Context())
		require.NoError(t, err)

		first := cleanup.Complete(t.Context())
		require.Error(t, first)
		assert.ErrorIs(t, first, unix.EBUSY)
		bound := cleanup.state.managedVMCgroup
		require.NotNil(t, bound)
		assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
		assert.True(t, cgroup.vmLeafPresent(testVMID))

		require.NoError(t, cleanup.Complete(t.Context()))
		assert.Equal(t, 2, unlinkCalls)
		assert.True(t, bound.snapshot().released)
		assert.Nil(t, cleanup.state.managedVMCgroup)
		assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
		require.NoError(t, cleanup.Release(t.Context()))
	})
}

func TestAbortCleanupManagedVMCgroupCloseFailureIsReportedOnceAndRetryConverges(t *testing.T) {
	t.Parallel()

	t.Run("absent prepared", func(t *testing.T) {
		authority, _ := newTestInstanceAuthority(t)
		cgroup := newManagedVMCgroupTestFixture(t)
		authority.cgroupDeps = cgroup.launchDeps()
		launch, err := registerTestLaunch(
			t,
			t.Context(),
			authority,
			testInstanceCaller(),
			testLaunchIntent(false),
		)
		require.NoError(t, err)
		require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
		prepared := launch.state.preparedManagedVMCgroup
		rootFD := prepared.state.rootFD
		closeErr := errors.New("close absent prepared managed VM cgroup")
		realClose := prepared.state.deps.close
		prepared.state.deps.close = func(ctx context.Context, fd int) error {
			err := realClose(ctx, fd)
			if fd == rootFD {
				return errors.Join(err, closeErr)
			}
			return err
		}
		cleanup, err := launch.Abort(t.Context())
		require.NoError(t, err)

		first := cleanup.Complete(t.Context())
		require.ErrorIs(t, first, closeErr)
		assert.True(t, prepared.snapshot().released)
		assert.Nil(t, cleanup.state.preparedManagedVMCgroup)
		assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
		require.NoError(t, cleanup.Complete(t.Context()))
		assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
		require.NoError(t, cleanup.Release(t.Context()))
	})

	t.Run("removed lease", func(t *testing.T) {
		authority, _ := newTestInstanceAuthority(t)
		cgroup := newManagedVMCgroupTestFixture(t)
		authority.cgroupDeps = cgroup.launchDeps()
		launch := readyTestLaunchForCgroupHandoff(t, authority, false)
		require.NoError(t, launch.prepareManagedVMCgroup(t.Context(), testManagedVMCgroupLimits()))
		require.NoError(t, launch.releaseToJailer(t.Context()))
		prepared := launch.state.preparedManagedVMCgroup
		rootFD := prepared.state.rootFD
		closeErr := errors.New("close removed managed VM cgroup")
		realClose := prepared.state.deps.close
		prepared.state.deps.close = func(ctx context.Context, fd int) error {
			err := realClose(ctx, fd)
			if fd == rootFD {
				return errors.Join(err, closeErr)
			}
			return err
		}
		cgroup.addVMLeaf(testVMID)
		cgroup.setMembership(testVMID, "")
		cleanup, err := launch.Abort(t.Context())
		require.NoError(t, err)

		first := cleanup.Complete(t.Context())
		require.ErrorIs(t, first, closeErr)
		assert.Nil(t, cleanup.state.preparedManagedVMCgroup)
		assert.Nil(t, cleanup.state.managedVMCgroup)
		assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
		assert.False(t, cgroup.vmLeafPresent(testVMID))
		require.NoError(t, cleanup.Complete(t.Context()))
		assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
		require.NoError(t, cleanup.Release(t.Context()))
	})
}
