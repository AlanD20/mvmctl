package network

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestLinkEffectCreatesDownOwnedBridgeTAPAndReciprocalVeth(t *testing.T) {
	t.Parallel()

	fixture := newLinkEffectFixture(t)
	bridge, err := fixture.effect.CreateLocalBridgeDown(
		t.Context(), fixture.pin, localBridgeCreateSpec{name: fixture.names.localBridge},
	)
	require.NoError(t, err)
	tap, err := fixture.effect.CreateTAPDown(
		t.Context(), fixture.pin, tapCreateSpec{name: fixture.names.tap, ownerUID: OwnerUID(testOwnerUID)},
	)
	require.NoError(t, err)
	veth, err := fixture.effect.CreateVethPairDown(
		t.Context(), fixture.pin, vethCreateSpec{
			hostName: fixture.names.hostVeth, peerName: fixture.names.namespaceVeth,
		},
	)
	require.NoError(t, err)

	wantBridge := linkIdentity{name: fixture.names.localBridge, ifIndex: 20, namespace: fixture.vmNamespace}
	wantTAP := linkIdentity{name: fixture.names.tap, ifIndex: 21, namespace: fixture.vmNamespace}
	wantVeth := vethPairIdentity{
		host: linkIdentity{name: fixture.names.hostVeth, ifIndex: 22, namespace: fixture.hostNamespace},
		peer: linkIdentity{name: fixture.names.namespaceVeth, ifIndex: 23, namespace: fixture.vmNamespace},
	}
	if diff := cmp.Diff(wantBridge, *bridge, cmp.AllowUnexported(linkIdentity{}, namespaceIdentity{})); diff != "" {
		t.Errorf("created bridge mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantTAP, *tap, cmp.AllowUnexported(linkIdentity{}, namespaceIdentity{})); diff != "" {
		t.Errorf("created TAP mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(
		wantVeth,
		*veth,
		cmp.AllowUnexported(vethPairIdentity{}, linkIdentity{}, namespaceIdentity{}),
	); diff != "" {
		t.Errorf("created veth mismatch (-want +got):\n%s", diff)
	}

	bridgeObservation := fixture.route.mustLink(t, fixture.vmFD, fixture.names.localBridge)
	assert.Equal(t, managedBridgeAlias(fixture.names.localBridge), bridgeObservation.alias)
	assert.Equal(t, linkKindBridge, bridgeObservation.kind)
	assert.Zero(t, bridgeObservation.flags&unix.IFF_UP)
	assert.Zero(t, bridgeObservation.master)
	tapObservation := fixture.route.mustLink(t, fixture.vmFD, fixture.names.tap)
	assert.Equal(t, uint32(testOwnerUID), tapObservation.tunOwner)
	assert.Equal(t, uint8(unix.IFF_TAP), tapObservation.tunType)
	assert.Zero(t, tapObservation.tunPI)
	assert.Equal(t, uint8(1), tapObservation.tunPersist)
	hostObservation := fixture.route.mustLink(t, fixture.hostFD, fixture.names.hostVeth)
	peerObservation := fixture.route.mustLink(t, fixture.vmFD, fixture.names.namespaceVeth)
	assert.Equal(t, peerObservation.ifIndex, hostObservation.peer)
	assert.Equal(t, hostObservation.ifIndex, peerObservation.peer)
	assert.Equal(t, 4, fixture.closeCount())
}

// Rationale: a transport error after mutation remains rollback-visible and is reconciled under a non-cancelled read.
func TestLinkEffectCreationUncertaintyReturnsIdentityAndInspectsWithoutCancellation(t *testing.T) {
	t.Parallel()

	fixture := newLinkEffectFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	fixture.route.createError = context.Canceled
	fixture.route.cancelCreate = cancel
	identity, err := fixture.effect.CreateLocalBridgeDown(
		ctx, fixture.pin, localBridgeCreateSpec{name: fixture.names.localBridge},
	)
	require.Error(t, err)
	require.NotNil(t, identity)
	assert.Equal(t, 20, identity.ifIndex)
	assert.Equal(t, 2, fixture.route.inspectCount(fixture.vmFD, fixture.names.localBridge))
	assert.False(t, fixture.route.lastInspectCancelled(fixture.vmFD, fixture.names.localBridge))

	noMutation := newLinkEffectFixture(t)
	noMutation.route.applyCreate = false
	noMutation.route.createError = errors.New("request rejected")
	identity, err = noMutation.effect.CreateLocalBridgeDown(
		t.Context(), noMutation.pin, localBridgeCreateSpec{name: noMutation.names.localBridge},
	)
	assert.Nil(t, identity)
	assert.Error(t, err)

	partialTAP := newLinkEffectFixture(t)
	tapCtx, tapCancel := context.WithCancel(t.Context())
	partialTAP.tap.createError = context.Canceled
	partialTAP.tap.cancelCreate = tapCancel
	tap, err := partialTAP.effect.CreateTAPDown(
		tapCtx,
		partialTAP.pin,
		tapCreateSpec{name: partialTAP.names.tap, ownerUID: OwnerUID(testOwnerUID)},
	)
	require.Error(t, err)
	require.NotNil(t, tap)
	assert.Equal(t, 21, tap.ifIndex)
	assert.Equal(t, managedTAPAlias(partialTAP.names.tap, OwnerUID(testOwnerUID)),
		partialTAP.route.mustLink(t, partialTAP.vmFD, partialTAP.names.tap).alias)
	assert.False(t, partialTAP.route.lastInspectCancelled(partialTAP.vmFD, partialTAP.names.tap))
}

func TestLinkEffectTAPAliasFailureLeavesNoPersistentIdentity(t *testing.T) {
	t.Parallel()

	fixture := newLinkEffectFixture(t)
	fixture.tap.createError = errors.New("set managed TAP alias: acknowledgement lost")
	fixture.tap.applyCreate = false
	identity, err := fixture.effect.CreateTAPDown(
		t.Context(), fixture.pin, tapCreateSpec{name: fixture.names.tap, ownerUID: OwnerUID(testOwnerUID)},
	)
	require.Error(t, err)
	assert.Nil(t, identity)
	assert.Nil(t, fixture.route.link(fixture.vmFD, fixture.names.tap))
}

func TestLinkEffectPreservesFatalTAPWorkerClassificationWithRollbackIdentity(t *testing.T) {
	t.Parallel()

	fixture := newLinkEffectFixture(t)
	fixture.tap.createError = markTAPThreadFatal(
		fixture.names.tap, nil, errors.New("host restoration unproven"),
	)
	identity, err := fixture.effect.CreateTAPDown(
		t.Context(), fixture.pin, tapCreateSpec{name: fixture.names.tap, ownerUID: OwnerUID(testOwnerUID)},
	)
	require.Error(t, err)
	require.NotNil(t, identity)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["network_worker_retired"])
	assert.Equal(t, true, domainErr.Details["network_thread_contaminated"])
}

func TestLinkEffectPartialVethCreationReturnsCleanupCapableIdentity(t *testing.T) {
	t.Parallel()

	fixture := newLinkEffectFixture(t)
	fixture.route.omitPeerAfterCreate = true
	identity, err := fixture.effect.CreateVethPairDown(
		t.Context(), fixture.pin, vethCreateSpec{
			hostName: fixture.names.hostVeth, peerName: fixture.names.namespaceVeth,
		},
	)
	require.Error(t, err)
	require.NotNil(t, identity)
	assert.Equal(t, 22, identity.host.ifIndex)
	assert.Equal(t, -1, identity.peer.ifIndex)

	require.NoError(t, fixture.effect.DeleteVethPair(t.Context(), fixture.pin, *identity))
	assert.Nil(t, fixture.route.link(fixture.hostFD, fixture.names.hostVeth))
	assert.Nil(t, fixture.route.link(fixture.vmFD, fixture.names.namespaceVeth))
}

func TestLinkEffectTAPOwnerMustMatchRetainedNamespaceOwner(t *testing.T) {
	t.Parallel()

	fixture := newLinkEffectFixture(t)
	identity, err := fixture.effect.CreateTAPDown(
		t.Context(), fixture.pin,
		tapCreateSpec{name: fixture.names.tap, ownerUID: OwnerUID(testOwnerUID + 1)},
	)
	assert.Nil(t, identity)
	assert.Error(t, err)
	assert.Nil(t, fixture.route.link(fixture.vmFD, fixture.names.tap))
}

func TestLinkEffectRejectsWrongCreatedIdentityAndRetainsRollbackResult(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*routeLinkObservation){
		"bridge kind":   func(link *routeLinkObservation) { link.kind = "dummy" },
		"bridge alias":  func(link *routeLinkObservation) { link.alias = "foreign" },
		"bridge up":     func(link *routeLinkObservation) { link.flags |= unix.IFF_UP },
		"bridge master": func(link *routeLinkObservation) { link.master = 99 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newLinkEffectFixture(t)
			fixture.route.afterCreate = mutate
			identity, err := fixture.effect.CreateLocalBridgeDown(
				t.Context(), fixture.pin, localBridgeCreateSpec{name: fixture.names.localBridge},
			)
			require.Error(t, err)
			require.NotNil(t, identity)
			assert.Equal(t, 20, identity.ifIndex)
		})
	}

	t.Run("TAP owner", func(t *testing.T) {
		fixture := newLinkEffectFixture(t)
		fixture.tap.afterCreate = func(link *routeLinkObservation) { link.tunOwner = testOwnerUID + 1 }
		identity, err := fixture.effect.CreateTAPDown(
			t.Context(), fixture.pin, tapCreateSpec{name: fixture.names.tap, ownerUID: OwnerUID(testOwnerUID)},
		)
		require.Error(t, err)
		require.NotNil(t, identity)
		assert.Equal(t, 21, identity.ifIndex)
	})

	t.Run("TAP group authority", func(t *testing.T) {
		fixture := newLinkEffectFixture(t)
		fixture.tap.afterCreate = func(link *routeLinkObservation) {
			link.tunGroup = testOwnerUID
			link.tunGroupPresent = true
		}
		identity, err := fixture.effect.CreateTAPDown(
			t.Context(), fixture.pin, tapCreateSpec{name: fixture.names.tap, ownerUID: OwnerUID(testOwnerUID)},
		)
		require.Error(t, err)
		require.NotNil(t, identity)
		assert.Equal(t, 21, identity.ifIndex)
	})

	t.Run("veth peer", func(t *testing.T) {
		fixture := newLinkEffectFixture(t)
		fixture.route.afterVethCreate = func(_ *routeLinkObservation, peer *routeLinkObservation) {
			peer.peer = 999
		}
		identity, err := fixture.effect.CreateVethPairDown(
			t.Context(), fixture.pin, vethCreateSpec{
				hostName: fixture.names.hostVeth, peerName: fixture.names.namespaceVeth,
			},
		)
		require.Error(t, err)
		require.NotNil(t, identity)
		assert.Equal(t, 22, identity.host.ifIndex)
		assert.Equal(t, 23, identity.peer.ifIndex)
	})
}

func TestLinkEffectAttachReadmitsEndpointsAndSetsOnlyMaster(t *testing.T) {
	t.Parallel()

	fixture := newLinkEffectFixture(t)
	bridge, tap, veth := fixture.seedAttachedTopology(t, false)
	require.NoError(t, fixture.effect.AttachNamespaceVethToLocalBridge(
		t.Context(), fixture.pin, veth, bridge,
	))
	require.NoError(t, fixture.effect.AttachTAPToLocalBridge(t.Context(), fixture.pin, tap, bridge))
	managedBridge := fixture.route.mustIdentity(
		fixture.hostFD, fixture.names.managedBridge, fixture.hostNamespace,
	)
	require.NoError(t, fixture.effect.AttachHostVethToManagedBridge(
		t.Context(), veth, managedBridge,
	))

	want := []linkMasterMutation{
		{namespaceFD: fixture.vmFD, ifIndex: veth.peer.ifIndex, master: bridge.ifIndex},
		{namespaceFD: fixture.vmFD, ifIndex: tap.ifIndex, master: bridge.ifIndex},
		{namespaceFD: fixture.hostFD, ifIndex: veth.host.ifIndex, master: managedBridge.ifIndex},
	}
	if diff := cmp.Diff(
		want,
		fixture.route.masterMutationsSnapshot(),
		cmp.AllowUnexported(linkMasterMutation{}),
	); diff != "" {
		t.Errorf("master-only attachment mismatch (-want +got):\n%s", diff)
	}
	assert.Zero(t, fixture.route.mustLink(t, fixture.vmFD, bridge.name).master)
	assert.Zero(t, fixture.route.mustLink(t, fixture.hostFD, managedBridge.name).master)
}

func TestLinkEffectAttachRejectsWrongNamespaceKindAliasOwnerAndMaster(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*linkEffectFixture, linkIdentity, linkIdentity, vethPairIdentity) error{
		"namespace": func(fixture *linkEffectFixture, bridge, _ linkIdentity, veth vethPairIdentity) error {
			veth.peer.namespace = fixture.hostNamespace
			return fixture.effect.AttachNamespaceVethToLocalBridge(t.Context(), fixture.pin, veth, bridge)
		},
		"kind": func(fixture *linkEffectFixture, bridge, _ linkIdentity, veth vethPairIdentity) error {
			link := fixture.route.mustLink(t, fixture.vmFD, veth.peer.name)
			link.kind = "dummy"
			fixture.route.put(fixture.vmFD, link)
			return fixture.effect.AttachNamespaceVethToLocalBridge(t.Context(), fixture.pin, veth, bridge)
		},
		"alias": func(fixture *linkEffectFixture, bridge, _ linkIdentity, veth vethPairIdentity) error {
			link := fixture.route.mustLink(t, fixture.vmFD, veth.peer.name)
			link.alias = "foreign"
			fixture.route.put(fixture.vmFD, link)
			return fixture.effect.AttachNamespaceVethToLocalBridge(t.Context(), fixture.pin, veth, bridge)
		},
		"owner": func(fixture *linkEffectFixture, bridge, tap linkIdentity, _ vethPairIdentity) error {
			link := fixture.route.mustLink(t, fixture.vmFD, tap.name)
			link.tunOwner++
			fixture.route.put(fixture.vmFD, link)
			return fixture.effect.AttachTAPToLocalBridge(t.Context(), fixture.pin, tap, bridge)
		},
		"master": func(fixture *linkEffectFixture, bridge, _ linkIdentity, veth vethPairIdentity) error {
			link := fixture.route.mustLink(t, fixture.vmFD, veth.peer.name)
			link.master = 999
			fixture.route.put(fixture.vmFD, link)
			return fixture.effect.AttachNamespaceVethToLocalBridge(t.Context(), fixture.pin, veth, bridge)
		},
	}
	for name, operation := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newLinkEffectFixture(t)
			bridge, tap, veth := fixture.seedAttachedTopology(t, false)
			err := operation(fixture, bridge, tap, veth)
			assert.Error(t, err)
			assert.Empty(t, fixture.route.masterMutationsSnapshot())
		})
	}
}

func TestLinkEffectDeletionIsExactIdempotentAndFreshlyProvesAbsence(t *testing.T) {
	t.Parallel()

	fixture := newLinkEffectFixture(t)
	bridge, tap, veth := fixture.seedAttachedTopology(t, true)
	require.NoError(t, fixture.effect.DeleteVethPair(t.Context(), fixture.pin, veth))
	require.NoError(t, fixture.effect.DeleteTAP(t.Context(), fixture.pin, tap))
	require.NoError(t, fixture.effect.DeleteLocalBridge(t.Context(), fixture.pin, bridge))
	assert.Nil(t, fixture.route.link(fixture.hostFD, veth.host.name))
	assert.Nil(t, fixture.route.link(fixture.vmFD, veth.peer.name))
	assert.Nil(t, fixture.route.link(fixture.vmFD, tap.name))
	assert.Nil(t, fixture.route.link(fixture.vmFD, bridge.name))

	deleteCount := fixture.route.deleteCount()
	require.NoError(t, fixture.effect.DeleteVethPair(t.Context(), fixture.pin, veth))
	require.NoError(t, fixture.effect.DeleteTAP(t.Context(), fixture.pin, tap))
	require.NoError(t, fixture.effect.DeleteLocalBridge(t.Context(), fixture.pin, bridge))
	assert.Equal(t, deleteCount, fixture.route.deleteCount())
}

func TestLinkEffectDeletionRejectsReuseDriftForeignChildrenAndWrongPeer(t *testing.T) {
	t.Parallel()

	t.Run("ifindex reuse", func(t *testing.T) {
		fixture := newLinkEffectFixture(t)
		bridge, _, _ := fixture.seedAttachedTopology(t, false)
		observation := fixture.route.mustLink(t, fixture.vmFD, bridge.name)
		observation.ifIndex++
		fixture.route.put(fixture.vmFD, observation)
		assert.Error(t, fixture.effect.DeleteLocalBridge(t.Context(), fixture.pin, bridge))
		assert.Zero(t, fixture.route.deleteCount())
	})

	t.Run("alias drift", func(t *testing.T) {
		fixture := newLinkEffectFixture(t)
		_, tap, _ := fixture.seedAttachedTopology(t, false)
		observation := fixture.route.mustLink(t, fixture.vmFD, tap.name)
		observation.alias = "foreign"
		fixture.route.put(fixture.vmFD, observation)
		assert.Error(t, fixture.effect.DeleteTAP(t.Context(), fixture.pin, tap))
		assert.Zero(t, fixture.route.deleteCount())
	})

	t.Run("foreign bridge child", func(t *testing.T) {
		fixture := newLinkEffectFixture(t)
		bridge, _, _ := fixture.seedAttachedTopology(t, false)
		fixture.route.put(fixture.vmFD, routeLinkObservation{
			name: "foreign0", alias: "foreign", kind: "dummy", ifIndex: 99, master: bridge.ifIndex,
		})
		assert.Error(t, fixture.effect.DeleteLocalBridge(t.Context(), fixture.pin, bridge))
		assert.Zero(t, fixture.route.deleteCount())
	})

	t.Run("wrong reciprocal peer", func(t *testing.T) {
		fixture := newLinkEffectFixture(t)
		_, _, veth := fixture.seedAttachedTopology(t, false)
		peer := fixture.route.mustLink(t, fixture.vmFD, veth.peer.name)
		peer.peer = 999
		fixture.route.put(fixture.vmFD, peer)
		assert.Error(t, fixture.effect.DeleteVethPair(t.Context(), fixture.pin, veth))
		assert.Zero(t, fixture.route.deleteCount())
	})
}

func TestLinkEffectCloseFailureIsCachedAndCreationResultRemainsVisible(t *testing.T) {
	t.Parallel()

	fixture := newLinkEffectFixture(t)
	fixture.closeError = errors.New("close failed")
	identity, err := fixture.effect.CreateLocalBridgeDown(
		t.Context(), fixture.pin, localBridgeCreateSpec{name: fixture.names.localBridge},
	)
	require.Error(t, err)
	require.NotNil(t, identity)
	assert.Equal(t, 20, identity.ifIndex)
	assert.Equal(t, 1, fixture.closeCount())
}

type linkEffectFixture struct {
	effect        *linkEffect
	route         *fakeRouteNetlink
	tap           *fakeTAPCreator
	retainer      *fakeLinkNamespaceRetainer
	pin           NamespacePin
	names         topologyNames
	vmNamespace   namespaceIdentity
	hostNamespace namespaceIdentity
	vmFD          int
	hostFD        int

	mu         sync.Mutex
	closes     int
	closeError error
	closeCtx   []context.Context
}

func newLinkEffectFixture(t *testing.T) *linkEffectFixture {
	t.Helper()
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	fixture := &linkEffectFixture{
		route:         newFakeRouteNetlink(),
		pin:           &fakeNamespacePin{},
		names:         deriveTopologyNames(key),
		vmNamespace:   namespaceIdentity{device: 5, inode: 400},
		hostNamespace: namespaceIdentity{device: 5, inode: 300},
		vmFD:          41,
		hostFD:        40,
	}
	fixture.retainer = &fakeLinkNamespaceRetainer{fixture: fixture}
	fixture.tap = &fakeTAPCreator{fixture: fixture, applyCreate: true}
	fixture.effect = &linkEffect{
		namespaces: fixture.retainer,
		route:      fixture.route,
		tap:        fixture.tap,
		host: func(context.Context) (*linkNamespaceLease, error) {
			return fixture.lease(fixture.hostFD, fixture.hostNamespace), nil
		},
	}
	return fixture
}

func (fixture *linkEffectFixture) lease(fd int, identity namespaceIdentity) *linkNamespaceLease {
	owner := OwnerUID(0)
	if fd == fixture.vmFD {
		owner = OwnerUID(testOwnerUID)
	}
	return &linkNamespaceLease{state: &linkNamespaceLeaseState{
		deps: namespaceEffectDeps{close: func(ctx context.Context, gotFD int) error {
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if gotFD != fd {
				return fmt.Errorf("unexpected close fd %d", gotFD)
			}
			fixture.closes++
			fixture.closeCtx = append(fixture.closeCtx, ctx)
			return fixture.closeError
		}},
		fd: fd, identity: identity, ownerUID: owner,
	}}
}

func (fixture *linkEffectFixture) closeCount() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.closes
}

func (fixture *linkEffectFixture) seedAttachedTopology(
	t *testing.T,
	attached bool,
) (linkIdentity, linkIdentity, vethPairIdentity) {
	t.Helper()
	bridge := linkIdentity{name: fixture.names.localBridge, ifIndex: 20, namespace: fixture.vmNamespace}
	tap := linkIdentity{name: fixture.names.tap, ifIndex: 21, namespace: fixture.vmNamespace}
	veth := vethPairIdentity{
		host: linkIdentity{name: fixture.names.hostVeth, ifIndex: 22, namespace: fixture.hostNamespace},
		peer: linkIdentity{name: fixture.names.namespaceVeth, ifIndex: 23, namespace: fixture.vmNamespace},
	}
	managedBridge := linkIdentity{name: fixture.names.managedBridge, ifIndex: 10, namespace: fixture.hostNamespace}
	fixture.route.put(fixture.vmFD, routeLinkObservation{
		name: bridge.name, alias: managedBridgeAlias(bridge.name), kind: linkKindBridge, ifIndex: bridge.ifIndex,
	})
	fixture.route.put(fixture.vmFD, routeLinkObservation{
		name:       tap.name,
		alias:      managedTAPAlias(tap.name, OwnerUID(testOwnerUID)),
		kind:       linkKindTAP,
		ifIndex:    tap.ifIndex,
		tunOwner:   testOwnerUID,
		tunType:    unix.IFF_TAP,
		tunPersist: 1,
	})
	fixture.route.put(fixture.hostFD, routeLinkObservation{
		name:  veth.host.name,
		alias: managedVethAlias("veth-host", veth.host.name, veth.peer.name),
		kind:  linkKindVeth, ifIndex: veth.host.ifIndex, peer: veth.peer.ifIndex,
	})
	fixture.route.put(fixture.vmFD, routeLinkObservation{
		name:  veth.peer.name,
		alias: managedVethAlias("veth-peer", veth.peer.name, veth.host.name),
		kind:  linkKindVeth, ifIndex: veth.peer.ifIndex, peer: veth.host.ifIndex,
	})
	fixture.route.put(fixture.hostFD, routeLinkObservation{
		name: managedBridge.name, alias: managedBridgeAlias(managedBridge.name), kind: linkKindBridge,
		ifIndex: managedBridge.ifIndex,
	})
	if attached {
		host := fixture.route.mustLink(t, fixture.hostFD, veth.host.name)
		host.master = managedBridge.ifIndex
		fixture.route.put(fixture.hostFD, host)
		peer := fixture.route.mustLink(t, fixture.vmFD, veth.peer.name)
		peer.master = bridge.ifIndex
		fixture.route.put(fixture.vmFD, peer)
		tapObservation := fixture.route.mustLink(t, fixture.vmFD, tap.name)
		tapObservation.master = bridge.ifIndex
		fixture.route.put(fixture.vmFD, tapObservation)
	}
	return bridge, tap, veth
}

type fakeLinkNamespaceRetainer struct {
	fixture *linkEffectFixture
}

func (retainer *fakeLinkNamespaceRetainer) retainLinkNamespace(
	context.Context,
	NamespacePin,
) (*linkNamespaceLease, error) {
	return retainer.fixture.lease(retainer.fixture.vmFD, retainer.fixture.vmNamespace), nil
}

type linkMasterMutation struct {
	namespaceFD int
	ifIndex     int
	master      int
}

type addressMutation struct {
	namespaceFD int
	ifIndex     int
	prefix      string
}

type fakeRouteNetlink struct {
	mu sync.Mutex

	links               map[int]map[linkName]routeLinkObservation
	addresses           map[int]map[int][]routeAddressObservation
	routes              map[int]map[int][]routeIPv4Observation
	inspects            map[string]int
	inspectCancellation map[string]bool
	masterMutations     []linkMasterMutation
	addressMutations    []addressMutation
	stateMutations      []linkStateMutation
	aliasError          error
	applyAlias          bool
	cancelAlias         context.CancelFunc
	addressError        error
	applyAddress        bool
	stateError          error
	applyState          bool
	deletes             int
	createError         error
	applyCreate         bool
	cancelCreate        context.CancelFunc
	afterCreate         func(*routeLinkObservation)
	afterVethCreate     func(*routeLinkObservation, *routeLinkObservation)
	afterAddress        func(int, int, addressExpectation)
	omitHostAfterCreate bool
	omitPeerAfterCreate bool
}

func newFakeRouteNetlink() *fakeRouteNetlink {
	return &fakeRouteNetlink{
		links:               make(map[int]map[linkName]routeLinkObservation),
		addresses:           make(map[int]map[int][]routeAddressObservation),
		routes:              make(map[int]map[int][]routeIPv4Observation),
		inspects:            make(map[string]int),
		inspectCancellation: make(map[string]bool),
		applyCreate:         true,
		applyAlias:          true,
		applyAddress:        true,
		applyState:          true,
	}
}

func (route *fakeRouteNetlink) inspectLink(
	ctx context.Context,
	fd int,
	name linkName,
) (*routeLinkObservation, error) {
	route.mu.Lock()
	defer route.mu.Unlock()
	key := fmt.Sprintf("%d/%s", fd, name)
	route.inspects[key]++
	route.inspectCancellation[key] = ctx.Err() != nil
	link, ok := route.links[fd][name]
	if !ok {
		return nil, nil
	}
	copyOfLink := link
	return &copyOfLink, nil
}

func (route *fakeRouteNetlink) listLinks(_ context.Context, fd int) ([]routeLinkObservation, error) {
	route.mu.Lock()
	defer route.mu.Unlock()
	links := make([]routeLinkObservation, 0, len(route.links[fd]))
	for _, link := range route.links[fd] {
		links = append(links, link)
	}
	sort.Slice(links, func(i, j int) bool { return links[i].ifIndex < links[j].ifIndex })
	return links, nil
}

func (route *fakeRouteNetlink) listIPv4Addresses(
	_ context.Context,
	fd int,
	ifIndex int,
) ([]routeAddressObservation, error) {
	route.mu.Lock()
	defer route.mu.Unlock()
	return append([]routeAddressObservation(nil), route.addresses[fd][ifIndex]...), nil
}

func (route *fakeRouteNetlink) listIPv4Routes(
	_ context.Context,
	fd int,
	ifIndex int,
) ([]routeIPv4Observation, error) {
	route.mu.Lock()
	defer route.mu.Unlock()
	return append([]routeIPv4Observation(nil), route.routes[fd][ifIndex]...), nil
}

func (route *fakeRouteNetlink) createBridgeDown(
	_ context.Context,
	fd int,
	name linkName,
	alias string,
) error {
	link := routeLinkObservation{name: name, alias: alias, kind: linkKindBridge, ifIndex: 20}
	return route.create(fd, &link)
}

func (route *fakeRouteNetlink) createVethPairDown(
	_ context.Context,
	hostFD int,
	peerFD int,
	hostName linkName,
	hostAlias string,
	peerName linkName,
	peerAlias string,
) error {
	route.mu.Lock()
	defer route.mu.Unlock()
	host := routeLinkObservation{
		name: hostName, alias: hostAlias, kind: linkKindVeth, ifIndex: 22, peer: 23,
	}
	peer := routeLinkObservation{
		name: peerName, alias: peerAlias, kind: linkKindVeth, ifIndex: 23, peer: 22,
	}
	if route.afterVethCreate != nil {
		route.afterVethCreate(&host, &peer)
	}
	if route.applyCreate {
		if !route.omitHostAfterCreate {
			route.putLocked(hostFD, host)
		}
		if !route.omitPeerAfterCreate {
			route.putLocked(peerFD, peer)
		}
	}
	if route.cancelCreate != nil {
		route.cancelCreate()
	}
	return route.createError
}

func (route *fakeRouteNetlink) create(fd int, link *routeLinkObservation) error {
	route.mu.Lock()
	defer route.mu.Unlock()
	if route.afterCreate != nil {
		route.afterCreate(link)
	}
	if route.applyCreate {
		route.putLocked(fd, *link)
	}
	if route.cancelCreate != nil {
		route.cancelCreate()
	}
	return route.createError
}

func (route *fakeRouteNetlink) setLinkMaster(_ context.Context, fd, ifIndex, master int) error {
	route.mu.Lock()
	defer route.mu.Unlock()
	route.masterMutations = append(route.masterMutations, linkMasterMutation{
		namespaceFD: fd, ifIndex: ifIndex, master: master,
	})
	for name, link := range route.links[fd] {
		if link.ifIndex == ifIndex {
			link.master = master
			route.links[fd][name] = link
			return nil
		}
	}
	return errors.New("link not found")
}

func (route *fakeRouteNetlink) setLinkAlias(_ context.Context, fd, ifIndex int, alias string) error {
	route.mu.Lock()
	defer route.mu.Unlock()
	for name, link := range route.links[fd] {
		if link.ifIndex == ifIndex {
			if route.applyAlias {
				link.alias = alias
				route.links[fd][name] = link
			}
			if route.cancelAlias != nil {
				route.cancelAlias()
			}
			return route.aliasError
		}
	}
	return errors.New("link not found")
}

func (route *fakeRouteNetlink) addIPv4Address(
	_ context.Context,
	fd int,
	ifIndex int,
	expectation addressExpectation,
) error {
	route.mu.Lock()
	defer route.mu.Unlock()
	route.addressMutations = append(route.addressMutations, addressMutation{
		namespaceFD: fd, ifIndex: ifIndex, prefix: expectation.prefix.String(),
	})
	if !route.applyAddress {
		return route.addressError
	}
	observation, err := addressExpectationToObservation(expectation, ifIndex)
	if err != nil {
		return err
	}
	if route.addresses[fd] == nil {
		route.addresses[fd] = make(map[int][]routeAddressObservation)
	}
	route.addresses[fd][ifIndex] = append(route.addresses[fd][ifIndex], observation)
	if route.afterAddress != nil {
		route.afterAddress(fd, ifIndex, expectation)
	}
	return route.addressError
}

type linkStateMutation struct {
	namespaceFD int
	ifIndex     int
	up          bool
}

func (route *fakeRouteNetlink) setLinkUp(_ context.Context, fd, ifIndex int, up bool) error {
	route.mu.Lock()
	defer route.mu.Unlock()
	route.stateMutations = append(route.stateMutations, linkStateMutation{
		namespaceFD: fd, ifIndex: ifIndex, up: up,
	})
	if route.applyState {
		for name, link := range route.links[fd] {
			if link.ifIndex != ifIndex {
				continue
			}
			if up {
				link.flags |= unix.IFF_UP
			} else {
				link.flags &^= unix.IFF_UP
			}
			route.links[fd][name] = link
			break
		}
	}
	return route.stateError
}

type fakeTAPCreator struct {
	fixture      *linkEffectFixture
	createError  error
	applyCreate  bool
	cancelCreate context.CancelFunc
	afterCreate  func(*routeLinkObservation)
}

func (creator *fakeTAPCreator) createTAPDown(
	_ context.Context,
	fd int,
	identity namespaceIdentity,
	name linkName,
	alias string,
	owner OwnerUID,
) error {
	if identity != creator.fixture.vmNamespace {
		return errors.New("unexpected TAP namespace identity")
	}
	link := routeLinkObservation{
		name: name, alias: alias, kind: linkKindTAP, ifIndex: 21,
		tunOwner: uint32(owner), tunType: unix.IFF_TAP, tunPersist: 1,
	}
	if creator.afterCreate != nil {
		creator.afterCreate(&link)
	}
	if creator.applyCreate {
		creator.fixture.route.put(fd, link)
	}
	if creator.cancelCreate != nil {
		creator.cancelCreate()
	}
	return creator.createError
}

func (route *fakeRouteNetlink) deleteLink(_ context.Context, fd, ifIndex int) error {
	route.mu.Lock()
	defer route.mu.Unlock()
	route.deletes++
	for name, link := range route.links[fd] {
		if link.ifIndex != ifIndex {
			continue
		}
		delete(route.links[fd], name)
		if link.kind == linkKindVeth {
			for otherFD, namespaceLinks := range route.links {
				for otherName, candidate := range namespaceLinks {
					if otherFD != fd && candidate.ifIndex == link.peer && candidate.peer == link.ifIndex {
						delete(route.links[otherFD], otherName)
					}
				}
			}
		}
		return nil
	}
	return unix.ENODEV
}

func (route *fakeRouteNetlink) put(fd int, link routeLinkObservation) {
	route.mu.Lock()
	defer route.mu.Unlock()
	route.putLocked(fd, link)
}

func (route *fakeRouteNetlink) putLocked(fd int, link routeLinkObservation) {
	if route.links[fd] == nil {
		route.links[fd] = make(map[linkName]routeLinkObservation)
	}
	route.links[fd][link.name] = link
}

func (route *fakeRouteNetlink) link(fd int, name linkName) *routeLinkObservation {
	route.mu.Lock()
	defer route.mu.Unlock()
	link, ok := route.links[fd][name]
	if !ok {
		return nil
	}
	copyOfLink := link
	return &copyOfLink
}

func (route *fakeRouteNetlink) mustLink(t *testing.T, fd int, name linkName) routeLinkObservation {
	t.Helper()
	link := route.link(fd, name)
	require.NotNil(t, link)
	return *link
}

func (route *fakeRouteNetlink) mustIdentity(
	fd int,
	name linkName,
	namespace namespaceIdentity,
) linkIdentity {
	link := route.link(fd, name)
	if link == nil {
		return linkIdentity{}
	}
	return linkIdentity{name: name, ifIndex: link.ifIndex, namespace: namespace}
}

func (route *fakeRouteNetlink) inspectCount(fd int, name linkName) int {
	route.mu.Lock()
	defer route.mu.Unlock()
	return route.inspects[fmt.Sprintf("%d/%s", fd, name)]
}

func (route *fakeRouteNetlink) lastInspectCancelled(fd int, name linkName) bool {
	route.mu.Lock()
	defer route.mu.Unlock()
	return route.inspectCancellation[fmt.Sprintf("%d/%s", fd, name)]
}

func (route *fakeRouteNetlink) masterMutationsSnapshot() []linkMasterMutation {
	route.mu.Lock()
	defer route.mu.Unlock()
	return append([]linkMasterMutation(nil), route.masterMutations...)
}

func (route *fakeRouteNetlink) addressMutationsSnapshot() []addressMutation {
	route.mu.Lock()
	defer route.mu.Unlock()
	return append([]addressMutation(nil), route.addressMutations...)
}

func (route *fakeRouteNetlink) stateMutationsSnapshot() []linkStateMutation {
	route.mu.Lock()
	defer route.mu.Unlock()
	return append([]linkStateMutation(nil), route.stateMutations...)
}

func (route *fakeRouteNetlink) stateMutationCount() int {
	route.mu.Lock()
	defer route.mu.Unlock()
	return len(route.stateMutations)
}

func (route *fakeRouteNetlink) addressesSnapshot(fd, ifIndex int) []routeAddressObservation {
	route.mu.Lock()
	defer route.mu.Unlock()
	return append([]routeAddressObservation(nil), route.addresses[fd][ifIndex]...)
}

func (route *fakeRouteNetlink) routesSnapshot(fd, ifIndex int) []routeIPv4Observation {
	route.mu.Lock()
	defer route.mu.Unlock()
	return append([]routeIPv4Observation(nil), route.routes[fd][ifIndex]...)
}

func (route *fakeRouteNetlink) linkByIndex(fd, ifIndex int) *routeLinkObservation {
	route.mu.Lock()
	defer route.mu.Unlock()
	for _, link := range route.links[fd] {
		if link.ifIndex == ifIndex {
			copyOfLink := link
			return &copyOfLink
		}
	}
	return nil
}

func (route *fakeRouteNetlink) putAddress(fd int, observation routeAddressObservation) {
	route.mu.Lock()
	defer route.mu.Unlock()
	if route.addresses[fd] == nil {
		route.addresses[fd] = make(map[int][]routeAddressObservation)
	}
	route.addresses[fd][observation.ifIndex] = append(route.addresses[fd][observation.ifIndex], observation)
}

func (route *fakeRouteNetlink) putRoute(fd int, observation routeIPv4Observation) {
	route.mu.Lock()
	defer route.mu.Unlock()
	route.putRouteLocked(fd, observation)
}

func (route *fakeRouteNetlink) putRouteLocked(fd int, observation routeIPv4Observation) {
	if route.routes[fd] == nil {
		route.routes[fd] = make(map[int][]routeIPv4Observation)
	}
	route.routes[fd][observation.ifIndex] = append(route.routes[fd][observation.ifIndex], observation)
}

func (route *fakeRouteNetlink) deleteCount() int {
	route.mu.Lock()
	defer route.mu.Unlock()
	return route.deletes
}
