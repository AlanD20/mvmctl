package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

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

// Rationale: global capacity authority remains private even when the common state base also holds guest markers.
func TestVMExecCapacityStoreSharedStateBaseAndPrivateDirectories(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		baseMode    os.FileMode
		privateMode os.FileMode
		wrongOwner  bool
		wantErr     bool
	}{
		"writable shared base":         {baseMode: 0775, wantErr: true},
		"unexpected shared owner":      {baseMode: 0755, wrongOwner: true, wantErr: true},
		"permissive private directory": {baseMode: 0755, privateMode: 0755, wantErr: true},
		"existing shared base":         {baseMode: 0755},
		"missing shared base":          {},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := prepareInstanceAuthorityTestRoot(t)
			base := filepath.Join(root, "var/lib/mvmctl")
			private := filepath.Join(base, "allocations")
			var before unix.Stat_t
			if tc.baseMode != 0 {
				require.NoError(t, os.Mkdir(base, tc.baseMode))
				require.NoError(t, os.Chmod(base, tc.baseMode))
				require.NoError(t, unix.Lstat(base, &before))
			}
			if tc.privateMode != 0 {
				require.NoError(t, os.Mkdir(private, tc.privateMode))
				require.NoError(t, os.Chmod(private, tc.privateMode))
			}
			deps := realVMExecCapacityStoreDeps()
			realStatx := deps.statx
			deps.statx = func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
				if err := realStatx(ctx, fd, flags, mask, stat); err != nil {
					return err
				}
				if tc.wrongOwner && stat.Ino == before.Ino {
					stat.Uid++
				}
				return nil
			}
			store, err := openVMExecCapacityStore(t.Context(), deps, vmExecCapacityStorePolicy{
				rootPath: root, stateRoot: vmExecCapacityStateRoot,
				expectedUID: uint32(os.Geteuid()), expectedGID: uint32(os.Getegid()),
			})
			if tc.wantErr {
				require.Error(t, err)
				assert.Nil(t, store)
				assert.NoDirExists(t, filepath.Join(private, "exec"))
				if tc.privateMode == 0 {
					assert.NoDirExists(t, private)
				}
			} else {
				require.NoError(t, err)
				require.NoError(t, store.Release(t.Context()))
				assertExactMode(t, private, 0700)
				assertExactMode(t, filepath.Join(private, "exec"), 0700)
			}
			var after unix.Stat_t
			require.NoError(t, unix.Lstat(base, &after))
			if tc.baseMode != 0 {
				assert.Equal(t, before.Ino, after.Ino)
				assert.Equal(t, before.Mode, after.Mode)
				assert.Equal(t, before.Uid, after.Uid)
				assert.Equal(t, before.Gid, after.Gid)
			} else {
				assert.Equal(t, uint32(0700), after.Mode&07777)
				assert.Equal(t, uint32(os.Geteuid()), after.Uid)
				assert.Equal(t, uint32(os.Getegid()), after.Gid)
			}
			if tc.privateMode != 0 {
				assertExactMode(t, private, tc.privateMode)
			}
		})
	}
}

func TestVMExecCapacityStoreRejectsAmbiguousEntriesAndUnsafeRecords(t *testing.T) {
	t.Parallel()
	// Rationale: fail-closed scans must not convert attacker-controlled filesystem shapes into trusted capacity state.

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
	// Rationale: crash recovery may delete only the exact safe temporary-file shape emitted by this store.

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
	// Rationale: pruning may reclaim only strictly validated stale authority, never a current live reservation.

	root := prepareInstanceAuthorityTestRoot(t)
	store := openTestVMExecCapacityStore(t, root)
	active := testVMExecCapacityRecord()
	active.createdBoottimeNS = 200_000_000_000
	active.expiresBoottimeNS = active.createdBoottimeNS + vmExecCapacityLifetimeNS
	expired := testVMExecCapacityRecordWithID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	expired.createdBoottimeNS = 1
	expired.expiresBoottimeNS = expired.createdBoottimeNS + vmExecCapacityLifetimeNS
	priorBoot := testVMExecCapacityRecordWithID(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	priorBoot.hostBootID = "7d46d84c-f2ae-4abc-9abc-0123456789ab"
	for _, record := range []vmExecCapacityRecord{active, expired, priorBoot} {
		require.NoError(t, store.createRecord(t.Context(), record))
	}

	records, err := store.scanAndPrune(t.Context(), active.hostBootID, 400_000_000_000)
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
	// Rationale: a failed directory sync occurs after publication, so callers need positive creation evidence.

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
	// Rationale: release must never use a prefix or directory-wide operation that can affect another reservation.

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

func TestVMExecCapacityStoreScanFailsClosedWithoutMutatingCanonicalCorruption(t *testing.T) {
	t.Parallel()
	// Rationale: canonical names and modes are insufficient authority; malformed, mismatched, and future records must
	// remain available for diagnosis rather than being interpreted or pruned.

	tests := map[string]struct {
		filename string
		raw      func(*testing.T) []byte
	}{
		"malformed JSON": {
			filename: testVMExecReservationID + ".json",
			raw:      func(*testing.T) []byte { return []byte("{") },
		},
		"filename payload mismatch": {
			filename: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.json",
			raw: func(t *testing.T) []byte {
				raw, err := encodeVMExecCapacityRecord(testVMExecCapacityRecord())
				require.NoError(t, err)
				return raw
			},
		},
		"future record": {
			filename: testVMExecReservationID + ".json",
			raw: func(t *testing.T) []byte {
				record := testVMExecCapacityRecord()
				record.createdBoottimeNS = 3_000_000_000
				record.expiresBoottimeNS = record.createdBoottimeNS + vmExecCapacityLifetimeNS
				raw, err := encodeVMExecCapacityRecord(record)
				require.NoError(t, err)
				return raw
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareInstanceAuthorityTestRoot(t)
			store := openTestVMExecCapacityStore(t, root)
			directory := filepath.Join(root, "var/lib/mvmctl/allocations/exec")
			path := filepath.Join(directory, tc.filename)
			raw := tc.raw(t)
			require.NoError(t, os.WriteFile(path, raw, 0600))

			_, err := store.scanAndPrune(t.Context(), testVMExecCapacityRecord().hostBootID, 2_000_000_000)
			require.Error(t, err)
			assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
			got, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, raw, got)
		})
	}
}

func TestVMExecCapacityStoreDoesNotPartiallyPruneBeforeLaterCorruption(t *testing.T) {
	t.Parallel()
	// Rationale: a deterministic name order must not turn discovery of a later ambiguous entry into a partial mutation.

	root := prepareInstanceAuthorityTestRoot(t)
	store := openTestVMExecCapacityStore(t, root)
	stale := testVMExecCapacityRecordWithID(t, "00000000000000000000000000000000")
	stale.hostBootID = "7d46d84c-f2ae-4abc-9abc-0123456789ab"
	require.NoError(t, store.createRecord(t.Context(), stale))
	stalePath := filepath.Join(root, "var/lib/mvmctl/allocations/exec/"+stale.reservationID.value+".json")
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(stalePath), "zz-ambiguous"), []byte("x"), 0600))

	_, err := store.scanAndPrune(t.Context(), testVMExecCapacityRecord().hostBootID, 2_000_000_000)
	require.Error(t, err)
	assert.FileExists(t, stalePath)
}

func TestVMExecCapacityStoreSyncsPartialPruneWithCancellationIndependentEvidence(t *testing.T) {
	t.Parallel()
	// Rationale: once an unlink succeeds, every later failure must report mutation and durably settle the directory even
	// when the initiating context has already been canceled.

	tests := map[string]struct {
		cancelAfterFirst bool
		failSecond       bool
		failSync         bool
		wantUncertain    bool
	}{
		"later unlink failure is durably reported": {failSecond: true},
		"later cancellation and sync failure is uncertain": {
			cancelAfterFirst: true,
			failSync:         true,
			wantUncertain:    true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := prepareInstanceAuthorityTestRoot(t)
			store := openTestVMExecCapacityStore(t, root)
			first := testVMExecCapacityRecordWithID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			second := testVMExecCapacityRecordWithID(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
			first.hostBootID = "7d46d84c-f2ae-4abc-9abc-0123456789ab"
			second.hostBootID = first.hostBootID
			require.NoError(t, store.createRecord(t.Context(), first))
			require.NoError(t, store.createRecord(t.Context(), second))

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			fault := errors.New("injected prune failure")
			realUnlink := store.state.deps.unlinkAt
			unlinks := 0
			store.state.deps.unlinkAt = func(unlinkCtx context.Context, fd int, entry string, flags int) error {
				unlinks++
				if unlinks == 1 {
					err := realUnlink(unlinkCtx, fd, entry, flags)
					if tc.cancelAfterFirst {
						cancel()
					}
					return err
				}
				if tc.failSecond {
					return fault
				}
				return realUnlink(unlinkCtx, fd, entry, flags)
			}
			realSync := store.state.deps.fsync
			syncCalls := 0
			store.state.deps.fsync = func(syncCtx context.Context, fd int) error {
				if fd == store.state.stateFD {
					syncCalls++
					assert.NoError(t, syncCtx.Err())
					if tc.failSync {
						return fault
					}
				}
				return realSync(syncCtx, fd)
			}

			_, err := store.scanAndPrune(ctx, testVMExecCapacityRecord().hostBootID, 2_000_000_000)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, true, domainErr.Details["records_pruned"])
			assert.Equal(t, tc.wantUncertain, domainErr.Details["durability_uncertain"])
			assert.Equal(t, 1, syncCalls)
			assert.NoFileExists(
				t,
				filepath.Join(root, "var/lib/mvmctl/allocations/exec/"+first.reservationID.value+".json"),
			)
			assert.FileExists(
				t,
				filepath.Join(root, "var/lib/mvmctl/allocations/exec/"+second.reservationID.value+".json"),
			)
		})
	}
}

func TestVMExecCapacityStoreReleaseWaitsForNestedScanRead(t *testing.T) {
	t.Parallel()
	// Rationale: Release must not close or recycle the directory descriptor while scan's nested record read is blocked.

	root := prepareInstanceAuthorityTestRoot(t)
	store := openTestVMExecCapacityStore(t, root)
	require.NoError(t, store.createRecord(t.Context(), testVMExecCapacityRecord()))
	entered := make(chan struct{})
	unblock := make(chan struct{})
	var once sync.Once
	realRead := store.state.deps.read
	store.state.deps.read = func(ctx context.Context, fd int, value []byte) (int, error) {
		once.Do(func() { close(entered) })
		<-unblock
		return realRead(ctx, fd, value)
	}
	scanDone := make(chan error, 1)
	go func() {
		_, err := store.scanAndPrune(context.Background(), testVMExecCapacityRecord().hostBootID, 2_000_000_000)
		scanDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("scan did not enter the blocked record read")
	}
	releaseDone := make(chan error, 1)
	releaseStarted := make(chan struct{})
	go func() {
		close(releaseStarted)
		releaseDone <- store.Release(context.Background())
	}()
	<-releaseStarted
	writerQueued := time.NewTimer(2 * time.Second)
	defer writerQueued.Stop()
	for store.state.mu.TryRLock() {
		store.state.mu.RUnlock()
		select {
		case <-writerQueued.C:
			t.Fatal("Release did not queue for the store lifecycle lock")
		default:
			runtime.Gosched()
		}
	}
	select {
	case err := <-releaseDone:
		t.Fatalf("Release returned before the scan completed: %v", err)
	default:
	}
	close(unblock)
	select {
	case err := <-scanDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("scan deadlocked with queued Release")
	}
	select {
	case err := <-releaseDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Release did not finish after scan")
	}
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
