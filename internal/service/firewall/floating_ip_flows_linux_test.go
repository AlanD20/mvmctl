package firewall

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFloatingIPFlowAuthorityRequiresScopeGenerationAndAbsentKey(t *testing.T) {
	intent := secondaryDurableIntent()
	compiled, err := intent.compile()
	require.NoError(t, err)
	resource := intent.FloatingIPs[0]
	for name, mutate := range map[string]func(*mutationLeaseState, *kernelObservation, *string, *uint64){
		"fenced":           func(*mutationLeaseState, *kernelObservation, *string, *uint64) {},
		"wrong generation": func(_ *mutationLeaseState, _ *kernelObservation, _ *string, g *uint64) { *g++ },
		"zero generation":  func(_ *mutationLeaseState, _ *kernelObservation, _ *string, g *uint64) { *g = 0 },
		"missing resource": func(_ *mutationLeaseState, _ *kernelObservation, id *string, _ *uint64) { *id = "absent" },
		"different network": func(s *mutationLeaseState,
			_ *kernelObservation,
			_ *string,
			_ *uint64) {
			s.binding.NetworkID = "other"
		},

		"different operator": func(s *mutationLeaseState,
			_ *kernelObservation,
			_ *string,
			_ *uint64) {
			s.binding.OwnerUID++
		},

		"live key": func(_ *mutationLeaseState, o *kernelObservation, _ *string, _ *uint64) {
			o.tables[0].secondaryElements = []secondaryLeaseElement{{key: compiled.bridge.secondaryOwners[resource.ID],
				timeout:   resource.LeaseDuration,
				remaining: time.Second}}
		},
		"expiry boundary key still present": func(_ *mutationLeaseState, o *kernelObservation, _ *string, _ *uint64) {
			o.tables[0].secondaryElements = []secondaryLeaseElement{{key: compiled.bridge.secondaryOwners[resource.ID],
				timeout: resource.LeaseDuration}}
		},
		"foreign generation": func(_ *mutationLeaseState,
			o *kernelObservation,
			_ *string,
			_ *uint64) {
			o.generationID = "foreign"
		},
	} {
		t.Run(name, func(t *testing.T) {
			state := &mutationLeaseState{intent: intent,
				compiled: compiled,
				binding: EndpointBinding{OwnerUID: resource.OwnerUID,
					NetworkID: resource.NetworkID}}
			observation := kernelObservationFromCompiled(compiled)
			id, generation := resource.ID, resource.Generation
			mutate(state, &observation, &id, &generation)
			address, err := state.floatingIPFlowAddress(id, generation, observation)
			if name == "fenced" {
				require.NoError(t, err)
				assert.Equal(t, resource.IPv4, address)
			} else {
				require.Error(t, err)
				assert.False(t, address.IsValid())
			}
		})
	}
	var lease *MutationLease
	require.Error(t, lease.InvalidateFloatingIPFlows(t.Context(), resource.ID, resource.Generation))
	require.Error(t,
		(&MutationLease{state: &mutationLeaseState{released: true}}).InvalidateFloatingIPFlows(t.Context(),
			resource.ID,
			resource.Generation))
}

type conntrackDeadlineConnection struct {
	*fakeKernelObservationConnection
	deadline    time.Time
	deadlineErr error
}

func (connection *conntrackDeadlineConnection) SetDeadline(deadline time.Time) error {
	connection.deadline = deadline
	return connection.deadlineErr
}

func TestConntrackSocketUsesExactRetainedNamespaceAndDeadline(t *testing.T) {
	fixture := newKernelObservationNamespaceFixture()
	deps := fixture.dependencies()
	connection, _, _ := testKernelTransportConnection(nil)
	socket := &conntrackDeadlineConnection{fakeKernelObservationConnection: connection}
	var gotConfig netlink.Config
	deps.dial = func(_ context.Context, family int, config *netlink.Config) (kernelObservationNetlinkConnection, error) {
		require.Equal(t, unix.NETLINK_NETFILTER, family)
		gotConfig = *config
		return socket, nil
	}
	authority := newKernelObservationNamespaceAuthorityWithDeps(deps)
	lease, err := authority.retainExpectedNamespace(t.Context(), fixture.identity)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	transport, err := lease.newConntrackTransport(ctx)
	require.NoError(t, err)
	assert.Equal(t, fixture.duplicateFD, gotConfig.NetNS)
	assert.GreaterOrEqual(t, gotConfig.NetNS, 3)
	assert.True(t, gotConfig.Strict)
	deadline, _ := ctx.Deadline()
	assert.Equal(t, deadline, socket.deadline)
	require.NoError(t, transport.close(context.WithoutCancel(ctx), nil))
	assert.Equal(t, 1, connection.closeCalls)
	require.NoError(t, lease.Release(t.Context()))
	_, err = lease.newConntrackTransport(t.Context())
	require.Error(t, err)
}

func TestConntrackNamespaceAndSocketFailureCloseWithoutFallback(t *testing.T) {
	for _, name := range []string{"unsafe FD",
		"identity drift",
		"deadline failure",
		"dial partial failure",
		"no deadline capability"} {
		t.Run(name, func(t *testing.T) {
			fixture := newKernelObservationNamespaceFixture()
			deps := fixture.dependencies()
			connection, _, _ := testKernelTransportConnection(nil)
			socket := &conntrackDeadlineConnection{fakeKernelObservationConnection: connection}
			dialed := 0
			deps.dial = func(context.Context, int, *netlink.Config) (kernelObservationNetlinkConnection, error) {
				dialed++
				if name == "dial partial failure" {
					return socket, errors.New("dial failed")
				}
				if name == "no deadline capability" {
					return connection, nil
				}
				return socket, nil
			}
			lease,
				err := newKernelObservationNamespaceAuthorityWithDeps(deps).retainExpectedNamespace(t.Context(),
				fixture.identity)
			require.NoError(t, err)
			switch name {
			case "unsafe FD":
				lease.state.fd = 0
			case "identity drift":
				stat := fixture.stats[fixture.duplicateFD]
				stat.Ino++
				fixture.stats[fixture.duplicateFD] = stat
			case "deadline failure":
				socket.deadlineErr = errors.New("deadline failed")
			}
			_, err = lease.newConntrackTransport(t.Context())
			require.Error(t, err)
			if name == "unsafe FD" || name == "identity drift" {
				assert.Zero(t, dialed)
				assert.Zero(t, connection.closeCalls)
			} else {
				assert.Equal(t, 1, dialed)
				assert.Equal(t, 1, connection.closeCalls)
			}
		})
	}
}

func TestConntrackMatchesAllTupleDirections(t *testing.T) {
	addresses := [4]netip.Addr{netip.MustParseAddr("10.0.0.1"),
		netip.MustParseAddr("10.0.0.2"),
		netip.MustParseAddr("10.0.0.3"),
		netip.MustParseAddr("10.0.0.4")}
	flow := conntrackFlow{addresses: addresses}
	for _, address := range addresses {
		assert.True(t, flow.matches(address))
	}
	assert.False(t, flow.matches(netip.MustParseAddr("10.0.0.5")))
}
