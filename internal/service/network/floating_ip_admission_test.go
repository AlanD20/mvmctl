package network

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/authorityfs"
)

// This fixture is root-record data for private invariants, never an admitted VM capability.
func floatingVMAdmissionRecord(t *testing.T) (topologyJournalRecord, networkAuthorityRecord, FloatingIPVMIncarnation) {
	t.Helper()
	_, topology, endpoint := newL3EffectFixture(t)
	network := networkAuthorityRecord{
		schemaVersion: managedNetworkSchemaVersion, ownerUID: testOwnerUID, networkID: NetworkID(testNetworkID),
		bootID: testBootID, managedBridge: topology.managedBridge,
		configuration: &managedNetworkConfiguration{l3: endpoint.network},
	}
	raw, err := encodeNetworkAuthorityRecord(network)
	require.NoError(t, err)
	digest := sha256.Sum256(raw)
	incarnation := FloatingIPVMIncarnation{BootID: testBootID, LaunchGeneration: 9, ExecutionUID: testExecutionUID}
	record := topologyJournalRecord{
		Schema: topologyJournalSchema, Lifecycle: topologyActive, OwnerUID: testOwnerUID,
		NetworkID: testNetworkID, VMID: testVMID, BootID: testBootID, LaunchGeneration: 9,
		NetworkDigest: hex.EncodeToString(digest[:]), Prefix: "10.44.0.0/24", Gateway: "10.44.0.1",
		GuestIPv4: "10.44.0.2", GuestMAC: endpoint.mac.String(), EndpointID: testVMID, Topology: topology.Export(),
		Progress: topologyProgress{Namespace: true, LocalBridge: true, TAP: true, Veth: true, Attached: true, L3: true,
			LoopbackUp: true, BridgeUp: true, TAPUp: true, PeerUp: true, HostUp: true},
	}
	require.NoError(t, validateTopologyJournal(record))
	return record, network, incarnation
}

func TestFloatingIPVMJournalRequiresExactCompletedCurrentIncarnation(t *testing.T) {
	for _, name := range []string{"exact", "other operator", "other VM", "stale boot", "stale incarnation boot",
		"other generation", "other execution UID", "pending", "cleaned", "not fully active", "rebound topology VM"} {
		t.Run(name, func(t *testing.T) {
			record, _, incarnation := floatingVMAdmissionRecord(t)
			owner, vm := uint32(testOwnerUID), VMID(testVMID)
			switch name {
			case "other operator":
				owner++
			case "other VM":
				vm = VMID(strings.Repeat("e", 32))
			case "stale boot":
				record.BootID = "22222222-2222-4222-8222-222222222222"
			case "stale incarnation boot":
				incarnation.BootID = "22222222-2222-4222-8222-222222222222"
			case "other generation":
				incarnation.LaunchGeneration++
			case "other execution UID":
				incarnation.ExecutionUID++
			case "pending":
				record.Pending = "host_up"
			case "cleaned":
				record.Lifecycle = topologyCleaned
				record.Progress = topologyProgress{}
			case "not fully active":
				record.Progress.HostUp = false
			case "rebound topology VM":
				record.Topology.VMID = strings.Repeat("e", 32)
			}
			key, err := admitFloatingIPVMJournal(record, owner, vm, incarnation, testBootID)
			if name != "exact" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			want := networkKey{ownerUID: OwnerUID(testOwnerUID), networkID: NetworkID(testNetworkID)}
			if diff := cmp.Diff(want, key.Network(), cmp.AllowUnexported(networkKey{})); diff != "" {
				t.Errorf("journal-selected network (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFloatingIPVMNetworkResolutionRejectsRootAuthorityDrift(t *testing.T) {
	for _, name := range []string{"exact", "other owner", "other network", "other boot", "legacy schema",
		"missing L3", "digest drift", "prefix drift", "gateway drift", "replaced bridge", "rebound topology veth"} {
		t.Run(name, func(t *testing.T) {
			record, network, incarnation := floatingVMAdmissionRecord(t)
			switch name {
			case "other owner":
				network.ownerUID++
			case "other network":
				network.networkID = NetworkID(strings.Repeat("f", 64))
			case "other boot":
				network.bootID = "22222222-2222-4222-8222-222222222222"
			case "legacy schema":
				network.schemaVersion = 1
			case "missing L3":
				network.configuration = nil
			case "digest drift":
				record.NetworkDigest = strings.Repeat("f", 64)
			case "prefix drift":
				record.Prefix = "10.45.0.0/24"
			case "gateway drift":
				record.Gateway = "10.44.0.3"
			case "replaced bridge":
				network.managedBridge.ifIndex++
			case "rebound topology veth":
				record.Topology.VethPair.Host.Namespace.Inode++
			}
			state := &floatingIPVMLeaseState{
				key: mustTopologyKey(t, testOwnerUID, testVMID, testNetworkID), incarnation: incarnation,
				network: &managedNetworkLease{record: network},
			}
			err := state.resolveNetwork(record)
			if name != "exact" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, testVMID, state.binding.VMID)
			require.Equal(t, testNetworkID, state.binding.NetworkID)
			require.Equal(t, "10.44.0.2", state.binding.IPv4.String())
			if diff := cmp.Diff(record.Topology.VethPair.Host,
				exportLinkIdentity(state.topology.veth.host)); diff != "" {
				t.Errorf("root host veth (-want +got):\n%s", diff)
			}
			require.Equal(t, "10.44.0.0/24", state.binding.Prefix.String())
			require.Equal(t, "10.44.0.1", state.binding.Gateway.String())
		})
	}
}

func TestFloatingIPVMAdmissionRequiresGenuineHold(t *testing.T) {
	server := NewFloatingIPServer()
	incarnation := FloatingIPVMIncarnation{BootID: testBootID, LaunchGeneration: 1, ExecutionUID: testExecutionUID}
	for _, hold := range []*authorityfs.TopologyHold{nil, {}} {
		lease, err := server.OpenVM(t.Context(), hold, incarnation)
		require.Error(t, err)
		require.Nil(t, lease)
	}
	lease, err := server.OpenVM(nil, &authorityfs.TopologyHold{}, incarnation)
	require.Error(t, err)
	require.Nil(t, lease)
}

// Root-only random lock authority follows networkless_test.go. No controller, guest, network link,
// namespace mount, or user database is touched. Positive live-topology admission is a separate gate.
func TestFloatingIPVMAdmissionGenuineHoldRefusalAndRelease(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("genuine Jailer hold requires root authority")
	}
	for _, name := range []string{"stale boot before transfer", "cancel before transfer",
		"missing topology after transfer"} {
		t.Run(name, func(t *testing.T) {
			var randomKey [16]byte
			_, err := rand.Read(randomKey[:])
			require.NoError(t, err)
			key, err := authorityfs.NewVMKey(hex.EncodeToString(randomKey[:]))
			require.NoError(t, err)
			jailer, err := authorityfs.OpenJailer(t.Context())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })
			owner, err := jailer.OpenUID(t.Context(), testOwnerUID)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, owner.Release(context.Background())) })
			vm, err := owner.AcquireVM(t.Context(), key)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, vm.Release(context.Background())) })
			hold, err := vm.RetainTopology(t.Context())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, hold.Release(context.Background())) })
			boot, err := readCurrentBootID(t.Context())
			require.NoError(t, err)
			incarnation := FloatingIPVMIncarnation{BootID: boot, LaunchGeneration: 1, ExecutionUID: testExecutionUID}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "stale boot before transfer" {
				incarnation.BootID = "22222222-2222-4222-8222-222222222222"
				if incarnation.BootID == boot {
					incarnation.BootID = testBootID
				}
			}
			if name == "cancel before transfer" {
				cancel()
			}
			lease, err := NewFloatingIPServer().OpenVM(ctx, hold, incarnation)
			require.Error(t, err)
			require.Nil(t, lease)
			_, _, bindingErr := hold.Binding()
			if name == "missing topology after transfer" {
				require.Error(t, bindingErr, "the input child must have transferred")
				require.NoError(t, vm.Release(context.Background()), "failed admission must drop its transferred child")
				return
			}
			require.NoError(t, bindingErr, "pre-transfer refusal must preserve caller authority")
			require.ErrorIs(t, vm.Release(context.Background()), authorityfs.ErrTopologyRetained)
			require.NoError(t, hold.Release(context.Background()))
			require.NoError(t, vm.Release(context.Background()))
		})
	}
}

func TestFloatingIPVMLeaseCopiedConcurrentReleaseCachesCloseFailure(t *testing.T) {
	fixture := newLinkEffectFixture(t)
	fault := errors.New("host pin close failed")
	fixture.closeError = fault
	network := &managedNetworkLease{host: fixture.lease(fixture.hostFD, fixture.hostNamespace)}
	lease := &FloatingIPVMLease{state: &floatingIPVMLeaseState{network: network}}
	copy := *lease
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() { defer group.Done(); failures <- copy.Release(ctx) }()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		require.ErrorIs(t, err, fault)
	}
	require.Equal(t, 1, fixture.closeCount())
	for _, closeCtx := range fixture.closeCtx {
		require.NoError(t, closeCtx.Err())
	}
	require.ErrorIs(t, lease.Release(t.Context()), fault)
}
