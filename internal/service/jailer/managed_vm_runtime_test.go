package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestCreateManagedVMRuntimeCreatesExactPinnedTree(t *testing.T) {
	t.Parallel()

	root := prepareManagedVMRuntimeTestRoot(t)
	caller := managedVMRuntimeTestCaller()
	lease, err := createManagedVMRuntime(
		t.Context(),
		realManagedVMRuntimeDeps(),
		managedVMRuntimeTestPolicy(root),
		caller,
		testVMID,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, lease.Remove(context.Background()))
		require.NoError(t, lease.Release(context.Background()))
	})

	paths := []struct {
		path string
		mode os.FileMode
		uid  uint32
		gid  uint32
	}{
		{path: "run/mvmctl", mode: 0o711, uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())},
		{path: "run/mvmctl/runtime", mode: 0o711, uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())},
		{path: "run/mvmctl/runtime/" + managedVMRuntimeUIDName(caller.uid), mode: 0o711,
			uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())},
		{path: "run/mvmctl/runtime/" + managedVMRuntimeUIDName(caller.uid) + "/" + testVMID,
			mode: 0o700, uid: caller.uid, gid: caller.gid},
	}
	for _, want := range paths {
		info, statErr := os.Lstat(filepath.Join(root, want.path))
		require.NoError(t, statErr)
		assert.True(t, info.IsDir())
		assert.Equal(t, want.mode, info.Mode().Perm())
		stat, ok := info.Sys().(*syscall.Stat_t)
		require.True(t, ok)
		assert.Equal(t, want.uid, stat.Uid)
		assert.Equal(t, want.gid, stat.Gid)
	}
	assert.Equal(t, caller, lease.state.caller)
	assert.Equal(t, testVMID, lease.state.vmID)
	assert.NotZero(t, lease.state.identity.device)
	assert.NotZero(t, lease.state.identity.inode)
}

// Rationale: a surviving runtime directory represents unreconciled lifecycle state. Reusing it would let stale
// sockets or caller-created leaves enter a new launch.
func TestCreateManagedVMRuntimeRejectsExistingVMDirectoryWithoutRepair(t *testing.T) {
	t.Parallel()

	root := prepareManagedVMRuntimeTestRoot(t)
	caller := managedVMRuntimeTestCaller()
	first, err := createManagedVMRuntime(
		t.Context(),
		realManagedVMRuntimeDeps(),
		managedVMRuntimeTestPolicy(root),
		caller,
		testVMID,
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, first.Remove(context.Background()))
		require.NoError(t, first.Release(context.Background()))
	})

	second, err := createManagedVMRuntime(
		t.Context(),
		realManagedVMRuntimeDeps(),
		managedVMRuntimeTestPolicy(root),
		caller,
		testVMID,
	)
	assert.Nil(t, second)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(err).Code)

	info, statErr := os.Lstat(filepath.Join(
		root,
		"run/mvmctl/runtime",
		managedVMRuntimeUIDName(caller.uid),
		testVMID,
	))
	require.NoError(t, statErr)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// Rationale: cleanup must never recursively remove caller-controlled content from the runtime output directory.
func TestManagedVMRuntimeRemoveRefusesNonemptyDirectory(t *testing.T) {
	t.Parallel()

	root := prepareManagedVMRuntimeTestRoot(t)
	caller := managedVMRuntimeTestCaller()
	lease, err := createManagedVMRuntime(
		t.Context(),
		realManagedVMRuntimeDeps(),
		managedVMRuntimeTestPolicy(root),
		caller,
		testVMID,
	)
	require.NoError(t, err)
	vmPath := filepath.Join(root, "run/mvmctl/runtime", managedVMRuntimeUIDName(caller.uid), testVMID)
	require.NoError(t, os.WriteFile(filepath.Join(vmPath, "unexpected"), []byte("retain"), 0o600))

	err = lease.Remove(t.Context())
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	assert.FileExists(t, filepath.Join(vmPath, "unexpected"))
	require.Error(t, lease.Release(t.Context()))

	require.NoError(t, os.Remove(filepath.Join(vmPath, "unexpected")))
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
	assert.NoDirExists(t, vmPath)
}

// Rationale: an unlink may commit in the kernel even when its wrapper reports an error. Cleanup must reconcile the
// missing exact entry and retain only the pending durability step for retry.
func TestManagedVMRuntimeRemoveReconcilesEffectfulUnlinkError(t *testing.T) {
	t.Parallel()

	root := prepareManagedVMRuntimeTestRoot(t)
	caller := managedVMRuntimeTestCaller()
	deps := realManagedVMRuntimeDeps()
	realUnlinkAt := deps.unlinkAt
	unlinks := 0
	effectErr := errors.New("unlink result unavailable")
	deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		if name != testVMID || flags != unix.AT_REMOVEDIR {
			return realUnlinkAt(ctx, parentFD, name, flags)
		}
		unlinks++
		if unlinks == 1 {
			require.NoError(t, realUnlinkAt(ctx, parentFD, name, flags))
			return effectErr
		}
		return realUnlinkAt(ctx, parentFD, name, flags)
	}
	lease, err := createManagedVMRuntime(
		t.Context(),
		deps,
		managedVMRuntimeTestPolicy(root),
		caller,
		testVMID,
	)
	require.NoError(t, err)

	err = lease.Remove(t.Context())
	require.ErrorIs(t, err, effectErr)
	assert.NoDirExists(t, filepath.Join(
		root,
		"run/mvmctl/runtime",
		managedVMRuntimeUIDName(caller.uid),
		testVMID,
	))
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
	assert.Equal(t, 1, unlinks)
}

// Rationale: once the directory is absent, a parent-sync failure must retry only durability and never attempt to
// reopen or remove the already-absent name.
func TestManagedVMRuntimeRemoveRetriesParentSyncAfterRemoval(t *testing.T) {
	t.Parallel()

	root := prepareManagedVMRuntimeTestRoot(t)
	caller := managedVMRuntimeTestCaller()
	deps := realManagedVMRuntimeDeps()
	realOpenAt2 := deps.openAt2
	realFsync := deps.fsync
	uidFD := -1
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil && name == managedVMRuntimeUIDName(caller.uid) {
			uidFD = fd
		}
		return fd, err
	}
	failSync := false
	syncErr := errors.New("runtime parent sync failed")
	deps.fsync = func(ctx context.Context, fd int) error {
		if failSync && fd == uidFD {
			failSync = false
			return syncErr
		}
		return realFsync(ctx, fd)
	}
	lease, err := createManagedVMRuntime(
		t.Context(),
		deps,
		managedVMRuntimeTestPolicy(root),
		caller,
		testVMID,
	)
	require.NoError(t, err)
	failSync = true

	err = lease.Remove(t.Context())
	require.ErrorIs(t, err, syncErr)
	assert.NoDirExists(t, filepath.Join(
		root,
		"run/mvmctl/runtime",
		managedVMRuntimeUIDName(caller.uid),
		testVMID,
	))
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

// Rationale: once mkdir succeeds, a later metadata failure must return cleanup authority rather than leak an
// untracked runtime directory that permanently blocks relaunch.
func TestCreateManagedVMRuntimeReturnsCleanupAuthorityAfterPartialCreation(t *testing.T) {
	t.Parallel()

	root := prepareManagedVMRuntimeTestRoot(t)
	caller := managedVMRuntimeTestCaller()
	deps := realManagedVMRuntimeDeps()
	realOpenAt2 := deps.openAt2
	realFchmod := deps.fchmod
	vmFD := -1
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil && name == testVMID {
			vmFD = fd
		}
		return fd, err
	}
	metadataErr := errors.New("runtime mode failed")
	deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
		if fd == vmFD {
			return metadataErr
		}
		return realFchmod(ctx, fd, mode)
	}

	lease, err := createManagedVMRuntime(
		t.Context(),
		deps,
		managedVMRuntimeTestPolicy(root),
		caller,
		testVMID,
	)
	require.ErrorIs(t, err, metadataErr)
	require.NotNil(t, lease)
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
	assert.NoDirExists(t, filepath.Join(
		root,
		"run/mvmctl/runtime",
		managedVMRuntimeUIDName(caller.uid),
		testVMID,
	))
}

func TestCreateManagedVMRuntimeRejectsCancelledContextBeforeEffects(t *testing.T) {
	t.Parallel()

	root := prepareManagedVMRuntimeTestRoot(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	lease, err := createManagedVMRuntime(
		ctx,
		realManagedVMRuntimeDeps(),
		managedVMRuntimeTestPolicy(root),
		managedVMRuntimeTestCaller(),
		testVMID,
	)
	assert.Nil(t, lease)
	require.ErrorIs(t, err, context.Canceled)
	assert.NoDirExists(t, filepath.Join(root, "run/mvmctl"))
}

// Rationale: root-controlled ancestors are authority for the runtime namespace. Existing unsafe objects must be
// rejected without chmod, chown, replacement, or symlink traversal.
func TestCreateManagedVMRuntimeRejectsUnsafeParentWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		prepare func(*testing.T, string)
		verify  func(*testing.T, string)
	}{
		"symlink": {
			prepare: func(t *testing.T, root string) {
				require.NoError(t, os.Mkdir(filepath.Join(root, "attacker"), 0o711))
				require.NoError(t, os.Symlink("../../attacker", filepath.Join(root, "run/mvmctl")))
			},
			verify: func(t *testing.T, root string) {
				info, err := os.Lstat(filepath.Join(root, "run/mvmctl"))
				require.NoError(t, err)
				assert.NotZero(t, info.Mode()&os.ModeSymlink)
			},
		},
		"wrong mode": {
			prepare: func(t *testing.T, root string) {
				require.NoError(t, os.Mkdir(filepath.Join(root, "run/mvmctl"), 0o700))
			},
			verify: func(t *testing.T, root string) {
				info, err := os.Lstat(filepath.Join(root, "run/mvmctl"))
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
			},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareManagedVMRuntimeTestRoot(t)
			test.prepare(t, root)
			lease, err := createManagedVMRuntime(
				t.Context(),
				realManagedVMRuntimeDeps(),
				managedVMRuntimeTestPolicy(root),
				managedVMRuntimeTestCaller(),
				testVMID,
			)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
			test.verify(t, root)
		})
	}
}

// Rationale: lifecycle capabilities are copied while they transfer between launch phases. All copies must share one
// removal and close result so descriptor reuse cannot turn a late cleanup into an unrelated filesystem operation.
func TestManagedVMRuntimeCopiesShareConcurrentCleanupState(t *testing.T) {
	t.Parallel()

	root := prepareManagedVMRuntimeTestRoot(t)
	deps := realManagedVMRuntimeDeps()
	realUnlinkAt := deps.unlinkAt
	realClose := deps.close
	var removes atomic.Int32
	var closes atomic.Int32
	deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		if name == testVMID && flags == unix.AT_REMOVEDIR {
			removes.Add(1)
		}
		return realUnlinkAt(ctx, parentFD, name, flags)
	}
	deps.close = func(ctx context.Context, fd int) error {
		closes.Add(1)
		return realClose(ctx, fd)
	}
	lease, err := createManagedVMRuntime(
		t.Context(),
		deps,
		managedVMRuntimeTestPolicy(root),
		managedVMRuntimeTestCaller(),
		testVMID,
	)
	require.NoError(t, err)
	copyOfLease := *lease
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	removeErrors := make(chan error, 2)
	var removeGroup sync.WaitGroup
	for _, candidate := range []*managedVMRuntimeLease{lease, &copyOfLease} {
		removeGroup.Add(1)
		go func(value *managedVMRuntimeLease) {
			defer removeGroup.Done()
			removeErrors <- value.Remove(cancelled)
		}(candidate)
	}
	removeGroup.Wait()
	close(removeErrors)
	for removeErr := range removeErrors {
		require.NoError(t, removeErr)
	}
	assert.Equal(t, int32(1), removes.Load())
	closes.Store(0)

	closeErrors := make(chan error, 2)
	var closeGroup sync.WaitGroup
	for _, candidate := range []*managedVMRuntimeLease{lease, &copyOfLease} {
		closeGroup.Add(1)
		go func(value *managedVMRuntimeLease) {
			defer closeGroup.Done()
			closeErrors <- value.Release(cancelled)
		}(candidate)
	}
	closeGroup.Wait()
	close(closeErrors)
	for closeErr := range closeErrors {
		require.NoError(t, closeErr)
	}
	assert.Equal(t, int32(6), closes.Load())
}

func prepareManagedVMRuntimeTestRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "run"), 0o755))
	return root
}

func managedVMRuntimeTestPolicy(root string) managedVMRuntimePolicy {
	return managedVMRuntimePolicy{
		rootPath:   root,
		trustedUID: uint32(os.Geteuid()),
		trustedGID: uint32(os.Getegid()),
	}
}

func managedVMRuntimeTestCaller() instanceCaller {
	if os.Geteuid() == 0 {
		return testInstanceCaller()
	}
	return instanceCaller{uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())}
}
