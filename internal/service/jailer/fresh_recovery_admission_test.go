package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFreshRecoveryActiveAdmissionPrecedesCleanupEffectsAndRemovesExactCgroup(t *testing.T) {
	t.Parallel()

	fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, true)
	wantGeneration := fixture.record.cleanupGeneration + 1

	cleanup, err := fixture.begin(t.Context())
	require.NoError(t, err)
	require.NotNil(t, cleanup)
	assert.Equal(t, wantGeneration, cleanup.snapshot().cleanupGeneration)
	assert.Nil(t, cleanup.state.freshRecoveryAdmission)
	assert.True(t, fixture.cgroup.vmLeafPresent(testVMID))
	if diff := cmp.Diff(
		[]string{"record-write", "cgroup-admission", "cgroup-membership"},
		fixture.events.snapshot(),
	); diff != "" {
		t.Errorf("BeginCleanup admission order mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.False(t, fixture.cgroup.vmLeafPresent(testVMID))
	assertInstanceRecordEqual(t, cleanup.snapshot(), fixture.readRecord(t))
	if diff := cmp.Diff(
		[]string{
			"record-write",
			"cgroup-admission",
			"cgroup-membership",
			"process-terminate",
			"process-reap",
			"cgroup-membership",
			"cgroup-remove",
			"record-write",
		},
		fixture.events.snapshot(),
	); diff != "" {
		t.Errorf("fresh recovery cleanup order mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestCleanupCompleteNilContextPreservesOrdinaryCompletionError(t *testing.T) {
	t.Parallel()

	t.Run("ordinary cleanup", func(t *testing.T) {
		fixture := newProcessIdentityFixture(t, false)
		cleanup, err := fixture.launch.Abort(t.Context())
		require.NoError(t, err)
		require.NotNil(t, cleanup)

		err = cleanup.Complete(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cleanup completion context is required")
		assert.NotContains(t, err.Error(), "fresh-recovery admission")
		require.NoError(t, cleanup.Complete(t.Context()))
		require.NoError(t, cleanup.Release(t.Context()))
	})

	t.Run("fresh cleanup with pending admission", func(t *testing.T) {
		fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, false)
		cleanup, err := fixture.begin(t.Context())
		require.Error(t, err)
		require.NotNil(t, cleanup)

		err = cleanup.Complete(nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "cleanup completion context is required")
		assert.NotContains(t, err.Error(), "fresh-recovery admission context")
		fixture.cgroup.addVMLeaf(testVMID)
		require.NoError(t, cleanup.Complete(t.Context()))
		require.NoError(t, cleanup.Release(t.Context()))
	})
}

// Rationale: absence observed while a recovered process is live is provisional. Cleanup must retain that causal
// absence until reap, then bind and remove an exact leaf that appeared in the meantime.
func TestFreshRecoveryLaunchingAbsentLeafAppearingBeforeReapIsBoundAndRemoved(t *testing.T) {
	t.Parallel()

	fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleLaunching, false)
	cleanup, err := fixture.begin(t.Context())
	require.NoError(t, err)
	prepared := cleanup.state.preparedManagedVMCgroup
	require.NotNil(t, prepared)
	assert.Nil(t, cleanup.state.freshRecoveryAdmission)
	realOpenAt2 := prepared.state.deps.openAt2
	var bindObserved atomic.Bool
	prepared.state.deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		if name == testVMID && bindObserved.CompareAndSwap(false, true) {
			fixture.events.add("cgroup-bind")
		}
		return realOpenAt2(ctx, fd, name, how)
	}

	fixture.cgroup.addVMLeaf(testVMID)
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.False(t, fixture.cgroup.vmLeafPresent(testVMID))
	events := fixture.events.snapshot()
	requireFreshRecoveryEventOrder(t, events, "process-reap", "cgroup-bind", "cgroup-remove")
	require.NoError(t, cleanup.Release(t.Context()))
}

// Rationale: A boot mismatch removes process authority from consideration, but it does not prove the fixed cgroup is
// safe to ignore. Present or unstable cgroup state must preserve the durable record without opening or signaling a PID.
func TestFreshRecoveryBootMismatchRejectsPresentOrUnstableCgroupWithoutEffects(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, *freshRecoveryAdmissionTestFixture, *managedVMCgroupAbsenceProbe){
		"present": func(_ *testing.T, fixture *freshRecoveryAdmissionTestFixture, _ *managedVMCgroupAbsenceProbe) {
			fixture.cgroup.addVMLeaf(testVMID)
		},
		"appeared": func(t *testing.T, fixture *freshRecoveryAdmissionTestFixture, probe *managedVMCgroupAbsenceProbe) {
			require.NoError(t, os.Mkdir(
				filepath.Join(fixture.cgroup.root, "mvmctl"),
				os.FileMode(managedVMCgroupParentMode),
			))
			realOpenAt2 := probe.deps.openAt2
			attempts := 0
			probe.deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
				if name == testVMID {
					attempts++
					if attempts == 2 {
						fixture.cgroup.addVMLeaf(testVMID)
					}
				}
				return realOpenAt2(ctx, fd, name, how)
			}
		},
		"disappeared": func(t *testing.T, fixture *freshRecoveryAdmissionTestFixture, probe *managedVMCgroupAbsenceProbe) {
			require.NoError(t, os.Mkdir(
				filepath.Join(fixture.cgroup.root, "mvmctl"),
				os.FileMode(managedVMCgroupParentMode),
			))
			realOpenAt2 := probe.deps.openAt2
			attempts := 0
			probe.deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
				if name == "mvmctl" {
					attempts++
					if attempts == 2 {
						require.NoError(t, os.Remove(filepath.Join(fixture.cgroup.root, "mvmctl")))
					}
				}
				return realOpenAt2(ctx, fd, name, how)
			}
		},
		"replacement": func(t *testing.T, fixture *freshRecoveryAdmissionTestFixture, probe *managedVMCgroupAbsenceProbe) {
			require.NoError(t, os.Mkdir(
				filepath.Join(fixture.cgroup.root, "mvmctl"),
				os.FileMode(managedVMCgroupParentMode),
			))
			probe.rewriteStatxCall("parent", 2, func(stat *unix.Statx_t) { stat.Ino++ })
		},
	}
	for name, configure := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, false)
			fixture.identityDeps.readBootID = func(context.Context) (string, error) {
				return processOtherBootID, nil
			}
			probe := newManagedVMCgroupAbsenceProbe(fixture.cgroup)
			configure(t, fixture, probe)
			fixture.installCgroupDeps(probe.deps)

			cleanup, err := fixture.begin(t.Context())
			assert.Nil(t, cleanup)
			require.Error(t, err)
			assertInstanceRecordEqual(t, fixture.record, fixture.readRecord(t))
			if diff := cmp.Diff([]string{}, fixture.processLog.snapshot()); diff != "" {
				t.Errorf("boot-mismatch process events mismatch (-want +got):\n%s", diff)
			}
			assert.NotContains(t, fixture.events.snapshot(), "record-write")
			assert.NotContains(t, fixture.events.snapshot(), "process-terminate")
			assert.NotContains(t, fixture.events.snapshot(), "cgroup-remove")
			probe.assertReadOnly(t)
		})
	}
}

func TestFreshRecoveryMissingCgroupIsAllowedOnlyForLaunchingOrAlreadyCleaning(t *testing.T) {
	t.Parallel()

	tests := map[string]instanceLifecycle{
		"launching": instanceLifecycleLaunching,
		"cleaning":  instanceLifecycleCleaning,
	}
	for name, lifecycle := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newFreshRecoveryAdmissionTestFixture(t, lifecycle, false)
			wantGeneration := fixture.record.cleanupGeneration
			if lifecycle == instanceLifecycleLaunching {
				wantGeneration++
			}

			cleanup, err := fixture.begin(t.Context())
			require.NoError(t, err)
			require.NotNil(t, cleanup)
			assert.Equal(t, wantGeneration, cleanup.snapshot().cleanupGeneration)
			require.NoError(t, cleanup.Complete(t.Context()))
			assert.Equal(t, wantGeneration, cleanup.snapshot().cleanupGeneration)
			assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
			assert.NotContains(t, fixture.events.snapshot(), "cgroup-remove")
			assertInstanceRecordEqual(t, cleanup.snapshot(), fixture.readRecord(t))
			require.NoError(t, cleanup.Release(t.Context()))
		})
	}
}

// Rationale: An active record promised exact managed-cgroup membership. Missing runtime cgroup state is inconsistent,
// so cleanup must retain all authority without effects until the exact leaf can be admitted on a later Complete call.
func TestFreshRecoveryActiveMissingCgroupStaysPendingAndRejectsReleaseUntilRetry(t *testing.T) {
	t.Parallel()

	fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, false)
	cleanup, err := fixture.begin(t.Context())
	require.NotNil(t, cleanup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "active fresh recovery has no managed VM cgroup")
	assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
	assertInstanceRecordEqual(t, cleanup.snapshot(), fixture.readRecord(t))
	assert.NotContains(t, fixture.events.snapshot(), "process-terminate")
	assert.NotContains(t, fixture.events.snapshot(), "cgroup-remove")
	require.Error(t, cleanup.Release(t.Context()))

	fixture.cgroup.addVMLeaf(testVMID)
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
	assert.False(t, fixture.cgroup.vmLeafPresent(testVMID))
	require.NoError(t, cleanup.Release(t.Context()))
}

// Rationale: Any failure after the cgroup leaf has been pinned must retain that exact lease. Reopening by name on
// retry would allow a replacement leaf to substitute for the authority already admitted into cleanup.
func TestFreshRecoveryMembershipAndFinalLocatorFailuresRetryWithSameCgroupLease(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		failLocatorCall int
		want            string
	}{
		"before membership": {failLocatorCall: 3, want: "before cgroup membership"},
		"after membership":  {failLocatorCall: 4, want: "after cgroup membership"},
		"final locator":     {failLocatorCall: 5, want: "locator changed during full verification"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, true)
			realDerive := fixture.recoveryDeps.deriveLocator
			deriveCalls := 0
			fixture.recoveryDeps.deriveLocator = func(
				ctx context.Context,
				fd int,
			) (instanceProcessLocator, error) {
				locator, err := realDerive(ctx, fd)
				deriveCalls++
				if err == nil && deriveCalls == tc.failLocatorCall {
					locator.startTimeTicks++
				}
				return locator, err
			}

			cleanup, err := fixture.begin(t.Context())
			require.NotNil(t, cleanup)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
			retained := cleanup.state.managedVMCgroup
			require.NotNil(t, retained)
			assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
			assert.NotContains(t, fixture.events.snapshot(), "process-terminate")
			assert.NotContains(t, fixture.events.snapshot(), "cgroup-remove")

			require.NoError(t, cleanup.Complete(t.Context()))
			assert.Equal(t, 1, countFreshRecoveryEvents(fixture.events.snapshot(), "cgroup-admission"))
			assert.True(t, retained.snapshot().released)
			assert.False(t, fixture.cgroup.vmLeafPresent(testVMID))
			require.NoError(t, cleanup.Release(t.Context()))
		})
	}
}

// Rationale: The cgroup and process proofs are still insufficient if the host boot changes at their final boundary.
// A later Complete call must repeat membership on the same leaf before any cleanup effect occurs.
func TestFreshRecoveryFinalBootFailureRetainsPendingAdmissionAndSameLeaseForRetry(t *testing.T) {
	t.Parallel()

	fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, true)
	fixture.identity.bootIDs = []string{processTestHostBootID, processOtherBootID, processTestHostBootID}
	cleanup, err := fixture.begin(t.Context())
	require.NotNil(t, cleanup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "host boot ID changed during fresh recovery admission")
	retained := cleanup.state.managedVMCgroup
	require.NotNil(t, retained)
	assert.NotContains(t, fixture.events.snapshot(), "process-terminate")
	assert.NotContains(t, fixture.events.snapshot(), "cgroup-remove")
	assertInstanceRecordEqual(t, cleanup.snapshot(), fixture.readRecord(t))

	require.NoError(t, cleanup.Complete(t.Context()))
	assert.Equal(t, 1, countFreshRecoveryEvents(fixture.events.snapshot(), "cgroup-admission"))
	assert.True(t, retained.snapshot().released)
	assert.False(t, fixture.cgroup.vmLeafPresent(testVMID))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestFreshRecoveryFinalBootReadErrorRetainsPendingAdmissionAndSameLeaseForRetry(t *testing.T) {
	t.Parallel()

	fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, true)
	readErr := errors.New("injected final boot ID read failure")
	reads := 0
	fixture.identityDeps.readBootID = func(context.Context) (string, error) {
		reads++
		if reads == 2 {
			return "", readErr
		}
		return processTestHostBootID, nil
	}
	cleanup, err := fixture.begin(t.Context())
	require.NotNil(t, cleanup)
	require.ErrorIs(t, err, readErr)
	retained := cleanup.state.managedVMCgroup
	require.NotNil(t, retained)
	assert.NotContains(t, fixture.events.snapshot(), "process-terminate")
	assertInstanceRecordEqual(t, cleanup.snapshot(), fixture.readRecord(t))

	require.NoError(t, cleanup.Complete(t.Context()))
	assert.Equal(t, 1, countFreshRecoveryEvents(fixture.events.snapshot(), "cgroup-admission"))
	assert.True(t, retained.snapshot().released)
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestFreshRecoverySameBootRejectsUnsafeForeignOrReplacementLeafBeforeEffects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure func(*testing.T, *freshRecoveryAdmissionTestFixture, *managedVMCgroupDeps)
		repair    func(*testing.T, *freshRecoveryAdmissionTestFixture)
	}{
		"unsafe": {
			configure: func(t *testing.T, fixture *freshRecoveryAdmissionTestFixture, _ *managedVMCgroupDeps) {
				require.NoError(t, os.Chmod(fixture.cgroup.vmLeafPath(testVMID), 0o700))
			},
			repair: func(t *testing.T, fixture *freshRecoveryAdmissionTestFixture) {
				require.NoError(t, os.Chmod(
					fixture.cgroup.vmLeafPath(testVMID),
					os.FileMode(managedVMCgroupLeafMode),
				))
			},
		},
		"foreign member": {
			configure: func(_ *testing.T, fixture *freshRecoveryAdmissionTestFixture, _ *managedVMCgroupDeps) {
				fixture.cgroup.setMembership(testVMID, "4343\n")
			},
			repair: func(_ *testing.T, fixture *freshRecoveryAdmissionTestFixture) {
				fixture.cgroup.setMembership(testVMID, "4242\n")
			},
		},
		"replacement mount identity": {
			configure: func(_ *testing.T, _ *freshRecoveryAdmissionTestFixture, deps *managedVMCgroupDeps) {
				realOpenAt2 := deps.openAt2
				realStatx := deps.statx
				leafFD := -1
				mutated := false
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
					if fd == leafFD && !mutated {
						mutated = true
						stat.Mnt_id++
					}
					return nil
				}
			},
			repair: func(*testing.T, *freshRecoveryAdmissionTestFixture) {},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, true)
			deps := fixture.cgroup.deps()
			tc.configure(t, fixture, &deps)
			fixture.installCgroupDeps(deps)

			cleanup, err := fixture.begin(t.Context())
			require.NotNil(t, cleanup)
			require.Error(t, err)
			assert.Equal(t, instanceLifecycleCleaning, fixture.readRecord(t).lifecycle)
			assert.NotContains(t, fixture.events.snapshot(), "process-terminate")
			assert.NotContains(t, fixture.events.snapshot(), "cgroup-remove")
			tc.repair(t, fixture)
			require.NoError(t, cleanup.Complete(t.Context()))
			require.NoError(t, cleanup.Release(t.Context()))
		})
	}
}

// Rationale: admission invokes filesystem and process callbacks. Reentrant lifecycle calls must observe the shared
// gate instead of deadlocking on the cleanup mutex or consuming authority mid-proof.
func TestFreshRecoveryAdmissionCallbacksRunOutsideCleanupMutexAndRejectReentrancy(t *testing.T) {
	t.Parallel()

	fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, true)
	writeErr := errors.New("injected post-rename durability failure")
	realFsync := fixture.authority.deps.fsync
	fileSynced := false
	failureInjected := false
	fixture.authority.deps.fsync = func(ctx context.Context, fd int) error {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG {
			fileSynced = true
			return realFsync(ctx, fd)
		}
		if fileSynced && !failureInjected {
			failureInjected = true
			return writeErr
		}
		return realFsync(ctx, fd)
	}
	cleanup, err := fixture.begin(t.Context())
	require.ErrorIs(t, err, writeErr)
	fixture.authority.deps.fsync = realFsync

	mutexHeldErr := errors.New("cleanup mutex held during admission callback")
	callbackEvents := &recoveryEventLog{}
	var nestedCompleteErr, nestedReleaseErr, nestedAdmissionErr error
	var reentered atomic.Bool
	checkUnlocked := func() error {
		if !cleanup.state.mu.TryLock() {
			return mutexHeldErr
		}
		cleanup.state.mu.Unlock()
		if reentered.CompareAndSwap(false, true) {
			nestedCompleteErr = cleanup.Complete(t.Context())
			nestedReleaseErr = cleanup.Release(t.Context())
			nestedAdmissionErr = cleanup.admitFreshRecovery(t.Context())
		}
		return nil
	}
	admission := cleanup.state.freshRecoveryAdmission
	realOpenRoot := admission.cgroupDeps.openRoot
	admission.cgroupDeps.openRoot = func(ctx context.Context) (int, error) {
		callbackEvents.add("cgroup-open-root")
		if err := checkUnlocked(); err != nil {
			return -1, err
		}
		return realOpenRoot(ctx)
	}
	recoveredState := cleanup.state.recoveredProcesses.state
	realDeriveLocator := recoveredState.deps.deriveLocator
	recoveredState.deps.deriveLocator = func(ctx context.Context, fd int) (instanceProcessLocator, error) {
		callbackEvents.add("recovered-locator")
		if err := checkUnlocked(); err != nil {
			return instanceProcessLocator{}, err
		}
		return realDeriveLocator(ctx, fd)
	}
	realReadBootID := admission.identityDeps.readBootID
	admission.identityDeps.readBootID = func(ctx context.Context) (string, error) {
		callbackEvents.add("final-boot-id")
		if err := checkUnlocked(); err != nil {
			return "", err
		}
		return realReadBootID(ctx)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- cleanup.admitFreshRecovery(ctx) }()
	select {
	case err = <-result:
	case <-ctx.Done():
		t.Fatalf("wait for reentrant fresh-recovery admission: %v", ctx.Err())
	}
	require.NotErrorIs(t, err, mutexHeldErr)
	require.NoError(t, err)
	if diff := cmp.Diff(
		[]string{
			"cgroup-open-root",
			"recovered-locator",
			"recovered-locator",
			"recovered-locator",
			"final-boot-id",
		},
		callbackEvents.snapshot(),
	); diff != "" {
		t.Errorf("unlocked fresh-recovery callbacks mismatch (-want +got):\n%s", diff)
	}
	for _, nestedErr := range []error{nestedCompleteErr, nestedReleaseErr, nestedAdmissionErr} {
		require.Error(t, nestedErr)
		assert.Contains(t, nestedErr.Error(), "in progress")
	}
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

func TestFreshRecoveryAdmissionGateRejectsConcurrentLifecycleCalls(t *testing.T) {
	t.Parallel()

	fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, false)
	cleanup, err := fixture.begin(t.Context())
	require.NotNil(t, cleanup)
	require.Error(t, err)
	fixture.cgroup.addVMLeaf(testVMID)

	entered := make(chan struct{})
	unblock := make(chan struct{})
	defer func() {
		select {
		case <-unblock:
		default:
			close(unblock)
		}
	}()
	realOpenRoot := cleanup.state.freshRecoveryAdmission.cgroupDeps.openRoot
	cleanup.state.freshRecoveryAdmission.cgroupDeps.openRoot = func(ctx context.Context) (int, error) {
		close(entered)
		select {
		case <-unblock:
		case <-ctx.Done():
			return -1, ctx.Err()
		}
		return realOpenRoot(ctx)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- cleanup.admitFreshRecovery(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatalf("wait for blocked admission callback: %v", ctx.Err())
	}
	for _, concurrentErr := range []error{
		cleanup.Complete(t.Context()),
		cleanup.Release(t.Context()),
		cleanup.admitFreshRecovery(t.Context()),
	} {
		require.Error(t, concurrentErr)
		assert.Contains(t, concurrentErr.Error(), "in progress")
	}
	close(unblock)
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatalf("wait for admission completion: %v", ctx.Err())
	}
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
}

// Rationale: Cleaning authority transfers only at the record rename boundary. A failure before rename must release
// rejected process authority, while a durability error after rename must return cleanup with admission still pending.
func TestFreshRecoveryCleaningWriteBoundaryControlsAdmissionOwnership(t *testing.T) {
	t.Parallel()

	t.Run("pre-rename", func(t *testing.T) {
		fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, true)
		writeErr := errors.New("injected pre-rename cleaning write failure")
		fixture.authority.deps.renameAt = func(context.Context, int, string, int, string) error {
			return writeErr
		}

		cleanup, err := fixture.begin(t.Context())
		assert.Nil(t, cleanup)
		require.ErrorIs(t, err, writeErr)
		assertInstanceRecordEqual(t, fixture.record, fixture.readRecord(t))
		assert.Zero(t, countFreshRecoveryEvents(fixture.events.snapshot(), "cgroup-admission"))
		assert.NotContains(t, fixture.events.snapshot(), "process-terminate")
		if diff := cmp.Diff([]string{"open:4242", "close:700"}, fixture.processLog.snapshot()); diff != "" {
			t.Errorf("rejected fresh recovery process events mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("post-rename", func(t *testing.T) {
		fixture := newFreshRecoveryAdmissionTestFixture(t, instanceLifecycleActive, true)
		writeErr := errors.New("injected post-rename cleaning durability failure")
		realFsync := fixture.authority.deps.fsync
		fileSynced := false
		failureInjected := false
		fixture.authority.deps.fsync = func(ctx context.Context, fd int) error {
			var stat unix.Stat_t
			if err := unix.Fstat(fd, &stat); err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG {
				fileSynced = true
				return realFsync(ctx, fd)
			}
			if fileSynced && !failureInjected {
				failureInjected = true
				return writeErr
			}
			return realFsync(ctx, fd)
		}

		cleanup, err := fixture.begin(t.Context())
		require.NotNil(t, cleanup)
		require.ErrorIs(t, err, writeErr)
		assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
		require.NotNil(t, cleanup.state.freshRecoveryAdmission)
		assert.Nil(t, cleanup.state.managedVMCgroup)
		assert.Zero(t, countFreshRecoveryEvents(fixture.events.snapshot(), "cgroup-admission"))
		require.Error(t, cleanup.Release(t.Context()))

		require.NoError(t, cleanup.Complete(t.Context()))
		assert.False(t, fixture.cgroup.vmLeafPresent(testVMID))
		require.NoError(t, cleanup.Release(t.Context()))
	})
}

type freshRecoveryAdmissionTestFixture struct {
	t            *testing.T
	record       instanceRecord
	authority    *instanceAuthority
	recoveryDeps processRecoveryDeps
	identityDeps processIdentityVerifierDeps
	identity     *fakeProcessIdentityDeps
	processLog   *recoveryEventLog
	events       *recoveryEventLog
	cgroup       *managedVMCgroupTestFixture
}

func newFreshRecoveryAdmissionTestFixture(
	t *testing.T,
	lifecycle instanceLifecycle,
	cgroupPresent bool,
) *freshRecoveryAdmissionTestFixture {
	t.Helper()
	record, identity := activeRecoveryTestRecord(t)
	switch lifecycle {
	case instanceLifecycleLaunching:
		record.lifecycle = instanceLifecycleLaunching
		record.active = nil
	case instanceLifecycleActive:
	case instanceLifecycleCleaning:
		var err error
		record, err = beginInstanceCleanup(testInstanceCaller(), record)
		require.NoError(t, err)
	default:
		t.Fatalf("unsupported fresh-recovery test lifecycle %q", lifecycle)
	}
	authority, recoveryDeps, identityDeps, processLog := newActiveRecoveryTestAuthority(t, record, identity)
	cgroup := newManagedVMCgroupTestFixture(t)
	if cgroupPresent {
		cgroup.addVMLeaf(testVMID)
	}
	events := &recoveryEventLog{}

	authorityDepsRename := authority.deps.renameAt
	authority.deps.renameAt = func(
		ctx context.Context,
		oldDirectoryFD int,
		oldName string,
		newDirectoryFD int,
		newName string,
	) error {
		events.add("record-write")
		return authorityDepsRename(ctx, oldDirectoryFD, oldName, newDirectoryFD, newName)
	}

	realTerminate := recoveryDeps.terminatePIDFD
	recoveryDeps.terminatePIDFD = func(ctx context.Context, fd int) error {
		events.add("process-terminate")
		return realTerminate(ctx, fd)
	}
	realWait := recoveryDeps.waitReapPIDFD
	recoveryDeps.waitReapPIDFD = func(ctx context.Context, fd int) error {
		events.add("process-reap")
		if err := realWait(ctx, fd); err != nil {
			return err
		}
		if cgroup.vmLeafPresent(testVMID) {
			cgroup.setMembership(testVMID, "")
		}
		return nil
	}

	result := &freshRecoveryAdmissionTestFixture{
		t:            t,
		record:       record,
		authority:    authority,
		recoveryDeps: recoveryDeps,
		identityDeps: identityDeps,
		identity:     identity,
		processLog:   processLog,
		events:       events,
		cgroup:       cgroup,
	}
	result.installCgroupDeps(cgroup.deps())
	return result
}

func (fixture *freshRecoveryAdmissionTestFixture) installCgroupDeps(deps managedVMCgroupDeps) {
	fixture.t.Helper()
	realOpenRoot := deps.openRoot
	deps.openRoot = func(ctx context.Context) (int, error) {
		fixture.events.add("cgroup-admission")
		return realOpenRoot(ctx)
	}
	realUnlinkAt := deps.unlinkAt
	deps.unlinkAt = func(ctx context.Context, fd int, name string, flags int) error {
		fixture.events.add("cgroup-remove")
		return realUnlinkAt(ctx, fd, name, flags)
	}
	realOpenAt2 := deps.openAt2
	deps.openAt2 = func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
		if name == "cgroup.procs" {
			fixture.events.add("cgroup-membership")
		}
		return realOpenAt2(ctx, fd, name, how)
	}
	fixture.authority.cgroupDeps = deps
}

func (fixture *freshRecoveryAdmissionTestFixture) begin(ctx context.Context) (*cleanupLease, error) {
	fixture.t.Helper()
	return beginFreshRecoveryCleanupWithDeps(
		ctx,
		fixture.authority,
		testInstanceCaller(),
		testVMID,
		fixture.recoveryDeps,
		fixture.identityDeps,
	)
}

func (fixture *freshRecoveryAdmissionTestFixture) readRecord(t *testing.T) instanceRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(
		fixture.authority.policy.rootPath,
		"var/lib/mvmctl/instances/1000",
		testVMID+".json",
	))
	require.NoError(t, err)
	record, err := decodeInstanceRecord(raw)
	require.NoError(t, err)
	return record
}

func countFreshRecoveryEvents(events []string, want string) int {
	count := 0
	for _, event := range events {
		if event == want {
			count++
		}
	}
	return count
}

func requireFreshRecoveryEventOrder(t *testing.T, events []string, wants ...string) {
	t.Helper()
	previous := -1
	for _, want := range wants {
		index := -1
		for candidate, event := range events {
			if event == want {
				index = candidate
				break
			}
		}
		require.NotEqual(t, -1, index, "missing event %q in %v", want, events)
		assert.Greater(t, index, previous, "event %q is out of order in %v", want, events)
		previous = index
	}
}
