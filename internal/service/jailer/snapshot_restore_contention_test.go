package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type snapshotRestoreBackingFixture struct {
	cache                            *managedCacheLease
	prepared                         *preparedSnapshotRestore
	store                            *protectedSnapshotStoreLease
	anchorPath, ownerPath, cachePath string
	sources                          []int
}

// Done observes entry into the context-aware wait while keeping the real context contract.
type restoreWaitObservedContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *restoreWaitObservedContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

// The existing restore preparation entrypoint uses its actual fixed root authority,
// with task-unique logical IDs and a disposable cache; no production seam is replaced.
func newSnapshotRestoreBackingFixture(t *testing.T) *snapshotRestoreBackingFixture {
	t.Helper()
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("restore backing effects require the disposable root-test VM")
	}
	f := &snapshotRestoreBackingFixture{ownerPath: "/var/lib/mvmctl/protected-backing/1000"}
	root := t.TempDir()
	fd := -1
	var identity protectedBackingIdentity
	// Retired keys are permanent. Select a physically fresh test cache without
	// deleting or rebinding authority if the filesystem reuses an earlier inode.
	for attempt := 0; attempt < 64; attempt++ {
		f.cachePath = filepath.Join(root, fmt.Sprintf("cache-%d", attempt))
		require.NoError(t, os.Mkdir(f.cachePath, 0700))
		candidate := openProtectedBranchTestFD(t, f.cachePath)
		var err error
		identity, err = protectedIdentity(candidate)
		require.NoError(t, err)
		key := fmt.Sprintf("%d-%d-%d", unix.Major(identity.Device), unix.Minor(identity.Device), identity.Inode)
		f.anchorPath = filepath.Join(f.ownerPath, key)
		_, err = os.Lstat(f.anchorPath)
		if errors.Is(err, os.ErrNotExist) {
			fd = candidate
			break
		}
		require.NoError(t, err)
		require.NoError(t, unix.Close(candidate))
	}
	require.NotEqual(t, -1, fd, "fixture needs a fresh physical cache identity")
	require.NoError(t, os.Chown(f.cachePath, 1000, 1000))
	locator, err := parseManagedCacheLocator(f.cachePath)
	require.NoError(t, err)
	f.cache = &managedCacheLease{cacheFD: fd, ownerUID: 1000, ownerGID: 1000, locator: locator,
		identity: managedCacheIdentity{deviceMajor: unix.Major(identity.Device),
			deviceMinor: unix.Minor(identity.Device), inode: identity.Inode},
		deps: realManagedCacheDeps(), configDeps: realManagedFirecrackerConfigDeps(), retained: []int{fd}}
	f.store, err = openProtectedSnapshotStore(t.Context(), f.cache, true)
	require.NoError(t, err)
	digest := sha256.Sum256([]byte(f.cachePath))
	kernelID := hex.EncodeToString(digest[:])
	vmID := kernelID[:32]
	f.prepared = &preparedSnapshotRestore{registered: true, ownerUID: 1000,
		cacheIdentity: f.cache.identity, targetVMID: vmID, artifacts: make(map[string]int),
		metadata: snapshotCaptureRecord{Source: vmLaunchDeviceAuthority{KernelID: kernelID}}}
	for _, artifact := range []struct {
		name string
		size uint64
	}{
		{"kernel", 6}, {"rootfs.img", managedVMRootfsMinBytes},
	} {
		path := filepath.Join(filepath.Dir(f.cachePath), artifact.name)
		require.NoError(t, os.WriteFile(path, []byte("marker"), 0600))
		require.NoError(t, os.Truncate(path, int64(artifact.size)))
		require.NoError(t, os.Chmod(path, 0400))
		source, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		require.NoError(t, err)
		f.sources = append(f.sources, source)
		f.prepared.artifacts[artifact.name] = source
		f.prepared.metadata.Artifacts = append(f.prepared.metadata.Artifacts,
			snapshotArtifact{Name: artifact.name, Bytes: artifact.size})
	}
	t.Cleanup(func() {
		for _, backing := range f.prepared.backings {
			require.NoError(t, backing.Remove(context.Background()))
		}
		require.NoError(t, f.prepared.releaseBackings(context.Background()))
		// Baseline failures may have published an earlier unreferenced object before
		// releasing its lease. Retire that exact fixture identity through its typed backing.
		for _, selection := range []struct {
			kind, id, artifact string
			bytes              uint64
		}{
			{protectedBackingKernel, kernelID, "kernel", 6},
			{protectedBackingRootfs, vmID, "rootfs.img", managedVMRootfsMinBytes},
		} {
			data, err := os.ReadFile(filepath.Join(f.anchorPath, selection.kind, selection.id, "record.json"))
			if errors.Is(err, os.ErrNotExist) {
				_, metadataErr := os.Lstat(filepath.Join(f.anchorPath, selection.kind, selection.id))
				if errors.Is(metadataErr, os.ErrNotExist) {
					continue
				}
				require.NoError(t, metadataErr)
				// After the no-copy assertions, finish only this fixture's admitted,
				// uninitialized identity so ordinary typed retirement can clean it.
				backing, err := restoreProtectedBacking(context.Background(), f.cache, selection.kind, selection.id,
					f.prepared.artifacts[selection.artifact], selection.bytes)
				require.NoError(t, err)
				require.NoError(t, backing.Remove(context.Background()))
				require.NoError(t, backing.Release(context.Background()))
				continue
			}
			require.NoError(t, err)
			var record protectedBackingRecord
			require.NoError(t, json.Unmarshal(data, &record))
			if record.Phase == "removed" {
				continue
			}
			require.Equal(t, "ready", record.Phase, "unknown fixture cleanup authority must remain retained")
			backing, err := protectBacking(context.Background(), f.cache, selection.kind, selection.id, selection.bytes, false)
			require.NoError(t, err)
			require.NoError(t, backing.Remove(context.Background()))
			require.NoError(t, backing.Release(context.Background()))
		}
		require.NoError(t, closeProtectedFDs(f.sources))
		require.NoError(t, f.store.Release(context.Background()))
		require.NoError(t, cleanProtectedCache(context.Background(), f.cache))
		require.NoError(t, f.cache.Release(context.Background()))
	})
	return f
}

func (f *snapshotRestoreBackingFixture) holdRootfsObject(t *testing.T) (int, int) {
	t.Helper()
	path := filepath.Join(f.anchorPath, protectedBackingRootfs, f.prepared.targetVMID)
	require.NoError(t, os.MkdirAll(path, 0700))
	metadata := openProtectedBranchTestFD(t, path)
	lock, err := acquireProtectedLock(t.Context(), metadata)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Close(metadata)) })
	return metadata, lock
}

func (f *snapshotRestoreBackingFixture) awaitPartialAdmission(t *testing.T) {
	t.Helper()
	logical := filepath.Join(f.ownerPath, "logical", protectedBackingRootfs, f.prepared.targetVMID, "logical.json")
	require.Eventually(t, func() bool {
		_, err := os.Lstat(logical)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "restore must reach the held later object")
}

func (f *snapshotRestoreBackingFixture) lockReleasedKernel(t *testing.T) int {
	t.Helper()
	path := filepath.Join(f.anchorPath, protectedBackingKernel, f.prepared.metadata.Source.KernelID)
	metadata := openProtectedBranchTestFD(t, path)
	t.Cleanup(func() { require.NoError(t, unix.Close(metadata)) })
	lock := -1
	require.Eventually(t, func() bool {
		var err error
		lock, err = acquireProtectedLock(t.Context(), metadata)
		if err != nil && !errors.Is(err, unix.EAGAIN) {
			t.Errorf("kernel lock probe: %v", err)
		}
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "partial kernel authority must release before waiting")
	return lock
}

func TestSnapshotRestoreBackingContentionReleasesPartialSetBeforeCopy(t *testing.T) {
	// Rationale: waiting for a later object must release earlier locks and leave all
	// backing bytes unpublished, then complete the original operation when contention ends.
	f := newSnapshotRestoreBackingFixture(t)
	_, held := f.holdRootfsObject(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.prepared.prepareBackings(ctx, f.cache); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	f.awaitPartialAdmission(t)
	kernelLock := f.lockReleasedKernel(t)
	assert.NoFileExists(t, filepath.Join(f.cachePath, protectedBackingDirectory, protectedBackingKernel,
		f.prepared.metadata.Source.KernelID, "disk"), "no copy may start with an incomplete lock set")
	require.NoError(t, unix.Close(kernelLock))
	require.NoError(t, unix.Close(held))
	require.NoError(t, <-done, "the original restore must succeed after real contention clears")
	for _, backing := range []*protectedBackingLease{f.prepared.kernel, f.prepared.rootfs} {
		marker := make([]byte, 6)
		_, err := unix.Pread(backing.fd, marker, 0)
		require.NoError(t, err)
		assert.Equal(t, "marker", string(marker))
		assert.Empty(t, backing.record.References, "preparation must not publish grants")
	}
	changed := []byte("clone!")
	n, err := unix.Pwrite(f.prepared.rootfs.fd, changed, 0)
	require.NoError(t, err)
	require.Equal(t, len(changed), n)
	restored := make([]byte, len(changed))
	n, err = unix.Pread(f.prepared.rootfs.fd, restored, 0)
	require.NoError(t, err)
	require.Equal(t, len(restored), n)
	assert.Equal(t, "clone!", string(restored), "restored rootfs must remain writable")
	original := make([]byte, 6)
	n, err = unix.Pread(f.prepared.artifacts["rootfs.img"], original, 0)
	require.NoError(t, err)
	require.Equal(t, len(original), n)
	assert.Equal(t, "marker", string(original), "restored writes must not change immutable capture bytes")
}

func TestSnapshotRestoreBackingContentionCancellationReleasesAuthority(t *testing.T) {
	// Rationale: cancelling a contention wait must leave no partial backing or lock ownership.
	f := newSnapshotRestoreBackingFixture(t)
	_, held := f.holdRootfsObject(t)
	defer func() { require.NoError(t, unix.Close(held)) }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.prepared.prepareBackings(ctx, f.cache); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	f.awaitPartialAdmission(t)
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
	kernelLock := f.lockReleasedKernel(t)
	require.NoError(t, unix.Close(kernelLock))
	assert.NoFileExists(t, filepath.Join(f.cachePath, protectedBackingDirectory, protectedBackingKernel,
		f.prepared.metadata.Source.KernelID, "disk"))
}

func TestSnapshotRestoreBackingTerminalAdmissionErrorDoesNotCopy(t *testing.T) {
	// Rationale: unsafe metadata is terminal rather than a reason to wait or publish an earlier object.
	f := newSnapshotRestoreBackingFixture(t)
	metadata, held := f.holdRootfsObject(t)
	require.NoError(t, unix.Close(held))
	require.NoError(t, unix.Fchmod(metadata, 0755))
	t.Cleanup(func() { require.NoError(t, unix.Fchmod(metadata, 0700)) })
	err := f.prepared.prepareBackings(t.Context(), f.cache)
	assert.ErrorContains(t, err, "unsafe ownership or mode")
	assert.NoFileExists(t, filepath.Join(f.cachePath, protectedBackingDirectory, protectedBackingKernel,
		f.prepared.metadata.Source.KernelID, "disk"))
}

func TestSnapshotRestoreInitialContentionBeforeReference(t *testing.T) {
	// Rationale: initial store/entry contention waits before reading capture metadata or
	// publishing a reference; cancellation and later terminal errors must remain distinct.
	for _, stage := range []string{"anchor", "entry"} {
		for _, cancelWait := range []bool{true, false} {
			name := stage + "_release"
			if cancelWait {
				name = stage + "_cancel"
			}
			t.Run(name, func(t *testing.T) {
				f := newSnapshotRestoreBackingFixture(t)
				digest := sha256.Sum256([]byte(t.Name() + f.cachePath))
				id := hex.EncodeToString(digest[:])
				entry, err := lockSnapshotStoreEntry(t.Context(), f.store, id, true)
				require.NoError(t, err)
				t.Cleanup(func() {
					if entry.directoryFD < 0 {
						var err error
						entry, err = lockSnapshotStoreEntry(context.Background(), f.store, id, false)
						require.NoError(t, err)
					}
					require.NoError(t, entry.remove(context.Background()))
					require.NoError(t, entry.Release(context.Background()))
				})
				held := -1
				if stage == "anchor" {
					require.NoError(t, entry.Release(t.Context()))
					anchor := openProtectedBranchTestFD(t, f.anchorPath)
					t.Cleanup(func() { require.NoError(t, unix.Close(anchor)) })
					held, err = acquireProtectedLock(t.Context(), anchor)
					require.NoError(t, err)
					t.Cleanup(func() {
						if held >= 0 {
							require.NoError(t, unix.Close(held))
						}
					})
				}
				base, cancel := context.WithCancel(t.Context())
				ctx := &restoreWaitObservedContext{Context: base, waiting: make(chan struct{})}
				done := make(chan error, 1)
				go func() {
					_, err := prepareSnapshotRestore(ctx, f.cache,
						VMSnapshotRestoreRequest{SnapshotID: id, VMID: f.prepared.targetVMID})
					done <- err
					close(done)
				}()
				t.Cleanup(func() { cancel(); <-done })
				select {
				case <-ctx.waiting:
				case err := <-done:
					t.Fatalf("restore returned instead of waiting on real %s contention: %v", stage, err)
				case <-time.After(5 * time.Second):
					t.Fatal("restore did not enter cancellable authority wait")
				}
				if cancelWait {
					cancel()
				}
				if held >= 0 {
					require.NoError(t, unix.Close(held))
					held = -1
				}
				require.NoError(t, entry.Release(t.Context()))
				err = <-done
				if cancelWait {
					assert.ErrorIs(t, err, context.Canceled)
				} else {
					assert.ErrorIs(t, err, unix.ENOENT, "missing capture metadata is terminal after lock admission")
				}
				entry, err = lockSnapshotStoreEntry(t.Context(), f.store, id, false)
				require.NoError(t, err)
				refs := os.NewFile(uintptr(openProtectedBranchTestFD(t,
					filepath.Join(f.cachePath, protectedBackingDirectory, "snapshots", id, "refs"))), "restore-reference-check")
				names, err := refs.Readdirnames(1)
				assert.ErrorIs(t, err, io.EOF)
				assert.Empty(t, names)
				require.NoError(t, refs.Close())
			})
		}
	}
}
