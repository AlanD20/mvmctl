package jailer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/model"
)

func TestRuntimeStatusLifecycleClassification(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		record    instanceRecord
		otherBoot bool
		want      runtimeStatus
	}{
		{"launching", testLaunchingInstanceRecord(), false, runtimeStatusUnavailable},
		{"cleaning", runtimeStatusCleaningRecord(), false, runtimeStatusUnavailable},
		{"previous boot launching", testLaunchingInstanceRecord(), true, runtimeStatusStopped},
		{"previous boot active", testActiveInstanceRecord(true), true, runtimeStatusStopped},
		{"previous boot cleaning", runtimeStatusCleaningRecord(), true, runtimeStatusStopped},
		{"cleaned", runtimeStatusCleanedRecord(), false, runtimeStatusStopped},
		{"active without relay", testActiveInstanceRecord(false), false, runtimeStatusRunning},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newRuntimeStatusFixture(t, tc.record)
			if tc.otherBoot {
				f.bootID = processOtherBootID
			}
			got, err := f.observe(t.Context(), tc.record.vmID)
			if tc.want == runtimeStatusUnavailable {
				require.Error(t, err)
				assert.Nil(t, got)
				assert.Empty(t, f.opened)
				assert.Zero(t, f.processDirectoryReads)
				assert.Zero(t, f.processMetadataReads)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(map[string]runtimeStatus{tc.record.vmID: tc.want}, got); diff != "" {
				t.Errorf("lifecycle observation mismatch (-want +got):\n%s", diff)
			}
			if tc.want == runtimeStatusStopped {
				assert.Empty(t, f.opened, "no PID from another boot may be opened")
				assert.Zero(t, f.processDirectoryReads, "stopped authority must not open process directories")
				assert.Zero(t, f.processMetadataReads, "stopped authority must not inspect process metadata")
			}
		})
	}
}

func runtimeStatusCleaningRecord() instanceRecord {
	record := testActiveInstanceRecord(false)
	record.lifecycle = instanceLifecycleCleaning
	record.cleanupGeneration = 1
	return record
}

func runtimeStatusCleanedRecord() instanceRecord {
	return instanceRecord{schemaVersion: 6, ownerUID: testAuthorityUID, vmID: testVMID,
		networkPlacement: model.VMNetworkPlacementManaged,
		lifecycle:        instanceLifecycleCleaned, launchGeneration: 1, cleanupGeneration: 1}
}

func TestRuntimeStatusAbsentAuthorityDoesNotCreateDurableState(t *testing.T) {
	t.Parallel()
	f := newRuntimeStatusFixture(t)
	got, err := f.observe(t.Context(), testVMID)
	require.NoError(t, err)
	if diff := cmp.Diff(map[string]runtimeStatus{testVMID: runtimeStatusStopped}, got); diff != "" {
		t.Errorf("absent authority observation mismatch (-want +got):\n%s", diff)
	}
	assert.NoDirExists(t, filepath.Join(f.root, "var/lib/mvmctl"))
	assert.Empty(t, f.opened)
	assert.Zero(t, f.processDirectoryReads)
	assert.Zero(t, f.processMetadataReads)
}

func TestRuntimeStatusAbsentRecordDoesNotReadProcesses(t *testing.T) {
	t.Parallel()
	other := runtimeStatusCleanedRecord()
	other.vmID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	f := newRuntimeStatusFixture(t, other)
	got, err := f.observe(t.Context(), testVMID)
	require.NoError(t, err)
	if diff := cmp.Diff(map[string]runtimeStatus{testVMID: runtimeStatusStopped}, got); diff != "" {
		t.Errorf("absent record observation mismatch (-want +got):\n%s", diff)
	}
	assert.Empty(t, f.opened)
	assert.Zero(t, f.processDirectoryReads)
	assert.Zero(t, f.processMetadataReads)
}

func TestRuntimeStatusBatchInputValidation(t *testing.T) {
	t.Parallel()
	tests := map[string][]string{
		"invalid ID": {"../escape"}, "duplicate": {testVMID, testVMID},
		"too many": make([]string, runtimeStatusMaxVMs+1),
	}
	for name, ids := range tests {
		t.Run(name, func(t *testing.T) {
			f := newRuntimeStatusFixture(t)
			got, err := f.observe(t.Context(), ids...)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Empty(t, f.tree.acquisitionSnapshot())
		})
	}
	t.Run("empty", func(t *testing.T) {
		f := newRuntimeStatusFixture(t)
		got, err := f.observe(t.Context())
		require.NoError(t, err)
		if diff := cmp.Diff(map[string]runtimeStatus{}, got); diff != "" {
			t.Errorf("empty batch mismatch: %s", diff)
		}
		assert.Empty(t, f.tree.acquisitionSnapshot())
	})
	t.Run("nil context", func(t *testing.T) {
		f := newRuntimeStatusFixture(t)
		got, err := f.observe(nil, testVMID)
		require.Error(t, err)
		assert.Nil(t, got)
	})
}

func TestRuntimeStatusRejectsForeignAndDuplicateOwnership(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		duplicate bool
	}{{"foreign owner", false}, {"duplicate global claim", true}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			foreign := runtimeStatusCleanedRecord()
			foreign.ownerUID++
			f := newRuntimeStatusFixture(t, foreign)
			if tc.duplicate {
				f.writeRecord(t, runtimeStatusCleanedRecord())
			}
			got, err := f.observe(t.Context(), testVMID)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Empty(t, f.opened)
		})
	}
}

func TestRuntimeStatusBatchReleasesEachVMLockBeforeNext(t *testing.T) {
	t.Parallel()
	first := runtimeStatusCleanedRecord()
	second := runtimeStatusCleanedRecord()
	second.vmID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	f := newRuntimeStatusFixture(t, first, second)
	var locks []authorityfs.VMKey
	held := false
	f.tree.onVMAcquire = func(_ context.Context, key authorityfs.VMKey) {
		assert.False(t, held, "status batch must retain only one VM lock at a time")
		held = true
		locks = append(locks, key)
	}
	f.tree.onVMRelease = func(context.Context, authorityfs.VMKey) error { held = false; return nil }
	ids := []string{first.vmID, second.vmID}
	got, err := f.observe(t.Context(), ids...)
	require.NoError(t, err)
	want := map[string]runtimeStatus{first.vmID: runtimeStatusStopped, second.vmID: runtimeStatusStopped}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("batch mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 1, f.scans)
	if diff := cmp.Diff([]string{"index", "vm", "vm"}, f.tree.acquisitionSnapshot()); diff != "" {
		t.Errorf("batch lock acquisition mismatch (-want +got):\n%s", diff)
	}
	assert.False(t, held)
	ordered := []string{first.vmID, second.vmID}
	sort.Strings(ordered)
	wantLocks := make([]authorityfs.VMKey, len(ordered))
	for index, id := range ordered {
		wantLocks[index], err = authorityfs.NewVMKey(id)
		require.NoError(t, err)
	}
	if diff := cmp.Diff(wantLocks, locks, cmp.AllowUnexported(authorityfs.VMKey{})); diff != "" {
		t.Errorf("VM lock order mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, first.vmID, ids[0], "caller order must not be mutated")
}

// Rationale: callers must not receive already-observed rows when a later requested record is unavailable.
func TestRuntimeStatusLaterFailureReturnsNoPartialBatch(t *testing.T) {
	t.Parallel()
	first := runtimeStatusCleanedRecord()
	first.vmID = "11111111111111111111111111111111"
	later := runtimeStatusCleaningRecord()
	later.vmID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	f := newRuntimeStatusFixture(t, first, later)
	got, err := f.observe(t.Context(), later.vmID, first.vmID)
	require.Error(t, err)
	assert.Nil(t, got)
	if diff := cmp.Diff([]string{"index", "vm", "vm"}, f.tree.acquisitionSnapshot()); diff != "" {
		t.Errorf("later failure must follow first observation (-want +got):\n%s", diff)
	}
}

func TestRuntimeStatusBootChangeInvalidatesBatch(t *testing.T) {
	t.Parallel()
	record := testActiveInstanceRecord(false)
	f := newRuntimeStatusFixture(t, record)
	reads := 0
	f.identity.readBootID = func(context.Context) (string, error) {
		reads++
		if reads > 1 {
			return processOtherBootID, nil
		}
		return processTestHostBootID, nil
	}
	got, err := f.observe(t.Context(), record.vmID)
	require.Error(t, err)
	assert.Nil(t, got)
}

func TestRuntimeStatusCorruptOrUnsafeStateIsUnavailable(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*testing.T, *runtimeStatusFixture){
		"unsafe instance root": func(t *testing.T, f *runtimeStatusFixture) {
			require.NoError(t, os.Chmod(filepath.Join(f.root, "var/lib/mvmctl/instances"), 0777))
		},
		"symlink record": func(t *testing.T, f *runtimeStatusFixture) {
			path := filepath.Join(f.root, "var/lib/mvmctl/instances/1000", testVMID+".json")
			require.NoError(t, os.Rename(path, path+".original"))
			require.NoError(t, os.Symlink(path+".original", path))
		},
		"corrupt record": func(t *testing.T, f *runtimeStatusFixture) {
			require.NoError(
				t,
				os.WriteFile(
					filepath.Join(f.root, "var/lib/mvmctl/instances/1000", testVMID+".json"),
					[]byte("{"),
					0600,
				),
			)
		},
		"owner scan failure": func(_ *testing.T, f *runtimeStatusFixture) {
			f.authority.deps.readDirNames = func(context.Context, int) ([]string, error) { return nil, unix.EACCES }
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			f := newRuntimeStatusFixture(t, runtimeStatusCleanedRecord())
			change(t, f)
			got, err := f.observe(t.Context(), testVMID)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Empty(t, f.opened)
		})
	}
}

func TestRuntimeStatusUnsafeDirectoryCloseErrorCannotProveAbsence(t *testing.T) {
	t.Parallel()
	f := newRuntimeStatusFixture(t, runtimeStatusCleanedRecord())
	require.NoError(t, os.Chmod(filepath.Join(f.root, "var/lib/mvmctl/instances"), 0777))
	closeFD := f.authority.deps.close
	f.authority.deps.close = func(ctx context.Context, fd int) error {
		if err := closeFD(ctx, fd); err != nil {
			return err
		}
		return unix.ENOENT
	}
	got, err := f.observe(t.Context(), testVMID)
	require.Error(t, err)
	assert.Nil(t, got)
}

// Rationale: waiting for a busy VM must obey cancellation and release the global index for other operations.
func TestRuntimeStatusCancellationReleasesIndex(t *testing.T) {
	t.Parallel()
	f := newRuntimeStatusFixture(t, runtimeStatusCleanedRecord())
	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	treeAuthority, err := f.tree.openJailer(t.Context())
	require.NoError(t, err)
	uid, err := treeAuthority.openUID(t.Context(), testAuthorityUID)
	require.NoError(t, err)
	lock, err := uid.acquireVM(t.Context(), key)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	got, err := f.observe(ctx, testVMID)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Nil(t, got)
	require.NoError(t, lock.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
	require.NoError(t, treeAuthority.Release(t.Context()))
	got, err = f.observe(t.Context(), testVMID)
	require.NoError(t, err)
	assert.Equal(t, runtimeStatusStopped, got[testVMID])
}

func BenchmarkRuntimeStatus1000(b *testing.B) {
	for _, state := range []runtimeStatus{runtimeStatusStopped, runtimeStatusRunning} {
		b.Run(string(state), func(b *testing.B) {
			records := make([]instanceRecord, runtimeStatusMaxVMs)
			ids := make([]string, len(records))
			for index := range records {
				records[index] = runtimeStatusCleanedRecord()
				if state == runtimeStatusRunning {
					records[index] = testActiveInstanceRecord(false)
					records[index].launch.process.pid = 10000 + index
					records[index].launch.process.startTimeTicks = uint64(index + 1)
					records[index].active.executable.inode = uint64(index + 1)
					records[index].active.jailRoot.inode = uint64(index + 1)
				}
				records[index].vmID = fmt.Sprintf("%032x", index+1)
				ids[index] = records[index].vmID
			}
			f := newRuntimeStatusFixture(b, records...)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				f.opened = f.opened[:0]
				f.closed = f.closed[:0]
				f.pidCloses = f.pidCloses[:0]
				got, err := f.observe(b.Context(), ids...)
				if err != nil {
					b.Fatal(err)
				}
				for _, id := range ids {
					if got[id] != state {
						b.Fatalf("unexpected state for %s: %s", id, got[id])
					}
				}
			}
		})
	}
}
