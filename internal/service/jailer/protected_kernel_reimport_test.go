package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type protectedKernelFixture struct {
	lease   *protectedBackingLease
	root    string
	source  int
	content []byte
}

// newProtectedKernelFixture exercises real root metadata, ACL, copy and publication effects
// entirely beneath a test directory in the disposable root-test runner.
func newProtectedKernelFixture(t *testing.T) *protectedKernelFixture {
	t.Helper()
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("protected kernel effects require the disposable root-test VM")
	}
	root := t.TempDir()
	for _, name := range []string{"object", "metadata"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, name), 0700))
	}
	content := []byte("original immutable kernel bytes")
	require.NoError(t, os.WriteFile(filepath.Join(root, "object", "disk"), content, 0600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "source"), content, 0600))
	var retained []int
	for _, path := range []string{filepath.Join(root, "object"), filepath.Join(root, "metadata")} {
		fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		require.NoError(t, err)
		retained = append(retained, fd)
	}
	disk, err := unix.Open(filepath.Join(root, "object", "disk"), unix.O_RDWR|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	retained = append(retained, disk)
	source, err := unix.Open(filepath.Join(root, "source"), unix.O_RDWR|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	retained = append(retained, source)
	lock, err := acquireProtectedLock(t.Context(), retained[1])
	require.NoError(t, err)
	retained = append(retained, lock)
	identity, err := protectedIdentity(disk)
	require.NoError(t, err)
	directory, err := protectedIdentity(retained[0])
	require.NoError(t, err)
	lease := &protectedBackingLease{
		fd: disk, directoryFD: retained[0], metadataFD: retained[1], lockFD: lock, retained: retained,
		record: protectedBackingRecord{Version: 1, Kind: protectedBackingKernel, ID: strings.Repeat("a", 64),
			Owner: 1000, Directory: directory, Backing: identity, Phase: "ready", Generation: 1,
			References: []protectedBackingReference{}},
	}
	require.NoError(t, setProtectedACL(t.Context(), disk, nil))
	require.NoError(t, writeProtectedRecord(t.Context(), lease.metadataFD, "record.json", lease.record))
	t.Cleanup(func() { require.NoError(t, lease.Release(context.Background())) })
	return &protectedKernelFixture{lease: lease, root: root, source: source, content: content}
}

func readKernelFixtureRecord(t *testing.T, fixture *protectedKernelFixture) protectedBackingRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.root, "metadata", "record.json"))
	require.NoError(t, err)
	var record protectedBackingRecord
	require.NoError(t, json.Unmarshal(data, &record))
	return record
}

func TestProtectedKernelRetirementAndIdenticalReimport(t *testing.T) {
	fixture := newProtectedKernelFixture(t)
	lease := fixture.lease
	require.NoError(t, lease.Remove(t.Context()))
	retired := readKernelFixtureRecord(t, fixture)
	digest := sha256.Sum256(fixture.content)
	assert.Equal(t, hex.EncodeToString(digest[:]), retired.KernelDigest)
	assert.Equal(t, "removed", retired.Phase)
	assert.Equal(t, uint64(1), retired.Generation)
	assert.NoFileExists(t, filepath.Join(fixture.root, "object", "disk"))
	require.NoError(t, lease.beginKernelReimport(t.Context()))
	require.NoError(t, lease.copyAndPublish(t.Context(), fixture.source, uint64(len(fixture.content))))
	reimported := readKernelFixtureRecord(t, fixture)
	assert.Equal(t, "ready", reimported.Phase)
	assert.Equal(t, uint64(2), reimported.Generation)
	assert.Equal(t, retired.KernelDigest, reimported.KernelDigest)
	got, err := os.ReadFile(filepath.Join(fixture.root, "object", "disk"))
	require.NoError(t, err)
	if diff := cmp.Diff(fixture.content, got); diff != "" {
		t.Errorf("reimported kernel bytes (-want +got):\n%s", diff)
	}
}

func TestProtectedKernelMismatchedReimportRemainsUnpublished(t *testing.T) {
	fixture := newProtectedKernelFixture(t)
	lease := fixture.lease
	require.NoError(t, lease.Remove(t.Context()))
	retired := readKernelFixtureRecord(t, fixture)
	require.NoError(t, lease.beginKernelReimport(t.Context()))
	changed := []byte("changed bytes under same selector")
	require.NoError(t, unix.Ftruncate(fixture.source, 0))
	n, err := unix.Pwrite(fixture.source, changed, 0)
	require.NoError(t, err)
	require.Equal(t, len(changed), n)
	err = lease.copyAndPublish(t.Context(), fixture.source, uint64(len(changed)))
	require.Error(t, err)
	assert.ErrorContains(t, err, "bytes differ")
	assert.NoFileExists(t, filepath.Join(fixture.root, "object", "disk"))
	record := readKernelFixtureRecord(t, fixture)
	assert.Equal(t, "kernel-reimport-copying", record.Phase)
	assert.Equal(t, retired.Generation, record.Generation)
	assert.Equal(t, retired.KernelDigest, record.KernelDigest)
}

func TestProtectedKernelRetirementRecoversUnlinkPhases(t *testing.T) {
	for _, alreadyUnlinked := range []bool{false, true} {
		name := "before unlink"
		if alreadyUnlinked {
			name = "after unlink"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newProtectedKernelFixture(t)
			lease := fixture.lease
			digest, err := hashProtectedKernel(t.Context(), lease.fd, lease.record.Backing)
			require.NoError(t, err)
			lease.record.KernelDigest, lease.record.Phase = digest, "removing"
			require.NoError(t, writeProtectedRecord(t.Context(), lease.metadataFD, "record.json", lease.record))
			if alreadyUnlinked {
				require.NoError(t, unix.Unlinkat(lease.directoryFD, "disk", 0))
			}
			lease.record = readKernelFixtureRecord(t, fixture)
			require.NoError(t, lease.finishRemoval(t.Context()))
			retired := readKernelFixtureRecord(t, fixture)
			assert.Equal(t, "removed", retired.Phase)
			assert.Equal(t, digest, retired.KernelDigest)
			assert.Equal(t, uint64(1), retired.Generation)
			assert.NoFileExists(t, filepath.Join(fixture.root, "object", "disk"))
		})
	}
}

func TestProtectedKernelDigestlessRetirementFailsClosed(t *testing.T) {
	fixture := newProtectedKernelFixture(t)
	lease := fixture.lease
	lease.record.Phase = "removing"
	require.Error(t, lease.finishRemoval(t.Context()))
	got, err := os.ReadFile(filepath.Join(fixture.root, "object", "disk"))
	require.NoError(t, err)
	if diff := cmp.Diff(fixture.content, got); diff != "" {
		t.Errorf("unretired bytes (-want +got):\n%s", diff)
	}
	lease.record.Phase = "removed"
	require.Error(t, lease.beginKernelReimport(t.Context()))
}

func TestProtectedKernelReimportRecoversPinnedCandidatePhases(t *testing.T) {
	// Rationale: A receiver restart after copy or rename must rehash the retained candidate
	// and commit exactly one generation without accepting a replacement inode.
	for _, phase := range []string{"copying", "copied", "renamed"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newProtectedKernelFixture(t)
			lease := fixture.lease
			require.NoError(t, lease.Remove(t.Context()))
			require.NoError(t, lease.beginKernelReimport(t.Context()))
			pending, err := openProtectedFile(
				t.Context(),
				lease.directoryFD,
				"pending",
				unix.O_RDWR|unix.O_CREAT|unix.O_EXCL,
			)
			require.NoError(t, err)
			require.NoError(t, setProtectedACL(t.Context(), pending, nil))
			require.NoError(t, copyProtectedBytes(t.Context(), fixture.source, pending, int64(len(fixture.content))))
			lease.record.Backing, err = protectedIdentity(pending)
			require.NoError(t, err)
			require.NoError(t, unix.Close(pending))
			if phase != "copying" {
				lease.record.Phase = "kernel-reimport-copied"
			}
			require.NoError(t, writeProtectedRecord(t.Context(), lease.metadataFD, "record.json", lease.record))
			candidate := lease.record.Backing
			if phase == "renamed" {
				require.NoError(
					t,
					unix.Renameat2(lease.directoryFD, "pending", lease.directoryFD, "disk", unix.RENAME_NOREPLACE),
				)
			}
			lease.record = readKernelFixtureRecord(t, fixture) // Restart from durable authority only.
			if phase == "copying" {
				err = lease.copyAndPublish(t.Context(), fixture.source, uint64(len(fixture.content)))
			} else {
				err = lease.publishImport(t.Context())
			}
			require.NoError(t, err)
			record := readKernelFixtureRecord(t, fixture)
			assert.Equal(t, uint64(2), record.Generation)
			assert.Equal(t, "ready", record.Phase)
			assert.Equal(t, candidate.Inode, record.Backing.Inode)
			got, err := os.ReadFile(filepath.Join(fixture.root, "object", "disk"))
			require.NoError(t, err)
			if diff := cmp.Diff(fixture.content, got); diff != "" {
				t.Errorf("recovered bytes (-want +got):\n%s", diff)
			}
		})
	}
}

func TestProtectedKernelRetirementRefusesVMGrants(t *testing.T) {
	fixture := newProtectedKernelFixture(t)
	fixture.lease.record.References = []protectedBackingReference{{VMID: strings.Repeat("b", 32), ExecutionUID: 2000}}
	require.Error(t, fixture.lease.Remove(t.Context()))
	got, err := os.ReadFile(filepath.Join(fixture.root, "object", "disk"))
	require.NoError(t, err)
	if diff := cmp.Diff(fixture.content, got); diff != "" {
		t.Errorf("referenced kernel bytes (-want +got):\n%s", diff)
	}
	assert.Equal(t, "ready", readKernelFixtureRecord(t, fixture).Phase)
}

func TestProtectedKernelRetirementRefusesIncompleteInitialImport(t *testing.T) {
	for _, phase := range []string{"copying", "copied"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newProtectedKernelFixture(t)
			fixture.lease.record.Phase = phase
			require.Error(t, fixture.lease.Remove(t.Context()))
			got, err := os.ReadFile(filepath.Join(fixture.root, "object", "disk"))
			require.NoError(t, err)
			if diff := cmp.Diff(fixture.content, got); diff != "" {
				t.Errorf("incomplete import bytes (-want +got):\n%s", diff)
			}
			assert.Empty(t, fixture.lease.record.KernelDigest)
		})
	}
}

func TestProtectedKernelReimportRefusesReplacedCandidate(t *testing.T) {
	fixture := newProtectedKernelFixture(t)
	lease := fixture.lease
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.beginKernelReimport(t.Context()))
	path := filepath.Join(fixture.root, "object", "pending")
	require.NoError(t, os.WriteFile(path, fixture.content, 0600))
	pending, err := openProtectedFile(t.Context(), lease.directoryFD, "pending", unix.O_RDWR)
	require.NoError(t, err)
	require.NoError(t, setProtectedACL(t.Context(), pending, nil))
	lease.record.Backing, err = protectedIdentity(pending)
	require.NoError(t, err)
	lease.record.Phase = "kernel-reimport-copied"
	require.NoError(t, writeProtectedRecord(t.Context(), lease.metadataFD, "record.json", lease.record))
	// Retain the old descriptor so the filesystem cannot reuse its inode for the replacement.
	defer func() { require.NoError(t, unix.Close(pending)) }()
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.WriteFile(path, fixture.content, 0600))
	require.Error(t, lease.publishImport(t.Context()))
	assert.NoFileExists(t, filepath.Join(fixture.root, "object", "disk"))
	assert.Equal(t, uint64(1), readKernelFixtureRecord(t, fixture).Generation)
}

func TestProtectedKernelReimportMissingRecordedCandidateFailsClosed(t *testing.T) {
	// Rationale: Durable authority for a candidate cannot authorize creating a different inode
	// after a crash or external loss, even when the import source still contains identical bytes.
	fixture := newProtectedKernelFixture(t)
	lease := fixture.lease
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.beginKernelReimport(t.Context()))
	pending, err := openProtectedFile(t.Context(), lease.directoryFD, "pending", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	require.NoError(t, err)
	defer func() { require.NoError(t, unix.Close(pending)) }()
	require.NoError(t, unix.Fsync(pending))
	require.NoError(t, unix.Fsync(lease.directoryFD))
	lease.record.Backing, err = protectedIdentity(pending)
	require.NoError(t, err)
	lease.record.Source, err = protectedIdentity(fixture.source)
	require.NoError(t, err)
	require.NoError(t, writeProtectedRecord(t.Context(), lease.metadataFD, "record.json", lease.record))
	wanted := readKernelFixtureRecord(t, fixture)
	require.NoError(t, unix.Unlinkat(lease.directoryFD, "pending", 0))
	require.NoError(t, unix.Fsync(lease.directoryFD))
	lease.record = readKernelFixtureRecord(t, fixture) // Recovery starts from retained durable identity.
	err = lease.copyAndPublish(t.Context(), fixture.source, uint64(len(fixture.content)))
	require.Error(t, err)
	assert.ErrorIs(t, err, unix.ENOENT)
	assert.NoFileExists(t, filepath.Join(fixture.root, "object", "pending"))
	assert.NoFileExists(t, filepath.Join(fixture.root, "object", "disk"))
	if diff := cmp.Diff(wanted, readKernelFixtureRecord(t, fixture)); diff != "" {
		t.Errorf("retained candidate authority (-want +got):\n%s", diff)
	}
}

func TestProtectedKernelReimportRehashesBeforePublish(t *testing.T) {
	fixture := newProtectedKernelFixture(t)
	lease := fixture.lease
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.beginKernelReimport(t.Context()))
	pending, err := openProtectedFile(t.Context(), lease.directoryFD, "pending", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	require.NoError(t, err)
	require.NoError(t, setProtectedACL(t.Context(), pending, nil))
	require.NoError(t, copyProtectedBytes(t.Context(), fixture.source, pending, int64(len(fixture.content))))
	lease.record.Backing, err = protectedIdentity(pending)
	require.NoError(t, err)
	lease.record.Phase = "kernel-reimport-copied"
	require.NoError(t, writeProtectedRecord(t.Context(), lease.metadataFD, "record.json", lease.record))
	_, err = unix.Pwrite(pending, []byte("altered!"), 0)
	require.NoError(t, err)
	require.NoError(t, unix.Close(pending))
	require.Error(t, lease.publishImport(t.Context()))
	assert.NoFileExists(t, filepath.Join(fixture.root, "object", "disk"))
	assert.Equal(t, uint64(1), readKernelFixtureRecord(t, fixture).Generation)
}

func TestProtectedKernelGenerationRefusesStaleBinding(t *testing.T) {
	// Rationale: Inode reuse alone must never revive a previous generation's authority.
	identity := managedVMConfigStat{deviceMajor: 8, inode: 99, size: 4096}
	lease := &protectedBackingLease{identity: identity, record: protectedBackingRecord{Generation: 2}}
	binding := protectedBackingBinding{identity: identity, bytes: identity.size, generation: 1}
	require.Error(t, lease.requireBinding(binding))
	binding.generation = 2
	assert.NoError(t, lease.requireBinding(binding))
}

func TestProtectedKernelRootSnapshotReferences(t *testing.T) {
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("root snapshot metadata requires the disposable root-test VM")
	}
	for _, scenario := range []string{"referenced", "different kernel", "interrupted capture", "removed snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.Chmod(root, 0700))
			fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			require.NoError(t, err)
			store := &protectedSnapshotStoreLease{fd: fd, retained: []int{fd}}
			t.Cleanup(func() { require.NoError(t, store.Release(context.Background())) })
			snapshotID := strings.Repeat("f", 64)
			entry, err := lockSnapshotStoreEntry(t.Context(), store, snapshotID, true)
			require.NoError(t, err)
			source := testManagedLaunchDeviceAuthority()
			source.Release.Version = infra.QualifiedVMLaunchFirecrackerVersion
			source.Config.MachineConfig.MemSizeMiB = 128
			for _, binding := range []*protectedBackingBinding{&source.Rootfs, &source.KernelBacking} {
				binding.cacheIdentity.inode = 11
				binding.identity = managedVMConfigStat{
					inode: 12, mode: unix.S_IFREG | 0400, nlink: 1, size: binding.bytes,
				}
			}
			record := snapshotCaptureRecord{Schema: 1, SnapshotID: snapshotID, OwnerUID: source.OwnerUID,
				Mode: "disks", Source: source, Artifacts: []snapshotArtifact{
					{Name: "rootfs.img", Bytes: source.Rootfs.bytes, Inode: 13, SHA256: strings.Repeat("1", 64)},
					{Name: "kernel", Bytes: source.KernelBacking.bytes, Inode: 14, SHA256: strings.Repeat("2", 64)},
				}}
			if scenario != "interrupted capture" {
				require.NoError(t, entry.publish(t.Context(), record))
			}
			require.NoError(t, entry.Release(t.Context()))
			if scenario == "removed snapshot" {
				entry, err = lockSnapshotStoreEntry(t.Context(), store, snapshotID, false)
				require.NoError(t, err)
				require.NoError(t, entry.remove(t.Context()))
				require.NoError(t, entry.Release(t.Context()))
			}
			selected := source.KernelID
			if scenario == "different kernel" {
				selected = strings.Repeat("b", 64)
			}
			err = requireKernelSnapshotStoreReferencesAbsent(t.Context(), store, source.OwnerUID, selected)
			if scenario == "referenced" || scenario == "interrupted capture" {
				require.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestProtectedMutableTombstonesCannotAuthorizeKernelReimport(t *testing.T) {
	for _, kind := range []string{protectedBackingVolume, protectedBackingRootfs, protectedBackingCloudInit} {
		t.Run(kind, func(t *testing.T) {
			lease := &protectedBackingLease{record: protectedBackingRecord{
				Kind: kind, Phase: "removed", KernelDigest: strings.Repeat("a", 64), Generation: 1,
			}}
			require.Error(t, lease.beginKernelReimport(t.Context()))
			assert.Equal(t, "removed", lease.record.Phase)
		})
	}
}
