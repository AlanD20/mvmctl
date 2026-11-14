package firewall

import (
	"encoding/json"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostTrafficDormantProjectionRetainsExactEmptyGeneration(t *testing.T) {
	// Rationale: Reboot must retire kernel state without losing logical grants or changing empty-state identity.
	intent := Intent{
		Networks: []NetworkIntent{{
			OwnerUID: 1000, NetworkID: "network", HostBridge: "bridge0",
			Subnet: netip.MustParsePrefix("10.0.0.0/24"), HostIPv4: netip.MustParseAddr("10.0.0.1"),
		}},
		Reservations: []EndpointReservation{{
			OwnerUID: 1000, VMID: "source", NetworkID: "network",
			Prefix: netip.MustParsePrefix("10.0.0.0/24"), Gateway: netip.MustParseAddr("10.0.0.1"),
			IPv4: netip.MustParseAddr("10.0.0.2"), MAC: [6]byte{2, 0, 0, 0, 0, 1},
		}},
		VMHostGrants: []VMHostTrafficGrant{{
			ID: "vm-host", SourceVMID: "source", Incarnation: "11111111-1111-4111-8111-111111111111",
			Protocol: TCP, Ports: PortInterval{First: 5432, Last: 5432},
		}},
		NetworkHostGrants: []NetworkHostTrafficGrant{{
			ID: "network-host", SourceNetworkID: "network", Incarnation: "22222222-2222-4222-8222-222222222222",
			Protocol: UDP, Ports: PortInterval{First: 53, Last: 54},
		}},
	}
	compiled, err := intent.compile()
	require.NoError(t, err)
	assert.True(t, compiled.empty)
	// CONTRACT: Existing canonical v1 empty vector, independently fixed by the original digest grammar.
	assert.Equal(t, "a686883941eb9b92f4c31e6fd9e82861b1f0135f64954041cb890a4f5826f4bb", compiled.id)
	for name, mutate := range map[string]func(*Intent){
		"implicit ports":     func(i *Intent) { i.VMHostGrants[0].Ports = PortInterval{} },
		"foreign network":    func(i *Intent) { i.NetworkHostGrants[0].SourceNetworkID = "foreign" },
		"absent reservation": func(i *Intent) { i.Reservations = nil },
		"absent incarnation": func(i *Intent) { i.VMHostGrants[0].Incarnation = "" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneIntent(intent)
			mutate(&changed)
			_, err := changed.compile()
			assert.Error(t, err, "dormancy must not bypass logical ownership or grammar admission")
		})
	}
}

func TestHostTrafficExtensionPreservesCanonicalExistingJournal(t *testing.T) {
	// Rationale: A canonical root journal written before host grants must remain byte-for-byte admitted.
	const oldIntent = `{"active_networks":null,"reservations":null,"endpoints":null,"networks":null,` +
		`"vm_grants":null,"network_grants":null,"host_services":null}`
	raw := []byte(`{"schema_version":1,"boot_id":"11111111-1111-4111-8111-111111111111",` +
		`"host_namespace":{"device":1,"inode":2},"before":` + oldIntent + `,"target":` + oldIntent + `,"prior_boot":null}`)
	journal, err := decodeIntentJournal(raw)
	require.NoError(t, err)
	encoded, err := json.Marshal(journal)
	require.NoError(t, err)
	assert.Equal(t, string(raw), string(encoded))
}
