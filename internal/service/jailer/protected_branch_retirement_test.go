package jailer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func openProtectedBranchTestFD(t *testing.T, path string) int {
	t.Helper()
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	require.NoError(t, err)
	return fd
}

func TestProtectedBranchImportRefusesLayoutBeforeBindingUnseenIdentity(t *testing.T) {
	// Rationale: A refused old store must not permanently bind an unseen ID to the wrong cache.
	for _, failure := range []string{"old-layout", "missing-authority", "replaced-authority"} {
		t.Run(failure, func(t *testing.T) {
			f := newProtectedCacheCleanupFixture(t)
			authority := filepath.Join(f.anchorPath, protectedSnapshotLockDirectory)
			if failure == "old-layout" {
				f.record.Version = 1
				require.NoError(t, writeProtectedRecord(t.Context(), f.anchor, "store.json", f.record))
			} else {
				require.NoError(t, os.Rename(authority, filepath.Join(f.root, "original-authority")))
				if failure == "replaced-authority" {
					require.NoError(t, os.Mkdir(authority, 0700))
				}
			}
			id := strings.Repeat("b", 64)
			before := protectedCacheTestTree(t, f.root)
			owner, err := unix.Dup(f.owner)
			require.NoError(t, err)
			anchor, err := unix.Dup(f.anchor)
			require.NoError(t, err)
			_, err = protectBackingInAnchor(t.Context(), f.cache, protectedBackingKernel, id, 6, true,
				-1, false, anchor, []int{owner, anchor})
			require.Error(t, err)
			if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
				t.Errorf("refused import published authority or changed bytes (-want +got):\n%s", diff)
			}

			// The same never-bound ID can still be legitimately imported into a fresh cache.
			cachePath, anchorPath := filepath.Join(f.root, "fresh-cache"), filepath.Join(f.ownerPath, "fresh-anchor")
			require.NoError(t, os.Mkdir(cachePath, 0700))
			require.NoError(t, os.Chown(cachePath, 1000, 1000))
			require.NoError(t, os.Mkdir(anchorPath, 0700))
			cacheFD := openProtectedBranchTestFD(t, cachePath)
			t.Cleanup(func() { require.NoError(t, unix.Close(cacheFD)) })
			var stat unix.Stat_t
			require.NoError(t, unix.Fstat(cacheFD, &stat))
			locator, err := parseManagedCacheLocator(cachePath)
			require.NoError(t, err)
			cache := &managedCacheLease{cacheFD: cacheFD, ownerUID: 1000, ownerGID: 1000, locator: locator,
				identity: managedCacheIdentity{deviceMajor: unix.Major(uint64(stat.Dev)),
					deviceMinor: unix.Minor(uint64(stat.Dev)), inode: stat.Ino},
				deps: realManagedCacheDeps(), configDeps: realManagedFirecrackerConfigDeps()}
			sourcePath := filepath.Join(f.root, "trusted-snapshot-source")
			require.NoError(t, os.WriteFile(sourcePath, []byte("kernel"), 0400))
			source, err := unix.Open(sourcePath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, unix.Close(source)) })
			owner, err = unix.Dup(f.owner)
			require.NoError(t, err)
			anchor = openProtectedBranchTestFD(t, anchorPath)
			lease, err := protectBackingInAnchor(t.Context(), cache, protectedBackingKernel, id, 6, true,
				source, false, anchor, []int{owner, anchor})
			require.NoError(t, err)
			require.NoError(t, lease.Release(t.Context()))
			bytes, err := os.ReadFile(
				filepath.Join(cachePath, protectedBackingDirectory, protectedBackingKernel, id, "disk"),
			)
			require.NoError(t, err)
			assert.Equal(t, []byte("kernel"), bytes)
			bytes, err = os.ReadFile(filepath.Join(f.ownerPath, "logical", protectedBackingKernel, id, "logical.json"))
			require.NoError(t, err)
			var logical protectedLogicalRecord
			require.NoError(t, json.Unmarshal(bytes, &logical))
			want := protectedLogicalRecord{Version: 1, Owner: 1000, Kind: protectedBackingKernel, ID: id,
				Cache: protectedBackingCache{
					Major: unix.Major(uint64(stat.Dev)),
					Minor: unix.Minor(uint64(stat.Dev)),
					Inode: stat.Ino,
				},
				Initialized: true}
			if diff := cmp.Diff(want, logical); diff != "" {
				t.Errorf("successful fresh import logical authority (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProtectedBranchInterruptedSnapshotCaptureRetainsRecoveryOutcome(t *testing.T) {
	// Rationale: A retry must expose the retained transaction, never erase it or recreate its lock.
	for _, stage := range []string{"paused", "publishing", "published", "missing-lock"} {
		t.Run(stage, func(t *testing.T) {
			f := newProtectedCacheCleanupFixture(t)
			data := openProtectedBranchTestFD(t, filepath.Join(f.cachePath, protectedBackingDirectory, "snapshots"))
			authority, err := openProtectedSnapshotLockAuthority(t.Context(), f.cache, f.anchor)
			require.NoError(t, err)
			store := &protectedSnapshotStoreLease{
				fd:              data,
				lockAuthorityFD: authority,
				retained:        []int{data, authority},
			}
			t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
			id := strings.Repeat("b", 64)
			entry, err := lockSnapshotStoreEntry(t.Context(), store, id, true)
			require.NoError(t, err)
			progress := snapshotCaptureProgress{SnapshotID: id, SourceVMID: testVMID, OwnerUID: 1000,
				LaunchGeneration: 1, Stage: stage, VMState: "paused", Published: stage == "published"}
			require.NoError(t, entry.writeProgress(t.Context(), progress))
			require.NoError(t, entry.Release(t.Context()))
			if stage == "missing-lock" {
				require.NoError(t, os.Rename(filepath.Join(f.anchorPath, protectedSnapshotLockDirectory, id+".lock"),
					filepath.Join(f.root, "retained-original-lock")))
			}
			before := protectedCacheTestTree(t, f.root)
			entry, err = createSnapshotCaptureEntry(t.Context(), store, id, testVMID, 1000)
			require.Nil(t, entry)
			require.Error(t, err)
			if stage == "missing-lock" {
				assert.ErrorIs(t, err, unix.ENOENT)
				assert.NotErrorIs(t, err, unix.EEXIST)
			} else {
				var outcome *errs.DomainError
				require.True(t, errors.As(err, &outcome))
				publication := "unpublished"
				if stage == "publishing" {
					publication = "unknown"
				} else if stage == "published" {
					publication = "published"
				}
				assert.Equal(t, errs.CodeSnapshotCreateFailed, outcome.Code)
				want := map[string]any{"publication_state": publication, "snapshot_id": id,
					"source_vm_id": testVMID, "snapshot_stage": stage, "vm_state": "unknown",
					"snapshot_published": stage == "published"}
				if diff := cmp.Diff(want, outcome.Details); diff != "" {
					t.Errorf("interrupted capture recovery details (-want +got):\n%s", diff)
				}
				assert.Contains(t, outcome.Message, "no automatic resume was attempted")
				assert.Contains(t, outcome.Err.Error(), "inspect or remove it before creating a new capture")
			}
			if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
				t.Errorf("capture retry changed retained transaction (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProtectedBranchSnapshotLockSurvivesRemovalAndRetirement(t *testing.T) {
	// Rationale: Removing data must never retire the independent inode defining snapshot exclusion.
	f := newProtectedCacheCleanupFixture(t)
	data := openProtectedBranchTestFD(t, filepath.Join(f.cachePath, protectedBackingDirectory, "snapshots"))
	authority, err := openProtectedSnapshotLockAuthority(t.Context(), f.cache, f.anchor)
	require.NoError(t, err)
	use, err := acquireProtectedStoreUse(t.Context(), f.anchor, false)
	require.NoError(t, err)
	store := &protectedSnapshotStoreLease{fd: data, lockAuthorityFD: authority, retained: []int{data, authority, use}}
	t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
	id := strings.Repeat("d", 64)
	entry, err := lockSnapshotStoreEntry(t.Context(), store, id, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, entry.Release(context.Background())) })
	artifact := filepath.Join(f.cachePath, protectedBackingDirectory, "snapshots", id, "rootfs.img")
	require.NoError(t, os.WriteFile(artifact, []byte("independent snapshot bytes"), 0400))
	reference := filepath.Join(filepath.Dir(artifact), "refs", testVMID+".json")
	require.NoError(t, os.WriteFile(reference, []byte("retained restored VM"), 0600))
	before := protectedCacheTestTree(t, f.root)
	require.Error(t, entry.remove(t.Context()))
	require.Error(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
		t.Errorf("restored reference lost data or authority (-want +got):\n%s", diff)
	}
	require.NoError(t, os.Remove(reference))
	require.NoError(t, entry.remove(t.Context()))
	require.NoError(t, store.Release(t.Context()))
	before = protectedCacheTestTree(t, f.root)
	require.Error(
		t,
		retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner),
		"independent held lock still excludes cleanup",
	)
	if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
		t.Errorf("held permanent lock refusal changed state (-want +got):\n%s", diff)
	}
	require.NoError(t, entry.Release(t.Context()))
	locksBefore := protectedCacheTestTree(t, filepath.Join(f.anchorPath, protectedSnapshotLockDirectory))
	require.NoError(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	if diff := cmp.Diff(locksBefore, protectedCacheTestTree(t, filepath.Join(f.anchorPath, protectedSnapshotLockDirectory))); diff != "" {
		t.Errorf("cache retirement changed permanent snapshot lock inode (-want +got):\n%s", diff)
	}
	assert.NoDirExists(t, filepath.Join(f.cachePath, protectedBackingDirectory))
}

type protectedInitialVolumeFixture struct {
	cache   *protectedCacheCleanupFixture
	storage *protectedVolumeStorage
	input   protectedBackingRecord
	rawPath string
	receipt initialVolumeRemovalReceipt
}

func newProtectedInitialVolumeFixture(t *testing.T, protected bool) *protectedInitialVolumeFixture {
	return newProtectedInitialVolumeFixtureOnFilesystem(t, protected, false)
}

func newProtectedInitialVolumeFixtureOnFilesystem(
	t *testing.T,
	protected, distinct bool,
) *protectedInitialVolumeFixture {
	t.Helper()
	f := newProtectedCacheCleanupFixtureOnFilesystem(t, distinct)
	input := f.objects[protectedBackingVolume]
	input.SnapshotSource = false
	directory := filepath.Join(f.cachePath, "volumes")
	require.NoError(t, os.Mkdir(directory, 0700))
	require.NoError(t, os.Chown(directory, 1000, 1000))
	rawPath := filepath.Join(directory, input.ID+".raw")
	require.NoError(t, os.WriteFile(rawPath, []byte("original managed raw volume bytes"), 0600))
	require.NoError(t, os.Chown(rawPath, 1000, 1000))
	var raw, parent unix.Stat_t
	var rawMount, parentMount unix.Statx_t
	require.NoError(t, unix.Lstat(rawPath, &raw))
	require.NoError(t, unix.Lstat(directory, &parent))
	require.NoError(t, unix.Statx(unix.AT_FDCWD, rawPath, 0, unix.STATX_MNT_ID, &rawMount))
	require.NoError(t, unix.Statx(unix.AT_FDCWD, directory, 0, unix.STATX_MNT_ID, &parentMount))
	receipt := initialVolumeRemovalReceipt{Schema: 1, VolumeID: input.ID, DeviceMajor: unix.Major(uint64(raw.Dev)),
		DeviceMinor: unix.Minor(uint64(raw.Dev)), Inode: raw.Ino, MountID: rawMount.Mnt_id,
		UID: raw.Uid, GID: raw.Gid, Mode: uint16(raw.Mode), Size: uint64(raw.Size),
		DirectoryDeviceMajor: unix.Major(uint64(parent.Dev)), DirectoryDeviceMinor: unix.Minor(uint64(parent.Dev)),
		DirectoryInode: parent.Ino, DirectoryMountID: parentMount.Mnt_id}
	if protected {
		input.Source = protectedBackingIdentity{Device: uint64(raw.Dev), Inode: raw.Ino, Size: raw.Size}
		input.SourceCTimeSec, input.SourceCTimeNsec = raw.Ctim.Sec, raw.Ctim.Nsec
	} else {
		input.Phase, input.Directory = "absent", protectedBackingIdentity{}
		data := filepath.Join(f.cachePath, protectedBackingDirectory, protectedBackingVolume)
		require.NoError(t, os.Remove(filepath.Join(data, input.ID)))
		require.NoError(t, os.Remove(data))
	}
	f.writeObject(t, protectedBackingVolume, input)
	metadata := openProtectedBranchTestFD(t, filepath.Join(f.anchorPath, protectedBackingVolume, input.ID))
	logical := openProtectedBranchTestFD(t, filepath.Join(f.ownerPath, "logical", protectedBackingVolume, input.ID))
	if !protected {
		require.NoError(t, writeProtectedRecord(t.Context(), logical, "logical.json", protectedLogicalRecord{
			Version: 1, Owner: 1000, Kind: protectedBackingVolume, ID: input.ID, Cache: input.Cache}))
	}
	lock, err := acquireProtectedLock(t.Context(), metadata)
	require.NoError(t, err)
	logicalLock, err := acquireProtectedLock(t.Context(), logical)
	require.NoError(t, err)
	use, err := acquireProtectedStoreUse(t.Context(), f.anchor, false)
	require.NoError(t, err)
	backing := &protectedBackingLease{fd: -1, lockFD: lock, metadataFD: metadata, logicalFD: logical,
		directoryFD: -1, ownerUID: 1000, cacheIdentity: f.cache.identity, record: input,
		retained: []int{metadata, logical, lock, logicalLock, use}}
	t.Cleanup(func() { require.NoError(t, backing.Release(context.Background())) })
	return &protectedInitialVolumeFixture{cache: f, storage: &protectedVolumeStorage{cache: f.cache,
		backing: backing, protected: protected}, input: input, rawPath: rawPath, receipt: receipt}
}

func (f *protectedInitialVolumeFixture) branch(t *testing.T) *protectedSnapshotStoreLease {
	t.Helper()
	fd := openProtectedBranchTestFD(t, filepath.Join(f.cache.cachePath, protectedBackingDirectory, "volume-removals"))
	lease := &protectedSnapshotStoreLease{fd: fd, lockAuthorityFD: -1, branch: "volume-removals",
		cacheIdentity: f.cache.cache.identity, ownerUID: 1000, retained: []int{fd}}
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	return lease
}

func (f *protectedInitialVolumeFixture) read(t *testing.T) protectedBackingRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.cache.anchorPath, protectedBackingVolume, f.input.ID, "record.json"))
	require.NoError(t, err)
	var record protectedBackingRecord
	require.NoError(t, json.Unmarshal(data, &record))
	return record
}

func TestProtectedBranchVolumeReceiptSurvivesTerminalCacheRetirement(t *testing.T) {
	for _, distinct := range []bool{false, true} {
		filesystem := "same authority filesystem"
		if distinct {
			filesystem = "distinct cache filesystem"
		}
		t.Run(filesystem, func(t *testing.T) {
			for _, protected := range []bool{false, true} {
				name := "initially unprotected"
				if protected {
					name = "previously protected"
				}
				t.Run(name, func(t *testing.T) {
					f := newProtectedInitialVolumeFixtureOnFilesystem(t, protected, distinct)
					var authority unix.Stat_t
					require.NoError(t, unix.Fstat(f.cache.anchor, &authority))
					assert.Equal(
						t,
						distinct,
						uint64(authority.Dev) != unix.Mkdev(f.receipt.DeviceMajor, f.receipt.DeviceMinor),
					)
					require.NoError(t, f.storage.removeInitialRawFromStore(t.Context(), f.input.ID, f.branch(t)))
					quarantine := filepath.Join(
						f.cache.cachePath,
						protectedBackingDirectory,
						"volume-removals",
						f.input.ID,
					)
					var stat unix.Stat_t
					require.NoError(t, unix.Lstat(quarantine, &stat))
					assert.Equal(t, uint32(0), stat.Uid)
					assert.Equal(t, uint32(unix.S_IFDIR|0700), stat.Mode)
					leaves, err := os.ReadDir(quarantine)
					require.NoError(t, err)
					assert.Empty(t, leaves, "receipt/marker authority never remains in cache data")
					want := f.input
					want.Phase = "removed"
					want.InitialRemoval = &initialVolumeRemovalProgress{Phase: "removed", Receipt: &f.receipt,
						Quarantine: protectedBackingIdentity{
							Device: uint64(stat.Dev),
							Inode:  stat.Ino,
							Size:   stat.Size,
						}}
					if diff := cmp.Diff(want, f.read(t)); diff != "" {
						t.Errorf("terminal root receipt (-want +got):\n%s", diff)
					}
					assert.NoFileExists(t, f.rawPath)
					replacement := []byte("unrelated replacement must never be adopted")
					require.NoError(t, os.WriteFile(f.rawPath, replacement, 0600))
					require.NoError(t, os.Chown(f.rawPath, 1000, 1000))
					before := protectedCacheTestTree(t, f.cache.root)
					require.NoError(t, f.storage.removeInitialRaw(t.Context(), f.input.ID))
					if diff := cmp.Diff(before, protectedCacheTestTree(t, f.cache.root)); diff != "" {
						t.Errorf("completed removal adopted replacement or changed receipt (-want +got):\n%s", diff)
					}
					require.NoError(t, f.storage.backing.Release(t.Context()))
					authorityBefore := protectedCacheTestTree(t, f.cache.ownerPath)
					require.NoError(t, retireProtectedCache(t.Context(), f.cache.cache, f.cache.anchor, f.cache.owner))
					authorityAfter := protectedCacheTestTree(t, f.cache.ownerPath)
					delete(authorityBefore, "/cache-anchor/store.json")
					delete(authorityAfter, "/cache-anchor/store.json")
					if diff := cmp.Diff(authorityBefore, authorityAfter); diff != "" {
						t.Errorf("cache retirement lost permanent source receipt (-want +got):\n%s", diff)
					}
					got, err := os.ReadFile(f.rawPath)
					require.NoError(t, err)
					if diff := cmp.Diff(replacement, got); diff != "" {
						t.Errorf("replacement bytes (-want +got):\n%s", diff)
					}
				})
			}
		})
	}
}

func TestProtectedBranchVolumeMissingSourceCommitsExactTerminalEvidence(t *testing.T) {
	f := newProtectedInitialVolumeFixture(t, false)
	require.NoError(t, os.Remove(f.rawPath))
	require.NoError(t, f.storage.removeInitialRawFromStore(t.Context(), f.input.ID, f.branch(t)))
	var stat unix.Stat_t
	require.NoError(
		t,
		unix.Lstat(filepath.Join(f.cache.cachePath, protectedBackingDirectory, "volume-removals", f.input.ID), &stat),
	)
	want := f.input
	want.Phase = "removed"
	want.InitialRemoval = &initialVolumeRemovalProgress{Phase: "removed", SourceAbsent: true,
		Quarantine: protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size}}
	if diff := cmp.Diff(want, f.read(t)); diff != "" {
		t.Errorf("synchronized absent-source terminal record (-want +got):\n%s", diff)
	}
	require.NoError(t, f.storage.backing.Release(t.Context()))
	require.NoError(t, retireProtectedCache(t.Context(), f.cache.cache, f.cache.anchor, f.cache.owner))
}

func TestProtectedBranchVolumeRefusesUnrecordedOrChangedQuarantine(t *testing.T) {
	for _, scenario := range []string{"unrecorded empty", "old receipt", "recorded inode replaced", "captured inode replaced"} {
		t.Run(scenario, func(t *testing.T) {
			f := newProtectedInitialVolumeFixture(t, true)
			path := filepath.Join(f.cache.cachePath, protectedBackingDirectory, "volume-removals", f.input.ID)
			if scenario == "unrecorded empty" || scenario == "old receipt" {
				require.NoError(t, os.Mkdir(path, 0700))
				if scenario == "old receipt" {
					require.NoError(t, os.WriteFile(filepath.Join(path, "receipt.json"), []byte("old receipt"), 0600))
				}
			} else {
				require.NoError(t, f.storage.backing.BeginInitialVolumeRemoval(t.Context()))
				quarantine, err := f.storage.backing.openInitialVolumeQuarantine(t.Context(), f.cache.cache, f.branch(t))
				require.NoError(t, err)
				if scenario == "recorded inode replaced" {
					require.NoError(t, os.Rename(path, filepath.Join(f.cache.root, "retained-quarantine")))
					require.NoError(t, os.Mkdir(path, 0700))
				} else {
					progress := f.storage.backing.record.InitialRemoval
					progress.Phase, progress.Receipt = "prepared", &f.receipt
					require.NoError(t, writeProtectedRecord(t.Context(), f.storage.backing.metadataFD, "record.json", f.storage.backing.record))
					source := openProtectedBranchTestFD(t, filepath.Dir(f.rawPath))
					require.NoError(t, unix.Renameat2(source, f.input.ID+".raw", quarantine, "data.raw", unix.RENAME_NOREPLACE))
					require.NoError(t, unix.Close(source))
					require.NoError(t, os.Rename(filepath.Join(path, "data.raw"), filepath.Join(f.cache.root, "retained-raw")))
					require.NoError(t, os.WriteFile(filepath.Join(path, "data.raw"), []byte("unadmitted replacement"), 0600))
				}
			}
			before := protectedCacheTestTree(t, f.cache.root)
			require.Error(t, f.storage.removeInitialRawFromStore(t.Context(), f.input.ID, f.branch(t)))
			if diff := cmp.Diff(before, protectedCacheTestTree(t, f.cache.root)); diff != "" {
				t.Errorf("ambiguous source-removal authority changed retained evidence (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProtectedBranchVolumeRemovalRecordedInterruptionRecovery(t *testing.T) {
	// Rationale: Only the retained external receipt distinguishes an applied capture/unlink.
	for _, stage := range []string{"preparation intent", "quarantine recorded", "receipt prepared", "capture visible", "captured committed", "unlink visible"} {
		t.Run(stage, func(t *testing.T) {
			f := newProtectedInitialVolumeFixture(t, true)
			require.NoError(t, f.storage.backing.BeginInitialVolumeRemoval(t.Context()))
			if stage != "preparation intent" {
				quarantine, err := f.storage.backing.openInitialVolumeQuarantine(
					t.Context(),
					f.cache.cache,
					f.branch(t),
				)
				require.NoError(t, err)
				if stage != "quarantine recorded" {
					progress := f.storage.backing.record.InitialRemoval
					progress.Phase, progress.Receipt = "prepared", &f.receipt
					require.NoError(
						t,
						writeProtectedRecord(
							t.Context(),
							f.storage.backing.metadataFD,
							"record.json",
							f.storage.backing.record,
						),
					)
					if stage != "receipt prepared" {
						source := openProtectedBranchTestFD(t, filepath.Dir(f.rawPath))
						require.NoError(
							t,
							unix.Renameat2(source, f.input.ID+".raw", quarantine, "data.raw", unix.RENAME_NOREPLACE),
						)
						require.NoError(t, unix.Close(source))
						if stage != "capture visible" {
							progress.Phase = "captured"
							require.NoError(
								t,
								writeProtectedRecord(
									t.Context(),
									f.storage.backing.metadataFD,
									"record.json",
									f.storage.backing.record,
								),
							)
							if stage == "unlink visible" {
								require.NoError(t, unix.Unlinkat(quarantine, "data.raw", 0))
							}
						}
					}
				}
			}
			before := protectedCacheTestTree(t, f.cache.root)
			require.Error(t, retireProtectedCache(t.Context(), f.cache.cache, f.cache.anchor, f.cache.owner))
			if diff := cmp.Diff(before, protectedCacheTestTree(t, f.cache.root)); diff != "" {
				t.Errorf("pending source removal was erased by cleanup (-want +got):\n%s", diff)
			}
			require.NoError(t, f.storage.removeInitialRawFromStore(t.Context(), f.input.ID, f.branch(t)))
			assert.NoFileExists(t, f.rawPath)
			got := f.read(t)
			assert.Equal(t, "removed", got.Phase)
			assert.Equal(t, "removed", got.InitialRemoval.Phase)
			if diff := cmp.Diff(&f.receipt, got.InitialRemoval.Receipt); diff != "" {
				t.Errorf("recovered exact source receipt (-want +got):\n%s", diff)
			}
			require.NoError(t, f.storage.backing.Release(t.Context()))
			require.NoError(t, retireProtectedCache(t.Context(), f.cache.cache, f.cache.anchor, f.cache.owner))
		})
	}
}

func TestProtectedBranchOldOrMissingAuthorityRefusesBeforeRetirement(t *testing.T) {
	cases := map[string]func(*testing.T, *protectedCacheCleanupFixture){
		"old layout": func(t *testing.T, f *protectedCacheCleanupFixture) {
			f.record.Version = 1
			require.NoError(t, writeProtectedRecord(t.Context(), f.anchor, "store.json", f.record))
		},
		"missing authority": func(t *testing.T, f *protectedCacheCleanupFixture) {
			require.NoError(
				t,
				os.Rename(
					filepath.Join(f.anchorPath, protectedSnapshotLockDirectory),
					filepath.Join(f.root, "retained-locks"),
				),
			)
		},
		"replaced authority": func(t *testing.T, f *protectedCacheCleanupFixture) {
			path := filepath.Join(f.anchorPath, protectedSnapshotLockDirectory)
			require.NoError(t, os.Rename(path, filepath.Join(f.root, "retained-locks")))
			require.NoError(t, os.Mkdir(path, 0700))
		},
		"unknown lock": func(t *testing.T, f *protectedCacheCleanupFixture) {
			require.NoError(
				t,
				os.WriteFile(filepath.Join(f.anchorPath, protectedSnapshotLockDirectory, "unknown"), nil, 0600),
			)
		},
		"nonempty lock": func(t *testing.T, f *protectedCacheCleanupFixture) {
			require.NoError(
				t,
				os.WriteFile(
					filepath.Join(f.anchorPath, protectedSnapshotLockDirectory, strings.Repeat("d", 64)+".lock"),
					[]byte("not a lock"),
					0600,
				),
			)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newProtectedCacheCleanupFixture(t)
			mutate(t, f)
			before := protectedCacheTestTree(t, f.root)
			require.Error(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
			if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
				t.Errorf("invalid branch authority was repaired or deleted (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProtectedBranchFreshStorePublishesExactLockAuthority(t *testing.T) {
	f := newProtectedCacheCleanupFixture(t)
	cachePath := filepath.Join(f.root, "fresh-cache")
	anchorPath := filepath.Join(f.root, "fresh-anchor")
	require.NoError(t, os.Mkdir(cachePath, 0700))
	require.NoError(t, os.Chown(cachePath, 1000, 1000))
	require.NoError(t, os.Mkdir(anchorPath, 0700))
	cacheFD := openProtectedBranchTestFD(t, cachePath)
	anchorFD := openProtectedBranchTestFD(t, anchorPath)
	t.Cleanup(func() { require.NoError(t, closeProtectedFDs([]int{cacheFD, anchorFD})) })
	var cacheStat unix.Stat_t
	require.NoError(t, unix.Fstat(cacheFD, &cacheStat))
	locator, err := parseManagedCacheLocator(cachePath)
	require.NoError(t, err)
	cache := &managedCacheLease{cacheFD: cacheFD, ownerUID: 1000, ownerGID: 1000, locator: locator,
		identity: managedCacheIdentity{
			deviceMajor: unix.Major(uint64(cacheStat.Dev)),
			deviceMinor: unix.Minor(uint64(cacheStat.Dev)),
			inode:       cacheStat.Ino,
		},
		deps: realManagedCacheDeps(), configDeps: realManagedFirecrackerConfigDeps()}
	lock, err := acquireProtectedLock(t.Context(), anchorFD)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Close(lock)) })
	store, err := pinProtectedStore(t.Context(), cache, anchorFD, true)
	require.NoError(t, err)
	require.NoError(t, unix.Close(store))
	var storeStat, lockStat unix.Stat_t
	require.NoError(t, unix.Lstat(filepath.Join(cachePath, protectedBackingDirectory), &storeStat))
	require.NoError(t, unix.Lstat(filepath.Join(anchorPath, protectedSnapshotLockDirectory), &lockStat))
	data, err := os.ReadFile(filepath.Join(anchorPath, "store.json"))
	require.NoError(t, err)
	var got protectedBackingAnchor
	require.NoError(t, json.Unmarshal(data, &got))
	want := protectedBackingAnchor{Version: 2, Owner: 1000, OwnerGID: 1000, Locator: cachePath,
		Cache: protectedBackingCache{
			Major: unix.Major(uint64(cacheStat.Dev)),
			Minor: unix.Minor(uint64(cacheStat.Dev)),
			Inode: cacheStat.Ino,
		},
		Store: protectedBackingIdentity{
			Device: uint64(storeStat.Dev),
			Inode:  storeStat.Ino,
			Size:   storeStat.Size,
		},
		SnapshotLocks: protectedBackingIdentity{Device: uint64(lockStat.Dev), Inode: lockStat.Ino, Size: lockStat.Size}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("fresh layout publication (-want +got):\n%s", diff)
	}
	assert.Equal(t, uint32(0), lockStat.Uid)
	assert.Equal(t, uint32(unix.S_IFDIR|0700), lockStat.Mode)
	require.NoError(
		t,
		os.Rename(
			filepath.Join(anchorPath, protectedSnapshotLockDirectory),
			filepath.Join(f.root, "retained-authority"),
		),
	)
	before := protectedCacheTestTree(t, f.root)
	_, err = pinProtectedStore(t.Context(), cache, anchorFD, true)
	require.Error(t, err)
	if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
		t.Errorf("populated layout recreated missing authority (-want +got):\n%s", diff)
	}
}

func TestProtectedBranchTerminalVolumeQuarantineRequiresExactEmptySuffix(t *testing.T) {
	for _, scenario := range []string{"missing", "replaced", "nonempty", "old marker"} {
		t.Run(scenario, func(t *testing.T) {
			f := newProtectedInitialVolumeFixture(t, true)
			require.NoError(t, f.storage.removeInitialRawFromStore(t.Context(), f.input.ID, f.branch(t)))
			require.NoError(t, f.storage.backing.Release(t.Context()))
			path := filepath.Join(f.cache.cachePath, protectedBackingDirectory, "volume-removals", f.input.ID)
			switch scenario {
			case "missing", "replaced":
				require.NoError(t, os.Rename(path, filepath.Join(f.cache.root, "retained-quarantine")))
				if scenario == "replaced" {
					require.NoError(t, os.Mkdir(path, 0700))
				}
			case "nonempty":
				require.NoError(t, os.WriteFile(filepath.Join(path, "data.raw"), []byte("retain unknown bytes"), 0600))
			case "old marker":
				require.NoError(t, os.WriteFile(filepath.Join(path, "captured"), nil, 0600))
			}
			before := protectedCacheTestTree(t, f.cache.root)
			require.Error(t, retireProtectedCache(t.Context(), f.cache.cache, f.cache.anchor, f.cache.owner))
			if diff := cmp.Diff(before, protectedCacheTestTree(t, f.cache.root)); diff != "" {
				t.Errorf("invalid terminal volume suffix changed evidence (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProtectedBranchChangedProtectedSourceRetainsBytesAndIntent(t *testing.T) {
	f := newProtectedInitialVolumeFixture(t, true)
	changed := []byte("changed source extent")
	require.NoError(t, os.WriteFile(f.rawPath, changed, 0600))
	require.Error(t, f.storage.removeInitialRawFromStore(t.Context(), f.input.ID, f.branch(t)))
	got, err := os.ReadFile(f.rawPath)
	require.NoError(t, err)
	if diff := cmp.Diff(changed, got); diff != "" {
		t.Errorf("changed source was consumed (-want +got):\n%s", diff)
	}
	var quarantine unix.Stat_t
	require.NoError(
		t,
		unix.Lstat(
			filepath.Join(f.cache.cachePath, protectedBackingDirectory, "volume-removals", f.input.ID),
			&quarantine,
		),
	)
	want := f.input
	want.Phase = "initial-removing"
	want.InitialRemoval = &initialVolumeRemovalProgress{Phase: "preparing",
		Quarantine: protectedBackingIdentity{
			Device: uint64(quarantine.Dev),
			Inode:  quarantine.Ino,
			Size:   quarantine.Size,
		}}
	if diff := cmp.Diff(want, f.read(t)); diff != "" {
		t.Errorf("source refusal changed retained identity (-want +got):\n%s", diff)
	}
	require.NoError(t, f.storage.backing.Release(t.Context()))
	before := protectedCacheTestTree(t, f.cache.root)
	require.Error(t, retireProtectedCache(t.Context(), f.cache.cache, f.cache.anchor, f.cache.owner))
	if diff := cmp.Diff(before, protectedCacheTestTree(t, f.cache.root)); diff != "" {
		t.Errorf("cache cleanup erased incomplete source removal (-want +got):\n%s", diff)
	}
}

func TestProtectedBranchCacheRetirementRecoversRemovedVolumeQuarantine(t *testing.T) {
	f := newProtectedInitialVolumeFixture(t, true)
	require.NoError(t, f.storage.removeInitialRawFromStore(t.Context(), f.input.ID, f.branch(t)))
	receipt := f.read(t)
	require.NoError(t, f.storage.backing.Release(t.Context()))
	deps := realProtectedCacheCaptureDeps()
	syncParent := deps.syncCacheParent
	deps.syncCacheParent = func(ctx context.Context, cache int) error {
		if f.cache.readAnchor(t).Retirement.Phase == "removing" {
			return unix.EIO
		}
		return syncParent(ctx, cache)
	}
	require.ErrorIs(
		t,
		retireProtectedCacheWithCapture(t.Context(), f.cache.cache, f.cache.anchor, f.cache.owner, deps),
		unix.EIO,
	)
	assert.Equal(t, "removing", f.cache.readAnchor(t).Retirement.Phase)
	assert.NoDirExists(
		t,
		filepath.Join(f.cache.cachePath, ProtectedCacheQuarantineDirectory, protectedCacheCapturedStore),
	)
	require.NoError(t, retireProtectedCache(t.Context(), f.cache.cache, f.cache.anchor, f.cache.owner))
	if diff := cmp.Diff(receipt, f.read(t)); diff != "" {
		t.Errorf("retirement replay changed external source receipt (-want +got):\n%s", diff)
	}
	var quarantine unix.Stat_t
	require.NoError(t, unix.Lstat(filepath.Join(f.cache.cachePath, ProtectedCacheQuarantineDirectory), &quarantine))
	want := f.cache.record
	directories := append([]protectedCacheDirectory{}, f.cache.manifest...)
	directories = append(
		directories,
		protectedCacheDirectory{
			Parent:   "volume-removals",
			Name:     f.input.ID,
			Identity: receipt.InitialRemoval.Quarantine,
		},
	)
	want.Retirement = &protectedCacheRetirement{Phase: "removed", Directories: directories,
		Quarantine: &protectedBackingIdentity{
			Device: uint64(quarantine.Dev),
			Inode:  quarantine.Ino,
			Size:   quarantine.Size,
		}}
	ordered := cmpopts.SortSlices(func(left, right protectedCacheDirectory) bool {
		return left.Parent+"/"+left.Name < right.Parent+"/"+right.Name
	})
	if diff := cmp.Diff(want, f.cache.readAnchor(t), ordered); diff != "" {
		t.Errorf("typed volume suffix retirement manifest (-want +got):\n%s", diff)
	}
}
