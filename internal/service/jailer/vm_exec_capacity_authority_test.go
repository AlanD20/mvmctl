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

	"mvmctl/pkg/errs"
)

func TestVMExecCapacityAuthorityRequiresPositivePrivatePolicy(t *testing.T) {
	t.Parallel()

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
}

func TestVMExecCapacityAuthorityPrunesStaleThenEnforcesGlobalLimitAcrossOwners(t *testing.T) {
	t.Parallel()

	fixture := newVMExecCapacityFixture(t, 1)
	store := fixture.openStore(t.Context())
	expired := testVMExecCapacityRecordWithID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	expired.createdBoottimeNS = 1
	expired.expiresBoottimeNS = fixture.boottimeNS
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

func TestVMExecCapacityAuthorityFailsClosedOnCorruptDirectoryEntry(t *testing.T) {
	t.Parallel()

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

func TestVMExecCapacityLeaseCopiedReleaseCachesFirstResultWithoutCancellation(t *testing.T) {
	t.Parallel()

	fixture := newVMExecCapacityFixture(t, 1)
	lease, err := fixture.authority(t).reserve(t.Context(), testVMExecCapacityIntent(1000))
	require.NoError(t, err)
	copyOfLease := *lease
	fixture.failDirectorySync = errors.New("injected release sync failure")
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	first := lease.Release(cancelled)
	require.ErrorIs(t, first, fixture.failDirectorySync)
	second := copyOfLease.Release(t.Context())
	assert.Same(t, first, second)
	assert.NoFileExists(t, fixture.recordPath(lease.state.record.reservationID))
}

func TestVMExecCapacityBoottimeEffectRejectsOverflow(t *testing.T) {
	t.Parallel()

	fixture := newVMExecCapacityFixture(t, 1)
	fixture.boottimeNS = ^uint64(0)
	lease, err := fixture.authority(t).reserve(t.Context(), testVMExecCapacityIntent(1000))
	assert.Nil(t, lease)
	require.Error(t, err)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

type vmExecCapacityFixture struct {
	root              string
	bootID            string
	boottimeNS        uint64
	reservationIDs    []string
	maxReservations   uint32
	lock              sync.Mutex
	idsMu             sync.Mutex
	failDirectorySync error
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
			return fixture.openStore(ctx), nil
		},
		openLockRoot: func(context.Context) (vmExecCapacityLockRoot, error) {
			return &testVMExecCapacityLockRoot{mutex: &fixture.lock}, nil
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
	if fixture.failDirectorySync != nil {
		realSync := store.state.deps.fsync
		store.state.deps.fsync = func(syncCtx context.Context, fd int) error {
			if fd == store.state.stateFD {
				return fixture.failDirectorySync
			}
			return realSync(syncCtx, fd)
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
	mutex *sync.Mutex
}

func (root *testVMExecCapacityLockRoot) acquire(context.Context) (vmExecCapacityLock, error) {
	root.mutex.Lock()
	return &testVMExecCapacityLock{mutex: root.mutex}, nil
}

func (*testVMExecCapacityLockRoot) Release(context.Context) error { return nil }

type testVMExecCapacityLock struct {
	mutex    *sync.Mutex
	released bool
}

func (lock *testVMExecCapacityLock) Release(context.Context) error {
	if !lock.released {
		lock.released = true
		lock.mutex.Unlock()
	}
	return nil
}
