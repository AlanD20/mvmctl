package authorityfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const (
	testUID       = uint32(1000)
	testVMID      = "0c3b18bebea288a6d609fab14bd9eb87"
	testReleaseID = "84f597105ced4fdcb569626b637026f234b5951275067c48f913740827de93b0"
)

func TestJailerCreatesFixedTreeAndLocks(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)

	index, err := jailer.AcquireIndex(t.Context())
	require.NoError(t, err)
	releaseKey, err := NewReleaseKey(testReleaseID)
	require.NoError(t, err)
	release, err := jailer.AcquireRelease(t.Context(), releaseKey)
	require.NoError(t, err)
	uid, err := jailer.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	vmKey, err := NewVMKey(testVMID)
	require.NoError(t, err)
	vm, err := uid.AcquireVM(t.Context(), vmKey)
	require.NoError(t, err)

	require.NoError(t, jailer.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, vm.Release(t.Context()))
	require.NoError(t, release.Release(t.Context()))
	require.NoError(t, index.Release(t.Context()))

	assertExactMode(t, filepath.Join(root, "run/mvmctl"), 0711)
	assertExactMode(t, filepath.Join(root, "run/mvmctl/authority"), 0700)
	assertExactMode(t, filepath.Join(root, "run/mvmctl/authority/releases"), 0700)
	assertExactMode(t, filepath.Join(root, "run/mvmctl/authority/index.lock"), 0600)
	assertExactMode(t, filepath.Join(root, "run/mvmctl/authority/releases/"+testReleaseID+".lock"), 0600)
	assertExactMode(t, filepath.Join(root, "run/mvmctl/authority/1000/"+testVMID+".lock"), 0600)
}

func TestJailerChildrenAndLocksOutliveParent(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	uid, err := jailer.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	index, err := jailer.AcquireIndex(t.Context())
	require.NoError(t, err)
	require.NoError(t, jailer.Release(t.Context()))

	vmKey, err := NewVMKey(testVMID)
	require.NoError(t, err)
	vm, err := uid.AcquireVM(t.Context(), vmKey)
	require.NoError(t, err)
	require.NoError(t, uid.Release(t.Context()))

	waitCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	reopened, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	blocked, err := reopened.AcquireIndex(waitCtx)
	assert.Nil(t, blocked)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	require.NoError(t, index.Release(t.Context()))
	require.NoError(t, vm.Release(t.Context()))
	require.NoError(t, reopened.Release(t.Context()))
}

func TestLockWaitIsCancellableAndLockFilePersists(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })

	first, err := jailer.AcquireIndex(t.Context())
	require.NoError(t, err)
	waitCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	second, err := jailer.AcquireIndex(waitCtx)
	assert.Nil(t, second)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, first.Release(t.Context()))

	path := filepath.Join(root, "run/mvmctl/authority/index.lock")
	assertExactMode(t, path, 0600)
	reacquired, err := jailer.AcquireIndex(t.Context())
	require.NoError(t, err)
	require.NoError(t, reacquired.Release(t.Context()))
	assert.FileExists(t, path)
}

func TestJailerRejectsUnsafeManagedTreeWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"symlink base": func(t *testing.T, root string) {
			require.NoError(t, os.Symlink("attacker", filepath.Join(root, "run/mvmctl")))
		},
		"wrong base mode": func(t *testing.T, root string) {
			require.NoError(t, os.Mkdir(filepath.Join(root, "run/mvmctl"), 0700))
		},
		"wrong authority mode": func(t *testing.T, root string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, "run/mvmctl/authority"), 0711))
			require.NoError(t, os.Chmod(filepath.Join(root, "run/mvmctl"), 0711))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareTestRoot(t)
			mutate(t, root)
			jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
			if jailer != nil {
				require.NoError(t, jailer.Release(context.Background()))
			}
			require.Error(t, err)
		})
	}
}

func TestJailerRejectsUnsafeExistingLockWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, lockPath string) {
			require.NoError(t, os.Symlink("attacker", lockPath))
		},
		"wrong mode": func(t *testing.T, lockPath string) {
			require.NoError(t, os.WriteFile(lockPath, nil, 0600))
			require.NoError(t, os.Chmod(lockPath, 0644))
		},
		"hard link": func(t *testing.T, lockPath string) {
			sourcePath := filepath.Join(filepath.Dir(lockPath), "other.lock")
			require.NoError(t, os.WriteFile(sourcePath, nil, 0600))
			require.NoError(t, os.Chmod(sourcePath, 0600))
			require.NoError(t, os.Link(sourcePath, lockPath))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareTestRoot(t)
			jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })
			lockPath := filepath.Join(root, "run/mvmctl/authority/index.lock")
			mutate(t, lockPath)

			lock, err := jailer.AcquireIndex(t.Context())
			assert.Nil(t, lock)
			require.Error(t, err)
			info, statErr := os.Lstat(lockPath)
			require.NoError(t, statErr)
			if name == "wrong mode" {
				assert.Equal(t, os.FileMode(0644), info.Mode().Perm())
			}
		})
	}
}

func TestJailerTreatsMkdirEEXISTAsConcurrentCreation(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	deps := realDependencies()
	realMkdirAt := deps.mkdirAt
	var chownCalls atomic.Int32
	var chmodCalls atomic.Int32
	deps.mkdirAt = func(ctx context.Context, parentFD int, name string, mode uint32) error {
		if err := realMkdirAt(ctx, parentFD, name, mode); err != nil {
			return err
		}
		return unix.EEXIST
	}
	deps.fchown = func(context.Context, int, int, int) error {
		chownCalls.Add(1)
		return errors.New("must not chown concurrently created object")
	}
	deps.fchmod = func(context.Context, int, uint32) error {
		chmodCalls.Add(1)
		return errors.New("must not chmod concurrently created object")
	}

	jailer, err := openJailerWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	require.NoError(t, jailer.Release(t.Context()))
	assert.Zero(t, chownCalls.Load())
	assert.Zero(t, chmodCalls.Load())
}

func TestJailerCreationFaultClosesEveryDescriptor(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*dependencies){
		"owner": func(deps *dependencies) {
			deps.fchown = func(context.Context, int, int, int) error { return errors.New("owner failed") }
		},
		"mode": func(deps *dependencies) {
			deps.fchmod = func(context.Context, int, uint32) error { return errors.New("mode failed") }
		},
		"sync": func(deps *dependencies) {
			deps.fsync = func(context.Context, int) error { return errors.New("sync failed") }
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareTestRoot(t)
			deps := realDependencies()
			realOpen := deps.open
			realOpenAt2 := deps.openAt2
			realClose := deps.close
			var opened atomic.Int32
			var closed atomic.Int32
			deps.open = func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
				fd, err := realOpen(ctx, name, flags, mode)
				if err == nil {
					opened.Add(1)
				}
				return fd, err
			}
			deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
				fd, err := realOpenAt2(ctx, parentFD, name, how)
				if err == nil {
					opened.Add(1)
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
			inject(&deps)

			jailer, err := openJailerWithPolicy(t.Context(), deps, testPolicy(root))
			assert.Nil(t, jailer)
			require.Error(t, err)
			assert.Equal(t, opened.Load(), closed.Load())
		})
	}
}

func TestLockCreationFaultClosesDescriptor(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*dependencies, *jailerState, *int){
		"owner": func(deps *dependencies, _ *jailerState, lockFD *int) {
			realFchown := deps.fchown
			deps.fchown = func(ctx context.Context, fd, uid, gid int) error {
				if fd == *lockFD {
					return errors.New("owner failed")
				}
				return realFchown(ctx, fd, uid, gid)
			}
		},
		"mode": func(deps *dependencies, _ *jailerState, lockFD *int) {
			realFchmod := deps.fchmod
			deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
				if fd == *lockFD {
					return errors.New("mode failed")
				}
				return realFchmod(ctx, fd, mode)
			}
		},
		"metadata": func(deps *dependencies, _ *jailerState, lockFD *int) {
			realFstat := deps.fstat
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if fd == *lockFD {
					return errors.New("metadata failed")
				}
				return realFstat(ctx, fd, stat)
			}
		},
		"lock sync": func(deps *dependencies, _ *jailerState, lockFD *int) {
			realFsync := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				if fd == *lockFD {
					return errors.New("lock sync failed")
				}
				return realFsync(ctx, fd)
			}
		},
		"parent sync": func(deps *dependencies, state *jailerState, lockFD *int) {
			realFsync := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				if *lockFD >= 0 && fd == state.authority {
					return errors.New("parent sync failed")
				}
				return realFsync(ctx, fd)
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareTestRoot(t)
			jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })
			state := jailer.state
			realOpenAt2 := state.deps.openAt2
			realClose := state.deps.close
			lockFD := -1
			closed := 0
			state.deps.openAt2 = func(
				ctx context.Context,
				parentFD int,
				entry string,
				how *unix.OpenHow,
			) (int, error) {
				fd, openErr := realOpenAt2(ctx, parentFD, entry, how)
				if openErr == nil && entry == "index.lock" {
					lockFD = fd
				}
				return fd, openErr
			}
			state.deps.close = func(ctx context.Context, fd int) error {
				if fd == lockFD {
					closed++
				}
				return realClose(ctx, fd)
			}
			inject(&state.deps, state, &lockFD)

			lock, err := jailer.AcquireIndex(t.Context())
			assert.Nil(t, lock)
			require.Error(t, err)
			assert.GreaterOrEqual(t, lockFD, 0)
			assert.Equal(t, 1, closed)
		})
	}
}

func TestLockReleaseIsCheckedIdempotentAndIgnoresCancellation(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	deps := realDependencies()
	realFlock := deps.flock
	realClose := deps.close
	var unlocks atomic.Int32
	var closes atomic.Int32
	var contextValueObserved atomic.Bool
	type contextKey struct{}
	deps.flock = func(ctx context.Context, fd int, how int) error {
		if how == unix.LOCK_UN {
			unlocks.Add(1)
			contextValueObserved.Store(ctx.Value(contextKey{}) == "retained")
		}
		return realFlock(ctx, fd, how)
	}
	deps.close = func(ctx context.Context, fd int) error {
		closes.Add(1)
		return realClose(ctx, fd)
	}
	jailer, err := openJailerWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	lock, err := jailer.AcquireIndex(t.Context())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "retained"))
	cancel()

	require.NoError(t, lock.Release(ctx))
	require.NoError(t, lock.Release(ctx))
	assert.Equal(t, int32(1), unlocks.Load())
	assert.True(t, contextValueObserved.Load())
	// Jailer still owns its independently retained descriptors.
	assert.Greater(t, closes.Load(), int32(0))
	require.NoError(t, jailer.Release(ctx))
}

func TestCapabilitiesCacheFirstReleaseErrorWithoutRetry(t *testing.T) {
	t.Parallel()

	releaseErr := errors.New("release failed")
	type releaseTest struct {
		release func(context.Context) error
		calls   *atomic.Int32
	}
	jailerCalls := &atomic.Int32{}
	jailer := &Jailer{state: &jailerState{
		deps: dependencies{close: func(context.Context, int) error {
			jailerCalls.Add(1)
			return releaseErr
		}},
		authority: 10,
		releases:  11,
		retained:  []int{12},
		active:    true,
	}}
	uidCalls := &atomic.Int32{}
	uid := &JailerUID{state: &jailerUIDState{deps: dependencies{close: func(context.Context, int) error {
		uidCalls.Add(1)
		return releaseErr
	}}, fd: 10, active: true}}
	lockCalls := &atomic.Int32{}
	lock := &Lock{state: &lockState{deps: dependencies{
		flock: func(context.Context, int, int) error {
			lockCalls.Add(1)
			return releaseErr
		},
		close: func(context.Context, int) error { return nil },
	}, fd: 10, active: true}}
	tests := map[string]releaseTest{
		"Jailer":     {release: jailer.Release, calls: jailerCalls},
		"Jailer UID": {release: uid.Release, calls: uidCalls},
		"lock":       {release: lock.Release, calls: lockCalls},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			first := test.release(t.Context())
			require.ErrorIs(t, first, releaseErr)
			second := test.release(t.Context())
			assert.Equal(t, first, second)
			assert.Equal(t, int32(1), test.calls.Load())
		})
	}
}

func TestCopiedCapabilitiesShareExactlyOnceReleaseState(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*atomic.Int32) (func(context.Context) error, func(context.Context) error){
		"Jailer": func(calls *atomic.Int32) (func(context.Context) error, func(context.Context) error) {
			original := Jailer{state: &jailerState{
				deps: dependencies{close: func(context.Context, int) error {
					calls.Add(1)
					return nil
				}},
				retained: []int{10}, active: true,
			}}
			copied := original
			return original.Release, copied.Release
		},
		"Jailer UID": func(calls *atomic.Int32) (func(context.Context) error, func(context.Context) error) {
			original := JailerUID{state: &jailerUIDState{
				deps: dependencies{close: func(context.Context, int) error {
					calls.Add(1)
					return nil
				}},
				fd: 10, active: true,
			}}
			copied := original
			return original.Release, copied.Release
		},
		"lock": func(calls *atomic.Int32) (func(context.Context) error, func(context.Context) error) {
			original := Lock{state: &lockState{
				deps: dependencies{
					flock: func(context.Context, int, int) error { return nil },
					close: func(context.Context, int) error {
						calls.Add(1)
						return nil
					},
				},
				fd: 10, active: true,
			}}
			copied := original
			return original.Release, copied.Release
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			calls := &atomic.Int32{}
			releaseOriginal, releaseCopy := build(calls)
			require.NoError(t, releaseOriginal(t.Context()))
			require.NoError(t, releaseCopy(t.Context()))
			assert.Equal(t, int32(1), calls.Load(), "a copied capability must not act on a reused descriptor")
		})
	}
}

func TestLockEINTRRetryObservesCancellationAndClosesDescriptor(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })
	state := jailer.state
	realOpenAt2 := state.deps.openAt2
	realClose := state.deps.close
	lockFD := -1
	closed := 0
	state.deps.openAt2 = func(
		ctx context.Context,
		parentFD int,
		entry string,
		how *unix.OpenHow,
	) (int, error) {
		fd, openErr := realOpenAt2(ctx, parentFD, entry, how)
		if openErr == nil && entry == "index.lock" {
			lockFD = fd
		}
		return fd, openErr
	}
	state.deps.close = func(ctx context.Context, fd int) error {
		if fd == lockFD {
			closed++
		}
		return realClose(ctx, fd)
	}
	ctx, cancel := context.WithCancel(t.Context())
	state.deps.flock = func(context.Context, int, int) error {
		cancel()
		return unix.EINTR
	}

	lock, err := jailer.AcquireIndex(ctx)
	assert.Nil(t, lock)
	require.ErrorIs(t, err, context.Canceled)
	assert.GreaterOrEqual(t, lockFD, 0)
	assert.Equal(t, 1, closed)
}

func TestZeroValueCapabilitiesFailClosed(t *testing.T) {
	t.Parallel()

	var jailer Jailer
	index, err := jailer.AcquireIndex(t.Context())
	assert.Nil(t, index)
	require.Error(t, err)
	uid, err := jailer.OpenUID(t.Context(), testUID)
	assert.Nil(t, uid)
	require.Error(t, err)
	require.NoError(t, jailer.Release(t.Context()))

	vmKey, err := NewVMKey(testVMID)
	require.NoError(t, err)
	var uidAuthority JailerUID
	vm, err := uidAuthority.AcquireVM(t.Context(), vmKey)
	assert.Nil(t, vm)
	require.Error(t, err)
	require.NoError(t, uidAuthority.Release(t.Context()))

	var lock Lock
	require.NoError(t, lock.Release(t.Context()))

	var nilJailer *Jailer
	index, err = nilJailer.AcquireIndex(t.Context())
	assert.Nil(t, index)
	require.Error(t, err)
	var nilUID *JailerUID
	vm, err = nilUID.AcquireVM(t.Context(), vmKey)
	assert.Nil(t, vm)
	require.Error(t, err)
}

func TestJailerConcurrentAcquireAndReleaseHasCoherentOutcome(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	start := make(chan struct{})
	acquired := make(chan *Lock, 1)
	acquireErr := make(chan error, 1)
	releaseErr := make(chan error, 1)
	go func() {
		<-start
		lock, err := jailer.AcquireIndex(t.Context())
		acquired <- lock
		acquireErr <- err
	}()
	go func() {
		<-start
		releaseErr <- jailer.Release(t.Context())
	}()
	close(start)

	lock := <-acquired
	err = <-acquireErr
	require.NoError(t, <-releaseErr)
	if lock == nil {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "released")
		return
	}
	require.NoError(t, err)
	require.NoError(t, lock.Release(t.Context()))
}

func TestClosedKeysRejectNonCanonicalValues(t *testing.T) {
	t.Parallel()

	vmTests := map[string]string{
		"empty":     "",
		"short":     "0c3",
		"uppercase": "0C3B18BEBEA288A6D609FAB14BD9EB87",
		"separator": "0c3b18bebea288a6d609fab14bd9eb8/",
	}
	for name, value := range vmTests {
		t.Run("vm "+name, func(t *testing.T) {
			_, err := NewVMKey(value)
			require.Error(t, err)
		})
	}
	releaseTests := map[string]string{
		"empty":     "",
		"short":     "84f597",
		"uppercase": "84F597105CED4FDCB569626B637026F234B5951275067C48F913740827DE93B0",
		"separator": testReleaseID[:63] + "/",
	}
	for name, value := range releaseTests {
		t.Run("release "+name, func(t *testing.T) {
			_, err := NewReleaseKey(value)
			require.Error(t, err)
		})
	}
}

func prepareTestRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "run"), 0755))
	return root
}

func testPolicy(root string) policy {
	return policy{rootPath: root, expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid())}
}

func assertExactMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm())
}

func TestJailerLockContentionWaitHookIsBounded(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	deps := realDependencies()
	realWait := deps.waitLock
	var waits atomic.Int32
	var once sync.Once
	observed := make(chan struct{})
	deps.waitLock = func(ctx context.Context) error {
		waits.Add(1)
		once.Do(func() { close(observed) })
		return realWait(ctx)
	}
	jailer, err := openJailerWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })
	first, err := jailer.AcquireIndex(t.Context())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, acquireErr := jailer.AcquireIndex(ctx)
		result <- acquireErr
	}()
	select {
	case <-observed:
	case <-time.After(time.Second):
		require.Fail(t, "lock contention was not observed")
	}
	cancel()
	require.Error(t, <-result)
	assert.GreaterOrEqual(t, waits.Load(), int32(1))
	require.NoError(t, first.Release(t.Context()))
}
