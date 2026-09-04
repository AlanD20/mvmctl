package network

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestLinkEffectConfiguresAndVerifiesOnlyCanonicalKernelDerivedL3(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	require.NoError(t, err)
	bridgeAddresses := fixture.route.addressesSnapshot(fixture.hostFD, topology.managedBridge.ifIndex)
	wantBridgeAddresses := []routeAddressObservation{{
		ifIndex: topology.managedBridge.ifIndex,
		prefix:  netip.MustParsePrefix("10.44.0.1/24"),
		scope:   unix.RT_SCOPE_UNIVERSE,
	}}
	if diff := cmp.Diff(wantBridgeAddresses, bridgeAddresses,
		cmp.AllowUnexported(routeAddressObservation{}),
		cmpopts.EquateComparable(netip.Prefix{}),
	); diff != "" {
		t.Errorf("managed bridge addresses mismatch (-want +got):\n%s", diff)
	}
	bridgeBookkeeping, err := deriveBridgeKernelBookkeeping(endpoint.network)
	require.NoError(t, err)
	wantBridgeRoutes := make([]routeIPv4Observation, 0, 4)
	for _, route := range []routeExpectation{
		endpoint.network.bridgeConnectedRoute,
		bridgeBookkeeping.local,
		bridgeBookkeeping.networkBroadcast,
		bridgeBookkeeping.subnetBroadcast,
	} {
		wantBridgeRoutes = append(
			wantBridgeRoutes,
			mustRouteObservation(t, route, topology.managedBridge.ifIndex),
		)
	}
	if diff := cmp.Diff(
		wantBridgeRoutes,
		fixture.route.routesSnapshot(fixture.hostFD, topology.managedBridge.ifIndex),
		cmp.AllowUnexported(routeIPv4Observation{}),
		cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
	); diff != "" {
		t.Errorf("managed bridge routes mismatch (-want +got):\n%s", diff)
	}
	loopback := fixture.route.mustLink(t, fixture.vmFD, loopbackLinkName)
	loopbackAddresses := fixture.route.addressesSnapshot(fixture.vmFD, loopback.ifIndex)
	wantLoopbackAddresses := []routeAddressObservation{{
		ifIndex: loopback.ifIndex,
		prefix:  netip.MustParsePrefix("127.0.0.1/8"),
		scope:   unix.RT_SCOPE_HOST,
	}}
	if diff := cmp.Diff(wantLoopbackAddresses, loopbackAddresses,
		cmp.AllowUnexported(routeAddressObservation{}),
		cmpopts.EquateComparable(netip.Prefix{}),
	); diff != "" {
		t.Errorf("loopback addresses mismatch (-want +got):\n%s", diff)
	}
	wantLoopbackRoutes := []routeIPv4Observation{
		mustRouteObservation(t, canonicalLoopbackL3.localRoute, loopback.ifIndex),
		mustRouteObservation(t, canonicalLoopbackL3.connectedRoute, loopback.ifIndex),
	}
	if diff := cmp.Diff(
		wantLoopbackRoutes,
		fixture.route.routesSnapshot(fixture.vmFD, loopback.ifIndex),
		cmp.AllowUnexported(routeIPv4Observation{}),
		cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
	); diff != "" {
		t.Errorf("loopback routes mismatch (-want +got):\n%s", diff)
	}

	for _, target := range []struct {
		fd       int
		ifIndex  int
		describe string
	}{
		{fixture.hostFD, topology.veth.host.ifIndex, "host veth"},
		{fixture.vmFD, topology.veth.peer.ifIndex, "namespace veth"},
		{fixture.vmFD, topology.localBridge.ifIndex, "local bridge"},
		{fixture.vmFD, topology.tap.ifIndex, "TAP"},
	} {
		assert.Empty(t, fixture.route.addressesSnapshot(target.fd, target.ifIndex), target.describe)
		assert.Empty(t, fixture.route.routesSnapshot(target.fd, target.ifIndex), target.describe)
	}
	wantMutations := []addressMutation{
		{namespaceFD: fixture.hostFD, ifIndex: topology.managedBridge.ifIndex, prefix: "10.44.0.1/24"},
		{namespaceFD: fixture.vmFD, ifIndex: loopback.ifIndex, prefix: "127.0.0.1/8"},
	}
	if diff := cmp.Diff(
		wantMutations,
		fixture.route.addressMutationsSnapshot(),
		cmp.AllowUnexported(addressMutation{}),
	); diff != "" {
		t.Errorf("address mutations mismatch (-want +got):\n%s", diff)
	}
	assert.Zero(t, fixture.route.stateMutationCount())
}

// Rationale: Fabric links are fixed unnumbered L2 members. An unexpected IPv4 address or route is authority-bearing
// state and must block configuration rather than be repaired or silently retained.
func TestLinkEffectRejectsUnexpectedFabricIPv4Authority(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*linkEffectFixture, Topology){
		"host veth address": func(fixture *linkEffectFixture, topology Topology) {
			fixture.route.putAddress(fixture.hostFD, routeAddressObservation{
				ifIndex: topology.veth.host.ifIndex,
				prefix:  netip.MustParsePrefix("10.44.0.9/24"),
				scope:   unix.RT_SCOPE_UNIVERSE,
			})
		},
		"namespace veth route": func(fixture *linkEffectFixture, topology Topology) {
			fixture.route.putRoute(fixture.vmFD, routeIPv4Observation{
				destination: netip.MustParsePrefix("10.99.0.0/24"),
				source:      netip.MustParseAddr("10.99.0.1"),
				ifIndex:     topology.veth.peer.ifIndex,
				table:       unix.RT_TABLE_MAIN,
				protocol:    unix.RTPROT_KERNEL,
				scope:       unix.RT_SCOPE_LINK,
				kind:        unix.RTN_UNICAST,
			})
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture, topology, endpoint := newL3EffectFixture(t)
			mutate(fixture, topology)

			err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

			assert.ErrorContains(t, errors.Unwrap(err), "fabric link has unexpected IPv4 authority")
			assert.Empty(t, fixture.route.addressMutationsSnapshot())
		})
	}
}

// Rationale: Numbered links admit one complete canonical address/route set. Extra or partial authority is not repaired
// because doing so could adopt or overwrite state that the retained endpoint does not own.
func TestLinkEffectRejectsUnexpectedNumberedIPv4Authority(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*linkEffectFixture, Topology){
		"extra bridge address": func(fixture *linkEffectFixture, topology Topology) {
			fixture.route.putAddress(fixture.hostFD, routeAddressObservation{
				ifIndex: topology.managedBridge.ifIndex,
				prefix:  netip.MustParsePrefix("10.44.0.9/24"),
				scope:   unix.RT_SCOPE_UNIVERSE,
			})
		},
		"near-match bridge broadcast route": func(fixture *linkEffectFixture, topology Topology) {
			fixture.route.putRoute(fixture.hostFD, routeIPv4Observation{
				destination: netip.MustParsePrefix("10.44.0.255/32"),
				source:      netip.MustParseAddr("10.44.0.2"),
				hasSource:   true,
				ifIndex:     topology.managedBridge.ifIndex,
				table:       unix.RT_TABLE_LOCAL,
				protocol:    unix.RTPROT_KERNEL,
				scope:       unix.RT_SCOPE_LINK,
				kind:        unix.RTN_BROADCAST,
			})
		},
		"extra loopback route": func(fixture *linkEffectFixture, _ Topology) {
			loopback := fixture.route.mustLink(t, fixture.vmFD, loopbackLinkName)
			fixture.route.putRoute(fixture.vmFD, routeIPv4Observation{
				destination: netip.MustParsePrefix("127.1.0.0/16"),
				source:      netip.MustParseAddr("127.0.0.1"),
				hasSource:   true,
				ifIndex:     loopback.ifIndex,
				table:       unix.RT_TABLE_LOCAL,
				protocol:    unix.RTPROT_KERNEL,
				scope:       unix.RT_SCOPE_HOST,
				kind:        unix.RTN_LOCAL,
			})
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture, topology, endpoint := newL3EffectFixture(t)
			require.NoError(t, fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint))
			mutate(fixture, topology)

			err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

			require.Error(t, err)
			assert.Len(t, fixture.route.addressMutationsSnapshot(), 2)
		})
	}
}

func TestLinkEffectRejectsEndpointRebindingBeforeKernelInspection(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	endpoint.key.ownerUID++

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	require.Error(t, err)
	assert.Empty(t, fixture.route.addressMutationsSnapshot())
	assert.Zero(t, fixture.route.inspectCount(fixture.hostFD, topology.managedBridge.name))
}

// Rationale: A lost mutation acknowledgement is not enough to classify address state. Fresh exact inspection must
// record that the address and its kernel route committed, retain the error, and let a retry converge without mutation.
func TestLinkEffectReconcilesAddressACKAmbiguityBeforeRetry(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	ackLost := errors.New("injected address acknowledgement loss")
	fixture.route.addressError = ackLost

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	require.Error(t, err)
	assert.ErrorIs(t, err, ackLost)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.ClassRetryable, domainErr.Class)
	assert.Equal(t, true, domainErr.Details["mutation_committed"])
	assert.Equal(t, false, domainErr.Details["mutation_outcome_unknown"])
	require.Len(t, fixture.route.addressMutationsSnapshot(), 1)

	fixture.route.addressError = nil
	require.NoError(t, fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint))
	assert.Len(t, fixture.route.addressMutationsSnapshot(), 2,
		"retry must retain the committed bridge address and configure only the pending loopback address")
	require.NoError(t, fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint))
	assert.Len(t, fixture.route.addressMutationsSnapshot(), 2, "fully converged retry must be read-only")
}

func TestLinkEffectClassifiesProvenUncommittedAddressErrorForRetry(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	ackError := errors.New("injected rejected address mutation")
	fixture.route.applyAddress = false
	fixture.route.addressError = ackError

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	require.Error(t, err)
	assert.ErrorIs(t, err, ackError)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.ClassRetryable, domainErr.Class)
	assert.Equal(t, false, domainErr.Details["mutation_committed"])
	assert.Equal(t, false, domainErr.Details["mutation_outcome_unknown"])
	assert.Empty(t, fixture.route.addressesSnapshot(fixture.hostFD, topology.managedBridge.ifIndex))
}

// Rationale: Each activation mutation must re-admit the exact derived role and use only the ifindex returned by that
// inspection. IFF_RUNNING/carrier is deliberately irrelevant; IFF_UP is the sole activation state bit.
func TestLinkEffectRaisesAndLowersExactTopologyRoles(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	require.NoError(t, fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint))
	loopback := fixture.route.mustLink(t, fixture.vmFD, loopbackLinkName)
	loopback.flags |= unix.IFF_RUNNING
	fixture.route.put(fixture.vmFD, loopback)

	raise := []func(context.Context, NamespacePin, Topology) error{
		fixture.effect.RaiseLoopback,
		fixture.effect.RaiseLocalBridge,
		fixture.effect.RaiseTAP,
		fixture.effect.RaiseNamespaceVeth,
		fixture.effect.RaiseHostVeth,
	}
	for _, operation := range raise {
		require.NoError(t, operation(t.Context(), fixture.pin, topology))
	}
	wantRaised := []linkStateMutation{
		{namespaceFD: fixture.vmFD, ifIndex: loopback.ifIndex, up: true},
		{namespaceFD: fixture.vmFD, ifIndex: topology.localBridge.ifIndex, up: true},
		{namespaceFD: fixture.vmFD, ifIndex: topology.tap.ifIndex, up: true},
		{namespaceFD: fixture.vmFD, ifIndex: topology.veth.peer.ifIndex, up: true},
		{namespaceFD: fixture.hostFD, ifIndex: topology.veth.host.ifIndex, up: true},
	}
	if diff := cmp.Diff(
		wantRaised,
		fixture.route.stateMutationsSnapshot(),
		cmp.AllowUnexported(linkStateMutation{}),
	); diff != "" {
		t.Errorf("raised link mutations mismatch (-want +got):\n%s", diff)
	}
	for _, target := range wantRaised {
		assert.NotZero(t, fixture.route.linkByIndex(target.namespaceFD, target.ifIndex).flags&unix.IFF_UP)
	}

	lower := []func(context.Context, NamespacePin, Topology) error{
		fixture.effect.LowerHostVeth,
		fixture.effect.LowerNamespaceVeth,
		fixture.effect.LowerTAP,
		fixture.effect.LowerLocalBridge,
		fixture.effect.LowerLoopback,
	}
	for _, operation := range lower {
		require.NoError(t, operation(t.Context(), fixture.pin, topology))
	}
	mutations := fixture.route.stateMutationsSnapshot()
	require.Len(t, mutations, 10)
	wantLowered := []linkStateMutation{
		{namespaceFD: fixture.hostFD, ifIndex: topology.veth.host.ifIndex, up: false},
		{namespaceFD: fixture.vmFD, ifIndex: topology.veth.peer.ifIndex, up: false},
		{namespaceFD: fixture.vmFD, ifIndex: topology.tap.ifIndex, up: false},
		{namespaceFD: fixture.vmFD, ifIndex: topology.localBridge.ifIndex, up: false},
		{namespaceFD: fixture.vmFD, ifIndex: loopback.ifIndex, up: false},
	}
	if diff := cmp.Diff(wantLowered, mutations[5:], cmp.AllowUnexported(linkStateMutation{})); diff != "" {
		t.Errorf("lowered link mutations mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: An identity mismatch must be detected before an ifindex enters a mutation request. This protects against
// a derived-name replacement reusing the retained numeric ifindex.
func TestLinkEffectRejectsReplacementBeforeLinkStateMutation(t *testing.T) {
	t.Parallel()

	fixture, topology, _ := newL3EffectFixture(t)
	host := fixture.route.mustLink(t, fixture.hostFD, topology.veth.host.name)
	host.alias = "foreign"
	fixture.route.put(fixture.hostFD, host)

	err := fixture.effect.RaiseHostVeth(t.Context(), fixture.pin, topology)

	assert.ErrorContains(t, errors.Unwrap(err), "ownership alias changed")
	assert.Zero(t, fixture.route.stateMutationCount())
}

// Rationale: A state mutation error can still mean the requested IFF_UP bit committed. The effect must freshly prove
// that state, preserve retryable evidence, and make the next call a read-only convergence check.
func TestLinkEffectReconcilesLinkStateACKAmbiguity(t *testing.T) {
	t.Parallel()

	fixture, topology, _ := newL3EffectFixture(t)
	ackLost := errors.New("injected link acknowledgement loss")
	fixture.route.stateError = ackLost

	err := fixture.effect.RaiseHostVeth(t.Context(), fixture.pin, topology)

	require.Error(t, err)
	assert.ErrorIs(t, err, ackLost)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["mutation_committed"])
	assert.Equal(t, false, domainErr.Details["mutation_outcome_unknown"])
	assert.Len(t, fixture.route.stateMutationsSnapshot(), 1)

	fixture.route.stateError = nil
	require.NoError(t, fixture.effect.RaiseHostVeth(t.Context(), fixture.pin, topology))
	assert.Len(t, fixture.route.stateMutationsSnapshot(), 1)
}

func newL3EffectFixture(t *testing.T) (*linkEffectFixture, Topology, endpointL3) {
	t.Helper()
	fixture := newLinkEffectFixture(t)
	localBridge, tap, veth := fixture.seedAttachedTopology(t, true)
	key := mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID)
	fixture.pin.(*fakeNamespacePin).handle = namespaceHandle{
		path: fixture.names.namespaceHandle, identity: fixture.vmNamespace, mode: namespaceHandleMode,
	}
	managedBridge := fixture.route.mustIdentity(
		fixture.hostFD,
		fixture.names.managedBridge,
		fixture.hostNamespace,
	)
	topology := Topology{
		key:           key,
		handle:        fixture.pin.Handle(),
		managedBridge: managedBridge,
		localBridge:   localBridge,
		tap:           tap,
		veth:          veth,
	}
	fixture.route.put(fixture.vmFD, routeLinkObservation{
		name: loopbackLinkName, ifIndex: 1, deviceType: unix.ARPHRD_LOOPBACK,
	})
	network, err := admitManagedNetworkL3(managedNetworkL3Input{prefix: netip.MustParsePrefix("10.44.0.0/24")})
	require.NoError(t, err)
	endpoint, err := deriveEndpointL3(key, network, endpointL3Input{
		guestAddress: netip.MustParseAddr("10.44.0.2"),
	})
	require.NoError(t, err)
	bridgeBookkeeping, err := deriveBridgeKernelBookkeeping(network)
	require.NoError(t, err)
	fixture.route.afterAddress = func(fd, ifIndex int, expectation addressExpectation) {
		if expectation == canonicalLoopbackL3.address {
			fixture.route.putRouteLocked(fd, mustRouteObservation(t, canonicalLoopbackL3.localRoute, ifIndex))
			fixture.route.putRouteLocked(fd, mustRouteObservation(t, canonicalLoopbackL3.connectedRoute, ifIndex))
			return
		}
		for _, route := range []routeExpectation{
			network.bridgeConnectedRoute,
			bridgeBookkeeping.local,
			bridgeBookkeeping.networkBroadcast,
			bridgeBookkeeping.subnetBroadcast,
		} {
			fixture.route.putRouteLocked(fd, mustRouteObservation(t, route, ifIndex))
		}
	}
	return fixture, topology, endpoint
}

func mustRouteObservation(t *testing.T, expectation routeExpectation, ifIndex int) routeIPv4Observation {
	t.Helper()
	observation, err := routeExpectationToObservation(expectation, ifIndex)
	require.NoError(t, err)
	return observation
}
