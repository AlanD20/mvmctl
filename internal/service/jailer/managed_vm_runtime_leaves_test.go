package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedVMRuntimeRemovesOnlyFixedLeaves(t *testing.T) {
	t.Parallel()

	lease, runtimePath := newManagedVMRuntimeLeafTestLease(t, realManagedVMRuntimeDeps())
	fixedLeaves := []string{
		"firecracker.api.socket",
		"vsock.sock",
		"console.sock",
		"firecracker.pid",
		"console.pid",
	}
	externalPath := filepath.Join(t.TempDir(), "external-target")
	require.NoError(t, os.WriteFile(externalPath, []byte("keep"), 0o600))
	for _, leaf := range fixedLeaves {
		if leaf == "vsock.sock" {
			require.NoError(t, os.Symlink(externalPath, filepath.Join(runtimePath, leaf)))
			continue
		}
		require.NoError(t, os.WriteFile(filepath.Join(runtimePath, leaf), []byte("stale"), 0o600))
	}
	unexpectedPath := filepath.Join(runtimePath, "operator-note")
	require.NoError(t, os.WriteFile(unexpectedPath, []byte("keep"), 0o600))

	require.NoError(t, lease.RemoveFixedLeaves(t.Context()))
	for _, leaf := range fixedLeaves {
		assert.NoFileExists(t, filepath.Join(runtimePath, leaf))
	}
	assert.FileExists(t, externalPath)
	assert.FileExists(t, unexpectedPath)
	require.Error(t, lease.Remove(t.Context()))

	require.NoError(t, os.Remove(unexpectedPath))
	require.NoError(t, lease.RemoveFixedLeaves(t.Context()))
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

// Rationale: cleanup must never turn a fixed output name into recursive deletion authority. A caller-created
// directory at a reserved name stays intact and leaves the runtime directory available for explicit reconciliation.
func TestManagedVMRuntimeFixedLeafDirectoryFailsClosed(t *testing.T) {
	t.Parallel()

	lease, runtimePath := newManagedVMRuntimeLeafTestLease(t, realManagedVMRuntimeDeps())
	directoryPath := filepath.Join(runtimePath, "vsock.sock")
	require.NoError(t, os.Mkdir(directoryPath, 0o700))
	nestedPath := filepath.Join(directoryPath, "nested")
	require.NoError(t, os.WriteFile(nestedPath, []byte("keep"), 0o600))

	require.Error(t, lease.RemoveFixedLeaves(t.Context()))
	assert.FileExists(t, nestedPath)

	require.NoError(t, os.Remove(nestedPath))
	require.NoError(t, os.Remove(directoryPath))
	require.NoError(t, lease.RemoveFixedLeaves(t.Context()))
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

// Rationale: an unlink can take effect while reporting failure. Retrying from the closed fixed-name set must safely
// accept already-absent leaves and continue without reconstructing a path or deleting an unexpected name.
func TestManagedVMRuntimeFixedLeafCleanupResumesAfterEffectfulUnlinkError(t *testing.T) {
	t.Parallel()

	deps := realManagedVMRuntimeDeps()
	realUnlinkAt := deps.unlinkAt
	effectErr := errors.New("unlink result lost")
	failed := false
	deps.unlinkAt = func(ctx context.Context, parentFD int, name string, flags int) error {
		err := realUnlinkAt(ctx, parentFD, name, flags)
		if name == "console.sock" && flags == 0 && !failed {
			failed = true
			require.NoError(t, err)
			return effectErr
		}
		return err
	}
	lease, runtimePath := newManagedVMRuntimeLeafTestLease(t, deps)
	for _, leaf := range []string{
		"firecracker.api.socket",
		"vsock.sock",
		"console.sock",
		"firecracker.pid",
		"console.pid",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(runtimePath, leaf), []byte("stale"), 0o600))
	}

	err := lease.RemoveFixedLeaves(t.Context())
	require.ErrorIs(t, err, effectErr)
	assert.NoFileExists(t, filepath.Join(runtimePath, "console.sock"))
	require.NoError(t, lease.RemoveFixedLeaves(t.Context()))
	require.NoError(t, lease.Remove(t.Context()))
	require.NoError(t, lease.Release(t.Context()))
}

func TestManagedVMRuntimeFixedLeafCleanupIgnoresCancellationAfterAdmission(t *testing.T) {
	t.Parallel()

	lease, runtimePath := newManagedVMRuntimeLeafTestLease(t, realManagedVMRuntimeDeps())
	leafPath := filepath.Join(runtimePath, "firecracker.pid")
	require.NoError(t, os.WriteFile(leafPath, []byte("123\n"), 0o600))
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, lease.RemoveFixedLeaves(cancelled))
	assert.NoFileExists(t, leafPath)
	require.NoError(t, lease.Remove(cancelled))
	require.NoError(t, lease.Release(cancelled))
}

func newManagedVMRuntimeLeafTestLease(
	t *testing.T,
	deps managedVMRuntimeDeps,
) (*managedVMRuntimeLease, string) {
	t.Helper()

	root := prepareManagedVMRuntimeTestRoot(t)
	lease, err := createManagedVMRuntime(
		t.Context(),
		deps,
		managedVMRuntimeTestPolicy(root),
		managedVMRuntimeTestCaller(),
		testVMID,
	)
	require.NoError(t, err)
	return lease, managedVMRuntimeTestPath(root)
}
