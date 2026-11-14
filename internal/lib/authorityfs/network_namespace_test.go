package authorityfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestNetworkNamespaceHandleCreatesExactPlaceholder(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority := openTestNetwork(t, root)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)

	handle, err := uid.CreateNamespaceHandle(t.Context(), key)
	require.NoError(t, err)
	ownerUID, boundKey, err := handle.Binding()
	require.NoError(t, err)
	assert.Equal(t, uint32(testUID), ownerUID)
	assert.Equal(t, key, boundKey)
	target, err := handle.TargetIdentity(t.Context())
	require.NoError(t, err)

	directoryPath := filepath.Join(root, "run/mvmctl/authority/1000/netns")
	placeholderPath := filepath.Join(directoryPath, testVMID)
	assertExactMode(t, directoryPath, 0o700)
	assertExactMode(t, placeholderPath, 0o600)
	assertTestOwner(t, directoryPath)
	assertTestOwner(t, placeholderPath)

	var directoryStat unix.Stat_t
	require.NoError(t, unix.Lstat(directoryPath, &directoryStat))
	var placeholderStat unix.Stat_t
	require.NoError(t, unix.Lstat(placeholderPath, &placeholderStat))
	assert.Equal(t, uint32(unix.S_IFDIR), directoryStat.Mode&unix.S_IFMT)
	assert.Equal(t, uint32(unix.S_IFREG), placeholderStat.Mode&unix.S_IFMT)
	assert.Equal(t, uint64(1), placeholderStat.Nlink)
	assert.Zero(t, placeholderStat.Size)
	want := NetworkNamespaceTargetIdentity{
		DirectoryDevice:   directoryStat.Dev,
		DirectoryInode:    directoryStat.Ino,
		DirectoryMount:    testNamespaceMountIdentity(t, directoryPath),
		PlaceholderDevice: placeholderStat.Dev,
		PlaceholderInode:  placeholderStat.Ino,
		PlaceholderMount:  testNamespaceMountIdentity(t, placeholderPath),
	}
	if diff := cmp.Diff(want, target); diff != "" {
		t.Errorf("namespace target identity mismatch (-want +got):\n%s", diff)
	}

	require.NoError(t, handle.Remove(t.Context()))
	_, err = os.Lstat(placeholderPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, handle.Release(t.Context()))
	_, _, err = handle.Binding()
	require.ErrorContains(t, err, "not active")
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))
}

// Rationale: every post-create fault must return the sole capability able to remove the mutated leaf.
func TestNetworkNamespaceHandleReturnsCleanupAuthorityAfterMutation(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*dependencies){
		"set owner": func(deps *dependencies) {
			original := deps.fchown
			var once sync.Once
			deps.fchown = func(ctx context.Context, fd, uid, gid int) error {
				var injected error
				once.Do(func() { injected = errors.New("owner failed") })
				if injected != nil {
					return injected
				}
				return original(ctx, fd, uid, gid)
			}
		},
		"set mode": func(deps *dependencies) {
			original := deps.fchmod
			var once sync.Once
			deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
				var injected error
				once.Do(func() { injected = errors.New("mode failed") })
				if injected != nil {
					return injected
				}
				return original(ctx, fd, mode)
			}
		},
		"inspect leaf": func(deps *dependencies) {
			original := deps.fstat
			var once sync.Once
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := original(ctx, fd, stat); err != nil {
					return err
				}
				if stat.Mode&unix.S_IFMT == unix.S_IFREG {
					var injected error
					once.Do(func() { injected = errors.New("inspect failed") })
					return injected
				}
				return nil
			}
		},
		"sync leaf": func(deps *dependencies) {
			original := deps.fsync
			var once sync.Once
			deps.fsync = func(ctx context.Context, fd int) error {
				var injected error
				once.Do(func() { injected = errors.New("sync failed") })
				if injected != nil {
					return injected
				}
				return original(ctx, fd)
			}
		},
		"sync directory": func(deps *dependencies) {
			original := deps.fsync
			var calls int
			deps.fsync = func(ctx context.Context, fd int) error {
				calls++
				if calls == 2 {
					return errors.New("directory sync failed")
				}
				return original(ctx, fd)
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareNamespaceAuthorityTree(t)
			deps := realDependencies()
			inject(&deps)
			authority, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
			require.NoError(t, err)
			uid, err := authority.OpenUID(t.Context(), testUID)
			require.NoError(t, err)
			key, err := NewVMKey(testVMID)
			require.NoError(t, err)

			handle, err := uid.CreateNamespaceHandle(t.Context(), key)
			require.Error(t, err)
			require.NotNil(t, handle)
			_, identityErr := handle.TargetIdentity(t.Context())
			require.ErrorContains(t, identityErr, "unavailable")
			_, _, bindingErr := handle.Binding()
			require.ErrorContains(t, bindingErr, "not active")
			require.NoError(t, handle.Remove(t.Context()))
			require.NoError(t, handle.Release(t.Context()))
			require.NoError(t, uid.Release(t.Context()))
			require.NoError(t, authority.Release(t.Context()))
			_, statErr := os.Lstat(filepath.Join(root, "run/mvmctl/authority/1000/netns", testVMID))
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func TestNetworkNamespaceHandleRejectsUnsafeExistingObjects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		prepare func(*testing.T, string)
		want    string
	}{
		"symlink directory": {
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.Symlink("elsewhere", path))
			},
			want: "open managed directory",
		},
		"wrong directory mode": {
			prepare: func(t *testing.T, path string) { require.NoError(t, os.Mkdir(path, 0o755)) },
			want:    "unexpected owner or mode",
		},
		"regular file directory": {
			prepare: func(t *testing.T, path string) { require.NoError(t, os.WriteFile(path, nil, 0o700)) },
			want:    "open managed directory",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareTestRoot(t)
			authority := openTestNetwork(t, root)
			uid, err := authority.OpenUID(t.Context(), testUID)
			require.NoError(t, err)
			directoryPath := filepath.Join(root, "run/mvmctl/authority/1000/netns")
			test.prepare(t, directoryPath)
			key, err := NewVMKey(testVMID)
			require.NoError(t, err)

			handle, err := uid.CreateNamespaceHandle(t.Context(), key)
			assert.Nil(t, handle)
			require.ErrorContains(t, err, test.want)
			require.NoError(t, uid.Release(t.Context()))
			require.NoError(t, authority.Release(t.Context()))
		})
	}
}

// Rationale: exclusive creation must leave every pre-existing reserved VM leaf untouched and return no capability.
func TestNetworkNamespaceHandleDoesNotAdoptExistingLeaf(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"plausible empty regular file": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, nil, 0o600))
		},
		"regular file": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte("foreign"), 0o600))
		},
		"symlink": func(t *testing.T, path string) {
			require.NoError(t, os.Symlink("foreign", path))
		},
		"hard link": func(t *testing.T, path string) {
			source := path + ".source"
			require.NoError(t, os.WriteFile(source, nil, 0o600))
			require.NoError(t, os.Link(source, path))
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareNamespaceAuthorityTree(t)
			authority := openTestNetwork(t, root)
			uid, err := authority.OpenUID(t.Context(), testUID)
			require.NoError(t, err)
			path := filepath.Join(root, "run/mvmctl/authority/1000/netns", testVMID)
			prepare(t, path)
			before, err := os.Lstat(path)
			require.NoError(t, err)
			key, err := NewVMKey(testVMID)
			require.NoError(t, err)

			handle, err := uid.CreateNamespaceHandle(t.Context(), key)
			assert.Nil(t, handle)
			require.ErrorIs(t, err, unix.EEXIST)
			after, err := os.Lstat(path)
			require.NoError(t, err)
			assert.Equal(t, before.Mode(), after.Mode())
			assert.Equal(t, before.Size(), after.Size())
			require.NoError(t, uid.Release(t.Context()))
			require.NoError(t, authority.Release(t.Context()))
		})
	}
}

// Rationale: mutable names can be replaced after creation, so cleanup must compare the retained and current inode.
func TestNetworkNamespaceHandleRejectsReplacement(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority := openTestNetwork(t, root)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	handle, err := uid.CreateNamespaceHandle(t.Context(), key)
	require.NoError(t, err)

	placeholderPath := filepath.Join(root, "run/mvmctl/authority/1000/netns", testVMID)
	retainedPath := placeholderPath + ".retained"
	require.NoError(t, os.Rename(placeholderPath, retainedPath))
	replacement, err := os.OpenFile(placeholderPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, replacement.Close())
	var replacementStat unix.Stat_t
	require.NoError(t, unix.Lstat(placeholderPath, &replacementStat))
	require.ErrorContains(t, handle.Remove(t.Context()), "identity changed")
	var remainingStat unix.Stat_t
	require.NoError(t, unix.Lstat(placeholderPath, &remainingStat))
	assert.Equal(t, replacementStat.Ino, remainingStat.Ino)

	require.NoError(t, os.Remove(placeholderPath))
	require.NoError(t, os.Rename(retainedPath, placeholderPath))
	require.NoError(t, handle.Remove(t.Context()))
	require.NoError(t, handle.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))
}

// Rationale: retained descriptors prove identity only while their exact owner, mode, and file type remain admitted.
func TestNetworkNamespaceHandleTargetIdentityRechecksMetadata(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate  func(*testing.T, string)
		restore func(*testing.T, string)
	}{
		"directory mode": {
			mutate:  func(t *testing.T, path string) { require.NoError(t, os.Chmod(filepath.Dir(path), 0o755)) },
			restore: func(t *testing.T, path string) { require.NoError(t, os.Chmod(filepath.Dir(path), 0o700)) },
		},
		"placeholder mode": {
			mutate:  func(t *testing.T, path string) { require.NoError(t, os.Chmod(path, 0o640)) },
			restore: func(t *testing.T, path string) { require.NoError(t, os.Chmod(path, 0o600)) },
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareTestRoot(t)
			authority := openTestNetwork(t, root)
			uid, err := authority.OpenUID(t.Context(), testUID)
			require.NoError(t, err)
			key, err := NewVMKey(testVMID)
			require.NoError(t, err)
			handle, err := uid.CreateNamespaceHandle(t.Context(), key)
			require.NoError(t, err)
			placeholderPath := filepath.Join(root, "run/mvmctl/authority/1000/netns", testVMID)
			test.mutate(t, placeholderPath)

			_, err = handle.TargetIdentity(t.Context())
			require.Error(t, err)

			test.restore(t, placeholderPath)
			require.NoError(t, handle.Remove(t.Context()))
			require.NoError(t, handle.Release(t.Context()))
			require.NoError(t, uid.Release(t.Context()))
			require.NoError(t, authority.Release(t.Context()))
		})
	}
}

// Rationale: cancellation after leaf ownership changes must stop further preparation and return cleanup authority.
func TestNetworkNamespaceHandleCancellationAfterMutationReturnsCapability(t *testing.T) {
	t.Parallel()

	root := prepareNamespaceAuthorityTree(t)
	deps := realDependencies()
	originalChown := deps.fchown
	ctx, cancel := context.WithCancel(t.Context())
	deps.fchown = func(effectCtx context.Context, fd, uid, gid int) error {
		err := originalChown(effectCtx, fd, uid, gid)
		cancel()
		return err
	}
	authority, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)

	handle, err := uid.CreateNamespaceHandle(ctx, key)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, handle)
	_, identityErr := handle.TargetIdentity(t.Context())
	require.ErrorContains(t, identityErr, "unavailable")
	_, _, bindingErr := handle.Binding()
	require.ErrorContains(t, bindingErr, "not active")
	require.NoError(t, handle.Remove(ctx))
	require.NoError(t, handle.Release(ctx))
	require.NoError(t, uid.Release(ctx))
	require.NoError(t, authority.Release(ctx))
}

// Rationale: cancellation after both durability effects still yields cleanup-only authority until admission commits.
func TestNetworkNamespaceHandleCancellationAfterDirectorySyncDeniesTargetAuthority(t *testing.T) {
	t.Parallel()

	root := prepareNamespaceAuthorityTree(t)
	deps := realDependencies()
	originalFsync := deps.fsync
	ctx, cancel := context.WithCancel(t.Context())
	var fsyncCalls int
	deps.fsync = func(effectCtx context.Context, fd int) error {
		if err := originalFsync(effectCtx, fd); err != nil {
			return err
		}
		fsyncCalls++
		if fsyncCalls == 2 {
			cancel()
		}
		return nil
	}
	authority, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)

	handle, err := uid.CreateNamespaceHandle(ctx, key)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, handle)
	_, identityErr := handle.TargetIdentity(t.Context())
	require.ErrorContains(t, identityErr, "unavailable")
	_, _, bindingErr := handle.Binding()
	require.ErrorContains(t, bindingErr, "not active")
	require.NoError(t, handle.Remove(ctx))
	require.NoError(t, handle.Release(ctx))
	require.NoError(t, uid.Release(ctx))
	require.NoError(t, authority.Release(ctx))
}

// Rationale: copied capabilities share one descriptor lifecycle even when cleanup callers race under cancellation.
func TestNetworkNamespaceHandleCopiedConcurrentReleaseIsCloseOnce(t *testing.T) {
	t.Parallel()

	root := prepareNamespaceAuthorityTree(t)
	deps := realDependencies()
	closeCounts := make(map[int]int)
	var closeMu sync.Mutex
	originalClose := deps.close
	failedFD := -1
	deps.close = func(ctx context.Context, fd int) error {
		assert.NoError(t, ctx.Err())
		closeMu.Lock()
		closeCounts[fd]++
		closeMu.Unlock()
		err := originalClose(ctx, fd)
		if fd == failedFD {
			return errors.Join(err, unix.EIO)
		}
		return err
	}
	authority, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	handle, err := uid.CreateNamespaceHandle(t.Context(), key)
	require.NoError(t, err)
	copyOfHandle := *handle
	failedFD = handle.state.placeholderFD
	directoryFD := handle.state.directoryFD
	require.NoError(t, handle.Remove(t.Context()))

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	results := make(chan error, 2)
	go func() { results <- handle.Release(cancelled) }()
	go func() { results <- copyOfHandle.Release(cancelled) }()
	first := <-results
	second := <-results
	require.ErrorIs(t, first, unix.EIO)
	require.ErrorIs(t, second, unix.EIO)
	handle.state.mu.Lock()
	assert.Zero(t, handle.state.ownerUID)
	assert.Equal(t, VMKey{}, handle.state.key)
	assert.False(t, handle.state.admitted)
	handle.state.mu.Unlock()
	closeMu.Lock()
	assert.Equal(t, 1, closeCounts[failedFD])
	assert.Equal(t, 1, closeCounts[directoryFD])
	closeMu.Unlock()
	require.NoError(t, uid.Release(cancelled))
	require.NoError(t, authority.Release(cancelled))
}

// Rationale: a completed unlink with failed directory sync must retry only durability, never the mutation.
func TestNetworkNamespaceHandleRemovalRetriesOnlyRemainingEffect(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		failUnlink bool
		failSync   bool
	}{
		"unlink pending":         {failUnlink: true},
		"directory sync pending": {failSync: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareNamespaceAuthorityTree(t)
			deps := realDependencies()
			var unlinkCalls int
			originalUnlink := deps.unlinkAt
			deps.unlinkAt = func(ctx context.Context, parentFD int, leaf string, flags int) error {
				unlinkCalls++
				if test.failUnlink && unlinkCalls == 1 {
					return unix.EIO
				}
				return originalUnlink(ctx, parentFD, leaf, flags)
			}
			var namespaceSyncCalls int
			originalFsync := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				if unlinkCalls > boolToInt(test.failUnlink) {
					namespaceSyncCalls++
					if test.failSync && namespaceSyncCalls == 1 {
						return unix.EIO
					}
				}
				return originalFsync(ctx, fd)
			}
			authority, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
			require.NoError(t, err)
			uid, err := authority.OpenUID(t.Context(), testUID)
			require.NoError(t, err)
			key, err := NewVMKey(testVMID)
			require.NoError(t, err)
			handle, err := uid.CreateNamespaceHandle(t.Context(), key)
			require.NoError(t, err)

			require.ErrorIs(t, handle.Remove(t.Context()), unix.EIO)
			if test.failSync {
				_, identityErr := handle.TargetIdentity(t.Context())
				require.ErrorContains(t, identityErr, "unavailable")
				_, _, bindingErr := handle.Binding()
				require.ErrorContains(t, bindingErr, "not active")
			}
			require.NoError(t, handle.Remove(t.Context()))
			assert.Equal(t, 1+boolToInt(test.failUnlink), unlinkCalls)
			assert.Equal(t, 1+boolToInt(test.failSync), namespaceSyncCalls)
			require.NoError(t, handle.Release(t.Context()))
			require.NoError(t, uid.Release(t.Context()))
			require.NoError(t, authority.Release(t.Context()))
		})
	}
}

// Rationale: unlink may complete in the kernel before an error is reported, so retry must resume at parent sync.
func TestNetworkNamespaceHandleReconcilesUnlinkEffectThenError(t *testing.T) {
	t.Parallel()

	root := prepareNamespaceAuthorityTree(t)
	deps := realDependencies()
	originalUnlink := deps.unlinkAt
	var unlinkCalls int
	deps.unlinkAt = func(ctx context.Context, parentFD int, leaf string, flags int) error {
		unlinkCalls++
		if err := originalUnlink(ctx, parentFD, leaf, flags); err != nil {
			return err
		}
		if unlinkCalls == 1 {
			return unix.EIO
		}
		return nil
	}
	authority, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	handle, err := uid.CreateNamespaceHandle(t.Context(), key)
	require.NoError(t, err)

	require.ErrorIs(t, handle.Remove(t.Context()), unix.EIO)
	_, identityErr := handle.TargetIdentity(t.Context())
	require.ErrorContains(t, identityErr, "unavailable")
	_, _, bindingErr := handle.Binding()
	require.ErrorContains(t, bindingErr, "not active")
	require.NoError(t, handle.Remove(t.Context()))
	assert.Equal(t, 1, unlinkCalls)
	require.NoError(t, handle.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))
}

// Rationale: error reconciliation may observe a plausible replacement but must never adopt or unlink it.
func TestNetworkNamespaceHandleUnlinkErrorReconciliationRejectsReplacement(t *testing.T) {
	t.Parallel()

	root := prepareNamespaceAuthorityTree(t)
	placeholderPath := filepath.Join(root, "run/mvmctl/authority/1000/netns", testVMID)
	retainedPath := placeholderPath + ".retained"
	deps := realDependencies()
	originalUnlink := deps.unlinkAt
	var unlinkCalls int
	deps.unlinkAt = func(ctx context.Context, parentFD int, leaf string, flags int) error {
		unlinkCalls++
		if unlinkCalls != 1 {
			return originalUnlink(ctx, parentFD, leaf, flags)
		}
		if err := os.Rename(placeholderPath, retainedPath); err != nil {
			return err
		}
		if err := os.WriteFile(placeholderPath, nil, 0o600); err != nil {
			return err
		}
		return unix.EIO
	}
	authority, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	handle, err := uid.CreateNamespaceHandle(t.Context(), key)
	require.NoError(t, err)

	require.ErrorContains(t, handle.Remove(t.Context()), "identity changed")
	replacement, err := os.Lstat(placeholderPath)
	require.NoError(t, err)
	assert.Zero(t, replacement.Size())
	assert.Equal(t, os.FileMode(0o600), replacement.Mode().Perm())
	require.NoError(t, os.Remove(placeholderPath))
	require.NoError(t, os.Rename(retainedPath, placeholderPath))
	require.NoError(t, handle.Remove(t.Context()))
	assert.Equal(t, 2, unlinkCalls)
	require.NoError(t, handle.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))
}

func TestNetworkNamespaceMountIdentityKinds(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		stat    unix.Statx_t
		want    NetworkNamespaceMountIdentity
		wantErr string
	}{
		"unique": {
			stat: unix.Statx_t{Mask: unix.STATX_MNT_ID | unix.STATX_MNT_ID_UNIQUE, Mnt_id: 41},
			want: NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityUnique, Value: 41},
		},
		"legacy": {
			stat: unix.Statx_t{Mask: unix.STATX_MNT_ID, Mnt_id: 41},
			want: NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityLegacy, Value: 41},
		},
		"unavailable": {
			want: NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityUnavailable},
		},
		"unique zero": {
			stat:    unix.Statx_t{Mask: unix.STATX_MNT_ID_UNIQUE},
			wantErr: "unique mount identity is zero",
		},
		"legacy zero": {
			stat:    unix.Statx_t{Mask: unix.STATX_MNT_ID},
			wantErr: "mount identity is zero",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			deps := dependencies{statx: func(_ context.Context, _ int, _, _ int, stat *unix.Statx_t) error {
				*stat = test.stat
				return nil
			}}

			got, err := inspectNetworkNamespaceMountIdentity(t.Context(), deps, 7)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
	assert.NotEqual(
		t,
		NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityLegacy, Value: 41},
		NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityUnique, Value: 41},
	)
}

func TestNetworkNamespaceHandleLifecycleRejectsInactiveAuthority(t *testing.T) {
	t.Parallel()

	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	var inactive *NetworkUID
	handle, err := inactive.CreateNamespaceHandle(cancelled, key)
	assert.Nil(t, handle)
	require.ErrorIs(t, err, context.Canceled)
	handle, err = inactive.CreateNamespaceHandle(t.Context(), key)
	assert.Nil(t, handle)
	require.ErrorContains(t, err, "not active")

	root := prepareTestRoot(t)
	authority := openTestNetwork(t, root)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	handle, err = uid.CreateNamespaceHandle(t.Context(), key)
	require.NoError(t, err)
	require.ErrorContains(t, handle.Release(t.Context()), "has not been removed")
	ownerUID, boundKey, err := handle.Binding()
	require.NoError(t, err)
	assert.Equal(t, uint32(testUID), ownerUID)
	assert.Equal(t, key, boundKey)
	require.NoError(t, handle.Remove(t.Context()))
	require.NoError(t, handle.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))
}

func prepareNamespaceAuthorityTree(t *testing.T) string {
	t.Helper()

	root := prepareTestRoot(t)
	authority := openTestNetwork(t, root)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(root, "run/mvmctl/authority/1000/netns"), 0o700))
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))
	return root
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func testNamespaceMountIdentity(t *testing.T, path string) NetworkNamespaceMountIdentity {
	t.Helper()
	var stat unix.Statx_t
	require.NoError(t, unix.Statx(
		unix.AT_FDCWD,
		path,
		unix.AT_NO_AUTOMOUNT|unix.AT_SYMLINK_NOFOLLOW|unix.AT_STATX_SYNC_AS_STAT,
		networkNamespaceStatxMask,
		&stat,
	))
	require.NotZero(t, stat.Mnt_id)
	if stat.Mask&unix.STATX_MNT_ID_UNIQUE != 0 {
		return NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityUnique, Value: stat.Mnt_id}
	}
	require.NotZero(t, stat.Mask&unix.STATX_MNT_ID)
	return NetworkNamespaceMountIdentity{Kind: NetworkNamespaceMountIdentityLegacy, Value: stat.Mnt_id}
}
