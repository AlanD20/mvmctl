package network

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

// These tests exercise private root/store and bridge invariants without manufacturing a ready
// firewall lease. Integrated flow-before-neighbor success/failure requires genuine retained authority.
func TestFloatingIPInvalidationRequiresRetainedNetworkMutation(t *testing.T) {
	for name, tx := range map[string]*managedNetworkTransaction{
		"missing transaction": nil,
		"missing mutation":    {},
		"empty capability":    {mutation: &authorityfs.NetworkMutation{}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, tx.invalidateFloatingIPNetworkState(t.Context(), floatingIPContractRecord()))
		})
	}
}

func TestFloatingIPInvalidationUsesExactDurableReservation(t *testing.T) {
	tests := map[string]func(*floatingIPRecord){
		"current reservation": nil,
		"stale generation":    func(r *floatingIPRecord) { r.Generation++ },
		"different address":   func(r *floatingIPRecord) { r.IPv4 = "10.10.0.101" },
		"different identity":  func(r *floatingIPRecord) { r.ID = strings.Repeat("c", 32) },
		"different owner":     func(r *floatingIPRecord) { r.OwnerUID++ },
		"different network":   func(r *floatingIPRecord) { r.NetworkID = strings.Repeat("d", 64) },
		"retired":             func(r *floatingIPRecord) { r.Retired = true },
		"zero generation":     func(r *floatingIPRecord) { r.Generation = 0 },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			fixture, store := floatingIPStoreFixture(t)
			record := floatingIPContractRecord()
			require.NoError(t, store.persist(t.Context(), record, nil))
			tx := floatingIPInvalidationStoreTransaction(fixture)
			if change != nil {
				change(&record)
			}
			got, err := tx.floatingIPInvalidationAddress(t.Context(), record)
			if change != nil {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, netip.MustParseAddr("10.10.0.100"), got)
		})
	}
}

func TestFloatingIPInvalidationRejectsUnavailableNetworkOrReservation(t *testing.T) {
	tests := map[string]func(*managedNetworkTransaction){
		"missing network":       func(tx *managedNetworkTransaction) { tx.found = false },
		"removed network":       func(tx *managedNetworkTransaction) { tx.removed = true },
		"cancelled network":     func(tx *managedNetworkTransaction) { tx.cancelled = true },
		"prior boot":            func(tx *managedNetworkTransaction) { tx.priorBoot = &tx.record },
		"legacy schema":         func(tx *managedNetworkTransaction) { tx.record.schemaVersion = 1 },
		"missing configuration": func(tx *managedNetworkTransaction) { tx.record.configuration = nil },
		"root owner differs":    func(tx *managedNetworkTransaction) { tx.record.ownerUID++ },
		"missing reservation":   nil,
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			fixture, _ := floatingIPStoreFixture(t)
			tx := floatingIPInvalidationStoreTransaction(fixture)
			if change != nil {
				change(tx)
			}
			_, err := tx.floatingIPInvalidationAddress(t.Context(), floatingIPContractRecord())
			require.Error(t, err)
		})
	}
}

func floatingIPInvalidationStoreTransaction(fixture *networkStoreFixture) *managedNetworkTransaction {
	record := fixture.record
	record.schemaVersion = managedNetworkSchemaVersion
	record.configuration = &managedNetworkConfiguration{
		l3: contractManagedNetworkL3("10.10.0.0/24", "10.10.0.1"),
	}
	return &managedNetworkTransaction{
		key:    networkKey{ownerUID: OwnerUID(testOwnerUID), networkID: NetworkID(testNetworkID)},
		record: record, found: true, store: fixture.uid,
	}
}

func TestFloatingIPInvalidationBridgeRequiresExactLiveL3WithoutOldVM(t *testing.T) {
	tests := map[string]func(*linkEffectFixture, Topology){
		"old VM absent": nil,
		"bridge missing": func(f *linkEffectFixture, topology Topology) {
			delete(f.route.links[f.hostFD], topology.managedBridge.name)
		},
		"bridge replaced": func(f *linkEffectFixture, topology Topology) {
			f.route.corruptLink(f.hostFD, topology.managedBridge.name, func(link *routeLinkObservation) {
				link.ifIndex++
			})
		},
		"bridge down": func(f *linkEffectFixture, topology Topology) {
			f.route.corruptLink(f.hostFD, topology.managedBridge.name, func(link *routeLinkObservation) {
				link.flags &^= unix.IFF_UP
			})
		},
		"address missing": func(f *linkEffectFixture, topology Topology) {
			delete(f.route.addresses[f.hostFD], topology.managedBridge.ifIndex)
		},
		"namespace differs": func(f *linkEffectFixture, _ Topology) { f.hostNamespace.inode++ },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			fixture, topology, endpoint := newL3EffectFixture(t)
			require.NoError(t, fixture.effect.ConfigureL3Down(t.Context(), fixture.pin, topology, endpoint))
			// The old endpoint is absent; only root-managed host bridge authority remains.
			delete(fixture.route.links, fixture.vmFD)
			delete(fixture.route.links[fixture.hostFD], topology.veth.host.name)
			if change != nil {
				change(fixture, topology)
			}
			tx := &managedNetworkTransaction{
				host: fixture.lease(fixture.hostFD, fixture.hostNamespace), route: fixture.route,
				record: networkAuthorityRecord{
					managedBridge: topology.managedBridge,
					configuration: &managedNetworkConfiguration{
						l3: contractManagedNetworkL3("10.44.0.0/24", "10.44.0.1"),
					},
				},
			}
			fd, bridge, err := tx.inspectFloatingIPInvalidationBridge(t.Context())
			if change != nil {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, fixture.hostFD, fd)
			require.Equal(t, topology.managedBridge.ifIndex, bridge.ifIndex)
		})
	}
}
