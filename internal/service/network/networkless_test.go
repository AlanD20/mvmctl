package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

// Rationale: rejecting a foreign VM leaf must not strand the transferred root VM hold. The foreign leaf is
// deliberately created through the real authority tree and remains untouched by the recovery attempt.
func TestNetworklessRejectedRecoveryReleasesHoldWithoutDeletingForeignLeaf(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root authority recovery requires root")
	}
	ctx := t.Context()
	var randomKey [16]byte
	_, err := rand.Read(randomKey[:])
	require.NoError(t, err)
	vmKey, err := authorityfs.NewVMKey(hex.EncodeToString(randomKey[:]))
	require.NoError(t, err)

	jailer, err := authorityfs.OpenJailer(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, jailer.Release(context.Background())) })
	owner, err := jailer.OpenUID(ctx, 1000)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Release(context.Background())) })
	vm, err := owner.AcquireVM(ctx, vmKey)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, vm.Release(context.Background())) })
	hold, err := vm.RetainTopology(ctx)
	require.NoError(t, err)

	networkRoot, err := authorityfs.OpenNetwork(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, networkRoot.Release(context.Background())) })
	networkOwner, err := networkRoot.OpenUID(ctx, 1000)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, networkOwner.Release(context.Background())) })
	foreign, err := networkOwner.CreateNamespaceHandle(ctx, vmKey)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, foreign.Remove(context.Background()))
		require.NoError(t, foreign.Release(context.Background()))
	})

	lease, err := RecoverNetworkless(ctx, hold, 1)
	require.Error(t, err)
	require.Nil(t, lease, "rejected recovery must close its retained authority before returning")
	_, err = foreign.TargetIdentity(ctx)
	require.NoError(t, err, "recovery must not remove the foreign leaf")
	require.NoError(t, vm.Release(ctx), "rejected recovery must release the VM topology hold")
}

func TestNetworklessJournalRejectsAmbiguousAuthority(t *testing.T) {
	const vmID = "0123456789abcdef0123456789abcdef"
	base := networklessJournalRecord{
		Schema: networklessJournalSchema, Lifecycle: networklessActive, OwnerUID: 1000, VMID: vmID,
		BootID: "00000000-0000-4000-8000-000000000000", LaunchGeneration: 7, LoopbackUp: true,
		Token: "0100000000000000000000000000000000000000000000000000000000000000", NamespaceMountID: 31,
		Namespace: NamespaceHandle{Path: "/run/mvmctl/authority/1000/netns/" + vmID,
			Identity: NamespaceIdentity{Device: 41, Inode: 42}, OwnerUID: 0, OwnerGID: 0, Mode: namespaceHandleMode},
		Target: topologyNamespaceTarget{DirectoryDevice: 11, DirectoryInode: 12,
			DirectoryMountKind: uint8(namespaceTargetMountIdentityUnique), DirectoryMount: 13,
			PlaceholderDevice: 21, PlaceholderInode: 22,
			PlaceholderMountKind: uint8(namespaceTargetMountIdentityUnique), PlaceholderMount: 23},
	}
	tests := map[string]struct {
		change func(*networklessJournalRecord)
		valid  bool
	}{
		"active_exact_identity":   {valid: true},
		"active_without_loopback": {change: func(record *networklessJournalRecord) { record.LoopbackUp = false }},
		"foreign_namespace_path": {change: func(record *networklessJournalRecord) {
			record.Namespace.Path = "/run/mvmctl/authority/1001/netns/" + vmID
		}},
		"partial_target_identity": {
			change: func(record *networklessJournalRecord) { record.Target.PlaceholderInode = 0 },
		},
		"unknown_stage": {change: func(record *networklessJournalRecord) { record.Pending = "adopt" }},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			record := base
			if tc.change != nil {
				tc.change(&record)
			}
			raw, err := json.Marshal(record)
			require.NoError(t, err)
			got, err := decodeNetworklessJournal(raw)
			if !tc.valid {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(record, got); diff != "" {
				t.Errorf("durable networkless identity mismatch (-want +got):\n%s", diff)
			}
		})
	}
	// A managed-network field is not a harmless extension of this distinct root authority.
	raw, err := json.Marshal(base)
	require.NoError(t, err)
	raw = append(raw[:len(raw)-1], []byte(`,"network_id":"fake"}`)...)
	_, err = decodeNetworklessJournal(raw)
	assert.Error(t, err)
}

func TestNetworklessIPv6RouteRejectsForeignGateway(t *testing.T) {
	const loIndex = 1
	for name, tc := range map[string]struct {
		scope          uint8
		foreignGateway bool
	}{
		"host_scope_loopback":   {scope: unix.RT_SCOPE_HOST},
		"kernel_scope_loopback": {scope: unix.RT_SCOPE_UNIVERSE},
		"foreign_gateway":       {scope: unix.RT_SCOPE_UNIVERSE, foreignGateway: true},
	} {
		t.Run(name, func(t *testing.T) {
			data := make([]byte, unix.SizeofRtMsg)
			data[0], data[1], data[4] = unix.AF_INET6, 128, unix.RT_TABLE_LOCAL
			data[5], data[6], data[7] = unix.RTPROT_KERNEL, tc.scope, unix.RTN_LOCAL
			loopback := make([]byte, 16)
			loopback[15] = 1
			var err error
			data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
				encoder.Bytes(unix.RTA_DST, loopback)
				encoder.Uint32(unix.RTA_OIF, loIndex)
				if tc.foreignGateway {
					encoder.Bytes(unix.RTA_GATEWAY, loopback)
				}
				return nil
			})
			require.NoError(t, err)
			message := netlink.Message{Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_NEWROUTE)}, Data: data}
			err = inspectNetworklessIPv6Route(message, loIndex)
			if tc.foreignGateway {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestNetworklessIPv6AddressAdmitsOnlyKernelLoopbackProtocol(t *testing.T) {
	loopback := make([]byte, 16)
	loopback[15] = 1
	for name, tc := range map[string]struct {
		protocol []byte
		valid    bool
	}{
		"legacy_without_protocol": {valid: true},
		"kernel_loopback":         {protocol: []byte{1}, valid: true},
		"router_advertisement":    {protocol: []byte{2}},
		"malformed_protocol":      {protocol: []byte{1, 0}},
	} {
		t.Run(name, func(t *testing.T) {
			data := make([]byte, unix.SizeofIfAddrmsg)
			data[0], data[1], data[3] = unix.AF_INET6, 128, unix.RT_SCOPE_HOST
			var err error
			data, err = appendRouteAttributes(data, func(encoder *netlink.AttributeEncoder) error {
				encoder.Bytes(unix.IFA_ADDRESS, loopback)
				if tc.protocol != nil {
					encoder.Bytes(networklessIFAProto, tc.protocol)
				}
				return nil
			})
			require.NoError(t, err)
			message := netlink.Message{Header: netlink.Header{Type: netlink.HeaderType(unix.RTM_NEWADDR)}, Data: data}
			err = inspectNetworklessIPv6Address(message)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
