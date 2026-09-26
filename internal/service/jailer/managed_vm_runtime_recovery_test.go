package jailer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreshRecoveryCleanupReopensAndRemovesStaleVMRuntime(t *testing.T) {
	authority, recoveryDeps, identityDeps := newManagedVMRuntimeRecoveryTestAuthority(t)
	runtimePath := prepareStaleManagedVMRuntimeTestDirectory(t, authority.policy.rootPath)
	for _, leaf := range []string{
		"firecracker.api.socket",
		"vsock.sock",
		"console.sock",
		"firecracker.pid",
		"console.pid",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(runtimePath, leaf), []byte("stale"), 0o600))
	}

	cleanup, err := beginFreshRecoveryCleanupWithDeps(
		t.Context(),
		authority,
		testInstanceCaller(),
		testVMID,
		recoveryDeps,
		identityDeps,
	)
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.NoDirExists(t, runtimePath)
	require.NoError(t, cleanup.Release(t.Context()))
}

// Rationale: recovery observes ephemeral state; it must not recreate an absent runtime hierarchy while cleaning a
// durable instance record after a host reboot.
func TestFreshRecoveryCleanupAbsentRuntimeDoesNotCreateRuntimeTree(t *testing.T) {
	authority, recoveryDeps, identityDeps := newManagedVMRuntimeRecoveryTestAuthority(t)
	runtimePath := filepath.Join(authority.policy.rootPath, "run/mvmctl/runtime")

	cleanup, err := beginFreshRecoveryCleanupWithDeps(
		t.Context(),
		authority,
		testInstanceCaller(),
		testVMID,
		recoveryDeps,
		identityDeps,
	)
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.NoDirExists(t, runtimePath)
	require.NoError(t, cleanup.Release(t.Context()))
}

// Rationale: recovery must not mark a VM cleaned when the non-authoritative runtime path cannot be safely reopened.
// It keeps the root-owned cleanup record and lock available for repair and retry instead of following a symlink.
func TestFreshRecoveryCleanupRejectsUnsafeRuntimeParentBeforeCleaned(t *testing.T) {
	authority, recoveryDeps, identityDeps := newManagedVMRuntimeRecoveryTestAuthority(t)
	root := authority.policy.rootPath
	mvmctlPath := filepath.Join(root, "run/mvmctl")
	require.NoError(t, os.Mkdir(mvmctlPath, 0o711))
	attackerPath := filepath.Join(root, "attacker")
	require.NoError(t, os.Mkdir(attackerPath, 0o711))
	runtimePath := filepath.Join(mvmctlPath, "runtime")
	require.NoError(t, os.Symlink(attackerPath, runtimePath))

	cleanup, err := beginFreshRecoveryCleanupWithDeps(
		t.Context(),
		authority,
		testInstanceCaller(),
		testVMID,
		recoveryDeps,
		identityDeps,
	)
	require.NoError(t, err)
	require.Error(t, cleanup.Complete(t.Context()))
	assert.Equal(t, instanceLifecycleCleaning, cleanup.snapshot().lifecycle)
	recordPath := filepath.Join(
		root,
		"var/lib/mvmctl/instances",
		managedVMRuntimeUIDName(testAuthorityUID),
		testVMID+".json",
	)
	raw, err := os.ReadFile(recordPath)
	require.NoError(t, err)
	persisted, err := decodeInstanceRecord(raw)
	require.NoError(t, err)
	assertInstanceRecordEqual(t, cleanup.snapshot(), persisted)
	info, err := os.Lstat(runtimePath)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink)

	require.NoError(t, os.Remove(runtimePath))
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
	raw, err = os.ReadFile(recordPath)
	require.NoError(t, err)
	persisted, err = decodeInstanceRecord(raw)
	require.NoError(t, err)
	assertInstanceRecordEqual(t, cleanup.snapshot(), persisted)
	require.NoError(t, cleanup.Release(t.Context()))
}

func newManagedVMRuntimeRecoveryTestAuthority(
	t *testing.T,
) (*instanceAuthority, processRecoveryDeps, processIdentityVerifierDeps) {
	t.Helper()

	root := prepareInstanceAuthorityTestRoot(t)
	// A launching record is only valid after the root-controlled grant inventory exists.
	// These cases use the real fixed inventory path and therefore run serially.
	require.NoError(t, prepareProtectedVMGrantInventory(t.Context(), testAuthorityUID))
	writeTestAuthorityRecord(t, root, testLaunchingInstanceRecord())
	authority := newInstanceAuthorityWithPolicy(
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
		newTestInstanceAuthorityTree(),
	)
	authority.cgroupDeps = newManagedVMCgroupTestFixture(t).deps()
	identityDeps := realProcessIdentityVerifierDeps()
	identityDeps.readBootID = func(context.Context) (string, error) { return processOtherBootID, nil }
	return authority, realProcessRecoveryDeps(), identityDeps
}

func prepareStaleManagedVMRuntimeTestDirectory(t *testing.T, root string) string {
	t.Helper()

	mvmctlPath := filepath.Join(root, "run/mvmctl")
	runtimePath := filepath.Join(mvmctlPath, "runtime")
	uidPath := filepath.Join(runtimePath, managedVMRuntimeUIDName(testAuthorityUID))
	vmPath := filepath.Join(uidPath, testVMID)
	for _, path := range []string{mvmctlPath, runtimePath, uidPath} {
		require.NoError(t, os.MkdirAll(path, 0o711))
		require.NoError(t, os.Chmod(path, 0o711))
	}
	require.NoError(t, os.Mkdir(vmPath, 0o700))
	require.NoError(t, os.Chown(vmPath, int(testAuthorityUID), int(testAuthorityGID)))
	return vmPath
}
