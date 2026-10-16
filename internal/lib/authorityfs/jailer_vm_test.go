package authorityfs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestJailerVMTopologyHoldBindsOwnerAndKey(t *testing.T) {
	t.Parallel()

	_, _, authority, key := openTestJailerVM(t, realDependencies())
	hold, err := authority.RetainTopology(t.Context())
	require.NoError(t, err)
	copyOfHold := *hold

	ownerUID, boundKey, err := hold.Binding()
	require.NoError(t, err)
	assert.Equal(t, testUID, ownerUID)
	assert.Equal(t, key.value, boundKey.value)

	require.NoError(t, hold.Release(t.Context()))
	ownerUID, boundKey, err = copyOfHold.Binding()
	assert.Zero(t, ownerUID)
	assert.Empty(t, boundKey.value)
	require.Error(t, err)
	require.NoError(t, authority.Release(t.Context()))
}

func TestJailerVMTopologyHoldRejectsInactiveStates(t *testing.T) {
	t.Parallel()

	var zero JailerVM
	hold, err := zero.RetainTopology(t.Context())
	assert.Nil(t, hold)
	require.Error(t, err)
	require.NoError(t, zero.Release(t.Context()))

	var zeroHold TopologyHold
	ownerUID, key, err := zeroHold.Binding()
	assert.Zero(t, ownerUID)
	assert.Empty(t, key.value)
	require.Error(t, err)
	require.NoError(t, zeroHold.Release(t.Context()))

	_, _, authority, _ := openTestJailerVM(t, realDependencies())
	require.NoError(t, authority.Release(t.Context()))
	hold, err = authority.RetainTopology(t.Context())
	assert.Nil(t, hold)
	require.Error(t, err)

	var nilAuthority *JailerVM
	hold, err = nilAuthority.RetainTopology(t.Context())
	assert.Nil(t, hold)
	require.Error(t, err)
}

func TestJailerVMRetainTopologyRejectsHoldLimitWithoutWrappingOrUnlocking(t *testing.T) {
	t.Parallel()

	var unlockCalls atomic.Int32
	lock := testJailerVMLock(&unlockCalls)
	authority := &JailerVM{state: &jailerVMState{
		lock: lock, ownerUID: testUID, topologyHolds: ^uint64(0),
	}}

	hold, err := authority.RetainTopology(t.Context())
	assert.Nil(t, hold)
	require.Error(t, err)
	assert.Equal(t, ^uint64(0), authority.state.topologyHolds)
	assert.False(t, authority.state.topologyBroken)
	releaseErr := authority.Release(t.Context())
	require.Error(t, releaseErr)
	assert.ErrorIs(t, releaseErr, ErrTopologyRetained)
	assert.Equal(t, int32(0), unlockCalls.Load())
	assert.Same(t, lock, authority.state.lock)

	authority.state.mu.Lock()
	authority.state.topologyHolds = 0
	authority.state.mu.Unlock()
	require.NoError(t, authority.Release(t.Context()))
	assert.Equal(t, int32(1), unlockCalls.Load())
}

func TestTopologyHoldReleaseRejectsOrphanAndCachesErrorAcrossCopies(t *testing.T) {
	t.Parallel()

	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	hold := &TopologyHold{state: &topologyHoldState{ownerUID: testUID, key: key}}
	copyOfHold := *hold

	ownerUID, boundKey, bindErr := hold.Binding()
	assert.Zero(t, ownerUID)
	assert.Empty(t, boundKey.value)
	require.Error(t, bindErr)
	first := hold.Release(t.Context())
	second := copyOfHold.Release(t.Context())
	require.Error(t, first)
	assert.Same(t, first, second)
	assert.Zero(t, hold.state.ownerUID)
	assert.Empty(t, hold.state.key.value)
}

func TestTopologyHoldReleaseRejectsCounterUnderflowWithoutUnlockingParent(t *testing.T) {
	t.Parallel()

	var unlockCalls atomic.Int32
	lock := testJailerVMLock(&unlockCalls)
	parent := &jailerVMState{lock: lock, ownerUID: testUID}
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	hold := &TopologyHold{state: &topologyHoldState{
		parent: parent, ownerUID: testUID, key: key,
	}}
	copyOfHold := *hold

	first := hold.Release(t.Context())
	second := copyOfHold.Release(t.Context())
	require.Error(t, first)
	assert.Same(t, first, second)
	assert.Zero(t, parent.topologyHolds)
	assert.True(t, parent.topologyBroken)
	parentReleaseErr := (&JailerVM{state: parent}).Release(t.Context())
	require.Error(t, parentReleaseErr)
	assert.ErrorIs(t, parentReleaseErr, ErrTopologyRetained)
	assert.Equal(t, int32(0), unlockCalls.Load())
	assert.Same(t, lock, parent.lock)

	require.NoError(t, lock.Release(t.Context()))
}

func TestJailerVMReleaseRejectsMissingUnderlyingLockAndCachesError(t *testing.T) {
	t.Parallel()

	authority := &JailerVM{state: &jailerVMState{ownerUID: testUID}}
	copyOfAuthority := *authority
	first := authority.Release(t.Context())
	second := copyOfAuthority.Release(t.Context())
	require.Error(t, first)
	assert.Same(t, first, second)
}

func TestJailerVMReleaseRefusesWhileTopologyIsRetained(t *testing.T) {
	t.Parallel()

	root, uid, authority, key := openTestJailerVM(t, realDependencies())
	hold, err := authority.RetainTopology(t.Context())
	require.NoError(t, err)

	err = authority.Release(t.Context())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTopologyRetained)

	reopened, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Release(context.Background())) })
	reopenedUID, err := reopened.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopenedUID.Release(context.Background())) })
	waitCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	blocked, err := reopenedUID.AcquireVM(waitCtx, key)
	assert.Nil(t, blocked)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	require.NoError(t, hold.Release(t.Context()))
	require.NoError(t, authority.Release(t.Context()))
	reacquired, err := reopenedUID.AcquireVM(t.Context(), key)
	require.NoError(t, err)
	require.NoError(t, reacquired.Release(t.Context()))
	require.NoError(t, uid.Release(t.Context()))
}

func TestTopologyHoldCopiesAndConcurrentReleaseDropOneHold(t *testing.T) {
	t.Parallel()

	_, _, authority, _ := openTestJailerVM(t, realDependencies())
	hold, err := authority.RetainTopology(t.Context())
	require.NoError(t, err)
	copyOfHold := *hold

	const callers = 32
	results := make(chan error, callers)
	for index := 0; index < callers; index++ {
		candidate := hold
		if index%2 == 0 {
			candidate = &copyOfHold
		}
		go func() {
			results <- candidate.Release(t.Context())
		}()
	}
	for range callers {
		require.NoError(t, <-results)
	}
	require.NoError(t, authority.Release(t.Context()))
}

func TestJailerVMCopiesConcurrentlyRetainIndependentTopologyHolds(t *testing.T) {
	t.Parallel()

	_, _, authority, _ := openTestJailerVM(t, realDependencies())
	copyOfAuthority := *authority
	const holds = 32
	topologies := make(chan *TopologyHold, holds)
	errorsSeen := make(chan error, holds)
	for index := range holds {
		candidate := authority
		if index%2 == 1 {
			candidate = &copyOfAuthority
		}
		go func() {
			topology, err := candidate.RetainTopology(t.Context())
			topologies <- topology
			errorsSeen <- err
		}()
	}
	retained := make([]*TopologyHold, 0, holds)
	for range holds {
		require.NoError(t, <-errorsSeen)
		topology := <-topologies
		require.NotNil(t, topology)
		retained = append(retained, topology)
	}
	releaseErr := authority.Release(t.Context())
	require.Error(t, releaseErr)
	assert.ErrorIs(t, releaseErr, ErrTopologyRetained)

	results := make(chan error, holds)
	for _, topology := range retained {
		go func() {
			results <- topology.Release(t.Context())
		}()
	}
	for range holds {
		require.NoError(t, <-results)
	}
	require.NoError(t, copyOfAuthority.Release(t.Context()))
}

func TestJailerVMConcurrentRetainAndReleaseSerialize(t *testing.T) {
	t.Parallel()

	root := prepareTestRoot(t)
	jailer, err := openJailerWithPolicy(t.Context(), realDependencies(), testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })
	uid, err := jailer.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uid.Release(context.Background())) })
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)

	for range 32 {
		authority, acquireErr := uid.AcquireVM(t.Context(), key)
		require.NoError(t, acquireErr)
		start := make(chan struct{})
		holdResult := make(chan *TopologyHold, 1)
		retainErr := make(chan error, 1)
		releaseErr := make(chan error, 1)
		go func() {
			<-start
			hold, retainResultErr := authority.RetainTopology(t.Context())
			holdResult <- hold
			retainErr <- retainResultErr
		}()
		go func() {
			<-start
			releaseErr <- authority.Release(t.Context())
		}()
		close(start)

		hold := <-holdResult
		retainResultErr := <-retainErr
		releaseResultErr := <-releaseErr
		if hold == nil {
			require.Error(t, retainResultErr)
			require.NoError(t, releaseResultErr)
			continue
		}
		require.NoError(t, retainResultErr)
		require.Error(t, releaseResultErr)
		assert.ErrorIs(t, releaseResultErr, ErrTopologyRetained)
		require.NoError(t, hold.Release(t.Context()))
		require.NoError(t, authority.Release(t.Context()))
	}
}

func TestJailerVMReleaseIgnoresCancellationForChildAndParentCleanup(t *testing.T) {
	t.Parallel()

	_, _, authority, _ := openTestJailerVM(t, realDependencies())
	hold, err := authority.RetainTopology(t.Context())
	require.NoError(t, err)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, hold.Release(cancelled))
	require.NoError(t, authority.Release(cancelled))
	require.NoError(t, authority.Release(cancelled))
}

func TestJailerVMReleaseCachesUnderlyingUnlockAndCloseErrors(t *testing.T) {
	t.Parallel()

	deps := realDependencies()
	realOpenAt2 := deps.openAt2
	realFlock := deps.flock
	realClose := deps.close
	var vmFD atomic.Int32
	vmFD.Store(-1)
	var unlockCalls atomic.Int32
	var closeCalls atomic.Int32
	deps.openAt2 = func(ctx context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
		fd, err := realOpenAt2(ctx, parentFD, name, how)
		if err == nil && name == testVMID+".lock" {
			vmFD.Store(int32(fd))
		}
		return fd, err
	}
	deps.flock = func(ctx context.Context, fd, operation int) error {
		if int32(fd) == vmFD.Load() && operation == unix.LOCK_UN {
			unlockCalls.Add(1)
			return errors.New("injected VM unlock failure")
		}
		return realFlock(ctx, fd, operation)
	}
	deps.close = func(ctx context.Context, fd int) error {
		err := realClose(ctx, fd)
		if int32(fd) == vmFD.Load() {
			closeCalls.Add(1)
			return errors.Join(err, errors.New("injected VM close failure"))
		}
		return err
	}

	_, _, authority, _ := openTestJailerVM(t, deps)
	copyOfAuthority := *authority
	const callers = 16
	results := make(chan error, callers)
	start := make(chan struct{})
	for index := range callers {
		candidate := authority
		if index%2 == 1 {
			candidate = &copyOfAuthority
		}
		go func() {
			<-start
			results <- candidate.Release(t.Context())
		}()
	}
	close(start)
	first := <-results
	require.Error(t, first)
	for range callers - 1 {
		assert.Same(t, first, <-results)
	}
	assert.Contains(t, first.Error(), "injected VM unlock failure")
	assert.Contains(t, first.Error(), "injected VM close failure")
	assert.Equal(t, int32(1), unlockCalls.Load())
	assert.Equal(t, int32(1), closeCalls.Load())
}

func TestRetainTopologyHonorsCancellationWithoutCreatingHold(t *testing.T) {
	t.Parallel()

	_, _, authority, _ := openTestJailerVM(t, realDependencies())
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	hold, err := authority.RetainTopology(cancelled)
	assert.Nil(t, hold)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	require.NoError(t, authority.Release(t.Context()))
}

func openTestJailerVM(
	t *testing.T,
	deps dependencies,
) (string, *JailerUID, *JailerVM, VMKey) {
	t.Helper()

	root := prepareTestRoot(t)
	jailer, err := openJailerWithPolicy(t.Context(), deps, testPolicy(root))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })
	uid, err := jailer.OpenUID(t.Context(), testUID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, uid.Release(context.Background())) })
	key, err := NewVMKey(testVMID)
	require.NoError(t, err)
	authority, err := uid.AcquireVM(t.Context(), key)
	require.NoError(t, err)
	return root, uid, authority, key
}

var _ interface {
	Binding() (uint32, VMKey, error)
	Release(context.Context) error
} = (*TopologyHold)(nil)

func testJailerVMLock(unlockCalls *atomic.Int32) *Lock {
	return &Lock{state: &lockState{
		deps: dependencies{
			flock: func(context.Context, int, int) error {
				unlockCalls.Add(1)
				return nil
			},
			close: func(context.Context, int) error { return nil },
		},
		fd: 1, active: true,
	}}
}
