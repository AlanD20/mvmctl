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
	copyOfTopology := *topology
	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)

	ownerUID, boundKey, err := topology.binding()
	require.NoError(t, err)
	assert.Equal(t, testAuthorityUID, ownerUID)
	if boundKey != key {
		t.Error("retained topology authority has the wrong opaque VM key")
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, topology.Release(cancelled))
	ownerUID, boundKey, err = copyOfTopology.binding()
	assert.Zero(t, ownerUID)
	assert.Equal(t, authorityfs.VMKey{}, boundKey)
	requireTopologyDomainError(t, err, errs.CodeVMStateInvalid, errs.ClassValidation, "", nil, nil)
	releaseTestLaunch(t, launch)
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
		testInstanceAuthorityDeps(t, root),
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
		assert.Contains(t, releaseErr.Error(), "must be consumed by Abort or activation")
		requireTopologyDomainError(
			t,
			releaseErr,
			errs.CodeVMStateInvalid,
			errs.ClassValidation,
			testVMID,
			nil,
			nil,
		)
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
	cleanup, err := copied.Abort(t.Context())
	require.NoError(t, err)
	require.NoError(t, cleanup.Complete(t.Context()))
	require.NoError(t, cleanup.Release(t.Context()))
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
		testInstanceAuthorityDeps(t, root),
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
	cleanup, err := launch.Abort(t.Context())
	require.NoError(t, err)
	copied := *cleanup
	require.NoError(t, cleanup.Complete(t.Context()))

	first := cleanup.Release(t.Context())
	second := copied.Release(t.Context())
	require.Error(t, first)
	assert.ErrorIs(t, first, vmReleaseErr)
	assert.Same(t, first, second)
	requireTopologyDomainError(
		t,
		first,
		errs.CodeProcessError,
		errs.ClassUnknown,
		"",
		vmReleaseErr,
		map[string]any{
			"instance_authority_cleanup_failed": true,
			"instance_authority_cleanup_detail": "release VM lock: release authority lock: injected VM authority release failure",
		},
	)
	assert.True(t, cleanup.state.released)
}

func TestVMAuthorityLeaseRejectsOwnerOnlyMismatchAndReleasesChild(t *testing.T) {
	t.Parallel()

	wantKey, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	child := &injectedTopologyAuthority{owner: testAuthorityUID + 1, key: wantKey}
	lease := &vmAuthorityLease{state: &vmAuthorityLeaseState{
		value: &injectedVMAuthority{child: child}, ownerUID: testAuthorityUID, key: wantKey, active: true,
	}}

	topology, err := lease.retainTopologyAuthority(t.Context())
	assert.Nil(t, topology)
	requireTopologyDomainError(t, err, errs.CodeVMAtomicFailed, errs.ClassInternal, "", nil, nil)
	assert.Equal(t, 1, child.releaseCount())
}

func TestVMAuthorityLeaseRejectsKeyOnlyMismatchAndPreservesChildCleanupError(t *testing.T) {
	t.Parallel()

	wantKey, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	wrongKey, err := authorityfs.NewVMKey("11111111111111111111111111111111")
	require.NoError(t, err)
	cleanupCause := errors.New("injected rejected topology cleanup failure")
	child := &injectedTopologyAuthority{
		owner: testAuthorityUID,
		key:   wrongKey,
		releaseErr: instanceLockError(
			"release VM topology authority",
			cleanupCause,
		),
	}
	lease := &vmAuthorityLease{state: &vmAuthorityLeaseState{
		value: &injectedVMAuthority{child: child}, ownerUID: testAuthorityUID, key: wantKey, active: true,
	}}

	topology, err := lease.retainTopologyAuthority(t.Context())
	assert.Nil(t, topology)
	requireTopologyDomainError(
		t,
		err,
		errs.CodeVMAtomicFailed,
		errs.ClassInternal,
		"",
		cleanupCause,
		map[string]any{
			"instance_authority_cleanup_failed": true,
			"instance_authority_cleanup_detail": "release rejected VM topology authority: release VM topology authority: " +
				"injected rejected topology cleanup failure",
		},
	)
	assert.Equal(t, 1, child.releaseCount())
}

func TestVMAuthorityLeaseReleaseRejectsMissingUnderlyingAuthorityAndCachesError(t *testing.T) {
	t.Parallel()

	lease := &vmAuthorityLease{state: &vmAuthorityLeaseState{active: true}}
	copyOfLease := *lease
	first := lease.Release(t.Context())
	second := copyOfLease.Release(t.Context())
	requireTopologyDomainError(t, first, errs.CodeVMStateInvalid, errs.ClassValidation, "", nil, nil)
	assert.Same(t, first, second)
}

func TestVMAuthorityLeaseRejectsNilRetainResultAndInactiveStates(t *testing.T) {
	t.Parallel()

	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	nilResult := &vmAuthorityLease{state: &vmAuthorityLeaseState{
		value: &injectedVMAuthority{}, ownerUID: testAuthorityUID, key: key, active: true,
	}}
	topology, err := nilResult.retainTopologyAuthority(t.Context())
	assert.Nil(t, topology)
	requireTopologyDomainError(t, err, errs.CodeVMAtomicFailed, errs.ClassInternal, "", nil, nil)

	states := map[string]*vmAuthorityLease{
		"inactive": {state: &vmAuthorityLeaseState{
			value: &injectedVMAuthority{}, ownerUID: testAuthorityUID, key: key,
		}},
		"released": {state: &vmAuthorityLeaseState{
			value: &injectedVMAuthority{}, ownerUID: testAuthorityUID, key: key, active: true, released: true,
		}},
	}
	for name, lease := range states {
		t.Run(name, func(t *testing.T) {
			topology, retainErr := lease.retainTopologyAuthority(t.Context())
			assert.Nil(t, topology)
			requireTopologyDomainError(
				t,
				retainErr,
				errs.CodeVMStateInvalid,
				errs.ClassValidation,
				"",
				nil,
				nil,
			)
		})
	}
}

func TestTopologyAuthorityLeaseReleaseRejectsMissingChildAndCachesError(t *testing.T) {
	t.Parallel()

	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	lease := &topologyAuthorityLease{state: &topologyAuthorityLeaseState{
		ownerUID: testAuthorityUID, key: key,
	}}
	copyOfLease := *lease
	first := lease.Release(t.Context())
	second := copyOfLease.Release(t.Context())
	requireTopologyDomainError(t, first, errs.CodeVMStateInvalid, errs.ClassValidation, "", nil, nil)
	assert.Same(t, first, second)
	ownerUID, boundKey, bindErr := copyOfLease.binding()
	assert.Zero(t, ownerUID)
	assert.Equal(t, authorityfs.VMKey{}, boundKey)
	requireTopologyDomainError(t, bindErr, errs.CodeVMStateInvalid, errs.ClassValidation, "", nil, nil)
}

func TestSystemTopologyAuthorityReleaseRejectsMissingChildAndCachesError(t *testing.T) {
	t.Parallel()

	authority := &systemTopologyAuthority{state: &systemTopologyAuthorityState{}}
	copyOfAuthority := *authority
	first := authority.Release(t.Context())
	second := copyOfAuthority.Release(t.Context())
	requireTopologyDomainError(t, first, errs.CodeVMStateInvalid, errs.ClassValidation, "", nil, nil)
	assert.Same(t, first, second)
	ownerUID, key, bindErr := copyOfAuthority.binding()
	assert.Zero(t, ownerUID)
	assert.Equal(t, authorityfs.VMKey{}, key)
	requireTopologyDomainError(t, bindErr, errs.CodeVMStateInvalid, errs.ClassValidation, "", nil, nil)
}

func TestTopologyAuthorityLeaseOwnsChildUntilBindingAdmissionReturns(t *testing.T) {
	t.Parallel()

	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	bindingStarted := make(chan struct{})
	allowBinding := make(chan struct{})
	child := &injectedTopologyAuthority{owner: testAuthorityUID, key: key}
	lease := &vmAuthorityLease{state: &vmAuthorityLeaseState{
		value: &injectedVMAuthority{child: child}, ownerUID: testAuthorityUID, key: key, active: true,
	}}
	topology, err := lease.retainTopologyAuthority(t.Context())
	require.NoError(t, err)
	child.mu.Lock()
	child.bindingStarted = bindingStarted
	child.allowBinding = allowBinding
	child.mu.Unlock()

	type bindingResult struct {
		ownerUID uint32
		key      authorityfs.VMKey
		err      error
	}
	bindingResults := make(chan bindingResult, 1)
	go func() {
		ownerUID, boundKey, bindingErr := topology.binding()
		bindingResults <- bindingResult{ownerUID: ownerUID, key: boundKey, err: bindingErr}
	}()
	<-bindingStarted
	releaseResults := make(chan error, 1)
	go func() { releaseResults <- topology.Release(t.Context()) }()
	select {
	case releaseErr := <-releaseResults:
		require.Failf(t, "topology authority released during binding admission", "release error: %v", releaseErr)
	case <-time.After(30 * time.Millisecond):
	}
	close(allowBinding)

	result := <-bindingResults
	require.NoError(t, result.err)
	assert.Equal(t, testAuthorityUID, result.ownerUID)
	assert.Equal(t, key, result.key)
	require.NoError(t, <-releaseResults)
	assert.Equal(t, 1, child.releaseCount())
}

func TestTopologyAuthorityLeaseCopiesConcurrentlyReleaseUnderlyingChildOnce(t *testing.T) {
	t.Parallel()

	key, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	releaseCause := errors.New("injected topology release failure")
	releaseErr := instanceLockError("release VM topology authority", releaseCause)
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
	requireTopologyDomainError(t, first, errs.CodeProcessError, errs.ClassUnknown, "", releaseCause, nil)
	assert.Same(t, releaseErr, first)
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
	// mu guards released and releases.
	mu             sync.Mutex
	owner          uint32
	key            authorityfs.VMKey
	released       bool
	releases       int
	releaseErr     error
	bindingStarted chan struct{}
	allowBinding   chan struct{}
}

func (authority *injectedTopologyAuthority) binding() (uint32, authorityfs.VMKey, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.released {
		return 0, authorityfs.VMKey{}, instanceStateError("", "VM topology authority is not active")
	}
	if authority.bindingStarted != nil {
		close(authority.bindingStarted)
		authority.bindingStarted = nil
	}
	if authority.allowBinding != nil {
		<-authority.allowBinding
		authority.allowBinding = nil
	}
	return authority.owner, authority.key, nil
}

func (authority *injectedTopologyAuthority) Release(context.Context) error {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.releases++
	authority.released = true
	return authority.releaseErr
}

func (authority *injectedTopologyAuthority) releaseCount() int {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.releases
}

func requireTopologyDomainError(
	t *testing.T,
	err error,
	code errs.Code,
	class errs.Class,
	entity string,
	cause error,
	details map[string]any,
) {
	t.Helper()

	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, code, domainErr.Code)
	assert.Equal(t, class, domainErr.Class)
	assert.Equal(t, entity, domainErr.Entity)
	assert.Equal(t, details, domainErr.Details)
	if cause == nil {
		assert.NoError(t, domainErr.Err)
		return
	}
	assert.ErrorIs(t, domainErr.Err, cause)
	assert.ErrorIs(t, err, cause)
}
