package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

func TestSnapshotRestorePreflightContention(t *testing.T) {
	// Rationale: public restore reads root metadata before its launch transaction.
	// That preflight must wait without publishing references or changing capture bytes;
	// ordinary inspection and removal must retain their immediate busy refusals.
	for _, stage := range []string{"store", "entry"} {
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
				source := testManagedLaunchDeviceAuthority()
				makeNetworklessLaunchDeviceAuthority(&source)
				source.VMID, source.Rootfs.logicalID = f.prepared.targetVMID, f.prepared.targetVMID
				source.KernelID = f.prepared.metadata.Source.KernelID
				source.KernelBacking.logicalID, source.KernelBacking.bytes = source.KernelID, 6
				source.Rootfs.cacheIdentity, source.KernelBacking.cacheIdentity = f.cache.identity, f.cache.identity
				source.Rootfs.identity, err = stableManagedVMConfigStat(
					t.Context(), f.cache.configDeps, f.prepared.artifacts["rootfs.img"],
				)
				require.NoError(t, err)
				source.KernelBacking.identity, err = stableManagedVMConfigStat(
					t.Context(), f.cache.configDeps, f.prepared.artifacts["kernel"],
				)
				require.NoError(t, err)
				source.Release.Version = infra.QualifiedVMLaunchFirecrackerVersion
				source.Config.MachineConfig = managedFirecrackerMachineConfig{VCPUCount: 1, MemSizeMiB: 256}
				record := snapshotCaptureRecord{
					Schema:     1,
					SnapshotID: id,
					OwnerUID:   1000,
					Mode:       "disks",
					Source:     source,
				}
				for _, artifact := range f.prepared.metadata.Artifacts {
					copied, err := entry.copyArtifact(
						t.Context(),
						artifact.Name,
						f.prepared.artifacts[artifact.Name],
						artifact.Bytes,
					)
					require.NoError(t, err)
					record.Artifacts = append(record.Artifacts, copied)
				}
				require.NoError(t, entry.publish(t.Context(), record))
				capturePath := filepath.Join(
					f.cachePath,
					protectedBackingDirectory,
					"snapshots",
					id,
					snapshotRecordLeaf,
				)
				before, err := os.ReadFile(capturePath)
				require.NoError(t, err)
				held := -1
				if stage == "store" {
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
				server := NewVMSnapshotServer()
				request := VMSnapshotRequest{CacheRoot: f.cachePath, SnapshotID: id}
				_, err = server.Inspect(t.Context(), 1000, 1000, request)
				require.ErrorIs(t, err, unix.EAGAIN, "ordinary inspection must still refuse a held lock")
				_, err = server.Remove(t.Context(), 1000, 1000, request)
				require.ErrorIs(t, err, unix.EAGAIN, "ordinary removal must still refuse a held lock")
				base, cancel := context.WithCancel(t.Context())
				ctx := &restoreWaitObservedContext{Context: base, waiting: make(chan struct{})}
				type outcome struct {
					projection model.SnapshotCaptureResult
					err        error
				}
				done := make(chan outcome, 1)
				go func() {
					projection, err := server.InspectForRestore(ctx, 1000, 1000, request)
					done <- outcome{projection: projection, err: err}
					close(done)
				}()
				t.Cleanup(func() { cancel(); <-done })
				select {
				case <-ctx.waiting:
				case result := <-done:
					t.Fatalf("restore preflight returned instead of waiting on %s contention: %v", stage, result.err)
				case <-time.After(5 * time.Second):
					t.Fatal("restore preflight did not enter cancellable authority wait")
				}
				if cancelWait {
					cancel()
				}
				if held >= 0 {
					require.NoError(t, unix.Close(held))
					held = -1
				}
				require.NoError(t, entry.Release(t.Context()))
				result := <-done
				if cancelWait {
					assert.ErrorIs(t, result.err, context.Canceled)
				} else {
					require.NoError(t, result.err)
					want := model.SnapshotCaptureResult{SnapshotID: id, SourceVMID: source.VMID,
						Mode: "disks", VCPUCount: 1, MemSizeMiB: 256, RootfsBytes: managedVMRootfsMinBytes,
						NetworkPlacement: model.VMNetworkPlacementNetworkless,
						ReleaseVersion:   infra.QualifiedVMLaunchFirecrackerVersion, ReleaseArchitecture: "x86_64",
						FirecrackerSHA256: source.FirecrackerSHA256, JailerSHA256: source.JailerSHA256,
						KernelID: source.KernelID, BootArgs: source.BootArgs}
					if diff := cmp.Diff(want, result.projection); diff != "" {
						t.Errorf("restore preflight projection (-want +got):\n%s", diff)
					}
				}
				entry, err = lockSnapshotStoreEntry(t.Context(), f.store, id, false)
				require.NoError(t, err, "preflight must release its exact store/entry authority")
				refs, err := os.ReadDir(filepath.Join(filepath.Dir(capturePath), "refs"))
				require.NoError(t, err)
				assert.Empty(t, refs, "metadata preflight must publish no restored-VM reference")
				after, err := os.ReadFile(capturePath)
				require.NoError(t, err)
				if diff := cmp.Diff(before, after); diff != "" {
					t.Errorf("immutable capture metadata changed (-want +got):\n%s", diff)
				}
			})
		}
	}
}
