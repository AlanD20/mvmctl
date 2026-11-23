package jailer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestJailDestinationRetainsExactDescriptorRelativeBindings(t *testing.T) {
	t.Parallel()

	fixture := newJailDestinationFixture()
	lease, err := retainJailDestination(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	require.Len(t, lease.state.bindings, 5)
	wantKinds := []jailDestinationBindingKind{
		jailDestinationBase,
		jailDestinationFirecracker,
		jailDestinationVM,
		jailDestinationRoot,
		jailDestinationExecutable,
	}
	gotKinds := make([]jailDestinationBindingKind, 0, len(lease.state.bindings))
	for _, binding := range lease.state.bindings {
		gotKinds = append(gotKinds, binding.kind)
		assert.NotZero(t, binding.identity.device)
		assert.NotZero(t, binding.identity.inode)
	}
	if diff := cmp.Diff(wantKinds, gotKinds); diff != "" {
		t.Errorf("binding kinds mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, []string{
		"open:/", "openat:10:var", "openat:11:lib", "openat:12:mvmctl", "openat:13:jailer",
		"openat:14:firecracker", "openat:15:" + testVMID, "openat:16:root", "openat:17:firecracker",
	}, fixture.openEvents())

	require.NoError(t, lease.Release(t.Context()))
	if diff := cmp.Diff([]int{10, 11, 12, 13, 18, 17, 16, 15, 14}, fixture.closedSnapshot()); diff != "" {
		t.Errorf("descriptor close order mismatch (-want +got):\n%s", diff)
	}
}

func TestJailDestinationSharedBaseAdmission(t *testing.T) {
	t.Parallel()

	// Rationale: launch preparation and sealed-tree retention must both admit the shared state base
	// under ADR-0016 without repairing it or weakening the private Jailer ancestors.
	tests := map[string]struct {
		fd      int
		mode    uint32
		uid     uint32
		gid     uint32
		wantErr bool
	}{
		"shared non-root owner":         {fd: 13, mode: unix.S_IFDIR | 0755, uid: 1000, wantErr: true},
		"shared regular file":           {fd: 13, mode: unix.S_IFREG | 0700, wantErr: true},
		"shared symlink":                {fd: 13, mode: unix.S_IFLNK | 0777, wantErr: true},
		"shared group writable":         {fd: 13, mode: unix.S_IFDIR | 0770, wantErr: true},
		"shared world writable":         {fd: 13, mode: unix.S_IFDIR | 0702, wantErr: true},
		"private jailer mode 0755":      {fd: 14, mode: unix.S_IFDIR | 0755, wantErr: true},
		"private firecracker mode 0755": {fd: 15, mode: unix.S_IFDIR | 0755, wantErr: true},
		"shared mode 0755":              {fd: 13, mode: unix.S_IFDIR | 0755},
		"shared mode 0700":              {fd: 13, mode: unix.S_IFDIR | 0700},
		"shared non-root group":         {fd: 13, mode: unix.S_IFDIR | 0755, gid: 1000},
	}
	walkers := map[string]func(context.Context, *jailDestinationFixture) (func(context.Context) error, error){
		"retained": func(ctx context.Context, fixture *jailDestinationFixture) (func(context.Context) error, error) {
			lease, err := retainJailDestination(ctx, fixture.deps(), testVMID)
			if err != nil {
				return nil, err
			}
			return lease.Release, nil
		},
		"prepared": func(ctx context.Context, fixture *jailDestinationFixture) (func(context.Context) error, error) {
			prepared, err := prepareJailDestination(ctx, fixture.preparedDeps(), testVMID)
			if err != nil {
				return nil, err
			}
			return prepared.Release, nil
		},
	}
	for walkerName, walk := range walkers {
		t.Run(walkerName, func(t *testing.T) {
			t.Parallel()
			for name, tc := range tests {
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					fixture := newJailDestinationFixture()
					stat := fixture.stats[tc.fd]
					stat.Mode, stat.Uid, stat.Gid = tc.mode, tc.uid, tc.gid
					fixture.stats[tc.fd] = stat
					sharedBefore := fixture.stats[13]

					release, err := walk(t.Context(), fixture)
					if tc.wantErr {
						require.Error(t, err)
						assert.Equal(t, errs.CodeProcessError, errs.AsDomainError(err).Code)
						wantClosed := make([]int, 0, tc.fd-9)
						for fd := 10; fd <= tc.fd; fd++ {
							wantClosed = append(wantClosed, fd)
						}
						if walkerName == "retained" && tc.fd == 15 {
							// Retained private bindings close in reverse order after traversal descriptors.
							wantClosed = []int{10, 11, 12, 13, 15, 14}
						}
						if diff := cmp.Diff(wantClosed, fixture.closedSnapshot()); diff != "" {
							t.Errorf("rejected walker descriptor cleanup mismatch (-want +got):\n%s", diff)
						}
						return
					}
					require.NoError(t, err)
					require.NoError(t, release(t.Context()))
					if diff := cmp.Diff(sharedBefore, fixture.stats[13]); diff != "" {
						t.Errorf("shared state base metadata changed (-want +got):\n%s", diff)
					}
				})
			}
		})
	}
}

func TestJailDestinationRejectsMetadataDriftAndClosesEveryOpenedDescriptor(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*jailDestinationFixture){
		"Jailer base mode": func(fixture *jailDestinationFixture) {
			stat := fixture.stats[14]
			stat.Mode = unix.S_IFDIR | 0755
			fixture.stats[14] = stat
		},
		"VM owner": func(fixture *jailDestinationFixture) {
			stat := fixture.stats[16]
			stat.Uid = 1000
			fixture.stats[16] = stat
		},
		"root mode": func(fixture *jailDestinationFixture) {
			stat := fixture.stats[17]
			stat.Mode = unix.S_IFDIR | 0700
			fixture.stats[17] = stat
		},
		"copy link count": func(fixture *jailDestinationFixture) {
			stat := fixture.stats[18]
			stat.Nlink = 2
			fixture.stats[18] = stat
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newJailDestinationFixture()
			mutate(fixture)

			lease, err := retainJailDestination(t.Context(), fixture.deps(), testVMID)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.Equal(t, errs.CodeProcessError, errs.AsDomainError(err).Code)
			assert.NotEmpty(t, fixture.closedSnapshot())
		})
	}
}

func TestJailDestinationRejectsSubstitutedMounts(t *testing.T) {
	t.Parallel()

	for name, fd := range map[string]int{
		"fixed base": 14,
		"VM":         16,
		"root":       17,
		"executable": 18,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newJailDestinationFixture()
			fixture.mounts[fd] = 88

			lease, err := retainJailDestination(t.Context(), fixture.deps(), testVMID)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "mount")
			assert.NotEmpty(t, fixture.closedSnapshot())
		})
	}
}

func TestPreparedJailDestinationRejectsSubstitutedFixedAncestorMount(t *testing.T) {
	t.Parallel()

	for name, fd := range map[string]int{"jailer": 14, "firecracker": 15} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := newJailDestinationFixture()
			fixture.mounts[fd] = 88
			deps := fixture.preparedDeps()

			prepared, err := prepareJailDestination(t.Context(), deps, testVMID)
			assert.Nil(t, prepared)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "substituted mount")
		})
	}
}

func TestJailDestinationCopiesConcurrentlyCloseOnceAndCacheFailure(t *testing.T) {
	t.Parallel()

	fixture := newJailDestinationFixture()
	closeErr := errors.New("injected copied executable close failure")
	fixture.closeErr[18] = closeErr
	lease, err := retainJailDestination(t.Context(), fixture.deps(), testVMID)
	require.NoError(t, err)
	copied := *lease

	const callers = 12
	start := make(chan struct{})
	results := make(chan error, callers)
	for index := range callers {
		candidate := lease
		if index%2 == 1 {
			candidate = &copied
		}
		go func() {
			<-start
			results <- candidate.Release(t.Context())
		}()
	}
	close(start)
	first := <-results
	require.Error(t, first)
	assert.ErrorIs(t, first, closeErr)
	for range callers - 1 {
		assert.Same(t, first, <-results)
	}
	if diff := cmp.Diff([]int{10, 11, 12, 13, 18, 17, 16, 15, 14}, fixture.closedSnapshot()); diff != "" {
		t.Errorf("descriptor close order mismatch (-want +got):\n%s", diff)
	}
}

type jailDestinationFixture struct {
	mu       sync.Mutex
	nextFD   int
	stats    map[int]unix.Stat_t
	mounts   map[int]uint64
	opened   []string
	closed   []int
	closeErr map[int]error
}

func newJailDestinationFixture() *jailDestinationFixture {
	stats := make(map[int]unix.Stat_t)
	for fd := 10; fd <= 12; fd++ {
		stats[fd] = unix.Stat_t{Dev: 1, Ino: uint64(fd), Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0}
	}
	stats[13] = unix.Stat_t{Dev: 1, Ino: 13, Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0}
	stats[14] = unix.Stat_t{Dev: 1, Ino: 14, Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0}
	stats[15] = unix.Stat_t{Dev: 1, Ino: 15, Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0}
	stats[16] = unix.Stat_t{Dev: 1, Ino: 16, Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0}
	stats[17] = unix.Stat_t{Dev: 1, Ino: 17, Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0}
	stats[18] = unix.Stat_t{
		Dev: 1, Ino: 18, Size: 4096, Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 0, Nlink: 1,
	}
	mounts := make(map[int]uint64)
	for fd := 10; fd <= 18; fd++ {
		mounts[fd] = 77
	}
	return &jailDestinationFixture{nextFD: 10, stats: stats, mounts: mounts, closeErr: make(map[int]error)}
}

func (fixture *jailDestinationFixture) deps() jailDestinationDeps {
	return jailDestinationDeps{
		open: func(_ context.Context, name string, flags int, _ uint32) (int, error) {
			if flags&unix.O_NOFOLLOW == 0 {
				return -1, errors.New("jail root open must not follow symlinks")
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fd := fixture.nextFD
			fixture.nextFD++
			fixture.opened = append(fixture.opened, "open:"+name)
			return fd, nil
		},
		openAt: func(_ context.Context, directoryFD int, name string, flags int, _ uint32) (int, error) {
			if flags&unix.O_NOFOLLOW == 0 {
				return -1, errors.New("jail component open must not follow symlinks")
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fd := fixture.nextFD
			fixture.nextFD++
			fixture.opened = append(fixture.opened, fmt.Sprintf("openat:%d:%s", directoryFD, name))
			return fd, nil
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			*stat = fixture.stats[fd]
			return nil
		},
		fchmod: func(_ context.Context, fd int, mode uint32) error {
			stat := fixture.stats[fd]
			stat.Mode = stat.Mode&unix.S_IFMT | mode
			fixture.stats[fd] = stat
			return nil
		},
		fchown: func(_ context.Context, fd, uid, gid int) error {
			stat := fixture.stats[fd]
			if uid >= 0 {
				stat.Uid = uint32(uid)
			}
			if gid >= 0 {
				stat.Gid = uint32(gid)
			}
			fixture.stats[fd] = stat
			return nil
		},
		statx: func(_ context.Context, fd int, flags, mask int, stat *unix.Statx_t) error {
			wantFlags := unix.AT_EMPTY_PATH | unix.AT_NO_AUTOMOUNT | unix.AT_SYMLINK_NOFOLLOW |
				unix.AT_STATX_FORCE_SYNC
			if flags != wantFlags {
				return fmt.Errorf("unexpected statx flags %d", flags)
			}
			stat.Mask = uint32(mask)
			stat.Mnt_id = fixture.mounts[fd]
			return nil
		},
		close: func(_ context.Context, fd int) error {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fixture.closed = append(fixture.closed, fd)
			return fixture.closeErr[fd]
		},
	}
}

// preparedDeps models the fresh launch's absent VM tree beneath the fixed private parent.
func (fixture *jailDestinationFixture) preparedDeps() jailDestinationDeps {
	deps := fixture.deps()
	openAt := deps.openAt
	deps.openAt = func(ctx context.Context, parentFD int, leaf string, flags int, mode uint32) (int, error) {
		if parentFD == 15 && leaf == testVMID {
			if flags&unix.O_NOFOLLOW == 0 {
				return -1, errors.New("fresh jail probe must not follow symlinks")
			}
			return -1, unix.ENOENT
		}
		return openAt(ctx, parentFD, leaf, flags, mode)
	}
	return deps
}

func (fixture *jailDestinationFixture) openEvents() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]string{}, fixture.opened...)
}

func (fixture *jailDestinationFixture) closedSnapshot() []int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]int{}, fixture.closed...)
}
