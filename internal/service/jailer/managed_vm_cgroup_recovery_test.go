package jailer

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

	"mvmctl/internal/infra"
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
			parentPath := filepath.Join(fixture.root, infra.JailerCgroupParent)
			if tc.addParent {
				require.NoError(t, os.Mkdir(parentPath, os.FileMode(managedVMCgroupParentMode)))
			}
			probe := &managedVMCgroupRecoveryMutationProbe{}
			deps := probe.deps(fixture)
			rootFD := -1
			parentFD := -1
			openedRoles := []string{}
			closedFDs := []int{}
			realOpenRoot := deps.openRoot
			deps.openRoot = func(ctx context.Context) (int, error) {
				fd, err := realOpenRoot(ctx)
				if err == nil {
					rootFD = fd
					openedRoles = append(openedRoles, "root")
				}
				return fd, err
			}
			realOpenAt2 := deps.openAt2
			deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
				openedFD, err := realOpenAt2(ctx, fd, name, how)
				if err == nil {
					parentFD = openedFD
					openedRoles = append(openedRoles, "parent")
				}
				return openedFD, err
			}
			realClose := deps.close
			deps.close = func(ctx context.Context, fd int) error {
				closedFDs = append(closedFDs, fd)
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
			wantOpenedRoles := []string{"root"}
			wantClosedFDs := []int{rootFD}
			absentPath := parentPath
			if tc.addParent {
				wantOpenedRoles = append(wantOpenedRoles, "parent")
				wantClosedFDs = []int{parentFD, rootFD}
				absentPath = fixture.vmLeafPath(testVMID)
			}
			if diff := cmp.Diff(wantOpenedRoles, openedRoles); diff != "" {
				t.Errorf("opened descriptor roles mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(wantClosedFDs, closedFDs); diff != "" {
				t.Errorf("closed descriptor order mismatch (-want +got):\n%s", diff)
			}
			_, statErr := os.Lstat(absentPath)
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func TestRecoverManagedVMCgroupPinsExistingLeafAndVerifiesMembership(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	probe := &managedVMCgroupRecoveryMutationProbe{}
	deps := probe.deps(fixture)
	verifier := &managedVMCgroupTestProcessVerifier{}

	lease, present, err := recoverManagedVMCgroup(
		t.Context(),
		deps,
		testVMID,
		processTestLocator(),
		processTestCgroupObservation(),
		verifier,
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.True(t, present)
	assert.Equal(t, 1, verifier.beforeCalls)
	assert.Equal(t, 1, verifier.afterCalls)
	probe.assertNone(t)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
	assert.False(t, fixture.vmLeafPresent(testVMID))
}

func TestRecoverManagedVMCgroupRejectsUnsafeExistingHierarchyWithoutMutation(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		makeUnsafe      func(*managedVMCgroupTestFixture, *managedVMCgroupDeps)
		assertPreserved func(*managedVMCgroupTestFixture)
	}{
		"parent metadata": {makeUnsafe: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
			require.NoError(t, os.Chmod(filepath.Join(fixture.root, infra.JailerCgroupParent), 0o700))
		}},
		"leaf metadata": {makeUnsafe: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
			require.NoError(t, os.Chmod(fixture.vmLeafPath(testVMID), 0o700))
		}},
		"cgroup2 mount": {makeUnsafe: func(_ *managedVMCgroupTestFixture, deps *managedVMCgroupDeps) {
			deps.statfs = func(context.Context, int, *unix.Statfs_t) error { return nil }
		}},
		"parent mount identity": {makeUnsafe: func(_ *managedVMCgroupTestFixture, deps *managedVMCgroupDeps) {
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
		}},
		"leaf mount identity": {makeUnsafe: func(_ *managedVMCgroupTestFixture, deps *managedVMCgroupDeps) {
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
		}},
		"parent symlink": {
			makeUnsafe: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
				parentPath := filepath.Join(fixture.root, infra.JailerCgroupParent)
				require.NoError(t, os.Remove(filepath.Join(fixture.vmLeafPath(testVMID), "cgroup.procs")))
				require.NoError(t, os.Remove(fixture.vmLeafPath(testVMID)))
				require.NoError(t, os.Remove(parentPath))
				require.NoError(t, os.Symlink("elsewhere", parentPath))
			},
			assertPreserved: func(fixture *managedVMCgroupTestFixture) {
				info, err := os.Lstat(filepath.Join(fixture.root, infra.JailerCgroupParent))
				require.NoError(t, err)
				assert.NotZero(t, info.Mode()&os.ModeSymlink)
			},
		},
		"leaf symlink": {
			makeUnsafe: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps) {
				require.NoError(t, os.Remove(filepath.Join(fixture.vmLeafPath(testVMID), "cgroup.procs")))
				require.NoError(t, os.Remove(fixture.vmLeafPath(testVMID)))
				require.NoError(t, os.Symlink("elsewhere", fixture.vmLeafPath(testVMID)))
			},
			assertPreserved: func(fixture *managedVMCgroupTestFixture) {
				info, err := os.Lstat(fixture.vmLeafPath(testVMID))
				require.NoError(t, err)
				assert.NotZero(t, info.Mode()&os.ModeSymlink)
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			fixture.addVMLeaf(testVMID)
			probe := &managedVMCgroupRecoveryMutationProbe{}
			deps := probe.deps(fixture)
			tc.makeUnsafe(fixture, &deps)

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
			if tc.assertPreserved != nil {
				tc.assertPreserved(fixture)
			} else {
				assert.True(t, fixture.vmLeafPresent(testVMID))
			}
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

	pidReuseErr := errors.New("pidfd locator changed after membership read")
	tests := map[string]struct {
		configure      func(*managedVMCgroupTestFixture, *managedVMCgroupDeps, *managedVMCgroupTestProcessVerifier)
		repair         func(*managedVMCgroupTestFixture, *managedVMCgroupTestProcessVerifier)
		wantErr        error
		wantBeforeCall int
		wantAfterCall  int
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
			repair:         func(*managedVMCgroupTestFixture, *managedVMCgroupTestProcessVerifier) {},
			wantBeforeCall: 1,
		},
		"membership mismatch": {
			configure: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupDeps, _ *managedVMCgroupTestProcessVerifier) {
				fixture.setMembership(testVMID, "4343\n")
			},
			repair: func(fixture *managedVMCgroupTestFixture, _ *managedVMCgroupTestProcessVerifier) {
				fixture.setMembership(testVMID, "4242\n")
			},
			wantBeforeCall: 1,
		},
		"PID reuse": {
			configure: func(_ *managedVMCgroupTestFixture, _ *managedVMCgroupDeps, verifier *managedVMCgroupTestProcessVerifier) {
				verifier.afterErr = pidReuseErr
			},
			repair: func(_ *managedVMCgroupTestFixture, verifier *managedVMCgroupTestProcessVerifier) {
				verifier.afterErr = nil
			},
			wantErr:        pidReuseErr,
			wantBeforeCall: 1,
			wantAfterCall:  1,
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
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			}
			require.NotNil(t, lease)
			assert.True(t, present)
			assert.Equal(t, tc.wantBeforeCall, verifier.beforeCalls)
			assert.Equal(t, tc.wantAfterCall, verifier.afterCalls)
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
			assert.Equal(t, tc.wantBeforeCall+1, verifier.beforeCalls)
			assert.Equal(t, tc.wantAfterCall+1, verifier.afterCalls)
			fixture.setMembership(testVMID, "")
			require.NoError(t, lease.Remove(t.Context()))
			require.NoError(t, lease.Release(t.Context()))
			probe.assertNone(t)
		})
	}
}

func TestRecoverManagedVMCgroupCancellationDuringMembershipRetainsSameLeaseForRetry(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	fixture.addVMLeaf(testVMID)
	probe := &managedVMCgroupRecoveryMutationProbe{}
	deps := probe.deps(fixture)
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		if err := ctx.Err(); err != nil {
			return -1, err
		}
		return realOpenAt2(ctx, fd, name, how)
	}
	ctx, cancel := context.WithCancel(t.Context())
	verifier := &cancelingManagedVMCgroupRecoveryVerifier{cancel: cancel}

	lease, present, err := recoverManagedVMCgroup(
		ctx,
		deps,
		testVMID,
		processTestLocator(),
		processTestCgroupObservation(),
		verifier,
	)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, lease)
	assert.True(t, present)
	assert.Equal(t, 1, verifier.beforeCalls)
	assert.Zero(t, verifier.afterCalls)
	assert.False(t, lease.snapshot().released)
	probe.assertNone(t)

	retryVerifier := &managedVMCgroupTestProcessVerifier{}
	require.NoError(t, lease.VerifyMembership(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		retryVerifier,
	))
	assert.Equal(t, 1, retryVerifier.beforeCalls)
	assert.Equal(t, 1, retryVerifier.afterCalls)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
	probe.assertNone(t)
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
	rootFD := -1
	parentFD := -1
	leafFD := -1
	openedAuthorityRoles := []string{}
	realOpenRoot := deps.openRoot
	deps.openRoot = func(ctx context.Context) (int, error) {
		fd, err := realOpenRoot(ctx)
		if err == nil {
			rootFD = fd
			openedAuthorityRoles = append(openedAuthorityRoles, "root")
		}
		return fd, err
	}
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		opened, err := realOpenAt2(ctx, fd, name, how)
		if err != nil {
			return opened, err
		}
		switch {
		case name == infra.JailerCgroupParent && parentFD < 0:
			parentFD = opened
			openedAuthorityRoles = append(openedAuthorityRoles, "parent")
		case name == testVMID && leafFD < 0:
			leafFD = opened
			openedAuthorityRoles = append(openedAuthorityRoles, "leaf")
		}
		return opened, nil
	}
	closedFDs := []int{}
	var closeMu sync.Mutex
	realClose := deps.close
	deps.close = func(ctx context.Context, fd int) error {
		closeMu.Lock()
		closedFDs = append(closedFDs, fd)
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
	if diff := cmp.Diff([]string{"root", "parent", "leaf"}, openedAuthorityRoles); diff != "" {
		t.Errorf("opened authority descriptor roles mismatch (-want +got):\n%s", diff)
	}
	expectedFDs := []int{rootFD, parentFD, leafFD}
	for _, fd := range expectedFDs {
		require.NotEqual(t, -1, fd)
	}
	require.NotEqual(t, rootFD, parentFD)
	require.NotEqual(t, rootFD, leafFD)
	require.NotEqual(t, parentFD, leafFD)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	closeMu.Lock()
	closedFDs = nil
	closeMu.Unlock()

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
	wantClosedFDs := []int{leafFD, parentFD, rootFD}
	if diff := cmp.Diff(wantClosedFDs, closedFDs); diff != "" {
		t.Errorf("released descriptor order mismatch (-want +got):\n%s", diff)
	}
	closeMu.Unlock()
	probe.assertNone(t)
}

type cancelingManagedVMCgroupRecoveryVerifier struct {
	cancel      context.CancelFunc
	beforeCalls int
	afterCalls  int
}

func (verifier *cancelingManagedVMCgroupRecoveryVerifier) verifyBeforeCgroupMembership(
	context.Context,
	instanceProcessLocator,
	processObservation,
) error {
	verifier.beforeCalls++
	verifier.cancel()
	return nil
}

func (verifier *cancelingManagedVMCgroupRecoveryVerifier) verifyAfterCgroupMembership(
	context.Context,
	instanceProcessLocator,
	processObservation,
) error {
	verifier.afterCalls++
	return nil
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
