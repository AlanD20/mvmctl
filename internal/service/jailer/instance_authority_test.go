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
	release := testLaunchIntent(false).release
	slot := releaseSlot{version: release.version, architecture: release.architecture}

	lease, err := authority.lockReleaseSlot(context.Background(), slot)
	require.NoError(t, err)
	require.NotNil(t, lease)
	assert.Equal(t, slot, lease.slot)
	require.NoError(t, lease.Release(context.Background()))
}

func TestReleaseSlotLeaseTransfersToSuccessfulLaunch(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	tree := newTestInstanceAuthorityTree()
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	registration := testLaunchIntent(false)
	slot := releaseSlotForIdentity(registration.release)
	slotLease, err := authority.lockReleaseSlot(
		context.Background(),
		slot,
	)
	require.NoError(t, err)

	launch, err := slotLease.registerLaunching(
		context.Background(),
		testInstanceCaller(),
		registration,
	)
	require.NoError(t, err)
	require.NotNil(t, launch)
	assert.Nil(t, slotLease.roots)
	assert.Nil(t, slotLease.releaseLock)
	assert.Equal(t, registration.release, launch.record.launch.release)
	assert.FileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
	require.NoError(t, slotLease.Release(context.Background()))

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
	release := testLaunchIntent(false).release
	slotLease, err := authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(release))
	require.NoError(t, err)

	require.NoError(t, slotLease.requireUnreferenced(context.Background(), release))
	require.NoError(t, slotLease.Release(context.Background()))
}

func TestReleaseSlotLeaseRejectsMismatchedLaunchWithoutWritingRecord(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	deps := realInstanceAuthorityDeps()
	tree := newTestInstanceAuthorityTree()
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
	registration := testLaunchIntent(false)
	slotLease, err := authority.lockReleaseSlot(
		context.Background(),
		releaseSlotForIdentity(registration.release),
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"release"}, tree.acquisitionSnapshot())

	registration.release.version = "1.17.0"
	launch, err := slotLease.registerLaunching(
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
	assert.NotNil(t, slotLease.roots)
	assert.NotNil(t, slotLease.releaseLock)
	require.NoError(t, slotLease.Release(context.Background()))
}

func TestReleaseSlotLeaseIndexFailureRetainsPreparedSlot(t *testing.T) {
	t.Parallel()

	tests := map[string]func(context.Context, *releaseSlotLease, launchIntent) error{
		"launch registration": func(
			ctx context.Context,
			lease *releaseSlotLease,
			registration launchIntent,
		) error {
			_, err := lease.registerLaunching(ctx, testInstanceCaller(), registration)
			return err
		},
		"release reference check": func(
			ctx context.Context,
			lease *releaseSlotLease,
			registration launchIntent,
		) error {
			return lease.requireUnreferenced(ctx, registration.release)
		},
	}
	for name, run := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareInstanceAuthorityTestRoot(t)
			deps := realInstanceAuthorityDeps()
			indexErr := errors.New("index lock failed")
			tree := newTestInstanceAuthorityTree()
			tree.indexErr = indexErr
			authority := newInstanceAuthorityWithPolicy(
				deps,
				testInstanceAuthorityPolicy(root),
				tree,
			)
			registration := testLaunchIntent(false)
			lease, err := authority.lockReleaseSlot(
				context.Background(),
				releaseSlotForIdentity(registration.release),
			)
			require.NoError(t, err)

			err = run(context.Background(), lease, registration)
			require.Error(t, err)
			assert.ErrorIs(t, err, indexErr)
			assert.Equal(t, []string{"release"}, tree.acquisitionSnapshot())
			assert.NotNil(t, lease.roots)
			assert.NotNil(t, lease.releaseLock)
			assert.NoFileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
			require.NoError(t, lease.Release(context.Background()))
		})
	}
}

func TestInstanceAuthorityReleaseSlotRegistersLaunchingAuthority(t *testing.T) {
	t.Parallel()

	authority, root := newTestInstanceAuthority(t)
	caller := testInstanceCaller()
	registration := testLaunchIntent(false)

	launch, err := registerTestLaunch(context.Background(), authority, caller, registration)
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

	duplicate, err := registerTestLaunch(context.Background(), authority, caller, registration)
	assert.Nil(t, duplicate)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAlreadyExists, errs.AsDomainError(err).Code)
}

func TestInstanceAuthorityCleanupAndSameOwnerRelaunch(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	caller := testInstanceCaller()
	launch, err := registerTestLaunch(context.Background(), authority, caller, testLaunchIntent(false))
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

	relaunched, err := registerTestLaunch(context.Background(), authority, caller, testLaunchIntent(false))
	require.NoError(t, err)
	assert.Equal(t, uint64(1), relaunched.record.cleanupGeneration)
	require.NoError(t, relaunched.Release(context.Background()))
}

func TestInstanceAuthorityRejectsForeignCaller(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	owner := testInstanceCaller()
	foreign := instanceCaller{uid: testAuthorityUID + 1, gid: testAuthorityGID}
	launch, err := registerTestLaunch(context.Background(), authority, owner, testLaunchIntent(false))
	require.NoError(t, err)
	require.NoError(t, launch.Release(context.Background()))

	foreignLaunch, err := registerTestLaunch(context.Background(), authority, foreign, testLaunchIntent(false))
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
			lease, err := registerTestLaunch(context.Background(), authority, caller, testLaunchIntent(false))
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
	slotLease, err := authority.lockReleaseSlot(
		context.Background(),
		releaseSlotForIdentity(registration.release),
	)
	require.NoError(t, err)

	lease, err := slotLease.registerLaunching(
		context.Background(),
		instanceCaller{uid: testAuthorityUID + 1, gid: testAuthorityGID},
		registration,
	)
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	assert.NotNil(t, slotLease.roots)
	assert.NotNil(t, slotLease.releaseLock)
	require.NoError(t, slotLease.Release(context.Background()))
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

	authority, _ := newTestInstanceAuthority(t)
	caller := testInstanceCaller()
	release := testLaunchIntent(false).release
	launch, err := registerTestLaunch(context.Background(), authority, caller, testLaunchIntent(false))
	require.NoError(t, err)
	require.NoError(t, launch.Release(context.Background()))

	slotLease, err := authority.lockReleaseSlot(context.Background(), releaseSlotForIdentity(release))
	require.NoError(t, err)
	err = slotLease.requireUnreferenced(context.Background(), release)
	require.Error(t, err)
	assert.Equal(t, errs.ClassConflict, errs.AsDomainError(err).Class)
	assert.NotNil(t, slotLease.roots)
	assert.NotNil(t, slotLease.releaseLock)
	require.NoError(t, slotLease.Release(context.Background()))

	cleanup, err := authority.BeginCleanup(context.Background(), caller, testVMID)
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
		releaseSlotForIdentity(testLaunchIntent(false).release),
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
	slot := releaseSlotForIdentity(testLaunchIntent(false).release)
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

	release := testLaunchIntent(false).release
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
	slotLease, err := authority.lockReleaseSlot(
		context.Background(),
		releaseSlotForIdentity(registration.release),
	)
	require.NoError(t, err)

	lease, err := slotLease.registerLaunching(
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
	slotLease, err := authority.lockReleaseSlot(
		context.Background(),
		releaseSlotForIdentity(registration.release),
	)
	require.NoError(t, err)

	lease, err := slotLease.registerLaunching(
		context.Background(),
		testInstanceCaller(),
		registration,
	)
	require.NoError(t, err)
	require.NoError(t, lease.Release(context.Background()))
	assert.Equal(t, []string{root}, opened)
}

func TestInstanceAuthorityPostRenameErrorReturnsNoLeaseAndDurableRecord(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	prepared := openTestInstanceAuthorityRoots(t, root)
	uidDirs, err := prepared.openUIDDirectories(context.Background(), testAuthorityUID)
	require.NoError(t, err)
	releaseLock, err := prepared.acquireReleaseSlotLock(
		context.Background(),
		releaseSlotForIdentity(testLaunchIntent(false).release),
	)
	require.NoError(t, err)
	indexLock, err := prepared.acquireIndexLock(context.Background())
	require.NoError(t, err)
	vmLock, err := uidDirs.acquireVMLock(context.Background(), testVMID)
	require.NoError(t, err)
	require.NoError(t, vmLock.Release(context.Background()))
	require.NoError(t, indexLock.Release(context.Background()))
	require.NoError(t, releaseLock.Release(context.Background()))
	require.NoError(t, uidDirs.Release(context.Background()))
	require.NoError(t, prepared.Release(context.Background()))
	deps := realInstanceAuthorityDeps()
	realFsync := deps.fsync
	var fileSynced atomic.Bool
	deps.fsync = func(ctx context.Context, fd int) error {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err == nil && stat.Mode&unix.S_IFMT == unix.S_IFREG {
			fileSynced.Store(true)
			return realFsync(ctx, fd)
		}
		if fileSynced.Load() {
			return errors.New("record directory sync failed")
		}
		return realFsync(ctx, fd)
	}
	authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), newTestInstanceAuthorityTree())
	registration := testLaunchIntent(false)
	slotLease, err := authority.lockReleaseSlot(
		context.Background(),
		releaseSlotForIdentity(registration.release),
	)
	require.NoError(t, err)

	lease, err := slotLease.registerLaunching(
		context.Background(),
		testInstanceCaller(),
		registration,
	)
	assert.Nil(t, lease)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["record_replaced"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	assert.FileExists(t, filepath.Join(root, "var/lib/mvmctl/instances/1000/"+testVMID+".json"))
	assert.NotNil(t, slotLease.roots)
	assert.NotNil(t, slotLease.releaseLock)
	require.NoError(t, slotLease.Release(context.Background()))
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
	ctx context.Context,
	authority *instanceAuthority,
	caller instanceCaller,
	registration launchIntent,
) (*launchLease, error) {
	slotLease, err := authority.lockReleaseSlot(ctx, releaseSlotForIdentity(registration.release))
	if err != nil {
		return nil, err
	}
	launch, err := slotLease.registerLaunching(ctx, caller, registration)
	if err != nil {
		return nil, appendInstanceOperationError(
			err,
			"release rejected test release slot lease",
			slotLease.Release(context.WithoutCancel(ctx)),
		)
	}
	return launch, nil
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
