package jailer

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type reentrantManagedVMCgroupLeaseTestVerifier struct {
	lease           *managedVMCgroupLease
	afterErr        error
	callbackLockErr error
	snapshot        managedVMCgroupLeaseSnapshot
	nestedVerifyErr error
	removeErr       error
	releaseErr      error
}

func (verifier *reentrantManagedVMCgroupLeaseTestVerifier) verifyBeforeCgroupMembership(
	ctx context.Context,
	locator instanceProcessLocator,
	observation processObservation,
) error {
	if !verifier.lease.state.mu.TryLock() {
		verifier.callbackLockErr = errors.New("membership verifier callback ran while the lease mutex was held")
		return verifier.callbackLockErr
	}
	verifier.lease.state.mu.Unlock()
	verifier.snapshot = verifier.lease.snapshot()
	verifier.nestedVerifyErr = verifier.lease.VerifyMembership(
		ctx,
		locator,
		observation,
		&managedVMCgroupTestProcessVerifier{},
	)
	verifier.removeErr = verifier.lease.Remove(ctx)
	verifier.releaseErr = verifier.lease.Release(ctx)
	return nil
}

func (verifier *reentrantManagedVMCgroupLeaseTestVerifier) verifyAfterCgroupMembership(
	context.Context,
	instanceProcessLocator,
	processObservation,
) error {
	return verifier.afterErr
}

func TestManagedVMCgroupVerifyMembershipRetriesOnSameLease(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	fixture.setMembership(testVMID, "4343\n")
	verifier := &managedVMCgroupTestProcessVerifier{}

	lease, err := prepared.Bind(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		verifier,
	)
	require.Error(t, err)
	require.NotNil(t, lease)
	fixture.setMembership(testVMID, "4242\n")
	require.NoError(t, lease.VerifyMembership(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		verifier,
	))
	assert.Equal(t, 2, verifier.beforeCalls)
	assert.Equal(t, 1, verifier.afterCalls)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestPreparedManagedVMCgroupBindRequiresJailerHandoffProvenance(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	fixture.addVMLeaf(testVMID)

	lease, err := prepared.Bind(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		&managedVMCgroupTestProcessVerifier{},
	)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, preparedManagedVMCgroupSnapshot{leafAbsent: true}, prepared.snapshot())
	assert.True(t, fixture.vmLeafPresent(testVMID))

	require.NoError(t, prepared.MarkJailerHandoff())
	lease, err = prepared.Bind(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		&managedVMCgroupTestProcessVerifier{},
	)
	require.NoError(t, err)
	require.NotNil(t, lease)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestManagedVMCgroupVerifyMembershipRejectsReentrantEffectsWithoutHoldingLock(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	lease := fixture.bindLease(t, fixture.deps())
	pidReuseErr := errors.New("retained pidfd now identifies a reused task")
	verifier := &reentrantManagedVMCgroupLeaseTestVerifier{lease: lease, afterErr: pidReuseErr}

	err := lease.VerifyMembership(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		verifier,
	)
	require.ErrorIs(t, err, pidReuseErr)
	assert.NoError(t, verifier.callbackLockErr)
	assert.Equal(t, managedVMCgroupLeaseSnapshot{}, verifier.snapshot)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(verifier.nestedVerifyErr).Code)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(verifier.removeErr).Code)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(verifier.releaseErr).Code)
	require.NoError(t, lease.VerifyMembership(
		t.Context(),
		processTestLocator(),
		processTestCgroupObservation(),
		&managedVMCgroupTestProcessVerifier{},
	))
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestPreparedManagedVMCgroupBindForCleanupReportsAbsentWithoutMutation(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	originalRootFD := prepared.state.rootFD
	originalParentFD := prepared.state.parentFD
	originalRetained := append([]int{}, prepared.state.retained...)
	realClose := prepared.state.deps.close
	var mu sync.Mutex
	closed := make(map[int]int, len(originalRetained))
	prepared.state.deps.close = func(ctx context.Context, fd int) error {
		mu.Lock()
		closed[fd]++
		mu.Unlock()
		return realClose(ctx, fd)
	}

	lease, present, err := prepared.BindForCleanup(t.Context())
	require.NoError(t, err)
	assert.Nil(t, lease)
	assert.False(t, present)
	assert.Equal(t, preparedManagedVMCgroupSnapshot{leafAbsent: true}, prepared.snapshot())
	assert.Equal(t, originalRootFD, prepared.state.rootFD)
	assert.Equal(t, originalParentFD, prepared.state.parentFD)
	assert.Equal(t, originalRetained, prepared.state.retained)
	require.NoError(t, prepared.Release(t.Context()))
	for _, fd := range originalRetained {
		assert.Equal(t, 1, closed[fd])
	}
	assert.False(t, fixture.vmLeafPresent(testVMID))
}

func TestPreparedManagedVMCgroupBindForCleanupTransfersPresentEmptyLeaf(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	fixture.setMembership(testVMID, "")

	lease, present, err := prepared.BindForCleanup(t.Context())
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.True(t, present)
	assert.True(t, prepared.snapshot().released)
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
	assert.False(t, fixture.vmLeafPresent(testVMID))
}

func TestPreparedManagedVMCgroupBindForCleanupRejectsUnsafeLeafWithoutTransfer(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	require.NoError(t, os.Chmod(fixture.vmLeafPath(testVMID), 0o700))

	lease, present, err := prepared.BindForCleanup(t.Context())
	require.Error(t, err)
	assert.Nil(t, lease)
	assert.False(t, present)
	assert.Equal(t, preparedManagedVMCgroupSnapshot{leafAbsent: true}, prepared.snapshot())
	require.NoError(t, prepared.Release(t.Context()))
	assert.True(t, fixture.vmLeafPresent(testVMID))
}

func TestPreparedManagedVMCgroupBindForCleanupRejectsLeafOutsidePinnedIdentity(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*unix.Statx_t){
		"device": func(stat *unix.Statx_t) { stat.Dev_minor++ },
		"mount":  func(stat *unix.Statx_t) { stat.Mnt_id++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newManagedVMCgroupTestFixture(t)
			deps := fixture.deps()
			realOpenAt2 := deps.openAt2
			realStatx := deps.statx
			leafFD := -1
			deps.openAt2 = func(ctx context.Context, parentFD int, entry string, how *unix.OpenHow) (int, error) {
				fd, err := realOpenAt2(ctx, parentFD, entry, how)
				if err == nil && entry == testVMID {
					leafFD = fd
				}
				return fd, err
			}
			deps.statx = func(ctx context.Context, fd int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, stat); err != nil {
					return err
				}
				if fd == leafFD {
					mutate(stat)
				}
				return nil
			}
			prepared, err := prepareManagedVMCgroup(t.Context(), deps, testVMID)
			require.NoError(t, err)
			require.NoError(t, prepared.MarkJailerHandoff())
			originalRootFD := prepared.state.rootFD
			originalParentFD := prepared.state.parentFD
			originalRetained := append([]int{}, prepared.state.retained...)
			fixture.addVMLeaf(testVMID)

			lease, present, err := prepared.BindForCleanup(t.Context())
			require.Error(t, err)
			assert.Nil(t, lease)
			assert.False(t, present)
			assert.Equal(t, originalRootFD, prepared.state.rootFD)
			assert.Equal(t, originalParentFD, prepared.state.parentFD)
			assert.Equal(t, originalRetained, prepared.state.retained)
			assert.Equal(t, preparedManagedVMCgroupSnapshot{leafAbsent: true}, prepared.snapshot())
			require.NoError(t, prepared.Release(t.Context()))
			assert.True(t, fixture.vmLeafPresent(testVMID))
		})
	}
}

func TestPreparedManagedVMCgroupBindForCleanupCopiedLeaseReleasesOnce(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	require.NoError(t, prepared.MarkJailerHandoff())
	fixture.addVMLeaf(testVMID)
	fixture.setMembership(testVMID, "")
	lease, present, err := prepared.BindForCleanup(t.Context())
	require.NoError(t, err)
	require.True(t, present)
	require.NoError(t, lease.Remove(t.Context()))

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
		return realClose(ctx, fd)
	}
	copied := *lease
	results := make(chan error, 8)
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
	for releaseErr := range results {
		require.NoError(t, releaseErr)
	}
	for fd := range retained {
		assert.Equal(t, 1, closed[fd])
	}
}

func TestPreparedManagedVMCgroupRecheckAbsentRejectsReentryWithoutHoldingLock(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	realOpenAt2 := prepared.state.deps.openAt2
	var callbackLockErr error
	var snapshot preparedManagedVMCgroupSnapshot
	var nestedBindErr error
	var nestedRecheckErr error
	var releaseErr error
	prepared.state.deps.openAt2 = func(
		ctx context.Context,
		fd int,
		name string,
		how *unix.OpenHow,
	) (int, error) {
		if name != testVMID {
			return realOpenAt2(ctx, fd, name, how)
		}
		if !prepared.state.mu.TryLock() {
			callbackLockErr = errors.New("absence dependency ran while prepared mutex was held")
			return -1, callbackLockErr
		}
		prepared.state.mu.Unlock()
		snapshot = prepared.snapshot()
		_, nestedBindErr = prepared.Bind(
			ctx,
			processTestLocator(),
			processTestCgroupObservation(),
			&managedVMCgroupTestProcessVerifier{},
		)
		nestedRecheckErr = prepared.RecheckAbsent(ctx)
		releaseErr = prepared.Release(ctx)
		return -1, unix.ENOENT
	}

	require.NoError(t, prepared.RecheckAbsent(t.Context()))
	assert.NoError(t, callbackLockErr)
	assert.Equal(t, preparedManagedVMCgroupSnapshot{leafAbsent: true}, snapshot)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(nestedBindErr).Code)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(nestedRecheckErr).Code)
	assert.Equal(t, errs.CodeVMStateInvalid, errs.AsDomainError(releaseErr).Code)
	require.NoError(t, prepared.RecheckAbsent(t.Context()))
	require.NoError(t, prepared.Release(t.Context()))
}
