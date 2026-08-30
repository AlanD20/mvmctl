package loopmount

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

func TestRootfsVMProvisionAdapterConvergedReplayUsesClosedReadWriteFlow(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsVMProvisionFixture(t)
	outcome, err := fixture.adapter().ProvisionVM(t.Context(), fixture.base.lease, FilesystemExt4, VMProvisionPlan{
		ResizeBytes: fixture.target,
	})
	require.NoError(t, err)
	assert.Equal(t, vmProvisionEffectStateModified, outcome.state)
	assert.Equal(t, fixture.target, outcome.rootfsBytes)
	assert.Zero(t, outcome.commandsRun)
	assert.Zero(t, fixture.truncateCalls, "a replay admitted at the target must not truncate again")
	assert.False(t, fixture.base.unlocked)
	require.Len(t, fixture.base.mounts, 2)
	if diff := cmp.Diff(testRootfsInspectMountCall{
		source: "/dev/loop3p1", target: rootfsInspectMountPath, filesystem: "ext4", flags: rootfsProvisionMountFlags,
	}, fixture.base.mounts[1], cmp.AllowUnexported(testRootfsInspectMountCall{})); diff != "" {
		t.Errorf("read-write mount mismatch (-want +got):\n%s", diff)
	}
	assertRootfsInspectEventOrder(t, fixture.base.events,
		"syncfs", "close:13", "unmount", "partition-close:41", "loop-clear", "close:11", "close:10", "backing-fsync",
	)
}

// Rationale: First admission at the old size must grow the retained backing descriptor exactly once, then complete
// the interrupted ext filesystem grow through only the fixed offline tools before reporting the requested size.
func TestRootfsVMProvisionAdapterFirstGrowConvergesBackingAndExtFilesystem(t *testing.T) {
	t.Parallel()

	oldBytes := minimumRootfsBytes
	targetBytes := oldBytes * 2
	fixture := newTestRootfsVMProvisionFixture(t)
	fixture.target = targetBytes
	fixture.backingBytes = oldBytes
	fixture.initialFilesystemBytes = oldBytes
	fixture.base.lease.admittedBytes = oldBytes
	fixture.base.lease.metadata.size = oldBytes
	var commands [][]string
	fixture.runTool = func(
		_ context.Context,
		args []string,
		_ system.RunCmdOpts,
	) (*system.RunResult, error) {
		commands = append(commands, slices.Clone(args))
		return &system.RunResult{ExitCode: 0}, nil
	}

	outcome, err := fixture.adapter().ProvisionVM(
		t.Context(),
		fixture.base.lease,
		FilesystemExt4,
		VMProvisionPlan{ResizeBytes: targetBytes},
	)
	require.NoError(t, err)
	assert.Equal(t, vmProvisionEffectStateModified, outcome.state)
	assert.Equal(t, targetBytes, outcome.rootfsBytes)
	assert.Equal(t, uint32(2), outcome.commandsRun)
	assert.Zero(t, outcome.filesWritten)
	assert.Equal(t, 1, fixture.truncateCalls)
	wantCommands := [][]string{
		{rootfsE2fsckPath, "-f", "-p", "/dev/loop3p1"},
		{rootfsResize2fsPath, "/dev/loop3p1"},
	}
	if diff := cmp.Diff(wantCommands, commands); diff != "" {
		t.Errorf("first-grow commands mismatch (-want +got):\n%s", diff)
	}
}

func TestRootfsVMProvisionAdapterClassifiesBackingGrowFaults(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure func(*testRootfsVMProvisionFixture)
		want      vmProvisionEffectState
	}{
		"truncate_failure_is_known_unchanged": {
			configure: func(f *testRootfsVMProvisionFixture) { f.truncateErr = errors.New("truncate") },
			want:      vmProvisionEffectStateUnchanged,
		},
		"post_truncate_sync_failure_is_durability_uncertain": {
			configure: func(f *testRootfsVMProvisionFixture) {
				f.backingFsyncErrors = []error{errors.New("sync grown backing"), nil}
			},
			want: vmProvisionEffectStateModifiedDurabilityUncertain,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			oldBytes := minimumRootfsBytes
			fixture := newTestRootfsVMProvisionFixture(t)
			fixture.target = oldBytes * 2
			fixture.backingBytes = oldBytes
			fixture.initialFilesystemBytes = oldBytes
			fixture.base.lease.admittedBytes = oldBytes
			fixture.base.lease.metadata.size = oldBytes
			tc.configure(fixture)

			outcome, err := fixture.adapter().ProvisionVM(
				t.Context(),
				fixture.base.lease,
				FilesystemExt4,
				VMProvisionPlan{ResizeBytes: fixture.target},
			)
			require.Error(t, err)
			assert.Equal(t, tc.want, outcome.state)
			assert.Equal(t, 1, fixture.truncateCalls)
			assert.NotContains(t, fixture.base.events, "attach")
		})
	}
}

func TestRootfsVMProvisionAdapterClassifiesGrowToolAndRemountFaults(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure       func(*testRootfsVMProvisionFixture)
		wantCommandsRun uint32
	}{
		"filesystem_check_failure_is_unknown": {
			configure: func(f *testRootfsVMProvisionFixture) {
				f.runTool = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
					return &system.RunResult{ExitCode: 4}, nil
				}
			},
		},
		"filesystem_resize_failure_is_unknown": {
			configure: func(f *testRootfsVMProvisionFixture) {
				calls := 0
				f.runTool = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
					calls++
					if calls == 2 {
						return &system.RunResult{ExitCode: 4}, nil
					}
					return &system.RunResult{ExitCode: 0}, nil
				}
			},
			wantCommandsRun: 1,
		},
		"post_grow_remount_failure_is_unknown": {
			configure: func(f *testRootfsVMProvisionFixture) {
				f.runTool = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
					return &system.RunResult{ExitCode: 0}, nil
				}
				f.remountErr = errors.New("remount")
			},
			wantCommandsRun: 2,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			oldBytes := minimumRootfsBytes
			fixture := newTestRootfsVMProvisionFixture(t)
			fixture.target = oldBytes * 2
			fixture.backingBytes = oldBytes
			fixture.initialFilesystemBytes = oldBytes
			fixture.base.lease.admittedBytes = oldBytes
			fixture.base.lease.metadata.size = oldBytes
			tc.configure(fixture)

			outcome, err := fixture.adapter().ProvisionVM(
				t.Context(),
				fixture.base.lease,
				FilesystemExt4,
				VMProvisionPlan{ResizeBytes: fixture.target},
			)
			require.Error(t, err)
			assert.Equal(t, vmProvisionEffectStateOutcomeUnknown, outcome.state)
			assert.Equal(t, tc.wantCommandsRun, outcome.commandsRun)
		})
	}
}

func TestRootfsVMProvisionAdapterClassifiesGrowRunnerContextErrorsAndCleansCapabilities(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cause        error
		cancelCaller bool
	}{
		"compiled_timeout": {cause: context.DeadlineExceeded},
		"caller_cancellation": {
			cause:        context.Canceled,
			cancelCaller: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			cancel := func() {}
			if tc.cancelCaller {
				var cancelContext context.CancelFunc
				ctx, cancelContext = context.WithCancel(ctx)
				cancel = cancelContext
				t.Cleanup(cancelContext)
			}
			oldBytes := minimumRootfsBytes
			fixture := newTestRootfsVMProvisionFixture(t)
			fixture.target = oldBytes * 2
			fixture.backingBytes = oldBytes
			fixture.initialFilesystemBytes = oldBytes
			fixture.base.lease.admittedBytes = oldBytes
			fixture.base.lease.metadata.size = oldBytes
			fixture.runTool = func(
				runCtx context.Context,
				_ []string,
				opts system.RunCmdOpts,
			) (*system.RunResult, error) {
				assert.Equal(t, 5*time.Minute, opts.Timeout)
				cancel()
				if tc.cancelCaller {
					return nil, runCtx.Err()
				}
				return nil, tc.cause
			}

			outcome, err := fixture.adapter().ProvisionVM(
				ctx,
				fixture.base.lease,
				FilesystemExt4,
				VMProvisionPlan{ResizeBytes: fixture.target},
			)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.cause)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
			assert.Equal(t, vmProvisionEffectStateOutcomeUnknown, outcome.state)
			assert.Zero(t, outcome.commandsRun)
			assert.False(t, fixture.base.cleanupSawCancellation)
			assert.True(t, fixture.base.source.state.released)
			assert.True(t, fixture.base.partitions.state.released)
			assert.True(t, fixture.base.loop.state.released)
			assert.Equal(t, 2, fixture.base.snapshotCalls)
			assert.False(t, fixture.base.unlocked)
		})
	}
}

func TestRootfsVMProvisionAdapterClassifiesBoundaryFaults(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure func(*testRootfsVMProvisionFixture)
		want      vmProvisionEffectState
	}{
		"unshare_is_known_unchanged": {
			configure: func(f *testRootfsVMProvisionFixture) { f.base.unshareErr = errors.New("denied") },
			want:      vmProvisionEffectStateUnchanged,
		},
		"private_propagation_is_known_unchanged": {
			configure: func(f *testRootfsVMProvisionFixture) { f.base.privateErr = errors.New("private") },
			want:      vmProvisionEffectStateUnchanged,
		},
		"attach_before_mount_is_known_unchanged": {
			configure: func(f *testRootfsVMProvisionFixture) { f.attachErr = errors.New("attach") },
			want:      vmProvisionEffectStateUnchanged,
		},
		"read_write_mount_has_unknown_outcome": {
			configure: func(f *testRootfsVMProvisionFixture) { f.base.imageMountErr = errors.New("mount") },
			want:      vmProvisionEffectStateOutcomeUnknown,
		},
		"filesystem_sync_is_durability_uncertain": {
			configure: func(f *testRootfsVMProvisionFixture) { f.syncfsErr = errors.New("syncfs") },
			want:      vmProvisionEffectStateModifiedDurabilityUncertain,
		},
		"unmount_is_durability_uncertain": {
			configure: func(f *testRootfsVMProvisionFixture) { f.base.unmountErr = errors.New("unmount") },
			want:      vmProvisionEffectStateModifiedDurabilityUncertain,
		},
		"backing_revalidation_is_durability_uncertain": {
			configure: func(f *testRootfsVMProvisionFixture) { f.finalSizeDelta = 1 },
			want:      vmProvisionEffectStateModifiedDurabilityUncertain,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsVMProvisionFixture(t)
			tc.configure(fixture)
			outcome, err := fixture.adapter().ProvisionVM(
				t.Context(),
				fixture.base.lease,
				FilesystemExt4,
				VMProvisionPlan{ResizeBytes: fixture.target},
			)
			require.Error(t, err)
			assert.Equal(t, tc.want, outcome.state)
			if fixture.base.unshareErr == nil {
				assert.False(t, fixture.base.unlocked)
			}
		})
	}
}

// Rationale: An unshare failure leaves the thread in the original namespace, so it must be returned to the runtime.
func TestRootfsVMProvisionAdapterUnlocksThreadWhenUnshareFails(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsVMProvisionFixture(t)
	fixture.base.unshareErr = errors.New("unshare failed")
	_, err := fixture.adapter().ProvisionVM(
		t.Context(),
		fixture.base.lease,
		FilesystemExt4,
		VMProvisionPlan{ResizeBytes: fixture.target},
	)
	require.Error(t, err)
	assert.True(t, fixture.base.unlocked)
	assertRootfsInspectEventOrder(t, fixture.base.events, "lock-thread", "unlock-thread")
}

func TestRootfsVMProvisionAdapterCancelsAfterThreadLockBeforeUnshare(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	fixture := newTestRootfsVMProvisionFixture(t)
	fixture.base.cancelAfterLock = cancel
	outcome, err := fixture.adapter().ProvisionVM(
		ctx,
		fixture.base.lease,
		FilesystemExt4,
		VMProvisionPlan{ResizeBytes: fixture.target},
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, vmProvisionEffectStateUnchanged, outcome.state)
	assert.True(t, fixture.base.unlocked)
	assert.False(t, fixture.base.unlockSawCancellation)
	assert.False(t, fixture.base.unshareCalled)
	assert.Empty(t, fixture.base.mounts)
	if diff := cmp.Diff([]string{"snapshot", "lock-thread", "unlock-thread"}, fixture.base.events); diff != "" {
		t.Errorf("pre-unshare cancellation events mismatch (-want +got):\n%s", diff)
	}
}

func TestRootfsVMProvisionAdapterCleanupIgnoresCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	fixture := newTestRootfsVMProvisionFixture(t)
	fixture.cancelDuringSync = cancel
	outcome, err := fixture.adapter().ProvisionVM(
		ctx,
		fixture.base.lease,
		FilesystemExt4,
		VMProvisionPlan{ResizeBytes: fixture.target},
	)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, vmProvisionEffectStateModifiedDurabilityUncertain, outcome.state)
	assert.False(t, fixture.base.cleanupSawCancellation)
	assert.True(t, fixture.base.source.state.released)
	assert.True(t, fixture.base.partitions.state.released)
	assert.True(t, fixture.base.loop.state.released)
}

func TestRootfsVMProvisionAdapterRejectsDeferredLegacyBehaviorBeforeEffects(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsVMProvisionFixture(t)
	outcome, err := fixture.adapter().ProvisionVM(
		t.Context(),
		fixture.base.lease,
		FilesystemExt4,
		VMProvisionPlan{InjectCloudInit: true},
	)
	require.Error(t, err)
	assert.ErrorContains(t, err, "unsupported")
	assert.Equal(t, vmProvisionEffectStateUnchanged, outcome.state)
	assert.Empty(t, fixture.base.mounts)
	assert.NotContains(t, fixture.base.events, "lock-thread")
}

func TestRunRootfsGrowToolUsesExactCommandAndClassifiesCompletion(t *testing.T) {
	t.Parallel()

	command := []string{rootfsE2fsckPath, "-f", "-p", "/dev/loop3"}
	tests := map[string]struct {
		result    *system.RunResult
		runErr    error
		wantCause error
		wantErr   bool
		wantState vmProvisionEffectState
	}{
		"accepted_corrected_exit": {
			result: &system.RunResult{ExitCode: 1}, wantState: vmProvisionEffectStateModifiedDurabilityUncertain,
		},
		"rejected_exit_is_unknown": {
			result: &system.RunResult{ExitCode: 4}, wantErr: true, wantState: vmProvisionEffectStateOutcomeUnknown,
		},
		"runner_failure_is_unknown": {
			runErr: errors.New(
				"wait status unavailable",
			),
			wantErr:   true,
			wantState: vmProvisionEffectStateOutcomeUnknown,
		},
		"runner_timeout_is_classified_and_unknown": {
			runErr:    context.DeadlineExceeded,
			wantCause: context.DeadlineExceeded,
			wantErr:   true,
			wantState: vmProvisionEffectStateOutcomeUnknown,
		},
		"runner_cancellation_is_classified_and_unknown": {
			runErr:    context.Canceled,
			wantCause: context.Canceled,
			wantErr:   true,
			wantState: vmProvisionEffectStateOutcomeUnknown,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tracker := &rootfsProvisionTracker{}
			called := false
			deps := rootfsVMProvisionDeps{run: func(
				_ context.Context,
				got []string,
				opts system.RunCmdOpts,
			) (*system.RunResult, error) {
				called = true
				if diff := cmp.Diff(command, got); diff != "" {
					t.Errorf("grow command mismatch (-want +got):\n%s", diff)
				}
				assert.False(t, opts.Check)
				assert.True(t, opts.Capture)
				assert.Equal(t, "/", opts.Cwd)
				assert.Equal(t, 5*time.Minute, opts.Timeout)
				assert.True(t, opts.ReplaceEnv)
				if diff := cmp.Diff(map[string]string{"LANG": "C", "LC_ALL": "C"}, opts.Env); diff != "" {
					t.Errorf("grow environment mismatch (-want +got):\n%s", diff)
				}
				assert.Equal(t, maxRootfsGrowToolStdoutBytes, opts.MaxStdoutBytes)
				assert.Equal(t, maxRootfsGrowToolStderrBytes, opts.MaxStderrBytes)
				return tc.result, tc.runErr
			}}
			err := runRootfsGrowTool(t.Context(), deps, command, []int{0, 1, 2}, tracker)
			assert.Equal(t, tc.wantErr, err != nil)
			if tc.wantCause != nil {
				assert.ErrorIs(t, err, tc.wantCause)
			}
			assert.True(t, called)
			assert.Equal(t, tc.wantState, tracker.state())
		})
	}
}

func TestRunRootfsGrowToolPassesEarlierCallerDeadlineToRunner(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	wantDeadline, ok := ctx.Deadline()
	require.True(t, ok)
	tracker := &rootfsProvisionTracker{}
	deps := rootfsVMProvisionDeps{run: func(
		runCtx context.Context,
		_ []string,
		opts system.RunCmdOpts,
	) (*system.RunResult, error) {
		gotDeadline, hasDeadline := runCtx.Deadline()
		assert.True(t, hasDeadline)
		assert.True(t, gotDeadline.Equal(wantDeadline))
		assert.Equal(t, 5*time.Minute, opts.Timeout)
		return nil, context.DeadlineExceeded
	}}

	err := runRootfsGrowTool(ctx, deps, []string{rootfsResize2fsPath, "/dev/loop3p1"}, []int{0}, tracker)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, vmProvisionEffectStateOutcomeUnknown, tracker.state())
}

func TestConvergeRootfsProvisionGrowUsesOnlyFilesystemSpecificFixedTools(t *testing.T) {
	t.Parallel()

	const target = uint64(256 << 20)
	t.Run("btrfs_online", func(t *testing.T) {
		var probes int
		var gotCommand []string
		tracker := &rootfsProvisionTracker{}
		commandsRun := uint32(0)
		deps := rootfsVMProvisionDeps{
			fstatfs: func(_ context.Context, _ int, stat *unix.Statfs_t) error {
				probes++
				stat.Bsize = 4096
				stat.Blocks = target / 4096
				if probes == 1 {
					stat.Blocks--
				}
				return nil
			},
			run: func(
				_ context.Context,
				args []string,
				_ system.RunCmdOpts,
			) (*system.RunResult, error) {
				gotCommand = slices.Clone(args)
				return &system.RunResult{ExitCode: 0}, nil
			},
		}
		rootFD, mounted, err := convergeRootfsProvisionGrow(
			t.Context(), deps, nil, nil, FilesystemBtrfs, "", target, 13, true, tracker, &commandsRun,
		)
		require.NoError(t, err)
		assert.Equal(t, 13, rootFD)
		assert.True(t, mounted)
		assert.Equal(t, uint32(1), commandsRun)
		if diff := cmp.Diff(
			[]string{rootfsBtrfsPath, "filesystem", "resize", "max", rootfsInspectMountPath},
			gotCommand,
		); diff != "" {
			t.Errorf("btrfs grow command mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("ext_offline", func(t *testing.T) {
		fixture := newTestRootfsVMProvisionFixture(t)
		deps := fixture.adapter().deps
		fixture.base.openAt2Count = 2
		var probes int
		deps.fstatfs = func(_ context.Context, fd int, stat *unix.Statfs_t) error {
			assert.Equal(t, 13, fd)
			probes++
			stat.Type = unix.EXT4_SUPER_MAGIC
			stat.Bsize = 4096
			stat.Blocks = target / 4096
			if probes == 1 {
				stat.Blocks--
			}
			return nil
		}
		var gotCommands [][]string
		deps.run = func(
			_ context.Context,
			args []string,
			_ system.RunCmdOpts,
		) (*system.RunResult, error) {
			gotCommands = append(gotCommands, slices.Clone(args))
			return &system.RunResult{ExitCode: 0}, nil
		}
		tracker := &rootfsProvisionTracker{modified: true}
		commandsRun := uint32(0)
		rootFD, mounted, err := convergeRootfsProvisionGrow(
			t.Context(),
			deps,
			&rootfsInspectMountpoint{parentFD: 10, rootFD: 11},
			fixture.base.source,
			FilesystemExt4,
			"/dev/loop3",
			target,
			13,
			true,
			tracker,
			&commandsRun,
		)
		require.NoError(t, err)
		assert.Equal(t, 13, rootFD)
		assert.True(t, mounted)
		assert.Equal(t, uint32(2), commandsRun)
		want := [][]string{
			{rootfsE2fsckPath, "-f", "-p", "/dev/loop3"},
			{rootfsResize2fsPath, "/dev/loop3"},
		}
		if diff := cmp.Diff(want, gotCommands); diff != "" {
			t.Errorf("ext grow commands mismatch (-want +got):\n%s", diff)
		}
	})
}

func TestGrowRootfsProvisionBackingUsesRetainedDescriptorOnce(t *testing.T) {
	t.Parallel()

	const oldBytes = uint64(128 << 20)
	const targetBytes = uint64(256 << 20)
	lease := &rootfsLease{rootfsFD: 27, access: rootfsReadWrite, admittedBytes: oldBytes}
	tracker := &rootfsProvisionTracker{}
	var calls []string
	err := growRootfsProvisionBacking(t.Context(), rootfsVMProvisionDeps{
		ftruncate: func(_ context.Context, fd int, size int64) error {
			assert.Equal(t, 27, fd)
			assert.Equal(t, int64(targetBytes), size)
			calls = append(calls, "truncate")
			return nil
		},
		fsync: func(_ context.Context, fd int) error {
			assert.Equal(t, 27, fd)
			calls = append(calls, "fsync")
			return nil
		},
	}, lease, targetBytes, tracker)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"truncate", "fsync"}, calls); diff != "" {
		t.Errorf("backing grow calls mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, vmProvisionEffectStateModifiedDurabilityUncertain, tracker.state())
}

func TestRootfsProvisionFixedServiceContentUsesAProtectedTokenFile(t *testing.T) {
	t.Parallel()

	for name, content := range map[string][]byte{
		"systemd": rootfsAgentSystemdService(1024),
		"openrc":  rootfsAgentOpenRCService(1024),
	} {
		t.Run(name, func(t *testing.T) {
			assert.Contains(t, string(content), "-port 1024")
			assert.Contains(t, string(content), "-token-file "+rootfsAgentTokenPath)
		})
	}
	assert.Contains(t, string(rootfsAgentSystemdService(1024)), "WantedBy=sysinit.target")
	assert.Equal(t, infra.GuestAgentTokenPath, rootfsAgentTokenPath)
	assert.Contains(t, string(rootfsSSHDConfig("root")), "PermitRootLogin prohibit-password")
	assert.Contains(t, string(rootfsSSHDConfig("alice")), "AllowUsers alice")
}

// Rationale: A systemd guest must receive only its matching service and one fixed wants link, while authentication
// state remains beneath a persistent root-only directory rather than the boot-time /run mount.
func TestApplyRootfsProvisionPlanEnablesAgentForExistingSystemdLayout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "usr/lib/systemd"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/systemd"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "usr/lib/systemd/systemd"), []byte("systemd"), 0755))
	tree := newTestUnprivilegedRootfsGuestTree(t, root)
	token := "5c9a355a-eafa-48e5-9305-76b95e9967ca"
	deps := rootfsVMProvisionDeps{
		agentBinary: func(context.Context) ([]byte, error) { return []byte("agent-binary"), nil },
	}
	plan := VMProvisionPlan{Agent: &AgentSetup{Port: 1024, Token: token}}

	filesWritten, err := applyRootfsProvisionPlan(t.Context(), tree, deps, plan)
	require.NoError(t, err)
	assert.Equal(t, uint32(4), filesWritten)
	assertRootfsProvisionedFile(t, root, "/usr/bin/mvm-agent", []byte("agent-binary"), 0755)
	assertRootfsProvisionedFile(t, root, infra.GuestAgentTokenPath, []byte(token+"\n"), 0600)
	assertRootfsProvisionedFile(
		t,
		root,
		"/etc/systemd/system/mvm-agent.service",
		rootfsAgentSystemdService(1024),
		0644,
	)
	assertRootfsProvisionedSymlink(
		t,
		root,
		"/etc/systemd/system/sysinit.target.wants/mvm-agent.service",
		"../mvm-agent.service",
	)
	assert.NoDirExists(t, filepath.Join(root, "etc/init.d"))
	info, err := os.Stat(filepath.Join(root, strings.TrimPrefix(infra.GuestAgentConfigDirectory, "/")))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0700), info.Mode().Perm())

	tree.tracker = &rootfsProvisionTracker{}
	replayedFiles, err := applyRootfsProvisionPlan(t.Context(), tree, deps, plan)
	require.NoError(t, err)
	assert.Zero(t, replayedFiles)
	assert.Equal(t, vmProvisionEffectStateUnchanged, tree.tracker.state())
}

// Rationale: An OpenRC guest must not require or create /etc/systemd and must receive only its fixed default-runlevel
// link after existing OpenRC layout evidence is pinned.
func TestApplyRootfsProvisionPlanEnablesAgentForExistingOpenRCLayout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "usr"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sbin"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/init.d"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/runlevels/default"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "sbin/openrc"), []byte("openrc"), 0755))
	tree := newTestUnprivilegedRootfsGuestTree(t, root)
	token := "5c9a355a-eafa-48e5-9305-76b95e9967ca"

	filesWritten, err := applyRootfsProvisionPlan(t.Context(), tree, rootfsVMProvisionDeps{
		agentBinary: func(context.Context) ([]byte, error) { return []byte("agent-binary"), nil },
	}, VMProvisionPlan{Agent: &AgentSetup{Port: 1024, Token: token}})
	require.NoError(t, err)
	assert.Equal(t, uint32(4), filesWritten)
	assertRootfsProvisionedFile(t, root, infra.GuestAgentTokenPath, []byte(token+"\n"), 0600)
	assertRootfsProvisionedFile(t, root, "/etc/init.d/mvm-agent", rootfsAgentOpenRCService(1024), 0755)
	assertRootfsProvisionedSymlink(t, root, "/etc/runlevels/default/mvm-agent", "/etc/init.d/mvm-agent")
	assert.NoDirExists(t, filepath.Join(root, "etc/systemd"))
}

// Rationale: Missing or ambiguous fixed init evidence must reject agent provisioning before any executable, token,
// service, or enablement link is published.
func TestApplyRootfsProvisionPlanRejectsUnprovedAgentInitLayoutBeforeWrites(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testing.T, string){
		"missing": func(*testing.T, string) {},
		"ambiguous": func(t *testing.T, root string) {
			require.NoError(t, os.MkdirAll(filepath.Join(root, "usr/lib/systemd"), 0755))
			require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/systemd"), 0755))
			require.NoError(t, os.MkdirAll(filepath.Join(root, "sbin"), 0755))
			require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/init.d"), 0755))
			require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/runlevels/default"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "usr/lib/systemd/systemd"), []byte("systemd"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "sbin/openrc"), []byte("openrc"), 0755))
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			setup(t, root)
			tree := newTestUnprivilegedRootfsGuestTree(t, root)
			filesWritten, err := applyRootfsProvisionPlan(t.Context(), tree, rootfsVMProvisionDeps{
				agentBinary: func(context.Context) ([]byte, error) { return []byte("agent-binary"), nil },
			}, VMProvisionPlan{Agent: &AgentSetup{
				Port: 1024, Token: "5c9a355a-eafa-48e5-9305-76b95e9967ca",
			}})
			require.Error(t, err)
			assert.ErrorContains(t, err, "init layout")
			assert.Zero(t, filesWritten)
			assert.NoFileExists(t, filepath.Join(root, "usr/bin/mvm-agent"))
			assert.NoFileExists(t, filepath.Join(root, strings.TrimPrefix(infra.GuestAgentTokenPath, "/")))
		})
	}
}

// Rationale: One complete plan exercises the adapter's ordered content contract across account-scoped SSH, sudo,
// persistent agent authentication, fixed boot enablement, and the bounded hosts and fstab rewrites.
func TestApplyRootfsProvisionPlanPublishesEverySupportedContentEffect(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, directory := range []string{
		"etc", "etc/ssh", "home/alice", "usr/bin", "usr/lib/systemd", "etc/systemd",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, directory), 0755))
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "usr/lib/systemd/systemd"),
		[]byte("systemd"),
		0755,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "etc/passwd"),
		[]byte("root:x:0:0:root:/root:/bin/sh\nalice:x:1000:1000:Alice:/home/alice:/bin/sh\n"),
		0644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "etc/hosts"),
		[]byte("127.0.0.1\tlocalhost\n127.0.1.1\told-name\n"),
		0644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "etc/fstab"),
		[]byte("/dev/vda / ext4 defaults 0 1\n/dev/vda1 /boot ext4 defaults 0 2\n"),
		0644,
	))
	hostname := "vm-a"
	dns := "192.0.2.53"
	user := "alice"
	token := "5c9a355a-eafa-48e5-9305-76b95e9967ca"
	keys := []string{"ssh-ed25519 AAAAtest-one", "ssh-ed25519 AAAAtest-two"}
	plan := VMProvisionPlan{
		Hostname: &hostname,
		DNS:      &dns,
		SSH:      &SSHSetup{User: user, PublicKeys: keys},
		SudoUser: &user,
		FixFstab: true,
		Agent:    &AgentSetup{Port: 1024, Token: token},
	}
	tree := newTestUnprivilegedRootfsGuestTree(t, root)
	filesWritten, err := applyRootfsProvisionPlan(t.Context(), tree, rootfsVMProvisionDeps{
		agentBinary: func(context.Context) ([]byte, error) { return []byte("agent-binary"), nil },
	}, plan)
	require.NoError(t, err)
	assert.Equal(t, uint32(11), filesWritten)

	expectedFiles := []struct {
		path    string
		content []byte
		mode    os.FileMode
	}{
		{rootfsHostnamePath, []byte("vm-a\n"), 0644},
		{rootfsHostsPath, []byte("127.0.0.1\tlocalhost\n127.0.1.1\tvm-a\n"), 0644},
		{rootfsDNSPath, []byte("nameserver 192.0.2.53\n"), 0644},
		{"/home/alice/.ssh/authorized_keys", []byte(strings.Join(keys, "\n") + "\n"), 0600},
		{rootfsSSHConfigPath, rootfsSSHDConfig(user), 0644},
		{rootfsSudoPolicyPath, []byte("alice ALL=(ALL) NOPASSWD: ALL\n"), 0440},
		{rootfsAgentBinaryPath, []byte("agent-binary"), 0755},
		{rootfsAgentTokenPath, []byte(token + "\n"), 0600},
		{rootfsAgentSystemdPath, rootfsAgentSystemdService(1024), 0644},
		{
			rootfsFstabPath,
			[]byte("/dev/vda / ext4 noatime,defaults 0 1\n# mvmctl: /dev/vda1 /boot ext4 defaults 0 2\n"),
			0644,
		},
	}
	for _, expected := range expectedFiles {
		assertRootfsProvisionedFile(t, root, expected.path, expected.content, expected.mode)
	}
	assertRootfsProvisionedSymlink(t, root, rootfsAgentSystemdWantsLink, rootfsAgentSystemdWantsTarget)
}

func TestRootfsGuestTreeAtomicallyReplacesAndReplaysWithoutFollowingSymlinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "etc"), 0755))
	victim := filepath.Join(root, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("outside\n"), 0600))
	require.NoError(t, os.Symlink(victim, filepath.Join(root, "etc", "hostname")))
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Close(rootFD)) })

	var sequence atomic.Uint32
	tracker := &rootfsProvisionTracker{}
	tree := rootfsGuestTree{rootFD: rootFD, tracker: tracker, deps: testSystemRootfsGuestTreeDeps(&sequence)}
	file := rootfsProvisionFile{
		path: "/etc/hostname", content: []byte("vm-a\n"), mode: 0640,
		uid: uint32(os.Getuid()), gid: uint32(os.Getgid()),
	}
	changed, err := tree.replaceFile(t.Context(), file)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, vmProvisionEffectStateModifiedDurabilityUncertain, tracker.state())
	got, err := os.ReadFile(filepath.Join(root, "etc", "hostname"))
	require.NoError(t, err)
	if diff := cmp.Diff(file.content, got); diff != "" {
		t.Errorf("published guest file mismatch (-want +got):\n%s", diff)
	}
	victimContent, err := os.ReadFile(victim)
	require.NoError(t, err)
	if diff := cmp.Diff([]byte("outside\n"), victimContent); diff != "" {
		t.Errorf("symlink victim content mismatch (-want +got):\n%s", diff)
	}

	replayTracker := &rootfsProvisionTracker{}
	tree.tracker = replayTracker
	changed, err = tree.replaceFile(t.Context(), file)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, vmProvisionEffectStateUnchanged, replayTracker.state())

	require.NoError(t, os.Symlink(root, filepath.Join(root, "etc", "escape")))
	_, err = tree.replaceFile(t.Context(), rootfsProvisionFile{
		path: "/etc/escape/file", content: []byte("blocked"), mode: 0600,
		uid: uint32(os.Getuid()), gid: uint32(os.Getgid()),
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "parent")
}

// Rationale: Failure before atomic rename leaves no published file or temporary artifact, but creating and writing
// the temporary inode still makes the durability outcome uncertain.
func TestRootfsGuestTreePublicationFailureCleansTemporaryFileAndReportsUncertainty(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "etc"), 0755))
	tree := newTestUnprivilegedRootfsGuestTree(t, root)
	tree.deps.renameAt = func(context.Context, int, string, int, string) error {
		return errors.New("publish fault")
	}

	changed, err := tree.replaceFile(t.Context(), rootfsProvisionFile{
		path: "/etc/hostname", content: []byte("vm-a\n"), mode: 0644,
	})
	require.Error(t, err)
	assert.True(t, changed)
	assert.Equal(t, vmProvisionEffectStateModifiedDurabilityUncertain, tree.tracker.state())
	assert.NoFileExists(t, filepath.Join(root, "etc/hostname"))
	entries, readErr := os.ReadDir(filepath.Join(root, "etc"))
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

// Rationale: Parent-directory fsync failure occurs after publication, so the target payload is observable while the
// tracker correctly refuses to claim durable completion.
func TestRootfsGuestTreeParentSyncFailureRetainsPublishedFileAndReportsUncertainty(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "etc"), 0755))
	tree := newTestUnprivilegedRootfsGuestTree(t, root)
	systemFsync := tree.deps.fsync
	fsyncCalls := 0
	tree.deps.fsync = func(ctx context.Context, fd int) error {
		fsyncCalls++
		if fsyncCalls == 2 {
			return errors.New("parent sync fault")
		}
		return systemFsync(ctx, fd)
	}

	changed, err := tree.replaceFile(t.Context(), rootfsProvisionFile{
		path: "/etc/hostname", content: []byte("vm-a\n"), mode: 0644,
	})
	require.Error(t, err)
	assert.True(t, changed)
	assert.Equal(t, vmProvisionEffectStateModifiedDurabilityUncertain, tree.tracker.state())
	assertRootfsProvisionedFile(t, root, "/etc/hostname", []byte("vm-a\n"), 0644)
}

func TestRootfsGuestTreeReplaceFixedSymlinkReplacesWrongTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	linkPath := filepath.Join(root, strings.TrimPrefix(rootfsAgentSystemdWantsLink, "/"))
	require.NoError(t, os.MkdirAll(filepath.Dir(linkPath), 0755))
	require.NoError(t, os.Symlink("../wrong-agent.service", linkPath))
	tree := newTestUnprivilegedRootfsGuestTree(t, root)

	changed, err := tree.replaceFixedSymlink(
		t.Context(),
		rootfsAgentSystemdWantsLink,
		rootfsAgentSystemdWantsTarget,
	)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, vmProvisionEffectStateModifiedDurabilityUncertain, tree.tracker.state())
	assertRootfsProvisionedSymlink(t, root, rootfsAgentSystemdWantsLink, rootfsAgentSystemdWantsTarget)
	assertNoRootfsProvisionTemporaryLeaves(t, filepath.Dir(linkPath))
}

// Rationale: Replacement authority covers an existing one-link symlink only; a regular file or a multiply linked
// symlink could carry authority beyond the fixed leaf and must remain untouched.
func TestRootfsGuestTreeReplaceFixedSymlinkRejectsUnsafeExistingLeaf(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		setup  func(*testing.T, string)
		verify func(*testing.T, string)
	}{
		"regular_file": {
			setup: func(t *testing.T, linkPath string) {
				require.NoError(t, os.WriteFile(linkPath, []byte("do not replace"), 0644))
			},
			verify: func(t *testing.T, linkPath string) {
				content, err := os.ReadFile(linkPath)
				require.NoError(t, err)
				if diff := cmp.Diff([]byte("do not replace"), content); diff != "" {
					t.Errorf("rejected regular leaf changed (-want +got):\n%s", diff)
				}
			},
		},
		"hardlinked_symlink": {
			setup: func(t *testing.T, linkPath string) {
				require.NoError(t, os.Symlink("../wrong-agent.service", linkPath))
				require.NoError(t, os.Link(linkPath, linkPath+".alias"))
			},
			verify: func(t *testing.T, linkPath string) {
				for _, path := range []string{linkPath, linkPath + ".alias"} {
					target, err := os.Readlink(path)
					require.NoError(t, err)
					assert.Equal(t, "../wrong-agent.service", target)
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			linkPath := filepath.Join(root, strings.TrimPrefix(rootfsAgentSystemdWantsLink, "/"))
			require.NoError(t, os.MkdirAll(filepath.Dir(linkPath), 0755))
			tc.setup(t, linkPath)
			tree := newTestUnprivilegedRootfsGuestTree(t, root)

			changed, err := tree.replaceFixedSymlink(
				t.Context(),
				rootfsAgentSystemdWantsLink,
				rootfsAgentSystemdWantsTarget,
			)
			require.Error(t, err)
			assert.ErrorContains(t, err, "unsafe type")
			assert.False(t, changed)
			assert.Equal(t, vmProvisionEffectStateUnchanged, tree.tracker.state())
			tc.verify(t, linkPath)
			assertNoRootfsProvisionTemporaryLeaves(t, filepath.Dir(linkPath))
		})
	}
}

// Rationale: Each publication boundary reports whether an inode may have changed and removes its deterministic
// temporary leaf, including the post-rename directory-sync boundary where the final link is already observable.
func TestRootfsGuestTreeReplaceFixedSymlinkClassifiesPublicationFaults(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		inject        func(*rootfsGuestTree)
		wantChanged   bool
		wantState     vmProvisionEffectState
		wantPublished bool
		wantError     string
	}{
		"symlink_creation": {
			inject: func(tree *rootfsGuestTree) {
				tree.deps.symlinkAt = func(context.Context, string, int, string) error {
					return errors.New("symlink fault")
				}
			},
			wantState: vmProvisionEffectStateUnchanged,
			wantError: "create managed guest agent temporary link",
		},
		"temporary_readback": {
			inject: func(tree *rootfsGuestTree) {
				tree.deps.readlinkAt = func(context.Context, int, string, []byte) (int, error) {
					return 0, errors.New("readlink fault")
				}
			},
			wantChanged: true,
			wantState:   vmProvisionEffectStateModifiedDurabilityUncertain,
			wantError:   "read managed guest agent enablement link",
		},
		"atomic_rename": {
			inject: func(tree *rootfsGuestTree) {
				tree.deps.renameAt = func(context.Context, int, string, int, string) error {
					return errors.New("rename fault")
				}
			},
			wantChanged: true,
			wantState:   vmProvisionEffectStateModifiedDurabilityUncertain,
			wantError:   "publish managed guest agent enablement link",
		},
		"parent_fsync": {
			inject: func(tree *rootfsGuestTree) {
				tree.deps.fsync = func(context.Context, int) error { return errors.New("fsync fault") }
			},
			wantChanged:   true,
			wantState:     vmProvisionEffectStateModifiedDurabilityUncertain,
			wantPublished: true,
			wantError:     "sync managed guest agent link parent",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			linkPath := filepath.Join(root, strings.TrimPrefix(rootfsAgentSystemdWantsLink, "/"))
			require.NoError(t, os.MkdirAll(filepath.Dir(linkPath), 0755))
			tree := newTestUnprivilegedRootfsGuestTree(t, root)
			tc.inject(&tree)

			changed, err := tree.replaceFixedSymlink(
				t.Context(),
				rootfsAgentSystemdWantsLink,
				rootfsAgentSystemdWantsTarget,
			)
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantError)
			assert.Equal(t, tc.wantChanged, changed)
			assert.Equal(t, tc.wantState, tree.tracker.state())
			assertNoRootfsProvisionTemporaryLeaves(t, filepath.Dir(linkPath))
			if tc.wantPublished {
				assertRootfsProvisionedSymlink(
					t,
					root,
					rootfsAgentSystemdWantsLink,
					rootfsAgentSystemdWantsTarget,
				)
				return
			}
			_, statErr := os.Lstat(linkPath)
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

// Rationale: Once an ancestor close is attempted its numeric descriptor is consumed, while the newly opened child
// remains owned and must still be closed exactly once on the error path.
func TestRootfsGuestTreeEnsureDirectoryTransfersChildOwnershipBeforeAncestorClose(t *testing.T) {
	t.Parallel()

	opened := []int{10, 11}
	var closes []int
	tree := rootfsGuestTree{
		rootFD:  1,
		tracker: &rootfsProvisionTracker{},
		deps: rootfsGuestTreeDeps{
			openAt2: func(context.Context, int, string, *unix.OpenHow) (int, error) {
				fd := opened[0]
				opened = opened[1:]
				return fd, nil
			},
			close: func(_ context.Context, fd int) error {
				closes = append(closes, fd)
				if fd == 10 {
					return errors.New("ambiguous ancestor close")
				}
				return nil
			},
		},
	}

	_, err := tree.ensureDirectory(t.Context(), "/etc/mvmctl", 0700, 0, 0)
	require.Error(t, err)
	if diff := cmp.Diff([]int{10, 11}, closes); diff != "" {
		t.Errorf("closed descriptors mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: Parent traversal has the same ownership transfer boundary as directory creation and must not leak the
// newly opened child or retry an ambiguous close against a descriptor number the kernel may already have reused.
func TestRootfsGuestTreeOpenParentTransfersChildOwnershipBeforeAncestorClose(t *testing.T) {
	t.Parallel()

	opened := []int{10, 11}
	var closes []int
	tree := rootfsGuestTree{
		rootFD: 1,
		deps: rootfsGuestTreeDeps{
			openAt2: func(context.Context, int, string, *unix.OpenHow) (int, error) {
				fd := opened[0]
				opened = opened[1:]
				return fd, nil
			},
			close: func(_ context.Context, fd int) error {
				closes = append(closes, fd)
				if fd == 10 {
					return errors.New("ambiguous ancestor close")
				}
				return nil
			},
		},
	}

	_, _, err := tree.openParent(t.Context(), "/etc/systemd/system/mvm-agent.service")
	require.Error(t, err)
	if diff := cmp.Diff([]int{10, 11}, closes); diff != "" {
		t.Errorf("closed descriptors mismatch (-want +got):\n%s", diff)
	}
}

// newTestUnprivilegedRootfsGuestTree uses real descriptor-relative operations while simulating successful root
// ownership changes by inode, allowing the privileged write contract to remain hermetic under an unprivileged test UID.
func newTestUnprivilegedRootfsGuestTree(t *testing.T, root string) rootfsGuestTree {
	t.Helper()

	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, unix.Close(rootFD)) })
	var sequence atomic.Uint32
	deps := testSystemRootfsGuestTreeDeps(&sequence)
	owners := make(map[uint64][2]uint32)
	deps.fchown = func(_ context.Context, fd, uid, gid int) error {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return err
		}
		owners[stat.Ino] = [2]uint32{uint32(uid), uint32(gid)}
		return nil
	}
	deps.fstat = func(_ context.Context, fd int, stat *unix.Stat_t) error {
		if err := unix.Fstat(fd, stat); err != nil {
			return err
		}
		if owner, ok := owners[stat.Ino]; ok {
			stat.Uid = owner[0]
			stat.Gid = owner[1]
		}
		return nil
	}
	return rootfsGuestTree{deps: deps, rootFD: rootFD, tracker: &rootfsProvisionTracker{}}
}

// assertRootfsProvisionedFile verifies the durable payload and mode through the mounted-tree test filesystem.
func assertRootfsProvisionedFile(t *testing.T, root, path string, want []byte, mode os.FileMode) {
	t.Helper()

	fullPath := filepath.Join(root, strings.TrimPrefix(path, "/"))
	got, err := os.ReadFile(fullPath)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("provisioned file content mismatch (-want +got):\n%s", diff)
	}
	info, err := os.Stat(fullPath)
	require.NoError(t, err)
	assert.Equal(t, mode, info.Mode().Perm())
}

// assertRootfsProvisionedSymlink verifies one exact boot-enablement link without following it.
func assertRootfsProvisionedSymlink(t *testing.T, root, path, wantTarget string) {
	t.Helper()

	fullPath := filepath.Join(root, strings.TrimPrefix(path, "/"))
	info, err := os.Lstat(fullPath)
	require.NoError(t, err)
	assert.Equal(t, os.ModeSymlink, info.Mode().Type())
	target, err := os.Readlink(fullPath)
	require.NoError(t, err)
	assert.Equal(t, wantTarget, target)
}

// assertNoRootfsProvisionTemporaryLeaves verifies cleanup without assuming anything about the final fixed leaf.
func assertNoRootfsProvisionTemporaryLeaves(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.False(t, strings.HasPrefix(entry.Name(), ".mvmctl-provision-"), entry.Name())
	}
}

// testSystemRootfsGuestTreeDeps returns real descriptor-relative guest-tree effects with deterministic temp names.
func testSystemRootfsGuestTreeDeps(sequence *atomic.Uint32) rootfsGuestTreeDeps {
	return rootfsGuestTreeDeps{
		openAt2: func(_ context.Context, fd int, path string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(fd, path, how)
		},
		mkdirAt: func(_ context.Context, fd int, path string, mode uint32) error {
			return unix.Mkdirat(fd, path, mode)
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error { return unix.Fstat(fd, stat) },
		fstatAt: func(_ context.Context, fd int, path string, stat *unix.Stat_t, flags int) error {
			return unix.Fstatat(fd, path, stat, flags)
		},
		pread: func(_ context.Context, fd int, data []byte, offset int64) (int, error) {
			count, err := unix.Pread(fd, data, offset)
			if count == 0 && err == nil {
				return 0, io.EOF
			}
			return count, err
		},
		pwrite: func(_ context.Context, fd int, data []byte, offset int64) (int, error) {
			return unix.Pwrite(fd, data, offset)
		},
		fchown: func(_ context.Context, fd, uid, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod: func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		fsync:  func(_ context.Context, fd int) error { return unix.Fsync(fd) },
		renameAt: func(_ context.Context, oldFD int, oldName string, newFD int, newName string) error {
			return unix.Renameat(oldFD, oldName, newFD, newName)
		},
		symlinkAt: func(_ context.Context, target string, fd int, name string) error {
			return unix.Symlinkat(target, fd, name)
		},
		readlinkAt: func(_ context.Context, fd int, path string, buffer []byte) (int, error) {
			return unix.Readlinkat(fd, path, buffer)
		},
		unlinkAt: func(_ context.Context, fd int, name string, flags int) error {
			return unix.Unlinkat(fd, name, flags)
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
		tempName: func(context.Context) (string, error) {
			suffix := rootfsTestDecimal(int(sequence.Add(1)))
			return ".mvmctl-provision-" + strings.Repeat("0", 32-len(suffix)) + suffix, nil
		},
	}
}

type testRootfsVMProvisionFixture struct {
	t                      *testing.T
	base                   *testRootfsImageInspectFixture
	target                 uint64
	backingBytes           uint64
	initialFilesystemBytes uint64
	filesystemStatCalls    int
	truncateCalls          int
	truncateErr            error
	backingFsyncCalls      int
	backingFsyncErrors     []error
	rootfsMountCalls       int
	remountErr             error
	runTool                func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error)
	attachErr              error
	syncfsErr              error
	finalSizeDelta         uint64
	cancelDuringSync       context.CancelFunc
}

// newTestRootfsVMProvisionFixture constructs a complete inert provisioning effect with observable cleanup state.
func newTestRootfsVMProvisionFixture(t *testing.T) *testRootfsVMProvisionFixture {
	t.Helper()
	fixture := &testRootfsVMProvisionFixture{
		t:                      t,
		base:                   newTestRootfsImageInspectFixture(t),
		target:                 minimumRootfsBytes,
		backingBytes:           minimumRootfsBytes,
		initialFilesystemBytes: minimumRootfsBytes,
	}
	fixture.base.lease.access = rootfsReadWrite
	fixture.base.lease.admittedBytes = fixture.target
	fixture.base.lease.metadata.size = fixture.target
	fixture.base.loop.state.access = rootfsReadWrite
	return fixture
}

func (fixture *testRootfsVMProvisionFixture) adapter() *rootfsVMProvisionAdapter {
	baseDeps := fixture.base.deps()
	deps := rootfsVMProvisionDeps{
		lockOSThread: baseDeps.lockOSThread, unlockOSThread: baseDeps.unlockOSThread,
		unshare: baseDeps.unshare,
		mount: func(ctx context.Context, source, target, filesystem string, flags uintptr, data string) error {
			if target == rootfsInspectMountPath {
				fixture.rootfsMountCalls++
				if fixture.rootfsMountCalls > 1 && fixture.remountErr != nil {
					return fixture.remountErr
				}
			}
			return baseDeps.mount(ctx, source, target, filesystem, flags, data)
		},
		unmount: baseDeps.unmount, open: baseDeps.open,
		openAt2: func(ctx context.Context, fd int, path string, how *unix.OpenHow) (int, error) {
			openedFD, err := baseDeps.openAt2(ctx, fd, path, how)
			if err != nil && fd == 10 && path == rootfsInspectMountName &&
				fixture.rootfsMountCalls > 1 && how.Flags == rootfsGuestDirectoryFlags &&
				how.Resolve == rootfsInspectResolveFlags {
				return 13, nil
			}
			return openedFD, err
		},
		mkdirAt: baseDeps.mkdirAt, fstat: baseDeps.fstat,
		fstatAt: func(context.Context, int, string, *unix.Stat_t, int) error { return unix.ENOENT },
		fstatfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error {
			assert.Equal(fixture.t, 13, fd)
			fixture.filesystemStatCalls++
			capacity := fixture.target
			if fixture.filesystemStatCalls <= 2 {
				capacity = fixture.initialFilesystemBytes
			}
			stat.Type = unix.EXT4_SUPER_MAGIC
			stat.Bsize = 4096
			stat.Blocks = capacity / 4096
			return nil
		},
		statx: func(ctx context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			fixture.base.snapshotCalls++
			if fixture.base.snapshotCalls > 1 {
				fixture.base.recordCleanupContext(ctx)
			}
			assert.Equal(fixture.t, 30, fd)
			assert.Equal(fixture.t, rootfsStatxFlags, flags)
			assert.Equal(fixture.t, rootfsStatxMask, mask)
			*stat = unix.Statx_t{
				Mask: rootfsStatxRequiredFile, Dev_major: 8, Dev_minor: 1, Ino: 99,
				Mode: unix.S_IFREG | 0600, Uid: 1000, Gid: 1000, Nlink: 1,
				Size: fixture.backingBytes,
			}
			if fixture.base.snapshotCalls > 1 {
				stat.Size += fixture.finalSizeDelta
			}
			fixture.base.events = append(fixture.base.events, "snapshot")
			return nil
		},
		pread:  func(context.Context, int, []byte, int64) (int, error) { return 0, io.EOF },
		pwrite: func(context.Context, int, []byte, int64) (int, error) { return 0, errors.New("unexpected write") },
		ftruncate: func(_ context.Context, _ int, size int64) error {
			fixture.truncateCalls++
			if fixture.truncateErr != nil {
				return fixture.truncateErr
			}
			fixture.backingBytes = uint64(size)
			return nil
		},
		fchown: func(context.Context, int, int, int) error { return nil },
		fchmod: func(context.Context, int, uint32) error { return nil },
		fsync: func(ctx context.Context, fd int) error {
			fixture.base.recordCleanupContext(ctx)
			fixture.base.events = append(fixture.base.events, "backing-fsync")
			call := fixture.backingFsyncCalls
			fixture.backingFsyncCalls++
			if call < len(fixture.backingFsyncErrors) {
				return fixture.backingFsyncErrors[call]
			}
			return nil
		},
		syncfs: func(ctx context.Context, fd int) error {
			assert.Equal(fixture.t, 13, fd)
			fixture.base.events = append(fixture.base.events, "syncfs")
			if fixture.cancelDuringSync != nil {
				fixture.cancelDuringSync()
				fixture.cancelDuringSync = nil
				return ctx.Err()
			}
			return fixture.syncfsErr
		},
		renameAt:    func(context.Context, int, string, int, string) error { return nil },
		symlinkAt:   func(context.Context, string, int, string) error { return nil },
		readlinkAt:  func(context.Context, int, string, []byte) (int, error) { return 0, nil },
		unlinkAt:    func(context.Context, int, string, int) error { return nil },
		close:       baseDeps.close,
		deviceBytes: func(context.Context, int) (uint64, error) { return fixture.target, nil },
		tempName: func(context.Context) (string, error) {
			return ".mvmctl-provision-00000000000000000000000000000001", nil
		},
		run: func(ctx context.Context, args []string, opts system.RunCmdOpts) (*system.RunResult, error) {
			if fixture.runTool != nil {
				return fixture.runTool(ctx, args, opts)
			}
			return nil, errors.New("unexpected grow tool")
		},
		agentBinary: func(context.Context) ([]byte, error) { return []byte("agent"), nil },
		attachLoop: func(context.Context, *rootfsLease) (*rootfsLoopDevice, error) {
			fixture.base.events = append(fixture.base.events, "attach")
			if fixture.attachErr != nil {
				return nil, fixture.attachErr
			}
			return fixture.base.loop, nil
		},
		discoverPartitions: baseDeps.discoverPartitions,
		probeFilesystem:    baseDeps.probeFilesystem,
	}
	return &rootfsVMProvisionAdapter{deps: deps}
}
