package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

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
	require.NoError(t, os.Chmod(root, os.FileMode(managedVMCgroupRootMode)))
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
	err = unix.Unlinkat(parentFD, name, flags)
	if err != nil {
		require.NoError(fixture.t, os.WriteFile(membershipPath, membership, 0o644))
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

func TestPreparedManagedVMCgroupBindsExactJailerLeafMembership(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
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

func TestManagedVMCgroupRemoveConsumesCancellationIndependently(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	lease := fixture.bindLease(t, fixture.deps())
	fixture.setMembership(testVMID, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, lease.Remove(ctx))
	require.NoError(t, lease.Release(ctx))
	assert.False(t, fixture.vmLeafPresent(testVMID))
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

// Rationale: once a safely admitted Jailer-created leaf is pinned, later process-proof failure must return that exact
// cleanup capability instead of leaving an untracked cgroup behind.
func TestPreparedManagedVMCgroupReturnsOwnedLeaseAfterPartialBindFailure(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
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
			require.NoError(t, os.Chmod(fixture.vmLeafPath(testVMID), 0o700))
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
