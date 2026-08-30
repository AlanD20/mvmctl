package loopmount

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
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

	wantMounts := []testRootfsInspectMountCall{
		{target: "/", flags: rootfsInspectPrivateFlags},
		{
			source: "/dev/loop3p1", target: rootfsInspectMountPath,
			filesystem: "ext4", flags: rootfsInspectMountFlags,
		},
	}
	if diff := cmp.Diff(wantMounts, fixture.mounts, cmp.AllowUnexported(testRootfsInspectMountCall{})); diff != "" {
		t.Errorf("image inspection mounts mismatch (-want +got):\n%s", diff)
	}
	assert.False(t, fixture.unlocked, "a thread with a private mount namespace must never rejoin the runtime pool")
	assert.True(t, fixture.source.state.released)
	assert.True(t, fixture.partitions.state.released)
	assert.True(t, fixture.loop.state.released)
	assert.Equal(t, 2, fixture.snapshotCalls)
	assertRootfsInspectEventOrder(t, fixture.events,
		"close:13", "unmount", "partition-close:41", "loop-clear", "loop-close", "close:11", "close:10",
	)
}

func TestRootfsImageInspectAdapterUsesValidRawImageSource(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsImageInspectFixture(t)
	fixture.partitions.state.devices = nil
	fixture.source = &rootfsSelectedSource{state: &rootfsSelectedSourceState{
		parent: fixture.loop.state, partitions: fixture.partitions.state, kind: rootfsMountSourceRaw,
		fd: 40, identity: fixture.loop.state.identity, filesystem: FilesystemExt4,
	}}

	outcome, err := fixture.adapter().InspectImage(t.Context(), fixture.lease, FilesystemExt4)
	require.NoError(t, err)
	assert.Equal(t, imageInspectEffectStateUnchanged, outcome.state)
	assert.Equal(t, "alpine", outcome.osType)
	require.NotNil(t, outcome.lease)
	require.NoError(t, outcome.cleanup(t.Context()))

	wantMounts := []testRootfsInspectMountCall{
		{target: "/", flags: rootfsInspectPrivateFlags},
		{
			source: "/dev/loop3", target: rootfsInspectMountPath,
			filesystem: "ext4", flags: rootfsInspectMountFlags,
		},
	}
	if diff := cmp.Diff(wantMounts, fixture.mounts, cmp.AllowUnexported(testRootfsInspectMountCall{})); diff != "" {
		t.Errorf("raw image inspection mounts mismatch (-want +got):\n%s", diff)
	}
	assert.False(t, fixture.unlocked, "a thread with a private mount namespace must never rejoin the runtime pool")
	assert.True(t, fixture.source.state.released)
	assert.True(t, fixture.partitions.state.released)
	assert.True(t, fixture.loop.state.released)
	assert.Equal(t, 2, fixture.sourceFstatCalls)
	assert.Equal(t, 2, fixture.snapshotCalls)
	wantEvents := []string{
		"snapshot", "lock-thread", "attach", "discover", "probe", "close:12", "read", "close:14", "close:13",
		"unmount", "loop-clear", "loop-close", "close:11", "close:10", "snapshot",
	}
	if diff := cmp.Diff(wantEvents, fixture.events); diff != "" {
		t.Errorf("raw image inspection cleanup order mismatch (-want +got):\n%s", diff)
	}
	assert.NotContains(t, fixture.events, "partition-close:41")
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

// Rationale: cancellation after thread pinning but before unshare must not create a thread-local namespace for an
// abandoned request. The still-shared thread is safe to return to Go only after cancellation-independent unlock.
func TestRootfsImageInspectAdapterCancelsBeforeUnshare(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	fixture := newTestRootfsImageInspectFixture(t)
	fixture.cancelAfterLock = cancel
	outcome, err := fixture.adapter().InspectImage(ctx, fixture.lease, FilesystemExt4)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, imageInspectEffectStateUnchanged, outcome.state)
	assert.True(t, fixture.unlocked)
	assert.False(t, fixture.unlockSawCancellation)
	assert.False(t, fixture.unshareCalled)
	assert.Empty(t, fixture.mounts)
	assert.Equal(t, 1, fixture.snapshotCalls)
}

func TestPrepareRootfsInspectMountpointRejectsNonCanonicalRuntimeMode(t *testing.T) {
	t.Parallel()

	// Rationale: /run/mvmctl is a shared managed layout root. Accepting a merely non-writable mode would permit this
	// effect to proceed while ordinary users cannot traverse runtime state or can list names that policy hides.
	fixture := newTestRootfsImageInspectFixture(t)
	fixture.runtimeMode = 0755
	mountpoint, err := prepareRootfsInspectMountpoint(t.Context(), fixture.deps())
	require.Error(t, err)
	assert.ErrorContains(t, err, "root:root mode 0711")
	assert.Nil(t, mountpoint)
	assert.Contains(t, fixture.events, "close:10")
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
			fixture.changeSourceBeforeMount = true
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

// Rationale: the retained source is rechecked after the pathname-based mount. A pre-mount-only check would permit a
// kernel device-node replacement to authorize inspection of a different block device.
func TestRootfsImageInspectAdapterRejectsSourceChangedAfterMount(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsImageInspectFixture(t)
	fixture.changeSourceAfterMount = true
	outcome, err := fixture.adapter().InspectImage(t.Context(), fixture.lease, FilesystemExt4)
	require.Error(t, err)
	assert.ErrorContains(t, err, "identity changed")
	assert.True(t, fixture.mounted)
	assert.Equal(t, 2, fixture.sourceFstatCalls)
	assert.Equal(t, imageInspectEffectStateOutcomeUnknown, outcome.state)
	assert.Empty(t, outcome.osType)
}

// Rationale: the fixed path is still a pathname mount target. Replacement between admission and mount must fail
// closed before the image mount syscall even though both objects have otherwise admissible metadata.
func TestRootfsImageInspectAdapterRejectsMountpointReplacement(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsImageInspectFixture(t)
	fixture.mountpointIdentityChanged = true
	outcome, err := fixture.adapter().InspectImage(t.Context(), fixture.lease, FilesystemExt4)
	require.Error(t, err)
	assert.ErrorContains(t, err, "mountpoint identity changed")
	assert.Equal(t, imageInspectEffectStateOutcomeUnknown, outcome.state)
	assert.False(t, fixture.mounted)
	require.Len(t, fixture.mounts, 1)
	assert.Equal(t, "/", fixture.mounts[0].target)
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

// Rationale: every owned capability must be released even after success. Any close, detach, or partition cleanup
// failure prevents an unchanged result and must retain bounded image-inspection-specific recovery evidence.
func TestRootfsImageInspectAdapterClassifiesCapabilityCleanupFaults(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testRootfsImageInspectFixture){
		"mounted root close": func(fixture *testRootfsImageInspectFixture) {
			fixture.mountedRootCloseErr = errors.New("close mounted root failed")
		},
		"partition close": func(fixture *testRootfsImageInspectFixture) {
			fixture.partitionCloseErr = errors.New("close partition failed")
		},
		"loop clear": func(fixture *testRootfsImageInspectFixture) {
			fixture.loopClearErr = errors.New("clear loop failed")
		},
		"loop close with bounded detail": func(fixture *testRootfsImageInspectFixture) {
			fixture.loopCloseErr = errors.New(strings.Repeat("x", 1024))
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
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, true, domainErr.Details["loopmount_image_inspect_cleanup_failed"])
			detail, ok := domainErr.Details["loopmount_image_inspect_cleanup_detail"].(string)
			require.True(t, ok)
			assert.LessOrEqual(t, len(detail), 512)
		})
	}
}

// Rationale: cleanup diagnostics supplement the authoritative operation error. They must not mutate or reclassify the
// original DomainError, and a later cleanup failure must not overwrite the first action-specific bounded detail.
func TestAppendRootfsInspectCleanupErrorPreservesPrimaryDomainError(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("primary cause")
	primaryDetails := map[string]any{"stage": "read-os-release"}
	primary := &errs.DomainError{
		Code: errs.CodeImageError, Message: "primary inspection failure", Op: "image.inspect",
		Entity: "image-abc", Class: errs.ClassRetryable, Err: primaryCause, Details: primaryDetails,
	}
	firstCleanupCause := errors.New("close OS release failed")
	first := appendRootfsInspectCleanupError(primary, "close fixed OS release leaf", firstCleanupCause)
	firstDomain := errs.AsDomainError(first)
	require.NotNil(t, firstDomain)
	require.NotSame(t, primary, firstDomain)

	wantMetadata := struct {
		Code    errs.Code
		Message string
		Op      string
		Entity  string
		Class   errs.Class
	}{
		Code: primary.Code, Message: primary.Message, Op: primary.Op, Entity: primary.Entity, Class: primary.Class,
	}
	gotMetadata := struct {
		Code    errs.Code
		Message string
		Op      string
		Entity  string
		Class   errs.Class
	}{
		Code: firstDomain.Code, Message: firstDomain.Message, Op: firstDomain.Op,
		Entity: firstDomain.Entity, Class: firstDomain.Class,
	}
	if diff := cmp.Diff(wantMetadata, gotMetadata); diff != "" {
		t.Errorf("primary DomainError metadata changed (-want +got):\n%s", diff)
	}
	assert.ErrorIs(t, first, primaryCause)
	assert.ErrorIs(t, first, firstCleanupCause)
	firstDetail, ok := firstDomain.Details[rootfsInspectCleanupDetailKey].(string)
	require.True(t, ok)
	assert.Equal(t, "close fixed OS release leaf: close OS release failed", firstDetail)
	assert.LessOrEqual(t, len(firstDetail), maxRootfsInspectCleanupDetailBytes)
	wantFirstDetails := map[string]any{
		"stage":                       "read-os-release",
		rootfsInspectCleanupFailedKey: true,
		rootfsInspectCleanupDetailKey: firstDetail,
	}
	if diff := cmp.Diff(wantFirstDetails, firstDomain.Details); diff != "" {
		t.Errorf("cleanup evidence mismatch (-want +got):\n%s", diff)
	}

	secondCleanupCause := errors.New("unmount failed")
	second := appendRootfsInspectCleanupError(first, "unmount inspected rootfs", secondCleanupCause)
	secondDomain := errs.AsDomainError(second)
	require.NotNil(t, secondDomain)
	require.NotSame(t, firstDomain, secondDomain)
	assert.ErrorIs(t, second, primaryCause)
	assert.ErrorIs(t, second, firstCleanupCause)
	assert.ErrorIs(t, second, secondCleanupCause)
	if diff := cmp.Diff(wantFirstDetails, secondDomain.Details); diff != "" {
		t.Errorf("existing cleanup evidence overwritten (-want +got):\n%s", diff)
	}

	wantPrimaryDetails := map[string]any{"stage": "read-os-release"}
	if diff := cmp.Diff(wantPrimaryDetails, primary.Details); diff != "" {
		t.Errorf("original primary details mutated (-want +got):\n%s", diff)
	}
	assert.Same(t, primaryCause, primary.Err)
	assert.Equal(t, "primary inspection failure", primary.Message)
	assert.Equal(t, "image.inspect", primary.Op)
	assert.Equal(t, "image-abc", primary.Entity)
	assert.Equal(t, errs.CodeImageError, primary.Code)
	assert.Equal(t, errs.ClassRetryable, primary.Class)
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

	unlocked                  bool
	unlockSawCancellation     bool
	mounted                   bool
	cleanupSawCancellation    bool
	unshareCalled             bool
	unshareErr                error
	privateErr                error
	imageMountErr             error
	unmountErr                error
	readErr                   error
	changeSourceBeforeMount   bool
	changeSourceAfterMount    bool
	sourceFstatCalls          int
	mountpointIdentityChanged bool
	changeSecondSnapshot      bool
	etcOSReleaseUnavailable   bool
	cancelDuringRead          context.CancelFunc
	cancelAfterLock           context.CancelFunc
	runtimeMode               uint32
	mountedRootCloseErr       error
	partitionCloseErr         error
	loopClearErr              error
	loopCloseErr              error
}

func newTestRootfsImageInspectFixture(t *testing.T) *testRootfsImageInspectFixture {
	t.Helper()
	fixture := &testRootfsImageInspectFixture{
		t: t, content: []byte("NAME=Alpine\nID=alpine\n"), runtimeMode: 0711,
	}
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
			return fixture.loopClearErr
		},
		close: func(ctx context.Context, _ int) error {
			fixture.recordCleanupContext(ctx)
			fixture.events = append(fixture.events, "loop-close")
			return fixture.loopCloseErr
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
		return fixture.partitionCloseErr
	}}
	fixture.source = &rootfsSelectedSource{state: &rootfsSelectedSourceState{
		parent: fixture.loop.state, partitions: fixture.partitions.state, kind: rootfsMountSourcePartition,
		fd: 41, identity: fixture.partitions.state.devices[0].identity,
		partition: 1, filesystem: FilesystemExt4,
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
			if fixture.cancelAfterLock != nil {
				fixture.cancelAfterLock()
			}
			return nil
		},
		unlockOSThread: func(ctx context.Context) error {
			fixture.unlocked = true
			fixture.unlockSawCancellation = ctx.Err() != nil
			fixture.events = append(fixture.events, "unlock-thread")
			return nil
		},
		unshare: func(context.Context, int) error {
			fixture.unshareCalled = true
			return fixture.unshareErr
		},
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
			if fd == 13 {
				return fixture.mountedRootCloseErr
			}
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
		*stat = unix.Stat_t{Mode: unix.S_IFDIR | fixture.runtimeMode, Uid: 0, Gid: 0, Dev: 1, Ino: 1, Nlink: 2}
	case 11:
		*stat = unix.Stat_t{Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0, Dev: 1, Ino: 2, Nlink: 2}
	case 12:
		inode := uint64(2)
		if fixture.mountpointIdentityChanged {
			inode++
		}
		*stat = unix.Stat_t{Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0, Dev: 1, Ino: inode, Nlink: 2}
	case 40:
		fixture.sourceFstatCalls++
		*stat = unix.Stat_t{Mode: unix.S_IFBLK | 0600, Rdev: unix.Mkdev(7, 3)}
	case 41:
		fixture.sourceFstatCalls++
		minor := uint32(1)
		if fixture.changeSourceBeforeMount || (fixture.changeSourceAfterMount && fixture.sourceFstatCalls == 2) {
			minor++
		}
		*stat = unix.Stat_t{Mode: unix.S_IFBLK | 0600, Rdev: unix.Mkdev(259, minor)}
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
