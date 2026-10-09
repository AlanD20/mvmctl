package jailer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type protectedCacheCleanupFixture struct {
	root, cachePath, ownerPath, anchorPath string
	cache                                  *managedCacheLease
	owner, anchor                          int
	record                                 protectedBackingAnchor
	objects                                map[string]protectedBackingRecord
	manifest                               []protectedCacheDirectory
}

// The fixture models the real root-owned anchor/logical layout with admitted cache descriptors.
// Cleanup admission, durable metadata and rmdir effects are production code, confined to QA tmp.
func newProtectedCacheCleanupFixture(t *testing.T) *protectedCacheCleanupFixture {
	t.Helper()
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("protected cache effects require the disposable root-test VM")
	}
	f := &protectedCacheCleanupFixture{root: t.TempDir(), objects: make(map[string]protectedBackingRecord)}
	f.cachePath = filepath.Join(f.root, "cache")
	f.ownerPath = filepath.Join(f.root, "owner")
	f.anchorPath = filepath.Join(f.ownerPath, "cache-anchor")
	for _, path := range []string{f.cachePath, f.ownerPath, f.anchorPath} {
		require.NoError(t, os.Mkdir(path, 0700))
	}
	require.NoError(t, os.Chown(f.cachePath, 1000, 1000))
	var retained []int
	open := func(path string) int {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		require.NoError(t, err)
		retained = append(retained, fd)
		return fd
	}
	cacheFD := open(f.cachePath)
	f.owner, f.anchor = open(f.ownerPath), open(f.anchorPath)
	identity, err := protectedIdentity(cacheFD)
	require.NoError(t, err)
	locator, err := parseManagedCacheLocator(f.cachePath)
	require.NoError(t, err)
	f.cache = &managedCacheLease{cacheFD: cacheFD, ownerUID: 1000, ownerGID: 1000,
		identity: managedCacheIdentity{
			deviceMajor: unix.Major(identity.Device),
			deviceMinor: unix.Minor(identity.Device),
			inode:       identity.Inode,
		}, locator: locator, deps: realManagedCacheDeps(),
		configDeps: realManagedFirecrackerConfigDeps(), retained: []int{cacheFD}}
	store := filepath.Join(f.cachePath, protectedBackingDirectory)
	require.NoError(t, os.Mkdir(store, 0700))
	storeIdentity, err := protectedIdentity(open(store))
	require.NoError(t, err)
	f.record = protectedBackingAnchor{Version: 1, Owner: 1000, OwnerGID: 1000,
		Cache: protectedCacheIdentity(f.cache.identity), Locator: f.cachePath, Store: storeIdentity}
	require.NoError(t, writeProtectedRecord(t.Context(), f.anchor, "store.json", f.record))
	for _, branch := range []string{"snapshots", "volume-removals"} {
		path := filepath.Join(store, branch)
		require.NoError(t, os.Mkdir(path, 0700))
		identity, err := protectedIdentity(open(path))
		require.NoError(t, err)
		f.manifest = append(f.manifest, protectedCacheDirectory{Name: branch, Identity: identity})
	}
	for _, kind := range []string{protectedBackingKernel, protectedBackingRootfs,
		protectedBackingVolume, protectedBackingCloudInit} {
		id := strings.Repeat("a", protectedKindIDLength(kind))
		dataKind := filepath.Join(store, kind)
		data := filepath.Join(dataKind, id)
		metadata := filepath.Join(f.anchorPath, kind, id)
		logical := filepath.Join(f.ownerPath, "logical", kind, id)
		for _, path := range []string{data, metadata, logical} {
			require.NoError(t, os.MkdirAll(path, 0700))
		}
		kindIdentity, err := protectedIdentity(open(dataKind))
		require.NoError(t, err)
		directory, err := protectedIdentity(open(data))
		require.NoError(t, err)
		f.manifest = append(f.manifest, protectedCacheDirectory{Name: kind, Identity: kindIdentity},
			protectedCacheDirectory{Parent: kind, Name: id, Identity: directory})
		metadataFD, logicalFD := open(metadata), open(logical)
		for _, fd := range []int{metadataFD, logicalFD} {
			lock, err := acquireProtectedLock(t.Context(), fd)
			require.NoError(t, err)
			require.NoError(t, unix.Close(lock))
		}
		index := protectedLogicalRecord{Version: 1, Owner: 1000, Kind: kind, ID: id,
			Cache: f.record.Cache, Initialized: true}
		require.NoError(t, writeProtectedRecord(t.Context(), logicalFD, "logical.json", index))
		record := protectedBackingRecord{Version: 1, Owner: 1000, Kind: kind, ID: id, Cache: f.record.Cache,
			Directory: directory, Phase: "removed", References: []protectedBackingReference{}}
		if kind == protectedBackingKernel {
			// Reproduce actual kernel retirement rather than simply fabricating a terminal phase.
			content := []byte("same immutable protected kernel bytes")
			path := filepath.Join(data, "disk")
			require.NoError(t, os.WriteFile(path, content, 0600))
			disk, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
			require.NoError(t, err)
			retained = append(retained, disk)
			record.Backing, err = protectedIdentity(disk)
			require.NoError(t, err)
			record.Phase, record.Generation = "ready", 1
			lock, err := acquireProtectedLock(t.Context(), metadataFD)
			require.NoError(t, err)
			lease := &protectedBackingLease{fd: disk, lockFD: lock, directoryFD: open(data),
				metadataFD: metadataFD, record: record}
			require.NoError(t, lease.Remove(t.Context()))
			record = lease.record
			require.NoError(t, unix.Close(lock))
		} else {
			require.NoError(t, writeProtectedRecord(t.Context(), metadataFD, "record.json", record))
		}
		f.objects[kind] = record
	}
	for _, exclusive := range []bool{false, true} {
		lock, err := acquireProtectedStoreUse(t.Context(), f.anchor, exclusive)
		require.NoError(t, err)
		require.NoError(t, unix.Close(lock))
	}
	lock, err := acquireProtectedLock(t.Context(), f.anchor)
	require.NoError(t, err)
	require.NoError(t, unix.Close(lock))
	t.Cleanup(func() { require.NoError(t, closeProtectedFDs(retained)) })
	return f
}

func (f *protectedCacheCleanupFixture) writeObject(t *testing.T, kind string, record protectedBackingRecord) {
	t.Helper()
	fd, err := unix.Open(filepath.Join(f.anchorPath, kind, record.ID), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	require.NoError(t, writeProtectedRecord(t.Context(), fd, "record.json", record))
	require.NoError(t, unix.Close(fd))
}

func (f *protectedCacheCleanupFixture) readAnchor(t *testing.T) protectedBackingAnchor {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.anchorPath, "store.json"))
	require.NoError(t, err)
	var record protectedBackingAnchor
	require.NoError(t, json.Unmarshal(data, &record))
	return record
}

type protectedCacheTestNode struct {
	Mode, UID, GID uint32
	Device, Inode  uint64
	Content        string
}

// Snapshot content and namespace identity independently, excluding read-induced atime changes.
func protectedCacheTestTree(t *testing.T, root string) map[string]protectedCacheTestNode {
	t.Helper()
	nodes := make(map[string]protectedCacheTestNode)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		var stat unix.Stat_t
		if err := unix.Lstat(path, &stat); err != nil {
			return err
		}
		node := protectedCacheTestNode{Mode: stat.Mode, UID: stat.Uid, GID: stat.Gid,
			Device: uint64(stat.Dev), Inode: stat.Ino}
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			node.Content = string(data)
		}
		nodes[strings.TrimPrefix(path, root)] = node
		return nil
	}))
	return nodes
}

func TestProtectedCacheCleanupRefusesBeforeParentMutation(t *testing.T) {
	// Rationale: A successful public prune is not authority to erase unknown or live root data.
	cases := map[string]func(*testing.T, *protectedCacheCleanupFixture){
		"unknown category": func(t *testing.T, f *protectedCacheCleanupFixture) {
			require.NoError(t, os.Mkdir(filepath.Join(f.cachePath, protectedBackingDirectory, "unknown"), 0700))
		},
		"nonempty terminal object": func(t *testing.T, f *protectedCacheCleanupFixture) {
			require.NoError(t, os.WriteFile(filepath.Join(f.cachePath, protectedBackingDirectory, "kernel",
				f.objects["kernel"].ID, "unknown"), []byte("retain"), 0600))
		},
		"live rootfs": func(t *testing.T, f *protectedCacheCleanupFixture) {
			record := f.objects["rootfs"]
			record.Phase = "ready"
			f.writeObject(t, "rootfs", record)
		},
		"transitional cloud-init": func(t *testing.T, f *protectedCacheCleanupFixture) {
			record := f.objects["cloud-init"]
			record.Phase = "cancelling-copy"
			f.writeObject(t, "cloud-init", record)
		},
		"current root grant": func(t *testing.T, f *protectedCacheCleanupFixture) {
			record := f.objects["volume"]
			record.References = []protectedBackingReference{{VMID: testVMID, ExecutionUID: 1001}}
			f.writeObject(t, "volume", record)
		},
		"next root grant": func(t *testing.T, f *protectedCacheCleanupFixture) {
			record := f.objects["kernel"]
			record.NextReferences = []protectedBackingReference{{VMID: testVMID, ExecutionUID: 1001}}
			f.writeObject(t, "kernel", record)
		},
		"permanent snapshot lock": func(t *testing.T, f *protectedCacheCleanupFixture) {
			require.NoError(t, os.WriteFile(filepath.Join(f.cachePath, protectedBackingDirectory, "snapshots",
				strings.Repeat("b", 64)+".lock"), nil, 0600))
		},
		"volume removal receipt": func(t *testing.T, f *protectedCacheCleanupFixture) {
			require.NoError(t, os.Mkdir(filepath.Join(f.cachePath, protectedBackingDirectory, "volume-removals",
				strings.Repeat("b", 64)), 0700))
		},
		"changed directory ownership": func(t *testing.T, f *protectedCacheCleanupFixture) {
			require.NoError(t, os.Chown(filepath.Join(f.cachePath, protectedBackingDirectory, "kernel"), 1000, 1000))
		},
		"replaced object inode": func(t *testing.T, f *protectedCacheCleanupFixture) {
			path := filepath.Join(f.cachePath, protectedBackingDirectory, "kernel", f.objects["kernel"].ID)
			require.NoError(t, os.Rename(path, filepath.Join(f.root, "retained-original")))
			require.NoError(t, os.Mkdir(path, 0700))
		},
		"missing store without intent": func(t *testing.T, f *protectedCacheCleanupFixture) {
			require.NoError(
				t,
				os.Rename(
					filepath.Join(f.cachePath, protectedBackingDirectory),
					filepath.Join(f.root, "retained-store"),
				),
			)
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newProtectedCacheCleanupFixture(t)
			change(t, f)
			before := protectedCacheTestTree(t, f.root)
			err := retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner)
			require.Error(t, err)
			// The primary failure contract is preservation, including exact root record payloads.
			if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
				t.Errorf("refused cleanup changed retained authority (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProtectedCacheCleanupRetiresAllTerminalParents(t *testing.T) {
	f := newProtectedCacheCleanupFixture(t)
	before := protectedCacheTestTree(t, f.ownerPath)
	require.NoError(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	assert.NoDirExists(t, filepath.Join(f.cachePath, protectedBackingDirectory))
	got := f.readAnchor(t)
	want := f.record
	var quarantine unix.Stat_t
	require.NoError(t, unix.Lstat(filepath.Join(f.cachePath, ProtectedCacheQuarantineDirectory), &quarantine))
	want.Retirement = &protectedCacheRetirement{Phase: "removed", Directories: f.manifest,
		Quarantine: &protectedBackingIdentity{
			Device: uint64(quarantine.Dev),
			Inode:  quarantine.Ino,
			Size:   quarantine.Size,
		}}
	ordered := cmpopts.SortSlices(func(left, right protectedCacheDirectory) bool {
		return left.Parent+"/"+left.Name < right.Parent+"/"+right.Name
	})
	if diff := cmp.Diff(want, got, ordered); diff != "" {
		t.Errorf("retired anchor (-want +got):\n%s", diff)
	}
	after := protectedCacheTestTree(t, f.ownerPath)
	delete(before, "/cache-anchor/store.json")
	delete(after, "/cache-anchor/store.json")
	if diff := cmp.Diff(before, after); diff != "" {
		t.Errorf("permanent authority changed (-want +got):\n%s", diff)
	}
	require.NoError(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	for _, create := range []bool{false, true} {
		_, err := pinProtectedStore(t.Context(), f.cache, f.anchor, create)
		assert.Error(t, err, "ordinary lookup/import cannot reopen an explicitly retired store")
	}
}

func TestProtectedCacheCleanupExcludesRetainedUsers(t *testing.T) {
	// Rationale: Empty directories alone cannot authorize unlink while a retained lease can write.
	f := newProtectedCacheCleanupFixture(t)
	use, err := acquireProtectedStoreUse(t.Context(), f.anchor, false)
	require.NoError(t, err)
	lease := &protectedSnapshotStoreLease{fd: -1, retained: []int{use}}
	before := protectedCacheTestTree(t, f.root)
	require.Error(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
		t.Errorf("held lease cleanup changed state (-want +got):\n%s", diff)
	}
	require.NoError(t, lease.Release(context.Background()))
	require.NoError(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	assert.NoDirExists(t, filepath.Join(f.cachePath, protectedBackingDirectory))
}

func TestProtectedCacheCleanupReconcilesRecordedInterruptedSuffix(t *testing.T) {
	// Rationale: Only durable exact identities authorize missing parents after interrupted cleanup.
	for _, suffix := range []string{"one object removed", "store removed before completion"} {
		t.Run(suffix, func(t *testing.T) {
			f := newProtectedCacheCleanupFixture(t)
			original := filepath.Join(f.cachePath, protectedBackingDirectory)
			child := filepath.Join(original, protectedCacheQuarantineChild)
			require.NoError(t, os.Mkdir(child, 0700))
			var stat unix.Stat_t
			require.NoError(t, unix.Lstat(child, &stat))
			quarantine := filepath.Join(f.cachePath, ProtectedCacheQuarantineDirectory)
			require.NoError(t, os.Rename(child, quarantine))
			captured := filepath.Join(quarantine, protectedCacheCapturedStore)
			require.NoError(t, os.Rename(original, captured))
			intent := f.record
			intent.Retirement = &protectedCacheRetirement{Phase: "removing", Directories: f.manifest,
				Quarantine: &protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size}}
			require.NoError(t, writeProtectedRecord(t.Context(), f.anchor, "store.json", intent))
			if suffix == "one object removed" {
				require.NoError(
					t,
					os.Remove(filepath.Join(captured, "kernel", f.objects["kernel"].ID)),
				)
			} else {
				for _, directory := range f.manifest {
					if directory.Parent != "" {
						require.NoError(t, os.Remove(filepath.Join(captured, directory.Parent, directory.Name)))
					}
				}
				for _, directory := range f.manifest {
					if directory.Parent == "" {
						require.NoError(t, os.Remove(filepath.Join(captured, directory.Name)))
					}
				}
				require.NoError(t, os.Remove(captured))
			}
			require.NoError(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
			assert.Equal(t, "removed", f.readAnchor(t).Retirement.Phase)
			assert.NoDirExists(t, filepath.Join(f.cachePath, protectedBackingDirectory))
		})
	}
}

func TestProtectedCacheCleanupCaptureRefusesCallerNamespaceReplacement(t *testing.T) {
	// Rationale: The caller can replace the source name despite retained root authority locks.
	f := newProtectedCacheCleanupFixture(t)
	original := filepath.Join(f.cachePath, protectedBackingDirectory)
	before := protectedCacheTestTree(t, original)
	retained := filepath.Join(f.root, "retained-original")
	deps := realProtectedCacheCaptureDeps()
	capture := deps.captureStore
	var replacement unix.Stat_t
	deps.captureStore = func(ctx context.Context, cache, quarantine int) error {
		require.NoError(t, os.Rename(original, retained))
		require.NoError(t, os.Mkdir(original, 0700))
		require.NoError(t, unix.Lstat(original, &replacement))
		return capture(ctx, cache, quarantine)
	}
	require.Error(t, retireProtectedCacheWithCapture(t.Context(), f.cache, f.anchor, f.owner, deps))
	assert.Equal(t, "capturing", f.readAnchor(t).Retirement.Phase)
	if diff := cmp.Diff(before, protectedCacheTestTree(t, retained)); diff != "" {
		t.Errorf("unadmitted namespace race changed original store (-want +got):\n%s", diff)
	}
	captured := filepath.Join(f.cachePath, ProtectedCacheQuarantineDirectory, protectedCacheCapturedStore)
	var actual unix.Stat_t
	require.NoError(t, unix.Lstat(captured, &actual))
	assert.Equal(t, replacement.Ino, actual.Ino, "the captured replacement is retained, never deleted")
	after := protectedCacheTestTree(t, f.root)
	require.Error(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	if diff := cmp.Diff(after, protectedCacheTestTree(t, f.root)); diff != "" {
		t.Errorf("replacement retry changed retained evidence (-want +got):\n%s", diff)
	}
}

func TestProtectedCacheCleanupAbsentStoreMustResyncParentBeforeCompletion(t *testing.T) {
	// Rationale: Interrupted unlink plus a failed parent sync must not become durable completion.
	f := newProtectedCacheCleanupFixture(t)
	deps := realProtectedCacheCaptureDeps()
	syncParent := deps.syncCacheParent
	failures := 0
	deps.syncCacheParent = func(ctx context.Context, cache int) error {
		if f.readAnchor(t).Retirement.Phase == "removing" {
			failures++
			return unix.EIO
		}
		return syncParent(ctx, cache)
	}
	for attempt := 0; attempt < 2; attempt++ {
		require.ErrorIs(t, retireProtectedCacheWithCapture(t.Context(), f.cache, f.anchor, f.owner, deps), unix.EIO)
		assert.Equal(t, "removing", f.readAnchor(t).Retirement.Phase)
		assert.NoDirExists(
			t,
			filepath.Join(f.cachePath, ProtectedCacheQuarantineDirectory, protectedCacheCapturedStore),
		)
	}
	assert.Equal(t, 2, failures, "already-absent recovery must retry the cache parent sync")
	require.NoError(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	assert.Equal(t, "removed", f.readAnchor(t).Retirement.Phase)
	require.NoError(t, unix.Rmdir(filepath.Join(f.cachePath, ProtectedCacheQuarantineDirectory)))
	before := protectedCacheTestTree(t, f.root)
	require.NoError(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
		t.Errorf("completed ordinary suffix retry rewrote authority (-want +got):\n%s", diff)
	}
	require.NoError(t, os.Mkdir(filepath.Join(f.cachePath, protectedBackingDirectory), 0700))
	before = protectedCacheTestTree(t, f.root)
	require.Error(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
		t.Errorf("completed store reappearance was mutated (-want +got):\n%s", diff)
	}
}

func TestProtectedCacheCleanupCancellationPreservesParents(t *testing.T) {
	f := newProtectedCacheCleanupFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := protectedCacheTestTree(t, f.root)
	err := retireProtectedCache(ctx, f.cache, f.anchor, f.owner)
	require.ErrorIs(t, err, context.Canceled)
	if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
		t.Errorf("cancelled cleanup changed state (-want +got):\n%s", diff)
	}
}

func TestProtectedCacheCleanupPublicationSyncRetryPrecedesRemoval(t *testing.T) {
	// Rationale: A visible removing record is not durable authority after publication sync failed.
	f := newProtectedCacheCleanupFixture(t)
	original := filepath.Join(f.cachePath, protectedBackingDirectory)
	child := filepath.Join(original, protectedCacheQuarantineChild)
	require.NoError(t, os.Mkdir(child, 0700))
	var stat unix.Stat_t
	require.NoError(t, unix.Lstat(child, &stat))
	quarantine := filepath.Join(f.cachePath, ProtectedCacheQuarantineDirectory)
	require.NoError(t, os.Rename(child, quarantine))
	captured := filepath.Join(quarantine, protectedCacheCapturedStore)
	require.NoError(t, os.Rename(original, captured))
	intent := f.record
	intent.Retirement = &protectedCacheRetirement{Phase: "capturing", Directories: f.manifest,
		Quarantine: &protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size}}
	require.NoError(t, writeProtectedRecord(t.Context(), f.anchor, "store.json", intent))
	intent.Retirement.Phase = "removing"
	// Publish a real fsynced canonical file with rename, deliberately WITHOUT syncing the
	// anchor afterward: this is the precise writer boundary before its failing directory sync.
	require.NoError(t, writeProtectedRecord(t.Context(), f.anchor, "retry-publication.json", intent))
	require.NoError(t, unix.Renameat(f.anchor, "retry-publication.json", f.anchor, "store.json"))
	deps := realProtectedCacheCaptureDeps()
	syncAttempts := 0
	deps.syncRetirementIntent = func(context.Context, int) error {
		syncAttempts++
		return unix.EIO
	}
	before := protectedCacheTestTree(t, f.root)
	require.ErrorIs(t, retireProtectedCacheWithCapture(t.Context(), f.cache, f.anchor, f.owner, deps), unix.EIO)
	assert.Equal(t, 1, syncAttempts, "retry must synchronize the visible publication before removal")
	if diff := cmp.Diff(before, protectedCacheTestTree(t, f.root)); diff != "" {
		t.Errorf("failed intent barrier allowed a cleanup effect (-want +got):\n%s", diff)
	}
	assert.DirExists(t, captured, "captured store survives an unsuccessful intent barrier")
	require.NoError(t, retireProtectedCache(t.Context(), f.cache, f.anchor, f.owner))
	assert.NoDirExists(t, captured)
	assert.Equal(t, "removed", f.readAnchor(t).Retirement.Phase)
}
