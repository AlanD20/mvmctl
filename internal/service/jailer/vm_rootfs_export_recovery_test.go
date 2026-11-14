package jailer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// newVMRootfsExportJournalFixture uses real root-only publication and readback;
// only Firecracker's state effects are represented by the test control below.
func newVMRootfsExportJournalFixture(t *testing.T) (*instanceUIDDirectories, *vmRootfsExportJournal) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("root journal authority runs only in the approved disposable runner")
	}
	dirs := &instanceUIDDirectories{deps: realInstanceAuthorityDeps(),
		policy: productionInstanceAuthorityPolicy(), uid: 1000}
	dirs.policy.rootPath = t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dirs.policy.rootPath, "var", "lib"), 0755))
	// Model host traversal parents regardless of umask; private authority children remain unchanged.
	require.NoError(t, os.Chmod(filepath.Join(dirs.policy.rootPath, "var"), 0755))
	require.NoError(t, os.Chmod(filepath.Join(dirs.policy.rootPath, "var", "lib"), 0755))
	journal, err := openVMRootfsExportJournal(t.Context(), dirs, testVMID, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, journal.close(context.Background())) })
	return dirs, journal
}

func rootfsExportTestProgress(state, stage string) vmRootfsExportProgress {
	return vmRootfsExportProgress{Schema: 1, OwnerUID: 1000, OwnerGID: 1000, VMID: testVMID,
		ImageID: strings.Repeat("c", 64), CacheRoot: "/home/operator/cache", Generation: 1,
		BootID: "00000000-0000-4000-8000-000000000001", OriginalState: state, Stage: stage,
		Rootfs: protectedBackingBinding{kind: protectedBackingRootfs, logicalID: testVMID, ownerUID: 1000,
			bytes: 4096, generation: 1, cacheIdentity: managedCacheIdentity{inode: 1},
			identity: managedVMConfigStat{inode: 2, uid: 0, gid: 0, mode: unix.S_IFREG | 0600, nlink: 1, size: 4096}}}
}

type rootfsExportTestControl struct {
	current     string
	resumeError error
	journal     *vmRootfsExportJournal
	resumeCount int
}

func (control *rootfsExportTestControl) state(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return control.current, nil
}

func (control *rootfsExportTestControl) pause(ctx context.Context) error {
	return errors.New("restoration must never issue pause")
}

func (control *rootfsExportTestControl) resume(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	progress, err := control.journal.read(ctx)
	if err != nil || progress == nil || progress.Stage != "resume_inflight" {
		return errors.New("resume had no durable original-state intent")
	}
	control.resumeCount++
	control.current = "Running"
	return control.resumeError
}

func TestVMRootfsExportRootRestorationPersistsStateAndIgnoresRequestCancellation(t *testing.T) {
	for name, test := range map[string]struct {
		original, current, stage string
		cancelled                bool
	}{
		"running_copy_failure": {original: "Running", current: "Paused", stage: "copying"},
		"running_cancellation": {original: "Running", current: "Paused", stage: "copying", cancelled: true},
		"paused_cancellation":  {original: "Paused", current: "Paused", stage: "copying", cancelled: true},
		"stopped_copy_failure": {original: "Stopped", stage: "copying"},
	} {
		t.Run(name, func(t *testing.T) {
			_, journal := newVMRootfsExportJournalFixture(t)
			progress := rootfsExportTestProgress(test.original, test.stage)
			require.NoError(t, journal.write(t.Context(), progress))
			control := &rootfsExportTestControl{current: test.current, journal: journal}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if test.cancelled {
				cancel()
			}
			state, err := finalizeVMRootfsExportState(ctx, control, journal, &progress)
			require.NoError(t, err)
			assert.Equal(t, test.original, state)
			if test.original != "Stopped" {
				assert.Equal(t, test.original, control.current)
			}
			persisted, err := journal.read(t.Context())
			require.NoError(t, err)
			require.NotNil(t, persisted)
			want := rootfsExportTestProgress(test.original, "restored")
			if diff := cmp.Diff(want, *persisted, cmpopts.EquateComparable(protectedBackingBinding{})); diff != "" {
				t.Errorf("durable restoration (-want +got):\n%s", diff)
			}
			if test.original != "Running" {
				assert.Zero(t, control.resumeCount) // Originally paused/stopped restoration is a no-op.
			}
		})
	}
}

func TestVMRootfsExportRootUncertainControlRemainsPendingWithoutBlindReplay(t *testing.T) {
	for _, stage := range []string{"pause_inflight", "resume_inflight"} {
		t.Run(stage, func(t *testing.T) {
			dirs, journal := newVMRootfsExportJournalFixture(t)
			progress := rootfsExportTestProgress("Running", stage)
			require.NoError(t, journal.write(t.Context(), progress))
			control := &rootfsExportTestControl{current: "Paused", journal: journal}
			state, err := finalizeVMRootfsExportState(t.Context(), control, journal, &progress)
			require.Error(t, err)
			assert.Equal(t, "unknown", state)
			assert.Equal(t, "Paused", control.current)
			assert.Zero(t, control.resumeCount) // A possibly applied mutation cannot be replayed.
			persisted, err := journal.read(t.Context())
			require.NoError(t, err)
			if diff := cmp.Diff(progress, *persisted, cmpopts.EquateComparable(protectedBackingBinding{})); diff != "" {
				t.Errorf("uncertain intent changed (-want +got):\n%s", diff)
			}
			err = requireVMRootfsExportIdle(t.Context(), dirs, instanceRecord{ownerUID: 1000, vmID: testVMID})
			require.Error(t, err)
			assert.Equal(t, errs.CodeImageImportFailed, errs.AsDomainError(err).Code)
		})
	}
}

func TestVMRootfsExportRootLostResumeResponseDoesNotClaimRestoredState(t *testing.T) {
	_, journal := newVMRootfsExportJournalFixture(t)
	progress := rootfsExportTestProgress("Running", "copy_quiesced")
	require.NoError(t, journal.write(t.Context(), progress))
	control := &rootfsExportTestControl{current: "Paused", journal: journal, resumeError: errors.New("response lost")}
	state, err := finalizeVMRootfsExportState(t.Context(), control, journal, &progress)
	require.Error(t, err)
	assert.Equal(t, "unknown", state)
	persisted, err := journal.read(t.Context())
	require.NoError(t, err)
	require.NotNil(t, persisted)
	assert.Equal(t, "resume_inflight", persisted.Stage)
	assert.Equal(
		t,
		"Running",
		control.current,
	) // Applied effects are deliberately not equated with confirmed responses.
}

func TestVMRootfsExportRootTerminationRetiresUncertainIntentWithoutResume(t *testing.T) {
	dirs, journal := newVMRootfsExportJournalFixture(t)
	progress := rootfsExportTestProgress("Running", "pause_inflight")
	require.NoError(t, journal.write(t.Context(), progress))
	record := instanceRecord{ownerUID: 1000, vmID: testVMID, launchGeneration: 1, lifecycle: instanceLifecycleCleaned}
	require.NoError(t, retireStoppedVMRootfsExport(t.Context(), dirs, record))
	persisted, err := journal.read(t.Context())
	require.NoError(t, err)
	assert.Nil(t, persisted)
	assert.NoError(t, requireVMRootfsExportIdle(t.Context(), dirs, record))
}

func TestVMRootfsExportRootRecoveryRefusesReplacementGeneration(t *testing.T) {
	dirs, journal := newVMRootfsExportJournalFixture(t)
	progress := rootfsExportTestProgress("Running", "paused")
	require.NoError(t, journal.write(t.Context(), progress))
	control := &rootfsExportTestControl{current: "Paused", journal: journal}
	locked := &lockedInstance{directories: dirs, record: instanceRecord{ownerUID: 1000, vmID: testVMID,
		launchGeneration: 2, lifecycle: instanceLifecycleActive}}
	current := vmLaunchDeviceAuthority{Rootfs: progress.Rootfs, HostBootID: progress.BootID}
	require.Error(t, recoverVMRootfsExport(t.Context(), locked, journal, control, current))
	assert.Equal(t, "Paused", control.current)
	assert.Zero(t, control.resumeCount)
	persisted, err := journal.read(t.Context())
	require.NoError(t, err)
	if diff := cmp.Diff(progress, *persisted, cmpopts.EquateComparable(protectedBackingBinding{})); diff != "" {
		t.Errorf("replaced generation altered retained intent (-want +got):\n%s", diff)
	}
}

func TestVMRootfsExportRootJournalRefusesCallerWritableIntent(t *testing.T) {
	dirs, journal := newVMRootfsExportJournalFixture(t)
	progress := rootfsExportTestProgress("Running", "paused")
	require.NoError(t, journal.write(t.Context(), progress))
	// A valid document is not authority after its mode permits caller writes.
	require.NoError(t, unix.Fchmodat(journal.directory, testVMID+".json", 0666, 0))
	_, err := journal.read(t.Context())
	require.Error(t, err)
	err = requireVMRootfsExportIdle(t.Context(), dirs, instanceRecord{ownerUID: 1000, vmID: testVMID})
	require.Error(t, err)
	assert.Equal(t, errs.CodeImageImportFailed, errs.AsDomainError(err).Code)
}

func TestVMRootfsExportRootCompletedIntentDoesNotBlockControl(t *testing.T) {
	dirs, journal := newVMRootfsExportJournalFixture(t)
	progress := rootfsExportTestProgress("Running", "complete")
	require.NoError(t, journal.write(t.Context(), progress))
	require.NoError(t, requireVMRootfsExportIdle(t.Context(), dirs,
		instanceRecord{ownerUID: 1000, vmID: testVMID, launchGeneration: 1}))
	persisted, err := journal.read(t.Context())
	require.NoError(t, err)
	require.NotNil(t, persisted)
	if diff := cmp.Diff(progress, *persisted, cmpopts.EquateComparable(protectedBackingBinding{})); diff != "" {
		t.Errorf("admission mutated completed intent (-want +got):\n%s", diff)
	}
}
