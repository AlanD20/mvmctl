package jailer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestInstanceAuthorityLocksReleaseSlotBeforeIdentityResolution(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	release := testReleaseIdentity()
	slot := releaseSlot{version: release.version, architecture: release.architecture}

	lease, err := authority.lockReleaseSlot(context.Background(), slot)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Equal(t, slot, lease.slot)
	require.NoError(t, lease.Release(context.Background()))
}

func TestPreparedReleaseTransfersToSuccessfulLaunch(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	tree := newTestInstanceAuthorityTree()
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	registration := testLaunchIntent(false)
	prepared := prepareTestRelease(t, authority)
	preparedBefore := prepared.snapshot()
	slot := preparedBefore.slot
	wantRelease := preparedBefore.identity

	launch, err := prepared.registerLaunching(
		context.Background(),
		testInstanceCaller(),
		registration,
	)
	require.NoError(t, err)
	require.NotNil(t, launch)
	assertPreparedReleaseTransferred(t, prepared)
	launchSnapshot := launch.snapshot()
	assert.True(t, launchSnapshot.hasVMLock)
	assert.True(t, launchSnapshot.hasDirectories)
	assertPreparedReleaseSnapshotOpen(t, launchSnapshot.prepared)
	assert.Equal(t, wantRelease, launchSnapshot.record.launch.release)
	assert.FileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
	require.NoError(t, prepared.Release(context.Background()))

	type result struct {
		lease *releaseSlotLease
		err   error
	}
	resultCh := make(chan result, 1)
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		lease, lockErr := authority.lockReleaseSlot(waitCtx, slot)
		resultCh <- result{lease: lease, err: lockErr}
	}()
	select {
	case <-tree.releaseWaitObserved(t, slot):
	case <-waitCtx.Done():
		require.Failf(t, "transferred release lock contention was not observed", "error: %v", waitCtx.Err())
	}
	require.NoError(t, launch.Release(context.Background()))
	select {
	case acquired := <-resultCh:
		require.NoError(t, acquired.err)
		require.NotNil(t, acquired.lease)
		require.NoError(t, acquired.lease.Release(context.Background()))
	case <-waitCtx.Done():
		require.Failf(t, "transferred release lock remained blocked", "error: %v", waitCtx.Err())
	}
}

// Rationale: copied launch values must remain one capability. Concurrent cleanup must close every owned descriptor
// once, preserve reverse lock order, ignore caller cancellation, and return the exact first cleanup result to all copies.
func TestLaunchLeaseCopiesConcurrentlyReleaseExactlyOnceAndCacheError(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	tree := newTestInstanceAuthorityTree()
	releaseEvents := make(chan string, 3)
	tree.onRelease = func(_ context.Context, description string) error {
		releaseEvents <- description
		return nil
	}
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	prepared := prepareTestRelease(t, authority)
	launch, err := prepared.registerLaunching(context.Background(), testInstanceCaller(), testLaunchIntent(false))
	require.NoError(t, err)
	require.Equal(t, "index", <-releaseEvents)
	assert.Equal(t, []string{"release", "index", "vm"}, tree.acquisitionSnapshot())

	copied := *launch
	cleanupErr := errors.New("injected launch directory close failure")
	var targetCloseCount atomic.Int32
	state := launch.state
	state.mu.Lock()
	targetFD := state.directories.stateFD
	realClose := state.directories.deps.close
	state.directories.deps.close = func(ctx context.Context, fd int) error {
		closeErr := realClose(ctx, fd)
		if fd == targetFD {
			targetCloseCount.Add(1)
			return errors.Join(closeErr, cleanupErr)
		}
		return closeErr
	}
	state.mu.Unlock()

	const releases = 16
	results := make(chan error, releases)
	start := make(chan struct{})
	for index := range releases {
		candidate := launch
		if index%2 == 1 {
			candidate = &copied
		}
		go func() {
			<-start
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			results <- candidate.Release(ctx)
		}()
	}
	close(start)
	first := <-results
	require.Error(t, first)
	assert.ErrorIs(t, first, cleanupErr)
	for range releases - 1 {
		assert.Same(t, first, <-results)
	}
	domainErr := errs.AsDomainError(first)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["instance_authority_cleanup_failed"])
	detail, ok := domainErr.Details["instance_authority_cleanup_detail"].(string)
	require.True(t, ok)
	assert.Equal(t, "release instance UID directories: release instance UID directory descriptors", detail)
	assert.LessOrEqual(t, len(detail), maxInstanceAuthorityCleanupDetailBytes)
	assert.Equal(t, int32(1), targetCloseCount.Load())
	assert.Equal(t, "vm", <-releaseEvents)
	assert.Equal(t, "release", <-releaseEvents)
	assert.True(t, launch.snapshot().released)
	assert.True(t, copied.snapshot().released)
}

func TestPreparedReleaseSerializesReleaseAgainstLaunchingTransfer(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	tree := newTestInstanceAuthorityTree()
	indexAcquire := make(chan struct{})
	continueRegistration := make(chan struct{})
	tree.onAcquire = func(_ context.Context, description string) {
		if description == "index" {
			close(indexAcquire)
			<-continueRegistration
		}
	}
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	prepared := prepareTestRelease(t, authority)
	type launchResult struct {
		lease *launchLease
		err   error
	}
	launchResults := make(chan launchResult, 1)
	go func() {
		lease, err := prepared.registerLaunching(context.Background(), testInstanceCaller(), testLaunchIntent(false))
		launchResults <- launchResult{lease: lease, err: err}
	}()
	<-indexAcquire
	releaseStarted := make(chan struct{})
	releaseResults := make(chan error, 1)
	go func() {
		close(releaseStarted)
		releaseResults <- prepared.Release(context.Background())
	}()
	<-releaseStarted
	select {
	case err := <-releaseResults:
		require.Failf(t, "prepared release escaped serialized transfer", "release error: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(continueRegistration)
	result := <-launchResults
	require.NoError(t, result.err)
	require.NotNil(t, result.lease)
	require.NoError(t, <-releaseResults)
	assertPreparedReleaseTransferred(t, prepared)
	assertPreparedReleaseSnapshotOpen(t, result.lease.snapshot().prepared)
	require.NoError(t, result.lease.Release(context.Background()))
}

func TestReleaseSlotLeaseRequiresResolvedIdentityToBeUnreferenced(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	release := testReleaseIdentity()
	slotLease, err := authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(release))
	require.NoError(t, err)

	require.NoError(t, slotLease.requireUnreferenced(context.Background(), release))
	require.NoError(t, slotLease.Release(context.Background()))
}

func TestPreparedReleaseInvalidLaunchDoesNotConsumeAuthority(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	tree := newTestInstanceAuthorityTree()
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	registration := testLaunchIntent(false)
	registration.process.pid = 0
	prepared := prepareTestRelease(t, authority)
	assert.Equal(t, []string{"release"}, tree.acquisitionSnapshot())

	launch, err := prepared.registerLaunching(
		context.Background(),
		testInstanceCaller(),
		registration,
	)
	assert.Nil(t, launch)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, []string{"release"}, tree.acquisitionSnapshot())
	assert.NoFileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
	assertPreparedReleaseSnapshotOpen(t, prepared.snapshot())
	require.NoError(t, prepared.Release(context.Background()))
}

func TestPreparedReleaseIndexFailureRetainsPreparedAuthority(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	indexErr := errors.New("index lock failed")
	tree := newTestInstanceAuthorityTree()
	tree.indexErr = indexErr
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	prepared := prepareTestRelease(t, authority)

	launch, err := prepared.registerLaunching(context.Background(), testInstanceCaller(), testLaunchIntent(false))
	assert.Nil(t, launch)
	require.Error(t, err)
	assert.ErrorIs(t, err, indexErr)
	assert.Equal(t, []string{"release"}, tree.acquisitionSnapshot())
	assertPreparedReleaseSnapshotOpen(t, prepared.snapshot())
	assert.NoFileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
	require.NoError(t, prepared.Release(context.Background()))
}

func TestInstanceAuthorityReleaseSlotRegistersLaunchingAuthority(t *testing.T) {
	t.Parallel()

	authority, root := newTestInstanceAuthority(t)
	caller := testInstanceCaller()
	registration := testLaunchIntent(false)

	launch, err := registerTestLaunch(t, context.Background(), authority, caller, registration)
	require.NoError(t, err)
	launchRecord := launch.snapshot().record
	assert.Equal(t, instanceLifecycleLaunching, launchRecord.lifecycle)
	assert.FileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))

	waitCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	blocked, err := authority.LockActive(waitCtx, caller, testVMID)
	assert.Nil(t, blocked)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, launch.Release(context.Background()))

	active, err := authority.LockActive(context.Background(), caller, testVMID)
	assert.Nil(t, active)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMNotRunning, errs.AsDomainError(err).Code)
	activeRecord, err := activateInstanceRecord(launchRecord, testActiveIdentityForLaunch(launchRecord, false))
	require.NoError(t, err)
	writeTestAuthorityRecord(t, root, activeRecord)

	active, err = authority.LockActive(context.Background(), caller, testVMID)
	require.NoError(t, err)
	if diff := diffInstanceRecord(activeRecord, active.record); diff != "" {
		t.Errorf("locked active record mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, active.Release(context.Background()))

	duplicate, err := registerTestLaunch(t, context.Background(), authority, caller, registration)
	assert.Nil(t, duplicate)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAlreadyExists, errs.AsDomainError(err).Code)
}

func TestInstanceAuthorityCleanupAndSameOwnerRelaunch(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	caller := testInstanceCaller()
	launch, err := registerTestLaunch(t, context.Background(), authority, caller, testLaunchIntent(false))
	require.NoError(t, err)
	require.NoError(t, launch.Release(context.Background()))

	cleanup, err := authority.BeginCleanup(context.Background(), caller, testVMID)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleCleaning, cleanup.record.lifecycle)
	assert.Equal(t, uint64(1), cleanup.record.cleanupGeneration)
	require.NoError(t, cleanup.Complete(context.Background()))
	assert.Equal(t, instanceLifecycleCleaned, cleanup.record.lifecycle)
	require.NoError(t, cleanup.Release(context.Background()))

	active, err := authority.LockActive(context.Background(), caller, testVMID)
	assert.Nil(t, active)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMNotRunning, errs.AsDomainError(err).Code)

	relaunchCaller := instanceCaller{uid: caller.uid, gid: caller.gid + 1}
	relaunchIntent := testLaunchIntent(false)
	relaunchIntent.cacheIdentity.inode++
	relaunched, err := registerTestLaunch(t, context.Background(), authority, relaunchCaller, relaunchIntent)
	require.NoError(t, err)
	relaunchedRecord := relaunched.snapshot().record
	assert.Equal(t, uint64(1), relaunchedRecord.cleanupGeneration)
	assert.Equal(t, uint64(2), relaunchedRecord.launchGeneration)
	assert.Equal(t, relaunchCaller.gid, relaunchedRecord.launch.ownerGID)
	assert.Equal(t, relaunchIntent.cacheIdentity, relaunchedRecord.launch.cacheIdentity)
	require.NoError(t, relaunched.Release(context.Background()))
}

func TestInstanceAuthorityRejectsForeignCaller(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	owner := testInstanceCaller()
	foreign := instanceCaller{uid: testAuthorityUID + 1, gid: testAuthorityGID}
	launch, err := registerTestLaunch(t, context.Background(), authority, owner, testLaunchIntent(false))
	require.NoError(t, err)
	require.NoError(t, launch.Release(context.Background()))

	foreignLaunch, err := registerTestLaunch(t, context.Background(), authority, foreign, testLaunchIntent(false))
	assert.Nil(t, foreignLaunch)
	require.Error(t, err)
	assert.Equal(t, errs.CodeUnauthorized, errs.AsDomainError(err).Code)
	assert.NotContains(t, err.Error(), "1000")

	active, err := authority.LockActive(context.Background(), foreign, testVMID)
	assert.Nil(t, active)
	require.Error(t, err)
	assert.Equal(t, errs.CodeUnauthorized, errs.AsDomainError(err).Code)

	cleanup, err := authority.BeginCleanup(context.Background(), foreign, testVMID)
	assert.Nil(t, cleanup)
	require.Error(t, err)
	assert.Equal(t, errs.CodeUnauthorized, errs.AsDomainError(err).Code)
}

func TestInstanceAuthorityCrossUIDFirstLaunchRaceHasOneOwner(t *testing.T) {
	t.Parallel()

	authority, root := newTestInstanceAuthority(t)
	callers := []instanceCaller{{uid: 1000, gid: 1000}, {uid: 1001, gid: 1001}}
	start := make(chan struct{})
	results := make(chan error, len(callers))
	var success atomic.Int32
	for _, caller := range callers {
		go func() {
			<-start
			lease, err := registerTestLaunch(t, context.Background(), authority, caller, testLaunchIntent(false))
			if err == nil {
				success.Add(1)
				err = lease.Release(context.Background())
			}
			results <- err
		}()
	}
	close(start)

	errorsSeen := make([]error, 0, len(callers))
	for range callers {
		errorsSeen = append(errorsSeen, <-results)
	}
	assert.Equal(t, int32(1), success.Load())
	unauthorized := 0
	for _, err := range errorsSeen {
		if domainErr := errs.AsDomainError(err); domainErr != nil && domainErr.Code == errs.CodeUnauthorized {
			unauthorized++
		}
	}
	assert.Equal(t, 1, unauthorized)

	records, err := filepath.Glob(filepath.Join(root, "var/lib/mvmctl/instances/*/"+testVMID+".json"))
	require.NoError(t, err)
	assert.Len(t, records, 1)
}

func TestInstanceAuthorityRejectsDuplicateGlobalClaim(t *testing.T) {
	t.Parallel()

	authority, root := newTestInstanceAuthority(t)
	writeTestAuthorityRecord(t, root, testLaunchingInstanceRecord())
	duplicate := testLaunchingInstanceRecord()
	duplicate.ownerUID = testAuthorityUID + 1
	writeTestAuthorityRecord(t, root, duplicate)

	lease, err := authority.LockActive(
		context.Background(),
		testInstanceCaller(),
		testVMID,
	)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func TestInstanceAuthorityRejectsCorruptGlobalClaim(t *testing.T) {
	t.Parallel()

	authority, root := newTestInstanceAuthority(t)
	recordDir := filepath.Join(root, "var/lib/mvmctl/instances/1000")
	require.NoError(t, os.MkdirAll(recordDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(recordDir, testVMID+".json"), []byte(`{"broken":true}`), 0600))
	registration := testLaunchIntent(false)
	prepared := prepareTestRelease(t, authority)

	lease, err := prepared.registerLaunching(
		context.Background(),
		instanceCaller{uid: testAuthorityUID + 1, gid: testAuthorityGID},
		registration,
	)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	assertPreparedReleaseSnapshotOpen(t, prepared.snapshot())
	require.NoError(t, prepared.Release(context.Background()))
}

func TestInstanceAuthorityRejectsRecordDirectoryIdentityMismatch(t *testing.T) {
	t.Parallel()

	authority, root := newTestInstanceAuthority(t)
	record := testLaunchingInstanceRecord()
	record.ownerUID = testAuthorityUID + 1
	raw, err := encodeInstanceRecord(record)
	require.NoError(t, err)
	recordDir := filepath.Join(root, "var/lib/mvmctl/instances/1000")
	require.NoError(t, os.MkdirAll(recordDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(recordDir, testVMID+".json"), raw, 0600))

	lease, err := authority.LockActive(
		context.Background(),
		testInstanceCaller(),
		testVMID,
	)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func TestInstanceAuthorityReleaseSlotReferenceCheckTracksLifecycle(t *testing.T) {
	t.Parallel()

	authority, root := newTestInstanceAuthority(t)
	caller := testInstanceCaller()
	launch, err := registerTestLaunch(t, context.Background(), authority, caller, testLaunchIntent(false))
	require.NoError(t, err)
	launchRecord := launch.snapshot().record
	release := launchRecord.launch.release
	require.NoError(t, launch.Release(context.Background()))

	slotLease, err := authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(release))
	require.NoError(t, err)
	err = slotLease.requireUnreferenced(context.Background(), release)
	require.Error(t, err)
	assert.Equal(t, errs.ClassConflict, errs.AsDomainError(err).Class)
	assert.NotNil(t, slotLease.roots)
	assert.NotNil(t, slotLease.releaseLock)
	require.NoError(t, slotLease.Release(context.Background()))
	activeRecord, err := activateInstanceRecord(launchRecord, testActiveIdentityForLaunch(launchRecord, false))
	require.NoError(t, err)
	writeTestAuthorityRecord(t, root, activeRecord)

	slotLease, err = authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(release))
	require.NoError(t, err)
	err = slotLease.requireUnreferenced(context.Background(), release)
	require.Error(t, err)
	assert.Equal(t, errs.ClassConflict, errs.AsDomainError(err).Class)
	require.NoError(t, slotLease.Release(context.Background()))

	cleanup, err := authority.BeginCleanup(context.Background(), caller, testVMID)
	require.NoError(t, err)
	require.NoError(t, cleanup.Release(context.Background()))

	slotLease, err = authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(release))
	require.NoError(t, err)
	err = slotLease.requireUnreferenced(context.Background(), release)
	require.Error(t, err)
	assert.Equal(t, errs.ClassConflict, errs.AsDomainError(err).Class)
	require.NoError(t, slotLease.Release(context.Background()))

	cleanup, err = authority.BeginCleanup(context.Background(), caller, testVMID)
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(context.Background()))
	require.NoError(t, cleanup.Release(context.Background()))

	slotLease, err = authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(release))
	require.NoError(t, err)
	require.NoError(t, slotLease.requireUnreferenced(context.Background(), release))
	require.NoError(t, slotLease.Release(context.Background()))
}

func TestInstanceAuthorityReleaseScanCreatesLockUnderAuthorityRoot(t *testing.T) {
	t.Parallel()

	// Rationale: Release scans create their serialization lock lazily. It must share the private authority tree and
	// never recreate the former top-level UID layout.
	root := prepareInstanceAuthorityTestRoot(t)
	record := testLaunchingInstanceRecord()
	raw, err := encodeInstanceRecord(record)
	require.NoError(t, err)
	recordDir := filepath.Join(root, "var/lib/mvmctl/instances/1000")
	require.NoError(t, os.MkdirAll(recordDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(recordDir, testVMID+".json"), raw, 0600))
	authority := newInstanceAuthorityWithPolicy(
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
		newTestInstanceAuthorityTree(),
	)

	lease, err := authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(record.launch.release))
	require.NoError(t, err)
	err = lease.requireUnreferenced(context.Background(), record.launch.release)
	require.Error(t, err)
	assert.Equal(t, errs.ClassConflict, errs.AsDomainError(err).Class)
	require.NoError(t, lease.Release(context.Background()))

	assert.FileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
}

func TestInstanceAuthorityReleaseSlotLeaseSerializesCompetingIdentities(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	tree := newTestInstanceAuthorityTree()
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	held, err := authority.lockReleaseSlot(
		context.Background(),
		releaseSlotForIdentity(testReleaseIdentity()),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		if held != nil {
			require.NoError(t, held.Release(context.Background()))
		}
	})

	type result struct {
		lease *releaseSlotLease
		err   error
	}
	resultCh := make(chan result, 1)
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		lease, lockErr := authority.lockReleaseSlot(
			waitCtx,
			releaseSlotForIdentity(testAlternateReleaseIdentity()),
		)
		resultCh <- result{lease: lease, err: lockErr}
	}()

	select {
	case <-tree.releaseWaitObserved(t, held.slot):
	case <-waitCtx.Done():
		require.Failf(t, "release lock contention was not observed", "error: %v", waitCtx.Err())
	}

	require.NoError(t, held.Release(context.Background()))
	held = nil
	select {
	case acquired := <-resultCh:
		require.NoError(t, acquired.err)
		require.NotNil(t, acquired.lease)
		require.NoError(t, acquired.lease.Release(context.Background()))
	case <-waitCtx.Done():
		require.Failf(t, "release lock remained blocked", "error: %v", waitCtx.Err())
	}
}

func TestInstanceAuthorityReleaseSlotWaitIsCancellableAndOwnerRemainsActive(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	tree := newTestInstanceAuthorityTree()
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	slot := releaseSlotForIdentity(testReleaseIdentity())
	held, err := authority.lockReleaseSlot(context.Background(), slot)
	require.NoError(t, err)

	type result struct {
		lease *releaseSlotLease
		err   error
	}
	resultCh := make(chan result, 1)
	waitCtx, cancel := context.WithCancel(context.Background())
	go func() {
		lease, lockErr := authority.lockReleaseSlot(waitCtx, slot)
		resultCh <- result{lease: lease, err: lockErr}
	}()

	select {
	case <-tree.releaseWaitObserved(t, slot):
	case <-time.After(time.Second):
		require.Fail(t, "release slot lock contention was not observed")
	}
	cancel()
	blocked := <-resultCh
	assert.Nil(t, blocked.lease)
	require.Error(t, blocked.err)
	assert.ErrorIs(t, blocked.err, context.Canceled)
	assert.NotNil(t, held.roots)
	assert.NotNil(t, held.releaseLock)
	require.NoError(t, held.Release(context.Background()))

	reacquired, err := authority.lockReleaseSlot(context.Background(), slot)
	require.NoError(t, err)
	require.NoError(t, reacquired.Release(context.Background()))
}

func TestInstanceAuthorityReleaseSlotReferenceCheckRejectsCorruptAuthorityRecord(t *testing.T) {
	t.Parallel()

	authority, root := newTestInstanceAuthority(t)
	recordDir := filepath.Join(root, "var/lib/mvmctl/instances/1000")
	require.NoError(t, os.MkdirAll(recordDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(recordDir, testVMID+".json"), []byte(`{"broken":true}`), 0600))

	release := testReleaseIdentity()
	lease, err := authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(release))
	require.NoError(t, err)
	err = lease.requireUnreferenced(context.Background(), release)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	assert.NotNil(t, lease.roots)
	assert.NotNil(t, lease.releaseLock)
	require.NoError(t, lease.Release(context.Background()))
}

func TestInstanceAuthorityRegisterLockOrder(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	tree := newTestInstanceAuthorityTree()
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	registration := testLaunchIntent(false)
	prepared := prepareTestRelease(t, authority)

	lease, err := prepared.registerLaunching(
		context.Background(),
		testInstanceCaller(),
		registration,
	)
	require.NoError(t, err)
	require.NoError(t, lease.Release(context.Background()))

	assert.Equal(t, []string{"release", "index", "vm"}, tree.acquisitionSnapshot())
}

func TestInstanceAuthorityUsesOnlyFixedRootPathOpen(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	realOpen := deps.open
	opened := make([]string, 0, 1)
	deps.open = func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
		opened = append(opened, path)
		return realOpen(ctx, path, flags, mode)
	}
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), newTestInstanceAuthorityTree())
	registration := testLaunchIntent(false)
	prepared := prepareTestRelease(t, authority)

	lease, err := prepared.registerLaunching(
		context.Background(),
		testInstanceCaller(),
		registration,
	)
	require.NoError(t, err)
	require.NoError(t, lease.Release(context.Background()))
	assert.Equal(t, []string{root}, opened)
}

func TestPreparedReleaseRegistrationFailuresBeforeRenameRetainAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*instanceAuthorityDeps, error){
		"write": func(deps *instanceAuthorityDeps, fault error) {
			deps.write = func(context.Context, int, []byte) (int, error) { return 0, fault }
		},
		"file fsync": func(deps *instanceAuthorityDeps, fault error) {
			realFsync := deps.fsync
			deps.fsync = func(ctx context.Context, fd int) error {
				var stat unix.Stat_t
				if err := unix.Fstat(fd, &stat); err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG {
					return fault
				}
				return realFsync(ctx, fd)
			}
		},
		"rename": func(deps *instanceAuthorityDeps, fault error) {
			deps.renameAt = func(context.Context, int, string, int, string) error { return fault }
		},
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			root := prepareInstanceAuthorityTestRoot(t)
			deps := realInstanceAuthorityDeps()
			fault := errors.New("injected " + name + " failure")
			inject(&deps, fault)
			authority := newInstanceAuthorityWithPolicy(
				deps,
				testInstanceAuthorityPolicy(root),
				newTestInstanceAuthorityTree(),
			)
			prepared := prepareTestRelease(t, authority)

			launch, err := prepared.registerLaunching(
				context.Background(),
				testInstanceCaller(),
				testLaunchIntent(false),
			)
			assert.Nil(t, launch)
			require.Error(t, err)
			assert.ErrorIs(t, err, fault)
			assertPreparedReleaseSnapshotOpen(t, prepared.snapshot())
			assert.NoFileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
			require.NoError(t, prepared.Release(context.Background()))
		})
	}
}

func TestInstanceAuthorityPostRenameErrorReturnsLaunchLeaseAndDurableRecord(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	authorityRoots := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := authorityRoots.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	releaseLock, err := authorityRoots.acquireReleaseSlotLock(
		context.Background(),
		releaseSlotForIdentity(testReleaseIdentity()),
	)
	require.NoError(t, err)
	indexLock, err := authorityRoots.acquireIndexLock(context.Background())
	require.NoError(t, err)
	vmLock, err := uidDirs.acquireVMLock(context.Background(), testVMID)
	require.NoError(t, err)
	require.NoError(t, vmLock.Release(context.Background()))
	require.NoError(t, indexLock.Release(context.Background()))
	require.NoError(t, releaseLock.Release(context.Background()))
	require.NoError(t, uidDirs.Release(context.Background()))
	require.NoError(t, authorityRoots.Release(context.Background()))
	deps := realInstanceAuthorityDeps()
	realFsync := deps.fsync
	syncErr := errors.New("record directory sync failed")
	indexErr := errors.New("index release failed")
	var fileSynced atomic.Bool
	deps.fsync = func(ctx context.Context, fd int) error {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG {
			fileSynced.Store(true)
			return realFsync(ctx, fd)
		}
		if fileSynced.Load() {
			return syncErr
		}
		return realFsync(ctx, fd)
	}
	tree := newTestInstanceAuthorityTree()
	tree.lockReleaseErrDescription = "index"
	tree.lockReleaseErr = indexErr
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	registration := testLaunchIntent(false)
	prepared := prepareTestRelease(t, authority)

	lease, err := prepared.registerLaunching(
		context.Background(),
		testInstanceCaller(),
		registration,
	)
	require.NotNil(t, lease)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Empty(t, domainErr.Entity)
	assert.Equal(t, "vm", domainErr.Op)
	assert.Equal(t, "sync instance authority record directory", domainErr.Message)
	assert.ErrorIs(t, err, syncErr)
	assert.ErrorIs(t, err, indexErr)
	assert.Equal(t, true, domainErr.Details["record_replaced"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	assert.Equal(t, true, domainErr.Details["instance_authority_cleanup_failed"])
	detail, ok := domainErr.Details["instance_authority_cleanup_detail"].(string)
	require.True(t, ok)
	assert.Equal(
		t,
		"release global index lock after launching record replacement: release authority lock: index release failed",
		detail,
	)
	assert.LessOrEqual(t, len(detail), maxInstanceAuthorityCleanupDetailBytes)
	recordPath := filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json")
	raw, readErr := os.ReadFile(recordPath)
	require.NoError(t, readErr)
	persisted, decodeErr := decodeInstanceRecord(raw)
	require.NoError(t, decodeErr)
	leaseSnapshot := lease.snapshot()
	assertInstanceRecordEqual(t, leaseSnapshot.record, persisted)
	assertPreparedReleaseSnapshotOpen(t, leaseSnapshot.prepared)
	assertPreparedReleaseTransferred(t, prepared)
	require.NoError(t, prepared.Release(context.Background()))
	require.NoError(t, lease.Release(context.Background()))
}

func TestInstanceAuthorityPostRenameCleanupErrorReturnsCleanupLease(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	writeTestAuthorityRecord(t, root, testLaunchingInstanceRecord())
	deps := realInstanceAuthorityDeps()
	realFsync := deps.fsync
	syncErr := errors.New("cleanup record directory sync failed")
	var fileSynced atomic.Bool
	deps.fsync = func(ctx context.Context, fd int) error {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG {
			fileSynced.Store(true)
			return realFsync(ctx, fd)
		}
		if fileSynced.Load() {
			return syncErr
		}
		return realFsync(ctx, fd)
	}
	authority := newInstanceAuthorityWithPolicy(
		deps,
		testInstanceAuthorityPolicy(root),
		newTestInstanceAuthorityTree(),
	)

	cleanup, err := authority.BeginCleanup(context.Background(), testInstanceCaller(), testVMID)
	require.NotNil(t, cleanup)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMAtomicFailed, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Empty(t, domainErr.Entity)
	assert.ErrorIs(t, err, syncErr)
	assert.Equal(t, true, domainErr.Details["record_replaced"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	assert.Equal(t, instanceLifecycleCleaning, cleanup.record.lifecycle)
	require.NoError(t, cleanup.Release(context.Background()))

	stableAuthority := newInstanceAuthorityWithPolicy(
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
		newTestInstanceAuthorityTree(),
	)
	resumed, err := stableAuthority.BeginCleanup(context.Background(), testInstanceCaller(), testVMID)
	require.NoError(t, err)
	assert.Equal(t, instanceLifecycleCleaning, resumed.record.lifecycle)
	require.NoError(t, resumed.Release(context.Background()))
}

func TestAppendInstanceOperationErrorClonesMetadataAndBoundsFirstDiagnostic(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("primary cause")
	siblingCause := errors.New("sibling cause")
	cleanupCause := errors.New(strings.Repeat("cleanup", 200))
	originalDetails := map[string]any{"record_replaced": true}
	primary := errs.WrapMsg(
		errs.CodeVMAtomicFailed,
		"stable primary message",
		primaryCause,
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity(testVMID),
		errs.WithDetails(originalDetails),
	)
	primary.Op = "original-op"
	wrapper := fmt.Errorf("outer wrapper: %w", errors.Join(primary, siblingCause))

	got := appendInstanceOperationError(wrapper, "release first resource", cleanupCause)
	domainErr := errs.AsDomainError(got)
	require.NotNil(t, domainErr)
	assert.NotSame(t, primary, domainErr)
	assert.Equal(t, primary.Code, domainErr.Code)
	assert.Equal(t, primary.Class, domainErr.Class)
	assert.Equal(t, primary.Entity, domainErr.Entity)
	assert.Equal(t, primary.Op, domainErr.Op)
	assert.Equal(t, primary.Message, domainErr.Message)
	assert.Equal(t, map[string]any{"record_replaced": true}, primary.Details)
	assert.Equal(t, "stable primary message", primary.Message)
	assert.ErrorIs(t, got, primaryCause)
	assert.ErrorIs(t, got, siblingCause)
	assert.ErrorIs(t, got, cleanupCause)
	assert.Equal(t, true, domainErr.Details["instance_authority_cleanup_failed"])
	detail, ok := domainErr.Details["instance_authority_cleanup_detail"].(string)
	require.True(t, ok)
	assert.LessOrEqual(t, len(detail), maxInstanceAuthorityCleanupDetailBytes)
	assert.True(t, strings.HasPrefix(detail, "release first resource: cleanup"))

	secondCleanup := errors.New("second cleanup cause")
	combined := appendInstanceOperationError(got, "release second resource", secondCleanup)
	combinedDomain := errs.AsDomainError(combined)
	require.NotNil(t, combinedDomain)
	assert.Equal(t, detail, combinedDomain.Details["instance_authority_cleanup_detail"])
	assert.ErrorIs(t, combined, secondCleanup)
}

func newTestInstanceAuthority(t *testing.T) (*instanceAuthority, string) {
	t.Helper()

	root := prepareInstanceAuthorityTestRoot(t)
	return newInstanceAuthorityWithPolicy(
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
		newTestInstanceAuthorityTree(),
	), root
}

func registerTestLaunch(
	t *testing.T,
	ctx context.Context,
	authority *instanceAuthority,
	caller instanceCaller,
	registration launchIntent,
) (*launchLease, error) {
	t.Helper()

	prepared := prepareTestRelease(t, authority)
	launch, err := prepared.registerLaunching(ctx, caller, registration)
	if err != nil && launch == nil {
		return nil, appendInstanceOperationError(
			err,
			"release rejected test prepared release",
			prepared.Release(context.WithoutCancel(ctx)),
		)
	}
	return launch, err
}

func prepareTestRelease(t *testing.T, authority *instanceAuthority) *preparedRelease {
	t.Helper()

	release := newTrustedReleaseExecutableFixture(t)
	writeTrustedReleaseManifestFile(t, release.store.slotPath, release.manifest)
	prepared, err := newReleaseAuthorityWithPolicy(
		authority,
		release.store.deps,
		release.store.policy,
	).prepareInstalled(t.Context(), release.store.slot)
	require.NoError(t, err)
	return prepared
}

func testActiveIdentityForLaunch(record instanceRecord, relayPresent bool) instanceActiveIdentity {
	identity := testActiveIdentity(relayPresent)
	identity.executable.manifestSHA256 = record.launch.release.firecrackerSHA256
	return identity
}

func assertPreparedReleaseSnapshotOpen(t *testing.T, snapshot preparedReleaseSnapshot) {
	t.Helper()

	assert.Equal(t, preparedReleaseOpen, snapshot.status)
	assert.True(t, snapshot.hasSlotLease)
	assert.True(t, snapshot.releaseLockActive)
	assert.True(t, snapshot.hasStore)
	assert.True(t, snapshot.hasDirectory)
	assert.True(t, snapshot.hasAdmission)
	assert.True(t, snapshot.hasExecutables)
	assert.GreaterOrEqual(t, snapshot.firecrackerFD, 0)
	assert.GreaterOrEqual(t, snapshot.jailerFD, 0)
}

func assertPreparedReleaseTransferred(t *testing.T, prepared *preparedRelease) {
	t.Helper()

	snapshot := prepared.snapshot()
	assert.Equal(t, preparedReleaseTransferred, snapshot.status)
	assert.False(t, snapshot.hasSlotLease)
	assert.False(t, snapshot.releaseLockActive)
	assert.False(t, snapshot.hasStore)
	assert.False(t, snapshot.hasDirectory)
	assert.False(t, snapshot.hasAdmission)
	assert.False(t, snapshot.hasExecutables)
}

func writeTestAuthorityRecord(t *testing.T, root string, record instanceRecord) {
	t.Helper()

	roots, err := openInstanceAuthorityRoots(
		context.Background(),
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
		newTestInstanceAuthorityTree(),
	)
	require.NoError(t, err)
	uidDirs, err := roots.openUIDDirectories(context.Background(), record.ownerUID)
	require.NoError(t, err)
	require.NoError(t, uidDirs.writeRecord(context.Background(), record))
	require.NoError(t, uidDirs.Release(context.Background()))
	require.NoError(t, roots.Release(context.Background()))
}
