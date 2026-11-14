package jailer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

func TestVMRootfsExportRootCleanedLifecycleRetriesFailedRetirement(t *testing.T) {
	// Rationale: the cleaned VM record commits before export retirement. A
	// busy retirement must remain retryable through the same stop/cleanup entry
	// point rather than leaving source intent permanently blocking relaunch.
	dirs, output, leaf := newVMRootfsExportOutputFixture(t)
	journal, err := openVMRootfsExportJournal(t.Context(), dirs, testVMID, false)
	require.NoError(t, err)
	defer func() { require.NoError(t, journal.close(context.Background())) }()
	progress := rootfsExportTestProgress("Running", "pause_inflight")
	progress.OutputDevice, progress.OutputInode = uint64(output.identity.Dev), output.identity.Ino
	parent, err := exportNode(output.entry.directory)
	require.NoError(t, err)
	progress.ParentDevice, progress.ParentInode = parent.Device, parent.Inode
	require.NoError(t, journal.write(t.Context(), progress))
	record := instanceRecord{schemaVersion: instanceRecordSchemaVersion, ownerUID: dirs.uid, vmID: testVMID,
		networkPlacement: model.VMNetworkPlacementManaged, lifecycle: instanceLifecycleCleaned,
		launchGeneration: progress.Generation, cleanupGeneration: progress.Generation}
	authority := newInstanceAuthorityWithPolicy(dirs.deps, dirs.policy, newTestInstanceAuthorityTree())
	roots, err := openInstanceAuthorityRoots(t.Context(), authority.deps, authority.policy, authority.authorityTree)
	require.NoError(t, err)
	defer func() { require.NoError(t, roots.Release(context.Background())) }()
	state, err := roots.openUIDDirectories(t.Context(), dirs.uid)
	require.NoError(t, err)
	defer func() { require.NoError(t, state.Release(context.Background())) }()
	require.NoError(t, state.writeRecord(t.Context(), record))
	recordPath := filepath.Join(dirs.policy.rootPath, "var/lib/mvmctl/instances/1000", testVMID+".json")
	before, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	// This is the post-commit failure window of Complete: the independent
	// ImageID lease is still busy although VM lifecycle cleanup is durable.
	require.Error(t, retireStoppedVMRootfsExport(t.Context(), state, record))
	lease, err := authority.beginLifecycleCleanup(t.Context(), testInstanceCaller(), testVMID)
	require.Error(t, err, "already-cleaned cleanup must retry retirement and report its remaining failure")
	assert.Nil(t, lease)
	retained, err := journal.read(t.Context())
	require.NoError(t, err)
	require.NotNil(t, retained)
	assert.Equal(t, "pause_inflight", retained.Stage)
	assert.Error(t, requireVMRootfsExportIdle(t.Context(), state, record))
	require.NoError(t, output.close(t.Context()))
	lease, err = authority.beginLifecycleCleanup(t.Context(), testInstanceCaller(), testVMID)
	require.NoError(t, err)
	assert.Nil(t, lease)
	retained, err = journal.read(t.Context())
	require.NoError(t, err)
	assert.Nil(t, retained)
	assert.NoError(t, requireVMRootfsExportIdle(t.Context(), state, record))
	_, err = os.Lstat(filepath.Dir(leaf))
	assert.ErrorIs(t, err, os.ErrNotExist)
	marker, err := os.ReadFile(filepath.Dir(leaf) + ".copy")
	require.NoError(t, err)
	assert.Equal(t, progress.ImageID, string(marker))
	// A subsequent stop is idempotent; it must not recreate data or rewrite
	// the existing cleaned generation while reconciling the root tombstone.
	lease, err = authority.beginLifecycleCleanup(t.Context(), testInstanceCaller(), testVMID)
	require.NoError(t, err)
	assert.Nil(t, lease)
	after, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after))
	var stat unix.Stat_t
	require.NoError(t, unix.Lstat(filepath.Dir(leaf)+".copy", &stat))
	assert.Zero(t, stat.Uid)
}
