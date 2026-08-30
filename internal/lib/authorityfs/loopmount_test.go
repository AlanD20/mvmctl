package authorityfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const testImageID = "84f597105ced4fdcb569626b637026f234b5951275067c48f913740827de93b0"

func TestImageKeyAdmitsOnlyCanonicalLowercaseHex(t *testing.T) {
	t.Parallel()

	key, err := NewImageKey(testImageID)
	require.NoError(t, err)
	assert.Equal(t, testImageID, key.value)

	tests := map[string]string{
		"empty":     "",
		"short":     testImageID[:63],
		"long":      testImageID + "0",
		"uppercase": "84F597105CED4FDCB569626B637026F234B5951275067C48F913740827DE93B0",
		"separator": testImageID[:63] + "/",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := NewImageKey(value)
			require.Error(t, err)
		})
	}
}

func TestLoopMountCreatesOnlyItsFixedAuthorityHierarchyAndLocks(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority, err := openLoopMountWithPolicy(
		t.Context(),
		realDependencies(),
		testPolicy(root),
		testUID,
	)
	require.NoError(t, err)

	vmKey, err := NewVMKey(testVMID)
	require.NoError(t, err)
	vmLock, err := authority.AcquireVM(t.Context(), vmKey)
	require.NoError(t, err)
	imageKey, err := NewImageKey(testImageID)
	require.NoError(t, err)
	imageLock, err := authority.AcquireImage(t.Context(), imageKey)
	require.NoError(t, err)

	require.NoError(t, imageLock.Release(t.Context()))
	require.NoError(t, vmLock.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))

	base := filepath.Join(root, "run/mvmctl")
	private := filepath.Join(base, "authority")
	loopmount := filepath.Join(private, formatUID(testUID), "loopmount")
	assertExactMode(t, base, 0711)
	assertExactMode(t, private, 0700)
	assertExactMode(t, filepath.Dir(loopmount), 0700)
	assertExactMode(t, loopmount, 0700)
	assertExactMode(t, filepath.Join(loopmount, "vm-"+testVMID+".lock"), 0600)
	assertExactMode(t, filepath.Join(loopmount, "image-"+testImageID+".lock"), 0600)
	for _, path := range []string{base, private, filepath.Dir(loopmount), loopmount} {
		assertTestOwner(t, path)
	}
	for _, path := range []string{
		filepath.Join(loopmount, "vm-"+testVMID+".lock"),
		filepath.Join(loopmount, "image-"+testImageID+".lock"),
	} {
		assertTestOwner(t, path)
		var stat unix.Stat_t
		require.NoError(t, unix.Stat(path, &stat))
		assert.Equal(t, uint64(1), stat.Nlink)
		assert.Zero(t, stat.Size)
	}
	assert.NoDirExists(t, filepath.Join(private, "releases"))
	assert.NoFileExists(t, filepath.Join(private, "index.lock"))

	entries, err := os.ReadDir(loopmount)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, "image-"+testImageID+".lock", entries[0].Name())
	assert.Equal(t, "vm-"+testVMID+".lock", entries[1].Name())
}

func TestOpenLoopMountRejectsZeroUID(t *testing.T) {
	t.Parallel()

	authority, err := OpenLoopMount(context.Background(), 0)
	assert.Nil(t, authority)
	require.Error(t, err)
}

func TestLoopMountContentionIsScopedByUIDFamilyAndIdentity(t *testing.T) {
	t.Parallel()

	const alternateVMID = "1c3b18bebea288a6d609fab14bd9eb87"
	const alternateImageID = "94f597105ced4fdcb569626b637026f234b5951275067c48f913740827de93b0"
	tests := map[string]struct {
		heldUID       uint32
		contenderUID  uint32
		heldFamily    string
		contendFamily string
		heldID        string
		contenderID   string
		wantBlocked   bool
	}{
		"same UID VM and ID": {
			testUID, testUID, "vm", "vm", testVMID, testVMID, true,
		},
		"same UID image and ID": {
			testUID, testUID, "image", "image", testImageID, testImageID, true,
		},
		"different UID": {
			testUID, testUID + 1, "vm", "vm", testVMID, testVMID, false,
		},
		"different family": {
			testUID, testUID, "vm", "image", testVMID, testImageID, false,
		},
		"different VM ID": {
			testUID, testUID, "vm", "vm", testVMID, alternateVMID, false,
		},
		"different image ID": {
			testUID, testUID, "image", "image", testImageID, alternateImageID, false,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareTestRoot(t)
			heldAuthority := openTestLoopMount(t, root, test.heldUID)
			contenderAuthority := openTestLoopMount(t, root, test.contenderUID)
			held, err := acquireLoopMountTestLock(t.Context(), heldAuthority, test.heldFamily, test.heldID)
			require.NoError(t, err)

			ctx := t.Context()
			if test.wantBlocked {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
				defer cancel()
			}
			contender, err := acquireLoopMountTestLock(
				ctx,
				contenderAuthority,
				test.contendFamily,
				test.contenderID,
			)
			if test.wantBlocked {
				assert.Nil(t, contender)
				require.ErrorIs(t, err, context.DeadlineExceeded)
			} else {
				require.NoError(t, err)
				require.NoError(t, contender.Release(t.Context()))
			}
			require.NoError(t, held.Release(t.Context()))
		})
	}
}

func TestLoopMountLockWaitIsCancellationAware(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	deps := realDependencies()
	realWait := deps.waitLock
	var observedOnce sync.Once
	observed := make(chan struct{})
	deps.waitLock = func(ctx context.Context) error {
		observedOnce.Do(func() { close(observed) })
		return realWait(ctx)
	}
	firstAuthority, err := openLoopMountWithPolicy(t.Context(), deps, testPolicy(root), testUID)
	require.NoError(t, err)
	secondAuthority, err := openLoopMountWithPolicy(t.Context(), deps, testPolicy(root), testUID)
	require.NoError(t, err)
	vmKey, err := NewVMKey(testVMID)
	require.NoError(t, err)
	held, err := firstAuthority.AcquireVM(t.Context(), vmKey)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, acquireErr := secondAuthority.AcquireVM(ctx, vmKey)
		result <- acquireErr
	}()
	select {
	case <-observed:
	case <-time.After(time.Second):
		require.Fail(t, "LoopMount lock contention was not observed")
	}
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
	require.NoError(t, held.Release(t.Context()))
	require.NoError(t, secondAuthority.Release(t.Context()))
	require.NoError(t, firstAuthority.Release(t.Context()))
}

func TestLoopMountLocksOutliveParentCapability(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority := openTestLoopMount(t, root, testUID)
	vmKey, err := NewVMKey(testVMID)
	require.NoError(t, err)
	held, err := authority.AcquireVM(t.Context(), vmKey)
	require.NoError(t, err)
	require.NoError(t, authority.Release(t.Context()))

	reopened := openTestLoopMount(t, root, testUID)
	waitCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	blocked, err := reopened.AcquireVM(waitCtx, vmKey)
	assert.Nil(t, blocked)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, held.Release(t.Context()))
	reacquired, err := reopened.AcquireVM(t.Context(), vmKey)
	require.NoError(t, err)
	require.NoError(t, reacquired.Release(t.Context()))
}

func TestCopiedLoopMountSharesCachedReleaseState(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	deps := realDependencies()
	realClose := deps.close
	releaseErr := errors.New("release failed")
	var closes atomic.Int32
	var contextObserved atomic.Bool
	type contextKey struct{}
	deps.close = func(ctx context.Context, fd int) error {
		closes.Add(1)
		contextObserved.Store(contextObserved.Load() || ctx.Value(contextKey{}) == "retained")
		return errors.Join(realClose(ctx, fd), releaseErr)
	}
	authority, err := openLoopMountWithPolicy(t.Context(), deps, testPolicy(root), testUID)
	require.NoError(t, err)
	copied := *authority
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "retained"))
	cancel()

	first := authority.Release(ctx)
	require.ErrorIs(t, first, releaseErr)
	closedAfterFirst := closes.Load()
	second := copied.Release(ctx)
	assert.Equal(t, first, second)
	assert.Equal(t, closedAfterFirst, closes.Load())
	assert.True(t, contextObserved.Load())
}

func TestLoopMountRejectsUnsafeUniqueDirectoriesWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		prepare func(*testing.T, string)
		path    string
	}{
		"UID wrong mode": {
			prepare: func(t *testing.T, root string) {
				prepareLoopMountParent(t, root)
				require.NoError(t, os.Chmod(filepath.Join(root, "run/mvmctl/authority", formatUID(testUID)), 0755))
			},
			path: filepath.Join("run/mvmctl/authority", formatUID(testUID)),
		},
		"loopmount wrong mode": {
			prepare: func(t *testing.T, root string) {
				prepareLoopMountParent(t, root)
				require.NoError(t, os.Mkdir(filepath.Join(
					root,
					"run/mvmctl/authority",
					formatUID(testUID),
					"loopmount",
				), 0755))
			},
			path: filepath.Join("run/mvmctl/authority", formatUID(testUID), "loopmount"),
		},
		"loopmount symlink": {
			prepare: func(t *testing.T, root string) {
				prepareLoopMountParent(t, root)
				require.NoError(t, os.Symlink("attacker", filepath.Join(
					root,
					"run/mvmctl/authority",
					formatUID(testUID),
					"loopmount",
				)))
			},
			path: filepath.Join("run/mvmctl/authority", formatUID(testUID), "loopmount"),
		},
		"loopmount file": {
			prepare: func(t *testing.T, root string) {
				prepareLoopMountParent(t, root)
				require.NoError(t, os.WriteFile(filepath.Join(
					root,
					"run/mvmctl/authority",
					formatUID(testUID),
					"loopmount",
				), nil, 0600))
			},
			path: filepath.Join("run/mvmctl/authority", formatUID(testUID), "loopmount"),
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareTestRoot(t)
			test.prepare(t, root)
			authority, err := openLoopMountWithPolicy(
				t.Context(), realDependencies(), testPolicy(root), testUID,
			)
			assert.Nil(t, authority)
			require.Error(t, err)
			info, statErr := os.Lstat(filepath.Join(root, test.path))
			require.NoError(t, statErr)
			if strings.Contains(name, "wrong mode") {
				assert.Equal(t, os.FileMode(0755), info.Mode().Perm())
			}
			if name == "loopmount symlink" {
				assert.NotZero(t, info.Mode()&os.ModeSymlink)
			}
		})
	}
}

func TestLoopMountRejectsUnsafeExistingLocksWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, path string) {
			require.NoError(t, os.Symlink("attacker", path))
		},
		"directory": func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0600))
		},
		"hard link": func(t *testing.T, path string) {
			source := filepath.Join(filepath.Dir(path), "other.lock")
			require.NoError(t, os.WriteFile(source, nil, 0600))
			require.NoError(t, os.Link(source, path))
		},
		"nonzero size": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte("x"), 0600))
		},
		"wrong mode": func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, nil, 0644))
		},
	}
	for name, create := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareTestRoot(t)
			authority := openTestLoopMount(t, root, testUID)
			path := filepath.Join(
				root,
				"run/mvmctl/authority",
				formatUID(testUID),
				"loopmount",
				"image-"+testImageID+".lock",
			)
			create(t, path)
			imageKey, err := NewImageKey(testImageID)
			require.NoError(t, err)

			lock, err := authority.AcquireImage(t.Context(), imageKey)
			assert.Nil(t, lock)
			require.Error(t, err)
			info, statErr := os.Lstat(path)
			require.NoError(t, statErr)
			if name == "nonzero size" {
				assert.Equal(t, int64(1), info.Size())
			}
			if name == "wrong mode" {
				assert.Equal(t, os.FileMode(0644), info.Mode().Perm())
			}
		})
	}
}

func TestLoopMountRejectsSimulatedForeignOwner(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority := openTestLoopMount(t, root, testUID)
	state := authority.state
	realOpenAt2 := state.deps.openAt2
	realFstat := state.deps.fstat
	lockFD := -1
	state.deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil && name == "vm-"+testVMID+".lock" {
			lockFD = fd
		}
		return fd, err
	}
	state.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		if err := realFstat(ctx, fd, stat); err != nil {
			return err
		}
		if fd == lockFD {
			stat.Uid++
		}
		return nil
	}
	vmKey, err := NewVMKey(testVMID)
	require.NoError(t, err)
	lock, err := authority.AcquireVM(t.Context(), vmKey)
	assert.Nil(t, lock)
	require.Error(t, err)
}

func TestLoopMountTreatsMkdirEEXISTAsConcurrentCreation(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	prepareLoopMountParent(t, root)
	deps := realDependencies()
	realMkdirAt := deps.mkdirAt
	var chownCalls atomic.Int32
	var chmodCalls atomic.Int32
	deps.mkdirAt = func(ctx context.Context, parentFD int, name string, mode uint32) error {
		if name != "loopmount" {
			return realMkdirAt(ctx, parentFD, name, mode)
		}
		require.NoError(t, realMkdirAt(ctx, parentFD, name, mode))
		return unix.EEXIST
	}
	deps.fchown = func(context.Context, int, int, int) error {
		chownCalls.Add(1)
		return errors.New("must not chown concurrently created directory")
	}
	deps.fchmod = func(context.Context, int, uint32) error {
		chmodCalls.Add(1)
		return errors.New("must not chmod concurrently created directory")
	}

	authority, err := openLoopMountWithPolicy(t.Context(), deps, testPolicy(root), testUID)
	require.NoError(t, err)
	require.NoError(t, authority.Release(t.Context()))
	assert.Zero(t, chownCalls.Load())
	assert.Zero(t, chmodCalls.Load())
	assertExactMode(t, filepath.Join(root, "run/mvmctl/authority", formatUID(testUID), "loopmount"), 0700)
}

func TestLoopMountExistingLockIsVerifiedWithoutRepair(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority := openTestLoopMount(t, root, testUID)
	path := filepath.Join(
		root,
		"run/mvmctl/authority",
		formatUID(testUID),
		"loopmount",
		"vm-"+testVMID+".lock",
	)
	require.NoError(t, os.WriteFile(path, nil, 0600))
	state := authority.state
	var chownCalls atomic.Int32
	var chmodCalls atomic.Int32
	state.deps.fchown = func(context.Context, int, int, int) error {
		chownCalls.Add(1)
		return errors.New("must not chown existing lock")
	}
	state.deps.fchmod = func(context.Context, int, uint32) error {
		chmodCalls.Add(1)
		return errors.New("must not chmod existing lock")
	}
	vmKey, err := NewVMKey(testVMID)
	require.NoError(t, err)
	lock, err := authority.AcquireVM(t.Context(), vmKey)
	require.NoError(t, err)
	require.NoError(t, lock.Release(t.Context()))
	assert.Zero(t, chownCalls.Load())
	assert.Zero(t, chmodCalls.Load())
}

func TestLoopMountDirectoryCreationFaultClosesEveryDescriptor(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*dependencies, *int, *int){
		"owner": func(deps *dependencies, targetFD *int, _ *int) {
			realFchown := deps.fchown
			deps.fchown = func(ctx context.Context, fd, uid, gid int) error {
				if fd == *targetFD {
					return errors.New("owner failed")
				}
				return realFchown(ctx, fd, uid, gid)
			}
		},
		"mode": func(deps *dependencies, targetFD *int, _ *int) {
			realFchmod := deps.fchmod
			deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
				if fd == *targetFD {
					return errors.New("mode failed")
				}
				return realFchmod(ctx, fd, mode)
			}
		},
		"metadata": func(deps *dependencies, targetFD *int, _ *int) {
			realFstat := deps.fstat
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if fd == *targetFD {
					return errors.New("metadata failed")
				}
				return realFstat(ctx, fd, stat)
			}
		},
		"directory sync": func(deps *dependencies, targetFD *int, _ *int) {
			realFsync := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				if fd == *targetFD {
					return errors.New("directory sync failed")
				}
				return realFsync(ctx, fd)
			}
		},
		"parent sync": func(deps *dependencies, targetFD *int, parentFD *int) {
			realFsync := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				if *targetFD >= 0 && fd == *parentFD {
					return errors.New("parent sync failed")
				}
				return realFsync(ctx, fd)
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareTestRoot(t)
			prepareLoopMountParent(t, root)
			deps := realDependencies()
			realOpen := deps.open
			realOpenAt2 := deps.openAt2
			realClose := deps.close
			var opened atomic.Int32
			var closed atomic.Int32
			targetFD := -1
			parentFD := -1
			deps.open = func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
				fd, err := realOpen(ctx, name, flags, mode)
				if err == nil {
					opened.Add(1)
				}
				return fd, err
			}
			deps.openAt2 = func(ctx context.Context, parent int, entry string, how *unix.OpenHow) (int, error) {
				fd, err := realOpenAt2(ctx, parent, entry, how)
				if err == nil {
					opened.Add(1)
					if entry == formatUID(testUID) {
						parentFD = fd
					}
					if entry == "loopmount" {
						targetFD = fd
					}
				}
				return fd, err
			}
			deps.close = func(ctx context.Context, fd int) error {
				err := realClose(ctx, fd)
				if err == nil {
					closed.Add(1)
				}
				return err
			}
			inject(&deps, &targetFD, &parentFD)

			authority, err := openLoopMountWithPolicy(t.Context(), deps, testPolicy(root), testUID)
			assert.Nil(t, authority)
			require.Error(t, err)
			assert.GreaterOrEqual(t, targetFD, 0)
			assert.Equal(t, opened.Load(), closed.Load())
		})
	}
}

func TestLoopMountLockSyncFaultClosesDescriptor(t *testing.T) {
	t.Parallel()

	tests := map[string]func(int, int) bool{
		"lock sync":   func(fd, lockFD int) bool { return fd == lockFD },
		"parent sync": func(fd, _ int) bool { return fd >= 0 },
	}
	for name, shouldFail := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareTestRoot(t)
			authority := openTestLoopMount(t, root, testUID)
			state := authority.state
			realOpenAt2 := state.deps.openAt2
			realFsync := state.deps.fsync
			realClose := state.deps.close
			lockFD := -1
			closed := 0
			state.deps.openAt2 = func(
				ctx context.Context,
				parentFD int,
				entry string,
				how *unix.OpenHow,
			) (int, error) {
				fd, err := realOpenAt2(ctx, parentFD, entry, how)
				if err == nil && entry == "image-"+testImageID+".lock" {
					lockFD = fd
				}
				return fd, err
			}
			state.deps.fsync = func(ctx context.Context, fd int) error {
				if name == "lock sync" && shouldFail(fd, lockFD) {
					return errors.New("lock sync failed")
				}
				if name == "parent sync" && lockFD >= 0 && fd == state.fd {
					return errors.New("parent sync failed")
				}
				return realFsync(ctx, fd)
			}
			state.deps.close = func(ctx context.Context, fd int) error {
				if fd == lockFD {
					closed++
				}
				return realClose(ctx, fd)
			}
			imageKey, err := NewImageKey(testImageID)
			require.NoError(t, err)

			lock, err := authority.AcquireImage(t.Context(), imageKey)
			assert.Nil(t, lock)
			require.Error(t, err)
			assert.GreaterOrEqual(t, lockFD, 0)
			assert.Equal(t, 1, closed)
		})
	}
}

func prepareLoopMountParent(t *testing.T, root string) {
	t.Helper()

	private := filepath.Join(root, "run/mvmctl/authority")
	require.NoError(t, os.MkdirAll(filepath.Join(private, formatUID(testUID)), 0700))
	require.NoError(t, os.Chmod(filepath.Join(root, "run/mvmctl"), 0711))
	require.NoError(t, os.Chmod(private, 0700))
	require.NoError(t, os.Chmod(filepath.Join(private, formatUID(testUID)), 0700))
}

func assertTestOwner(t *testing.T, path string) {
	t.Helper()

	var stat unix.Stat_t
	require.NoError(t, unix.Stat(path, &stat))
	assert.Equal(t, uint32(os.Geteuid()), stat.Uid)
	assert.Equal(t, uint32(os.Getegid()), stat.Gid)
}

func openTestLoopMount(t *testing.T, root string, uid uint32) *LoopMount {
	t.Helper()

	authority, err := openLoopMountWithPolicy(t.Context(), realDependencies(), testPolicy(root), uid)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, authority.Release(context.Background())) })
	return authority
}

func acquireLoopMountTestLock(
	ctx context.Context,
	authority *LoopMount,
	family string,
	identity string,
) (*Lock, error) {
	if family == "vm" {
		key, err := NewVMKey(identity)
		if err != nil {
			return nil, err
		}
		return authority.AcquireVM(ctx, key)
	}
	key, err := NewImageKey(identity)
	if err != nil {
		return nil, err
	}
	return authority.AcquireImage(ctx, key)
}
