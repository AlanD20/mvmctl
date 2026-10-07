package jailer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

func TestVMRootfsExportCopySurvivesSourcePathReplacement(t *testing.T) {
	// Rationale: the descriptor copy owns its admitted inode; replacing its old
	// pathname must not redirect reads. QA separately owns the guest-marker journey.
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source")
	want := []byte{0, 1, 4, 0xff}
	require.NoError(t, os.WriteFile(sourcePath, want, 0600))
	source, err := os.Open(sourcePath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, source.Close()) })
	require.NoError(t, os.Rename(sourcePath, sourcePath+".protected"))
	require.NoError(t, os.WriteFile(sourcePath, []byte{10, 13}, 0600))
	target, err := os.Create(filepath.Join(directory, "copy"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, target.Close()) })
	require.NoError(t, copyVMRootfsExportBytes(t.Context(), int(source.Fd()), int(target.Fd()), uint64(len(want))))
	got, err := os.ReadFile(target.Name())
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("independent protected copy (-want +got):\n%s", diff)
	}
}

func TestVMRootfsExportCopyRefusesWrongExtentAndCancellationBeforeMutation(t *testing.T) {
	for name, test := range map[string]struct {
		length    uint64
		cancelled bool
	}{
		"short_extent": {length: 3}, "long_extent": {length: 5},
		"cancelled": {length: 4, cancelled: true},
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			sourcePath, targetPath := filepath.Join(directory, "source"), filepath.Join(directory, "target")
			require.NoError(t, os.WriteFile(sourcePath, []byte("disk"), 0600))
			want := []byte("target untouched")
			require.NoError(t, os.WriteFile(targetPath, want, 0600))
			source, err := os.Open(sourcePath)
			require.NoError(t, err)
			defer func() { require.NoError(t, source.Close()) }()
			target, err := os.OpenFile(targetPath, os.O_RDWR, 0600)
			require.NoError(t, err)
			defer func() { require.NoError(t, target.Close()) }()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.cancelled {
				cancel()
			}
			err = copyVMRootfsExportBytes(ctx, int(source.Fd()), int(target.Fd()), test.length)
			require.Error(t, err)
			if test.cancelled {
				assert.ErrorIs(t, err, context.Canceled)
			}
			got, readErr := os.ReadFile(targetPath)
			require.NoError(t, readErr)
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("refused copy mutated target (-want +got):\n%s", diff)
			}
		})
	}
}

// The real root namespace is confined by the existing instance-authority policy.
func newVMRootfsExportOutputFixture(t *testing.T) (*instanceUIDDirectories, *vmRootfsExportOutput, string) {
	t.Helper()
	dirs, _ := newVMRootfsExportJournalFixture(t)
	output, err := prepareVMRootfsExportOutput(t.Context(), dirs, rootfsExportTestProgress("Running", "prepared"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, output.close(context.Background())) })
	leaf := filepath.Join(
		dirs.policy.rootPath,
		"var/lib/mvmctl/vm-rootfs-export-data/1000",
		output.entry.imageID,
		"disk.raw",
	)
	return dirs, output, leaf
}

func exportFixtureServer(dirs *instanceUIDDirectories) *VMRootfsExportServer {
	return &VMRootfsExportServer{instances: newInstanceAuthorityWithPolicy(dirs.deps, dirs.policy, nil)}
}

func TestVMRootfsExportRootSentinelPrecedesNamedBytesAndSurvivesReceiverDeath(t *testing.T) {
	dirs, output, leaf := newVMRootfsExportOutputFixture(t)
	marker := filepath.Dir(leaf) + ".copy"
	got, err := os.ReadFile(marker)
	require.NoError(t, err)
	assert.Equal(t, output.entry.imageID, string(got))
	_, err = os.Lstat(leaf)
	assert.ErrorIs(t, err, os.ErrNotExist)
	var anonymous unix.Stat_t
	require.NoError(t, unix.Fstat(output.fd, &anonymous))
	assert.Zero(t, anonymous.Nlink)
	// Receiver death loses the candidate FD, not the durable release authority.
	require.NoError(t, output.close(t.Context()))
	result, err := exportFixtureServer(dirs).Release(t.Context(), 1000, 1000,
		VMRootfsExportReleaseRequest{ImageID: strings.Repeat("c", 64)})
	require.NoError(t, err)
	assert.True(t, result.Found)
	assert.True(t, result.Released)
	_, err = os.Lstat(filepath.Dir(leaf))
	assert.ErrorIs(t, err, os.ErrNotExist)
	got, err = os.ReadFile(marker)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("c", 64), string(got))
	// The sentinel keeps a supported tombstone retry reachable; it grants no adoption.
	result, err = exportFixtureServer(dirs).Release(t.Context(), 1000, 1000,
		VMRootfsExportReleaseRequest{ImageID: strings.Repeat("c", 64)})
	require.NoError(t, err)
	assert.True(t, result.Found)
	assert.True(t, result.Released)
}

func TestVMRootfsExportRootPublicationReleasePreservesReaderAndSourceIntent(t *testing.T) {
	dirs, output, leaf := newVMRootfsExportOutputFixture(t)
	journal, err := openVMRootfsExportJournal(t.Context(), dirs, testVMID, false)
	require.NoError(t, err)
	defer func() { require.NoError(t, journal.close(context.Background())) }()
	intent := rootfsExportTestProgress("Running", "resume_inflight")
	require.NoError(t, journal.write(t.Context(), intent))
	want := []byte(strings.Repeat("disk", 1024))
	require.NoError(t, writeSnapshotBytes(t.Context(), output.fd, want))
	result, err := output.publish(t.Context(), uint64(len(want)))
	require.NoError(t, err)
	var named unix.Stat_t
	require.NoError(t, unix.Lstat(leaf, &named))
	assert.Equal(t, uint64(named.Dev), result.Device)
	assert.Equal(t, named.Ino, result.Inode)
	assert.Equal(t, uint32(unix.S_IFREG|0440), named.Mode)
	assert.Zero(t, named.Uid)
	assert.Zero(t, named.Gid)
	reader, err := os.Open(leaf)
	require.NoError(t, err)
	defer func() { require.NoError(t, reader.Close()) }()
	// A live root ImageID lease is busy, never a second cleanup owner.
	_, err = exportFixtureServer(dirs).Release(t.Context(), 1000, 1000,
		VMRootfsExportReleaseRequest{ImageID: result.ImageID})
	require.Error(t, err)
	require.NoError(t, output.close(t.Context()))
	released, err := exportFixtureServer(dirs).Release(t.Context(), 1000, 1000,
		VMRootfsExportReleaseRequest{ImageID: result.ImageID})
	require.NoError(t, err)
	assert.True(t, released.SourceRecoveryPending)
	assert.Equal(t, testVMID, released.SourceVMID)
	_, err = os.Lstat(leaf)
	assert.ErrorIs(t, err, os.ErrNotExist)
	got := make([]byte, len(want))
	_, err = reader.ReadAt(got, 0)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("reader changed after release (-want +got):\n%s", diff)
	}
	remaining, err := journal.read(t.Context())
	require.NoError(t, err)
	if diff := cmp.Diff(
		intent,
		*remaining,
		cmp.AllowUnexported(protectedBackingBinding{}, managedCacheIdentity{}, managedVMConfigStat{}),
	); diff != "" {
		t.Errorf("release changed source recovery authority (-want +got):\n%s", diff)
	}
}

func TestVMRootfsExportRootDuplicateIDAndAncestorReplacementCannotRedirectCleanup(t *testing.T) {
	dirs, output, leaf := newVMRootfsExportOutputFixture(t)
	want := []byte(strings.Repeat("disk", 1024))
	require.NoError(t, writeSnapshotBytes(t.Context(), output.fd, want))
	_, err := output.publish(t.Context(), uint64(len(want)))
	require.NoError(t, err)
	require.NoError(t, output.close(t.Context()))
	other := rootfsExportTestProgress("Stopped", "prepared")
	other.VMID, other.Rootfs.logicalID = strings.Repeat("d", 32), strings.Repeat("d", 32)
	other.CacheRoot = "/home/operator/another-cache"
	_, err = prepareVMRootfsExportOutput(t.Context(), dirs, other)
	require.Error(t, err)
	got, err := os.ReadFile(leaf)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	uidDir := filepath.Dir(filepath.Dir(leaf))
	require.NoError(t, os.Rename(uidDir, uidDir+".original"))
	require.NoError(t, os.Mkdir(uidDir, 0700))
	_, err = exportFixtureServer(dirs).Release(t.Context(), 1000, 1000,
		VMRootfsExportReleaseRequest{ImageID: other.ImageID})
	require.Error(t, err)
	got, err = os.ReadFile(filepath.Join(uidDir+".original", other.ImageID, "disk.raw"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
	require.NoError(t, os.Remove(uidDir))
	require.NoError(t, os.Rename(uidDir+".original", uidDir))
	_, err = exportFixtureServer(dirs).Release(t.Context(), 1000, 1000,
		VMRootfsExportReleaseRequest{ImageID: other.ImageID})
	require.NoError(t, err)
}

func TestVMRootfsExportRootPublicationRefusesWrongExtentAndNeverOverwrites(t *testing.T) {
	for _, refusal := range []string{"wrong_extent", "existing_leaf"} {
		t.Run(refusal, func(t *testing.T) {
			_, output, leaf := newVMRootfsExportOutputFixture(t)
			payload := []byte(strings.Repeat("disk", 1024))
			require.NoError(t, writeSnapshotBytes(t.Context(), output.fd, payload))
			length := uint64(len(payload))
			if refusal == "wrong_extent" {
				length++
			} else {
				require.NoError(t, os.WriteFile(leaf, []byte("unowned"), 0600))
			}
			_, err := output.publish(t.Context(), length)
			require.Error(t, err)
			if refusal == "wrong_extent" {
				require.NoError(t, output.discard(t.Context()))
				_, err = os.Lstat(leaf)
				assert.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.Error(t, output.discard(t.Context()))
				got, err := os.ReadFile(leaf)
				require.NoError(t, err)
				assert.Equal(t, "unowned", string(got))
			}
		})
	}
}

func TestVMRootfsExportRootOperatorCanOnlyReadIndependentCopy(t *testing.T) {
	dirs, output, leaf := newVMRootfsExportOutputFixture(t)
	require.NoError(t, writeSnapshotBytes(t.Context(), output.fd, []byte(strings.Repeat("disk", 1024))))
	_, err := output.publish(t.Context(), 4096)
	require.NoError(t, err)
	// Run the actual kernel permission checks with no supplementary groups or
	// root capabilities. Cwd confines the test to its disposable authority tree.
	require.NoError(t, os.Chmod(dirs.policy.rootPath, 0711))
	relative, err := filepath.Rel(dirs.policy.rootPath, leaf)
	require.NoError(t, err)
	const check = `import errno, os, sys
leaf = sys.argv[1]
assert os.getuid() == 1000 and not os.getgroups()
with open(leaf, 'rb') as f:
    assert f.read() == b'disk' * 1024
def denied(operation):
    try:
        operation()
    except OSError as e:
        assert e.errno in (errno.EACCES, errno.EPERM), e
    else:
        raise AssertionError('operator mutation/traversal was allowed')
denied(lambda: os.open(leaf, os.O_WRONLY))
denied(lambda: os.unlink(leaf))
denied(lambda: os.rename(leaf, leaf + '.replacement'))
denied(lambda: os.open(os.path.dirname(leaf) + '/new', os.O_CREAT | os.O_WRONLY, 0o600))
denied(lambda: os.rename(os.path.dirname(leaf), os.path.dirname(leaf) + '.replacement'))
denied(lambda: os.rename('var/lib/mvmctl/vm-rootfs-export-data/1000', 'var/lib/mvmctl/other'))
denied(lambda: os.listdir('var/lib/mvmctl/vm-rootfs-exports'))
`
	_, err = system.DefaultRunner.Run(t.Context(), []string{"setpriv", "--reuid=1000", "--regid=1000",
		"--clear-groups", "--bounding-set=-all", "python3", "-c", check, relative},
		system.RunCmdOpts{Check: true, Capture: true, Cwd: dirs.policy.rootPath, Timeout: 10 * time.Second})
	require.NoError(t, err)
	_, err = system.DefaultRunner.Run(t.Context(), []string{
		"setpriv",
		"--reuid=1001",
		"--regid=1001",
		"--clear-groups",
		"--bounding-set=-all",
		"python3",
		"-c",
		"import os,sys;\ntry: open(sys.argv[1], 'rb')\nexcept PermissionError: pass\nelse: raise AssertionError('foreign caller read copy')",
		relative,
	},
		system.RunCmdOpts{Check: true, Capture: true, Cwd: dirs.policy.rootPath, Timeout: 10 * time.Second})
	require.NoError(t, err)
	require.NoError(t, output.discard(t.Context()))
}

func TestVMRootfsExportRootCanceledAndForeignReleaseCannotDeleteCopy(t *testing.T) {
	dirs, output, leaf := newVMRootfsExportOutputFixture(t)
	want := []byte(strings.Repeat("disk", 1024))
	require.NoError(t, writeSnapshotBytes(t.Context(), output.fd, want))
	result, err := output.publish(t.Context(), 4096)
	require.NoError(t, err)
	require.NoError(t, output.close(t.Context()))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = exportFixtureServer(dirs).Release(ctx, 1000, 1000, VMRootfsExportReleaseRequest{ImageID: result.ImageID})
	require.ErrorIs(t, err, context.Canceled)
	foreign, err := exportFixtureServer(
		dirs,
	).Release(t.Context(), 1001, 1001, VMRootfsExportReleaseRequest{ImageID: result.ImageID})
	require.NoError(t, err)
	assert.False(t, foreign.Found)
	got, err := os.ReadFile(leaf)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	_, err = exportFixtureServer(
		dirs,
	).Release(t.Context(), 1000, 1000, VMRootfsExportReleaseRequest{ImageID: result.ImageID})
	require.NoError(t, err)
}

func TestVMRootfsExportRootFilesystemFailurePublishesNoFallback(t *testing.T) {
	dirs, output, leaf := newVMRootfsExportOutputFixture(t)
	// A failed candidate filesystem operation has no caller-cache fallback and
	// leaves its root-only retirement authority reachable for a later receiver.
	require.NoError(t, unix.Close(output.fd))
	output.fd = -1
	_, err := output.publish(t.Context(), 4096)
	require.Error(t, err)
	_, err = os.Lstat(leaf)
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, output.close(t.Context()))
	result, err := exportFixtureServer(dirs).Release(t.Context(), 1000, 1000,
		VMRootfsExportReleaseRequest{ImageID: strings.Repeat("c", 64)})
	require.NoError(t, err)
	assert.True(t, result.Found)
	assert.True(t, result.Released)
	_, err = os.Lstat(filepath.Dir(leaf) + ".copy")
	require.NoError(t, err)
}

func TestVMRootfsExportRootReleaseRetiresDataWithoutClaimingUnknownSourceRestored(t *testing.T) {
	dirs, output, leaf := newVMRootfsExportOutputFixture(t)
	journal, err := openVMRootfsExportJournal(t.Context(), dirs, testVMID, false)
	require.NoError(t, err)
	defer func() { require.NoError(t, journal.close(context.Background())) }()
	intent := rootfsExportTestProgress("Running", "resume_inflight")
	require.NoError(t, journal.write(t.Context(), intent))
	path := filepath.Join(dirs.policy.rootPath, "var/lib/mvmctl/vm-rootfs-exports/1000", testVMID+".json")
	require.NoError(t, os.Chmod(path, 0666))
	require.NoError(t, output.close(t.Context()))
	result, err := exportFixtureServer(dirs).Release(t.Context(), 1000, 1000,
		VMRootfsExportReleaseRequest{ImageID: intent.ImageID})
	require.NoError(t, err)
	assert.True(t, result.Released)
	assert.True(t, result.SourceRecoveryPending)
	_, err = os.Lstat(filepath.Dir(leaf))
	assert.ErrorIs(t, err, os.ErrNotExist)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0666), info.Mode().Perm(), "copy release must not rewrite source recovery authority")
}

func TestVMRootfsExportRootNeverAdoptsUnrecordedDirectory(t *testing.T) {
	// Rationale: neither EEXIST nor an interrupted mkdir proves the exact
	// deletion authority needed to remove even an empty root-owned directory.
	for name, interrupted := range map[string]bool{"preexisting_empty": false, "crash_before_identity": true} {
		t.Run(name, func(t *testing.T) {
			dirs, _ := newVMRootfsExportJournalFixture(t)
			progress := rootfsExportTestProgress("Running", "prepared")
			entry, err := lockVMRootfsExportStoreEntry(t.Context(), dirs, progress.ImageID)
			require.NoError(t, err)
			require.NoError(t, entry.openDataParents(t.Context(), true))
			if interrupted {
				root, rootErr := exportNode(entry.dataRoot)
				require.NoError(t, rootErr)
				uid, uidErr := exportNode(entry.uidDirectory)
				require.NoError(t, uidErr)
				// Durable allocation intent and sentinel exist, but the receiver
				// died between mkdir and the exact directory-identity checkpoint.
				entry.record = &vmRootfsExportCopyRecord{Schema: 1, Source: progress, Phase: "allocating",
					DataRoot: root, UIDDirectory: uid}
				require.NoError(t, entry.writeRecord(t.Context()))
				require.NoError(t, entry.createPresenceMarker(t.Context()))
			}
			require.NoError(t, unix.Mkdirat(entry.uidDirectory, progress.ImageID, 0700))
			require.NoError(t, unix.Fsync(entry.uidDirectory))
			var before unix.Stat_t
			require.NoError(t, unix.Fstatat(entry.uidDirectory, progress.ImageID, &before, unix.AT_SYMLINK_NOFOLLOW))
			require.NoError(t, entry.close(t.Context()))
			if !interrupted {
				_, err = prepareVMRootfsExportOutput(t.Context(), dirs, progress)
				require.Error(t, err)
			}
			path := filepath.Join(dirs.policy.rootPath, "var/lib/mvmctl/vm-rootfs-export-data/1000", progress.ImageID)
			var after unix.Stat_t
			require.NoError(t, unix.Lstat(path, &after), "failed prepare must preserve an unrecorded empty directory")
			assert.Equal(t, before.Ino, after.Ino)
			assert.Equal(t, before.Dev, after.Dev)
			_, err = exportFixtureServer(dirs).Release(t.Context(), 1000, 1000,
				VMRootfsExportReleaseRequest{ImageID: progress.ImageID})
			require.Error(t, err)
			require.NoError(t, unix.Lstat(path, &after), "a later receiver must not guess missing directory identity")
			assert.Equal(t, before.Ino, after.Ino)
			assert.Equal(t, before.Dev, after.Dev)
			names, err := os.ReadDir(path)
			require.NoError(t, err)
			assert.Empty(t, names, "no output may be published in an unadopted directory")
		})
	}
}
