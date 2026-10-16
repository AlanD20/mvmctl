package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestLaunchLeaseRuntimeTransfersThroughAbortAndCompletesCleanup(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	runtimeRoot := prepareManagedVMRuntimeTestRoot(t)
	require.NoError(t, launch.prepareRuntimeDirectory(
		t.Context(),
		realManagedVMRuntimeDeps(),
		managedVMRuntimeTestPolicy(runtimeRoot),
	))
	assert.True(t, launch.snapshot().hasRuntimeDirectory)
	assert.DirExists(t, managedVMRuntimeTestPath(runtimeRoot))

	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	assert.False(t, launch.snapshot().hasRuntimeDirectory)
	require.NotNil(t, cleanup.state.runtimeDirectory)
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.NoDirExists(t, managedVMRuntimeTestPath(runtimeRoot))
	assert.Nil(t, cleanup.state.runtimeDirectory)
	require.NoError(t, cleanup.Release(t.Context()))
}

// Rationale: a post-create preparation failure occurs after durable launching authority exists. The launch lease must
// retain the partial runtime capability so abort can remove it without reconstructing a path.
func TestLaunchLeaseRetainsPartialRuntimeAuthorityForAbort(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	runtimeRoot := prepareManagedVMRuntimeTestRoot(t)
	deps := realManagedVMRuntimeDeps()
	realOpenAt2 := deps.openAt2
	realFchmod := deps.fchmod
	vmFD := -1
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, openErr := realOpenAt2(ctx, parentFD, name, how)
		if openErr == nil && name == testVMID {
			vmFD = fd
		}
		return fd, openErr
	}
	prepareErr := errors.New("prepare runtime failed")
	deps.fchmod = func(ctx context.Context, fd int, mode uint32) error {
		if fd == vmFD {
			return prepareErr
		}
		return realFchmod(ctx, fd, mode)
	}

	err = launch.prepareRuntimeDirectory(t.Context(), deps, managedVMRuntimeTestPolicy(runtimeRoot))
	require.ErrorIs(t, err, prepareErr)
	assert.True(t, launch.snapshot().hasRuntimeDirectory)
	cleanup, abortErr := launch.Abort(t.Context())
	require.NoError(t, abortErr)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
	assert.NoDirExists(t, managedVMRuntimeTestPath(runtimeRoot))
}

func TestLaunchLeaseRuntimeTransfersThroughActivationAndActiveCleanup(t *testing.T) {
	t.Parallel()

	fixture := newProcessIdentityFixture(t, false)
	runtimeRoot := prepareManagedVMRuntimeTestRoot(t)
	require.NoError(t, fixture.launch.prepareRuntimeDirectory(
		t.Context(),
		realManagedVMRuntimeDeps(),
		managedVMRuntimeTestPolicy(runtimeRoot),
	))
	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.NoError(t, err)
	require.NotNil(t, active.state.runtimeDirectory)
	assert.Nil(t, fixture.launch.state.runtimeDirectory)

	cleanup, err := active.BeginCleanup(t.Context())
	require.NoError(t, err)
	assert.Nil(t, active.state.runtimeDirectory)
	require.NotNil(t, cleanup.state.runtimeDirectory)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
	assert.NoDirExists(t, managedVMRuntimeTestPath(runtimeRoot))
}

// Rationale: caller-created leaves must keep cleanup in the durable cleaning state until an authority-verified leaf
// cleanup step removes them. The VM directory itself must never be recursively deleted.
func TestCleanupLeaseDoesNotCommitCleanedWhileRuntimeDirectoryIsNonEmpty(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	runtimeRoot := prepareManagedVMRuntimeTestRoot(t)
	require.NoError(t, launch.prepareRuntimeDirectory(
		t.Context(),
		realManagedVMRuntimeDeps(),
		managedVMRuntimeTestPolicy(runtimeRoot),
	))
	runtimePath := managedVMRuntimeTestPath(runtimeRoot)
	require.NoError(t, os.WriteFile(filepath.Join(runtimePath, "api.socket"), []byte("stale"), 0o600))

	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	require.Error(t, cleanup.Complete(t.Context()))
	assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
	assert.DirExists(t, runtimePath)

	require.NoError(t, os.Remove(filepath.Join(runtimePath, "api.socket")))
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
	require.NoError(t, cleanup.Release(t.Context()))
	assert.NoDirExists(t, runtimePath)
}

func managedVMRuntimeTestPath(root string) string {
	return filepath.Join(
		root,
		"run/mvmctl/runtime",
		managedVMRuntimeUIDName(managedVMRuntimeTestCaller().uid),
		testVMID,
	)
}
