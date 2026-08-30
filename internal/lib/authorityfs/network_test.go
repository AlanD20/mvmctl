package authorityfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const (
	testNetworkID          = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testAlternateNetworkID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func TestNetworkKeyAdmitsOnlyCanonicalLowercaseHex(t *testing.T) {
	t.Parallel()

	key, err := NewNetworkKey(testNetworkID)
	require.NoError(t, err)
	assert.Equal(t, testNetworkID, key.value)

	tests := map[string]string{
		"empty":     "",
		"short":     testNetworkID[:63],
		"long":      testNetworkID + "b",
		"uppercase": "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		"separator": testNetworkID[:63] + "/",
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := NewNetworkKey(value)
			require.Error(t, err)
		})
	}
}

func TestOpenNetworkCreationFaultClosesEveryDescriptor(t *testing.T) {
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

			authority, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
			assert.Nil(t, authority)
			require.Error(t, err)
			assert.Equal(t, opened.Load(), closed.Load())
		})
	}
}

func TestNetworkFilesystemEffectsReceiveOperationContext(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	deps := realDependencies()
	type contextKey struct{}
	const contextValue = "network-operation"
	var seenMu sync.Mutex
	seen := make(map[string]bool)
	mark := func(operation string, ctx context.Context) {
		seenMu.Lock()
		seen[operation] = seen[operation] || ctx.Value(contextKey{}) == contextValue
		seenMu.Unlock()
	}
	realOpen := deps.open
	deps.open = func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
		mark("open", ctx)
		return realOpen(ctx, name, flags, mode)
	}
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		mark("openat2", ctx)
		return realOpenAt2(ctx, parentFD, name, how)
	}
	realMkdirAt := deps.mkdirAt
	deps.mkdirAt = func(ctx context.Context, parentFD int, name string, mode uint32) error {
		mark("mkdirat", ctx)
		return realMkdirAt(ctx, parentFD, name, mode)
	}
	realFstat := deps.fstat
	deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
		mark("fstat", ctx)
		return realFstat(ctx, fd, stat)
	}
	realFchown := deps.fchown
	deps.fchown = func(ctx context.Context, fd, uid, gid int) error {
		mark("fchown", ctx)
		return realFchown(ctx, fd, uid, gid)
	}
	realFchmod := deps.fchmod
	deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
		mark("fchmod", ctx)
		return realFchmod(ctx, fd, mode)
	}
	realFsync := deps.fsync
	deps.fsync = func(ctx context.Context, fd int) error {
		mark("fsync", ctx)
		return realFsync(ctx, fd)
	}
	realClose := deps.close
	deps.close = func(ctx context.Context, fd int) error {
		mark("close", ctx)
		return realClose(ctx, fd)
	}
	realFlock := deps.flock
	deps.flock = func(ctx context.Context, fd, how int) error {
		mark("flock", ctx)
		return realFlock(ctx, fd, how)
	}

	ctx := context.WithValue(t.Context(), contextKey{}, contextValue)
	authority, err := openNetworkWithPolicy(ctx, deps, testPolicy(root))
	require.NoError(t, err)
	uid, err := authority.OpenUID(ctx, testUID)
	require.NoError(t, err)
	key, err := NewNetworkKey(testNetworkID)
	require.NoError(t, err)
	lock, err := uid.AcquireNetwork(ctx, key)
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, lock.Release(cancelled))
	require.NoError(t, uid.Release(cancelled))
	require.NoError(t, authority.Release(cancelled))

	wantOperations := []string{
		"close", "fchmod", "fchown", "flock", "fstat", "fsync", "mkdirat", "open", "openat2",
	}
	seenMu.Lock()
	gotOperations := make([]string, 0, len(seen))
	for operation, retained := range seen {
		assert.True(t, retained, "%s did not receive the operation context", operation)
		gotOperations = append(gotOperations, operation)
	}
	seenMu.Unlock()
	slices.Sort(gotOperations)
	if diff := cmp.Diff(wantOperations, gotOperations); diff != "" {
		t.Errorf("context-aware filesystem effects mismatch (-want +got):\n%s", diff)
	}
}

func TestNetworkCreatesOnlyFixedAuthorityHierarchyAndLocks(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority, err := openNetworkWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	index, err := authority.AcquireIndex(t.Context())
	require.NoError(t, err)
	key, err := NewNetworkKey(testNetworkID)
	require.NoError(t, err)
	networkLock, err := uid.AcquireNetwork(t.Context(), key)
	require.NoError(t, err)

	require.NoError(t, networkLock.Release(t.Context()))
	require.NoError(t, index.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))

	base := filepath.Join(root, "run/mvmctl")
	private := filepath.Join(base, "authority")
	uidPath := filepath.Join(private, formatUID(testUID))
	indexPath := filepath.Join(private, networkIndexLockName)
	lockPath := filepath.Join(uidPath, "network-"+testNetworkID+".lock")
	for path, mode := range map[string]os.FileMode{
		base:      0711,
		private:   0700,
		uidPath:   0700,
		indexPath: 0600,
		lockPath:  0600,
	} {
		assertExactMode(t, path, mode)
		assertTestOwner(t, path)
	}
	for _, path := range []string{indexPath, lockPath} {
		var stat unix.Stat_t
		require.NoError(t, unix.Stat(path, &stat))
		assert.Equal(t, uint64(1), stat.Nlink)
		assert.Zero(t, stat.Size)
	}
	assert.NoDirExists(t, filepath.Join(private, "releases"))
	assert.NoFileExists(t, filepath.Join(private, "index.lock"))

	entries, err := os.ReadDir(private)
	require.NoError(t, err)
	wantEntries := []string{formatUID(testUID), networkIndexLockName}
	gotEntries := make([]string, 0, len(entries))
	for _, entry := range entries {
		gotEntries = append(gotEntries, entry.Name())
	}
	if diff := cmp.Diff(wantEntries, gotEntries); diff != "" {
		t.Errorf("network authority entries mismatch (-want +got):\n%s", diff)
	}
	uidEntries, err := os.ReadDir(uidPath)
	require.NoError(t, err)
	require.Len(t, uidEntries, 1)
	assert.Equal(t, "network-"+testNetworkID+".lock", uidEntries[0].Name())
}

func TestNetworkChildrenAndLocksOutliveParentCapabilities(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	authority := openTestNetwork(t, root)
	uid, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	index, err := authority.AcquireIndex(t.Context())
	require.NoError(t, err)
	key, err := NewNetworkKey(testNetworkID)
	require.NoError(t, err)
	held, err := uid.AcquireNetwork(t.Context(), key)
	require.NoError(t, err)

	require.NoError(t, authority.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
	reopened := openTestNetwork(t, root)
	reopenedUID, err := reopened.OpenUID(t.Context(), testUID)
	require.NoError(t, err)

	indexCtx, cancelIndex := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancelIndex()
	blockedIndex, err := reopened.AcquireIndex(indexCtx)
	assert.Nil(t, blockedIndex)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	lockCtx, cancelLock := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancelLock()
	blockedNetwork, err := reopenedUID.AcquireNetwork(lockCtx, key)
	assert.Nil(t, blockedNetwork)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	require.NoError(t, held.Release(t.Context()))
	require.NoError(t, index.Release(t.Context()))
	reacquired, err := reopenedUID.AcquireNetwork(t.Context(), key)
	require.NoError(t, err)
	require.NoError(t, reacquired.Release(t.Context()))
	require.NoError(t, reopenedUID.Release(t.Context()))
	require.NoError(t, reopened.Release(t.Context()))
}

func TestNetworkContendedKeyDoesNotBlockIndependentKeyOnSameCapability(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	deps := realDependencies()
	realWait := deps.waitLock
	var waitOnce sync.Once
	waitObserved := make(chan struct{})
	deps.waitLock = func(ctx context.Context) error {
		waitOnce.Do(func() { close(waitObserved) })
		return realWait(ctx)
	}
	authorityA, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	authorityB, err := openNetworkWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	uidA, err := authorityA.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	uidB, err := authorityB.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	heldKey, err := NewNetworkKey(testNetworkID)
	require.NoError(t, err)
	otherKey, err := NewNetworkKey(testAlternateNetworkID)
	require.NoError(t, err)
	held, err := uidA.AcquireNetwork(t.Context(), heldKey)
	require.NoError(t, err)

	type lockResult struct {
		lock *Lock
		err  error
	}
	waitCtx, cancelWait := context.WithCancel(t.Context())
	waitResult := make(chan lockResult, 1)
	go func() {
		lock, acquireErr := uidB.AcquireNetwork(waitCtx, heldKey)
		waitResult <- lockResult{lock: lock, err: acquireErr}
	}()
	select {
	case <-waitObserved:
	case <-time.After(time.Second):
		require.FailNow(t, "contended network acquisition did not reach lock wait")
	}

	otherResult := make(chan lockResult, 1)
	go func() {
		lock, acquireErr := uidB.AcquireNetwork(t.Context(), otherKey)
		otherResult <- lockResult{lock: lock, err: acquireErr}
	}()
	select {
	case result := <-otherResult:
		require.NoError(t, result.err)
		require.NotNil(t, result.lock)
		require.NoError(t, result.lock.Release(t.Context()))
	case <-time.After(100 * time.Millisecond):
		require.FailNow(t, "independent network key was blocked behind a contended key")
	}

	cancelWait()
	result := <-waitResult
	assert.Nil(t, result.lock)
	require.ErrorIs(t, result.err, context.Canceled)
	require.NoError(t, held.Release(t.Context()))
	require.NoError(t, uidB.Release(t.Context()))
	require.NoError(t, uidA.Release(t.Context()))
	require.NoError(t, authorityB.Release(t.Context()))
	require.NoError(t, authorityA.Release(t.Context()))
}

func TestNetworkRejectsUnsafeExistingLocksWithoutRepair(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		perUID bool
		mutate func(*testing.T, string)
	}{
		"index symlink": {
			mutate: func(t *testing.T, path string) { require.NoError(t, os.Symlink("attacker", path)) },
		},
		"index wrong mode": {
			mutate: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, nil, 0600))
				require.NoError(t, os.Chmod(path, 0644))
			},
		},
		"index nonempty": {
			mutate: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, []byte("x"), 0600))
			},
		},
		"network hard link": {
			perUID: true,
			mutate: func(t *testing.T, path string) {
				source := filepath.Join(filepath.Dir(path), "other.lock")
				require.NoError(t, os.WriteFile(source, nil, 0600))
				require.NoError(t, os.Link(source, path))
			},
		},
		"network wrong mode": {
			perUID: true,
			mutate: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, nil, 0600))
				require.NoError(t, os.Chmod(path, 0644))
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareTestRoot(t)
			authority := openTestNetwork(t, root)
			t.Cleanup(func() { require.NoError(t, authority.Release(context.Background())) })
			var uid *NetworkUID
			path := filepath.Join(root, "run/mvmctl/authority", networkIndexLockName)
			if tc.perUID {
				var err error
				uid, err = authority.OpenUID(t.Context(), testUID)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, uid.Release(context.Background())) })
				path = filepath.Join(
					root,
					"run/mvmctl/authority",
					formatUID(testUID),
					"network-"+testNetworkID+".lock",
				)
			}
			tc.mutate(t, path)

			var lock *Lock
			var err error
			if tc.perUID {
				key, keyErr := NewNetworkKey(testNetworkID)
				require.NoError(t, keyErr)
				lock, err = uid.AcquireNetwork(t.Context(), key)
			} else {
				lock, err = authority.AcquireIndex(t.Context())
			}
			assert.Nil(t, lock)
			require.Error(t, err)
			info, statErr := os.Lstat(path)
			require.NoError(t, statErr)
			if strings.Contains(name, "wrong mode") {
				assert.Equal(t, os.FileMode(0644), info.Mode().Perm())
			}
		})
	}
}

func TestNetworkUIDCreationFaultClosesRejectedDescriptor(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*dependencies, *networkState, *int){
		"owner": func(deps *dependencies, _ *networkState, targetFD *int) {
			real := deps.fchown
			deps.fchown = func(ctx context.Context, fd, uid, gid int) error {
				if fd == *targetFD {
					return errors.New("owner failed")
				}
				return real(ctx, fd, uid, gid)
			}
		},
		"mode": func(deps *dependencies, _ *networkState, targetFD *int) {
			real := deps.fchmod
			deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
				if fd == *targetFD {
					return errors.New("mode failed")
				}
				return real(ctx, fd, mode)
			}
		},
		"metadata": func(deps *dependencies, _ *networkState, targetFD *int) {
			real := deps.fstat
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if fd == *targetFD {
					return errors.New("metadata failed")
				}
				return real(ctx, fd, stat)
			}
		},
		"child sync": func(deps *dependencies, _ *networkState, targetFD *int) {
			real := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				if fd == *targetFD {
					return errors.New("child sync failed")
				}
				return real(ctx, fd)
			}
		},
		"parent sync": func(deps *dependencies, state *networkState, targetFD *int) {
			real := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				if *targetFD >= 0 && fd == state.authority {
					return errors.New("parent sync failed")
				}
				return real(ctx, fd)
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareTestRoot(t)
			authority := openTestNetwork(t, root)
			t.Cleanup(func() { require.NoError(t, authority.Release(context.Background())) })
			state := authority.state
			realOpenAt2 := state.deps.openAt2
			realClose := state.deps.close
			targetFD := -1
			closed := 0
			state.deps.openAt2 = func(
				ctx context.Context,
				parentFD int,
				entry string,
				how *unix.OpenHow,
			) (int, error) {
				fd, openErr := realOpenAt2(ctx, parentFD, entry, how)
				if openErr == nil && entry == formatUID(testUID) {
					targetFD = fd
				}
				return fd, openErr
			}
			state.deps.close = func(ctx context.Context, fd int) error {
				if fd == targetFD {
					closed++
				}
				return realClose(ctx, fd)
			}
			inject(&state.deps, state, &targetFD)

			uid, err := authority.OpenUID(t.Context(), testUID)
			assert.Nil(t, uid)
			require.Error(t, err)
			assert.GreaterOrEqual(t, targetFD, 0)
			assert.Equal(t, 1, closed)
		})
	}
}

func TestNetworkLockCreationFaultClosesRejectedDescriptor(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*dependencies, *networkUIDState, *int){
		"owner": func(deps *dependencies, _ *networkUIDState, lockFD *int) {
			real := deps.fchown
			deps.fchown = func(ctx context.Context, fd, uid, gid int) error {
				if fd == *lockFD {
					return errors.New("owner failed")
				}
				return real(ctx, fd, uid, gid)
			}
		},
		"mode": func(deps *dependencies, _ *networkUIDState, lockFD *int) {
			real := deps.fchmod
			deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
				if fd == *lockFD {
					return errors.New("mode failed")
				}
				return real(ctx, fd, mode)
			}
		},
		"metadata": func(deps *dependencies, _ *networkUIDState, lockFD *int) {
			real := deps.fstat
			deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if fd == *lockFD {
					return errors.New("metadata failed")
				}
				return real(ctx, fd, stat)
			}
		},
		"lock sync": func(deps *dependencies, _ *networkUIDState, lockFD *int) {
			real := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				if fd == *lockFD {
					return errors.New("lock sync failed")
				}
				return real(ctx, fd)
			}
		},
		"parent sync": func(deps *dependencies, state *networkUIDState, lockFD *int) {
			real := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				if *lockFD >= 0 && fd == state.fd {
					return errors.New("parent sync failed")
				}
				return real(ctx, fd)
			}
		},
		"flock": func(deps *dependencies, _ *networkUIDState, lockFD *int) {
			real := deps.flock
			deps.flock = func(ctx context.Context, fd, how int) error {
				if fd == *lockFD && how != unix.LOCK_UN {
					return errors.New("flock failed")
				}
				return real(ctx, fd, how)
			}
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareTestRoot(t)
			authority := openTestNetwork(t, root)
			t.Cleanup(func() { require.NoError(t, authority.Release(context.Background())) })
			uid, err := authority.OpenUID(t.Context(), testUID)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, uid.Release(context.Background())) })
			state := uid.state
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
				if openErr == nil && entry == "network-"+testNetworkID+".lock" {
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
			key, keyErr := NewNetworkKey(testNetworkID)
			require.NoError(t, keyErr)

			lock, err := uid.AcquireNetwork(t.Context(), key)
			assert.Nil(t, lock)
			require.Error(t, err)
			assert.GreaterOrEqual(t, lockFD, 0)
			assert.Equal(t, 1, closed)
		})
	}
}

func TestCopiedNetworkCapabilitiesCacheReleaseAndCloseOnce(t *testing.T) {
	t.Parallel()

	releaseErr := errors.New("release failed")
	type contextKey struct{}
	tests := map[string]func(*atomic.Int32, *atomic.Bool) (func(context.Context) error, func(context.Context) error){
		"network": func(calls *atomic.Int32, observed *atomic.Bool) (func(context.Context) error, func(context.Context) error) {
			original := Network{state: &networkState{
				deps: dependencies{close: func(ctx context.Context, _ int) error {
					calls.Add(1)
					observed.Store(ctx.Value(contextKey{}) == "retained")
					return releaseErr
				}},
				retained: []int{10}, active: true,
			}}
			copied := original
			return original.Release, copied.Release
		},
		"network UID": func(
			calls *atomic.Int32,
			observed *atomic.Bool,
		) (func(context.Context) error, func(context.Context) error) {
			original := NetworkUID{state: &networkUIDState{
				deps: dependencies{close: func(ctx context.Context, _ int) error {
					calls.Add(1)
					observed.Store(ctx.Value(contextKey{}) == "retained")
					return releaseErr
				}},
				fd: 10, active: true,
			}}
			copied := original
			return original.Release, copied.Release
		},
	}
	for name, build := range tests {
		t.Run(name, func(t *testing.T) {
			calls := &atomic.Int32{}
			observed := &atomic.Bool{}
			releaseOriginal, releaseCopy := build(calls, observed)
			ctx, cancel := context.WithCancel(context.WithValue(t.Context(), contextKey{}, "retained"))
			cancel()

			first := releaseOriginal(ctx)
			require.ErrorIs(t, first, releaseErr)
			second := releaseCopy(ctx)
			require.Same(t, first, second)
			assert.Equal(t, int32(1), calls.Load())
			assert.True(t, observed.Load())
		})
	}
}

func TestNetworkConcurrentCreationAndAcquireReleaseAreCoherent(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	const workers = 8
	start := make(chan struct{})
	errorsByWorker := make(chan error, workers)
	for range workers {
		go func() {
			<-start
			authority, err := openNetworkWithPolicy(t.Context(), realDependencies(), testPolicy(root))
			if err != nil {
				errorsByWorker <- err
				return
			}
			uid, err := authority.OpenUID(t.Context(), testUID)
			if err == nil {
				err = uid.Release(t.Context())
			}
			if releaseErr := authority.Release(t.Context()); err == nil {
				err = releaseErr
			}
			errorsByWorker <- err
		}()
	}
	close(start)
	for range workers {
		require.NoError(t, <-errorsByWorker)
	}

	authority := openTestNetwork(t, root)
	start = make(chan struct{})
	acquired := make(chan *Lock, 1)
	acquireErr := make(chan error, 1)
	releaseErr := make(chan error, 1)
	go func() {
		<-start
		lock, err := authority.AcquireIndex(t.Context())
		acquired <- lock
		acquireErr <- err
	}()
	go func() {
		<-start
		releaseErr <- authority.Release(t.Context())
	}()
	close(start)
	lock := <-acquired
	err := <-acquireErr
	require.NoError(t, <-releaseErr)
	if lock == nil {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "released")
		return
	}
	require.NoError(t, err)
	require.NoError(t, lock.Release(t.Context()))
}

func TestNetworkCapabilitiesFailClosedWhenMissingOrReleased(t *testing.T) {
	t.Parallel()

	key, err := NewNetworkKey(testNetworkID)
	require.NoError(t, err)
	var zero Network
	index, err := zero.AcquireIndex(t.Context())
	assert.Nil(t, index)
	require.Error(t, err)
	uid, err := zero.OpenUID(t.Context(), testUID)
	assert.Nil(t, uid)
	require.Error(t, err)
	require.NoError(t, zero.Release(t.Context()))
	var nilAuthority *Network
	index, err = nilAuthority.AcquireIndex(t.Context())
	assert.Nil(t, index)
	require.Error(t, err)
	uid, err = nilAuthority.OpenUID(t.Context(), testUID)
	assert.Nil(t, uid)
	require.Error(t, err)

	var zeroUID NetworkUID
	lock, err := zeroUID.AcquireNetwork(t.Context(), key)
	assert.Nil(t, lock)
	require.Error(t, err)
	require.NoError(t, zeroUID.Release(t.Context()))
	var nilUID *NetworkUID
	lock, err = nilUID.AcquireNetwork(t.Context(), key)
	assert.Nil(t, lock)
	require.Error(t, err)

	root := prepareTestRoot(t)
	authority := openTestNetwork(t, root)
	rejectedUID, err := authority.OpenUID(t.Context(), 0)
	assert.Nil(t, rejectedUID)
	require.Error(t, err)
	activeUID, err := authority.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	require.NoError(t, activeUID.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))
	index, err = authority.AcquireIndex(t.Context())
	assert.Nil(t, index)
	require.Error(t, err)
	lock, err = activeUID.AcquireNetwork(t.Context(), key)
	assert.Nil(t, lock)
	require.Error(t, err)
}

func openTestNetwork(t *testing.T, root string) *Network {
	t.Helper()
	authority, err := openNetworkWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	return authority
}
