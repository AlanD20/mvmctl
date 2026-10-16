package jailer

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

type testInstanceAuthorityTree struct {
	// mu guards mutable fake configuration, lock maps, callbacks, and acquisition history.
	mu                        sync.Mutex
	index                     *testInstanceLockGate
	releases                  map[authorityfs.ReleaseKey]*testInstanceLockGate
	vms                       map[testInstanceVMKey]*testInstanceLockGate
	acquired                  []string
	openErr                   error
	indexErr                  error
	releaseErr                error
	vmErr                     error
	treeReleaseErr            error
	uidReleaseErr             error
	lockReleaseErr            error
	lockReleaseErrDescription string
	onAcquire                 func(context.Context, string)
	onRelease                 func(context.Context, string) error
	onVMAcquire               func(context.Context, authorityfs.VMKey)
	onVMRelease               func(context.Context, authorityfs.VMKey) error
	onTreeRelease             func(context.Context) error
}

type testInstanceJailerAuthority struct {
	tree *testInstanceAuthorityTree
	// mu guards released and releaseErr.
	mu         sync.Mutex
	released   bool
	releaseErr error
}

type testInstanceJailerUID struct {
	tree *testInstanceAuthorityTree
	uid  uint32
	// mu guards released and releaseErr.
	mu         sync.Mutex
	released   bool
	releaseErr error
}

type testInstanceLockGate struct {
	token        chan struct{}
	waitObserved chan struct{}
	waitOnce     sync.Once
}

type testInstanceAuthorityLock struct {
	tree        *testInstanceAuthorityTree
	gate        *testInstanceLockGate
	description string
	// mu guards released and releaseErr.
	mu         sync.Mutex
	released   bool
	releaseErr error
}

type testInstanceVMAuthority struct {
	tree     *testInstanceAuthorityTree
	gate     *testInstanceLockGate
	ownerUID uint32
	key      authorityfs.VMKey
	// mu guards topologyHolds and every lifecycle field below.
	mu            sync.Mutex
	topologyHolds uint64
	released      bool
	releaseErr    error
}

type testInstanceTopologyAuthority struct {
	state *testInstanceTopologyAuthorityState
}

type testInstanceTopologyAuthorityState struct {
	// mu guards parent, binding, and every lifecycle field below.
	mu         sync.Mutex
	parent     *testInstanceVMAuthority
	ownerUID   uint32
	key        authorityfs.VMKey
	released   bool
	releaseErr error
}

type testInstanceVMKey struct {
	uid uint32
	key authorityfs.VMKey
}

func newTestInstanceAuthorityTree() *testInstanceAuthorityTree {
	return &testInstanceAuthorityTree{
		index:    newTestInstanceLockGate(),
		releases: make(map[authorityfs.ReleaseKey]*testInstanceLockGate),
		vms:      make(map[testInstanceVMKey]*testInstanceLockGate),
	}
}

func newTestInstanceLockGate() *testInstanceLockGate {
	gate := &testInstanceLockGate{token: make(chan struct{}, 1), waitObserved: make(chan struct{})}
	gate.token <- struct{}{}
	return gate
}

func (tree *testInstanceAuthorityTree) openJailer(ctx context.Context) (instanceJailerAuthority, error) {
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("open Jailer authority tree", err)
	}
	tree.mu.Lock()
	openErr := tree.openErr
	tree.mu.Unlock()
	if openErr != nil {
		return nil, instanceAtomicError("open Jailer authority tree", openErr)
	}
	return &testInstanceJailerAuthority{tree: tree}, nil
}

func (tree *testInstanceAuthorityTree) openExistingJailer(ctx context.Context) (instanceJailerAuthority, error) {
	return tree.openJailer(ctx)
}

func (authority *testInstanceJailerAuthority) acquireIndex(
	ctx context.Context,
) (instanceAuthorityLock, error) {
	authority.mu.Lock()
	released := authority.released
	authority.mu.Unlock()
	if released {
		return nil, instanceLockError("acquire global index lock", errors.New("Jailer authority is released"))
	}
	authority.tree.mu.Lock()
	indexErr := authority.tree.indexErr
	authority.tree.mu.Unlock()
	if indexErr != nil {
		return nil, mapAuthorityFSLockError("global index", indexErr)
	}
	return authority.tree.acquire(ctx, authority.tree.index, "index")
}

func (authority *testInstanceJailerAuthority) acquireRelease(
	ctx context.Context,
	key authorityfs.ReleaseKey,
) (instanceAuthorityLock, error) {
	authority.mu.Lock()
	released := authority.released
	authority.mu.Unlock()
	if released {
		return nil, instanceLockError("acquire release lock", errors.New("Jailer authority is released"))
	}
	authority.tree.mu.Lock()
	releaseErr := authority.tree.releaseErr
	if releaseErr != nil {
		authority.tree.mu.Unlock()
		return nil, mapAuthorityFSLockError("release", releaseErr)
	}
	gate := authority.tree.releases[key]
	if gate == nil {
		gate = newTestInstanceLockGate()
		authority.tree.releases[key] = gate
	}
	authority.tree.mu.Unlock()
	return authority.tree.acquire(ctx, gate, "release")
}

func (authority *testInstanceJailerAuthority) openUID(
	ctx context.Context,
	uid uint32,
) (instanceJailerUID, error) {
	authority.mu.Lock()
	released := authority.released
	authority.mu.Unlock()
	if released {
		return nil, instanceAtomicError("open Jailer UID authority", errors.New("Jailer authority is released"))
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("open Jailer UID authority", err)
	}
	if uid == 0 {
		return nil, instanceAtomicError("open Jailer UID authority", errors.New("Jailer UID must be non-zero"))
	}
	return &testInstanceJailerUID{tree: authority.tree, uid: uid}, nil
}

func (authority *testInstanceJailerAuthority) Release(ctx context.Context) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return authority.releaseErr
	}
	authority.released = true
	authority.tree.mu.Lock()
	treeReleaseErr := authority.tree.treeReleaseErr
	onTreeRelease := authority.tree.onTreeRelease
	authority.tree.mu.Unlock()
	if onTreeRelease != nil {
		treeReleaseErr = errors.Join(treeReleaseErr, onTreeRelease(ctx))
	}
	if treeReleaseErr != nil {
		authority.releaseErr = instanceAtomicError("release Jailer authority tree", treeReleaseErr)
	}
	return authority.releaseErr
}

func (uid *testInstanceJailerUID) acquireVM(
	ctx context.Context,
	key authorityfs.VMKey,
) (instanceVMAuthority, error) {
	uid.mu.Lock()
	released := uid.released
	uid.mu.Unlock()
	if released {
		return nil, instanceLockError("acquire VM lock", errors.New("Jailer UID authority is released"))
	}
	uid.tree.mu.Lock()
	vmErr := uid.tree.vmErr
	if vmErr != nil {
		uid.tree.mu.Unlock()
		return nil, mapAuthorityFSLockError("VM", vmErr)
	}
	mapKey := testInstanceVMKey{uid: uid.uid, key: key}
	gate := uid.tree.vms[mapKey]
	if gate == nil {
		gate = newTestInstanceLockGate()
		uid.tree.vms[mapKey] = gate
	}
	uid.tree.mu.Unlock()
	if err := uid.tree.waitAcquire(ctx, gate, "vm"); err != nil {
		return nil, err
	}
	uid.tree.mu.Lock()
	onVMAcquire := uid.tree.onVMAcquire
	uid.tree.mu.Unlock()
	if onVMAcquire != nil {
		onVMAcquire(ctx, key)
	}
	return &testInstanceVMAuthority{tree: uid.tree, gate: gate, ownerUID: uid.uid, key: key}, nil
}

func (uid *testInstanceJailerUID) Release(context.Context) error {
	uid.mu.Lock()
	defer uid.mu.Unlock()
	if uid.released {
		return uid.releaseErr
	}
	uid.released = true
	uid.tree.mu.Lock()
	uidReleaseErr := uid.tree.uidReleaseErr
	uid.tree.mu.Unlock()
	if uidReleaseErr != nil {
		uid.releaseErr = instanceAtomicError("release Jailer UID authority", uidReleaseErr)
	}
	return uid.releaseErr
}

func (tree *testInstanceAuthorityTree) acquire(
	ctx context.Context,
	gate *testInstanceLockGate,
	description string,
) (instanceAuthorityLock, error) {
	if err := tree.waitAcquire(ctx, gate, description); err != nil {
		return nil, err
	}
	return &testInstanceAuthorityLock{tree: tree, gate: gate, description: description}, nil
}

func (tree *testInstanceAuthorityTree) waitAcquire(
	ctx context.Context,
	gate *testInstanceLockGate,
	description string,
) error {
	if err := ctx.Err(); err != nil {
		return instanceLockContextError(description, err)
	}
	select {
	case <-gate.token:
		tree.mu.Lock()
		tree.acquired = append(tree.acquired, description)
		tree.mu.Unlock()
		tree.observeAcquire(ctx, description)
		return nil
	default:
		gate.waitOnce.Do(func() { close(gate.waitObserved) })
	}
	select {
	case <-ctx.Done():
		return instanceLockContextError(description, ctx.Err())
	case <-gate.token:
	}
	tree.mu.Lock()
	tree.acquired = append(tree.acquired, description)
	tree.mu.Unlock()
	tree.observeAcquire(ctx, description)
	return nil
}

func (tree *testInstanceAuthorityTree) releaseWaitObserved(t *testing.T, slot releaseSlot) <-chan struct{} {
	t.Helper()

	key, err := authorityfs.NewReleaseKey(releaseSlotDigest(slot))
	require.NoError(t, err)
	tree.mu.Lock()
	defer tree.mu.Unlock()
	gate := tree.releases[key]
	require.NotNil(t, gate)
	return gate.waitObserved
}

func (lock *testInstanceAuthorityLock) Release(ctx context.Context) error {
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.released {
		return lock.releaseErr
	}
	lock.released = true
	lock.tree.mu.Lock()
	var releaseErr error
	if lock.tree.lockReleaseErrDescription == "" || lock.tree.lockReleaseErrDescription == lock.description {
		releaseErr = lock.tree.lockReleaseErr
		lock.tree.lockReleaseErr = nil
	}
	onRelease := lock.tree.onRelease
	lock.tree.mu.Unlock()
	if onRelease != nil {
		releaseErr = errors.Join(releaseErr, onRelease(ctx, lock.description))
	}
	if releaseErr != nil {
		lock.releaseErr = instanceLockError("release authority lock", releaseErr)
	}
	lock.gate.token <- struct{}{}
	return lock.releaseErr
}

func (authority *testInstanceVMAuthority) retainTopology(
	ctx context.Context,
) (instanceTopologyAuthority, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return nil, instanceStateError("", "VM authority is not active")
	}
	if err := ctx.Err(); err != nil {
		return nil, instanceLockContextError("VM topology authority", err)
	}
	authority.topologyHolds++
	return &testInstanceTopologyAuthority{state: &testInstanceTopologyAuthorityState{
		parent: authority, ownerUID: authority.ownerUID, key: authority.key,
	}}, nil
}

func (authority *testInstanceVMAuthority) Release(ctx context.Context) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return authority.releaseErr
	}
	if authority.topologyHolds != 0 {
		return instanceLockError("release VM authority", authorityfs.ErrTopologyRetained)
	}
	authority.released = true
	authority.tree.mu.Lock()
	var releaseErr error
	if authority.tree.lockReleaseErrDescription == "" || authority.tree.lockReleaseErrDescription == "vm" {
		releaseErr = authority.tree.lockReleaseErr
		authority.tree.lockReleaseErr = nil
	}
	onRelease := authority.tree.onRelease
	onVMRelease := authority.tree.onVMRelease
	authority.tree.mu.Unlock()
	if onRelease != nil {
		releaseErr = errors.Join(releaseErr, onRelease(ctx, "vm"))
	}
	if onVMRelease != nil {
		releaseErr = errors.Join(releaseErr, onVMRelease(ctx, authority.key))
	}
	if releaseErr != nil {
		authority.releaseErr = instanceLockError("release authority lock", releaseErr)
	}
	authority.gate.token <- struct{}{}
	return authority.releaseErr
}

func (authority *testInstanceTopologyAuthority) binding() (uint32, authorityfs.VMKey, error) {
	if authority == nil || authority.state == nil {
		return 0, authorityfs.VMKey{}, instanceStateError("", "VM topology authority is not active")
	}
	state := authority.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || state.parent == nil {
		return 0, authorityfs.VMKey{}, instanceStateError("", "VM topology authority is not active")
	}
	return state.ownerUID, state.key, nil
}

func (authority *testInstanceTopologyAuthority) Release(context.Context) error {
	if authority == nil || authority.state == nil {
		return nil
	}
	state := authority.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	state.parent.mu.Lock()
	if state.parent.topologyHolds == 0 {
		state.releaseErr = instanceAtomicError("test VM topology hold counter underflow", nil)
	} else {
		state.parent.topologyHolds--
	}
	state.parent.mu.Unlock()
	state.parent = nil
	state.ownerUID = 0
	state.key = authorityfs.VMKey{}
	state.released = true
	return state.releaseErr
}

func (tree *testInstanceAuthorityTree) observeAcquire(ctx context.Context, description string) {
	tree.mu.Lock()
	onAcquire := tree.onAcquire
	tree.mu.Unlock()
	if onAcquire != nil {
		onAcquire(ctx, description)
	}
}

func testAuthorityTree(t *testing.T, authority *instanceAuthority) *testInstanceAuthorityTree {
	t.Helper()

	tree, ok := authority.authorityTree.(*testInstanceAuthorityTree)
	require.True(t, ok)
	return tree
}

func (tree *testInstanceAuthorityTree) acquisitionSnapshot() []string {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	return append([]string(nil), tree.acquired...)
}

func (tree *testInstanceAuthorityTree) failIndex(cause error) {
	tree.mu.Lock()
	defer tree.mu.Unlock()
	tree.indexErr = errors.Join(cause)
}

func TestSystemAuthorityAdaptersPreserveCachedReleaseResult(t *testing.T) {
	t.Parallel()

	authorityErr := instanceAtomicError("release Jailer authority tree", errors.New("authority release failed"))
	authority := &systemJailerAuthority{released: true, releaseErr: authorityErr}
	assertSameError(t, authorityErr, authority.Release(t.Context()))
	assertSameError(t, authorityErr, authority.Release(t.Context()))

	uidErr := instanceAtomicError("release Jailer UID authority", errors.New("UID release failed"))
	uid := &systemJailerUID{released: true, releaseErr: uidErr}
	assertSameError(t, uidErr, uid.Release(t.Context()))
	assertSameError(t, uidErr, uid.Release(t.Context()))

	lockErr := instanceLockError("release authority lock", errors.New("lock release failed"))
	lock := &systemAuthorityLock{released: true, releaseErr: lockErr}
	assertSameError(t, lockErr, lock.Release(t.Context()))
	assertSameError(t, lockErr, lock.Release(t.Context()))

	vmErr := instanceLockError("release VM authority", errors.New("VM release failed"))
	vm := &systemVMAuthority{released: true, releaseErr: vmErr}
	assertSameError(t, vmErr, vm.Release(t.Context()))
	assertSameError(t, vmErr, vm.Release(t.Context()))

	topologyErr := instanceLockError("release VM topology authority", errors.New("topology release failed"))
	topology := &systemTopologyAuthority{state: &systemTopologyAuthorityState{
		released: true, releaseErr: topologyErr,
	}}
	assertSameError(t, topologyErr, topology.Release(t.Context()))
	assertSameError(t, topologyErr, topology.Release(t.Context()))
}

func TestSystemAuthorityAdaptersRejectAcquireAfterRelease(t *testing.T) {
	t.Parallel()

	authority := &systemJailerAuthority{released: true}
	index, err := authority.acquireIndex(t.Context())
	assert.Nil(t, index)
	require.Error(t, err)
	uid, err := authority.openUID(t.Context(), testAuthorityUID)
	assert.Nil(t, uid)
	require.Error(t, err)
	releaseKey, err := authorityfs.NewReleaseKey(
		releaseSlotDigest(releaseSlot{version: "1.16.1", architecture: "x86_64"}),
	)
	require.NoError(t, err)
	release, err := authority.acquireRelease(t.Context(), releaseKey)
	assert.Nil(t, release)
	require.Error(t, err)

	uidAuthority := &systemJailerUID{released: true}
	vmKey, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	vm, err := uidAuthority.acquireVM(t.Context(), vmKey)
	assert.Nil(t, vm)
	require.Error(t, err)

	vmAuthority := &systemVMAuthority{released: true}
	topology, err := vmAuthority.retainTopology(t.Context())
	assert.Nil(t, topology)
	require.Error(t, err)

	topologyAuthority := &systemTopologyAuthority{state: &systemTopologyAuthorityState{released: true}}
	ownerUID, boundKey, err := topologyAuthority.binding()
	assert.Zero(t, ownerUID)
	assert.Equal(t, authorityfs.VMKey{}, boundKey)
	require.Error(t, err)
}

func TestTestAuthorityAdapterMapsErrorsLikeSystemAdapter(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cause     error
		wantClass errs.Class
	}{
		"raw failure":  {cause: unix.EIO, wantClass: errs.ClassUnknown},
		"cancellation": {cause: context.Canceled, wantClass: errs.ClassRetryable},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			tree := newTestInstanceAuthorityTree()
			tree.indexErr = test.cause
			authorityValue, err := tree.openJailer(t.Context())
			require.NoError(t, err)
			lock, err := authorityValue.acquireIndex(t.Context())
			assert.Nil(t, lock)
			require.ErrorIs(t, err, test.cause)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeProcessError, domainErr.Code)
			assert.Equal(t, test.wantClass, domainErr.Class)
		})
	}

	tree := newTestInstanceAuthorityTree()
	tree.openErr = unix.EIO
	authorityValue, err := tree.openJailer(t.Context())
	assert.Nil(t, authorityValue)
	require.ErrorIs(t, err, unix.EIO)
	assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
}

func TestTestAuthorityAdapterRejectsUseAfterRelease(t *testing.T) {
	t.Parallel()

	tree := newTestInstanceAuthorityTree()
	authorityValue, err := tree.openJailer(t.Context())
	require.NoError(t, err)
	uidValue, err := authorityValue.openUID(t.Context(), testAuthorityUID)
	require.NoError(t, err)
	require.NoError(t, uidValue.Release(t.Context()))
	vmKey, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	vm, err := uidValue.acquireVM(t.Context(), vmKey)
	assert.Nil(t, vm)
	require.Error(t, err)

	require.NoError(t, authorityValue.Release(t.Context()))
	index, err := authorityValue.acquireIndex(t.Context())
	assert.Nil(t, index)
	require.Error(t, err)
	uidValue, err = authorityValue.openUID(t.Context(), testAuthorityUID)
	assert.Nil(t, uidValue)
	require.Error(t, err)
}

func TestZeroValueSystemAuthorityAdaptersFailClosedAndCacheError(t *testing.T) {
	t.Parallel()

	tests := map[string]func(context.Context) error{
		"Jailer":     (&systemJailerAuthority{}).Release,
		"Jailer UID": (&systemJailerUID{}).Release,
		"lock":       (&systemAuthorityLock{}).Release,
		"VM":         (&systemVMAuthority{}).Release,
	}
	for name, release := range tests {
		t.Run(name, func(t *testing.T) {
			first := release(t.Context())
			require.Error(t, first)
			second := release(t.Context())
			assertSameError(t, first, second)
		})
	}
}

func assertSameError(t *testing.T, want error, got error) {
	t.Helper()

	require.Same(t, want, got)
}
