package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	slot := prepared.slotLease.slot
	wantRelease := prepared.admission.identity

	launch, err := prepared.registerLaunching(
		context.Background(),
		testInstanceCaller(),
		registration,
	)
	require.NoError(t, err)
	require.NotNil(t, launch)
	assert.Nil(t, prepared.slotLease)
	assert.Nil(t, prepared.store)
	assert.Nil(t, prepared.directory)
	assert.Nil(t, prepared.admission)
	require.NotNil(t, launch.prepared)
	assert.NotNil(t, launch.prepared.store)
	assert.NotNil(t, launch.prepared.directory)
	assert.NotNil(t, launch.prepared.admission)
	assert.Equal(t, wantRelease, launch.record.launch.release)
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
	assert.NotNil(t, prepared.slotLease)
	assert.NotNil(t, prepared.store)
	assert.NotNil(t, prepared.directory)
	assert.NotNil(t, prepared.admission)
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
	assert.NotNil(t, prepared.slotLease)
	assert.NotNil(t, prepared.store)
	assert.NotNil(t, prepared.directory)
	assert.NotNil(t, prepared.admission)
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
	assert.Equal(t, instanceLifecycleLaunching, launch.record.lifecycle)
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
	activeRecord, err := activateInstanceRecord(launch.record, testActiveIdentityForLaunch(launch.record, false))
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
	assert.Equal(t, uint64(1), relaunched.record.cleanupGeneration)
	assert.Equal(t, uint64(2), relaunched.record.launchGeneration)
	assert.Equal(t, relaunchCaller.gid, relaunched.record.launch.ownerGID)
	assert.Equal(t, relaunchIntent.cacheIdentity, relaunched.record.launch.cacheIdentity)
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
	assert.NotNil(t, prepared.slotLease)
	assert.NotNil(t, prepared.admission)
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
	release := launch.record.launch.release
	require.NoError(t, launch.Release(context.Background()))

	slotLease, err := authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(release))
	require.NoError(t, err)
	err = slotLease.requireUnreferenced(context.Background(), release)
	require.Error(t, err)
	assert.Equal(t, errs.ClassConflict, errs.AsDomainError(err).Class)
	assert.NotNil(t, slotLease.roots)
	assert.NotNil(t, slotLease.releaseLock)
	require.NoError(t, slotLease.Release(context.Background()))
	activeRecord, err := activateInstanceRecord(launch.record, testActiveIdentityForLaunch(launch.record, false))
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
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), newTestInstanceAuthorityTree())
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
	assert.ErrorIs(t, err, syncErr)
	assert.Equal(t, true, domainErr.Details["record_replaced"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	assert.FileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
	assert.Nil(t, prepared.slotLease)
	assert.Nil(t, prepared.store)
	assert.Nil(t, prepared.directory)
	assert.Nil(t, prepared.admission)
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
