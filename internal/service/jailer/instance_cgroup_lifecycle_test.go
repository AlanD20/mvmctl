package jailer

import (
	"context"
	"errors"
	"testing"

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

func readyTestLaunchForCgroupHandoff(t *testing.T, authority *instanceAuthority) *launchLease {
	t.Helper()
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
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
	authority.cgroupDeps = cgroup.deps()
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)

	require.NoError(t, launch.prepareManagedVMCgroup(t.Context()))
	assert.True(t, launch.snapshot().hasPreparedManagedVMCgroup)
	require.Error(t, launch.prepareManagedVMCgroup(t.Context()))

	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestLaunchReleaseToJailerRequiresPreparedManagedVMCgroup(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	launch := readyTestLaunchForCgroupHandoff(t, authority)

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
	authority.cgroupDeps = cgroup.deps()
	launch := readyTestLaunchForCgroupHandoff(t, authority)
	require.NoError(t, launch.prepareManagedVMCgroup(t.Context()))
	cgroup.addVMLeaf(testVMID)

	require.Error(t, launch.releaseToJailer(t.Context()))
	assert.Equal(t, blockedLaunchProcessesOpen, launch.state.processes.state.status)
	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	cgroup.setMembership(testVMID, "")
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
	assert.False(t, cgroup.vmLeafPresent(testVMID))
}

func TestAbortCleanupReapsBeforeReleasingAbsentManagedVMCgroup(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	cgroup := newManagedVMCgroupTestFixture(t)
	authority.cgroupDeps = cgroup.deps()
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	require.NoError(t, launch.prepareManagedVMCgroup(t.Context()))
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
	cgroupDeps := cgroup.deps()
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
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	require.NoError(t, launch.prepareManagedVMCgroup(t.Context()))
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
		authority.cgroupDeps = cgroup.deps()
		launch, err := registerTestLaunch(
			t,
			t.Context(),
			authority,
			testInstanceCaller(),
			testLaunchIntent(false),
		)
		require.NoError(t, err)
		require.NoError(t, launch.prepareManagedVMCgroup(t.Context()))
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
		cgroupDeps := cgroup.deps()
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
		launch, err := registerTestLaunch(
			t,
			t.Context(),
			authority,
			testInstanceCaller(),
			testLaunchIntent(false),
		)
		require.NoError(t, err)
		require.NoError(t, launch.prepareManagedVMCgroup(t.Context()))
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
