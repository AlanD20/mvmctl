package network

import (
	"encoding/binary"
	"net/netip"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Rationale: Several ordinary default routes may share one uplink, but metrics must never authorize
// choosing between different uplinks. Reordered kernel dumps must retain the same exact route evidence.
func TestObserveSelectedUplinkGroupsRoutesByOutputLink(t *testing.T) {
	t.Parallel()

	base := testRouteMessageSpec{
		destination: "0.0.0.0/0", gateway: "192.0.2.1", source: "192.0.2.10", ifIndex: 10,
		table: unix.RT_TABLE_MAIN, protocol: unix.RTPROT_STATIC, scope: unix.RT_SCOPE_UNIVERSE,
		kind: unix.RTN_UNICAST, metric: 100,
	}
	tests := map[string]func(*testRouteMessageSpec){
		"identical route observations": func(*testRouteMessageSpec) {},
		"different metric":             func(route *testRouteMessageSpec) { route.metric = 200 },
		"different gateway":            func(route *testRouteMessageSpec) { route.gateway = "192.0.2.2" },
		"different preferred source":   func(route *testRouteMessageSpec) { route.source = "192.0.2.11" },
		"different protocol":           func(route *testRouteMessageSpec) { route.protocol = unix.RTPROT_DHCP },
		"different scope":              func(route *testRouteMessageSpec) { route.scope = unix.RT_SCOPE_LINK },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			other := base
			change(&other)
			routes := []netlink.Message{testRouteIPv4Message(t, other), testRouteIPv4Message(t, base)}
			links := []netlink.Message{testRouteLinkMessage(t, "eth0", "external", 10, unix.IFF_UP, 0, 0, "veth", nil)}
			adapter := testUplinkAdapter(t, &routes, &links)
			namespace := namespaceIdentity{device: 5, inode: 300}
			want := selectedUplink{
				link: linkIdentity{name: "eth0", ifIndex: 10, namespace: namespace}, kind: "veth", alias: "external",
				route: routeIPv4Observation{
					destination: netip.MustParsePrefix("0.0.0.0/0"),
					gateway:     netip.MustParseAddr(base.gateway), hasGateway: true,
					source: netip.MustParseAddr(base.source), hasSource: true,
					ifIndex: 10, table: unix.RT_TABLE_MAIN, protocol: unix.RTPROT_STATIC,
					scope: unix.RT_SCOPE_UNIVERSE, kind: unix.RTN_UNICAST, metric: 100, hasMetric: true,
				},
			}
			for range 2 {
				got, err := observeSelectedUplink(t.Context(), adapter, 41, namespace)
				require.NoError(t, err)
				if diff := cmp.Diff(want, got,
					cmp.AllowUnexported(selectedUplink{}, linkIdentity{}, namespaceIdentity{}, routeIPv4Observation{}),
					cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{}),
				); diff != "" {
					t.Errorf("selected uplink mismatch (-want +got):\n%s", diff)
				}
				slices.Reverse(routes)
			}
		})
	}
}

// Rationale: Absent and explicit zero metrics are distinct retained evidence and need a stable tie break.
func TestObserveSelectedUplinkDistinguishesAbsentAndZeroMetric(t *testing.T) {
	t.Parallel()

	absent := testRouteIPv4Message(t, testRouteMessageSpec{
		destination: "0.0.0.0/0", gateway: "192.0.2.1", ifIndex: 10, table: unix.RT_TABLE_MAIN,
		protocol: unix.RTPROT_STATIC, scope: unix.RT_SCOPE_UNIVERSE, kind: unix.RTN_UNICAST,
	})
	explicit := absent
	explicit.Data = appendTestRouteAttribute(t, slices.Clone(absent.Data), netlink.Attribute{
		Type: unix.RTA_PRIORITY, Data: []byte{0, 0, 0, 0},
	})
	routes := []netlink.Message{explicit, absent}
	links := []netlink.Message{testRouteLinkMessage(t, "eth0", "external", 10, unix.IFF_UP, 0, 0, "veth", nil)}
	adapter := testUplinkAdapter(t, &routes, &links)
	for range 2 {
		selected, err := observeSelectedUplink(t.Context(), adapter, 41, namespaceIdentity{device: 5, inode: 300})
		require.NoError(t, err)
		assert.False(t, selected.route.hasMetric)
		assert.Zero(t, selected.route.metric)
		slices.Reverse(routes)
	}
}

func TestObserveSelectedUplinkRejectsAmbiguousOrMissingAuthority(t *testing.T) {
	t.Parallel()

	first := testRouteMessageSpec{
		destination: "0.0.0.0/0", gateway: "192.0.2.1", ifIndex: 10, table: unix.RT_TABLE_MAIN,
		protocol: unix.RTPROT_STATIC, scope: unix.RT_SCOPE_UNIVERSE, kind: unix.RTN_UNICAST, metric: 100,
	}
	second := first
	second.ifIndex, second.metric = 11, 200
	firstMessage, secondMessage := testRouteIPv4Message(t, first), testRouteIPv4Message(t, second)
	firstLink := testRouteLinkMessage(t, "eth0", "external", 10, unix.IFF_UP, 0, 0, "veth", nil)
	secondLink := testRouteLinkMessage(t, "eth1", "external", 11, unix.IFF_UP, 0, 0, "veth", nil)
	multipath := firstMessage
	multipath.Data = appendTestRouteAttribute(t, slices.Clone(firstMessage.Data), netlink.Attribute{
		Type: unix.RTA_MULTIPATH, Data: []byte{1, 2, 3, 4},
	})
	tests := map[string]struct {
		routes  []netlink.Message
		links   []netlink.Message
		wantErr string
	}{
		"no default routes": {links: []netlink.Message{firstLink}, wantErr: "found 0"},
		"different uplinks despite different metrics": {
			routes: []netlink.Message{firstMessage, secondMessage}, links: []netlink.Message{firstLink, secondLink},
			wantErr: "found 2",
		},
		"missing output link": {routes: []netlink.Message{firstMessage}, wantErr: "output link is missing"},
		"duplicate link identity": {
			routes: []netlink.Message{firstMessage}, links: []netlink.Message{firstLink, firstLink},
			wantErr: "repeats an ifindex",
		},
		"multipath alongside valid route": {
			routes: []netlink.Message{firstMessage, multipath}, links: []netlink.Message{firstLink},
			wantErr: "multipath",
		},
		"only managed output link": {
			routes: []netlink.Message{firstMessage},
			links: []netlink.Message{
				testRouteLinkMessage(t, "eth0", "mvmctl:owned", 10, unix.IFF_UP, 0, 0, "veth", nil),
			},
			wantErr: "found 0",
		},
		"only loopback output link": {
			routes: []netlink.Message{firstMessage},
			links: []netlink.Message{
				testRouteLinkMessage(t, "lo", "external", 10, unix.IFF_LOOPBACK, 0, 0, "veth", nil),
			},
			wantErr: "found 0",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			adapter := testUplinkAdapter(t, &test.routes, &test.links)
			_, err := observeSelectedUplink(t.Context(), adapter, 41, namespaceIdentity{device: 5, inode: 300})
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

// A second routing table can have a multipath default while Auto selects the main-table uplink.
func TestObserveSelectedUplinkIgnoresDefaultsInOtherTables(t *testing.T) {
	t.Parallel()

	main := testRouteIPv4Message(t, testRouteMessageSpec{
		destination: "0.0.0.0/0", gateway: "192.0.2.1", ifIndex: 10, table: unix.RT_TABLE_MAIN,
		protocol: unix.RTPROT_STATIC, scope: unix.RT_SCOPE_UNIVERSE, kind: unix.RTN_UNICAST,
	})
	other := main
	other.Data = appendTestRouteAttribute(t, slices.Clone(main.Data), netlink.Attribute{
		Type: unix.RTA_TABLE, Data: binary.NativeEndian.AppendUint32(nil, 4000),
	})
	other.Data = appendTestRouteAttribute(t, other.Data, netlink.Attribute{Type: unix.RTA_MULTIPATH, Data: []byte{1}})
	routes := []netlink.Message{other, main}
	links := []netlink.Message{testRouteLinkMessage(t, "eth0", "external", 10, unix.IFF_UP, 0, 0, "veth", nil)}
	adapter := testUplinkAdapter(t, &routes, &links)
	selected, err := observeSelectedUplink(t.Context(), adapter, 41, namespaceIdentity{device: 5, inode: 300})
	require.NoError(t, err)
	assert.Equal(t, linkName("eth0"), selected.link.name)
	assert.EqualValues(t, unix.RT_TABLE_MAIN, selected.route.table)
}

// Rationale: Admitting another route on the same uplink must not weaken retained route/link checks.
func TestUplinkRevalidationRetainsExactRouteAndLinkEvidence(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*testRouteMessageSpec, *testRouteMessageSpec, *[]netlink.Message){
		"selected route gateway changed": func(first, _ *testRouteMessageSpec, _ *[]netlink.Message) {
			first.gateway = "192.0.2.2"
		},
		"selected route metric changed": func(first, _ *testRouteMessageSpec, _ *[]netlink.Message) {
			first.metric = 101
		},
		"second route changes to another uplink": func(_, second *testRouteMessageSpec, _ *[]netlink.Message) {
			second.ifIndex = 11
		},
		"link renamed": func(_, _ *testRouteMessageSpec, links *[]netlink.Message) {
			(*links)[0] = testRouteLinkMessage(t, "renamed", "external", 10, unix.IFF_UP, 0, 0, "veth", nil)
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newLinkEffectFixture(t)
			first := testRouteMessageSpec{
				destination: "0.0.0.0/0", gateway: "192.0.2.1", ifIndex: 10, table: unix.RT_TABLE_MAIN,
				protocol: unix.RTPROT_STATIC, scope: unix.RT_SCOPE_UNIVERSE, kind: unix.RTN_UNICAST, metric: 100,
			}
			second := first
			second.metric = 200
			routes := []netlink.Message{testRouteIPv4Message(t, first), testRouteIPv4Message(t, second)}
			links := []netlink.Message{
				testRouteLinkMessage(t, "eth0", "external", 10, unix.IFF_UP, 0, 0, "veth", nil),
				testRouteLinkMessage(t, "eth1", "external", 11, unix.IFF_UP, 0, 0, "veth", nil),
			}
			adapter := testUplinkAdapter(t, &routes, &links)
			selected, err := observeSelectedUplink(t.Context(), adapter, fixture.hostFD, fixture.hostNamespace)
			require.NoError(t, err)
			lease := &uplinkLease{
				host: fixture.lease(fixture.hostFD, fixture.hostNamespace), route: adapter, selected: selected,
			}
			t.Cleanup(func() { require.NoError(t, lease.close(t.Context())) })
			slices.Reverse(routes)
			require.NoError(t, lease.revalidate(t.Context()))
			change(&first, &second, &links)
			routes = []netlink.Message{testRouteIPv4Message(t, first), testRouteIPv4Message(t, second)}
			assert.Error(t, lease.revalidate(t.Context()))
		})
	}
}

// testUplinkAdapter serves route and link dumps through the real decoders without kernel effects.
func testUplinkAdapter(t *testing.T, routes, links *[]netlink.Message) *directRouteNetlink {
	t.Helper()
	return &directRouteNetlink{dial: func(_ int, config *netlink.Config) (routeNetlinkConnection, error) {
		require.NotNil(t, config)
		assert.True(t, config.Strict)
		return &fakeRouteNetlinkConnection{execute: func(message netlink.Message) ([]netlink.Message, error) {
			switch message.Header.Type {
			case netlink.HeaderType(unix.RTM_GETROUTE):
				return *routes, nil
			case netlink.HeaderType(unix.RTM_GETLINK):
				return *links, nil
			default:
				t.Fatalf("unexpected uplink request type %d", message.Header.Type)
				return nil, nil
			}
		}}, nil
	}}
}
