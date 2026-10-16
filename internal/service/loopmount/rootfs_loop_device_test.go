package loopmount

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type rootfsLoopContextKey struct{}

func TestAttachRootfsLoopDeviceUsesOnlyAdmittedDescriptorAndClosedAccess(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		access    rootfsAccess
		wantFlags uint32
	}{
		"read only": {
			access:    rootfsReadOnly,
			wantFlags: unix.LO_FLAGS_AUTOCLEAR | unix.LO_FLAGS_PARTSCAN | unix.LO_FLAGS_READ_ONLY,
		},
		"read write": {
			access:    rootfsReadWrite,
			wantFlags: unix.LO_FLAGS_AUTOCLEAR | unix.LO_FLAGS_PARTSCAN,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsLoopFixture()
			lease := &rootfsLease{rootfsFD: 41, access: test.access}

			device, err := attachRootfsLoopDevice(t.Context(), fixture.dependencies(), lease)
			require.NoError(t, err)
			require.NotNil(t, device)
			require.NotNil(t, device.state)

			assert.Equal(t, []testRootfsLoopOpen{
				{path: rootfsLoopControlPath, flags: unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW},
				{path: "/dev/loop3", flags: unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW},
			}, fixture.opens)
			assert.Equal(t, []int{10}, fixture.getFreeFDs)
			assert.Equal(t, []int{20}, fixture.configureFDs)
			require.Len(t, fixture.configs, 1)
			wantConfig := unix.LoopConfig{
				Fd: 41,
				Info: unix.LoopInfo64{
					Flags: test.wantFlags,
				},
			}
			if diff := cmp.Diff(wantConfig, fixture.configs[0]); diff != "" {
				t.Errorf("LOOP_CONFIGURE mismatch (-want +got):\n%s", diff)
			}
			assert.Equal(t, 3, device.state.index)
			assert.Equal(t, rootfsBlockIdentity{major: 7, minor: 3}, device.state.identity)
			assert.Equal(t, test.access, device.state.access)
			assert.Equal(t, []string{"close.10"}, fixture.events)

			require.NoError(t, device.Release(t.Context()))
			assert.Equal(t, []string{"close.10", "clear.20", "close.20"}, fixture.events)
		})
	}
}

func TestAttachRootfsLoopDeviceRetriesOnlyConfigureContention(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsLoopFixture()
	fixture.freeIndices = []int{3, 4, 5}
	fixture.openFDs["/dev/loop4"] = []int{21}
	fixture.openFDs["/dev/loop5"] = []int{22}
	fixture.stats[21] = loopDeviceStat(4)
	fixture.stats[22] = loopDeviceStat(5)
	fixture.configureErrors = []error{unix.EBUSY, unix.EBUSY, nil}
	lease := &rootfsLease{rootfsFD: 41, access: rootfsReadWrite}

	device, err := attachRootfsLoopDevice(t.Context(), fixture.dependencies(), lease)
	require.NoError(t, err)
	require.NotNil(t, device)

	assert.Equal(t, 3, fixture.getFreeCalls)
	assert.Equal(t, []int{10, 10, 10}, fixture.getFreeFDs)
	assert.Equal(t, []int{20, 21, 22}, fixture.configureFDs)
	assert.Len(t, fixture.configs, 3)
	assert.Equal(t, []string{"close.20", "close.21", "close.10"}, fixture.events)
	assert.Empty(t, fixture.clears)
	require.NoError(t, device.Release(t.Context()))
	assert.Equal(t, []string{"close.20", "close.21", "close.10", "clear.22", "close.22"}, fixture.events)
}

func TestAttachRootfsLoopDeviceBoundsConfigureContention(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsLoopFixture()
	fixture.freeIndices = make([]int, maxRootfsLoopConfigureAttempts)
	fixture.configureErrors = make([]error, maxRootfsLoopConfigureAttempts)
	for index := range maxRootfsLoopConfigureAttempts {
		fixture.freeIndices[index] = index
		fixture.configureErrors[index] = unix.EBUSY
		path, pathErr := rootfsLoopDevicePath(index)
		require.NoError(t, pathErr)
		fixture.openFDs[path] = []int{100 + index}
		fixture.stats[100+index] = loopDeviceStat(index)
	}
	lease := &rootfsLease{rootfsFD: 41, access: rootfsReadWrite}

	device, err := attachRootfsLoopDevice(t.Context(), fixture.dependencies(), lease)
	assert.Nil(t, device)
	require.Error(t, err)
	assert.ErrorContains(t, err, "remained busy")
	assert.Equal(t, maxRootfsLoopConfigureAttempts, fixture.getFreeCalls)
	assert.Len(t, fixture.configs, maxRootfsLoopConfigureAttempts)
	assert.Empty(t, fixture.clears)
	assert.Len(t, fixture.closes, maxRootfsLoopConfigureAttempts+1)
}

func TestAttachRootfsLoopDeviceFailsClosedAndReleasesOpenedDescriptors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate    func(*testRootfsLoopFixture, *rootfsLease)
		wantErr   string
		wantOpens int
		wantClose []int
	}{
		"control open error": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.openErrors[rootfsLoopControlPath] = unix.EACCES
			},
			wantErr: "open loop-control device", wantOpens: 1,
		},
		"control stat error": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.statErrors[10] = unix.EIO
			},
			wantErr: "inspect loop-control device", wantOpens: 1, wantClose: []int{10},
		},
		"invalid control device": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.stats[10] = unix.Stat_t{Mode: unix.S_IFREG | 0o600}
			},
			wantErr: "not the loop-control device", wantOpens: 1, wantClose: []int{10},
		},
		"get-free error": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.getFreeErrors = []error{unix.EIO}
			},
			wantErr: "allocate free loop device", wantOpens: 1, wantClose: []int{10},
		},
		"negative loop index": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.freeIndices = []int{-1}
			},
			wantErr: "index is outside", wantOpens: 1, wantClose: []int{10},
		},
		"loop index exceeds ABI": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.freeIndices = []int{maxRootfsLoopDeviceIndex + 1}
			},
			wantErr: "index is outside", wantOpens: 1, wantClose: []int{10},
		},
		"loop open error": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.openErrors["/dev/loop3"] = unix.EACCES
			},
			wantErr: "open allocated loop device", wantOpens: 2, wantClose: []int{10},
		},
		"invalid loop device identity": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.stats[20] = loopDeviceStat(4)
			},
			wantErr: "not the requested loop block device", wantOpens: 2, wantClose: []int{20, 10},
		},
		"loop stat error": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.statErrors[20] = unix.EIO
			},
			wantErr: "inspect allocated loop device", wantOpens: 2, wantClose: []int{20, 10},
		},
		"configure error": {
			mutate: func(fixture *testRootfsLoopFixture, _ *rootfsLease) {
				fixture.configureErrors = []error{unix.EIO}
			},
			wantErr: "configure admitted rootfs loop device", wantOpens: 2, wantClose: []int{20, 10},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsLoopFixture()
			lease := &rootfsLease{rootfsFD: 41, access: rootfsReadWrite}
			test.mutate(fixture, lease)

			device, err := attachRootfsLoopDevice(t.Context(), fixture.dependencies(), lease)
			assert.Nil(t, device)
			requireRootfsLoopContractError(t, err, test.wantErr)
			assert.Len(t, fixture.opens, test.wantOpens)
			assert.Equal(t, test.wantClose, fixture.closes)
			assert.Empty(t, fixture.clears)
		})
	}
}

func TestAttachRootfsLoopDeviceRejectsInvalidAuthorityBeforeSystemEffects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		ctx     context.Context
		fixture *testRootfsLoopFixture
		deps    func(*testRootfsLoopFixture) rootfsLoopDeps
		lease   *rootfsLease
		wantErr string
	}{
		"nil context": {
			fixture: newTestRootfsLoopFixture(),
			lease:   &rootfsLease{rootfsFD: 41, access: rootfsReadWrite},
			wantErr: "context is required",
		},
		"cancelled context": {
			ctx:     cancelledRootfsLoopContext(),
			fixture: newTestRootfsLoopFixture(),
			lease:   &rootfsLease{rootfsFD: 41, access: rootfsReadWrite},
			wantErr: "context ended",
		},
		"incomplete dependencies": {
			ctx: t.Context(), fixture: newTestRootfsLoopFixture(),
			deps:    func(*testRootfsLoopFixture) rootfsLoopDeps { return rootfsLoopDeps{} },
			lease:   &rootfsLease{rootfsFD: 41, access: rootfsReadWrite},
			wantErr: "dependencies are incomplete",
		},
		"nil rootfs lease": {
			ctx: t.Context(), fixture: newTestRootfsLoopFixture(),
			wantErr: "authority is missing",
		},
		"released rootfs lease": {
			ctx: t.Context(), fixture: newTestRootfsLoopFixture(),
			lease:   &rootfsLease{rootfsFD: -1, access: rootfsReadWrite, released: true},
			wantErr: "authority is inactive",
		},
		"invalid rootfs access": {
			ctx: t.Context(), fixture: newTestRootfsLoopFixture(),
			lease:   &rootfsLease{rootfsFD: 41},
			wantErr: "access is invalid",
		},
		"invalid rootfs descriptor": {
			ctx: t.Context(), fixture: newTestRootfsLoopFixture(),
			lease:   &rootfsLease{rootfsFD: -1, access: rootfsReadWrite},
			wantErr: "descriptor is invalid",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			deps := test.fixture.dependencies()
			if test.deps != nil {
				deps = test.deps(test.fixture)
			}
			device, err := attachRootfsLoopDevice(test.ctx, deps, test.lease)
			assert.Nil(t, device)
			requireRootfsLoopContractError(t, err, test.wantErr)
			assert.Empty(t, test.fixture.opens)
			assert.Empty(t, test.fixture.getFreeFDs)
			assert.Empty(t, test.fixture.configureFDs)
			assert.Empty(t, test.fixture.clears)
			assert.Empty(t, test.fixture.closes)
		})
	}
}

func TestAttachRootfsLoopDeviceStopsAfterContextEndsBetweenAttempts(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	fixture := newTestRootfsLoopFixture()
	fixture.freeIndices = []int{3, 4}
	fixture.configureErrors = []error{unix.EBUSY}
	fixture.configureHook = func(int) { cancel() }
	lease := &rootfsLease{rootfsFD: 41, access: rootfsReadWrite}

	device, err := attachRootfsLoopDevice(ctx, fixture.dependencies(), lease)
	assert.Nil(t, device)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []int{10}, fixture.getFreeFDs)
	assert.Equal(t, []int{20}, fixture.configureFDs)
	assert.Equal(t, []string{"close.20", "close.10"}, fixture.events)
	assert.Empty(t, fixture.clears)
}

func TestAttachRootfsLoopDeviceReturnsConfiguredLeaseWhenControlCloseFails(t *testing.T) {
	t.Parallel()

	closeCause := errors.New("control close failed")
	fixture := newTestRootfsLoopFixture()
	fixture.closeErrors[10] = closeCause
	lease := &rootfsLease{rootfsFD: 41, access: rootfsReadWrite}

	device, err := attachRootfsLoopDevice(t.Context(), fixture.dependencies(), lease)
	require.NotNil(t, device)
	require.ErrorIs(t, err, closeCause)
	assert.ErrorContains(t, err, "close loop-control device")
	assert.Empty(t, fixture.clears)

	require.NoError(t, device.Release(t.Context()))
	assert.Equal(t, []string{"close.10", "clear.20", "close.20"}, fixture.events)
}

func TestAttachRootfsLoopDeviceNeverClearsAConfigureFailure(t *testing.T) {
	t.Parallel()

	configureCause := errors.New("configure failed")
	closeCause := errors.New("candidate close failed")
	fixture := newTestRootfsLoopFixture()
	fixture.configureErrors = []error{configureCause}
	fixture.closeErrors[20] = closeCause
	lease := &rootfsLease{rootfsFD: 41, access: rootfsReadWrite}

	device, err := attachRootfsLoopDevice(t.Context(), fixture.dependencies(), lease)
	assert.Nil(t, device)
	require.ErrorIs(t, err, configureCause)
	require.ErrorIs(t, err, closeCause)
	assert.Empty(t, fixture.clears)
	assert.Equal(t, []string{"close.20", "close.10"}, fixture.events)
}

func TestRootfsLoopDeviceReleaseIsCheckedSharedAndCancellationIndependent(t *testing.T) {
	t.Parallel()

	clearCause := errors.New("clear failed")
	closeCause := errors.New("close failed")
	fixture := newTestRootfsLoopFixture()
	fixture.clearErrors[20] = clearCause
	fixture.closeErrors[20] = closeCause
	device := &rootfsLoopDevice{state: &rootfsLoopDeviceState{
		deps: fixture.dependencies(), fd: 20, index: 3,
		identity: rootfsBlockIdentity{major: 7, minor: 3},
	}}
	copyOfDevice := *device
	value := &struct{}{}
	base := context.WithValue(context.Background(), rootfsLoopContextKey{}, value)
	cleanupCtx, cancel := context.WithCancel(base)
	cancel()

	err := device.Release(cleanupCtx)
	require.ErrorIs(t, err, clearCause)
	require.ErrorIs(t, err, closeCause)
	assert.Same(t, value, fixture.clearContexts[0].Value(rootfsLoopContextKey{}))
	assert.Same(t, value, fixture.closeContexts[len(fixture.closeContexts)-1].Value(rootfsLoopContextKey{}))
	assert.NoError(t, fixture.clearContexts[0].Err())
	assert.NoError(t, fixture.closeContexts[len(fixture.closeContexts)-1].Err())
	assert.Equal(t, []string{"clear.20", "close.20"}, fixture.events)

	secondErr := copyOfDevice.Release(t.Context())
	require.ErrorIs(t, secondErr, clearCause)
	require.ErrorIs(t, secondErr, closeCause)
	assert.Equal(t, []string{"clear.20", "close.20"}, fixture.events)
}

// Concurrent copied handles must share one cleanup transition; descriptor reuse makes a duplicate clear or close unsafe.
func TestRootfsLoopDeviceReleaseSerializesConcurrentCopiedHandles(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsLoopFixture()
	device := &rootfsLoopDevice{state: &rootfsLoopDeviceState{
		deps: fixture.dependencies(), fd: 20, index: 3,
		identity: rootfsBlockIdentity{major: 7, minor: 3},
	}}

	const callers = 32
	start := make(chan struct{})
	results := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		copyOfDevice := *device
		go func() {
			ready.Done()
			<-start
			results <- copyOfDevice.Release(t.Context())
		}()
	}
	ready.Wait()
	close(start)

	for range callers {
		require.NoError(t, <-results)
	}
	assert.Equal(t, []int{20}, fixture.clears)
	assert.Equal(t, []int{20}, fixture.closes)
	assert.Equal(t, []string{"clear.20", "close.20"}, fixture.events)
}

func TestRootfsLoopDeviceReleaseTreatsAlreadyDetachedAsSuccess(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsLoopFixture()
	fixture.clearErrors[20] = unix.ENXIO
	device := &rootfsLoopDevice{state: &rootfsLoopDeviceState{
		deps: fixture.dependencies(), fd: 20, index: 3,
		identity: rootfsBlockIdentity{major: 7, minor: 3},
	}}

	require.NoError(t, device.Release(t.Context()))
	assert.Equal(t, []string{"clear.20", "close.20"}, fixture.events)
	var nilDevice *rootfsLoopDevice
	require.NoError(t, nilDevice.Release(t.Context()))
	require.Error(t, (&rootfsLoopDevice{}).Release(t.Context()))
}

func TestRootfsLoopDeviceReleaseClosesWhenClearDependencyIsMissing(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsLoopFixture()
	deps := fixture.dependencies()
	deps.clear = nil
	device := &rootfsLoopDevice{state: &rootfsLoopDeviceState{
		deps: deps, fd: 20, index: 3,
		identity: rootfsBlockIdentity{major: 7, minor: 3},
	}}

	err := device.Release(t.Context())
	require.ErrorContains(t, err, "clear dependency is missing")
	assert.Equal(t, []string{"close.20"}, fixture.events)
}

type testRootfsLoopOpen struct {
	path  string
	flags int
}

type testRootfsLoopFixture struct {
	opens           []testRootfsLoopOpen
	openFDs         map[string][]int
	openErrors      map[string]error
	stats           map[int]unix.Stat_t
	statErrors      map[int]error
	freeIndices     []int
	getFreeErrors   []error
	getFreeCalls    int
	getFreeFDs      []int
	configs         []unix.LoopConfig
	configureFDs    []int
	configureErrors []error
	configureHook   func(int)
	clears          []int
	clearErrors     map[int]error
	clearContexts   []context.Context
	closes          []int
	closeErrors     map[int]error
	closeContexts   []context.Context
	events          []string
}

func newTestRootfsLoopFixture() *testRootfsLoopFixture {
	return &testRootfsLoopFixture{
		openFDs: map[string][]int{
			rootfsLoopControlPath: {10},
			"/dev/loop3":          {20},
		},
		openErrors:  make(map[string]error),
		stats:       map[int]unix.Stat_t{10: loopControlStat(), 20: loopDeviceStat(3)},
		statErrors:  make(map[int]error),
		freeIndices: []int{3},
		clearErrors: make(map[int]error),
		closeErrors: make(map[int]error),
	}
}

func (fixture *testRootfsLoopFixture) dependencies() rootfsLoopDeps {
	return rootfsLoopDeps{
		open: func(_ context.Context, path string, flags int, _ uint32) (int, error) {
			fixture.opens = append(fixture.opens, testRootfsLoopOpen{path: path, flags: flags})
			if err := fixture.openErrors[path]; err != nil {
				return -1, err
			}
			fds := fixture.openFDs[path]
			if len(fds) == 0 {
				return -1, unix.ENOENT
			}
			fd := fds[0]
			fixture.openFDs[path] = fds[1:]
			return fd, nil
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			if err := fixture.statErrors[fd]; err != nil {
				return err
			}
			value, exists := fixture.stats[fd]
			if !exists {
				return unix.EBADF
			}
			*stat = value
			return nil
		},
		getFree: func(_ context.Context, controlFD int) (int, error) {
			call := fixture.getFreeCalls
			fixture.getFreeCalls++
			fixture.getFreeFDs = append(fixture.getFreeFDs, controlFD)
			if call < len(fixture.getFreeErrors) && fixture.getFreeErrors[call] != nil {
				return -1, fixture.getFreeErrors[call]
			}
			if call >= len(fixture.freeIndices) {
				return -1, unix.ENOSPC
			}
			return fixture.freeIndices[call], nil
		},
		configure: func(_ context.Context, loopFD int, config *unix.LoopConfig) error {
			fixture.configureFDs = append(fixture.configureFDs, loopFD)
			fixture.configs = append(fixture.configs, *config)
			call := len(fixture.configs) - 1
			if fixture.configureHook != nil {
				fixture.configureHook(call)
			}
			if call < len(fixture.configureErrors) {
				return fixture.configureErrors[call]
			}
			return nil
		},
		clear: func(ctx context.Context, fd int) error {
			fixture.clears = append(fixture.clears, fd)
			fixture.clearContexts = append(fixture.clearContexts, ctx)
			fixture.events = append(fixture.events, "clear."+strconv.Itoa(fd))
			return fixture.clearErrors[fd]
		},
		close: func(ctx context.Context, fd int) error {
			fixture.closes = append(fixture.closes, fd)
			fixture.closeContexts = append(fixture.closeContexts, ctx)
			fixture.events = append(fixture.events, "close."+strconv.Itoa(fd))
			return fixture.closeErrors[fd]
		},
	}
}

func loopControlStat() unix.Stat_t {
	return unix.Stat_t{Mode: unix.S_IFCHR | 0o600, Rdev: unix.Mkdev(10, 237)}
}

func loopDeviceStat(index int) unix.Stat_t {
	return unix.Stat_t{Mode: unix.S_IFBLK | 0o600, Rdev: unix.Mkdev(7, uint32(index))}
}

func cancelledRootfsLoopContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
