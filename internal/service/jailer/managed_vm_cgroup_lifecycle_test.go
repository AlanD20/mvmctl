package jailer

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingManagedVMCgroupTestVerifier struct {
	beforeEntered chan struct{}
	allowBefore   chan struct{}
	afterErr      error
}

func (verifier *blockingManagedVMCgroupTestVerifier) verifyBeforeCgroupMembership(
	context.Context,
	instanceProcessLocator,
	processObservation,
) error {
	close(verifier.beforeEntered)
	<-verifier.allowBefore
	return nil
}

func (verifier *blockingManagedVMCgroupTestVerifier) verifyAfterCgroupMembership(
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

func TestManagedVMCgroupVerifyMembershipSerializesCleanupAndDetectsPIDReuse(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	lease := fixture.bindLease(t, fixture.deps())
	pidReuseErr := errors.New("retained pidfd now identifies a reused task")
	verifier := &blockingManagedVMCgroupTestVerifier{
		beforeEntered: make(chan struct{}),
		allowBefore:   make(chan struct{}),
		afterErr:      pidReuseErr,
	}
	verifyResult := make(chan error, 1)
	go func() {
		verifyResult <- lease.VerifyMembership(
			t.Context(),
			processTestLocator(),
			processTestCgroupObservation(),
			verifier,
		)
	}()
	<-verifier.beforeEntered

	cleanupStarted := make(chan struct{}, 2)
	removeResult := make(chan error, 1)
	releaseResult := make(chan error, 1)
	go func() {
		cleanupStarted <- struct{}{}
		removeResult <- lease.Remove(t.Context())
	}()
	go func() {
		cleanupStarted <- struct{}{}
		releaseResult <- lease.Release(t.Context())
	}()
	<-cleanupStarted
	<-cleanupStarted
	select {
	case err := <-removeResult:
		t.Fatalf("Remove completed before membership verification: %v", err)
	case err := <-releaseResult:
		t.Fatalf("Release completed before membership verification: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(verifier.allowBefore)
	require.ErrorIs(t, <-verifyResult, pidReuseErr)
	require.Error(t, <-removeResult)
	require.Error(t, <-releaseResult)
	fixture.setMembership(testVMID, "")
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestPreparedManagedVMCgroupBindForCleanupReportsAbsentWithoutMutation(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)

	lease, present, err := prepared.BindForCleanup(t.Context())
	require.NoError(t, err)
	assert.Nil(t, lease)
	assert.False(t, present)
	assert.Equal(t, preparedManagedVMCgroupSnapshot{leafAbsent: true}, prepared.snapshot())
	require.NoError(t, prepared.Release(t.Context()))
	assert.False(t, fixture.vmLeafPresent(testVMID))
}

func TestPreparedManagedVMCgroupBindForCleanupTransfersPresentEmptyLeaf(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
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

func TestPreparedManagedVMCgroupBindForCleanupCopiedLeaseReleasesOnce(t *testing.T) {
	t.Parallel()

	fixture := newManagedVMCgroupTestFixture(t)
	prepared, err := prepareManagedVMCgroup(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
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
