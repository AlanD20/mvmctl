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
	opened   []string
	closed   []int
	closeErr map[int]error
}

func newJailDestinationFixture() *jailDestinationFixture {
	stats := make(map[int]unix.Stat_t)
	for fd := 10; fd <= 13; fd++ {
		stats[fd] = unix.Stat_t{Dev: 1, Ino: uint64(fd), Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0}
	}
	stats[14] = unix.Stat_t{Dev: 1, Ino: 14, Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0}
	stats[15] = unix.Stat_t{Dev: 1, Ino: 15, Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0}
	stats[16] = unix.Stat_t{Dev: 1, Ino: 16, Mode: unix.S_IFDIR | 0700, Uid: 0, Gid: 0}
	stats[17] = unix.Stat_t{Dev: 1, Ino: 17, Mode: unix.S_IFDIR | 0755, Uid: 0, Gid: 0}
	stats[18] = unix.Stat_t{
		Dev: 1, Ino: 18, Size: 4096, Mode: unix.S_IFREG | 0755, Uid: 0, Gid: 0, Nlink: 1,
	}
	return &jailDestinationFixture{nextFD: 10, stats: stats, closeErr: make(map[int]error)}
}

func (fixture *jailDestinationFixture) deps() jailDestinationDeps {
	return jailDestinationDeps{
		open: func(_ context.Context, name string, _ int, _ uint32) (int, error) {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fd := fixture.nextFD
			fixture.nextFD++
			fixture.opened = append(fixture.opened, "open:"+name)
			return fd, nil
		},
		openAt: func(_ context.Context, directoryFD int, name string, _ int, _ uint32) (int, error) {
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
		close: func(_ context.Context, fd int) error {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			fixture.closed = append(fixture.closed, fd)
			return fixture.closeErr[fd]
		},
	}
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
