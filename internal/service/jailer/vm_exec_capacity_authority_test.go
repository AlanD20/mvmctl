package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/vmexecwire"
	"mvmctl/pkg/errs"
)

func TestVMExecCapacityAuthorityRequiresPositivePrivatePolicy(t *testing.T) {
	t.Parallel()
	// Rationale: a zero private ceiling would silently disable all admission instead of defining a usable authority.

	fixture := newVMExecCapacityFixture(t, 1)
	_, err := newVMExecCapacityAuthorityWithDeps(vmExecCapacityPolicy{}, fixture.deps())
	require.Error(t, err)
	assert.Equal(t, errs.ClassValidation, errs.AsDomainError(err).Class)
}

func TestVMExecCapacityAuthorityReservesRootGeneratedBootBoundRecord(t *testing.T) {
	t.Parallel()

	fixture := newVMExecCapacityFixture(t, 2)
	authority := fixture.authority(t)
	intent := testVMExecCapacityIntent(1000)
	lease, err := authority.reserve(t.Context(), intent)
	require.NoError(t, err)
	require.NotNil(t, lease)
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })

	want := testVMExecCapacityRecord()
	want.createdBoottimeNS = fixture.boottimeNS
	want.expiresBoottimeNS = fixture.boottimeNS + vmExecCapacityLifetimeNS
	if diff := cmp.Diff(want, lease.state.record, cmp.AllowUnexported(
		vmExecCapacityRecord{}, vmExecReservationID{},
	)); diff != "" {
		t.Errorf("reserved VM-exec capacity record mismatch (-want +got):\n%s", diff)
	}
	assert.FileExists(t, filepath.Join(
		fixture.root,
		"var/lib/mvmctl/allocations/exec/"+testVMExecReservationID+".json",
	))
	stopAt, timingErr := lease.relayStopBoottimeNS()
	require.NoError(t, timingErr)
	assert.Equal(t, fixture.boottimeNS+uint64(vmexecwire.MaxExecutionDuration), stopAt)
	assert.Equal(t, stopAt+uint64(vmExecCapacityTerminalCleanupGrace), lease.state.record.expiresBoottimeNS)
}

func TestVMExecCapacityAuthorityPrunesStaleThenEnforcesGlobalLimitAcrossOwners(t *testing.T) {
	t.Parallel()
	// Rationale: the global limit spans users and may count only records valid for the current boot and boottime.

	fixture := newVMExecCapacityFixture(t, 1)
	fixture.boottimeNS = vmExecCapacityLifetimeNS + 1
	store := fixture.openStore(t.Context())
	expired := testVMExecCapacityRecordWithID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	expired.createdBoottimeNS = 1
	expired.expiresBoottimeNS = expired.createdBoottimeNS + vmExecCapacityLifetimeNS
	priorBoot := testVMExecCapacityRecordWithID(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	priorBoot.hostBootID = "7d46d84c-f2ae-4abc-9abc-0123456789ab"
	require.NoError(t, store.createRecord(t.Context(), expired))
	require.NoError(t, store.createRecord(t.Context(), priorBoot))
	require.NoError(t, store.Release(t.Context()))

	authority := fixture.authority(t)
	first, err := authority.reserve(t.Context(), testVMExecCapacityIntent(1000))
	require.NoError(t, err)
	require.NotNil(t, first)
	t.Cleanup(func() { require.NoError(t, first.Release(context.Background())) })

	second, err := authority.reserve(t.Context(), testVMExecCapacityIntent(2000))
	assert.Nil(t, second)
	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeVMResourceExhausted, domainErr.Code)
	assert.Equal(t, errs.ClassConflict, domainErr.Class)
	assert.NoFileExists(t, filepath.Join(
		fixture.root,
		"var/lib/mvmctl/allocations/exec/"+expired.reservationID.value+".json",
	))
	assert.NoFileExists(t, filepath.Join(
		fixture.root,
		"var/lib/mvmctl/allocations/exec/"+priorBoot.reservationID.value+".json",
	))
}

func TestVMExecCapacityAuthoritiesSerializeGlobalCeilingAcrossIndependentInstances(t *testing.T) {
	t.Parallel()
	// Rationale: separate CLI processes construct independent authorities, so only the shared root lock may serialize
	// their read-check-create transition at a ceiling of one.

	fixture := newVMExecCapacityFixture(t, 1)
	authorities := []*vmExecCapacityAuthority{fixture.authority(t), fixture.authority(t)}
	start := make(chan struct{})
	type result struct {
		lease *vmExecCapacityLease
		err   error
	}
	results := make(chan result, len(authorities))
	for index, authority := range authorities {
		go func() {
			<-start
			lease, err := authority.reserve(context.Background(), testVMExecCapacityIntent(uint32(1000+index)))
			results <- result{lease: lease, err: err}
		}()
	}
	close(start)
	got := make([]result, 0, len(authorities))
	for range authorities {
		select {
		case item := <-results:
			got = append(got, item)
		case <-time.After(2 * time.Second):
			t.Fatal("independent capacity authorities did not complete")
		}
	}
	var admitted *vmExecCapacityLease
	exhausted := 0
	for _, item := range got {
		if item.lease != nil {
			require.NoError(t, item.err)
			require.Nil(t, admitted)
			admitted = item.lease
			continue
		}
		require.Error(t, item.err)
		assert.Equal(t, errs.CodeVMResourceExhausted, errs.AsDomainError(item.err).Code)
		exhausted++
	}
	require.NotNil(t, admitted)
	assert.Equal(t, 1, exhausted)
	require.NoError(t, admitted.Release(t.Context()))
}

func TestVMExecCapacityAuthorityFailsClosedOnCorruptDirectoryEntry(t *testing.T) {
	t.Parallel()
	// Rationale: an ambiguous root-owned entry must block admission without being silently removed.

	fixture := newVMExecCapacityFixture(t, 2)
	store := fixture.openStore(t.Context())
	require.NoError(t, store.Release(t.Context()))
	directory := filepath.Join(fixture.root, "var/lib/mvmctl/allocations/exec")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "ambiguous"), []byte("x"), 0600))

	lease, err := fixture.authority(t).reserve(t.Context(), testVMExecCapacityIntent(1000))
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
	entries, readErr := os.ReadDir(directory)
	require.NoError(t, readErr)
	require.Len(t, entries, 1)
	assert.Equal(t, "ambiguous", entries[0].Name())
}

func TestVMExecCapacityReleaseRequiresOwnerAndNeverDeletesAnotherRecord(t *testing.T) {
	t.Parallel()
	// Rationale: an exact ID is not sufficient authority to delete another unprivileged caller's reservation.

	fixture := newVMExecCapacityFixture(t, 2)
	authority := fixture.authority(t)
	first, err := authority.reserve(t.Context(), testVMExecCapacityIntent(1000))
	require.NoError(t, err)
	second, err := authority.reserve(t.Context(), testVMExecCapacityIntent(1000))
	require.NoError(t, err)

	err = authority.release(t.Context(), vmExecCapacityCaller{ownerUID: 2000}, first.state.record.reservationID)
	require.Error(t, err)
	assert.Equal(t, errs.CodeUnauthorized, errs.AsDomainError(err).Code)
	assert.FileExists(t, fixture.recordPath(first.state.record.reservationID))
	assert.FileExists(t, fixture.recordPath(second.state.record.reservationID))

	require.NoError(
		t,
		authority.release(t.Context(), vmExecCapacityCaller{ownerUID: 1000}, first.state.record.reservationID),
	)
	require.NoError(
		t,
		authority.release(t.Context(), vmExecCapacityCaller{ownerUID: 1000}, first.state.record.reservationID),
	)
	assert.NoFileExists(t, fixture.recordPath(first.state.record.reservationID))
	assert.FileExists(t, fixture.recordPath(second.state.record.reservationID))
	require.NoError(t, second.Release(t.Context()))
}

func TestVMExecCapacityPostRenameUncertaintyReturnsReleasableLease(t *testing.T) {
	t.Parallel()
	// Rationale: publication precedes directory durability, so uncertainty must still return a releasable exact lease.

	fixture := newVMExecCapacityFixture(t, 1)
	fixture.failDirectorySync = errors.New("injected directory sync failure")
	lease, err := fixture.authority(t).reserve(t.Context(), testVMExecCapacityIntent(1000))
	require.ErrorIs(t, err, fixture.failDirectorySync)
	require.NotNil(t, lease)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["record_created"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	assert.FileExists(t, fixture.recordPath(lease.state.record.reservationID))

	fixture.failDirectorySync = nil
	require.NoError(t, lease.Release(t.Context()))
	assert.NoFileExists(t, fixture.recordPath(lease.state.record.reservationID))
}

func TestVMExecCapacityLeaseConcurrentCopiedReleaseCachesDeletionUncertainty(t *testing.T) {
	t.Parallel()
	// Rationale: copied lease values may be released concurrently; they must perform one removal and share the exact
	// first uncertain result without losing deletion evidence.

	fixture := newVMExecCapacityFixture(t, 1)
	lease, err := fixture.authority(t).reserve(t.Context(), testVMExecCapacityIntent(1000))
	require.NoError(t, err)
	beforeOpen := fixture.openStoreCalls.Load()
	fixture.failDirectorySync = errors.New("injected release sync failure")
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	const releases = 12
	start := make(chan struct{})
	results := make(chan error, releases)
	for range releases {
		copyOfLease := *lease
		go func(value vmExecCapacityLease) {
			<-start
			results <- value.Release(cancelled)
		}(copyOfLease)
	}
	close(start)
	got := make([]error, 0, releases)
	for range releases {
		select {
		case releaseErr := <-results:
			got = append(got, releaseErr)
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent copied lease releases did not complete")
		}
	}
	first := got[0]
	require.ErrorIs(t, first, fixture.failDirectorySync)
	domainErr := errs.AsDomainError(first)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["record_deleted"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])
	for _, releaseErr := range got[1:] {
		assert.Same(t, first, releaseErr)
	}
	assert.Equal(t, beforeOpen+1, fixture.openStoreCalls.Load())
	assert.Equal(t, int32(1), fixture.recordUnlinkCalls.Load())
	assert.NoFileExists(t, fixture.recordPath(lease.state.record.reservationID))
}

func TestVMExecCapacityReleasePreservesDeletionEvidenceThroughCleanupFailures(t *testing.T) {
	t.Parallel()
	// Rationale: once the record is durably removed, a later descriptor or lock cleanup failure must not make callers
	// believe admission may still be occupied.

	tests := map[string]func(*vmExecCapacityFixture, error){
		"store":     func(fixture *vmExecCapacityFixture, err error) { fixture.failStoreRelease = err },
		"lock":      func(fixture *vmExecCapacityFixture, err error) { fixture.failLockRelease = err },
		"lock root": func(fixture *vmExecCapacityFixture, err error) { fixture.failLockRootRelease = err },
	}
	for name, inject := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newVMExecCapacityFixture(t, 1)
			lease, err := fixture.authority(t).reserve(t.Context(), testVMExecCapacityIntent(1000))
			require.NoError(t, err)
			fault := errors.New("injected release cleanup failure")
			inject(fixture, fault)

			err = lease.Release(t.Context())
			require.ErrorIs(t, err, fault)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, true, domainErr.Details["record_deleted"])
			assert.Equal(t, false, domainErr.Details["durability_uncertain"])
			assert.NoFileExists(t, fixture.recordPath(lease.state.record.reservationID))
		})
	}
}

func TestVMExecCapacityCollisionWithFailedCleanupDoesNotRetry(t *testing.T) {
	t.Parallel()
	// Rationale: an EEXIST collision is retryable only when its private temporary record was fully cleaned up; otherwise
	// another attempt would conceal a persistent ambiguous entry.

	fixture := newVMExecCapacityFixture(t, 2)
	store := fixture.openStore(t.Context())
	require.NoError(t, store.createRecord(t.Context(), testVMExecCapacityRecord()))
	require.NoError(t, store.Release(t.Context()))
	fixture.failTempCleanup = errors.New("injected collision cleanup failure")

	lease, err := fixture.authority(t).reserve(t.Context(), testVMExecCapacityIntent(1000))
	assert.Nil(t, lease)
	require.ErrorIs(t, err, unix.EEXIST)
	require.ErrorIs(t, err, fixture.failTempCleanup)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["cleanup_required"])
	fixture.idsMu.Lock()
	assert.Len(t, fixture.reservationIDs, 1)
	fixture.idsMu.Unlock()
}

func TestVMExecCapacityBoottimeEffectRejectsOverflow(t *testing.T) {
	t.Parallel()
	// Rationale: wrapped expiry would make a newly admitted reservation immediately pruneable.

	fixture := newVMExecCapacityFixture(t, 1)
	fixture.boottimeNS = ^uint64(0)
	lease, err := fixture.authority(t).reserve(t.Context(), testVMExecCapacityIntent(1000))
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

type vmExecCapacityFixture struct {
	root                string
	bootID              string
	boottimeNS          uint64
	reservationIDs      []string
	maxReservations     uint32
	lock                sync.Mutex
	idsMu               sync.Mutex
	failDirectorySync   error
	failTempCleanup     error
	failStoreRelease    error
	failLockRelease     error
	failLockRootRelease error
	openStoreCalls      atomic.Int32
	recordUnlinkCalls   atomic.Int32
}

func newVMExecCapacityFixture(t *testing.T, maxReservations uint32) *vmExecCapacityFixture {
	t.Helper()
	return &vmExecCapacityFixture{
		root:            prepareInstanceAuthorityTestRoot(t),
		bootID:          testVMExecCapacityRecord().hostBootID,
		boottimeNS:      1_000_000_000,
		reservationIDs:  []string{testVMExecReservationID, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		maxReservations: maxReservations,
	}
}

func (fixture *vmExecCapacityFixture) authority(t *testing.T) *vmExecCapacityAuthority {
	t.Helper()
	authority, err := newVMExecCapacityAuthorityWithDeps(
		vmExecCapacityPolicy{maxReservations: fixture.maxReservations},
		fixture.deps(),
	)
	require.NoError(t, err)
	return authority
}

func (fixture *vmExecCapacityFixture) deps() vmExecCapacityAuthorityDeps {
	return vmExecCapacityAuthorityDeps{
		openStore: func(ctx context.Context) (*vmExecCapacityStore, error) {
			fixture.openStoreCalls.Add(1)
			return fixture.openStore(ctx), nil
		},
		openLockRoot: func(context.Context) (vmExecCapacityLockRoot, error) {
			return &testVMExecCapacityLockRoot{
				mutex: &fixture.lock, releaseErr: fixture.failLockRootRelease,
				lockReleaseErr: fixture.failLockRelease,
			}, nil
		},
		currentBootID: func(context.Context) (string, error) { return fixture.bootID, nil },
		newReservationID: func(context.Context) (vmExecReservationID, error) {
			fixture.idsMu.Lock()
			defer fixture.idsMu.Unlock()
			if len(fixture.reservationIDs) == 0 {
				return vmExecReservationID{}, errors.New("reservation IDs exhausted")
			}
			value := fixture.reservationIDs[0]
			fixture.reservationIDs = fixture.reservationIDs[1:]
			return newVMExecReservationID(value)
		},
		clock: testVMExecCapacityClock{now: func(context.Context) (uint64, error) {
			return fixture.boottimeNS, nil
		}},
	}
}

func (fixture *vmExecCapacityFixture) openStore(ctx context.Context) *vmExecCapacityStore {
	deps := realVMExecCapacityStoreDeps()
	store, err := openVMExecCapacityStore(ctx, deps, vmExecCapacityStorePolicy{
		rootPath: fixture.root, stateRoot: vmExecCapacityStateRoot, expectedUID: uint32(os.Geteuid()),
		expectedGID: uint32(os.Getegid()),
	})
	if err != nil {
		panic(err)
	}
	realUnlink := store.state.deps.unlinkAt
	store.state.deps.unlinkAt = func(unlinkCtx context.Context, fd int, name string, flags int) error {
		if vmExecCapacityRecordNamePattern.MatchString(name) {
			fixture.recordUnlinkCalls.Add(1)
		}
		return realUnlink(unlinkCtx, fd, name, flags)
	}
	if fixture.failDirectorySync != nil {
		realSync := store.state.deps.fsync
		store.state.deps.fsync = func(syncCtx context.Context, fd int) error {
			if fd == store.state.stateFD {
				return fixture.failDirectorySync
			}
			return realSync(syncCtx, fd)
		}
	}
	if fixture.failTempCleanup != nil {
		countedUnlink := store.state.deps.unlinkAt
		store.state.deps.unlinkAt = func(unlinkCtx context.Context, fd int, name string, flags int) error {
			if vmExecCapacityTempNamePattern.MatchString(name) {
				return fixture.failTempCleanup
			}
			return countedUnlink(unlinkCtx, fd, name, flags)
		}
	}
	if fixture.failStoreRelease != nil {
		realClose := store.state.deps.close
		stateFD := store.state.stateFD
		store.state.deps.close = func(closeCtx context.Context, fd int) error {
			closeErr := realClose(closeCtx, fd)
			if fd == stateFD {
				return errors.Join(closeErr, fixture.failStoreRelease)
			}
			return closeErr
		}
	}
	return store
}

func (fixture *vmExecCapacityFixture) recordPath(id vmExecReservationID) string {
	return filepath.Join(fixture.root, "var/lib/mvmctl/allocations/exec", id.value+".json")
}

func testVMExecCapacityIntent(ownerUID uint32) vmExecCapacityIntent {
	return vmExecCapacityIntent{
		caller:                      vmExecCapacityCaller{ownerUID: ownerUID},
		sourceVMID:                  testVMID,
		sourceLaunchGeneration:      7,
		destinationVMID:             testVMExecDestinationID,
		destinationLaunchGeneration: 9,
	}
}

type testVMExecCapacityClock struct {
	now func(context.Context) (uint64, error)
}

func (clock testVMExecCapacityClock) boottimeNS(ctx context.Context) (uint64, error) {
	return clock.now(ctx)
}

type testVMExecCapacityLockRoot struct {
	mutex          *sync.Mutex
	releaseErr     error
	lockReleaseErr error
}

func (root *testVMExecCapacityLockRoot) acquire(context.Context) (vmExecCapacityLock, error) {
	root.mutex.Lock()
	return &testVMExecCapacityLock{mutex: root.mutex, releaseErr: root.lockReleaseErr}, nil
}

func (root *testVMExecCapacityLockRoot) Release(context.Context) error { return root.releaseErr }

type testVMExecCapacityLock struct {
	mutex      *sync.Mutex
	released   bool
	releaseErr error
}

func (lock *testVMExecCapacityLock) Release(context.Context) error {
	if !lock.released {
		lock.released = true
		lock.mutex.Unlock()
	}
	return lock.releaseErr
}
