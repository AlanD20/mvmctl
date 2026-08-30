package loopmount

import (
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRootfsImageInspectAdapterUsesClosedReadOnlyNamespaceFlow(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsImageInspectFixture(t)
	outcome, err := fixture.adapter().InspectImage(t.Context(), fixture.lease, FilesystemExt4)
	require.NoError(t, err)
	assert.Equal(t, imageInspectEffectStateUnchanged, outcome.state)
	assert.Equal(t, "alpine", outcome.osType)
	require.NotNil(t, outcome.lease)
	require.NoError(t, outcome.cleanup(t.Context()))

	require.Len(t, fixture.mounts, 2)
	assert.Equal(t, testRootfsInspectMountCall{target: "/", flags: rootfsInspectPrivateFlags}, fixture.mounts[0])
	assert.Equal(t, testRootfsInspectMountCall{
		source: "/dev/loop3", target: rootfsInspectMountPath, filesystem: "ext4", flags: rootfsInspectMountFlags,
	}, fixture.mounts[1])
	assert.False(t, fixture.unlocked, "a thread with a private mount namespace must never rejoin the runtime pool")
	assert.True(t, fixture.source.state.released)
	assert.True(t, fixture.partitions.state.released)
	assert.True(t, fixture.loop.state.released)
	assert.Equal(t, 2, fixture.snapshotCalls)
	assertRootfsInspectEventOrder(t, fixture.events,
		"close:13", "unmount", "partition-close:41", "loop-clear", "loop-close", "close:11", "close:10",
	)
}

func TestRootfsImageInspectAdapterUnlocksOnlyWhenUnshareFails(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsImageInspectFixture(t)
	fixture.unshareErr = errors.New("unshare denied")
	outcome, err := fixture.adapter().InspectImage(t.Context(), fixture.lease, FilesystemExt4)
	require.Error(t, err)
	assert.ErrorContains(t, err, "mount namespace")
	assert.Equal(t, imageInspectEffectStateUnchanged, outcome.state)
	assert.True(t, fixture.unlocked)
	assert.Empty(t, fixture.mounts)
	assert.Equal(t, 1, fixture.snapshotCalls)
}

func TestRootfsImageInspectAdapterClassifiesPostUnshareFaultsUnknownAndCleansInReverse(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testRootfsImageInspectFixture){
		"private propagation": func(fixture *testRootfsImageInspectFixture) {
			fixture.privateErr = errors.New("private failed")
		},
		"mount": func(fixture *testRootfsImageInspectFixture) {
			fixture.imageMountErr = errors.New("mount failed")
		},
		"source identity": func(fixture *testRootfsImageInspectFixture) {
			fixture.sourceIdentityChanged = true
		},
		"OS release read": func(fixture *testRootfsImageInspectFixture) {
			fixture.readErr = errors.New("read failed")
		},
	}
	for name, configure := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsImageInspectFixture(t)
			configure(fixture)
			outcome, err := fixture.adapter().InspectImage(t.Context(), fixture.lease, FilesystemExt4)
			require.Error(t, err)
			assert.Equal(t, imageInspectEffectStateOutcomeUnknown, outcome.state)
			assert.False(t, fixture.unlocked)
			assert.Equal(t, 2, fixture.snapshotCalls, "cleanup must prove the backing image again")
			if fixture.mounted {
				assertRootfsInspectEventOrder(
					t,
					fixture.events,
					"close:13",
					"unmount",
					"partition-close:41",
					"loop-clear",
				)
			}
		})
	}
}

func TestRootfsImageInspectAdapterCleanupIgnoresCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	fixture := newTestRootfsImageInspectFixture(t)
	fixture.cancelDuringRead = cancel
	outcome, err := fixture.adapter().InspectImage(ctx, fixture.lease, FilesystemExt4)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, imageInspectEffectStateOutcomeUnknown, outcome.state)
	assert.False(t, fixture.cleanupSawCancellation)
	assert.True(t, fixture.loop.state.released)
	assert.True(t, fixture.partitions.state.released)
	assert.Equal(t, 2, fixture.snapshotCalls)
}

func TestRootfsImageInspectAdapterNeverClaimsUnchangedWhenCleanupOrSnapshotIsUncertain(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testRootfsImageInspectFixture){
		"unmount failure": func(fixture *testRootfsImageInspectFixture) {
			fixture.unmountErr = errors.New("unmount uncertain")
		},
		"backing metadata changed": func(fixture *testRootfsImageInspectFixture) {
			fixture.changeSecondSnapshot = true
		},
	}
	for name, configure := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsImageInspectFixture(t)
			configure(fixture)
			outcome, err := fixture.adapter().InspectImage(t.Context(), fixture.lease, FilesystemExt4)
			require.Error(t, err)
			assert.Equal(t, imageInspectEffectStateOutcomeUnknown, outcome.state)
			assert.Empty(t, outcome.osType)
		})
	}
}

func TestReadRootfsOSReleaseUsesFixedNoSymlinkFallback(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsImageInspectFixture(t)
	fixture.etcOSReleaseUnavailable = true
	osType, err := readRootfsOSRelease(t.Context(), fixture.deps(), 13)
	require.NoError(t, err)
	assert.Equal(t, "alpine", osType)
	require.GreaterOrEqual(t, len(fixture.openAt2Calls), 2)
	assert.Equal(t, "etc/os-release", fixture.openAt2Calls[0].path)
	assert.Equal(t, "usr/lib/os-release", fixture.openAt2Calls[1].path)
	for _, call := range fixture.openAt2Calls[:2] {
		assert.Equal(t, uint64(rootfsInspectLeafResolveFlags), call.how.Resolve)
		assert.Equal(t, uint64(rootfsInspectFileOpenFlags), call.how.Flags)
	}
}

// Rationale: inspection source validation and reverse cleanup share borrowed parent capabilities. Even an erroneous
// concurrent teardown must fail closed without racing mutable loop or partition state.
func TestActiveRootfsInspectSourceSerializesConcurrentCapabilityTeardown(t *testing.T) {
	t.Parallel()

	for range 32 {
		loop := &rootfsLoopDevice{state: &rootfsLoopDeviceState{
			deps: rootfsLoopDeps{
				clear: func(context.Context, int) error { return nil },
				close: func(context.Context, int) error { return nil },
			},
			fd: 40, index: 3, identity: rootfsBlockIdentity{major: 7, minor: 3}, access: rootfsReadOnly,
		}}
		partitions := &rootfsPartitionSet{state: &rootfsPartitionSetState{
			deps:   rootfsPartitionDiscoveryDeps{close: func(context.Context, int) error { return nil }},
			parent: loop.state, parentIndex: 3, parentIdentity: loop.state.identity,
		}}
		source := &rootfsSelectedSource{state: &rootfsSelectedSourceState{
			parent: loop.state, partitions: partitions.state, kind: rootfsMountSourceRaw,
			fd: 40, identity: loop.state.identity, filesystem: FilesystemExt4,
		}}
		deps := rootfsImageInspectDeps{fstat: func(_ context.Context, _ int, stat *unix.Stat_t) error {
			*stat = unix.Stat_t{Mode: unix.S_IFBLK | 0600, Rdev: unix.Mkdev(7, 3)}
			return nil
		}}
		start := make(chan struct{})
		results := make(chan error, 4)
		go func() {
			<-start
			_, err := activeRootfsInspectSource(t.Context(), deps, source, FilesystemExt4)
			results <- err
		}()
		go func() { <-start; results <- source.Release(t.Context()) }()
		go func() { <-start; results <- partitions.Release(t.Context()) }()
		go func() { <-start; results <- loop.Release(t.Context()) }()
		close(start)
		for range 4 {
			<-results // Validation may win or lose each race; completion without a data race is the contract.
		}
	}
}

type testRootfsInspectMountCall struct {
	source     string
	target     string
	filesystem string
	flags      uintptr
}

type testRootfsInspectOpenAt2Call struct {
	path string
	how  unix.OpenHow
}

type testRootfsImageInspectFixture struct {
	t          *testing.T
	lease      *rootfsLease
	loop       *rootfsLoopDevice
	partitions *rootfsPartitionSet
	source     *rootfsSelectedSource

	events        []string
	mounts        []testRootfsInspectMountCall
	openAt2Calls  []testRootfsInspectOpenAt2Call
	content       []byte
	readOffset    int
	openAt2Count  int
	snapshotCalls int

	unlocked                bool
	mounted                 bool
	cleanupSawCancellation  bool
	unshareErr              error
	privateErr              error
	imageMountErr           error
	unmountErr              error
	readErr                 error
	sourceIdentityChanged   bool
	changeSecondSnapshot    bool
	etcOSReleaseUnavailable bool
	cancelDuringRead        context.CancelFunc
}

func newTestRootfsImageInspectFixture(t *testing.T) *testRootfsImageInspectFixture {
	t.Helper()
	fixture := &testRootfsImageInspectFixture{t: t, content: []byte("NAME=Alpine\nID=alpine\n")}
	metadata := rootfsStableMetadata{
		deviceMajor: 8, deviceMinor: 1, inode: 99, mode: unix.S_IFREG | 0600,
		uid: 1000, gid: 1000, nlink: 1, size: 4096,
	}
	fixture.lease = &rootfsLease{rootfsFD: 30, access: rootfsReadOnly, metadata: metadata}
	fixture.loop = &rootfsLoopDevice{state: &rootfsLoopDeviceState{
		fd: 40, index: 3, identity: rootfsBlockIdentity{major: 7, minor: 3}, access: rootfsReadOnly,
	}}
	fixture.loop.state.deps = rootfsLoopDeps{
		clear: func(ctx context.Context, _ int) error {
			fixture.recordCleanupContext(ctx)
			assert.True(t, fixture.partitions.state.released)
			fixture.events = append(fixture.events, "loop-clear")
			return nil
		},
		close: func(ctx context.Context, _ int) error {
			fixture.recordCleanupContext(ctx)
			fixture.events = append(fixture.events, "loop-close")
			return nil
		},
	}
	fixture.partitions = &rootfsPartitionSet{state: &rootfsPartitionSetState{
		parent: fixture.loop.state, parentIndex: 3, parentIdentity: fixture.loop.state.identity,
		devices: []rootfsPartitionDevice{{number: 1, fd: 41, identity: rootfsBlockIdentity{major: 259, minor: 1}}},
	}}
	fixture.partitions.state.deps = rootfsPartitionDiscoveryDeps{close: func(ctx context.Context, fd int) error {
		fixture.recordCleanupContext(ctx)
		assert.True(t, fixture.source == nil || fixture.source.state.released)
		fixture.events = append(fixture.events, "partition-close:41")
		return nil
	}}
	fixture.source = &rootfsSelectedSource{state: &rootfsSelectedSourceState{
		parent: fixture.loop.state, partitions: fixture.partitions.state, kind: rootfsMountSourceRaw,
		fd: 40, identity: fixture.loop.state.identity, filesystem: FilesystemExt4,
	}}
	return fixture
}

func (fixture *testRootfsImageInspectFixture) adapter() *rootfsImageInspectAdapter {
	return &rootfsImageInspectAdapter{deps: fixture.deps()}
}

func (fixture *testRootfsImageInspectFixture) deps() rootfsImageInspectDeps {
	return rootfsImageInspectDeps{
		lockOSThread: func(context.Context) error {
			fixture.events = append(fixture.events, "lock-thread")
			return nil
		},
		unlockOSThread: func(context.Context) error {
			fixture.unlocked = true
			fixture.events = append(fixture.events, "unlock-thread")
			return nil
		},
		unshare: func(context.Context, int) error { return fixture.unshareErr },
		mount: func(_ context.Context, source, target, filesystem string, flags uintptr, _ string) error {
			fixture.mounts = append(fixture.mounts, testRootfsInspectMountCall{
				source: source, target: target, filesystem: filesystem, flags: flags,
			})
			if target == "/" {
				return fixture.privateErr
			}
			if fixture.imageMountErr == nil {
				fixture.mounted = true
			}
			return fixture.imageMountErr
		},
		unmount: func(ctx context.Context, target string, flags int) error {
			fixture.recordCleanupContext(ctx)
			assert.Equal(fixture.t, rootfsInspectMountPath, target)
			assert.Zero(fixture.t, flags)
			fixture.events = append(fixture.events, "unmount")
			return fixture.unmountErr
		},
		open: func(_ context.Context, path string, flags int, mode uint32) (int, error) {
			assert.Equal(fixture.t, rootfsInspectRuntimeDirectory, path)
			assert.Equal(fixture.t, rootfsInspectOpenFlags, flags)
			assert.Zero(fixture.t, mode)
			return 10, nil
		},
		openAt2: fixture.openAt2,
		mkdirAt: func(_ context.Context, fd int, path string, mode uint32) error {
			assert.Equal(fixture.t, 10, fd)
			assert.Equal(fixture.t, rootfsInspectMountName, path)
			assert.Equal(fixture.t, uint32(rootfsInspectMountMode), mode)
			return unix.EEXIST
		},
		fstat: fixture.fstat,
		fstatfs: func(_ context.Context, fd int, stat *unix.Statfs_t) error {
			assert.Equal(fixture.t, 13, fd)
			stat.Type = unix.EXT4_SUPER_MAGIC
			return nil
		},
		statx: fixture.statx,
		read:  fixture.read,
		close: func(ctx context.Context, fd int) error {
			fixture.recordCleanupContext(ctx)
			fixture.events = append(fixture.events, "close:"+rootfsTestDecimal(fd))
			return nil
		},
		attachLoop: func(context.Context, *rootfsLease) (*rootfsLoopDevice, error) {
			fixture.events = append(fixture.events, "attach")
			return fixture.loop, nil
		},
		discoverPartitions: func(context.Context, *rootfsLoopDevice) (*rootfsPartitionSet, error) {
			fixture.events = append(fixture.events, "discover")
			return fixture.partitions, nil
		},
		probeFilesystem: func(
			context.Context,
			Filesystem,
			*rootfsLoopDevice,
			*rootfsPartitionSet,
		) (*rootfsSelectedSource, error) {
			fixture.events = append(fixture.events, "probe")
			return fixture.source, nil
		},
	}
}

func (fixture *testRootfsImageInspectFixture) openAt2(
	_ context.Context,
	fd int,
	path string,
	how *unix.OpenHow,
) (int, error) {
	fixture.openAt2Calls = append(fixture.openAt2Calls, testRootfsInspectOpenAt2Call{path: path, how: *how})
	if path == "etc/os-release" && fixture.etcOSReleaseUnavailable {
		return -1, unix.ELOOP
	}
	if path == "etc/os-release" || path == "usr/lib/os-release" {
		assert.Equal(fixture.t, 13, fd)
		return 14, nil
	}
	fixture.openAt2Count++
	switch fixture.openAt2Count {
	case 1:
		assert.Equal(fixture.t, 10, fd)
		return 11, nil
	case 2:
		assert.Equal(fixture.t, 10, fd)
		return 12, nil
	case 3:
		assert.Equal(fixture.t, 10, fd)
		return 13, nil
	default:
		return -1, errors.New("unexpected openat2")
	}
}

func (fixture *testRootfsImageInspectFixture) fstat(_ context.Context, fd int, stat *unix.Stat_t) error {
	switch fd {
	case 10:
		*stat = unix.Stat_t{Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0, Dev: 1, Ino: 1, Nlink: 2}
	case 11, 12:
		*stat = unix.Stat_t{Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0, Dev: 1, Ino: 2, Nlink: 2}
	case 40:
		minor := uint32(3)
		if fixture.sourceIdentityChanged {
			minor++
		}
		*stat = unix.Stat_t{Mode: unix.S_IFBLK | 0600, Rdev: unix.Mkdev(7, minor)}
	case 14:
		*stat = unix.Stat_t{
			Mode: unix.S_IFREG | 0644, Uid: 0, Gid: 0, Dev: 2, Ino: 5, Nlink: 1,
			Size: int64(len(fixture.content)),
		}
	default:
		return errors.New("unexpected fstat descriptor")
	}
	return nil
}

func (fixture *testRootfsImageInspectFixture) statx(
	ctx context.Context,
	fd, flags, mask int,
	stat *unix.Statx_t,
) error {
	fixture.snapshotCalls++
	if fixture.snapshotCalls > 1 {
		fixture.recordCleanupContext(ctx)
	}
	assert.Equal(fixture.t, 30, fd)
	assert.Equal(fixture.t, rootfsStatxFlags, flags)
	assert.Equal(fixture.t, rootfsInspectSnapshotMask, mask)
	*stat = unix.Statx_t{
		Mask: rootfsStatxRequiredFile, Dev_major: 8, Dev_minor: 1, Ino: 99,
		Mode: unix.S_IFREG | 0600, Uid: 1000, Gid: 1000, Nlink: 1, Size: 4096,
	}
	if fixture.changeSecondSnapshot && fixture.snapshotCalls == 2 {
		stat.Mtime.Sec++
	}
	fixture.events = append(fixture.events, "snapshot")
	return nil
}

func (fixture *testRootfsImageInspectFixture) read(ctx context.Context, fd int, buffer []byte) (int, error) {
	assert.Equal(fixture.t, 14, fd)
	if fixture.cancelDuringRead != nil {
		fixture.cancelDuringRead()
		fixture.cancelDuringRead = nil
		return 0, context.Canceled
	}
	if fixture.readErr != nil {
		return 0, fixture.readErr
	}
	if fixture.readOffset == len(fixture.content) {
		return 0, io.EOF
	}
	count := copy(buffer, fixture.content[fixture.readOffset:])
	fixture.readOffset += count
	fixture.events = append(fixture.events, "read")
	return count, nil
}

func (fixture *testRootfsImageInspectFixture) recordCleanupContext(ctx context.Context) {
	if ctx.Err() != nil {
		fixture.cleanupSawCancellation = true
	}
}

func assertRootfsInspectEventOrder(t *testing.T, events []string, ordered ...string) {
	t.Helper()
	previous := -1
	for _, event := range ordered {
		index := slices.Index(events, event)
		require.Greater(t, index, previous, "event %q missing or out of order in %v", event, events)
		previous = index
	}
}

func rootfsTestDecimal(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append(digits, byte('0'+value%10))
		value /= 10
	}
	slices.Reverse(digits)
	return string(digits)
}
