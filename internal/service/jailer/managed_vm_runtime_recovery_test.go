package jailer

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreshRecoveryCleanupReopensAndRemovesStaleVMRuntime(t *testing.T) {
	authority, recoveryDeps, identityDeps := newManagedVMRuntimeRecoveryTestAuthority(t)
	caller := managedVMRuntimeRecoveryTestCaller()
	runtimePath := prepareStaleManagedVMRuntimeTestDirectory(t, authority.policy.rootPath, caller)
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
		caller,
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
	caller := managedVMRuntimeRecoveryTestCaller()
	runtimePath := filepath.Join(authority.policy.rootPath, "run/mvmctl/runtime")

	cleanup, err := beginFreshRecoveryCleanupWithDeps(
		t.Context(),
		authority,
		caller,
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
	caller := managedVMRuntimeRecoveryTestCaller()
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
		caller,
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
		managedVMRuntimeUIDName(caller.uid),
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
	caller := managedVMRuntimeRecoveryTestCaller()
	// These root-only cases use a dedicated UID so their real fixed inventory cannot
	// satisfy another recovery fixture's missing-inventory check by test ordering.
	ownerPath := filepath.Join("/var/lib/mvmctl/protected-backing", strconv.FormatUint(uint64(caller.uid), 10))
	_, err := os.Lstat(ownerPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, prepareProtectedVMGrantInventory(t.Context(), caller.uid))
	t.Cleanup(func() {
		for _, kind := range protectedInventoryKinds() {
			require.NoError(t, os.Remove(filepath.Join(ownerPath, "logical", kind)))
		}
		require.NoError(t, os.Remove(filepath.Join(ownerPath, "logical")))
		require.NoError(t, os.Remove(ownerPath))
	})
	record, err := registerLaunchingInstanceRecord(
		caller, testLaunchIntent(false), testLaunchProcessLocators(false), testReleaseIdentity(), nil,
	)
	require.NoError(t, err)
	writeTestAuthorityRecord(t, root, record)
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

func managedVMRuntimeRecoveryTestCaller() instanceCaller {
	uid := uint32(100_000_000 + os.Getpid())
	return instanceCaller{uid: uid, gid: uid}
}

func prepareStaleManagedVMRuntimeTestDirectory(t *testing.T, root string, caller instanceCaller) string {
	t.Helper()

	mvmctlPath := filepath.Join(root, "run/mvmctl")
	runtimePath := filepath.Join(mvmctlPath, "runtime")
	uidPath := filepath.Join(runtimePath, managedVMRuntimeUIDName(caller.uid))
	vmPath := filepath.Join(uidPath, testVMID)
	for _, path := range []string{mvmctlPath, runtimePath, uidPath} {
		require.NoError(t, os.MkdirAll(path, 0o711))
		require.NoError(t, os.Chmod(path, 0o711))
	}
	require.NoError(t, os.Mkdir(vmPath, 0o700))
	require.NoError(t, os.Chown(vmPath, int(caller.uid), int(caller.gid)))
	return vmPath
}
