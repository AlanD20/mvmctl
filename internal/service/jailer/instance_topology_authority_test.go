package jailer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

func TestLaunchLeaseRetainsOwnerBoundTopologyAuthority(t *testing.T) {
	t.Parallel()

	authority, _ := newTestInstanceAuthority(t)
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	topology, err := launch.retainTopologyAuthority(t.Context())
	require.NoError(t, err)
	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)

	assert.Equal(t, testAuthorityUID, topology.ownerUID())
	if topology.vmKey() != key {
		t.Error("retained topology authority has the wrong opaque VM key")
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, topology.Release(cancelled))
	require.NoError(t, launch.Release(cancelled))
	retained, err := launch.retainTopologyAuthority(t.Context())
	assert.Nil(t, retained)
	require.Error(t, err)

	var zero launchLease
	retained, err = zero.retainTopologyAuthority(t.Context())
	assert.Nil(t, retained)
	require.Error(t, err)
}

// Rationale: a topology consumer must keep the VM lock and every composite launch resource live. Releasing the launch
// value before its child must be a non-consuming rejection, not partial cleanup followed by lost VM authority.
func TestLaunchLeaseReleaseWithTopologyHoldPreservesCompositeAuthority(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	tree := newTestInstanceAuthorityTree()
	releaseEvents := make(chan string, 4)
	tree.onRelease = func(_ context.Context, description string) error {
		releaseEvents <- description
		return nil
	}
	authority := newInstanceAuthorityWithPolicy(
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
		tree,
	)
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	require.Equal(t, "index", <-releaseEvents)
	topology, err := launch.retainTopologyAuthority(t.Context())
	require.NoError(t, err)
	before := launch.snapshot()
	copied := *launch

	const callers = 8
	results := make(chan error, callers)
	for index := range callers {
		candidate := launch
		if index%2 == 1 {
			candidate = &copied
		}
		go func() {
			results <- candidate.Release(t.Context())
		}()
	}
	for range callers {
		releaseErr := <-results
		require.Error(t, releaseErr)
		assert.ErrorIs(t, releaseErr, authorityfs.ErrTopologyRetained)
	}

	after := launch.snapshot()
	assert.False(t, after.released)
	assert.True(t, after.hasVMLock)
	assert.True(t, after.hasDirectories)
	assertPreparedReleaseSnapshotOpen(t, after.prepared)
	assertInstanceRecordEqual(t, before.record, after.record)
	select {
	case event := <-releaseEvents:
		require.Failf(t, "launch release performed partial cleanup", "unexpected release: %s", event)
	default:
	}

	waitCtx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	blocked, lockErr := authority.LockActive(waitCtx, testInstanceCaller(), testVMID)
	assert.Nil(t, blocked)
	require.Error(t, lockErr)
	assert.ErrorIs(t, lockErr, context.DeadlineExceeded)
	assert.Equal(t, "index", <-releaseEvents)

	require.NoError(t, topology.Release(t.Context()))
	require.NoError(t, copied.Release(t.Context()))
	assert.Equal(t, "vm", <-releaseEvents)
	assert.Equal(t, "release", <-releaseEvents)
	assert.True(t, launch.snapshot().released)
}

func TestLaunchLeaseVMReleaseErrorKeepsExactCleanupEvidenceAndCachesResult(t *testing.T) {
	t.Parallel()

	root := prepareInstanceAuthorityTestRoot(t)
	tree := newTestInstanceAuthorityTree()
	vmReleaseErr := errors.New("injected VM authority release failure")
	tree.lockReleaseErrDescription = "vm"
	tree.lockReleaseErr = vmReleaseErr
	authority := newInstanceAuthorityWithPolicy(
		realInstanceAuthorityDeps(),
		testInstanceAuthorityPolicy(root),
		tree,
	)
	launch, err := registerTestLaunch(
		t,
		t.Context(),
		authority,
		testInstanceCaller(),
		testLaunchIntent(false),
	)
	require.NoError(t, err)
	copied := *launch

	first := launch.Release(t.Context())
	second := copied.Release(t.Context())
	require.Error(t, first)
	assert.ErrorIs(t, first, vmReleaseErr)
	assert.Same(t, first, second)
	domainErr := errs.AsDomainError(first)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeProcessError, domainErr.Code)
	assert.Equal(t, true, domainErr.Details["instance_authority_cleanup_failed"])
	assert.Equal(
		t,
		"release VM lock: release authority lock: injected VM authority release failure",
		domainErr.Details["instance_authority_cleanup_detail"],
	)
	assert.True(t, launch.snapshot().released)
}

func TestVMAuthorityLeaseRejectsMismatchedInjectedTopologyBinding(t *testing.T) {
	t.Parallel()

	wantKey, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	wrongKey, err := authorityfs.NewVMKey("11111111111111111111111111111111")
	require.NoError(t, err)
	child := &injectedTopologyAuthority{owner: testAuthorityUID, key: wrongKey}
	lease := &vmAuthorityLease{state: &vmAuthorityLeaseState{
		value: &injectedVMAuthority{child: child}, ownerUID: testAuthorityUID, key: wantKey, active: true,
	}}

	topology, err := lease.retainTopologyAuthority(t.Context())
	assert.Nil(t, topology)
	require.Error(t, err)
	assert.Equal(t, 1, child.releaseCount())
}

func TestVMAuthorityLeaseReleaseRejectsMissingUnderlyingAuthorityAndCachesError(t *testing.T) {
	t.Parallel()

	lease := &vmAuthorityLease{state: &vmAuthorityLeaseState{active: true}}
	copyOfLease := *lease
	first := lease.Release(t.Context())
	second := copyOfLease.Release(t.Context())
	require.Error(t, first)
	assert.Same(t, first, second)
}

func TestTopologyAuthorityLeaseCopiesConcurrentlyReleaseUnderlyingChildOnce(t *testing.T) {
	t.Parallel()

	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	releaseErr := errors.New("injected topology release failure")
	child := &injectedTopologyAuthority{owner: testAuthorityUID, key: key, releaseErr: releaseErr}
	lease := &vmAuthorityLease{state: &vmAuthorityLeaseState{
		value: &injectedVMAuthority{child: child}, ownerUID: testAuthorityUID, key: key, active: true,
	}}
	topology, err := lease.retainTopologyAuthority(t.Context())
	require.NoError(t, err)
	copyOfTopology := *topology

	const callers = 16
	results := make(chan error, callers)
	for index := range callers {
		candidate := topology
		if index%2 == 1 {
			candidate = &copyOfTopology
		}
		go func() {
			cancelled, cancel := context.WithCancel(t.Context())
			cancel()
			results <- candidate.Release(cancelled)
		}()
	}
	first := <-results
	require.Error(t, first)
	assert.ErrorIs(t, first, releaseErr)
	for range callers - 1 {
		assert.Same(t, first, <-results)
	}
	assert.Equal(t, 1, child.releaseCount())
}

type injectedVMAuthority struct {
	child instanceTopologyAuthority
}

func (authority *injectedVMAuthority) retainTopology(context.Context) (instanceTopologyAuthority, error) {
	return authority.child, nil
}

func (*injectedVMAuthority) Release(context.Context) error {
	return nil
}

type injectedTopologyAuthority struct {
	// mu guards releases.
	mu         sync.Mutex
	owner      uint32
	key        authorityfs.VMKey
	releases   int
	releaseErr error
}

func (authority *injectedTopologyAuthority) ownerUID() uint32 {
	return authority.owner
}

func (authority *injectedTopologyAuthority) vmKey() authorityfs.VMKey {
	return authority.key
}

func (authority *injectedTopologyAuthority) Release(context.Context) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.releases++
	return authority.releaseErr
}

func (authority *injectedTopologyAuthority) releaseCount() int {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.releases
}
