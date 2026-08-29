package network

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

const (
	testOwnerUID  = uint32(1000)
	testVMID      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testNetworkID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestNewTopologyKeyRejectsUntrustedIdentityShapes(t *testing.T) {
	tests := map[string]struct {
		ownerUID    uint32
		vmID        string
		networkID   string
		wantMessage string
	}{
		"root_cannot_own_user_vm": {
			ownerUID: 0, vmID: testVMID, networkID: testNetworkID,
			wantMessage: "owner UID must identify a non-root user",
		},
		"vm_id_must_be_immutable_hex_id": {
			ownerUID: testOwnerUID, vmID: "../../caller-name", networkID: testNetworkID,
			wantMessage: "vm ID must be 32 lowercase hexadecimal characters",
		},
		"network_id_must_be_immutable_hex_id": {
			ownerUID: testOwnerUID, vmID: testVMID, networkID: "bridge-from-caller",
			wantMessage: "network ID must be 64 lowercase hexadecimal characters",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := newTopologyKey(tc.ownerUID, tc.vmID, tc.networkID)

			require.Error(t, err)
			de := errs.AsDomainError(err)
			require.NotNil(t, de)
			assert.Equal(t, errs.CodeValidationFailed, de.Code)
			assert.Equal(t, tc.wantMessage, de.Message)
		})
	}
}

func TestDeriveTopologyNamesIsStableAndIdentityBound(t *testing.T) {
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

	got := deriveTopologyNames(key)
	want := topologyNames{
		namespaceHandle: namespaceHandlePath(
			"/run/mvmctl/authority/1000/netns/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		),
		managedBridge: linkName("mvbeec319e5586b"),
		hostVeth:      linkName("mvhe89e2abf9abd"),
		namespaceVeth: linkName("mvne40ca9fc46c4"),
		localBridge:   linkName("mvlbc942db0d115"),
		tap:           linkName("mvt0444ca744944"),
	}

	if diff := cmp.Diff(want, got, cmp.AllowUnexported(topologyNames{})); diff != "" {
		t.Errorf("deriveTopologyNames() mismatch (-want +got):\n%s", diff)
	}
	for _, name := range []linkName{
		got.managedBridge, got.hostVeth, got.namespaceVeth, got.localBridge, got.tap,
	} {
		assert.LessOrEqual(t, len(name.String()), linuxInterfaceNameMax)
	}

	otherOwner := deriveTopologyNames(mustTopologyKey(t, testOwnerUID+1, testVMID, testNetworkID))
	otherVM := deriveTopologyNames(mustTopologyKey(
		t,
		testOwnerUID,
		"caaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		testNetworkID,
	))
	otherNetwork := deriveTopologyNames(mustTopologyKey(
		t,
		testOwnerUID,
		testVMID,
		"dbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	))

	for _, pair := range [][2]linkName{
		{got.managedBridge, otherOwner.managedBridge},
		{got.hostVeth, otherOwner.hostVeth},
		{got.namespaceVeth, otherOwner.namespaceVeth},
		{got.localBridge, otherOwner.localBridge},
		{got.tap, otherOwner.tap},
	} {
		assert.NotEqual(t, pair[0], pair[1], "owner identity must bind every managed link")
	}
	assert.Equal(t, got.managedBridge, otherVM.managedBridge)
	assert.NotEqual(t, got.hostVeth, otherVM.hostVeth)
	assert.NotEqual(t, got.namespaceVeth, otherVM.namespaceVeth)
	assert.NotEqual(t, got.localBridge, otherVM.localBridge)
	assert.NotEqual(t, got.tap, otherVM.tap)
	assert.NotEqual(t, got.managedBridge, otherNetwork.managedBridge)
	assert.Equal(t, got.hostVeth, otherNetwork.hostVeth)
	assert.Equal(t, got.namespaceVeth, otherNetwork.namespaceVeth)
	assert.Equal(t, got.localBridge, otherNetwork.localBridge)
	assert.Equal(t, got.tap, otherNetwork.tap)
}

func TestServicePrepareBuildsDownTopologyWhileHoldingOrderedLeases(t *testing.T) {
	fixture := newTopologyFixture(t)

	preparation, err := fixture.service.prepare(context.Background(), fixture.launch, fixture.key.NetworkID())

	require.NoError(t, err)
	require.NotNil(t, preparation)
	wantOrder := []string{
		"lock_network",
		"create_namespace",
		"create_local_bridge_down",
		"create_tap_down",
		"create_veth_pair_down",
		"attach_namespace_veth",
		"attach_tap",
		"attach_host_veth",
	}
	if diff := cmp.Diff(wantOrder, *fixture.events); diff != "" {
		t.Errorf("creation order mismatch (-want +got):\n%s", diff)
	}
	assert.False(t, fixture.lock.released, "network lock must span later firewall and launch integration")
	assert.False(t, fixture.pin.released, "namespace pin must span later Jailer integration")

	topology := preparation.Topology()
	wantTopology := Topology{
		key:           fixture.key,
		handle:        fixture.pin.handle,
		managedBridge: fixture.lock.bridge,
		localBridge:   fixture.effects.localBridge,
		tap:           fixture.effects.tap,
		veth:          fixture.effects.veth,
	}
	if diff := cmp.Diff(
		wantTopology,
		topology,
		cmp.AllowUnexported(
			Topology{},
			topologyKey{},
			namespaceHandle{},
			namespaceIdentity{},
			linkIdentity{},
			vethPairIdentity{},
		),
	); diff != "" {
		t.Errorf("prepared topology mismatch (-want +got):\n%s", diff)
	}
	wantRecord := TopologyRecord{
		OwnerUID:  testOwnerUID,
		VMID:      testVMID,
		NetworkID: testNetworkID,
		Namespace: NamespaceHandle{
			Path: "/run/mvmctl/authority/1000/netns/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Identity: NamespaceIdentity{
				Device: 41,
				Inode:  42,
			},
			OwnerUID: 0,
			OwnerGID: 0,
			Mode:     namespaceHandleMode,
		},
		ManagedBridge: LinkIdentity{
			Name:    "mvbeec319e5586b",
			IfIndex: 10,
			Namespace: NamespaceIdentity{
				Device: 51,
				Inode:  52,
			},
		},
		LocalBridge: LinkIdentity{
			Name:    "mvlbc942db0d115",
			IfIndex: 20,
			Namespace: NamespaceIdentity{
				Device: 41,
				Inode:  42,
			},
		},
		TAP: LinkIdentity{
			Name:    "mvt0444ca744944",
			IfIndex: 21,
			Namespace: NamespaceIdentity{
				Device: 41,
				Inode:  42,
			},
		},
		VethPair: VethPairIdentity{
			Host: LinkIdentity{
				Name:    "mvhe89e2abf9abd",
				IfIndex: 22,
				Namespace: NamespaceIdentity{
					Device: 51,
					Inode:  52,
				},
			},
			Peer: LinkIdentity{
				Name:    "mvne40ca9fc46c4",
				IfIndex: 23,
				Namespace: NamespaceIdentity{
					Device: 41,
					Inode:  42,
				},
			},
		},
	}
	if diff := cmp.Diff(wantRecord, topology.Export()); diff != "" {
		t.Errorf("exported topology authority mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, topologyPreparationPrepared, preparation.state)

	require.NoError(t, preparation.Cleanup(context.Background()))
	require.NoError(t, preparation.Cleanup(context.Background()))
	assert.True(t, fixture.pin.released)
	assert.True(t, fixture.lock.released)
	wantDeletes := []string{"delete_veth_pair", "delete_tap", "delete_local_bridge", "delete_namespace"}
	if diff := cmp.Diff(wantDeletes, fixture.effects.deleteEvents); diff != "" {
		t.Errorf("prepared topology cleanup mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, topologyPreparationCleaned, preparation.state)
}

func TestServicePrepareRequiresVMLockBeforeNetworkLock(t *testing.T) {
	fixture := newTopologyFixture(t)
	fixture.launch.lock = nil

	preparation, err := fixture.service.prepare(context.Background(), fixture.launch, fixture.key.NetworkID())

	require.Error(t, err)
	assert.Nil(t, preparation)
	de := errs.AsDomainError(err)
	require.NotNil(t, de)
	assert.Equal(t, errs.CodeNetworkCreateFailed, de.Code)
	assert.Equal(t, errs.ClassInternal, de.Class)
	cause := errs.AsDomainError(de.Err)
	require.NotNil(t, cause)
	assert.Equal(t, errs.CodeValidationFailed, cause.Code)
	assert.Equal(t, "vm launch lease is not active", cause.Message)
	assert.Empty(t, *fixture.events)
}

func TestServicePrepareRollsBackEveryCompletedPrefix(t *testing.T) {
	tests := []struct {
		name        string
		failAt      string
		wantDeletes []string
	}{
		{name: "namespace", failAt: "create_namespace"},
		{name: "local_bridge", failAt: "create_local_bridge_down", wantDeletes: []string{"delete_namespace"}},
		{name: "tap", failAt: "create_tap_down", wantDeletes: []string{"delete_local_bridge", "delete_namespace"}},
		{
			name:   "veth_pair",
			failAt: "create_veth_pair_down",
			wantDeletes: []string{
				"delete_tap", "delete_local_bridge", "delete_namespace",
			},
		},
		{
			name:   "namespace_veth_attachment",
			failAt: "attach_namespace_veth",
			wantDeletes: []string{
				"delete_veth_pair", "delete_tap", "delete_local_bridge", "delete_namespace",
			},
		},
		{
			name:   "tap_attachment",
			failAt: "attach_tap",
			wantDeletes: []string{
				"delete_veth_pair", "delete_tap", "delete_local_bridge", "delete_namespace",
			},
		},
		{
			name:   "host_veth_attachment",
			failAt: "attach_host_veth",
			wantDeletes: []string{
				"delete_veth_pair", "delete_tap", "delete_local_bridge", "delete_namespace",
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTopologyFixture(t)
			fixture.effects.failAt = tc.failAt

			preparation, err := fixture.service.prepare(
				context.Background(),
				fixture.launch,
				fixture.key.NetworkID(),
			)

			require.Error(t, err)
			assert.Nil(t, preparation)
			de := errs.AsDomainError(err)
			require.NotNil(t, de)
			assert.Equal(t, errs.CodeNetworkCreateFailed, de.Code)
			if diff := cmp.Diff(tc.wantDeletes, fixture.effects.deleteEvents); diff != "" {
				t.Errorf("rollback order mismatch (-want +got):\n%s", diff)
			}
			assert.True(t, fixture.lock.released)
			if tc.failAt == "create_namespace" {
				assert.False(t, fixture.pin.released)
			} else {
				assert.True(t, fixture.pin.released)
			}
		})
	}
}

// Rationale: A syscall adapter may create a resource and then fail while pinning or inspecting it. The returned exact
// partial identity must be recorded before the error is handled so rollback cannot leak that resource.
func TestServicePrepareRollsBackMutationThenError(t *testing.T) {
	tests := []struct {
		name        string
		failAt      string
		wantDeletes []string
	}{
		{
			name:        "namespace_survives_error",
			failAt:      "create_namespace",
			wantDeletes: []string{"delete_namespace"},
		},
		{
			name:        "local_bridge_survives_error",
			failAt:      "create_local_bridge_down",
			wantDeletes: []string{"delete_local_bridge", "delete_namespace"},
		},
		{
			name:        "tap_survives_error",
			failAt:      "create_tap_down",
			wantDeletes: []string{"delete_tap", "delete_local_bridge", "delete_namespace"},
		},
		{
			name:   "veth_pair_survives_error",
			failAt: "create_veth_pair_down",
			wantDeletes: []string{
				"delete_veth_pair", "delete_tap", "delete_local_bridge", "delete_namespace",
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTopologyFixture(t)
			fixture.effects.failAfterMutationAt = tc.failAt

			preparation, err := fixture.service.prepare(
				context.Background(),
				fixture.launch,
				fixture.key.NetworkID(),
			)

			require.Error(t, err)
			assert.Nil(t, preparation)
			de := errs.AsDomainError(err)
			require.NotNil(t, de)
			assert.Equal(t, errs.CodeNetworkCreateFailed, de.Code)
			if diff := cmp.Diff(tc.wantDeletes, fixture.effects.deleteEvents); diff != "" {
				t.Errorf("mutation-then-error rollback mismatch (-want +got):\n%s", diff)
			}
			assert.True(t, fixture.pin.released)
			assert.True(t, fixture.lock.released)
		})
	}
}

// Rationale: A rollback failure must retain the exact lock, namespace pin, and remaining identity so cleanup can be
// retried without rediscovering or trusting mutable host names.
func TestServicePrepareReturnsRecoveryPreparationWhenRollbackFails(t *testing.T) {
	fixture := newTopologyFixture(t)
	fixture.effects.failAt = "attach_host_veth"
	fixture.effects.deleteFailures["delete_veth_pair"] = errors.New("injected delete failure")

	preparation, err := fixture.service.prepare(context.Background(), fixture.launch, fixture.key.NetworkID())

	require.Error(t, err)
	require.NotNil(t, preparation)
	de := errs.AsDomainError(err)
	require.NotNil(t, de)
	assert.Equal(t, true, de.Details["cleanup_required"])
	assert.False(t, fixture.lock.released)
	assert.False(t, fixture.pin.released)
	if diff := cmp.Diff([]string{"delete_veth_pair"}, fixture.effects.deleteEvents); diff != "" {
		t.Errorf("failed rollback prefix mismatch (-want +got):\n%s", diff)
	}

	delete(fixture.effects.deleteFailures, "delete_veth_pair")
	require.NoError(t, preparation.Cleanup(context.Background()))
	require.NoError(t, preparation.Cleanup(context.Background()))
	assert.True(t, fixture.lock.released)
	assert.True(t, fixture.pin.released)
	wantDeletes := []string{
		"delete_veth_pair",
		"delete_veth_pair",
		"delete_tap",
		"delete_local_bridge",
		"delete_namespace",
	}
	if diff := cmp.Diff(wantDeletes, fixture.effects.deleteEvents); diff != "" {
		t.Errorf("retried cleanup order mismatch (-want +got):\n%s", diff)
	}
}

func TestTopologyPreparationCleanupRetriesOnlyTheFailedSuffix(t *testing.T) {
	fixture := newTopologyFixture(t)
	preparation, err := fixture.service.prepare(context.Background(), fixture.launch, fixture.key.NetworkID())
	require.NoError(t, err)
	fixture.effects.deleteFailures["delete_tap"] = errors.New("injected TAP cleanup failure")

	err = preparation.Cleanup(context.Background())

	require.Error(t, err)
	de := errs.AsDomainError(err)
	require.NotNil(t, de)
	assert.Equal(t, errs.CodeNetworkRemoveFailed, de.Code)
	if diff := cmp.Diff(
		[]string{"delete_veth_pair", "delete_tap"},
		fixture.effects.deleteEvents,
	); diff != "" {
		t.Errorf("failed cleanup suffix mismatch (-want +got):\n%s", diff)
	}
	assert.False(t, fixture.lock.released)
	assert.False(t, fixture.pin.released)

	delete(fixture.effects.deleteFailures, "delete_tap")
	require.NoError(t, preparation.Cleanup(context.Background()))
	require.NoError(t, preparation.Cleanup(context.Background()))
	wantDeletes := []string{
		"delete_veth_pair",
		"delete_tap",
		"delete_tap",
		"delete_local_bridge",
		"delete_namespace",
	}
	if diff := cmp.Diff(wantDeletes, fixture.effects.deleteEvents); diff != "" {
		t.Errorf("cleanup retry mismatch (-want +got):\n%s", diff)
	}
	assert.True(t, fixture.lock.released)
	assert.True(t, fixture.pin.released)
}

// Rationale: The create context may be cancelled at the failure point. Rollback must deliberately use a non-cancelled
// context or every namespace and link created earlier in the transaction would leak.
func TestServicePrepareRollbackSurvivesCallerCancellation(t *testing.T) {
	fixture := newTopologyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.effects.cancelAt = "attach_host_veth"
	fixture.effects.cancel = cancel

	preparation, err := fixture.service.prepare(ctx, fixture.launch, fixture.key.NetworkID())

	require.Error(t, err)
	assert.Nil(t, preparation)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, fixture.effects.cleanupSawCancellation)
	assert.True(t, fixture.pin.released)
	assert.True(t, fixture.lock.released)
}

func TestServicePrepareRejectsUnpinnedOrMismatchedEffectIdentity(t *testing.T) {
	tests := map[string]func(*topologyFixture){
		"namespace_handle_not_root_owned": func(f *topologyFixture) {
			f.pin.handle.ownerUID = testOwnerUID
		},
		"namespace_path_not_derived": func(f *topologyFixture) {
			f.pin.handle.path = namespaceHandlePath("/tmp/caller-controlled")
		},
		"namespace_identity_missing": func(f *topologyFixture) {
			f.pin.handle.identity = namespaceIdentity{}
		},
		"local_bridge_name_mismatch": func(f *topologyFixture) {
			f.effects.mutateAt = "create_local_bridge_down"
		},
		"tap_namespace_mismatch": func(f *topologyFixture) {
			f.effects.mutateAt = "create_tap_down"
		},
		"veth_name_mismatch": func(f *topologyFixture) {
			f.effects.mutateAt = "create_veth_pair_down"
		},
		"namespace_effect_omits_pin": func(f *topologyFixture) {
			f.effects.omitAt = "create_namespace"
		},
		"local_bridge_effect_omits_identity": func(f *topologyFixture) {
			f.effects.omitAt = "create_local_bridge_down"
		},
		"tap_effect_omits_identity": func(f *topologyFixture) {
			f.effects.omitAt = "create_tap_down"
		},
		"veth_effect_omits_identity": func(f *topologyFixture) {
			f.effects.omitAt = "create_veth_pair_down"
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newTopologyFixture(t)
			mutate(fixture)

			preparation, err := fixture.service.prepare(
				context.Background(),
				fixture.launch,
				fixture.key.NetworkID(),
			)

			require.Error(t, err)
			assert.Nil(t, preparation)
			de := errs.AsDomainError(err)
			require.NotNil(t, de)
			assert.Equal(t, errs.CodeNetworkCreateFailed, de.Code)
			assert.True(t, fixture.lock.released)
		})
	}
}

type topologyFixture struct {
	key     topologyKey
	events  *[]string
	launch  *vmLaunchLease
	lock    *fakeNetworkLock
	locker  *fakeNetworkLocker
	pin     *fakeNamespacePin
	effects *fakeTopologyEffects
	service *service
}

func newTopologyFixture(t *testing.T) *topologyFixture {
	t.Helper()
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	names := deriveTopologyNames(key)
	vmNamespace := namespaceIdentity{device: 41, inode: 42}
	hostNamespace := namespaceIdentity{device: 51, inode: 52}
	events := make([]string, 0, 16)
	pin := &fakeNamespacePin{handle: namespaceHandle{
		path:     names.namespaceHandle,
		identity: vmNamespace,
		ownerUID: 0,
		ownerGID: 0,
		mode:     namespaceHandleMode,
	}}
	lock := &fakeNetworkLock{
		key: key.Network(),
		bridge: linkIdentity{
			name:      names.managedBridge,
			ifIndex:   10,
			namespace: hostNamespace,
		},
		events: &events,
	}
	locker := &fakeNetworkLocker{lock: lock, events: &events}
	effects := &fakeTopologyEffects{
		pin:            pin,
		vmNamespace:    vmNamespace,
		hostNamespace:  hostNamespace,
		events:         &events,
		deleteFailures: make(map[string]error),
	}
	service := newService(locker, effects)
	return &topologyFixture{
		key: key, events: &events,
		launch:  &vmLaunchLease{ownerUID: key.OwnerUID(), vmID: key.VMID(), lock: &vmLockCapability{}},
		lock:    lock,
		locker:  locker,
		pin:     pin,
		effects: effects,
		service: service,
	}
}

func mustTopologyKey(t *testing.T, ownerUID uint32, vmID, networkID string) topologyKey {
	t.Helper()
	key, err := newTopologyKey(ownerUID, vmID, networkID)
	require.NoError(t, err)
	return key
}

type fakeNetworkLocker struct {
	lock   *fakeNetworkLock
	events *[]string
}

func (locker *fakeNetworkLocker) LockNetwork(
	_ context.Context,
	key networkKey,
) (NetworkLock, error) {
	*locker.events = append(*locker.events, "lock_network")
	if key != locker.lock.key {
		return nil, errors.New("unexpected network lock key")
	}
	return locker.lock, nil
}

type fakeNetworkLock struct {
	key      networkKey
	bridge   linkIdentity
	released bool
	events   *[]string
}

func (lock *fakeNetworkLock) Key() networkKey             { return lock.key }
func (lock *fakeNetworkLock) ManagedBridge() linkIdentity { return lock.bridge }
func (lock *fakeNetworkLock) Release(_ context.Context) error {
	if lock.released {
		return nil
	}
	*lock.events = append(*lock.events, "release_network_lock")
	lock.released = true
	return nil
}

type fakeNamespacePin struct {
	handle   namespaceHandle
	released bool
}

func (pin *fakeNamespacePin) Handle() namespaceHandle { return pin.handle }
func (pin *fakeNamespacePin) Release(_ context.Context) error {
	pin.released = true
	return nil
}

type fakeTopologyEffects struct {
	pin                    *fakeNamespacePin
	vmNamespace            namespaceIdentity
	hostNamespace          namespaceIdentity
	localBridge            linkIdentity
	tap                    linkIdentity
	veth                   vethPairIdentity
	events                 *[]string
	deleteEvents           []string
	failAt                 string
	failAfterMutationAt    string
	mutateAt               string
	omitAt                 string
	cancelAt               string
	cancel                 context.CancelFunc
	cleanupSawCancellation bool
	deleteFailures         map[string]error
}

func (effects *fakeTopologyEffects) CreateNamespace(
	ctx context.Context,
	_ namespaceCreateSpec,
) (NamespacePin, error) {
	if err := effects.enter(ctx, "create_namespace"); err != nil {
		return nil, err
	}
	if effects.omitAt == "create_namespace" {
		return nil, nil
	}
	if err := effects.afterMutation("create_namespace"); err != nil {
		return effects.pin, err
	}
	return effects.pin, nil
}

func (effects *fakeTopologyEffects) CreateLocalBridgeDown(
	ctx context.Context,
	_ NamespacePin,
	spec localBridgeCreateSpec,
) (*linkIdentity, error) {
	if err := effects.enter(ctx, "create_local_bridge_down"); err != nil {
		return nil, err
	}
	if effects.omitAt == "create_local_bridge_down" {
		return nil, nil
	}
	identity := linkIdentity{name: spec.name, ifIndex: 20, namespace: effects.vmNamespace}
	if effects.mutateAt == "create_local_bridge_down" {
		identity.name = linkName("caller-name")
	}
	effects.localBridge = identity
	if err := effects.afterMutation("create_local_bridge_down"); err != nil {
		return &identity, err
	}
	return &identity, nil
}

func (effects *fakeTopologyEffects) CreateTAPDown(
	ctx context.Context,
	_ NamespacePin,
	spec tapCreateSpec,
) (*linkIdentity, error) {
	if err := effects.enter(ctx, "create_tap_down"); err != nil {
		return nil, err
	}
	if effects.omitAt == "create_tap_down" {
		return nil, nil
	}
	identity := linkIdentity{name: spec.name, ifIndex: 21, namespace: effects.vmNamespace}
	if effects.mutateAt == "create_tap_down" {
		identity.namespace = effects.hostNamespace
	}
	effects.tap = identity
	if err := effects.afterMutation("create_tap_down"); err != nil {
		return &identity, err
	}
	return &identity, nil
}

func (effects *fakeTopologyEffects) CreateVethPairDown(
	ctx context.Context,
	_ NamespacePin,
	spec vethCreateSpec,
) (*vethPairIdentity, error) {
	if err := effects.enter(ctx, "create_veth_pair_down"); err != nil {
		return nil, err
	}
	if effects.omitAt == "create_veth_pair_down" {
		return nil, nil
	}
	identity := vethPairIdentity{
		host: linkIdentity{name: spec.hostName, ifIndex: 22, namespace: effects.hostNamespace},
		peer: linkIdentity{name: spec.peerName, ifIndex: 23, namespace: effects.vmNamespace},
	}
	if effects.mutateAt == "create_veth_pair_down" {
		identity.peer.name = linkName("caller-name")
	}
	effects.veth = identity
	if err := effects.afterMutation("create_veth_pair_down"); err != nil {
		return &identity, err
	}
	return &identity, nil
}

func (effects *fakeTopologyEffects) AttachNamespaceVethToLocalBridge(
	ctx context.Context,
	_ NamespacePin,
	_ vethPairIdentity,
	_ linkIdentity,
) error {
	return effects.enter(ctx, "attach_namespace_veth")
}

func (effects *fakeTopologyEffects) AttachTAPToLocalBridge(
	ctx context.Context,
	_ NamespacePin,
	_ linkIdentity,
	_ linkIdentity,
) error {
	return effects.enter(ctx, "attach_tap")
}

func (effects *fakeTopologyEffects) AttachHostVethToManagedBridge(
	ctx context.Context,
	_ vethPairIdentity,
	_ linkIdentity,
) error {
	return effects.enter(ctx, "attach_host_veth")
}

func (effects *fakeTopologyEffects) DeleteVethPair(
	ctx context.Context,
	_ NamespacePin,
	_ vethPairIdentity,
) error {
	return effects.delete(ctx, "delete_veth_pair")
}

func (effects *fakeTopologyEffects) DeleteTAP(
	ctx context.Context,
	_ NamespacePin,
	_ linkIdentity,
) error {
	return effects.delete(ctx, "delete_tap")
}

func (effects *fakeTopologyEffects) DeleteLocalBridge(
	ctx context.Context,
	_ NamespacePin,
	_ linkIdentity,
) error {
	return effects.delete(ctx, "delete_local_bridge")
}

func (effects *fakeTopologyEffects) DeleteNamespace(
	ctx context.Context,
	_ NamespacePin,
	_ namespaceIdentity,
) error {
	return effects.delete(ctx, "delete_namespace")
}

func (effects *fakeTopologyEffects) enter(ctx context.Context, operation string) error {
	*effects.events = append(*effects.events, operation)
	if effects.cancelAt == operation {
		effects.cancel()
		return ctx.Err()
	}
	if effects.failAt == operation {
		return errors.New("injected " + strings.ReplaceAll(operation, "_", " ") + " failure")
	}
	return nil
}

func (effects *fakeTopologyEffects) afterMutation(operation string) error {
	if effects.failAfterMutationAt != operation {
		return nil
	}
	return errors.New("injected post-mutation " + strings.ReplaceAll(operation, "_", " ") + " failure")
}

func (effects *fakeTopologyEffects) delete(ctx context.Context, operation string) error {
	effects.deleteEvents = append(effects.deleteEvents, operation)
	if ctx.Err() != nil {
		effects.cleanupSawCancellation = true
	}
	return effects.deleteFailures[operation]
}
