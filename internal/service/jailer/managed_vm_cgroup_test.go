package jailer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

type managedVMCgroupTestFixture struct {
	t    *testing.T
	root string
}

func newManagedVMCgroupTestFixture(t *testing.T) *managedVMCgroupTestFixture {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o755))
	return &managedVMCgroupTestFixture{t: t, root: root}
}

func (fixture *managedVMCgroupTestFixture) deps() managedVMCgroupDeps {
	fixture.t.Helper()
	deps := realManagedVMCgroupDeps()
	deps.trustedUID = uint32(os.Geteuid())
	deps.trustedGID = uint32(os.Getegid())
	deps.openRoot = func(context.Context) (int, error) {
		return unix.Open(fixture.root, managedVMCgroupDirectoryFlags, 0)
	}
	deps.statfs = func(context.Context, int, *unix.Statfs_t) error { return nil }
	realStatfs := deps.statfs
	deps.statfs = func(ctx context.Context, fd int, stat *unix.Statfs_t) error {
		if err := realStatfs(ctx, fd, stat); err != nil {
			return err
		}
		stat.Type = unix.CGROUP2_SUPER_MAGIC
		return nil
	}
	deps.unlinkAt = fixture.removeCgroupLeaf
	return deps
}

func (fixture *managedVMCgroupTestFixture) launchDeps() managedVMCgroupDeps {
	fixture.t.Helper()
	createManagedVMCgroupControllerFiles(
		fixture.t,
		fixture.root,
		"cpu memory pids\n",
		"cpu memory pids\n",
	)
	deps := fixture.deps()
	realMkdirAt := deps.mkdirAt
	deps.mkdirAt = func(ctx context.Context, rootFD int, name string, mode uint32) error {
		if err := realMkdirAt(ctx, rootFD, name, mode); err != nil {
			return err
		}
		if name == infra.JailerCgroupParent {
			createManagedVMCgroupControllerFiles(
				fixture.t,
				filepath.Join(fixture.root, infra.JailerCgroupParent),
				"cpu memory pids\n",
				"cpu memory pids\n",
			)
		}
		return nil
	}
	return deps
}

func (fixture *managedVMCgroupTestFixture) removeCgroupLeaf(
	_ context.Context,
	parentFD int,
	name string,
	flags int,
) error {
	fixture.t.Helper()
	parent, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(parentFD))
	require.NoError(fixture.t, err)
	membershipPath := filepath.Join(parent, name, "cgroup.procs")
	membership, err := os.ReadFile(membershipPath)
	require.NoError(fixture.t, err)
	require.NoError(fixture.t, os.Remove(membershipPath))
	controlContents := make(map[string][]byte)
	for _, control := range []string{
		"cpu.max", "cpu.weight", "memory.high", "memory.max", "memory.swap.max", "pids.max",
	} {
		path := filepath.Join(parent, name, control)
		content, readErr := os.ReadFile(path)
		if readErr == nil {
			controlContents[path] = content
		} else if !errors.Is(readErr, os.ErrNotExist) {
			require.NoError(fixture.t, readErr)
		}
		removeErr := os.Remove(path)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			require.NoError(fixture.t, removeErr)
		}
	}
	err = unix.Unlinkat(parentFD, name, flags)
	if err != nil {
		require.NoError(fixture.t, os.WriteFile(membershipPath, membership, 0o644))
		for path, content := range controlContents {
			require.NoError(fixture.t, os.WriteFile(path, content, 0o644))
		}
	}
	return err
}

func (fixture *managedVMCgroupTestFixture) addVMLeaf(vmID string) {
	fixture.t.Helper()
	parent := filepath.Join(fixture.root, "mvmctl")
	require.NoError(fixture.t, os.MkdirAll(parent, os.FileMode(managedVMCgroupParentMode)))
	require.NoError(fixture.t, os.Chmod(parent, os.FileMode(managedVMCgroupParentMode)))
	require.NoError(fixture.t, os.Mkdir(filepath.Join(parent, vmID), os.FileMode(managedVMCgroupLeafMode)))
	fixture.setMembership(vmID, "4242\n")
}

func (fixture *managedVMCgroupTestFixture) setMembership(vmID string, membership string) {
	fixture.t.Helper()
	require.NoError(fixture.t, os.WriteFile(
		filepath.Join(fixture.vmLeafPath(vmID), "cgroup.procs"),
		[]byte(membership),
		0o644,
	))
}

func (fixture *managedVMCgroupTestFixture) vmLeafPath(vmID string) string {
	fixture.t.Helper()
	return filepath.Join(fixture.root, "mvmctl", vmID)
}

func (fixture *managedVMCgroupTestFixture) vmLeafPresent(vmID string) bool {
	fixture.t.Helper()
	_, err := os.Lstat(fixture.vmLeafPath(vmID))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	require.NoError(fixture.t, err)
	return true
}

func (fixture *managedVMCgroupTestFixture) bindLease(t *testing.T, deps managedVMCgroupDeps) *managedVMCgroupLease {
	t.Helper()
	prepared, err := prepareManagedVMCgroup(t.Context(), deps, testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	lease, err := prepared.Bind(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		&managedVMCgroupTestProcessVerifier{},
	)
	require.NoError(t, err)
	return lease
}

func TestPrepareManagedVMCgroupProvesDerivedLeafAbsentWithoutCreating(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, prepared.Release(t.Context())) })

	assert.False(t, fixture.vmLeafPresent(testVMID))
	assert.True(t, prepared.snapshot().leafAbsent)
}

// A cgroup-v2 mount can expose a root-owned 0555 mount root while the mount is
// writable. The root mode must not be confused with the managed child policy.
func TestPrepareManagedVMCgroupAcceptsReadOnlyModeOnCgroupRoot(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	deps := fixture.deps()
	openRoot := deps.openRoot
	rootFD := -1
	deps.openRoot = func(ctx context.Context) (int, error) {
		fd, err := openRoot(ctx)
		if err == nil {
			rootFD = fd
		}
		return fd, err
	}
	statx := deps.statx
	deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
		if err := statx(ctx, fd, stat); err != nil {
			return err
		}
		if fd == rootFD {
			stat.Mode = stat.Mode&^0o7777 | 0o555
		}
		return nil
	}

	prepared, err := prepareManagedVMCgroup(t.Context(), deps, testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.Release(t.Context()))
}

// Rationale: a same-name cgroup can contain authority from an interrupted or foreign lifecycle. A fresh launch must
// never adopt it, even when its metadata looks otherwise admissible.
func TestPrepareManagedVMCgroupRejectsPreexistingLeafWithoutAdoption(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)

	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	assert.Nil(t, prepared)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(err).Code)
	assert.True(t, fixture.vmLeafPresent(testVMID))
}

func TestPrepareManagedVMCgroupRejectsUnsafeRootOrParent(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*managedVMCgroupTestFixture, *managedVMCgroupDeps){
		"root owner": func(_ *managedVMCgroupTestFixture, deps *managedVMCgroupDeps) {
			realStatx := deps.statx
			deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, stat); err != nil {
					return err
				}
				stat.Uid++
				return nil
			}
		},
		"root mode": func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
			require.NoError(t, os.Chmod(fixture.root, 0o750))
		},
		"filesystem type": func(_ *managedVMCgroupTestFixture, deps *managedVMCgroupDeps) {
			deps.statfs = func(context.Context, int, *unix.Statfs_t) error { return nil }
		},
		"parent symlink": func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
			require.NoError(t, os.Symlink("elsewhere", filepath.Join(fixture.root, infra.JailerCgroupParent)))
		},
		"parent mount identity": func(_ *managedVMCgroupTestFixture, deps *managedVMCgroupDeps) {
			realOpenAt2 := deps.openAt2
			realStatx := deps.statx
			parentFD := -1
			deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
				opened, err := realOpenAt2(ctx, fd, name, how)
				if err == nil && name == infra.JailerCgroupParent {
					parentFD = opened
				}
				return opened, err
			}
			deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, stat); err != nil {
					return err
				}
				if fd == parentFD {
					stat.Mnt_id++
				}
				return nil
			}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			deps := fixture.deps()
			mutate(fixture, &deps)

			prepared, err := prepareManagedVMCgroup(t.Context(), deps, testVMID)
			assert.Nil(t, prepared)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestPrepareManagedVMCgroupNormalizesOnlyFreshUmaskFilteredParent(t *testing.T) {
	t.Parallel()

	t.Run("fresh parent", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		deps := fixture.deps()
		deps.mkdirAt = func(_ context.Context, parentFD int, name string, _ uint32) error {
			return unix.Mkdirat(parentFD, name, 0o700)
		}
		realFchown := deps.fchown
		fchownCalls := 0
		initialUID := uint32(0)
		initialGID := uint32(0)
		gotUID := -1
		gotGID := -1
		deps.fchown = func(ctx context.Context, fd int, uid int, gid int) error {
			var stat unix.Stat_t
			require.NoError(t, unix.Fstat(fd, &stat))
			fchownCalls++
			initialUID = stat.Uid
			initialGID = stat.Gid
			gotUID = uid
			gotGID = gid
			return realFchown(ctx, fd, uid, gid)
		}

		prepared, err := prepareManagedVMCgroup(t.Context(), deps, testVMID)
		require.NoError(t, err)
		require.NoError(t, prepared.Release(t.Context()))
		assert.Equal(t, 1, fchownCalls)
		assert.Equal(t, deps.trustedUID, initialUID)
		assert.Equal(t, deps.trustedGID, initialGID)
		assert.Equal(t, int(deps.trustedUID), gotUID)
		assert.Equal(t, int(deps.trustedGID), gotGID)
		info, err := os.Lstat(filepath.Join(fixture.root, infra.JailerCgroupParent))
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(managedVMCgroupParentMode), info.Mode().Perm())
	})

	t.Run("preexisting parent", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		parentPath := filepath.Join(fixture.root, infra.JailerCgroupParent)
		require.NoError(t, os.Mkdir(parentPath, 0o700))
		deps := fixture.deps()
		realOpenAt2 := deps.openAt2
		parentOpenCalls := 0
		deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			if name == infra.JailerCgroupParent {
				parentOpenCalls++
				if parentOpenCalls == 1 {
					return -1, unix.ENOENT
				}
			}
			return realOpenAt2(ctx, parentFD, name, how)
		}
		deps.mkdirAt = func(context.Context, int, string, uint32) error { return unix.EEXIST }
		fchownCalls := 0
		deps.fchown = func(context.Context, int, int, int) error {
			fchownCalls++
			return nil
		}

		prepared, err := prepareManagedVMCgroup(t.Context(), deps, testVMID)
		assert.Nil(t, prepared)
		require.Error(t, err)
		assert.Equal(t, 0, fchownCalls)
		info, err := os.Lstat(parentPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	})
}

func TestPreparedManagedVMCgroupBindsExactJailerLeafMembership(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	verifier := &managedVMCgroupTestProcessVerifier{}

	lease, err := prepared.Bind(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		verifier,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	fixture.setMembership(testVMID, "")
	t.Cleanup(func() {
		require.NoError(t, lease.Remove(t.Context()))
		require.NoError(t, lease.Release(t.Context()))
	})
	assert.True(t, prepared.snapshot().released)
	assert.Equal(t, 1, verifier.beforeCalls)
	assert.Equal(t, 1, verifier.afterCalls)
}

func TestManagedVMCgroupRemoveKeepsNonemptyAndBusyLeafRetryable(t *testing.T) {
	t.Parallel()

	t.Run("nonempty", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		lease := fixture.bindLease(t, fixture.deps())

		err := lease.Remove(t.Context())
		require.Error(t, err)
		assert.True(t, fixture.vmLeafPresent(testVMID))
		assert.False(t, lease.snapshot().removed)

		fixture.setMembership(testVMID, "")
		require.NoError(t, lease.Remove(t.Context()))
		require.NoError(t, lease.Release(t.Context()))
		assert.False(t, fixture.vmLeafPresent(testVMID))
	})

	t.Run("busy", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		deps := fixture.deps()
		realUnlinkAt := deps.unlinkAt
		calls := 0
		deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
			calls++
			if calls == 1 {
				return unix.EBUSY
			}
			return realUnlinkAt(ctx, parentFD, name, flags)
		}
		lease := fixture.bindLease(t, deps)
		fixture.setMembership(testVMID, "")

		err := lease.Remove(t.Context())
		require.ErrorIs(t, err, unix.EBUSY)
		assert.True(t, fixture.vmLeafPresent(testVMID))
		assert.False(t, lease.snapshot().removed)

		require.NoError(t, lease.Remove(t.Context()))
		require.NoError(t, lease.Release(t.Context()))
		assert.Equal(t, 2, calls)
	})
}

// Rationale: an unlink wrapper can report failure after the exact leaf was removed and a same-name replacement was
// installed. Reconciliation may retire the original capability, but must never remove the replacement on retry.
func TestManagedVMCgroupRemoveNeverRemovesReplacementAfterEffectfulError(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	deps := fixture.deps()
	realUnlinkAt := deps.unlinkAt
	replaced := false
	deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		if replaced {
			return realUnlinkAt(ctx, parentFD, name, flags)
		}
		replaced = true
		require.NoError(t, realUnlinkAt(ctx, parentFD, name, flags))
		fixture.addVMLeaf(testVMID)
		fixture.setMembership(testVMID, "7777\n")
		return unix.EBUSY
	}
	lease := fixture.bindLease(t, deps)
	fixture.setMembership(testVMID, "")

	err := lease.Remove(t.Context())
	require.ErrorIs(t, err, unix.EBUSY)
	assert.True(t, fixture.vmLeafPresent(testVMID))
	assert.True(t, lease.snapshot().removed)

	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
	assert.True(t, fixture.vmLeafPresent(testVMID))
}

// Rationale: cleanup may unlink the exact leaf and then lose its first reconciliation observation. A later retry must
// use the retained descriptor's zero link count to recognize the completed effect instead of becoming permanently
// stuck on ENOENT.
func TestManagedVMCgroupRemoveRetriesReconciliationAfterEffectfulUnlink(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	lease := fixture.bindLease(t, fixture.deps())
	fixture.setMembership(testVMID, "")
	realOpenAt2 := lease.state.deps.openAt2
	realUnlinkAt := lease.state.deps.unlinkAt
	unlinked := false
	reconcileAttempts := 0
	transientErr := errors.New("transient reconciliation failure")
	lease.state.deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		if unlinked && name == testVMID {
			reconcileAttempts++
			if reconcileAttempts == 1 {
				return -1, transientErr
			}
		}
		return realOpenAt2(ctx, parentFD, name, how)
	}
	lease.state.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		require.NoError(t, realUnlinkAt(ctx, parentFD, name, flags))
		unlinked = true
		return unix.EBUSY
	}

	err := lease.Remove(t.Context())
	require.ErrorIs(t, err, unix.EBUSY)
	require.ErrorIs(t, err, transientErr)
	assert.False(t, lease.snapshot().removed)

	require.NoError(t, lease.Remove(t.Context()))
	assert.True(t, lease.snapshot().removed)
	require.NoError(t, lease.Release(t.Context()))
}

func TestManagedVMCgroupRemoveConsumesCancellationIndependently(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	lease := fixture.bindLease(t, fixture.deps())
	fixture.setMembership(testVMID, "")
	realPread := lease.state.deps.pread
	realUnlinkAt := lease.state.deps.unlinkAt
	lease.state.deps.pread = func(ctx context.Context, fd int, buffer []byte, offset int64) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, fmt.Errorf("pread received cancelled cleanup context: %w", err)
		}
		return realPread(ctx, fd, buffer, offset)
	}
	lease.state.deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("unlink received cancelled cleanup context: %w", err)
		}
		return realUnlinkAt(ctx, parentFD, name, flags)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, lease.Remove(ctx))
	require.NoError(t, lease.Release(ctx))
	assert.False(t, fixture.vmLeafPresent(testVMID))
}

func TestPreparedManagedVMCgroupBindDoesNotHoldPreparedLockDuringProcessProof(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	verifier := &reentrantManagedVMCgroupTestVerifier{prepared: prepared}
	result := make(chan struct {
		lease *managedVMCgroupLease
		err   error
	}, 1)
	go func() {
		lease, bindErr := prepared.Bind(
			t.Context(),
			processTestLocator(),
			processTestCgroupObservation(),
			verifier,
		)
		result <- struct {
			lease *managedVMCgroupLease
			err   error
		}{lease: lease, err: bindErr}
	}()

	select {
	case got := <-result:
		require.NoError(t, got.err)
		require.NotNil(t, got.lease)
		fixture.setMembership(testVMID, "")
		require.NoError(t, got.lease.Remove(t.Context()))
		require.NoError(t, got.lease.Release(t.Context()))
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Bind deadlocked while the process verifier re-entered prepared state")
	}
}

// Rationale: cgroup.procs is a dynamic pseudo-file and a short first read is not EOF. A later process must be
// included in the membership proof rather than hidden behind the short read.
func TestPreparedManagedVMCgroupBindRejectsMemberHiddenAfterShortRead(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	realPread := prepared.state.deps.pread
	chunks := map[int64][]byte{0: []byte("4242\n"), 5: []byte("4343\n")}
	prepared.state.deps.pread = func(_ context.Context, _ int, buffer []byte, offset int64) (int, error) {
		chunk := chunks[offset]
		return copy(buffer, chunk), nil
	}

	lease, err := prepared.Bind(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		&managedVMCgroupTestProcessVerifier{},
	)
	lease.state.deps.pread = realPread
	if err == nil {
		fixture.setMembership(testVMID, "")
		require.NoError(t, lease.Remove(t.Context()))
		require.NoError(t, lease.Release(t.Context()))
		t.Error("Bind accepted a second cgroup member hidden after a short first read")
		return
	}
	require.NotNil(t, lease)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestManagedVMCgroupCopiedConcurrentReleaseClosesOnceAndCachesFirstError(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	lease := fixture.bindLease(t, fixture.deps())
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))

	closeErr := errors.New("retained descriptor close failed")
	realClose := lease.state.deps.close
	retained := make(map[int]bool, len(lease.state.retained))
	for _, fd := range lease.state.retained {
		retained[fd] = true
	}
	var mu sync.Mutex
	closed := make(map[int]int, len(retained))
	lease.state.deps.close = func(ctx context.Context, fd int) error {
		mu.Lock()
		if retained[fd] {
			closed[fd]++
		}
		mu.Unlock()
		err := realClose(ctx, fd)
		if fd == lease.state.leafFD {
			return errors.Join(err, closeErr)
		}
		return err
	}
	copied := *lease
	results := make(chan error, 16)
	var wait sync.WaitGroup
	for index := 0; index < cap(results); index++ {
		wait.Add(1)
		go func(useCopy bool) {
			defer wait.Done()
			if useCopy {
				results <- copied.Release(context.Background())
				return
			}
			results <- lease.Release(context.Background())
		}(index%2 == 0)
	}
	wait.Wait()
	close(results)
	for err := range results {
		require.ErrorIs(t, err, closeErr)
	}
	for fd := range retained {
		assert.Equal(t, 1, closed[fd])
	}
}

func TestPreparedManagedVMCgroupCopiedConcurrentReleaseClosesOnceAndCachesFirstError(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	closeErr := errors.New("prepared descriptor close failed")
	realClose := prepared.state.deps.close
	retained := make(map[int]bool, len(prepared.state.retained))
	for _, fd := range prepared.state.retained {
		retained[fd] = true
	}
	var mu sync.Mutex
	closed := make(map[int]int, len(retained))
	prepared.state.deps.close = func(ctx context.Context, fd int) error {
		mu.Lock()
		if retained[fd] {
			closed[fd]++
		}
		mu.Unlock()
		err := realClose(ctx, fd)
		if fd == prepared.state.parentFD {
			return errors.Join(err, closeErr)
		}
		return err
	}
	copied := *prepared
	results := make(chan error, 16)
	var wait sync.WaitGroup
	for index := 0; index < cap(results); index++ {
		wait.Add(1)
		go func(useCopy bool) {
			defer wait.Done()
			if useCopy {
				results <- copied.Release(context.Background())
				return
			}
			results <- prepared.Release(context.Background())
		}(index%2 == 0)
	}
	wait.Wait()
	close(results)
	for err := range results {
		require.ErrorIs(t, err, closeErr)
	}
	for fd := range retained {
		assert.Equal(t, 1, closed[fd])
	}
}

func TestManagedVMCgroupReleaseConsumesCancellationIndependently(t *testing.T) {
	t.Parallel()

	t.Run("prepared", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
		require.NoError(t, err)
		realClose := prepared.state.deps.close
		prepared.state.deps.close = func(ctx context.Context, fd int) error {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("prepared close received cancelled context: %w", err)
			}
			return realClose(ctx, fd)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		require.NoError(t, prepared.Release(ctx))
		assert.True(t, prepared.snapshot().released)
	})

	t.Run("bound", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		lease := fixture.bindLease(t, fixture.deps())
		fixture.setMembership(testVMID, "")
		require.NoError(t, lease.Remove(t.Context()))
		realClose := lease.state.deps.close
		lease.state.deps.close = func(ctx context.Context, fd int) error {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("bound close received cancelled context: %w", err)
			}
			return realClose(ctx, fd)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		require.NoError(t, lease.Release(ctx))
		assert.True(t, lease.snapshot().released)
	})
}

func TestPreparedManagedVMCgroupBindHandlesBoundedMembershipReads(t *testing.T) {
	t.Parallel()

	t.Run("retries EINTR", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
		require.NoError(t, err)
		require.NoError(t, prepared.MarkJailerHandoff())
		fixture.addVMLeaf(testVMID)
		realPread := prepared.state.deps.pread
		calls := 0
		prepared.state.deps.pread = func(ctx context.Context, fd int, buffer []byte, offset int64) (int, error) {
			calls++
			if calls == 1 {
				return 0, unix.EINTR
			}
			return realPread(ctx, fd, buffer, offset)
		}

		lease, err := prepared.Bind(
			t.Context(),
			processTestLocator(),
			processTestCgroupObservation(),
			&managedVMCgroupTestProcessVerifier{},
		)
		require.NoError(t, err)
		require.NotNil(t, lease)
		lease.state.deps.pread = realPread
		fixture.setMembership(testVMID, "")
		require.NoError(t, lease.Remove(t.Context()))
		require.NoError(t, lease.Release(t.Context()))
	})

	t.Run("rejects cumulative maximum plus one", func(t *testing.T) {
		fixture := newManagedVMCgroupTestFixture(t)
		prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
		require.NoError(t, err)
		require.NoError(t, prepared.MarkJailerHandoff())
		fixture.addVMLeaf(testVMID)
		realPread := prepared.state.deps.pread
		totalRead := 0
		prepared.state.deps.pread = func(_ context.Context, _ int, buffer []byte, offset int64) (int, error) {
			remaining := managedVMCgroupMaxProcs + 1 - int(offset)
			if remaining <= 0 {
				return 0, nil
			}
			count := min(len(buffer), remaining)
			for index := range buffer[:count] {
				buffer[index] = '1'
			}
			totalRead += count
			return count, nil
		}

		lease, err := prepared.Bind(
			t.Context(),
			processTestLocator(),
			processTestCgroupObservation(),
			&managedVMCgroupTestProcessVerifier{},
		)
		require.Error(t, err)
		require.NotNil(t, lease)
		assert.Equal(t, managedVMCgroupMaxProcs+1, totalRead)
		lease.state.deps.pread = realPread
		fixture.setMembership(testVMID, "")
		require.NoError(t, lease.Remove(t.Context()))
		require.NoError(t, lease.Release(t.Context()))
	})

	tests := map[string]int{
		"negative count":  -1,
		"oversized count": managedVMCgroupMaxProcs + 2,
	}
	for name, count := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
			require.NoError(t, err)
			require.NoError(t, prepared.MarkJailerHandoff())
			fixture.addVMLeaf(testVMID)
			realPread := prepared.state.deps.pread
			prepared.state.deps.pread = func(context.Context, int, []byte, int64) (int, error) {
				return count, nil
			}
			var lease *managedVMCgroupLease
			var bindErr error
			assert.NotPanics(t, func() {
				lease, bindErr = prepared.Bind(
					t.Context(),
					processTestLocator(),
					processTestCgroupObservation(),
					&managedVMCgroupTestProcessVerifier{},
				)
			})
			require.Error(t, bindErr)
			require.NotNil(t, lease)
			lease.state.deps.pread = realPread
			fixture.setMembership(testVMID, "")
			require.NoError(t, lease.Remove(t.Context()))
			require.NoError(t, lease.Release(t.Context()))
		})
	}
}

// Rationale: once a safely admitted Jailer-created leaf is pinned, later process-proof failure must return that exact
// cleanup capability instead of leaving an untracked cgroup behind.
func TestPreparedManagedVMCgroupReturnsOwnedLeaseAfterPartialBindFailure(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	proofErr := errors.New("retained pidfd locator changed")
	verifier := &managedVMCgroupTestProcessVerifier{afterErr: proofErr}

	lease, err := prepared.Bind(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		verifier,
	)
	require.ErrorIs(t, err, proofErr)
	require.NotNil(t, lease)
	assert.True(t, prepared.snapshot().released)
	assert.Equal(t, 1, verifier.beforeCalls)
	assert.Equal(t, 1, verifier.afterCalls)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestPreparedManagedVMCgroupRejectsUnsafeJailerLeaf(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*managedVMCgroupTestFixture, *managedVMCgroupDeps){
		"mode": func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
			fixture.addVMLeaf(testVMID)
			require.NoError(t, os.Chmod(fixture.vmLeafPath(testVMID), 0o755))
		},
		"symlink": func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
			require.NoError(t, os.Symlink("elsewhere", fixture.vmLeafPath(testVMID)))
		},
		"mount identity": func(fixture *managedVMCgroupTestFixture, deps *managedVMCgroupDeps) {
			fixture.addVMLeaf(testVMID)
			realOpenAt2 := deps.openAt2
			realStatx := deps.statx
			leafFD := -1
			deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
				opened, err := realOpenAt2(ctx, fd, name, how)
				if err == nil && name == testVMID {
					leafFD = opened
				}
				return opened, err
			}
			deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, stat); err != nil {
					return err
				}
				if fd == leafFD {
					stat.Mnt_id++
				}
				return nil
			}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			deps := fixture.deps()
			prepared, err := prepareManagedVMCgroup(t.Context(), deps, testVMID)
			require.NoError(t, err)
			require.NoError(t, prepared.MarkJailerHandoff())
			t.Cleanup(func() { require.NoError(t, prepared.Release(t.Context())) })
			mutate(fixture, &deps)
			prepared.state.deps = deps

			lease, err := prepared.Bind(
				t.Context(),
				processTestLocator(),
				processTestCgroupObservation(),
				&managedVMCgroupTestProcessVerifier{},
			)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.False(t, prepared.snapshot().released)
		})
	}
}

// Rationale: cgroup.procs is independently security-sensitive. A safe leaf directory cannot lend authority to a
// membership pseudo-file with substituted type, ownership, mode, or filesystem identity.
func TestPreparedManagedVMCgroupRejectsUnsafeMembershipControlMetadata(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*unix.Statx_t){
		"type":  func(stat *unix.Statx_t) { stat.Mode = stat.Mode&^unix.S_IFMT | unix.S_IFDIR },
		"mode":  func(stat *unix.Statx_t) { stat.Mode = stat.Mode&^0o7777 | 0o600 },
		"group": func(stat *unix.Statx_t) { stat.Gid++ },
		"mount identity": func(stat *unix.Statx_t) {
			stat.Mnt_id++
		},
		"device major": func(stat *unix.Statx_t) { stat.Dev_major++ },
		"device minor": func(stat *unix.Statx_t) { stat.Dev_minor++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			deps := fixture.deps()
			membershipFD := -1
			realOpenAt2 := deps.openAt2
			deps.openAt2 = func(ctx context.Context, fd int, child string, how *unix.OpenHow) (int, error) {
				opened, err := realOpenAt2(ctx, fd, child, how)
				if err == nil && child == "cgroup.procs" {
					membershipFD = opened
				}
				return opened, err
			}
			realStatx := deps.statx
			deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, stat); err != nil {
					return err
				}
				if fd == membershipFD {
					mutate(stat)
				}
				return nil
			}
			prepared, err := prepareManagedVMCgroup(t.Context(), deps, testVMID)
			require.NoError(t, err)
			require.NoError(t, prepared.MarkJailerHandoff())
			fixture.addVMLeaf(testVMID)

			lease, err := prepared.Bind(
				t.Context(),
				processTestLocator(),
				processTestCgroupObservation(),
				&managedVMCgroupTestProcessVerifier{},
			)
			require.Error(t, err)
			require.NotNil(t, lease)
			lease.state.deps.statx = realStatx
			fixture.setMembership(testVMID, "")
			require.NoError(t, lease.Remove(t.Context()))
			require.NoError(t, lease.Release(t.Context()))
		})
	}
}

func TestPreparedManagedVMCgroupRejectsLocatorMembershipMismatchAndPIDReuse(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		locator     instanceProcessLocator
		observation processObservation
		membership  string
		afterErr    error
	}{
		"locator start ticks": {
			locator:     instanceProcessLocator{pid: processTestFirecrackerPID, startTimeTicks: 1},
			observation: processTestCgroupObservation(),
			membership:  "4242\n",
		},
		"derived cgroup": {
			locator: processTestLocator(),
			observation: processObservation{
				startTimeTicks: processTestFirecrackerTicks,
				cgroupPath:     infra.CgroupV2Root + "/foreign/" + testVMID,
			},
			membership: "4242\n",
		},
		"numeric membership": {
			locator:     processTestLocator(),
			observation: processTestCgroupObservation(),
			membership:  "4343\n",
		},
		"multiple members": {
			locator:     processTestLocator(),
			observation: processTestCgroupObservation(),
			membership:  "4242\n4343\n",
		},
		"malformed member": {
			locator:     processTestLocator(),
			observation: processTestCgroupObservation(),
			membership:  "04242\n",
		},
		"pid reuse after membership read": {
			locator:     processTestLocator(),
			observation: processTestCgroupObservation(),
			membership:  "4242\n",
			afterErr:    errors.New("retained pidfd now resolves to reused PID task"),
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
			require.NoError(t, err)
			require.NoError(t, prepared.MarkJailerHandoff())
			fixture.addVMLeaf(testVMID)
			fixture.setMembership(testVMID, tc.membership)

			lease, err := prepared.Bind(
				t.Context(),
				tc.locator,
				tc.observation,
				&managedVMCgroupTestProcessVerifier{afterErr: tc.afterErr},
			)
			require.Error(t, err)
			require.NotNil(t, lease)
			assert.True(t, prepared.snapshot().released)
			fixture.setMembership(testVMID, "")
			require.NoError(t, lease.Remove(t.Context()))
			require.NoError(t, lease.Release(t.Context()))
		})
	}
}

type managedVMCgroupTestProcessVerifier struct {
	mu          sync.Mutex
	beforeErr   error
	afterErr    error
	beforeCalls int
	afterCalls  int
}

type reentrantManagedVMCgroupTestVerifier struct {
	prepared *preparedManagedVMCgroup
}

func (verifier *reentrantManagedVMCgroupTestVerifier) verifyBeforeCgroupMembership(
	context.Context,
	instanceProcessLocator,
	processObservation,
) error {
	if !verifier.prepared.snapshot().released {
		return errors.New("prepared authority remained active after leaf admission")
	}
	return nil
}

func (verifier *reentrantManagedVMCgroupTestVerifier) verifyAfterCgroupMembership(
	context.Context,
	instanceProcessLocator,
	processObservation,
) error {
	return verifier.prepared.Release(context.Background())
}

func (verifier *managedVMCgroupTestProcessVerifier) verifyBeforeCgroupMembership(
	context.Context,
	instanceProcessLocator,
	processObservation,
) error {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.beforeCalls++
	return verifier.beforeErr
}

func (verifier *managedVMCgroupTestProcessVerifier) verifyAfterCgroupMembership(
	context.Context,
	instanceProcessLocator,
	processObservation,
) error {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	verifier.afterCalls++
	return verifier.afterErr
}

func processTestLocator() instanceProcessLocator {
	return instanceProcessLocator{pid: processTestFirecrackerPID, startTimeTicks: processTestFirecrackerTicks}
}

func processTestCgroupObservation() processObservation {
	return processObservation{
		startTimeTicks: processTestFirecrackerTicks,
		cgroupPath:     infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + testVMID,
	}
}
