package loopmount

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type rootfsPartitionContextKey struct{}

func TestDiscoverRootfsPartitionDevicesAdmitsRawOnlyLayout(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsPartitionFixture(3)
	parent := testRootfsPartitionParent(3, rootfsReadWrite)

	set, err := discoverRootfsPartitionDevices(t.Context(), fixture.dependencies(), parent)
	require.NoError(t, err)
	require.NotNil(t, set)
	require.NotNil(t, set.state)

	assert.Empty(t, set.state.devices)
	assert.Equal(t, 3, set.state.parentIndex)
	assert.Equal(t, rootfsBlockIdentity{major: 7, minor: 3}, set.state.parentIdentity)
	assert.Equal(t, 1, fixture.listCalls)
	assert.Equal(t, []int{rootfsPartitionDirectoryEntryLimit}, fixture.listLimits)
	assert.Equal(t, []testRootfsPartitionOpen{
		{path: rootfsSysfsBlockPath, flags: rootfsSysfsDirectoryOpenFlags},
		{path: "/sys/class/block/loop3/dev", flags: rootfsSysfsAttributeOpenFlags},
		{path: "/sys/class/block/loop3/dev", flags: rootfsSysfsAttributeOpenFlags},
	}, fixture.opens)
	assert.Equal(t, []string{
		"close:/sys/class/block/loop3/dev",
		"close:/sys/class/block/loop3/dev",
		"close:/sys/class/block",
	}, fixture.events)

	require.NoError(t, set.Release(t.Context()))
	assert.Len(t, fixture.events, 3)
}

func TestDiscoverRootfsPartitionDevicesUsesClosedAccessAndExactEvidence(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		access    rootfsAccess
		wantFlags int
	}{
		"read only": {
			access:    rootfsReadOnly,
			wantFlags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		},
		"read write": {
			access:    rootfsReadWrite,
			wantFlags: unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsPartitionFixture(12)
			fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 7}, 20)
			parent := testRootfsPartitionParent(12, test.access)

			set, err := discoverRootfsPartitionDevices(t.Context(), fixture.dependencies(), parent)
			require.NoError(t, err)
			require.NotNil(t, set)
			wantDevices := []rootfsPartitionDevice{{
				number: 1,
				fd:     20,
				identity: rootfsBlockIdentity{
					major: 259,
					minor: 7,
				},
			}}
			if diff := cmp.Diff(
				wantDevices,
				set.state.devices,
				cmp.AllowUnexported(rootfsPartitionDevice{}, rootfsBlockIdentity{}),
			); diff != "" {
				t.Errorf("admitted partition devices mismatch (-want +got):\n%s", diff)
			}
			assert.Equal(t, []testRootfsPartitionOpen{
				{path: rootfsSysfsBlockPath, flags: rootfsSysfsDirectoryOpenFlags},
				{path: "/sys/class/block/loop12/dev", flags: rootfsSysfsAttributeOpenFlags},
				{path: "/sys/class/block/loop12p1/partition", flags: rootfsSysfsAttributeOpenFlags},
				{path: "/sys/class/block/loop12p1/dev", flags: rootfsSysfsAttributeOpenFlags},
				{path: "/dev/loop12p1", flags: test.wantFlags},
				{path: "/sys/class/block/loop12p1/partition", flags: rootfsSysfsAttributeOpenFlags},
				{path: "/sys/class/block/loop12p1/dev", flags: rootfsSysfsAttributeOpenFlags},
				{path: "/sys/class/block/loop12/dev", flags: rootfsSysfsAttributeOpenFlags},
			}, fixture.opens)
			assert.Equal(t, 1, fixture.listCalls)

			require.NoError(t, set.Release(t.Context()))
			assert.Equal(t, []int{20}, fixture.deviceCloses)
		})
	}
}

func TestDiscoverRootfsPartitionDevicesAdmitsSparseAndMaximumLayouts(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		partitions []uint8
	}{
		"sparse":          {partitions: []uint8{1, 7, 16}},
		"exactly sixteen": {partitions: rootfsPartitionNumbers(16)},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsPartitionFixture(9)
			want := make([]rootfsPartitionDevice, 0, len(test.partitions))
			for _, partition := range test.partitions {
				identity := rootfsBlockIdentity{major: 259, minor: uint32(100 + partition)}
				fd := 40 + int(partition)
				fixture.addPartition(partition, identity, fd)
				want = append(want, rootfsPartitionDevice{number: partition, fd: fd, identity: identity})
			}
			// Directory order is deliberately reversed; admission order is the canonical partition number order.
			for left, right := 1, len(fixture.names)-1; left < right; left, right = left+1, right-1 {
				fixture.names[left], fixture.names[right] = fixture.names[right], fixture.names[left]
			}

			set, err := discoverRootfsPartitionDevices(
				t.Context(), fixture.dependencies(), testRootfsPartitionParent(9, rootfsReadWrite),
			)
			require.NoError(t, err)
			require.NotNil(t, set)
			if diff := cmp.Diff(
				want,
				set.state.devices,
				cmp.AllowUnexported(rootfsPartitionDevice{}, rootfsBlockIdentity{}),
			); diff != "" {
				t.Errorf("admitted partition devices mismatch (-want +got):\n%s", diff)
			}

			require.NoError(t, set.Release(t.Context()))
			wantClosed := make([]int, 0, len(want))
			for index := len(want) - 1; index >= 0; index-- {
				wantClosed = append(wantClosed, want[index].fd)
			}
			if diff := cmp.Diff(wantClosed, fixture.deviceCloses); diff != "" {
				t.Errorf("partition close order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDiscoverRootfsPartitionDevicesRejectsInvalidDirectoryEvidence(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		names   []string
		wantErr string
	}{
		"raw name is missing":       {names: []string{"loop3p1"}, wantErr: "raw loop device name is missing"},
		"raw name is duplicated":    {names: []string{"loop3", "loop3"}, wantErr: "raw loop device name is duplicated"},
		"partition suffix is empty": {names: []string{"loop3", "loop3p"}, wantErr: "partition name is malformed"},
		"partition suffix is text":  {names: []string{"loop3", "loop3px"}, wantErr: "partition name is malformed"},
		"partition is zero":         {names: []string{"loop3", "loop3p0"}, wantErr: "partition number is outside"},
		"partition has leading zero": {
			names: []string{"loop3", "loop3p01"}, wantErr: "partition name is malformed",
		},
		"partition exceeds bound": {
			names: []string{"loop3", "loop3p17"}, wantErr: "partition number is outside",
		},
		"partition number is duplicated": {
			names: []string{"loop3", "loop3p1", "loop3p1"}, wantErr: "partition number is duplicated",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsPartitionFixture(3)
			fixture.names = test.names

			set, err := discoverRootfsPartitionDevices(
				t.Context(), fixture.dependencies(), testRootfsPartitionParent(3, rootfsReadWrite),
			)
			assert.Nil(t, set)
			requireRootfsPartitionContractError(t, err, test.wantErr)
			assert.Equal(t, 1, fixture.listCalls)
			assert.Empty(t, fixture.deviceOpens)
		})
	}
}

func TestDiscoverRootfsPartitionDevicesRejectsInvalidKernelEvidence(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate  func(*testRootfsPartitionFixture)
		wantErr string
	}{
		"raw identity differs from parent": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.attributes["/sys/class/block/loop3/dev"] = [][]byte{[]byte("7:4\n")}
			},
			wantErr: "raw loop identity does not match",
		},
		"partition attribute differs from name": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.attributes["/sys/class/block/loop3p1/partition"] = [][]byte{[]byte("2\n")}
			},
			wantErr: "partition attribute does not match",
		},
		"partition identity reuses raw device": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.attributes["/sys/class/block/loop3p1/dev"] = [][]byte{[]byte("7:3\n")}
			},
			wantErr: "partition identity matches the raw loop device",
		},
		"partition identities are duplicated": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.addPartition(2, rootfsBlockIdentity{major: 259, minor: 1}, 21)
			},
			wantErr: "partition identity is duplicated",
		},
		"partition device is not block": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.deviceStats["/dev/loop3p1"] = unix.Stat_t{Mode: unix.S_IFREG | 0o600}
			},
			wantErr: "not the admitted partition block device",
		},
		"partition device identity differs from sysfs": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.deviceStats["/dev/loop3p1"] = partitionDeviceStat(rootfsBlockIdentity{major: 259, minor: 2})
			},
			wantErr: "not the admitted partition block device",
		},
		"partition device symlink is rejected": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.openErrors["/dev/loop3p1"] = unix.ELOOP
			},
			wantErr: "open admitted partition device",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsPartitionFixture(3)
			fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
			test.mutate(fixture)

			set, err := discoverRootfsPartitionDevices(
				t.Context(), fixture.dependencies(), testRootfsPartitionParent(3, rootfsReadWrite),
			)
			assert.Nil(t, set)
			requireRootfsPartitionContractError(t, err, test.wantErr)
		})
	}
}

func TestDiscoverRootfsPartitionDevicesRejectsEvidenceChangedAfterOpen(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate  func(*testRootfsPartitionFixture)
		wantErr string
	}{
		"partition number changed": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.attributes["/sys/class/block/loop3p1/partition"] = [][]byte{[]byte("1\n"), []byte("2\n")}
			},
			wantErr: "partition evidence changed after open",
		},
		"partition identity changed": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.attributes["/sys/class/block/loop3p1/dev"] = [][]byte{
					[]byte("259:1\n"), []byte("259:2\n"),
				}
			},
			wantErr: "partition evidence changed after open",
		},
		"raw identity changed": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.attributes["/sys/class/block/loop3/dev"] = [][]byte{[]byte("7:3\n"), []byte("7:4\n")}
			},
			wantErr: "raw loop evidence changed after open",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsPartitionFixture(3)
			fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
			test.mutate(fixture)

			set, err := discoverRootfsPartitionDevices(
				t.Context(), fixture.dependencies(), testRootfsPartitionParent(3, rootfsReadWrite),
			)
			assert.Nil(t, set)
			requireRootfsPartitionContractError(t, err, test.wantErr)
			assert.Equal(t, []int{20}, fixture.deviceCloses)
		})
	}
}

// Rationale: Invalid or inactive authority must fail before root opens any sysfs or device object.
func TestDiscoverRootfsPartitionDevicesRejectsInvalidAuthorityBeforeEffects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		ctx        func() context.Context
		mutateDeps func(*rootfsPartitionDiscoveryDeps)
		parent     *rootfsLoopDevice
		wantErr    string
	}{
		"nil context": {
			parent: testRootfsPartitionParent(3, rootfsReadWrite), wantErr: "context is required",
		},
		"cancelled context": {
			ctx:    func() context.Context { return cancelledRootfsLoopContext() },
			parent: testRootfsPartitionParent(3, rootfsReadWrite), wantErr: "context ended",
		},
		"incomplete dependencies": {
			ctx: func() context.Context { return t.Context() },
			mutateDeps: func(deps *rootfsPartitionDiscoveryDeps) {
				*deps = rootfsPartitionDiscoveryDeps{}
			},
			parent: testRootfsPartitionParent(3, rootfsReadWrite), wantErr: "dependencies are incomplete",
		},
		"nil parent": {
			ctx: func() context.Context { return t.Context() }, wantErr: "loop device is missing",
		},
		"zero parent": {
			ctx:    func() context.Context { return t.Context() },
			parent: &rootfsLoopDevice{}, wantErr: "loop device is inactive",
		},
		"released parent": {
			ctx: func() context.Context { return t.Context() },
			parent: &rootfsLoopDevice{state: &rootfsLoopDeviceState{
				fd: -1, index: -1, released: true,
			}}, wantErr: "loop device is inactive",
		},
		"invalid parent descriptor": {
			ctx: func() context.Context { return t.Context() },
			parent: &rootfsLoopDevice{state: &rootfsLoopDeviceState{
				fd: -1, index: 3, identity: rootfsBlockIdentity{major: 7, minor: 3}, access: rootfsReadWrite,
			}}, wantErr: "loop device state is invalid",
		},
		"invalid parent access": {
			ctx: func() context.Context { return t.Context() },
			parent: &rootfsLoopDevice{state: &rootfsLoopDeviceState{
				fd: 50, index: 3, identity: rootfsBlockIdentity{major: 7, minor: 3},
			}}, wantErr: "loop device access is invalid",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsPartitionFixture(3)
			deps := fixture.dependencies()
			if test.mutateDeps != nil {
				test.mutateDeps(&deps)
			}
			var ctx context.Context
			if test.ctx != nil {
				ctx = test.ctx()
			}
			set, err := discoverRootfsPartitionDevices(ctx, deps, test.parent)
			assert.Nil(t, set)
			requireRootfsPartitionContractError(t, err, test.wantErr)
			assert.Empty(t, fixture.opens)
			assert.Zero(t, fixture.listCalls)
			assert.Empty(t, fixture.deviceCloses)
		})
	}
}

func TestDiscoverRootfsPartitionDevicesClosesPartialAdmissionInReverse(t *testing.T) {
	t.Parallel()

	closeCause := errors.New("close first partition failed")
	fixture := newTestRootfsPartitionFixture(3)
	fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
	fixture.addPartition(2, rootfsBlockIdentity{major: 259, minor: 2}, 21)
	fixture.addPartition(3, rootfsBlockIdentity{major: 259, minor: 3}, 22)
	fixture.openErrors["/dev/loop3p3"] = unix.EACCES
	fixture.closeErrors["/dev/loop3p1"] = closeCause
	parent := testRootfsPartitionParent(3, rootfsReadWrite)

	set, err := discoverRootfsPartitionDevices(
		t.Context(), fixture.dependencies(), parent,
	)
	assert.Nil(t, set)
	require.ErrorIs(t, err, unix.EACCES)
	require.ErrorIs(t, err, closeCause)
	assert.Equal(t, []int{21, 20}, fixture.deviceCloses)
	assert.False(t, parent.state.released)
	assert.Equal(t, 50, parent.state.fd)
}

func TestDiscoverRootfsPartitionDevicesStopsAfterCancellationBetweenEntries(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	fixture := newTestRootfsPartitionFixture(3)
	fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
	fixture.addPartition(2, rootfsBlockIdentity{major: 259, minor: 2}, 21)
	fixture.readHook = func(path string, openCall int) {
		if path == "/sys/class/block/loop3/dev" && openCall == 1 {
			cancel()
		}
	}

	set, err := discoverRootfsPartitionDevices(
		ctx,
		fixture.dependencies(),
		testRootfsPartitionParent(3, rootfsReadWrite),
	)
	assert.Nil(t, set)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []int{20}, fixture.deviceCloses)
	assert.Equal(t, []string{"/dev/loop3p1"}, fixture.deviceOpens)
}

func TestDiscoverRootfsPartitionDevicesReturnsLeaseWhenDirectoryCloseFails(t *testing.T) {
	t.Parallel()

	closeCause := errors.New("directory close failed")
	fixture := newTestRootfsPartitionFixture(3)
	fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
	fixture.closeErrors[rootfsSysfsBlockPath] = closeCause

	set, err := discoverRootfsPartitionDevices(
		t.Context(), fixture.dependencies(), testRootfsPartitionParent(3, rootfsReadWrite),
	)
	require.NotNil(t, set)
	require.ErrorIs(t, err, closeCause)
	assert.ErrorContains(t, err, "close sysfs block directory")
	assert.Empty(t, fixture.deviceCloses)

	require.NoError(t, set.Release(t.Context()))
	assert.Equal(t, []int{20}, fixture.deviceCloses)
}

func TestDiscoverRootfsPartitionDevicesSurfacesInfrastructureFaults(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate  func(*testRootfsPartitionFixture)
		wantErr string
	}{
		"directory open": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.openErrors[rootfsSysfsBlockPath] = unix.EACCES
			},
			wantErr: "open sysfs block directory",
		},
		"directory stat": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.statErrors[rootfsSysfsBlockPath] = unix.EIO
			},
			wantErr: "inspect sysfs block directory",
		},
		"directory is not a directory": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.directoryStat = unix.Stat_t{Mode: unix.S_IFREG | 0o444}
			},
			wantErr: "not a directory",
		},
		"directory list": {
			mutate:  func(fixture *testRootfsPartitionFixture) { fixture.listErr = unix.EIO },
			wantErr: "enumerate sysfs block devices",
		},
		"attribute open": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.openErrors["/sys/class/block/loop3/dev"] = unix.EACCES
			},
			wantErr: "open sysfs block attribute",
		},
		"listed attribute is missing": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
				delete(fixture.attributes, "/sys/class/block/loop3p1/partition")
			},
			wantErr: "open sysfs block attribute",
		},
		"attribute stat": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.statErrors["/sys/class/block/loop3/dev"] = unix.EIO
			},
			wantErr: "inspect sysfs block attribute",
		},
		"attribute is not regular": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.attributeStats["/sys/class/block/loop3/dev"] = unix.Stat_t{Mode: unix.S_IFLNK | 0o777}
			},
			wantErr: "sysfs block attribute is not regular",
		},
		"attribute read": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.readErrors["/sys/class/block/loop3/dev"] = unix.EIO
			},
			wantErr: "read sysfs block attribute",
		},
		"attribute close": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.closeErrors["/sys/class/block/loop3/dev"] = unix.EIO
			},
			wantErr: "close sysfs block attribute",
		},
		"partition device open": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
				fixture.openErrors["/dev/loop3p1"] = unix.EACCES
			},
			wantErr: "open admitted partition device",
		},
		"listed partition device is missing": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
				delete(fixture.deviceFDs, "/dev/loop3p1")
			},
			wantErr: "open admitted partition device",
		},
		"partition device stat": {
			mutate: func(fixture *testRootfsPartitionFixture) {
				fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
				fixture.statErrors["/dev/loop3p1"] = unix.EIO
			},
			wantErr: "inspect admitted partition device",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsPartitionFixture(3)
			test.mutate(fixture)

			set, err := discoverRootfsPartitionDevices(
				t.Context(), fixture.dependencies(), testRootfsPartitionParent(3, rootfsReadWrite),
			)
			assert.Nil(t, set)
			requireRootfsPartitionContractError(t, err, test.wantErr)
		})
	}
}

func TestDiscoverRootfsPartitionDevicesRejectsMalformedOrUnboundedAttributes(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		content    []byte
		noProgress bool
		wantErr    string
	}{
		"missing newline": {content: []byte("7:3"), wantErr: "identity attribute is malformed"},
		"extra newline":   {content: []byte("7:3\n\n"), wantErr: "identity attribute is malformed"},
		"leading zero":    {content: []byte("07:3\n"), wantErr: "identity attribute is malformed"},
		"whitespace":      {content: []byte("7:3 \n"), wantErr: "identity attribute is malformed"},
		"oversized": {
			content: []byte(strings.Repeat("1", maxRootfsSysfsAttributeBytes+1)),
			wantErr: "exceeds the supported byte bound",
		},
		"zero progress": {content: []byte("7:3\n"), noProgress: true, wantErr: "made no progress"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTestRootfsPartitionFixture(3)
			fixture.attributes["/sys/class/block/loop3/dev"] = [][]byte{test.content}
			fixture.readNoProgress["/sys/class/block/loop3/dev"] = test.noProgress

			set, err := discoverRootfsPartitionDevices(
				t.Context(), fixture.dependencies(), testRootfsPartitionParent(3, rootfsReadWrite),
			)
			assert.Nil(t, set)
			requireRootfsPartitionContractError(t, err, test.wantErr)
		})
	}
}

func TestRootfsPartitionSetReleaseIsSharedCheckedAndCancellationIndependent(t *testing.T) {
	t.Parallel()

	firstCause := errors.New("close partition two failed")
	secondCause := errors.New("close partition one failed")
	fixture := newTestRootfsPartitionFixture(3)
	fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
	fixture.addPartition(2, rootfsBlockIdentity{major: 259, minor: 2}, 21)
	fixture.closeErrors["/dev/loop3p2"] = firstCause
	fixture.closeErrors["/dev/loop3p1"] = secondCause
	set := testRootfsPartitionSet(fixture, 3, []rootfsPartitionDevice{
		{number: 1, fd: 20, identity: rootfsBlockIdentity{major: 259, minor: 1}},
		{number: 2, fd: 21, identity: rootfsBlockIdentity{major: 259, minor: 2}},
	})
	copyOfSet := *set
	value := &struct{}{}
	base := context.WithValue(context.Background(), rootfsPartitionContextKey{}, value)
	cleanupCtx, cancel := context.WithCancel(base)
	cancel()

	err := set.Release(cleanupCtx)
	require.ErrorIs(t, err, firstCause)
	require.ErrorIs(t, err, secondCause)
	assert.Equal(t, []int{21, 20}, fixture.deviceCloses)
	for _, observed := range fixture.closeContexts {
		assert.NoError(t, observed.Err())
		assert.Same(t, value, observed.Value(rootfsPartitionContextKey{}))
	}

	secondErr := copyOfSet.Release(t.Context())
	assert.Same(t, err, secondErr)
	require.ErrorIs(t, secondErr, firstCause)
	require.ErrorIs(t, secondErr, secondCause)
	assert.Equal(t, []int{21, 20}, fixture.deviceCloses)
}

// Rationale: A copied capability must not close reused descriptors twice; all copies share one release transition.
func TestRootfsPartitionSetReleaseSerializesConcurrentCopies(t *testing.T) {
	t.Parallel()

	fixture := newTestRootfsPartitionFixture(3)
	fixture.addPartition(1, rootfsBlockIdentity{major: 259, minor: 1}, 20)
	set := testRootfsPartitionSet(fixture, 3, []rootfsPartitionDevice{{
		number: 1, fd: 20, identity: rootfsBlockIdentity{major: 259, minor: 1},
	}})

	const callers = 32
	start := make(chan struct{})
	results := make(chan error, callers)
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		copyOfSet := *set
		go func() {
			ready.Done()
			<-start
			results <- copyOfSet.Release(t.Context())
		}()
	}
	ready.Wait()
	close(start)
	for range callers {
		require.NoError(t, <-results)
	}
	assert.Equal(t, []int{20}, fixture.deviceCloses)
}

func TestRootfsPartitionSetReleaseHandlesNilAndZeroState(t *testing.T) {
	t.Parallel()

	var nilSet *rootfsPartitionSet
	require.NoError(t, nilSet.Release(t.Context()))
	require.Error(t, (&rootfsPartitionSet{}).Release(t.Context()))
}

func TestRootfsLoopPartitionDevicePathRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		loopIndex int
		partition uint8
		want      string
		wantErr   string
	}{
		"negative loop index": {loopIndex: -1, partition: 1, wantErr: "loop device index"},
		"large loop index": {
			loopIndex: maxRootfsLoopDeviceIndex + 1, partition: 1, wantErr: "loop device index",
		},
		"zero partition": {loopIndex: 3, wantErr: "partition number"},
		"partition above bound": {
			loopIndex: 3, partition: maxRootfsPartitionCount + 1, wantErr: "partition number",
		},
		"valid maximums": {
			loopIndex: maxRootfsLoopDeviceIndex, partition: maxRootfsPartitionCount,
			want: "/dev/loop1048575p16",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := rootfsLoopPartitionDevicePath(test.loopIndex, test.partition)
			if test.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

type testRootfsPartitionOpen struct {
	path  string
	flags int
}

type testRootfsPartitionReadState struct {
	path   string
	value  []byte
	offset int
}

type testRootfsPartitionFixture struct {
	names           []string
	attributes      map[string][][]byte
	attributeOpens  map[string]int
	attributeStats  map[string]unix.Stat_t
	deviceFDs       map[string]int
	deviceStats     map[string]unix.Stat_t
	openErrors      map[string]error
	statErrors      map[string]error
	readErrors      map[string]error
	readNoProgress  map[string]bool
	closeErrors     map[string]error
	readHook        func(string, int)
	listErr         error
	listCalls       int
	listLimits      []int
	opens           []testRootfsPartitionOpen
	deviceOpens     []string
	deviceCloses    []int
	closeContexts   []context.Context
	events          []string
	fdPaths         map[int]string
	reads           map[int]*testRootfsPartitionReadState
	nextAttributeFD int
	directoryStat   unix.Stat_t
}

func newTestRootfsPartitionFixture(loopIndex int) *testRootfsPartitionFixture {
	rawName := "loop" + strconv.Itoa(loopIndex)
	rawDevPath := rootfsSysfsBlockPath + "/" + rawName + "/dev"
	return &testRootfsPartitionFixture{
		names:           []string{rawName},
		attributes:      map[string][][]byte{rawDevPath: {[]byte("7:" + strconv.Itoa(loopIndex) + "\n")}},
		attributeOpens:  make(map[string]int),
		attributeStats:  make(map[string]unix.Stat_t),
		deviceFDs:       make(map[string]int),
		deviceStats:     make(map[string]unix.Stat_t),
		openErrors:      make(map[string]error),
		statErrors:      make(map[string]error),
		readErrors:      make(map[string]error),
		readNoProgress:  make(map[string]bool),
		closeErrors:     make(map[string]error),
		fdPaths:         map[int]string{10: rootfsSysfsBlockPath},
		reads:           make(map[int]*testRootfsPartitionReadState),
		nextAttributeFD: 100,
		directoryStat:   unix.Stat_t{Mode: unix.S_IFDIR | 0o755},
	}
}

func (fixture *testRootfsPartitionFixture) addPartition(
	partition uint8,
	identity rootfsBlockIdentity,
	fd int,
) {
	name := "loop3p" + strconv.Itoa(int(partition))
	if len(fixture.names) > 0 {
		raw := fixture.names[0]
		if raw != "" {
			name = raw + "p" + strconv.Itoa(int(partition))
		}
	}
	fixture.names = append(fixture.names, name)
	base := rootfsSysfsBlockPath + "/" + name
	fixture.attributes[base+"/partition"] = [][]byte{[]byte(strconv.Itoa(int(partition)) + "\n")}
	fixture.attributes[base+"/dev"] = [][]byte{
		[]byte(
			strconv.FormatUint(
				uint64(identity.major),
				10,
			) + ":" + strconv.FormatUint(
				uint64(identity.minor),
				10,
			) + "\n",
		),
	}
	path := "/dev/" + name
	fixture.deviceFDs[path] = fd
	fixture.deviceStats[path] = partitionDeviceStat(identity)
	fixture.fdPaths[fd] = path
}

func (fixture *testRootfsPartitionFixture) dependencies() rootfsPartitionDiscoveryDeps {
	return rootfsPartitionDiscoveryDeps{
		open: func(_ context.Context, path string, flags int, _ uint32) (int, error) {
			fixture.opens = append(fixture.opens, testRootfsPartitionOpen{path: path, flags: flags})
			if err := fixture.openErrors[path]; err != nil {
				return -1, err
			}
			if path == rootfsSysfsBlockPath {
				return 10, nil
			}
			if fd, exists := fixture.deviceFDs[path]; exists {
				fixture.deviceOpens = append(fixture.deviceOpens, path)
				return fd, nil
			}
			values, exists := fixture.attributes[path]
			if !exists || len(values) == 0 {
				return -1, unix.ENOENT
			}
			call := fixture.attributeOpens[path]
			fixture.attributeOpens[path] = call + 1
			value := values[len(values)-1]
			if call < len(values) {
				value = values[call]
			}
			fd := fixture.nextAttributeFD
			fixture.nextAttributeFD++
			fixture.fdPaths[fd] = path
			fixture.reads[fd] = &testRootfsPartitionReadState{path: path, value: value}
			return fd, nil
		},
		list: func(_ context.Context, fd int, limit int) ([]string, error) {
			fixture.listCalls++
			fixture.listLimits = append(fixture.listLimits, limit)
			if fd != 10 {
				return nil, unix.EBADF
			}
			if fixture.listErr != nil {
				return nil, fixture.listErr
			}
			return append([]string(nil), fixture.names...), nil
		},
		read: func(_ context.Context, fd int, buffer []byte) (int, error) {
			state, exists := fixture.reads[fd]
			if !exists {
				return 0, unix.EBADF
			}
			if err := fixture.readErrors[state.path]; err != nil {
				return 0, err
			}
			if fixture.readNoProgress[state.path] {
				return 0, nil
			}
			if state.offset == len(state.value) {
				return 0, io.EOF
			}
			remaining := len(state.value) - state.offset
			count := min(remaining, len(buffer), 2)
			copy(buffer, state.value[state.offset:state.offset+count])
			state.offset += count
			if fixture.readHook != nil && state.offset == len(state.value) {
				fixture.readHook(state.path, fixture.attributeOpens[state.path]-1)
			}
			return count, nil
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			path, exists := fixture.fdPaths[fd]
			if !exists {
				return unix.EBADF
			}
			if err := fixture.statErrors[path]; err != nil {
				return err
			}
			switch {
			case path == rootfsSysfsBlockPath:
				*stat = fixture.directoryStat
			case fixture.reads[fd] != nil:
				value, exists := fixture.attributeStats[path]
				if !exists {
					value = unix.Stat_t{Mode: unix.S_IFREG | 0o444}
				}
				*stat = value
			default:
				value, exists := fixture.deviceStats[path]
				if !exists {
					return unix.EBADF
				}
				*stat = value
			}
			return nil
		},
		close: func(ctx context.Context, fd int) error {
			path, exists := fixture.fdPaths[fd]
			if !exists {
				return unix.EBADF
			}
			fixture.closeContexts = append(fixture.closeContexts, ctx)
			fixture.events = append(fixture.events, "close:"+path)
			if _, device := fixture.deviceFDs[path]; device {
				fixture.deviceCloses = append(fixture.deviceCloses, fd)
			}
			return fixture.closeErrors[path]
		},
	}
}

func testRootfsPartitionParent(index int, access rootfsAccess) *rootfsLoopDevice {
	return &rootfsLoopDevice{state: &rootfsLoopDeviceState{
		fd:       50,
		index:    index,
		identity: rootfsBlockIdentity{major: 7, minor: uint32(index)},
		access:   access,
	}}
}

func testRootfsPartitionSet(
	fixture *testRootfsPartitionFixture,
	parentIndex int,
	devices []rootfsPartitionDevice,
) *rootfsPartitionSet {
	return &rootfsPartitionSet{state: &rootfsPartitionSetState{
		deps:           fixture.dependencies(),
		parentIndex:    parentIndex,
		parentIdentity: rootfsBlockIdentity{major: 7, minor: uint32(parentIndex)},
		devices:        devices,
	}}
}

func partitionDeviceStat(identity rootfsBlockIdentity) unix.Stat_t {
	return unix.Stat_t{Mode: unix.S_IFBLK | 0o600, Rdev: unix.Mkdev(identity.major, identity.minor)}
}

func rootfsPartitionNumbers(count int) []uint8 {
	numbers := make([]uint8, count)
	for index := range count {
		numbers[index] = uint8(index + 1)
	}
	return numbers
}

func requireRootfsPartitionContractError(t *testing.T, err error, contains string) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorContains(t, err, contains)
	assert.Equal(t, errs.CodeLoopMountError, errs.AsDomainError(err).Code)
}
