package loopmount

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

func TestAuthorityFSRootfsResourceLocksSelectOnlyCallerFamilyAndIdentity(t *testing.T) {
	t.Parallel()

	vmKey, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	imageKey, err := authorityfs.NewImageKey(testImageID)
	require.NoError(t, err)
	tests := []struct {
		name      string
		selection rootfsSelection
		wantVM    authorityfs.VMKey
		wantImage authorityfs.ImageKey
	}{
		{
			name: "VM",
			selection: rootfsSelection{
				kind: rootfsVMProvisionSelection, resourceID: testVMID, expectedBytes: 1, replayBytes: 2,
			},
			wantVM: vmKey,
		},
		{
			name: "image inspection",
			selection: rootfsSelection{
				kind: rootfsImageInspectSelection, resourceID: testImageID, expectedBytes: 3, replayBytes: 4,
			},
			wantImage: imageKey,
		},
		{
			name: "image optimization",
			selection: rootfsSelection{
				kind: rootfsImageOptimizeSelection, resourceID: testImageID, expectedBytes: 5, replayBytes: 6,
			},
			wantImage: imageKey,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authority := &testLoopMountLockAuthority{
				wantVM: test.wantVM, wantImage: test.wantImage,
				lock: &testLoopMountAuthorityLock{},
			}
			tree := &testLoopMountAuthorityTree{authority: authority}
			locks := &authorityFSRootfsResourceLocks{tree: tree}

			lease, acquireErr := locks.Acquire(t.Context(), testRootfsCaller(), test.selection)
			require.NoError(t, acquireErr)
			require.NotNil(t, lease)
			require.NoError(t, lease.Release(t.Context()))

			assert.Equal(t, 1, tree.opens)
			assert.Equal(t, testRootfsCaller().UID, tree.uid)
			if test.wantVM != (authorityfs.VMKey{}) {
				assert.True(t, authority.vmMatched)
				assert.Zero(t, authority.imageCalls)
			} else {
				assert.True(t, authority.imageMatched)
				assert.Zero(t, authority.vmCalls)
			}
		})
	}
}

func TestAuthorityFSRootfsResourceLocksRejectInvalidInputBeforeOpeningAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		caller    Caller
		selection rootfsSelection
	}{
		"zero UID": {
			caller:    Caller{GID: testRootfsCaller().GID},
			selection: rootfsSelection{kind: rootfsVMProvisionSelection, resourceID: testVMID},
		},
		"zero GID": {
			caller:    Caller{UID: testRootfsCaller().UID},
			selection: rootfsSelection{kind: rootfsVMProvisionSelection, resourceID: testVMID},
		},
		"invalid kind": {
			caller: testRootfsCaller(), selection: rootfsSelection{resourceID: testVMID},
		},
		"invalid VM ID": {
			caller:    testRootfsCaller(),
			selection: rootfsSelection{kind: rootfsVMProvisionSelection, resourceID: "not-a-vm"},
		},
		"invalid image ID": {
			caller:    testRootfsCaller(),
			selection: rootfsSelection{kind: rootfsImageInspectSelection, resourceID: "not-an-image"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			tree := &testLoopMountAuthorityTree{}
			locks := &authorityFSRootfsResourceLocks{tree: tree}

			lease, err := locks.Acquire(t.Context(), test.caller, test.selection)
			assert.Nil(t, lease)
			require.Error(t, err)
			assert.Equal(t, errs.CodeValidationFailed, errs.AsDomainError(err).Code)
			assert.Zero(t, tree.opens)
		})
	}
}

func TestAuthorityFSRootfsResourceLocksMapAcquisitionErrors(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		cause       error
		fromTree    bool
		wantClass   errs.Class
		wantLease   bool
		wantRelease int
	}{
		"tree cancellation": {
			cause: context.Canceled, fromTree: true, wantClass: errs.ClassRetryable, wantLease: true, wantRelease: 1,
		},
		"tree operational failure": {
			cause: unix.EIO, fromTree: true, wantClass: errs.ClassInternal, wantLease: true, wantRelease: 1,
		},
		"lock deadline": {
			cause: context.DeadlineExceeded, wantClass: errs.ClassRetryable, wantLease: true, wantRelease: 1,
		},
		"lock operational failure": {
			cause: unix.EIO, wantClass: errs.ClassInternal, wantLease: true, wantRelease: 1,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			authority := &testLoopMountLockAuthority{}
			tree := &testLoopMountAuthorityTree{authority: authority}
			if test.fromTree {
				tree.err = test.cause
			} else {
				authority.vmErr = test.cause
			}
			locks := &authorityFSRootfsResourceLocks{tree: tree}

			lease, err := locks.Acquire(t.Context(), testRootfsCaller(), rootfsSelection{
				kind: rootfsVMProvisionSelection, resourceID: testVMID,
			})
			if test.wantLease {
				require.NotNil(t, lease)
				require.NoError(t, lease.Release(t.Context()))
			} else {
				assert.Nil(t, lease)
			}
			require.ErrorIs(t, err, test.cause)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
			assert.Equal(t, test.wantClass, domainErr.Class)
			assert.Equal(t, test.wantRelease, authority.releaseCount)
		})
	}
}

func TestAuthorityFSRootfsResourceLocksRetainPartialLockForRelease(t *testing.T) {
	t.Parallel()

	// Rationale: A lock implementation may acquire authority before reporting an error. The non-nil lease is the only
	// way the Server can deterministically release both partial effects.
	events := make([]string, 0, 2)
	lock := &testLoopMountAuthorityLock{events: &events}
	authority := &testLoopMountLockAuthority{
		lock: lock, vmErr: errors.New("outcome unknown"), events: &events,
	}
	locks := &authorityFSRootfsResourceLocks{tree: &testLoopMountAuthorityTree{authority: authority}}

	lease, err := locks.Acquire(t.Context(), testRootfsCaller(), rootfsSelection{
		kind: rootfsVMProvisionSelection, resourceID: testVMID,
	})
	require.NotNil(t, lease)
	require.Error(t, err)
	require.NoError(t, lease.Release(t.Context()))
	if diff := cmp.Diff([]string{"lock", "authority"}, events); diff != "" {
		t.Errorf("partial lock release order mismatch (-want +got):\n%s", diff)
	}
}

func TestAuthorityFSRootfsResourceLockLeaseCachesJoinedReleaseResult(t *testing.T) {
	t.Parallel()

	// Rationale: Repeating cleanup against a reused descriptor is unsafe. Copied lease values must share one release
	// state, attempt lock and parent cleanup despite cancellation, and return the exact cached joined result.
	events := make([]string, 0, 2)
	lockErr := errors.New("lock release failed")
	authorityErr := errors.New("authority release failed")
	lock := &testLoopMountAuthorityLock{
		events: &events, err: lockErr, contextKey: testRootfsLockContextKey{},
	}
	authority := &testLoopMountLockAuthority{
		lock: lock, events: &events, releaseErr: authorityErr, contextKey: testRootfsLockContextKey{},
	}
	locks := &authorityFSRootfsResourceLocks{tree: &testLoopMountAuthorityTree{authority: authority}}
	leaseValue, err := locks.Acquire(t.Context(), testRootfsCaller(), rootfsSelection{
		kind: rootfsVMProvisionSelection, resourceID: testVMID,
	})
	require.NoError(t, err)
	lease, ok := leaseValue.(*authorityFSRootfsResourceLockLease)
	require.True(t, ok)
	copied := *lease
	ctx, cancel := context.WithCancel(context.WithValue(t.Context(), testRootfsLockContextKey{}, "retained"))
	cancel()

	first := lease.Release(ctx)
	require.ErrorIs(t, first, lockErr)
	require.ErrorIs(t, first, authorityErr)
	second := copied.Release(ctx)
	require.Same(t, first, second)
	if diff := cmp.Diff([]string{"lock", "authority"}, events); diff != "" {
		t.Errorf("release order mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, 1, lock.releaseCount)
	assert.Equal(t, 1, authority.releaseCount)
	assert.True(t, lock.contextValueObserved)
	assert.True(t, authority.contextValueObserved)
}

func TestAuthorityFSRootfsResourceLocksFailClosedOnMissingCapabilities(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		tree      loopMountAuthorityTree
		wantLease bool
	}{
		"missing tree": {},
		"missing authority": {
			tree: &testLoopMountAuthorityTree{},
		},
		"missing lock": {
			tree: &testLoopMountAuthorityTree{authority: &testLoopMountLockAuthority{}}, wantLease: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			locks := &authorityFSRootfsResourceLocks{tree: test.tree}
			lease, err := locks.Acquire(t.Context(), testRootfsCaller(), rootfsSelection{
				kind: rootfsVMProvisionSelection, resourceID: testVMID,
			})
			if test.wantLease {
				require.NotNil(t, lease)
				require.NoError(t, lease.Release(t.Context()))
			} else {
				assert.Nil(t, lease)
			}
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeLoopMountError, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
		})
	}

	zero := &authorityFSRootfsResourceLockLease{}
	require.Error(t, zero.Release(t.Context()))
	var nilLease *authorityFSRootfsResourceLockLease
	require.NoError(t, nilLease.Release(t.Context()))
}

type testLoopMountAuthorityTree struct {
	authority loopMountLockAuthority
	err       error
	opens     int
	uid       uint32
}

func (tree *testLoopMountAuthorityTree) openLoopMount(
	_ context.Context,
	uid uint32,
) (loopMountLockAuthority, error) {
	tree.opens++
	tree.uid = uid
	return tree.authority, tree.err
}

type testLoopMountLockAuthority struct {
	wantVM               authorityfs.VMKey
	wantImage            authorityfs.ImageKey
	lock                 loopMountAuthorityLock
	vmErr                error
	imageErr             error
	releaseErr           error
	events               *[]string
	contextKey           testRootfsLockContextKey
	vmCalls              int
	imageCalls           int
	releaseCount         int
	vmMatched            bool
	imageMatched         bool
	contextValueObserved bool
}

type testRootfsLockContextKey struct{}

func (authority *testLoopMountLockAuthority) acquireVM(
	_ context.Context,
	key authorityfs.VMKey,
) (loopMountAuthorityLock, error) {
	authority.vmCalls++
	authority.vmMatched = key == authority.wantVM
	return authority.lock, authority.vmErr
}

func (authority *testLoopMountLockAuthority) acquireImage(
	_ context.Context,
	key authorityfs.ImageKey,
) (loopMountAuthorityLock, error) {
	authority.imageCalls++
	authority.imageMatched = key == authority.wantImage
	return authority.lock, authority.imageErr
}

func (authority *testLoopMountLockAuthority) Release(ctx context.Context) error {
	authority.releaseCount++
	if authority.events != nil {
		*authority.events = append(*authority.events, "authority")
	}
	authority.contextValueObserved = ctx.Value(authority.contextKey) == "retained"
	return authority.releaseErr
}

type testLoopMountAuthorityLock struct {
	err                  error
	events               *[]string
	contextKey           testRootfsLockContextKey
	releaseCount         int
	contextValueObserved bool
}

func (lock *testLoopMountAuthorityLock) Release(ctx context.Context) error {
	lock.releaseCount++
	if lock.events != nil {
		*lock.events = append(*lock.events, "lock")
	}
	lock.contextValueObserved = ctx.Value(lock.contextKey) == "retained"
	return lock.err
}
