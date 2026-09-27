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
	wantBridgeRoutes := []routeIPv4Observation{
		literalRoute("10.44.0.0/24", "10.44.0.1", topology.managedBridge.ifIndex,
			unix.RT_TABLE_MAIN, unix.RTPROT_KERNEL, unix.RT_SCOPE_LINK, unix.RTN_UNICAST),
		literalRoute("10.44.0.1/32", "10.44.0.1", topology.managedBridge.ifIndex,
			unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
		literalRoute("10.44.0.255/32", "10.44.0.1", topology.managedBridge.ifIndex,
			unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_LINK, unix.RTN_BROADCAST),
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
		literalRoute("127.0.0.1/32", "127.0.0.1", loopback.ifIndex,
			unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
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
	assert.NotZero(t, fixture.route.mustLink(t, fixture.hostFD, topology.managedBridge.name).flags&unix.IFF_UP)

	require.NoError(t, fixture.effect.RaiseLoopback(t.Context(), fixture.pin, topology))
	wantRaisedLoopbackRoutes := []routeIPv4Observation{
		literalRoute("127.0.0.1/32", "127.0.0.1", loopback.ifIndex,
			unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
		literalRoute("127.0.0.0/8", "127.0.0.1", loopback.ifIndex,
			unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
		literalRoute("127.255.255.255/32", "127.0.0.1", loopback.ifIndex,
			unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_LINK, unix.RTN_BROADCAST),
	}
	if diff := cmp.Diff(
		wantRaisedLoopbackRoutes,
		fixture.route.routesSnapshot(fixture.vmFD, loopback.ifIndex),
		cmp.AllowUnexported(routeIPv4Observation{}),
		cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
	); diff != "" {
		t.Errorf("raised loopback routes mismatch (-want +got):\n%s", diff)
	}
}

// Rationale: A reused guest IP may inherit a stale neighbor on the shared host bridge, but clearing a second
// address or an entry on another link would exceed the VM topology's ownership boundary.
func TestLinkEffectInvalidatesOnlyAdmittedManagedNeighbor(t *testing.T) {
	t.Parallel()
	fixture, topology, endpoint := newL3EffectFixture(t)
	address := endpoint.guestAddress.value
	target := routeNeighborKey{
		namespaceFD: fixture.hostFD, ifIndex: topology.managedBridge.ifIndex, address: address,
	}
	other := routeNeighborKey{
		namespaceFD: fixture.hostFD, ifIndex: topology.managedBridge.ifIndex,
		address: netip.MustParseAddr("10.44.0.3"),
	}
	fixture.route.neighbors[target] = routeNeighborObservation{ifIndex: target.ifIndex, address: target.address}
	fixture.route.neighbors[other] = routeNeighborObservation{ifIndex: other.ifIndex, address: other.address}

	require.NoError(t, fixture.effect.InvalidateManagedNeighbor(t.Context(), fixture.pin, topology, endpoint))

	_, targetPresent := fixture.route.neighbors[target]
	assert.False(t, targetPresent)
	_, otherPresent := fixture.route.neighbors[other]
	assert.True(t, otherPresent)
	if diff := cmp.Diff([]routeNeighborKey{target}, fixture.route.neighborInvalidations,
		cmp.AllowUnexported(routeNeighborKey{}), cmpopts.EquateComparable(netip.Addr{})); diff != "" {
		t.Errorf("neighbor invalidation target mismatch (-want +got):\n%s", diff)
	}
}

func TestLinkEffectRejectsNeighborInvalidationAfterHostExposure(t *testing.T) {
	t.Parallel()
	fixture, topology, endpoint := newL3EffectFixture(t)
	hostVeth := fixture.route.mustLink(t, fixture.hostFD, topology.veth.host.name)
	hostVeth.flags |= unix.IFF_UP
	fixture.route.put(fixture.hostFD, hostVeth)

	err := fixture.effect.InvalidateManagedNeighbor(t.Context(), fixture.pin, topology, endpoint)

	require.Error(t, err)
	assert.ErrorContains(t, errors.Unwrap(err), "host veth down")
	assert.Empty(t, fixture.route.neighborInvalidations)
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

func TestLinkEffectClassifiesAddressInspectionUncertainty(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	inspectionError := errors.New("injected post-address inspection failure")
	fixture.route.addressInspectError = func(_ context.Context, fd, ifIndex, count int) error {
		if fd == fixture.hostFD && ifIndex == topology.managedBridge.ifIndex && count == 2 {
			return inspectionError
		}
		return nil
	}

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	require.Error(t, err)
	assert.ErrorIs(t, err, inspectionError)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, false, domainErr.Details["mutation_committed"])
	assert.Equal(t, true, domainErr.Details["mutation_outcome_unknown"])
}

func TestLinkEffectRejectsAddressTargetReplacementAfterMutation(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	afterAddress := fixture.route.afterAddress
	fixture.route.afterAddress = func(fd, ifIndex int, expectation addressExpectation) {
		afterAddress(fd, ifIndex, expectation)
		if fd == fixture.hostFD && ifIndex == topology.managedBridge.ifIndex {
			link := fixture.route.links[fd][topology.managedBridge.name]
			link.ifIndex++
			fixture.route.links[fd][topology.managedBridge.name] = link
		}
	}

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	require.Error(t, err)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, false, domainErr.Details["mutation_committed"])
	assert.Equal(t, true, domainErr.Details["mutation_outcome_unknown"])
	assert.Equal(t, 1, fixture.route.addressInspectCount(fixture.hostFD, topology.managedBridge.ifIndex),
		"post-mutation inspection must not use the stale ifindex")
	assert.Empty(t, fixture.route.addressesSnapshot(fixture.hostFD, topology.managedBridge.ifIndex+1),
		"the replacement ifindex must never become address-inspection authority")
}

func TestLinkEffectReadmitsAddressTargetImmediatelyBeforeMutation(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	fixture.route.addressInspectError = func(_ context.Context, fd, ifIndex, count int) error {
		if fd == fixture.hostFD && ifIndex == topology.managedBridge.ifIndex && count == 1 {
			link := fixture.route.links[fd][topology.managedBridge.name]
			link.ifIndex++
			fixture.route.links[fd][topology.managedBridge.name] = link
		}
		return nil
	}

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	require.Error(t, err)
	assert.Empty(t, fixture.route.addressMutationsSnapshot())
}

func TestLinkEffectRejectsEveryCorruptTopologyBindingBeforeL3Mutation(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*linkEffectFixture, *Topology){
		"namespace binding": func(fixture *linkEffectFixture, topology *Topology) {
			topology.managedBridge.namespace = fixture.vmNamespace
		},
		"name": func(fixture *linkEffectFixture, topology *Topology) {
			fixture.route.corruptLink(fixture.vmFD, topology.localBridge.name, func(link *routeLinkObservation) {
				link.name = "foreign0"
			})
		},
		"ifindex": func(fixture *linkEffectFixture, topology *Topology) {
			fixture.route.corruptLink(fixture.vmFD, topology.tap.name, func(link *routeLinkObservation) {
				link.ifIndex++
			})
		},
		"kind": func(fixture *linkEffectFixture, topology *Topology) {
			fixture.route.corruptLink(fixture.hostFD, topology.managedBridge.name, func(link *routeLinkObservation) {
				link.kind = "dummy"
			})
		},
		"master": func(fixture *linkEffectFixture, topology *Topology) {
			fixture.route.corruptLink(fixture.vmFD, topology.tap.name, func(link *routeLinkObservation) {
				link.master++
			})
		},
		"peer reciprocity": func(fixture *linkEffectFixture, topology *Topology) {
			fixture.route.corruptLink(fixture.hostFD, topology.veth.host.name, func(link *routeLinkObservation) {
				link.peer++
			})
		},
		"TAP authority": func(fixture *linkEffectFixture, topology *Topology) {
			fixture.route.corruptLink(fixture.vmFD, topology.tap.name, func(link *routeLinkObservation) {
				link.tunOwner++
			})
		},
		"loopback device": func(fixture *linkEffectFixture, _ *Topology) {
			fixture.route.corruptLink(fixture.vmFD, loopbackLinkName, func(link *routeLinkObservation) {
				link.deviceType = unix.ARPHRD_ETHER
			})
		},
		"loopback extra authority": func(fixture *linkEffectFixture, _ *Topology) {
			fixture.route.corruptLink(fixture.vmFD, loopbackLinkName, func(link *routeLinkObservation) {
				link.alias = "foreign"
			})
		},
		"managed bridge down": func(fixture *linkEffectFixture, topology *Topology) {
			fixture.route.corruptLink(fixture.hostFD, topology.managedBridge.name, func(link *routeLinkObservation) {
				link.flags &^= unix.IFF_UP
			})
		},
	}

	for name, corrupt := range tests {
		t.Run(name, func(t *testing.T) {
			fixture, topology, endpoint := newL3EffectFixture(t)
			corrupt(fixture, &topology)

			err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

			require.Error(t, err)
			assert.Empty(t, fixture.route.addressMutationsSnapshot())
			assert.Zero(t, fixture.route.stateMutationCount())
		})
	}
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

// Rationale: Linux may retain the exact kernel-created 127/8 local route after lo is lowered.
// Cleanup must still admit that bounded route set without accepting an unrelated route.
func TestLinkEffectLowerLoopbackAcceptsRetainedConnectedRoute(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	require.NoError(t, fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint))
	require.NoError(t, fixture.effect.RaiseLoopback(t.Context(), fixture.pin, topology))
	originalAfterState := fixture.route.afterState
	fixture.route.afterState = func(fd, ifIndex int, up bool) {
		originalAfterState(fd, ifIndex, up)
		if fd == fixture.vmFD && ifIndex == 1 && !up {
			fixture.route.replaceRoutesLocked(fd, ifIndex, []routeIPv4Observation{
				literalRoute("127.0.0.1/32", "127.0.0.1", ifIndex,
					unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
				literalRoute("127.0.0.0/8", "127.0.0.1", ifIndex,
					unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
			})
		}
	}
	require.NoError(t, fixture.effect.LowerLoopback(t.Context(), fixture.pin, topology))
	loopback := fixture.route.mustLink(t, fixture.vmFD, loopbackLinkName)
	assert.Zero(t, loopback.flags&unix.IFF_UP)
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

func TestLinkEffectClassifiesStillBeforeLinkStateError(t *testing.T) {
	t.Parallel()

	fixture, topology, _ := newL3EffectFixture(t)
	ackError := errors.New("injected rejected link mutation")
	fixture.route.applyState = false
	fixture.route.stateError = ackError

	err := fixture.effect.RaiseHostVeth(t.Context(), fixture.pin, topology)

	require.Error(t, err)
	assert.ErrorIs(t, err, ackError)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, false, domainErr.Details["mutation_committed"])
	assert.Equal(t, false, domainErr.Details["mutation_outcome_unknown"])
}

func TestLinkEffectClassifiesUnknownLinkStateInspection(t *testing.T) {
	t.Parallel()

	fixture, topology, _ := newL3EffectFixture(t)
	ackLost := errors.New("injected link acknowledgement loss")
	inspectionError := errors.New("injected post-link inspection failure")
	fixture.route.stateError = ackLost
	fixture.route.inspectError = func(_ int, name linkName, _ int) error {
		if name == topology.managedBridge.name && len(fixture.route.stateMutations) > 0 {
			return inspectionError
		}
		return nil
	}

	err := fixture.effect.RaiseHostVeth(t.Context(), fixture.pin, topology)

	require.Error(t, err)
	assert.ErrorIs(t, err, ackLost)
	assert.ErrorIs(t, err, inspectionError)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, false, domainErr.Details["mutation_committed"])
	assert.Equal(t, true, domainErr.Details["mutation_outcome_unknown"])
}

func TestLinkEffectPreservesDomainErrorClassification(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	cause := errs.New(
		errs.CodeNetworkNotFound,
		"classified inspection failure",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity("retained-network"),
		errs.WithDetails(map[string]any{"proof": "preserved"}),
	)
	fixture.route.addressInspectError = func(context.Context, int, int, int) error { return cause }

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, cause.Code, domainErr.Code)
	assert.Equal(t, cause.Class, domainErr.Class)
	assert.Equal(t, cause.Entity, domainErr.Entity)
	assert.Equal(t, cause.Details, domainErr.Details)
	assert.ErrorIs(t, err, cause)
}

func TestLinkEffectClosesBothNamespaceLeasesHostFirst(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	hostCloseError := errors.New("injected host lease close failure")
	vmCloseError := errors.New("injected VM lease close failure")
	fixture.closeErrors = map[int]error{
		fixture.hostFD: hostCloseError,
		fixture.vmFD:   vmCloseError,
	}

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	require.Error(t, err)
	assert.ErrorIs(t, err, hostCloseError)
	assert.ErrorIs(t, err, vmCloseError)
	assert.Equal(t, []int{fixture.hostFD, fixture.vmFD}, fixture.closeOrderSnapshot())
}

func TestLinkEffectPreservesPrimaryAndCloseFailures(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	primary := errors.New("injected primary inspection failure")
	closeFailure := errors.New("injected close failure")
	fixture.route.addressInspectError = func(context.Context, int, int, int) error { return primary }
	fixture.closeErrors = map[int]error{fixture.hostFD: closeFailure}

	err := fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint)

	require.Error(t, err)
	assert.ErrorIs(t, err, primary)
	assert.ErrorIs(t, err, closeFailure)
	assert.Equal(t, []int{fixture.hostFD, fixture.vmFD}, fixture.closeOrderSnapshot())
}

func TestLinkEffectInspectsAndClosesWithoutCanceledMutationContext(t *testing.T) {
	t.Parallel()

	fixture, topology, endpoint := newL3EffectFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	fixture.route.cancelAddress = cancel

	err := fixture.effect.ConfigureL3Down(ctx, fixture.pin, topology, endpoint)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.False(t, fixture.route.lastInspectCancelled(fixture.hostFD, topology.managedBridge.name))
	for _, closeCtx := range fixture.closeContextsSnapshot() {
		assert.NoError(t, closeCtx.Err())
	}
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
	bridgeObservation := fixture.route.mustLink(t, fixture.hostFD, fixture.names.managedBridge)
	bridgeObservation.flags |= unix.IFF_UP
	fixture.route.put(fixture.hostFD, bridgeObservation)
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
	fixture.route.afterAddress = func(fd, ifIndex int, expectation addressExpectation) {
		if expectation == canonicalLoopbackL3.address {
			fixture.route.replaceRoutesLocked(fd, ifIndex, []routeIPv4Observation{
				literalRoute("127.0.0.1/32", "127.0.0.1", ifIndex,
					unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
			})
			return
		}
		fixture.route.replaceRoutesLocked(fd, ifIndex, []routeIPv4Observation{
			literalRoute("10.44.0.0/24", "10.44.0.1", ifIndex,
				unix.RT_TABLE_MAIN, unix.RTPROT_KERNEL, unix.RT_SCOPE_LINK, unix.RTN_UNICAST),
			literalRoute("10.44.0.1/32", "10.44.0.1", ifIndex,
				unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
			literalRoute("10.44.0.255/32", "10.44.0.1", ifIndex,
				unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_LINK, unix.RTN_BROADCAST),
		})
	}
	fixture.route.afterState = func(fd, ifIndex int, up bool) {
		if fd != fixture.vmFD || ifIndex != 1 {
			return
		}
		routes := []routeIPv4Observation{
			literalRoute("127.0.0.1/32", "127.0.0.1", ifIndex,
				unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
		}
		if up {
			routes = append(routes,
				literalRoute("127.0.0.0/8", "127.0.0.1", ifIndex,
					unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_HOST, unix.RTN_LOCAL),
				literalRoute("127.255.255.255/32", "127.0.0.1", ifIndex,
					unix.RT_TABLE_LOCAL, unix.RTPROT_KERNEL, unix.RT_SCOPE_LINK, unix.RTN_BROADCAST),
			)
		}
		fixture.route.replaceRoutesLocked(fd, ifIndex, routes)
	}
	return fixture, topology, endpoint
}

func literalRoute(
	destination string,
	source string,
	ifIndex int,
	table uint32,
	protocol uint8,
	scope uint8,
	kind uint8,
) routeIPv4Observation {
	return routeIPv4Observation{
		destination: netip.MustParsePrefix(destination),
		source:      netip.MustParseAddr(source),
		hasSource:   true,
		ifIndex:     ifIndex,
		table:       table,
		protocol:    protocol,
		scope:       scope,
		kind:        kind,
	}
}
