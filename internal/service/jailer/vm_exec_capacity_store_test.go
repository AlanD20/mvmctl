package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

func TestVMExecCapacityStoreCreatesExactTreeAndRoundTripsRecord(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	store := openTestVMExecCapacityStore(t, root)
	record := testVMExecCapacityRecord()
	require.NoError(t, store.createRecord(t.Context(), record))

	got, found, err := store.readRecord(t.Context(), record.reservationID)
	require.NoError(t, err)
	assert.True(t, found)
	if diff := cmp.Diff(record, got, cmp.AllowUnexported(vmExecCapacityRecord{}, vmExecReservationID{})); diff != "" {
		t.Errorf("stored VM-exec capacity record mismatch (-want +got):\n%s", diff)
	}

	directory := filepath.Join(root, "var/lib/mvmctl/allocations/exec")
	assertExactMode(t, filepath.Join(root, "var/lib/mvmctl"), 0700)
	assertExactMode(t, filepath.Join(root, "var/lib/mvmctl/allocations"), 0700)
	assertExactMode(t, directory, 0700)
	assertExactMode(t, filepath.Join(directory, testVMExecReservationID+".json"), 0600)
}

func TestVMExecCapacityStoreRejectsAmbiguousEntriesAndUnsafeRecords(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"unexpected entry": func(t *testing.T, directory string) {
			require.NoError(t, os.WriteFile(filepath.Join(directory, "attacker"), []byte("x"), 0600))
		},
		"record symlink": func(t *testing.T, directory string) {
			require.NoError(t, os.Symlink("missing", filepath.Join(directory, testVMExecReservationID+".json")))
		},
		"record wrong mode": func(t *testing.T, directory string) {
			raw, err := encodeVMExecCapacityRecord(testVMExecCapacityRecord())
			require.NoError(t, err)
			path := filepath.Join(directory, testVMExecReservationID+".json")
			require.NoError(t, os.WriteFile(path, raw, 0644))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareInstanceAuthorityTestRoot(t)
			store := openTestVMExecCapacityStore(t, root)
			directory := filepath.Join(root, "var/lib/mvmctl/allocations/exec")
			mutate(t, directory)

			_, err := store.scanAndPrune(t.Context(), testVMExecCapacityRecord().hostBootID, 2_000_000_000)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestVMExecCapacityStoreRecoversOnlyCanonicalSafeCrashTemps(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate  func(*testing.T, string)
		wantErr bool
	}{
		"safe regular temp is pruned": {
			mutate: func(t *testing.T, tempPath string) {
				require.NoError(t, os.WriteFile(tempPath, []byte("partial"), 0600))
			},
		},
		"unsafe mode fails closed": {
			mutate: func(t *testing.T, tempPath string) {
				require.NoError(t, os.WriteFile(tempPath, []byte("partial"), 0644))
			},
			wantErr: true,
		},
		"symlink fails closed": {
			mutate: func(t *testing.T, tempPath string) {
				require.NoError(t, os.Symlink("missing", tempPath))
			},
			wantErr: true,
		},
		"safe temp is retained when another entry is ambiguous": {
			mutate: func(t *testing.T, tempPath string) {
				require.NoError(t, os.WriteFile(tempPath, []byte("partial"), 0600))
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(tempPath), "ambiguous"), []byte("x"), 0600))
			},
			wantErr: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareInstanceAuthorityTestRoot(t)
			store := openTestVMExecCapacityStore(t, root)
			tempPath := filepath.Join(
				root,
				"var/lib/mvmctl/allocations/exec/.mvm-exec-cccccccccccccccccccccccccccccccc.tmp",
			)
			tc.mutate(t, tempPath)

			_, err := store.scanAndPrune(t.Context(), testVMExecCapacityRecord().hostBootID, 2_000_000_000)
			if tc.wantErr {
				require.Error(t, err)
				assert.FileExists(t, tempPath)
				return
			}
			require.NoError(t, err)
			assert.NoFileExists(t, tempPath)
		})
	}
}

func TestVMExecCapacityStorePrunesOnlyValidExpiredOrPriorBootRecords(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	store := openTestVMExecCapacityStore(t, root)
	active := testVMExecCapacityRecord()
	active.expiresBoottimeNS = 10_000_000_000
	expired := testVMExecCapacityRecordWithID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	expired.expiresBoottimeNS = 2_000_000_000
	priorBoot := testVMExecCapacityRecordWithID(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	priorBoot.hostBootID = "7d46d84c-f2ae-4abc-9abc-0123456789ab"
	for _, record := range []vmExecCapacityRecord{active, expired, priorBoot} {
		require.NoError(t, store.createRecord(t.Context(), record))
	}

	records, err := store.scanAndPrune(t.Context(), active.hostBootID, 3_000_000_000)
	require.NoError(t, err)
	if diff := cmp.Diff([]vmExecCapacityRecord{active}, records, cmp.AllowUnexported(
		vmExecCapacityRecord{}, vmExecReservationID{},
	)); diff != "" {
		t.Errorf("active VM-exec capacity records mismatch (-want +got):\n%s", diff)
	}
	assert.NoFileExists(t, filepath.Join(root, "var/lib/mvmctl/allocations/exec/"+expired.reservationID.value+".json"))
	assert.NoFileExists(
		t,
		filepath.Join(root, "var/lib/mvmctl/allocations/exec/"+priorBoot.reservationID.value+".json"),
	)
}

func TestVMExecCapacityStoreReturnsCreatedRecordOnPostRenameUncertainty(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	store := openTestVMExecCapacityStore(t, root)
	fault := errors.New("injected directory sync failure")
	realSync := store.state.deps.fsync
	store.state.deps.fsync = func(ctx context.Context, fd int) error {
		if fd == store.state.stateFD {
			return fault
		}
		return realSync(ctx, fd)
	}
	record := testVMExecCapacityRecord()
	err := store.createRecord(t.Context(), record)
	require.ErrorIs(t, err, fault)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["record_created"])
	assert.Equal(t, true, domainErr.Details["durability_uncertain"])

	got, found, readErr := store.readRecord(t.Context(), record.reservationID)
	require.NoError(t, readErr)
	assert.True(t, found)
	if diff := cmp.Diff(record, got, cmp.AllowUnexported(vmExecCapacityRecord{}, vmExecReservationID{})); diff != "" {
		t.Errorf("created uncertain record mismatch (-want +got):\n%s", diff)
	}
}

func TestVMExecCapacityStoreRemoveIsExactAndIdempotent(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	store := openTestVMExecCapacityStore(t, root)
	first := testVMExecCapacityRecord()
	second := testVMExecCapacityRecordWithID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	require.NoError(t, store.createRecord(t.Context(), first))
	require.NoError(t, store.createRecord(t.Context(), second))

	removed, err := store.removeRecord(t.Context(), first.reservationID)
	require.NoError(t, err)
	assert.True(t, removed)
	removed, err = store.removeRecord(t.Context(), first.reservationID)
	require.NoError(t, err)
	assert.False(t, removed)
	_, found, err := store.readRecord(t.Context(), second.reservationID)
	require.NoError(t, err)
	assert.True(t, found)
}

func openTestVMExecCapacityStore(t *testing.T, root string) *vmExecCapacityStore {
	t.Helper()

	store, err := openVMExecCapacityStore(t.Context(), realVMExecCapacityStoreDeps(), vmExecCapacityStorePolicy{
		rootPath: root, stateRoot: vmExecCapacityStateRoot, expectedUID: uint32(os.Geteuid()),
		expectedGID: uint32(os.Getegid()),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
	return store
}

func testVMExecCapacityRecordWithID(t *testing.T, value string) vmExecCapacityRecord {
	t.Helper()

	record := testVMExecCapacityRecord()
	reservationID, err := newVMExecReservationID(value)
	require.NoError(t, err)
	record.reservationID = reservationID
	return record
}
