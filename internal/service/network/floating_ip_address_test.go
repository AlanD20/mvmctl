package network

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFloatingIPv4SelectionExcludesGatewayAndAllReservations(t *testing.T) {
	cases := map[string]struct {
		prefix    string
		gateway   string
		requested string
		reserved  []string
		want      string
	}{
		"automatic skips primary and floating": {
			prefix:   "10.10.0.0/24",
			gateway:  "10.10.0.1",
			reserved: []string{"10.10.0.2", "10.10.0.3"},
			want:     "10.10.0.4",
		},
		"arbitrary explicit secondary": {
			prefix: "10.10.0.0/24", gateway: "10.10.0.1", requested: "10.10.0.179", want: "10.10.0.179",
		},
		"custom gateway": {
			prefix: "192.168.1.0/24", gateway: "192.168.1.2", reserved: []string{"192.168.1.1"}, want: "192.168.1.3",
		},
		"smallest supported network": {
			prefix: "172.31.255.252/30", gateway: "172.31.255.254", want: "172.31.255.253",
		},
		"large subnet first gap": {
			prefix: "10.0.0.0/8", gateway: "10.0.0.1", reserved: []string{"10.255.255.254"}, want: "10.0.0.2",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gateway := netip.MustParseAddr(tc.gateway)
			network, err := admitManagedNetworkL3(managedNetworkL3Input{
				prefix: netip.MustParsePrefix(tc.prefix), gateway: &gateway,
			})
			require.NoError(t, err)
			var reserved []netip.Addr
			for _, address := range tc.reserved {
				reserved = append(reserved, netip.MustParseAddr(address))
			}
			got, err := selectFloatingIPv4(network, tc.requested, reserved)
			require.NoError(t, err)
			if got.value.String() != tc.want {
				t.Errorf("selected address = %s, want %s", got.value, tc.want)
			}
		})
	}
}

func TestFloatingIPv4SelectionRefusesInvalidAndUnavailableAddresses(t *testing.T) {
	cases := map[string]struct {
		requested string
		reserved  []netip.Addr
	}{
		"network":            {requested: "10.0.0.0"},
		"broadcast":          {requested: "10.0.0.3"},
		"gateway":            {requested: "10.0.0.1"},
		"outside subnet":     {requested: "10.0.0.4"},
		"IPv6":               {requested: "::1"},
		"mapped IPv4":        {requested: "::ffff:10.0.0.2"},
		"leading whitespace": {requested: " 10.0.0.2"},
		"primary collision": {
			requested: "10.0.0.2",
			reserved:  []netip.Addr{netip.MustParseAddr("10.0.0.2")},
		},
		"exhausted network":                  {reserved: []netip.Addr{netip.MustParseAddr("10.0.0.2")}},
		"corrupt reservation outside subnet": {reserved: []netip.Addr{netip.MustParseAddr("10.0.0.4")}},
		"corrupt reservation gateway":        {reserved: []netip.Addr{netip.MustParseAddr("10.0.0.1")}},
		"corrupt reservation zero":           {reserved: []netip.Addr{{}}},
		"explicit request with corrupt reservation": {
			requested: "10.0.0.2", reserved: []netip.Addr{netip.MustParseAddr("10.0.0.4")},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			network, err := admitManagedNetworkL3(managedNetworkL3Input{prefix: netip.MustParsePrefix("10.0.0.0/30")})
			require.NoError(t, err)
			_, err = selectFloatingIPv4(network, tc.requested, tc.reserved)
			require.Error(t, err)
		})
	}
}

func TestFloatingIPv4SelectionRefusesInconsistentNetworkAuthority(t *testing.T) {
	network, err := admitManagedNetworkL3(managedNetworkL3Input{prefix: netip.MustParsePrefix("10.0.0.0/24")})
	require.NoError(t, err)
	network.gateway.value = netip.MustParseAddr("10.1.0.1")
	_, err = selectFloatingIPv4(network, "10.0.0.2", nil)
	require.Error(t, err, "a canonical requested address cannot repair inconsistent root network authority")
}
