package image

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

func runVMJournalIsolated(t *testing.T) bool {
	t.Helper()
	if os.Getenv("MVM_VM_JOURNAL_TEST_CHILD") == t.Name() {
		privateCache := os.Getenv("MVM_CACHE_DIR")
		require.NotEmpty(t, privateCache)
		admittedCache, err := infra.GetCacheDir()
		require.NoError(t, err)
		require.Equal(t, privateCache, admittedCache, "child must admit only its parent-owned private cache")
		return false
	}
	// Cache resolution is process-wide. The parent owns this namespace until its
	// synchronous child exits, including files deliberately retained by refusal tests.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0],
		"-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.count=1", "-test.timeout=30s", "-test.v")
	command.Env = append(os.Environ(), "MVM_VM_JOURNAL_TEST_CHILD="+t.Name(), "MVM_CACHE_DIR="+t.TempDir())
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "=== RUN   "+t.Name()+"\n", "exact child test must actually run")
	require.Contains(t, string(output), "--- PASS: "+t.Name()+" (", "exact child test must complete successfully")
	t.Logf("isolated child result:\n%s", output)
	return true
}

// The subprocess fake changes real bytes through the offered descriptor, rather than
// merely recording a command. No privileged receiver is needed for these caller-owned invariants.
type vmJournalRunner struct {
	calls  [][]string
	effect func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error)
}

func (runner *vmJournalRunner) Run(
	ctx context.Context,
	args []string,
	opts system.RunCmdOpts,
) (*system.RunResult, error) {
	runner.calls = append(runner.calls, append([]string(nil), args...))
	return runner.effect(ctx, args, opts)
}

func (runner *vmJournalRunner) Stream(
	_ context.Context,
	_ []string,
	_ system.RunCmdOpts,
) (<-chan system.StreamLine, error) {
	return nil, errors.New("unexpected VM journal streaming call")
}

func installVMJournalRunner(t *testing.T, runner *vmJournalRunner) {
	t.Helper()
	original := system.DefaultRunner
	system.DefaultRunner = runner
	t.Cleanup(func() { system.DefaultRunner = original })
}

func newVMJournalStage(t *testing.T) (*stagedImage, string, string) {
	t.Helper()
	cache := setupHermeticCache(t)
	source := filepath.Join(cache, "source.img")
	require.NoError(t, os.WriteFile(source, []byte("unreplayed VM journal bytes"), 0600))
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(t.Name())))
	stage, err := prepareStagedImage(context.Background(), source, id, "ext4")
	require.NoError(t, err)
	return stage, source, filepath.Join(stage.request.CacheLocator, "images", "staging", stage.request.ImageID)
}

func writeVMJournalDescriptor(t *testing.T, args []string) {
	t.Helper()
	require.Len(t, args, 5)
	file, err := os.OpenFile(args[4], os.O_RDWR, 0)
	require.NoError(t, err)
	_, err = file.WriteAt([]byte("replayed"), 0)
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func assertVMJournalBytes(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, string(data))
}

func TestVMInspectionJournalReplayOnlyMutatesDisposableCopy(t *testing.T) {
	if runVMJournalIsolated(t) {
		return
	}
	for _, exit := range []int{0, 1} {
		t.Run(string(rune('0'+exit)), func(t *testing.T) {
			stage, source, directory := newVMJournalStage(t)
			before, err := os.Stat(source)
			require.NoError(t, err)
			runner := &vmJournalRunner{effect: func(
				_ context.Context, args []string, opts system.RunCmdOpts,
			) (*system.RunResult, error) {
				// Fixed journal-only, unprivileged and synchronous invocation is the mutation boundary.
				if diff := cmp.Diff([]string{"/usr/sbin/e2fsck", "-p", "-E", "journal_only"}, args[:4]); diff != "" {
					t.Errorf("journal-only tool (-want +got):\n%s", diff)
				}
				assert.False(t, opts.Privileged || opts.Interactive || opts.StartOnly)
				assert.True(t, opts.ReplaceEnv)
				assert.NotContains(t, opts.Env, "E2FSCK_CONFIG")
				writeVMJournalDescriptor(t, args)
				return &system.RunResult{ExitCode: exit}, nil
			}}
			installVMJournalRunner(t, runner)
			require.NoError(t, stage.prepareInspectionSource(context.Background(), imageInspectionVMRootfs, "ext4"))
			assertVMJournalBytes(t, filepath.Join(directory, "rootfs.img"), "replayeded VM journal bytes")
			assertVMJournalBytes(t, source, "unreplayed VM journal bytes")
			after, err := os.Stat(source)
			require.NoError(t, err)
			assert.True(t, os.SameFile(before, after), "journal replay must not replace the extracted source")
			require.NoError(t, stage.close(true))
			_, err = os.Stat(directory)
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestVMInspectionJournalPartialFailureRefreshesIdentityAndCleansCopy(t *testing.T) {
	if runVMJournalIsolated(t) {
		return
	}
	for _, name := range []string{"tool_error", "exit_2", "exit_4", "nil_result", "cancel_after_write"} {
		t.Run(name, func(t *testing.T) {
			stage, source, directory := newVMJournalStage(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			toolErr := errors.New("journal tool failed after writing")
			runner := &vmJournalRunner{effect: func(
				_ context.Context, args []string, _ system.RunCmdOpts,
			) (*system.RunResult, error) {
				writeVMJournalDescriptor(t, args)
				switch name {
				case "tool_error":
					return nil, toolErr
				case "exit_2":
					return &system.RunResult{ExitCode: 2}, nil
				case "exit_4":
					return &system.RunResult{ExitCode: 4}, nil
				case "cancel_after_write":
					cancel()
					return nil, context.Canceled
				default:
					return nil, nil
				}
			}}
			installVMJournalRunner(t, runner)
			err := stage.prepareInspectionSource(ctx, imageInspectionVMRootfs, "ext4")
			require.Error(t, err)
			if name == "cancel_after_write" {
				assert.ErrorIs(t, err, context.Canceled)
			}
			if name == "tool_error" {
				assert.ErrorIs(t, err, toolErr)
			}
			info, statErr := os.Stat(filepath.Join(directory, "rootfs.img"))
			require.NoError(t, statErr)
			identity, statErr := stagedInfoIdentity(info)
			require.NoError(t, statErr)
			if diff := cmp.Diff(identity, stage.rootfsIdentity); diff != "" {
				t.Errorf("partial-write cleanup identity (-want +got):\n%s", diff)
			}
			require.NoError(t, stage.close(true))
			_, statErr = os.Stat(directory)
			assert.ErrorIs(t, statErr, os.ErrNotExist, "failed preparation must not retain an owned disposable copy")
			assertVMJournalBytes(t, source, "unreplayed VM journal bytes")
		})
	}
}

func TestVMInspectionJournalReplacementNeverAdoptedOrDeleted(t *testing.T) {
	if runVMJournalIsolated(t) {
		return
	}
	stage, source, directory := newVMJournalStage(t)
	rootfs := filepath.Join(directory, "rootfs.img")
	retained := filepath.Join(directory, "original.img")
	runner := &vmJournalRunner{effect: func(
		_ context.Context, args []string, _ system.RunCmdOpts,
	) (*system.RunResult, error) {
		require.NoError(t, os.Rename(rootfs, retained))
		require.NoError(t, os.WriteFile(rootfs, []byte("replacement must survive"), 0600))
		writeVMJournalDescriptor(t, args)
		return &system.RunResult{ExitCode: 0}, nil
	}}
	installVMJournalRunner(t, runner)
	require.Error(t, stage.prepareInspectionSource(context.Background(), imageInspectionVMRootfs, "ext4"))
	require.NoError(t, stage.close(true))
	assertVMJournalBytes(t, rootfs, "replacement must survive")
	assertVMJournalBytes(t, retained, "replayeded VM journal bytes")
	assertVMJournalBytes(t, source, "unreplayed VM journal bytes")
}

func TestVMInspectionJournalNamedInspectorCleansPartialToolFailure(t *testing.T) {
	if runVMJournalIsolated(t) {
		return
	}
	stage, source, directory := newVMJournalStage(t)
	require.NoError(t, stage.close(true))
	toolErr := errors.New("journal replay interrupted after writing")
	runner := &vmJournalRunner{effect: func(
		_ context.Context, args []string, _ system.RunCmdOpts,
	) (*system.RunResult, error) {
		writeVMJournalDescriptor(t, args)
		return nil, toolErr
	}}
	installVMJournalRunner(t, runner)
	_, err := inspectVMRootfsStagedImage(context.Background(), source, stage.request.ImageID, "ext4")
	require.Error(t, err)
	assert.ErrorIs(t, err, toolErr)
	_, err = os.Stat(directory)
	assert.ErrorIs(t, err, os.ErrNotExist, "pre-dispatch preparation failure must clean the exact owned stage")
	assertVMJournalBytes(t, source, "unreplayed VM journal bytes")
}

func TestVMInspectionJournalRejectsChangedOwnedCopyBeforeMutation(t *testing.T) {
	if runVMJournalIsolated(t) {
		return
	}
	for _, name := range []string{"mode", "size", "hardlink"} {
		t.Run(name, func(t *testing.T) {
			stage, source, directory := newVMJournalStage(t)
			rootfs := filepath.Join(directory, "rootfs.img")
			switch name {
			case "mode":
				require.NoError(t, os.Chmod(rootfs, 0644))
			case "size":
				require.NoError(t, os.Truncate(rootfs, 5))
			case "hardlink":
				require.NoError(t, os.Link(rootfs, filepath.Join(directory, "alias.img")))
			}
			before, err := os.ReadFile(rootfs)
			require.NoError(t, err)
			runner := &vmJournalRunner{effect: func(
				_ context.Context, args []string, _ system.RunCmdOpts,
			) (*system.RunResult, error) {
				writeVMJournalDescriptor(t, args)
				return &system.RunResult{}, nil
			}}
			installVMJournalRunner(t, runner)
			require.Error(t, stage.prepareInspectionSource(context.Background(), imageInspectionVMRootfs, "ext4"))
			require.NoError(t, stage.close(true))
			assertVMJournalBytes(t, rootfs, string(before))
			assertVMJournalBytes(t, source, "unreplayed VM journal bytes")
			assert.Empty(t, runner.calls, "unsafe copy must not reach the mutation tool")
		})
	}
}

func TestVMInspectionJournalOrdinaryAndNonExt4InputsRemainUnchanged(t *testing.T) {
	if runVMJournalIsolated(t) {
		return
	}
	for _, name := range []string{"ordinary_ext4", "vm_non_ext4"} {
		t.Run(name, func(t *testing.T) {
			stage, source, directory := newVMJournalStage(t)
			runner := &vmJournalRunner{effect: func(
				_ context.Context, args []string, _ system.RunCmdOpts,
			) (*system.RunResult, error) {
				writeVMJournalDescriptor(t, args)
				return &system.RunResult{}, nil
			}}
			installVMJournalRunner(t, runner)
			kind, fsType := imageInspectionOrdinary, "ext4"
			if name == "vm_non_ext4" {
				kind, fsType = imageInspectionVMRootfs, "ext3"
			}
			require.NoError(t, stage.prepareInspectionSource(context.Background(), kind, fsType))
			assertVMJournalBytes(t, filepath.Join(directory, "rootfs.img"), "unreplayed VM journal bytes")
			assertVMJournalBytes(t, source, "unreplayed VM journal bytes")
			assert.Empty(t, runner.calls, "ordinary imports and non-ext4 filesystems must not be journal-replayed")
			require.NoError(t, stage.close(true))
		})
	}
}
