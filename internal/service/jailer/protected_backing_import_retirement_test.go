package jailer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

type protectedImportCancellationFixture struct {
	cancellation *protectedImportCancellation
	cache        *managedCacheLease
	root         string
	data         string
	metadata     string
	logical      string
	source       string
}

// The fixed protocol uses real root-only metadata and data directories. Only syscall failures
// are injected; candidate admission, durable record publication and unlink are production code.
func newProtectedImportCancellationFixture(
	t *testing.T, kind, phase, leaf string, recorded bool, candidateBytes int64,
) *protectedImportCancellationFixture {
	t.Helper()
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("root-owned import cancellation runs in the disposable root-test VM")
	}
	root := t.TempDir()
	store := filepath.Join(root, protectedBackingDirectory)
	kindDirectory := filepath.Join(store, kind)
	require.NoError(t, os.Mkdir(store, 0700))
	require.NoError(t, os.Mkdir(kindDirectory, 0700))
	fixture := &protectedImportCancellationFixture{
		root: root, data: filepath.Join(kindDirectory, testVMID), metadata: filepath.Join(root, "metadata"),
		logical: filepath.Join(root, "logical"), source: filepath.Join(root, "operator-source"),
	}
	var retained []int
	for _, path := range []string{root, fixture.data, fixture.metadata, fixture.logical, store, kindDirectory} {
		if path != root && path != store && path != kindDirectory {
			require.NoError(t, os.Mkdir(path, 0700))
		}
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		require.NoError(t, err)
		retained = append(retained, fd)
	}
	t.Cleanup(func() { require.NoError(t, closeProtectedFDs(retained)) })
	cacheIdentity, err := protectedIdentity(retained[0])
	require.NoError(t, err)
	cache := managedCacheIdentity{
		deviceMajor: unix.Major(cacheIdentity.Device), deviceMinor: unix.Minor(cacheIdentity.Device),
		inode: cacheIdentity.Inode,
	}
	fixture.cache = &managedCacheLease{
		cacheFD: retained[0], identity: cache, ownerUID: 1000, ownerGID: 1000, deps: realManagedCacheDeps(),
		configDeps: realManagedFirecrackerConfigDeps(), retained: []int{retained[0]},
	}
	directory, err := protectedIdentity(retained[1])
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fixture.source, []byte("operator staging bytes must survive"), 0600))
	if kind == protectedBackingRootfs {
		require.NoError(t, os.Truncate(fixture.source, int64(managedVMRootfsMinBytes)))
	}
	require.NoError(t, os.Chown(fixture.source, 1000, 1000))
	var source unix.Stat_t
	require.NoError(t, unix.Stat(fixture.source, &source))
	record := protectedBackingRecord{
		Version: 1, Kind: kind, ID: testVMID, Owner: 1000, Cache: protectedCacheIdentity(cache),
		Directory: directory, Phase: phase, References: []protectedBackingReference{},
		Source: protectedBackingIdentity{Device: uint64(source.Dev), Inode: source.Ino, Size: source.Size},
	}
	if leaf != "" {
		path := filepath.Join(fixture.data, leaf)
		require.NoError(t, os.WriteFile(path, nil, 0600))
		require.NoError(t, os.Truncate(path, candidateBytes))
		var stat unix.Stat_t
		require.NoError(t, unix.Stat(path, &stat))
		if recorded {
			record.Backing = protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}
			if phase == "copied" || phase == "cancelling-copied" {
				record.Backing.Size = stat.Size
			}
		}
	}
	fixture.cancellation = &protectedImportCancellation{
		cache: fixture.cache, anchorFD: retained[2], storeFD: retained[4], dataKindFD: retained[5],
		directoryFD: retained[1], metadataFD: retained[2], logicalFD: retained[3], record: record,
	}
	storeIdentity, err := protectedIdentity(retained[4])
	require.NoError(t, err)
	locks, err := openProtectedDirectory(t.Context(), retained[2], protectedSnapshotLockDirectory, true, true)
	require.NoError(t, err)
	locksIdentity, err := protectedIdentity(locks)
	require.NoError(t, err)
	require.NoError(t, unix.Close(locks))
	require.NoError(t, writeProtectedRecord(t.Context(), retained[2], "store.json", protectedBackingAnchor{
		Version: protectedStoreLayoutVersion, Owner: 1000, OwnerGID: 1000, Cache: record.Cache, Locator: root, Store: storeIdentity,
		SnapshotLocks: locksIdentity,
	}))
	require.NoError(t, writeProtectedRecord(t.Context(), retained[2], "record.json", record))
	require.NoError(t, writeProtectedRecord(t.Context(), retained[3], "logical.json", protectedLogicalRecord{
		Version: 1, Owner: 1000, Kind: kind, ID: testVMID, Cache: record.Cache, Initialized: true,
	}))
	return fixture
}

func (fixture *protectedImportCancellationFixture) readRecord(t *testing.T) protectedBackingRecord {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixture.metadata, "record.json"))
	require.NoError(t, err)
	var record protectedBackingRecord
	require.NoError(t, json.Unmarshal(raw, &record))
	return record
}

func (fixture *protectedImportCancellationFixture) requireTombstone(t *testing.T, original protectedBackingRecord) {
	t.Helper()
	got := fixture.readRecord(t)
	original.Phase = "removed"
	if diff := cmp.Diff(original, got); diff != "" {
		t.Errorf("retained import tombstone (-want +got):\n%s", diff)
	}
	raw, err := os.ReadFile(filepath.Join(fixture.logical, "logical.json"))
	require.NoError(t, err)
	var logical protectedLogicalRecord
	require.NoError(t, json.Unmarshal(raw, &logical))
	assert.True(t, logical.Initialized, "removed backing must never admit initial-source fallback")
	entries, err := os.ReadDir(fixture.data)
	require.NoError(t, err)
	assert.Empty(t, entries, "the original object directory remains, with no data leaf")
}

func TestProtectedImportCancellationRemovesOnlyVerifiedPartialCopy(t *testing.T) {
	for _, kind := range []string{protectedBackingRootfs, protectedBackingCloudInit} {
		for _, tc := range []struct {
			name, phase, leaf string
			recorded          bool
			bytes             int64
			acl               bool
		}{
			{name: "source rejected before pending", phase: "copying"},
			{name: "unrecorded empty pending", phase: "copying", leaf: "pending"},
			{name: "recorded empty before ACL", phase: "copying", leaf: "pending", recorded: true},
			{name: "recorded partial copy", phase: "copying", leaf: "pending", recorded: true, bytes: 4, acl: true},
			{name: "completed pending", phase: "copied", leaf: "pending", recorded: true, bytes: -1, acl: true},
			{name: "renamed completed disk", phase: "copied", leaf: "disk", recorded: true, bytes: -1, acl: true},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				fullBytes := int64(len("operator staging bytes must survive"))
				if kind == protectedBackingRootfs {
					fullBytes = int64(managedVMRootfsMinBytes)
				}
				candidateBytes := tc.bytes
				if candidateBytes < 0 {
					candidateBytes = fullBytes
				}
				fixture := newProtectedImportCancellationFixture(
					t,
					kind,
					tc.phase,
					tc.leaf,
					tc.recorded,
					candidateBytes,
				)
				cancellation := fixture.cancellation
				if tc.name == "source rejected before pending" {
					// Rationale: source rejection happens after the initialized copying record exists,
					// but before any source or candidate identity has been recorded.
					cancellation.record.Source = protectedBackingIdentity{}
					require.NoError(
						t,
						writeProtectedRecord(t.Context(), cancellation.metadataFD, "record.json", cancellation.record),
					)
					importLease := &protectedBackingLease{
						metadataFD:  cancellation.metadataFD,
						directoryFD: cancellation.directoryFD,
						record:      cancellation.record,
					}
					require.Error(t, importLease.completeImport(t.Context(), fixture.cache, uint64(fullBytes)))
					cancellation.record = fixture.readRecord(t)
					assert.Zero(t, cancellation.record.Source.Inode)
					assert.Zero(t, cancellation.record.Backing.Inode)
				}
				if tc.acl {
					fd, err := openProtectedFile(t.Context(), cancellation.directoryFD, tc.leaf, unix.O_RDONLY)
					require.NoError(t, err)
					require.NoError(t, errors.Join(setProtectedACL(t.Context(), fd, nil), unix.Close(fd)))
				}
				original := fixture.readRecord(t)
				if original.Backing.Inode == 0 && tc.leaf != "" {
					// CONTRACT: durable intent pins the independently observed empty candidate.
					var pending unix.Stat_t
					require.NoError(t, unix.Stat(filepath.Join(fixture.data, tc.leaf), &pending))
					original.Backing = protectedBackingIdentity{Device: uint64(pending.Dev), Inode: pending.Ino}
				}
				var sourceBefore unix.Stat_t
				require.NoError(t, unix.Stat(fixture.source, &sourceBefore))
				require.NoError(t, cancellation.cancel(t.Context(), productionProtectedImportCancellationDeps()))
				fixture.requireTombstone(t, original)
				var sourceAfter unix.Stat_t
				require.NoError(t, unix.Stat(fixture.source, &sourceAfter))
				if diff := cmp.Diff(sourceBefore, sourceAfter); diff != "" {
					t.Errorf("operator source metadata changed (-want +got):\n%s", diff)
				}
				fd, err := unix.Open(fixture.source, unix.O_RDONLY|unix.O_CLOEXEC, 0)
				require.NoError(t, err)
				prefix := make([]byte, len("operator staging bytes must survive"))
				_, readErr := unix.Pread(fd, prefix, 0)
				require.NoError(t, errors.Join(readErr, unix.Close(fd)))
				assert.Equal(t, "operator staging bytes must survive", string(prefix))
			})
		}
	}
}

// Rationale: retry must distinguish pre-intent failure from a durable cancellation whose unlink
// or directory fsync has already happened. Terminal metadata may follow only a synced empty dir.
func TestProtectedImportCancellationDurableInterruptionBoundaries(t *testing.T) {
	for _, sourcePhase := range []string{"copying", "copied"} {
		for _, tc := range []struct {
			name, wantPhase string
			wantLeaf        bool
		}{
			{"before intent", "copying", true},
			{"after intent", "cancelling-copy", true},
			{"before unlink", "cancelling-copy", true},
			{"after unlink", "cancelling-copy", false},
			{"before directory sync", "cancelling-copy", false},
			{"after directory sync", "cancelling-copy", false},
			{"before terminal", "cancelling-copy", false},
			{"after terminal", "removed", false},
		} {
			t.Run(sourcePhase+"/"+tc.name, func(t *testing.T) {
				wantPhase, cancelPhase := tc.wantPhase, "cancelling-copy"
				candidateBytes := int64(4)
				if sourcePhase == "copied" {
					cancelPhase = "cancelling-copied"
					candidateBytes = int64(len("operator staging bytes must survive"))
					if wantPhase == "copying" {
						wantPhase = "copied"
					} else if wantPhase == "cancelling-copy" {
						wantPhase = cancelPhase
					}
				}
				fixture := newProtectedImportCancellationFixture(
					t,
					protectedBackingCloudInit,
					sourcePhase,
					"pending",
					true,
					candidateBytes,
				)
				original := fixture.readRecord(t)
				fault := errors.New("injected cancellation boundary")
				deps := productionProtectedImportCancellationDeps()
				realWrite, realUnlink, realSync := deps.writeRecord, deps.unlinkCandidate, deps.syncDirectory
				synced := false
				deps.writeRecord = func(ctx context.Context, fd int, record protectedBackingRecord) error {
					boundary := "intent"
					if record.Phase == "removed" {
						boundary = "terminal"
						assert.True(t, synced, "removed must follow successful directory sync")
					}
					if tc.name == "before "+boundary {
						return fault
					}
					if err := realWrite(ctx, fd, record); err != nil {
						return err
					}
					if tc.name == "after "+boundary {
						return fault
					}
					return nil
				}
				deps.unlinkCandidate = func(ctx context.Context, fd int, leaf protectedImportLeaf) error {
					assert.Equal(t, cancelPhase, fixture.readRecord(t).Phase, "unlink requires durable intent")
					if tc.name == "before unlink" {
						return fault
					}
					if err := realUnlink(ctx, fd, leaf); err != nil {
						return err
					}
					if tc.name == "after unlink" {
						return fault
					}
					return nil
				}
				deps.syncDirectory = func(ctx context.Context, fd int) error {
					if tc.name == "before directory sync" {
						return fault
					}
					if err := realSync(ctx, fd); err != nil {
						return err
					}
					synced = true
					if tc.name == "after directory sync" {
						return fault
					}
					return nil
				}
				assert.ErrorIs(t, fixture.cancellation.cancel(t.Context(), deps), fault)
				assert.Equal(t, wantPhase, fixture.readRecord(t).Phase)
				_, statErr := os.Lstat(filepath.Join(fixture.data, "pending"))
				assert.Equal(t, tc.wantLeaf, statErr == nil)
				if wantPhase != "removed" {
					fresh := *fixture.cancellation
					require.NoError(
						t,
						fresh.readAuthority(
							t.Context(),
							1000,
							fixture.cache.identity,
							protectedBackingCloudInit,
							testVMID,
						),
					)
					require.NoError(t, fresh.cancel(t.Context(), productionProtectedImportCancellationDeps()))
				}
				fixture.requireTombstone(t, original)
			})
		}
	}
}

// Rationale: ambiguous protected bytes and authority remain untouched, even when metadata is
// syntactically valid. Import retirement may not normalize a candidate to make it removable.
func TestProtectedImportCancellationRefusesAmbiguityWithoutMutation(t *testing.T) {
	tests := map[string]func(*testing.T, *protectedImportCancellationFixture){
		"current grant": func(_ *testing.T, f *protectedImportCancellationFixture) {
			f.cancellation.record.References = []protectedBackingReference{{VMID: testVMID, ExecutionUID: 1001}}
		},
		"next grant": func(_ *testing.T, f *protectedImportCancellationFixture) {
			f.cancellation.record.NextReferences = []protectedBackingReference{{VMID: testVMID, ExecutionUID: 1001}}
		},
		"foreign directory": func(_ *testing.T, f *protectedImportCancellationFixture) {
			f.cancellation.record.Directory.Inode++
		},
		"replaced object directory": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(t, os.Rename(f.data, filepath.Join(f.root, "original-object")))
			require.NoError(t, os.Mkdir(f.data, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(f.data, "pending"), []byte("replacement"), 0600))
		},
		"replaced store": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(
				t,
				os.Rename(filepath.Join(f.root, protectedBackingDirectory), filepath.Join(f.root, "original-store")),
			)
			require.NoError(t, os.Mkdir(filepath.Join(f.root, protectedBackingDirectory), 0700))
			require.NoError(t, os.MkdirAll(f.data, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(f.data, "pending"), []byte("replacement"), 0600))
		},
		"replaced inode": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(t, os.Rename(filepath.Join(f.data, "pending"), filepath.Join(f.root, "original-pending")))
			require.NoError(t, os.WriteFile(filepath.Join(f.data, "pending"), []byte("replacement"), 0600))
		},
		"two leaves": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(t, os.WriteFile(filepath.Join(f.data, "disk"), nil, 0600))
		},
		"unknown leaf": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(t, os.Rename(filepath.Join(f.data, "pending"), filepath.Join(f.data, "unexpected")))
		},
		"unrecorded nonempty": func(_ *testing.T, f *protectedImportCancellationFixture) {
			f.cancellation.record.Backing = protectedBackingIdentity{}
		},
		"symlink": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(t, os.Remove(filepath.Join(f.data, "pending")))
			require.NoError(t, os.Symlink(f.source, filepath.Join(f.data, "pending")))
		},
		"oversized partial": func(_ *testing.T, f *protectedImportCancellationFixture) {
			f.cancellation.record.Source.Size = 1
		},
		"copying disk": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(t, os.Rename(filepath.Join(f.data, "pending"), filepath.Join(f.data, "disk")))
		},
		"copied short candidate": func(_ *testing.T, f *protectedImportCancellationFixture) {
			f.cancellation.record.Phase = "copied"
			f.cancellation.record.Backing.Size = f.cancellation.record.Source.Size
		},
		"unsafe mode": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(t, os.Chmod(filepath.Join(f.data, "pending"), 0640))
		},
		"foreign owner": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(t, os.Chown(filepath.Join(f.data, "pending"), 1000, 1000))
		},
		"hard link": func(t *testing.T, f *protectedImportCancellationFixture) {
			require.NoError(t, os.Link(filepath.Join(f.data, "pending"), filepath.Join(f.root, "alias")))
		},
		"foreign ACL": func(t *testing.T, f *protectedImportCancellationFixture) {
			fd, err := openProtectedFile(t.Context(), f.cancellation.directoryFD, "pending", unix.O_RDONLY)
			require.NoError(t, err)
			require.NoError(t, errors.Join(setProtectedACL(t.Context(), fd,
				[]protectedBackingReference{{VMID: testVMID, ExecutionUID: 1001, Writable: true}}), unix.Close(fd)))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newProtectedImportCancellationFixture(
				t,
				protectedBackingCloudInit,
				"copying",
				"pending",
				true,
				4,
			)
			mutate(t, fixture)
			require.NoError(
				t,
				writeProtectedRecord(
					t.Context(),
					fixture.cancellation.metadataFD,
					"record.json",
					fixture.cancellation.record,
				),
			)
			before := fixture.readRecord(t)
			entriesBefore, err := os.ReadDir(fixture.data)
			require.NoError(t, err)
			var statsBefore []unix.Stat_t
			for _, entry := range entriesBefore {
				var stat unix.Stat_t
				require.NoError(t, unix.Lstat(filepath.Join(fixture.data, entry.Name()), &stat))
				statsBefore = append(statsBefore, stat)
			}
			assert.Error(t, fixture.cancellation.cancel(t.Context(), productionProtectedImportCancellationDeps()))
			if diff := cmp.Diff(before, fixture.readRecord(t)); diff != "" {
				t.Errorf("refused import metadata changed (-want +got):\n%s", diff)
			}
			entriesAfter, err := os.ReadDir(fixture.data)
			require.NoError(t, err)
			require.Len(t, entriesAfter, len(entriesBefore))
			for index, entry := range entriesAfter {
				assert.Equal(t, entriesBefore[index].Name(), entry.Name())
				var stat unix.Stat_t
				require.NoError(t, unix.Lstat(filepath.Join(fixture.data, entry.Name()), &stat))
				if diff := cmp.Diff(statsBefore[index], stat); diff != "" {
					t.Errorf("refused candidate metadata changed (-want +got):\n%s", diff)
				}
			}
		})
	}
}

// Rationale: even a live task outside the managed jail can retain a writable descriptor. Task
// absence must fail before the coordinator reaches tree/grant inspection or any import effects.
func TestProtectedImportRetirementLiveTaskRefusesBeforeEffects(t *testing.T) {
	fixture := newProtectedImportCancellationFixture(t, protectedBackingCloudInit, "copying", "pending", true, 4)
	before := fixture.readRecord(t)
	retirement := &protectedImportRetirement{
		ownerUID: 1000, vmID: testVMID,
		locked: &lockedInstance{vmLock: &vmAuthorityLease{state: &vmAuthorityLeaseState{
			policy: testInstanceAuthorityPolicy(fixture.root), accounts: realExecutionIdentityAccountDeps(),
		}}},
		identity: vmExecutionIdentity{uid: uint32(os.Geteuid()), gid: uint32(os.Getegid())},
	}
	assert.Error(t, retirement.requireAbsence(t.Context()))
	if diff := cmp.Diff(before, fixture.readRecord(t)); diff != "" {
		t.Errorf("task refusal changed durable import state (-want +got):\n%s", diff)
	}
	var stat unix.Stat_t
	require.NoError(t, unix.Stat(filepath.Join(fixture.data, "pending"), &stat))
	assert.Equal(t, before.Backing.Inode, stat.Ino)
	assert.Equal(t, int64(4), stat.Size)
}

// Rationale: a fixed, correctly named root-owned leaf is not enough if it is a bind mount. The
// cancellation path must detect mount crossing before metadata intent or an unlink can occur.
func TestProtectedImportCancellationRefusesMountedCandidate(t *testing.T) {
	fixture := newProtectedImportCancellationFixture(t, protectedBackingCloudInit, "copying", "pending", true, 4)
	before := fixture.readRecord(t)
	foreign := filepath.Join(fixture.root, "foreign-backing")
	require.NoError(t, os.WriteFile(foreign, []byte("foreign mounted bytes"), 0600))
	target := filepath.Join(fixture.data, "pending")
	require.NoError(t, unix.Mount(foreign, target, "", unix.MS_BIND, ""))
	t.Cleanup(func() { require.NoError(t, unix.Unmount(target, 0)) })
	assert.Error(t, fixture.cancellation.cancel(t.Context(), productionProtectedImportCancellationDeps()))
	if diff := cmp.Diff(before, fixture.readRecord(t)); diff != "" {
		t.Errorf("mount refusal changed durable import state (-want +got):\n%s", diff)
	}
	bytes, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "foreign mounted bytes", string(bytes))
}

// Rationale: caller cancellation after durable intent or unlink must preserve replay authority,
// and a fresh removal must finish without opening or copying the original operator staging file.
func TestProtectedImportCancellationContextInterruptionResumes(t *testing.T) {
	for _, boundary := range []string{"intent", "unlink"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := newProtectedImportCancellationFixture(
				t,
				protectedBackingCloudInit,
				"copying",
				"pending",
				true,
				4,
			)
			original := fixture.readRecord(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			deps := productionProtectedImportCancellationDeps()
			realWrite, realUnlink := deps.writeRecord, deps.unlinkCandidate
			deps.writeRecord = func(ctx context.Context, fd int, record protectedBackingRecord) error {
				if err := realWrite(ctx, fd, record); err != nil {
					return err
				}
				if boundary == "intent" {
					cancel()
				}
				return nil
			}
			deps.unlinkCandidate = func(ctx context.Context, fd int, leaf protectedImportLeaf) error {
				if err := realUnlink(ctx, fd, leaf); err != nil {
					return err
				}
				cancel()
				return nil
			}
			assert.ErrorIs(t, fixture.cancellation.cancel(ctx, deps), context.Canceled)
			assert.Equal(t, "cancelling-copy", fixture.readRecord(t).Phase)
			fresh := *fixture.cancellation
			require.NoError(
				t,
				fresh.readAuthority(t.Context(), 1000, fixture.cache.identity, protectedBackingCloudInit, testVMID),
			)
			require.NoError(t, fresh.cancel(t.Context(), productionProtectedImportCancellationDeps()))
			fixture.requireTombstone(t, original)
		})
	}
}

// Rationale: a prior intent rename can be visible without a successful metadata-directory
// fsync. Replay must complete a fresh durability fence before it may unlink the retained inode.
func TestProtectedImportCancellationReplayRequiresDurableIntentAgain(t *testing.T) {
	for _, phase := range []string{"cancelling-copy", "cancelling-copied"} {
		t.Run(phase, func(t *testing.T) {
			candidateBytes := int64(4)
			if phase == "cancelling-copied" {
				candidateBytes = int64(len("operator staging bytes must survive"))
			}
			fixture := newProtectedImportCancellationFixture(
				t,
				protectedBackingCloudInit,
				phase,
				"pending",
				true,
				candidateBytes,
			)
			before := fixture.readRecord(t)
			fault := errors.New("intent durability remains uncertain")
			deps := productionProtectedImportCancellationDeps()
			deps.writeRecord = func(context.Context, int, protectedBackingRecord) error { return fault }
			assert.ErrorIs(t, fixture.cancellation.cancel(t.Context(), deps), fault)
			if diff := cmp.Diff(before, fixture.readRecord(t)); diff != "" {
				t.Errorf("refused replay changed intent (-want +got):\n%s", diff)
			}
			var stat unix.Stat_t
			require.NoError(t, unix.Stat(filepath.Join(fixture.data, "pending"), &stat))
			assert.Equal(t, before.Backing.Inode, stat.Ino)
			assert.Equal(t, candidateBytes, stat.Size)
		})
	}
}

// Rationale: cancellation is scoped to the initialized selector and the exact cache/owner/VM
// tuple. A corrupt but well-formed record must not turn retained descriptors into deletion rights.
func TestProtectedImportCancellationRereadsLogicalAndRecordAuthority(t *testing.T) {
	for _, field := range []string{"uninitialized", "logical cache", "record cache", "record owner", "record VM"} {
		t.Run(field, func(t *testing.T) {
			fixture := newProtectedImportCancellationFixture(
				t,
				protectedBackingCloudInit,
				"copying",
				"pending",
				true,
				4,
			)
			record := fixture.readRecord(t)
			logical := protectedLogicalRecord{
				Version: 1, Owner: 1000, Kind: protectedBackingCloudInit, ID: testVMID,
				Cache: record.Cache, Initialized: true,
			}
			switch field {
			case "uninitialized":
				logical.Initialized = false
			case "logical cache":
				logical.Cache.Inode++
			case "record cache":
				record.Cache.Inode++
			case "record owner":
				record.Owner++
			case "record VM":
				record.ID = "11111111111111111111111111111111"
			}
			require.NoError(
				t,
				writeProtectedRecord(t.Context(), fixture.cancellation.logicalFD, "logical.json", logical),
			)
			require.NoError(
				t,
				writeProtectedRecord(t.Context(), fixture.cancellation.metadataFD, "record.json", record),
			)
			assert.Error(t, fixture.cancellation.readAuthority(
				t.Context(), 1000, fixture.cache.identity, protectedBackingCloudInit, testVMID,
			))
			if diff := cmp.Diff(record, fixture.readRecord(t)); diff != "" {
				t.Errorf("refused root metadata changed (-want +got):\n%s", diff)
			}
			var stat unix.Stat_t
			require.NoError(t, unix.Stat(filepath.Join(fixture.data, "pending"), &stat))
			assert.Equal(t, record.Backing.Inode, stat.Ino)
			assert.Equal(t, int64(4), stat.Size)
		})
	}
}

func TestProtectedImportRetirementRejectsLostOrForeignVMAuthority(t *testing.T) {
	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		mutate func(*protectedImportRetirement)
	}{
		{"released lock", func(r *protectedImportRetirement) { r.locked.vmLock.state.released = true }},
		{"inactive lock", func(r *protectedImportRetirement) { r.locked.vmLock.state.active = false }},
		{"foreign owner", func(r *protectedImportRetirement) { r.locked.record.ownerUID++ }},
		{"foreign VM", func(r *protectedImportRetirement) { r.locked.record.vmID = "11111111111111111111111111111111" }},
		{"uncleaned VM", func(r *protectedImportRetirement) { r.locked.record.lifecycle = instanceLifecycleCleaning }},
		{"operator execution identity", func(r *protectedImportRetirement) { r.identity.uid = r.ownerUID }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			retirement := &protectedImportRetirement{
				ownerUID: 1000, vmID: testVMID, identity: vmExecutionIdentity{uid: 1001, gid: 1001},
				locked: &lockedInstance{
					record: instanceRecord{ownerUID: 1000, vmID: testVMID, lifecycle: instanceLifecycleCleaned},
					vmLock: &vmAuthorityLease{state: &vmAuthorityLeaseState{
						value: &injectedVMAuthority{}, ownerUID: 1000, key: key, active: true,
					}},
				},
			}
			tc.mutate(retirement)
			state := retirement.locked.vmLock.state
			state.mu.Lock()
			assert.Error(t, retirement.requireLockedAuthority(state))
			state.mu.Unlock()
		})
	}
}
