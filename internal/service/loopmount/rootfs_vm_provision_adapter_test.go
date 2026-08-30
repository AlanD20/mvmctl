package loopmount

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
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
	assert.Equal(t, testRootfsInspectMountCall{
		source: "/dev/loop3p1", target: rootfsInspectMountPath, filesystem: "ext4", flags: rootfsProvisionMountFlags,
	}, fixture.base.mounts[1])
	assertRootfsInspectEventOrder(t, fixture.base.events,
		"syncfs", "close:13", "unmount", "partition-close:41", "loop-clear", "close:11", "close:10", "backing-fsync",
	)
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
				assert.True(t, opts.ReplaceEnv)
				assert.Equal(t, map[string]string{"LANG": "C", "LC_ALL": "C"}, opts.Env)
				assert.Equal(t, maxRootfsGrowToolStdoutBytes, opts.MaxStdoutBytes)
				assert.Equal(t, maxRootfsGrowToolStderrBytes, opts.MaxStderrBytes)
				return tc.result, tc.runErr
			}}
			err := runRootfsGrowTool(t.Context(), deps, command, []int{0, 1, 2}, tracker)
			assert.Equal(t, tc.wantErr, err != nil)
			assert.True(t, called)
			assert.Equal(t, tc.wantState, tracker.state())
		})
	}
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
		assert.Equal(t, []string{rootfsBtrfsPath, "filesystem", "resize", "max", rootfsInspectMountPath}, gotCommand)
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
	assert.Equal(t, []string{"truncate", "fsync"}, calls)
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
	assert.Equal(t, "/run/mvm-agent.token", rootfsAgentTokenPath)
	assert.Contains(t, string(rootfsSSHDConfig("root")), "PermitRootLogin prohibit-password")
	assert.Contains(t, string(rootfsSSHDConfig("alice")), "AllowUsers alice")
}

type testRootfsVMProvisionFixture struct {
	t                *testing.T
	base             *testRootfsImageInspectFixture
	target           uint64
	truncateCalls    int
	attachErr        error
	syncfsErr        error
	finalSizeDelta   uint64
	cancelDuringSync context.CancelFunc
}

func newTestRootfsVMProvisionFixture(t *testing.T) *testRootfsVMProvisionFixture {
	t.Helper()
	fixture := &testRootfsVMProvisionFixture{
		t:      t,
		base:   newTestRootfsImageInspectFixture(t),
		target: minimumRootfsBytes,
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
		unshare: baseDeps.unshare, mount: baseDeps.mount, unmount: baseDeps.unmount, open: baseDeps.open,
		openAt2: baseDeps.openAt2, mkdirAt: baseDeps.mkdirAt, fstat: baseDeps.fstat,
		fstatAt: func(context.Context, int, string, *unix.Stat_t, int) error { return unix.ENOENT },
		fstatfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error {
			assert.Equal(fixture.t, 13, fd)
			stat.Type = unix.EXT4_SUPER_MAGIC
			stat.Bsize = 4096
			stat.Blocks = fixture.target / 4096
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
				Size: fixture.target,
			}
			if fixture.base.snapshotCalls > 1 {
				stat.Size += fixture.finalSizeDelta
			}
			fixture.base.events = append(fixture.base.events, "snapshot")
			return nil
		},
		pread:     func(context.Context, int, []byte, int64) (int, error) { return 0, io.EOF },
		pwrite:    func(context.Context, int, []byte, int64) (int, error) { return 0, errors.New("unexpected write") },
		ftruncate: func(context.Context, int, int64) error { fixture.truncateCalls++; return nil },
		fchown:    func(context.Context, int, int, int) error { return nil },
		fchmod:    func(context.Context, int, uint32) error { return nil },
		fsync: func(ctx context.Context, fd int) error {
			fixture.base.recordCleanupContext(ctx)
			fixture.base.events = append(fixture.base.events, "backing-fsync")
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
		unlinkAt:    func(context.Context, int, string, int) error { return nil },
		close:       baseDeps.close,
		deviceBytes: func(context.Context, int) (uint64, error) { return fixture.target, nil },
		tempName: func(context.Context) (string, error) {
			return ".mvmctl-provision-00000000000000000000000000000001", nil
		},
		run: func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
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
