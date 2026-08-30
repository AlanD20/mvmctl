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
	testOwnerUID      = uint32(1000)
	testVMID          = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testNetworkID     = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testManagedBridge = linkName("mvbeec319e5586b")
	testHostVeth      = linkName("mvhe89e2abf9abd")
	testNamespaceVeth = linkName("mvne40ca9fc46c4")
	testLocalBridge   = linkName("mvlbc942db0d115")
	testTAP           = linkName("mvt0444ca744944")
	testNamespacePath = namespaceHandlePath("/run/mvmctl/authority/1000/netns/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
)

// Rationale: Root-side topology admission must reject caller-shaped identifiers before acquiring authority locks or
// constructing host resource names.
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

// Rationale: Link names are persisted root-authority identifiers. These expected names are independent SHA-256
// contract vectors calculated from the displayed NUL-separated preimages, not copied from deriveTopologyNames output.
func TestHashedLinkNameMatchesIndependentContractVectors(t *testing.T) {
	tests := map[string]struct {
		prefix   string
		preimage string
		want     linkName
	}{
		"managed_bridge": {
			prefix:   "mvb",
			preimage: "1000\x00" + testNetworkID + "\x00managed-bridge",
			want:     testManagedBridge,
		},
		"host_veth": {
			prefix:   "mvh",
			preimage: "1000\x00" + testVMID + "\x00host-veth",
			want:     testHostVeth,
		},
		"namespace_veth": {
			prefix:   "mvn",
			preimage: "1000\x00" + testVMID + "\x00namespace-veth",
			want:     testNamespaceVeth,
		},
		"local_bridge": {
			prefix:   "mvl",
			preimage: "1000\x00" + testVMID + "\x00local-bridge",
			want:     testLocalBridge,
		},
		"tap": {
			prefix:   "mvt",
			preimage: "1000\x00" + testVMID + "\x00tap",
			want:     testTAP,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := hashedLinkName(tc.prefix, tc.preimage)

			assert.Equal(t, tc.want, got)
		})
	}
}

// Rationale: These names are a shared security contract for the topology, authority, and firewall stages. A bridge is
// network-bound while namespace-local links are VM-bound, so neither may accidentally inherit the other's identity.
func TestDeriveTopologyNamesIsStableAndIdentityBound(t *testing.T) {
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)

	got := deriveTopologyNames(key)
	// CONTRACT: Namespace paths contain the immutable VM ID. Link literals are the independent vectors above.
	want := topologyNames{
		namespaceHandle: testNamespacePath,
		managedBridge:   testManagedBridge,
		hostVeth:        testHostVeth,
		namespaceVeth:   testNamespaceVeth,
		localBridge:     testLocalBridge,
		tap:             testTAP,
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

// Rationale: A prepared topology becomes privileged authority for later firewall and Jailer stages. The transaction
// must establish every exact identity while retaining both leases, and cleanup must revoke that authority in reverse.
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

	topology, err := preparation.Topology()
	require.NoError(t, err)
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
			Path: testNamespacePath.String(),
			Identity: NamespaceIdentity{
				Device: 41,
				Inode:  42,
			},
			OwnerUID: 0,
			OwnerGID: 0,
			Mode:     namespaceHandleMode,
		},
		ManagedBridge: LinkIdentity{
			Name:    testManagedBridge.String(),
			IfIndex: 10,
			Namespace: NamespaceIdentity{
				Device: 51,
				Inode:  52,
			},
		},
		LocalBridge: LinkIdentity{
			Name:    testLocalBridge.String(),
			IfIndex: 20,
			Namespace: NamespaceIdentity{
				Device: 41,
				Inode:  42,
			},
		},
		TAP: LinkIdentity{
			Name:    testTAP.String(),
			IfIndex: 21,
			Namespace: NamespaceIdentity{
				Device: 41,
				Inode:  42,
			},
		},
		VethPair: VethPairIdentity{
			Host: LinkIdentity{
				Name:    testHostVeth.String(),
				IfIndex: 22,
				Namespace: NamespaceIdentity{
					Device: 51,
					Inode:  52,
				},
			},
			Peer: LinkIdentity{
				Name:    testNamespaceVeth.String(),
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
	assert.Equal(t, topologyPreparationCommitted, preparation.state)

	require.NoError(t, preparation.Cleanup(context.Background()))
	require.NoError(t, preparation.Cleanup(context.Background()))
	assert.True(t, fixture.pin.released)
	assert.True(t, fixture.lock.released)
	wantDeletes := []string{"delete_veth_pair", "delete_tap", "delete_local_bridge", "delete_namespace"}
	if diff := cmp.Diff(wantDeletes, fixture.effects.deleteEvents); diff != "" {
		t.Errorf("prepared topology cleanup mismatch (-want +got):\n%s", diff)
	}
	assert.Equal(t, topologyPreparationCleaned, preparation.state)
	// Rationale: Cleanup revokes the authority represented by the topology. Returning the prior record here would let a
	// later firewall or Jailer stage act on identities whose locks and namespace pin are gone.
	assertTopologyUnavailable(t, preparation)
}

// Rationale: The network lock is later in the global lock order. A caller must not reach it with a forgeable or
// inactive VM-lock claim.
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

// Rationale: A locker may acquire the kernel/authority lock and then fail while inspecting its record. The returned
// lock remains owned and must be released without cancellation while the acquisition error remains primary.
func TestServicePrepareReleasesLockReturnedWithError(t *testing.T) {
	tests := map[string]struct {
		cancelDuringLock bool
		wantError        error
	}{
		"ordinary_error": {
			wantError: errors.New("injected lock verification failure"),
		},
		"caller_cancelled_during_lock": {
			cancelDuringLock: true,
			wantError:        context.Canceled,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var ctx context.Context = context.Background()
			config := fakeNetworkLockerConfig{lockError: tc.wantError}
			if tc.cancelDuringLock {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				config = fakeNetworkLockerConfig{cancel: cancel}
			}
			fixture := newTopologyFixtureWithLocker(t, config)

			preparation, err := fixture.service.prepare(ctx, fixture.launch, fixture.key.NetworkID())

			require.Error(t, err)
			assert.Nil(t, preparation)
			assert.ErrorIs(t, err, tc.wantError)
			assert.True(t, fixture.lock.released)
			assert.False(t, fixture.lock.releaseSawCancellation)
			assert.False(t, fixture.pin.released)
			if diff := cmp.Diff(
				[]string{"lock_network", "release_network_lock"},
				*fixture.events,
			); diff != "" {
				t.Errorf("lock error cleanup order mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: Lock admission may already classify a root-authority failure for the caller. Rolling back the retained
// lock must not replace that exact DomainError with a generic topology-creation error.
func TestServicePreparePreservesDomainErrorReturnedWithLock(t *testing.T) {
	primary := errs.New(
		errs.CodeNetworkError,
		"managed network authority record failed verification",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity(testNetworkID),
		errs.WithDetails(map[string]any{"record_mismatch": true}),
	)
	fixture := newTopologyFixtureWithLocker(t, fakeNetworkLockerConfig{lockError: primary})

	preparation, err := fixture.service.prepare(context.Background(), fixture.launch, fixture.key.NetworkID())

	require.Error(t, err)
	assert.Nil(t, preparation)
	assert.Same(t, primary, errs.AsDomainError(err))
	assert.Equal(t, errs.CodeNetworkError, primary.Code)
	assert.Equal(t, errs.ClassConflict, primary.Class)
	assert.Equal(t, testNetworkID, primary.Entity)
	assert.Equal(t, "managed network authority record failed verification", primary.Message)
	assert.Equal(t, true, primary.Details["record_mismatch"])
	assert.True(t, fixture.lock.released)
	assert.False(t, fixture.lock.releaseSawCancellation)
	if diff := cmp.Diff(
		[]string{"lock_network", "release_network_lock"},
		*fixture.events,
	); diff != "" {
		t.Errorf("DomainError lock cleanup order mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: A second failure while releasing an admitted lock must be attached to the original classified failure.
// Recovery needs cleanup_required without losing the lock verifier's code, class, entity, or existing details.
func TestServicePrepareRetainsPrimaryDomainErrorWhenLockCleanupFails(t *testing.T) {
	primary := errs.New(
		errs.CodeNetworkError,
		"managed network authority record failed verification",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity(testNetworkID),
		errs.WithDetails(map[string]any{"record_mismatch": true}),
	)
	releaseErr := errors.New("injected network lock release failure")
	fixture := newTopologyFixtureWithLocker(t, fakeNetworkLockerConfig{lockError: primary})
	fixture.lock.releaseError = releaseErr

	preparation, err := fixture.service.prepare(context.Background(), fixture.launch, fixture.key.NetworkID())

	require.Error(t, err)
	require.NotNil(t, preparation)
	assert.Same(t, primary, errs.AsDomainError(err))
	assert.ErrorIs(t, err, releaseErr)
	assert.Equal(t, errs.CodeNetworkError, primary.Code)
	assert.Equal(t, errs.ClassConflict, primary.Class)
	assert.Equal(t, testNetworkID, primary.Entity)
	assert.Contains(t, primary.Message, "failed to release network topology authority")
	assert.Equal(t, true, primary.Details["record_mismatch"])
	assert.Equal(t, true, primary.Details["cleanup_required"])
	assert.False(t, fixture.lock.released)
	assertTopologyUnavailable(t, preparation)

	fixture.lock.releaseError = nil
	require.NoError(t, preparation.Cleanup(context.Background()))
}

// Rationale: If releasing a lock returned with an acquisition error also fails, the exact lock must remain in a
// recovery preparation and the error must explicitly require cleanup without losing either failure.
func TestServicePrepareRetainsLockWhenLockErrorCleanupFails(t *testing.T) {
	lockErr := errors.New("injected lock verification failure")
	releaseErr := errors.New("injected network lock release failure")
	fixture := newTopologyFixtureWithLocker(t, fakeNetworkLockerConfig{lockError: lockErr})
	fixture.lock.releaseError = releaseErr

	preparation, err := fixture.service.prepare(context.Background(), fixture.launch, fixture.key.NetworkID())

	require.Error(t, err)
	require.NotNil(t, preparation)
	assert.ErrorIs(t, err, lockErr)
	assert.ErrorIs(t, err, releaseErr)
	de := errs.AsDomainError(err)
	require.NotNil(t, de)
	assert.Equal(t, true, de.Details["cleanup_required"])
	assert.False(t, fixture.lock.released)
	assertTopologyUnavailable(t, preparation)

	fixture.lock.releaseError = nil
	require.NoError(t, preparation.Cleanup(context.Background()))
	assert.True(t, fixture.lock.released)
	assertTopologyUnavailable(t, preparation)
}

// Rationale: Every successful prefix is privileged state. Failure at any following step must unwind that exact prefix
// in reverse order before releasing authority.
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
// retried without rediscovering or trusting mutable host names, but partial identity must remain unavailable to later
// authority stages.
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
	assertTopologyUnavailable(t, preparation)
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

// Rationale: Cleanup progress is durable within the retained preparation. A retry must resume at the failed step and
// never repeat already-completed destructive effects.
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
	assertTopologyUnavailable(t, preparation)

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
	assertTopologyUnavailable(t, preparation)
}

// Rationale: Only the exact committed state may authorize later privileged stages. Building, rollback, recovery, and
// deleted preparations can retain stale identity fields and must still return zero authority.
func TestTopologyPreparationTopologyRequiresExactCommittedState(t *testing.T) {
	fixture := newTopologyFixture(t)
	prepared, err := fixture.service.prepare(context.Background(), fixture.launch, fixture.key.NetworkID())
	require.NoError(t, err)

	tests := map[string]func(*topologyPreparation){
		"building": func(candidate *topologyPreparation) {
			candidate.state = topologyPreparationBuilding
		},
		"rollback_in_progress": func(candidate *topologyPreparation) {
			candidate.state = topologyPreparationCleaning
		},
		"deleted": func(candidate *topologyPreparation) {
			candidate.state = topologyPreparationCleaned
		},
		"committed_without_lock": func(candidate *topologyPreparation) {
			candidate.lock = nil
		},
		"committed_without_namespace_pin": func(candidate *topologyPreparation) {
			candidate.pin = nil
		},
		"committed_after_partial_veth_rollback": func(candidate *topologyPreparation) {
			candidate.vethMade = false
		},
		"committed_after_partial_tap_rollback": func(candidate *topologyPreparation) {
			candidate.tapMade = false
		},
		"committed_after_partial_bridge_rollback": func(candidate *topologyPreparation) {
			candidate.localBridgeMade = false
		},
		"committed_after_namespace_removal": func(candidate *topologyPreparation) {
			candidate.namespaceCreated = false
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := *prepared
			mutate(&candidate)

			assertTopologyUnavailable(t, &candidate)
		})
	}

	// CONTRACT: The untouched committed value still holds both leases and all four created-resource identities.
	topology, err := prepared.Topology()
	require.NoError(t, err)
	assert.Equal(t, testVMID, topology.Export().VMID)
	assert.Equal(t, testNamespacePath.String(), topology.Export().Namespace.Path)

	require.NoError(t, prepared.Cleanup(context.Background()))
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

// Rationale: Root adapters are trusted code but their returned kernel identities are still verified before a topology
// can become authority for firewall or Jailer stages.
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
	pin     *fakeNamespacePin
	effects *fakeTopologyEffects
	service *service
}

// newTopologyFixture constructs one valid owner-bound topology with a shared event log. Every fake identity is pinned
// independently from the service output so tests can detect name, namespace, and rollback regressions.
func newTopologyFixture(t *testing.T) *topologyFixture {
	t.Helper()
	return newTopologyFixtureWithLocker(t, fakeNetworkLockerConfig{})
}

// newTopologyFixtureWithLocker constructs a valid held-VM-lock transaction while injecting only network-lock behavior.
// Kernel namespace identities and ifindexes are literal fixture inputs, and expected link names come from the independent
// contract vectors above. The helper never reads a produced Topology to build its expected state, which prevents the
// setup from teaching assertions to accept an implementation-derived identity.
func newTopologyFixtureWithLocker(t *testing.T, config fakeNetworkLockerConfig) *topologyFixture {
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
	locker := &fakeNetworkLocker{
		lock:      lock,
		events:    &events,
		lockError: config.lockError,
		cancel:    config.cancel,
	}
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
		pin:     pin,
		effects: effects,
		service: service,
	}
}

type fakeNetworkLockerConfig struct {
	lockError error
	cancel    context.CancelFunc
}

func mustTopologyKey(t *testing.T, ownerUID uint32, vmID, networkID string) topologyKey {
	t.Helper()
	key, err := newTopologyKey(ownerUID, vmID, networkID)
	require.NoError(t, err)
	return key
}

// assertTopologyUnavailable verifies that non-committed lifecycle states return no usable authority identity and a
// structured network-domain failure.
func assertTopologyUnavailable(t *testing.T, preparation *topologyPreparation) {
	t.Helper()
	topology, err := preparation.Topology()
	require.Error(t, err)
	de := errs.AsDomainError(err)
	require.NotNil(t, de)
	assert.Equal(t, errs.CodeNetworkError, de.Code)
	if diff := cmp.Diff(
		Topology{},
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
		t.Errorf("unavailable topology must be zero (-want +got):\n%s", diff)
	}
}

type fakeNetworkLocker struct {
	lock      *fakeNetworkLock
	events    *[]string
	lockError error
	cancel    context.CancelFunc
}

func (locker *fakeNetworkLocker) LockNetwork(
	ctx context.Context,
	key networkKey,
) (NetworkLock, error) {
	*locker.events = append(*locker.events, "lock_network")
	if key != locker.lock.key {
		return nil, errors.New("unexpected network lock key")
	}
	if locker.cancel != nil {
		locker.cancel()
		return locker.lock, ctx.Err()
	}
	return locker.lock, locker.lockError
}

type fakeNetworkLock struct {
	key                    networkKey
	bridge                 linkIdentity
	released               bool
	releaseSawCancellation bool
	releaseError           error
	events                 *[]string
}

func (lock *fakeNetworkLock) Key() networkKey             { return lock.key }
func (lock *fakeNetworkLock) ManagedBridge() linkIdentity { return lock.bridge }
func (lock *fakeNetworkLock) Release(ctx context.Context) error {
	if lock.released {
		return nil
	}
	if ctx.Err() != nil {
		lock.releaseSawCancellation = true
	}
	*lock.events = append(*lock.events, "release_network_lock")
	if lock.releaseError != nil {
		return lock.releaseError
	}
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
