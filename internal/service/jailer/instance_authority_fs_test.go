package jailer

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/authorityfs"
)

type testInstanceAuthorityTree struct {
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
}

type testInstanceJailerAuthority struct {
	tree       *testInstanceAuthorityTree
	mu         sync.Mutex
	released   bool
	releaseErr error
}

type testInstanceJailerUID struct {
	tree       *testInstanceAuthorityTree
	uid        uint32
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
	mu          sync.Mutex
	released    bool
	releaseErr  error
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

func (tree *testInstanceAuthorityTree) openJailer(context.Context) (instanceJailerAuthority, error) {
	if tree.openErr != nil {
		return nil, tree.openErr
	}
	return &testInstanceJailerAuthority{tree: tree}, nil
}

func (authority *testInstanceJailerAuthority) acquireIndex(
	ctx context.Context,
) (instanceAuthorityLock, error) {
	if authority.tree.indexErr != nil {
		return nil, authority.tree.indexErr
	}
	return authority.tree.acquire(ctx, authority.tree.index, "index")
}

func (authority *testInstanceJailerAuthority) acquireRelease(
	ctx context.Context,
	key authorityfs.ReleaseKey,
) (instanceAuthorityLock, error) {
	if authority.tree.releaseErr != nil {
		return nil, authority.tree.releaseErr
	}
	authority.tree.mu.Lock()
	gate := authority.tree.releases[key]
	if gate == nil {
		gate = newTestInstanceLockGate()
		authority.tree.releases[key] = gate
	}
	authority.tree.mu.Unlock()
	return authority.tree.acquire(ctx, gate, "release")
}

func (authority *testInstanceJailerAuthority) openUID(
	_ context.Context,
	uid uint32,
) (instanceJailerUID, error) {
	return &testInstanceJailerUID{tree: authority.tree, uid: uid}, nil
}

func (authority *testInstanceJailerAuthority) Release(context.Context) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return authority.releaseErr
	}
	authority.released = true
	authority.releaseErr = authority.tree.treeReleaseErr
	return authority.releaseErr
}

func (uid *testInstanceJailerUID) acquireVM(
	ctx context.Context,
	key authorityfs.VMKey,
) (instanceAuthorityLock, error) {
	if uid.tree.vmErr != nil {
		return nil, uid.tree.vmErr
	}
	uid.tree.mu.Lock()
	mapKey := testInstanceVMKey{uid: uid.uid, key: key}
	gate := uid.tree.vms[mapKey]
	if gate == nil {
		gate = newTestInstanceLockGate()
		uid.tree.vms[mapKey] = gate
	}
	uid.tree.mu.Unlock()
	return uid.tree.acquire(ctx, gate, "vm")
}

func (uid *testInstanceJailerUID) Release(context.Context) error {
	uid.mu.Lock()
	defer uid.mu.Unlock()
	if uid.released {
		return uid.releaseErr
	}
	uid.released = true
	uid.releaseErr = uid.tree.uidReleaseErr
	return uid.releaseErr
}

func (tree *testInstanceAuthorityTree) acquire(
	ctx context.Context,
	gate *testInstanceLockGate,
	description string,
) (instanceAuthorityLock, error) {
	select {
	case <-gate.token:
		tree.mu.Lock()
		tree.acquired = append(tree.acquired, description)
		tree.mu.Unlock()
		tree.observeAcquire(ctx, description)
		return &testInstanceAuthorityLock{tree: tree, gate: gate, description: description}, nil
	default:
		gate.waitOnce.Do(func() { close(gate.waitObserved) })
	}
	select {
	case <-ctx.Done():
		return nil, instanceLockContextError(description, ctx.Err())
	case <-gate.token:
	}
	tree.mu.Lock()
	tree.acquired = append(tree.acquired, description)
	tree.mu.Unlock()
	tree.observeAcquire(ctx, description)
	return &testInstanceAuthorityLock{tree: tree, gate: gate, description: description}, nil
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
}

func assertSameError(t *testing.T, want error, got error) {
	t.Helper()

	require.Same(t, want, got)
}
