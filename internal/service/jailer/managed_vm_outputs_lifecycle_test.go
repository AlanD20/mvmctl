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

	"mvmctl/pkg/errs"
)

// Rationale: a post-rename error returns cleanup authority, but that non-nil lease must never authorize outputs.
func TestPersistentOutputsRequireSuccessfulRegistrationReturn(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"pre-rename", "record directory sync", "index release"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			fixture := newPersistentOutputResources(t)
			fixture.write(t, "firecracker.log", "prior log")
			root := prepareInstanceAuthorityTestRoot(t)
			deps := realInstanceAuthorityDeps()
			tree := newTestInstanceAuthorityTree()
			fault := errors.New("registration did not complete")
			recordRenamed := false
			realRename, realSync := deps.renameAt, deps.fsync
			switch stage {
			case "pre-rename":
				deps.renameAt = func(context.Context, int, string, int, string) error { return fault }
			case "record directory sync":
				deps.renameAt = func(ctx context.Context, oldFD int, oldName string, newFD int, newName string) error {
					err := realRename(ctx, oldFD, oldName, newFD, newName)
					recordRenamed = err == nil
					return err
				}
				deps.fsync = func(ctx context.Context, fd int) error {
					if recordRenamed {
						return fault
					}
					return realSync(ctx, fd)
				}
			case "index release":
				tree.lockReleaseErrDescription = "index"
				tree.lockReleaseErr = fault
			}
			authority := newInstanceAuthorityWithPolicy(deps, testInstanceAuthorityPolicy(root), tree)
			regErr := fixture.register(t, authority)
			require.ErrorIs(t, regErr, fault)
			require.Error(t, fixture.prepare(t.Context(), managedPersistentMetricsEnabled))
			assert.Equal(t, "prior log", readPersistentOutput(t, fixture.directory, "firecracker.log"))
			assert.NoFileExists(t, filepath.Join(fixture.directory, "firecracker.console.log"))
			assert.NotEmpty(t, fixture.cache.retained)
			if stage == "pre-rename" {
				assert.Nil(t, fixture.launch)
				return
			}
			require.NotNil(t, fixture.launch)
			assert.False(t, fixture.launch.state.registrationComplete)
			assert.Nil(t, fixture.launch.state.persistentOutputs)
			assert.Equal(t, true, errs.AsDomainError(regErr).Details["record_replaced"])
			raw, err := os.ReadFile(filepath.Join(root, "var/lib/mvmctl/instances/1000", testVMID+".json"))
			require.NoError(t, err)
			record, err := decodeInstanceRecord(raw)
			require.NoError(t, err)
			assert.Equal(t, instanceLifecycleLaunching, record.lifecycle)
			recordRenamed = false
			fixture.launch.state.directories.deps.fsync = realSync
			cleanupPersistentOutputLaunch(t, fixture.launch)
		})
	}
}

func TestPersistentOutputsRequireBlockedChildren(t *testing.T) {
	t.Parallel()
	for _, status := range []blockedLaunchProcessStatus{
		blockedLaunchProcessesRelayReleasedToJailer,
		blockedLaunchProcessesReleasedToJailer,
	} {
		t.Run(string(rune('0'+status)), func(t *testing.T) {
			t.Parallel()
			fixture := newPersistentOutputFixture(t)
			fixture.write(t, "firecracker.log", "running process evidence")
			fixture.launch.state.processes.state.status = status
			err := fixture.prepare(t.Context(), managedPersistentMetricsDisabled)
			require.Error(t, err)
			assert.Contains(t, errs.AsDomainError(err).Message, "both children to remain blocked")
			assert.Nil(t, fixture.launch.state.persistentOutputs)
			assert.Equal(t, "running process evidence", readPersistentOutput(t, fixture.directory, "firecracker.log"))
		})
	}
}

func TestPersistentOutputsAbortPreservesFilesAndTransfersAuthority(t *testing.T) {
	t.Parallel()
	fixture := newPersistentOutputFixture(t)
	require.NoError(t, fixture.prepare(t.Context(), managedPersistentMetricsEnabled))
	outputs := fixture.launch.state.persistentOutputs
	fixture.write(t, "firecracker.log", "launch evidence")
	cleanup, err := fixture.launch.Abort(t.Context())
	require.NoError(t, err)
	assert.Nil(t, fixture.launch.state.persistentOutputs)
	assert.Same(t, outputs, cleanup.state.persistentOutputs)
	require.NoError(t, cleanup.Complete(t.Context()))
	assert.Nil(t, cleanup.state.persistentOutputs)
	assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
	assert.Equal(t, "launch evidence", readPersistentOutput(t, fixture.directory, "firecracker.log"))
	require.NoError(t, cleanup.Release(t.Context()))
}

// attachPersistentOutputLifecycleFixture isolates ownership transfer from admission: the file is already prepared,
// and the existing process fixture has already completed its simulated Jailer handoff. Admission is covered separately.
func attachPersistentOutputLifecycleFixture(
	t *testing.T,
	launch *launchLease,
) (*managedVMPersistentOutputLease, string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "firecracker.log")
	require.NoError(t, os.WriteFile(path, []byte("active output evidence"), 0600))
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	require.NoError(t, err)
	outputs := &managedVMPersistentOutputLease{state: &managedVMPersistentOutputState{
		deps: realManagedPersistentOutputDeps(), vmDirectoryFD: -1, retained: []int{fd},
		outputs: []managedPersistentOutput{{name: managedPersistentLog, fd: fd}}, attempted: true, ready: true,
	}}
	launch.state.persistentOutputs = outputs
	return outputs, directory
}

func TestPersistentOutputsTransferThroughActivationAndCleanup(t *testing.T) {
	t.Parallel()
	for _, postRenameFailure := range []bool{false, true} {
		name := "success"
		if postRenameFailure {
			name = "active rename followed by sync failure"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newProcessIdentityFixture(t, false)
			t.Cleanup(func() { cleanupPersistentOutputLaunch(t, fixture.launch) })
			outputs, directory := attachPersistentOutputLifecycleFixture(t, fixture.launch)
			deps := &fixture.launch.state.directories.deps
			realSync, realRename := deps.fsync, deps.renameAt
			fault := errors.New("active record sync failure")
			renamed := false
			if postRenameFailure {
				deps.renameAt = func(ctx context.Context, oldFD int, oldName string, newFD int, newName string) error {
					err := realRename(ctx, oldFD, oldName, newFD, newName)
					renamed = err == nil
					return err
				}
				deps.fsync = func(ctx context.Context, fd int) error {
					if renamed {
						return fault
					}
					return realSync(ctx, fd)
				}
			}
			active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
			if postRenameFailure {
				require.ErrorIs(t, err, fault)
				assert.Equal(t, true, errs.AsDomainError(err).Details["record_replaced"])
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, active)
			assert.Nil(t, fixture.launch.state.persistentOutputs)
			assert.Same(t, outputs, active.state.persistentOutputs)
			active.state.directories.deps.fsync = realSync
			cleanup, err := active.BeginCleanup(t.Context())
			require.NoError(t, err)
			assert.Nil(t, active.state.persistentOutputs)
			assert.Same(t, outputs, cleanup.state.persistentOutputs)
			require.NoError(t, cleanup.Complete(t.Context()))
			assert.Equal(t, instanceLifecycleCleaned, cleanup.snapshot().lifecycle)
			assert.Nil(t, cleanup.state.persistentOutputs)
			assert.Equal(t, "active output evidence", readPersistentOutput(t, directory, "firecracker.log"))
			require.NoError(t, cleanup.Release(t.Context()))
		})
	}
}

// Rationale: an attached incomplete output lease must block both handoff and direct activation; absence stays
// optional because the current private graph has not cut over to the complete production launch transaction.
func TestPersistentOutputsIncompleteAuthorityBlocksHandoffAndActivation(t *testing.T) {
	t.Parallel()
	fixture := newProcessIdentityFixture(t, false)
	t.Cleanup(func() { cleanupPersistentOutputLaunch(t, fixture.launch) })
	outputs, directory := attachPersistentOutputLifecycleFixture(t, fixture.launch)
	outputs.state.ready = false
	err := fixture.launch.releaseToJailer(t.Context())
	require.Error(t, err)
	assert.Contains(t, errs.AsDomainError(err).Message, "persistent output preparation")
	active, err := activateVerifiedProcessWithDeps(t.Context(), fixture.launch, fixture.fake.deps())
	require.Error(t, err)
	assert.Nil(t, active)
	assert.Equal(t, instanceLifecycleLaunching, fixture.launch.snapshot().record.lifecycle)
	assert.Equal(t, "active output evidence", readPersistentOutput(t, directory, "firecracker.log"))
}
