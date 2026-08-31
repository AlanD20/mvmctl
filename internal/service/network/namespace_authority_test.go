package network

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/pkg/errs"
)

type namespaceAuthorityContextKey struct{}

// Rationale: parent capabilities close in reverse order while the child retains exact identity and cleanup authority.
func TestNamespaceHandleAuthorityMapsIdentityAndOutlivesParents(t *testing.T) {
	t.Parallel()

	recorder := &namespaceAuthorityRecorder{}
	target := &fakeNamespaceAuthorityTarget{
		identity: namespaceTargetIdentity{
			directoryDevice:   31,
			directoryInode:    37,
			directoryMount:    namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnique, value: 43},
			placeholderDevice: 41,
			placeholderInode:  73,
			placeholderMount: namespaceTargetMountIdentity{
				kind: namespaceTargetMountIdentityLegacy, value: 79,
			},
		},
		recorder: recorder,
	}
	uid := &fakeNamespaceAuthorityUID{target: target, recorder: recorder}
	root := &fakeNamespaceAuthorityRoot{uid: uid, recorder: recorder}
	authority := newNamespaceHandleAuthority(&fakeNamespaceAuthorityTree{root: root, recorder: recorder})
	ctx := context.WithValue(t.Context(), namespaceAuthorityContextKey{}, "retained")

	lease, err := authority.create(ctx, mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID))
	require.NoError(t, err)
	require.NotNil(t, lease)
	identity, err := lease.targetIdentity(ctx)
	require.NoError(t, err)
	wantIdentity := namespaceTargetIdentity{
		directoryDevice:   31,
		directoryInode:    37,
		directoryMount:    namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnique, value: 43},
		placeholderDevice: 41,
		placeholderInode:  73,
		placeholderMount: namespaceTargetMountIdentity{
			kind: namespaceTargetMountIdentityLegacy, value: 79,
		},
	}
	if diff := cmp.Diff(
		wantIdentity,
		identity,
		cmp.AllowUnexported(namespaceTargetIdentity{}, namespaceTargetMountIdentity{}),
	); diff != "" {
		t.Errorf("namespace target identity mismatch (-want +got):\n%s", diff)
	}
	require.NoError(t, lease.remove(ctx))
	require.NoError(t, lease.release(ctx))

	wantKey, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	assert.Equal(t, uint32(testOwnerUID), root.openedUID)
	assert.Equal(t, wantKey, uid.createdKey)
	wantEvents := []string{
		"open_network",
		"open_uid",
		"create_namespace_handle",
		"binding",
		"release_uid",
		"release_root",
		"binding",
		"target_identity",
		"remove_handle",
		"release_handle",
	}
	if diff := cmp.Diff(wantEvents, recorder.eventsSnapshot()); diff != "" {
		t.Errorf("namespace authority events mismatch (-want +got):\n%s", diff)
	}
	assert.True(t, recorder.allCleanupContextsSafe())
}

// Rationale: any fault after placeholder creation transfers the child while parent cleanup remains non-cancellable.
func TestNamespaceHandleAuthorityFaultsPreserveCleanupOwnership(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		configure func(*fakeNamespaceAuthorityTree, *fakeNamespaceAuthorityRoot, *fakeNamespaceAuthorityUID)
		wantLease bool
	}{
		"open root": {
			configure: func(tree *fakeNamespaceAuthorityTree, _ *fakeNamespaceAuthorityRoot, _ *fakeNamespaceAuthorityUID) {
				tree.openErr = errors.New("open root")
			},
		},
		"open UID": {
			configure: func(_ *fakeNamespaceAuthorityTree, root *fakeNamespaceAuthorityRoot, _ *fakeNamespaceAuthorityUID) {
				root.openErr = errors.New("open uid")
			},
		},
		"create before mutation": {
			configure: func(_ *fakeNamespaceAuthorityTree, _ *fakeNamespaceAuthorityRoot, uid *fakeNamespaceAuthorityUID) {
				uid.target = nil
				uid.createErr = errors.New("create placeholder")
			},
		},
		"create after mutation": {
			configure: func(_ *fakeNamespaceAuthorityTree, _ *fakeNamespaceAuthorityRoot, uid *fakeNamespaceAuthorityUID) {
				uid.createErr = errors.New("prepare placeholder")
			},
			wantLease: true,
		},
		"release UID": {
			configure: func(_ *fakeNamespaceAuthorityTree, _ *fakeNamespaceAuthorityRoot, uid *fakeNamespaceAuthorityUID) {
				uid.releaseErr = errors.New("release uid")
			},
			wantLease: true,
		},
		"release root": {
			configure: func(_ *fakeNamespaceAuthorityTree, root *fakeNamespaceAuthorityRoot, _ *fakeNamespaceAuthorityUID) {
				root.releaseErr = errors.New("release root")
			},
			wantLease: true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := &namespaceAuthorityRecorder{}
			target := &fakeNamespaceAuthorityTarget{
				identity: namespaceTargetIdentity{
					directoryDevice: 2,
					directoryInode:  3,
					directoryMount: namespaceTargetMountIdentity{
						kind: namespaceTargetMountIdentityUnavailable,
					},
					placeholderDevice: 5,
					placeholderInode:  7,
					placeholderMount: namespaceTargetMountIdentity{
						kind: namespaceTargetMountIdentityUnavailable,
					},
				},
				recorder: recorder,
			}
			uid := &fakeNamespaceAuthorityUID{target: target, recorder: recorder}
			root := &fakeNamespaceAuthorityRoot{uid: uid, recorder: recorder}
			tree := &fakeNamespaceAuthorityTree{root: root, recorder: recorder}
			test.configure(tree, root, uid)
			authority := newNamespaceHandleAuthority(tree)

			lease, err := authority.create(
				t.Context(),
				mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID),
			)
			require.Error(t, err)
			assert.Equal(t, test.wantLease, lease != nil)
			assertNamespaceAuthorityCreateError(t, err, test.wantLease)
		})
	}
}

func TestNamespaceHandleAuthorityCancellationAfterMutationReturnsLease(t *testing.T) {
	t.Parallel()

	recorder := &namespaceAuthorityRecorder{}
	target := &fakeNamespaceAuthorityTarget{
		identity: namespaceTargetIdentity{
			directoryDevice:   11,
			directoryInode:    13,
			directoryMount:    namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityLegacy, value: 17},
			placeholderDevice: 19,
			placeholderInode:  23,
			placeholderMount: namespaceTargetMountIdentity{
				kind: namespaceTargetMountIdentityUnique, value: 29,
			},
		},
		recorder: recorder,
	}
	ctx, cancel := context.WithCancel(context.WithValue(
		t.Context(),
		namespaceAuthorityContextKey{},
		"retained",
	))
	uid := &fakeNamespaceAuthorityUID{target: target, recorder: recorder, afterCreate: cancel}
	root := &fakeNamespaceAuthorityRoot{uid: uid, recorder: recorder}
	authority := newNamespaceHandleAuthority(&fakeNamespaceAuthorityTree{root: root, recorder: recorder})

	lease, err := authority.create(ctx, mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID))
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, lease)
	assertNamespaceAuthorityCreateError(t, err, true)
	assert.True(t, recorder.allCleanupContextsSafe())
}

func TestNamespaceHandleLeaseRejectsIncompleteIdentity(t *testing.T) {
	t.Parallel()

	target := &fakeNamespaceAuthorityTarget{identity: namespaceTargetIdentity{
		directoryDevice:   1,
		directoryInode:    2,
		directoryMount:    namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityLegacy, value: 3},
		placeholderDevice: 5,
		placeholderMount:  namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityLegacy, value: 7},
	}}
	vmKey, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	lease := &namespaceHandleLease{
		target: target, ownerUID: uint32(testOwnerUID), vmID: VMID(testVMID), vmKey: vmKey,
	}

	identity, err := lease.targetIdentity(t.Context())
	assert.Equal(t, namespaceTargetIdentity{}, identity)
	domainErr := requireNamespaceDomainError(t, err)
	require.ErrorContains(t, domainErr.Err, "invalid")
}

// Rationale: a valid capability for a different authenticated owner or VM must never authorize namespace effects.
func TestNamespaceHandleAuthorityRejectsBindingMismatch(t *testing.T) {
	t.Parallel()

	wantKey, err := authorityfs.NewVMKey(testVMID)
	require.NoError(t, err)
	otherKey, err := authorityfs.NewVMKey("fedcba9876543210fedcba9876543210")
	require.NoError(t, err)
	tests := map[string]struct {
		ownerUID uint32
		vmKey    authorityfs.VMKey
	}{
		"owner":  {ownerUID: uint32(testOwnerUID + 1), vmKey: wantKey},
		"VM key": {ownerUID: uint32(testOwnerUID), vmKey: otherKey},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			recorder := &namespaceAuthorityRecorder{}
			target := &fakeNamespaceAuthorityTarget{
				identity: namespaceTargetIdentity{
					directoryDevice: 2,
					directoryInode:  3,
					directoryMount: namespaceTargetMountIdentity{
						kind: namespaceTargetMountIdentityUnavailable,
					},
					placeholderDevice: 5,
					placeholderInode:  7,
					placeholderMount: namespaceTargetMountIdentity{
						kind: namespaceTargetMountIdentityUnavailable,
					},
				},
				ownerUID: test.ownerUID,
				vmKey:    test.vmKey,
				recorder: recorder,
			}
			uid := &fakeNamespaceAuthorityUID{target: target, recorder: recorder}
			root := &fakeNamespaceAuthorityRoot{uid: uid, recorder: recorder}
			authority := newNamespaceHandleAuthority(&fakeNamespaceAuthorityTree{root: root, recorder: recorder})

			lease, createErr := authority.create(
				t.Context(),
				mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID),
			)
			require.ErrorContains(t, createErr, "binding")
			require.NotNil(t, lease)
			assertNamespaceAuthorityCreateError(t, createErr, true)
			assert.Zero(t, recorder.eventCount("target_identity"))
			require.NoError(t, lease.remove(t.Context()))
			require.NoError(t, lease.release(t.Context()))
		})
	}
}

func TestNamespaceTargetMountIdentityMappingPreservesKindAndValue(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		given authorityfs.NetworkNamespaceMountIdentity
		want  namespaceTargetMountIdentity
	}{
		"unavailable": {
			given: authorityfs.NetworkNamespaceMountIdentity{
				Kind: authorityfs.NetworkNamespaceMountIdentityUnavailable,
			},
			want: namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnavailable},
		},
		"legacy": {
			given: authorityfs.NetworkNamespaceMountIdentity{
				Kind: authorityfs.NetworkNamespaceMountIdentityLegacy, Value: 41,
			},
			want: namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityLegacy, value: 41},
		},
		"unique": {
			given: authorityfs.NetworkNamespaceMountIdentity{
				Kind: authorityfs.NetworkNamespaceMountIdentityUnique, Value: 41,
			},
			want: namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnique, value: 41},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := mapNamespaceTargetMountIdentity(test.given)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
	assert.NotEqual(
		t,
		namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityLegacy, value: 41},
		namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityUnique, value: 41},
	)
	invalid := []authorityfs.NetworkNamespaceMountIdentity{
		{Kind: authorityfs.NetworkNamespaceMountIdentityUnavailable, Value: 1},
		{Kind: authorityfs.NetworkNamespaceMountIdentityLegacy},
		{Kind: authorityfs.NetworkNamespaceMountIdentityKind(255), Value: 1},
	}
	for _, identity := range invalid {
		_, err := mapNamespaceTargetMountIdentity(identity)
		require.Error(t, err)
	}
}

func requireNamespaceDomainError(t *testing.T, err error) *errs.DomainError {
	t.Helper()
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	return domainErr
}

func assertNamespaceAuthorityCreateError(t *testing.T, err error, cleanupRequired bool) {
	t.Helper()
	domainErr := requireNamespaceDomainError(t, err)
	var wantDetails map[string]any
	if cleanupRequired {
		wantDetails = map[string]any{"cleanup_required": true}
	}
	want := namespaceErrorMetadata{
		Code: errs.CodeNetworkCreateFailed, Op: "network", Entity: testVMID, Class: errs.ClassInternal,
		Details: wantDetails,
	}
	got := namespaceErrorMetadata{
		Code: domainErr.Code, Op: domainErr.Op, Entity: domainErr.Entity, Class: domainErr.Class,
		Details: domainErr.Details,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("namespace authority error mismatch (-want +got):\n%s", diff)
	}
}

type namespaceAuthorityRecorder struct {
	mu       sync.Mutex
	events   []string
	contexts []bool
}

func (recorder *namespaceAuthorityRecorder) add(event string) {
	if recorder == nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
}

func (recorder *namespaceAuthorityRecorder) addCleanup(ctx context.Context, event string) {
	if recorder == nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.events = append(recorder.events, event)
	recorder.contexts = append(
		recorder.contexts,
		ctx.Err() == nil && ctx.Value(namespaceAuthorityContextKey{}) == "retained",
	)
}

func (recorder *namespaceAuthorityRecorder) eventsSnapshot() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.events...)
}

func (recorder *namespaceAuthorityRecorder) eventCount(event string) int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	var count int
	for _, got := range recorder.events {
		if got == event {
			count++
		}
	}
	return count
}

func (recorder *namespaceAuthorityRecorder) allCleanupContextsSafe() bool {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.contexts) == 0 {
		return false
	}
	for _, safe := range recorder.contexts {
		if !safe {
			return false
		}
	}
	return true
}

type fakeNamespaceAuthorityTree struct {
	root     networkLockRoot
	openErr  error
	recorder *namespaceAuthorityRecorder
}

func (tree *fakeNamespaceAuthorityTree) openNetwork(context.Context) (networkLockRoot, error) {
	tree.recorder.add("open_network")
	return tree.root, tree.openErr
}

type fakeNamespaceAuthorityRoot struct {
	uid        networkLockUID
	openErr    error
	releaseErr error
	recorder   *namespaceAuthorityRecorder
	openedUID  uint32
}

func (*fakeNamespaceAuthorityRoot) acquireIndex(context.Context) (networkAuthorityLock, error) {
	return nil, errors.New("unexpected index lock acquisition")
}

func (root *fakeNamespaceAuthorityRoot) openUID(_ context.Context, uid uint32) (networkLockUID, error) {
	root.recorder.add("open_uid")
	root.openedUID = uid
	return root.uid, root.openErr
}

func (root *fakeNamespaceAuthorityRoot) Release(ctx context.Context) error {
	root.recorder.addCleanup(ctx, "release_root")
	return root.releaseErr
}

type fakeNamespaceAuthorityUID struct {
	target      namespaceAuthorityTarget
	createErr   error
	releaseErr  error
	recorder    *namespaceAuthorityRecorder
	createdKey  authorityfs.VMKey
	afterCreate func()
}

func (*fakeNamespaceAuthorityUID) acquireNetwork(
	context.Context,
	authorityfs.NetworkKey,
) (networkAuthorityLock, error) {
	return nil, errors.New("unexpected managed-network lock acquisition")
}

func (uid *fakeNamespaceAuthorityUID) createNamespaceHandle(
	_ context.Context,
	key authorityfs.VMKey,
) (namespaceAuthorityTarget, error) {
	uid.recorder.add("create_namespace_handle")
	uid.createdKey = key
	if uid.afterCreate != nil {
		uid.afterCreate()
	}
	return uid.target, uid.createErr
}

func (uid *fakeNamespaceAuthorityUID) Release(ctx context.Context) error {
	uid.recorder.addCleanup(ctx, "release_uid")
	return uid.releaseErr
}

type fakeNamespaceAuthorityTarget struct {
	identity   namespaceTargetIdentity
	ownerUID   uint32
	vmKey      authorityfs.VMKey
	bindingErr error
	recorder   *namespaceAuthorityRecorder
}

func (target *fakeNamespaceAuthorityTarget) binding() (uint32, authorityfs.VMKey, error) {
	target.recorder.add("binding")
	ownerUID := target.ownerUID
	if ownerUID == 0 {
		ownerUID = uint32(testOwnerUID)
	}
	vmKey := target.vmKey
	if vmKey == (authorityfs.VMKey{}) {
		vmKey, _ = authorityfs.NewVMKey(testVMID)
	}
	return ownerUID, vmKey, target.bindingErr
}

func (target *fakeNamespaceAuthorityTarget) targetIdentity(
	context.Context,
) (namespaceTargetIdentity, error) {
	target.recorder.add("target_identity")
	return target.identity, nil
}

func (target *fakeNamespaceAuthorityTarget) remove(context.Context) error {
	target.recorder.add("remove_handle")
	return nil
}

func (target *fakeNamespaceAuthorityTarget) release(context.Context) error {
	target.recorder.add("release_handle")
	return nil
}
