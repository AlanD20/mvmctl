package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRecoverManagedVMCgroupMissingHierarchyIsAbsentWithoutMutation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		addParent bool
	}{
		"parent": {},
		"leaf":   {addParent: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			if tc.addParent {
				parent := filepath.Join(fixture.root, "mvmctl")
				require.NoError(t, os.Mkdir(parent, os.FileMode(managedVMCgroupParentMode)))
			}
			probe := &managedVMCgroupRecoveryMutationProbe{}
			deps := probe.deps(fixture)
			opened := map[int]int{}
			closed := map[int]int{}
			realOpenRoot := deps.openRoot
			deps.openRoot = func(ctx context.Context) (int, error) {
				fd, err := realOpenRoot(ctx)
				if err == nil {
					opened[fd]++
				}
				return fd, err
			}
			realOpenAt2 := deps.openAt2
			deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
				openedFD, err := realOpenAt2(ctx, fd, name, how)
				if err == nil {
					opened[openedFD]++
				}
				return openedFD, err
			}
			realClose := deps.close
			deps.close = func(ctx context.Context, fd int) error {
				closed[fd]++
				return realClose(ctx, fd)
			}
			lease, present, err := recoverManagedVMCgroup(
				t.Context(),
				deps,
				testVMID,
				processTestLocator(),
				processTestCgroupObservation(),
				&managedVMCgroupTestProcessVerifier{},
			)
			require.NoError(t, err)
			assert.Nil(t, lease)
			assert.False(t, present)
			probe.assertNone(t)
			assert.Equal(t, opened, closed)
		})
	}
}

func TestRecoverManagedVMCgroupPinsExistingLeafAndVerifiesMembership(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	probe := &managedVMCgroupRecoveryMutationProbe{}
	deps := probe.deps(fixture)

	lease, present, err := recoverManagedVMCgroup(
		t.Context(),
		deps,
		testVMID,
		processTestLocator(),
		processTestCgroupObservation(),
		&managedVMCgroupTestProcessVerifier{},
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.True(t, present)
	probe.assertNone(t)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
	assert.False(t, fixture.vmLeafPresent(testVMID))
}

func TestRecoverManagedVMCgroupRejectsUnsafeExistingHierarchyWithoutMutation(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*managedVMCgroupTestFixture, *managedVMCgroupDeps){
		"parent metadata": func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
			require.NoError(t, os.Chmod(filepath.Join(fixture.root, "mvmctl"), 0o700))
		},
		"leaf metadata": func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
			require.NoError(t, os.Chmod(fixture.vmLeafPath(testVMID), 0o700))
		},
		"cgroup2 mount": func(_ *managedVMCgroupTestFixture, deps *managedVMCgroupDeps) {
			deps.statfs = func(context.Context, int, *unix.Statfs_t) error { return nil }
		},
		"leaf mount identity": func(_ *managedVMCgroupTestFixture, deps *managedVMCgroupDeps) {
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
	for name, makeUnsafe := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			probe := &managedVMCgroupRecoveryMutationProbe{}
			deps := probe.deps(fixture)
			makeUnsafe(fixture, &deps)

			lease, present, err := recoverManagedVMCgroup(
				t.Context(),
				deps,
				testVMID,
				processTestLocator(),
				processTestCgroupObservation(),
				&managedVMCgroupTestProcessVerifier{},
			)
			assert.Nil(t, lease)
			assert.False(t, present)
			require.Error(t, err)
			assert.True(t, fixture.vmLeafPresent(testVMID))
			probe.assertNone(t)
		})
	}
}

func TestRecoverManagedVMCgroupPreAdmissionFailuresReturnNoAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*managedVMCgroupDeps){
		"open leaf": func(deps *managedVMCgroupDeps) {
			realOpenAt2 := deps.openAt2
			deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
				if name == testVMID {
					return -1, unix.EIO
				}
				return realOpenAt2(ctx, fd, name, how)
			}
		},
		"inspect leaf": func(deps *managedVMCgroupDeps) {
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
				if fd == leafFD {
					return unix.EIO
				}
				return realStatx(ctx, fd, stat)
			}
		},
	}
	for name, fail := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			probe := &managedVMCgroupRecoveryMutationProbe{}
			deps := probe.deps(fixture)
			fail(&deps)

			lease, present, err := recoverManagedVMCgroup(
				t.Context(),
				deps,
				testVMID,
				processTestLocator(),
				processTestCgroupObservation(),
				&managedVMCgroupTestProcessVerifier{},
			)
			assert.Nil(t, lease)
			assert.False(t, present)
			require.Error(t, err)
			assert.True(t, fixture.vmLeafPresent(testVMID))
			probe.assertNone(t)
		})
	}
}

func TestRecoverManagedVMCgroupPostAdmissionProofFailureRetainsSameLeaseForRetry(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure func(*managedVMCgroupTestFixture, *managedVMCgroupDeps, *managedVMCgroupTestProcessVerifier)
		repair    func(*managedVMCgroupTestFixture, *managedVMCgroupTestProcessVerifier)
	}{
		"membership open": {
			configure: func(_ *managedVMCgroupTestFixture, deps *managedVMCgroupDeps, _ *managedVMCgroupTestProcessVerifier) {
				realOpenAt2 := deps.openAt2
				failed := true
				deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
					if name == "cgroup.procs" && failed {
						failed = false
						return -1, unix.EIO
					}
					return realOpenAt2(ctx, fd, name, how)
				}
			},
			repair: func(*managedVMCgroupTestFixture, *managedVMCgroupTestProcessVerifier) {},
		},
		"membership mismatch": {
			configure: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps, _ *managedVMCgroupTestProcessVerifier) {
				fixture.setMembership(testVMID, "4343\n")
			},
			repair: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupTestProcessVerifier) {
				fixture.setMembership(testVMID, "4242\n")
			},
		},
		"PID reuse": {
			configure: func(_ *managedVMCgroupTestFixture, _ *managedVMCgroupDeps, verifier *managedVMCgroupTestProcessVerifier) {
				verifier.afterErr = errors.New("pidfd locator changed after membership read")
			},
			repair: func(_ *managedVMCgroupTestFixture, verifier *managedVMCgroupTestProcessVerifier) {
				verifier.afterErr = nil
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			probe := &managedVMCgroupRecoveryMutationProbe{}
			deps := probe.deps(fixture)
			verifier := &managedVMCgroupTestProcessVerifier{}
			tc.configure(fixture, &deps, verifier)

			lease, present, err := recoverManagedVMCgroup(
				t.Context(),
				deps,
				testVMID,
				processTestLocator(),
				processTestCgroupObservation(),
				verifier,
			)
			require.Error(t, err)
			require.NotNil(t, lease)
			assert.True(t, present)
			assert.False(t, lease.snapshot().released)
			assert.True(t, fixture.vmLeafPresent(testVMID))
			probe.assertNone(t)

			tc.repair(fixture, verifier)
			require.NoError(t, lease.VerifyMembership(
				t.Context(),
				processTestLocator(),
				processTestCgroupObservation(),
				verifier,
			))
			fixture.setMembership(testVMID, "")
			require.NoError(t, lease.Remove(t.Context()))
			require.NoError(t, lease.Release(t.Context()))
			probe.assertNone(t)
		})
	}
}

func TestRecoverManagedVMCgroupRejectsCanceledContextWithoutMutation(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	probe := &managedVMCgroupRecoveryMutationProbe{}
	deps := probe.deps(fixture)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	lease, present, err := recoverManagedVMCgroup(
		ctx,
		deps,
		testVMID,
		processTestLocator(),
		processTestCgroupObservation(),
		&managedVMCgroupTestProcessVerifier{},
	)
	assert.Nil(t, lease)
	assert.False(t, present)
	require.ErrorIs(t, err, context.Canceled)
	assert.True(t, fixture.vmLeafPresent(testVMID))
	probe.assertNone(t)
}

func TestRecoverManagedVMCgroupCopiedConcurrentReleaseClosesOnce(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	probe := &managedVMCgroupRecoveryMutationProbe{}
	deps := probe.deps(fixture)
	closed := map[int]int{}
	var closeMu sync.Mutex
	realClose := deps.close
	deps.close = func(ctx context.Context, fd int) error {
		closeMu.Lock()
		closed[fd]++
		closeMu.Unlock()
		return realClose(ctx, fd)
	}
	lease, present, err := recoverManagedVMCgroup(
		t.Context(),
		deps,
		testVMID,
		processTestLocator(),
		processTestCgroupObservation(),
		&managedVMCgroupTestProcessVerifier{},
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.True(t, present)
	lease.state.mu.Lock()
	retained := append([]int{}, lease.state.retained...)
	lease.state.mu.Unlock()
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))

	copied := *lease
	results := make(chan error, 16)
	var group sync.WaitGroup
	for index := 0; index < cap(results); index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			if index%2 == 0 {
				results <- lease.Release(t.Context())
				return
			}
			results <- copied.Release(t.Context())
		}(index)
	}
	group.Wait()
	close(results)
	for releaseErr := range results {
		require.NoError(t, releaseErr)
	}
	closeMu.Lock()
	for _, fd := range retained {
		assert.Equal(t, 1, closed[fd])
	}
	closeMu.Unlock()
	probe.assertNone(t)
}

type managedVMCgroupRecoveryMutationProbe struct {
	mu    sync.Mutex
	calls int
}

func (probe *managedVMCgroupRecoveryMutationProbe) deps(
	fixture *managedVMCgroupTestFixture,
) managedVMCgroupDeps {
	deps := fixture.deps()
	record := func() error {
		probe.mu.Lock()
		defer probe.mu.Unlock()
		probe.calls++
		return errors.New("recovery attempted to mutate the cgroup hierarchy")
	}
	deps.mkdirAt = func(context.Context, int, string, uint32) error { return record() }
	deps.fchown = func(context.Context, int, int, int) error { return record() }
	deps.fchmod = func(context.Context, int, uint32) error { return record() }
	return deps
}

func (probe *managedVMCgroupRecoveryMutationProbe) assertNone(t *testing.T) {
	t.Helper()
	probe.mu.Lock()
	defer probe.mu.Unlock()
	assert.Zero(t, probe.calls)
}
