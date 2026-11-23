package firewall

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/nftables"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// Private invariant/effect tests do not manufacture a ready global mutation capability. Actual root
// transaction composition and timed packet qualification remain separate integration gates.
func floatingLeaseTestState(t *testing.T) *mutationLeaseState {
	t.Helper()
	intent := secondaryDurableIntent()
	compiled, err := intent.compile()
	require.NoError(t, err)
	return &mutationLeaseState{intent: intent, compiled: compiled, binding: intent.Endpoints[0]}
}

func ownerlessFloatingLease(binding FloatingIPBinding) FloatingIPBinding {
	binding.OwnerVMID, binding.OwnerLaunchGeneration, binding.LeaseDuration, binding.Lifetime = "", 0, 0, ""
	return binding
}

func TestFloatingIPFenceIntentRefusesIdentityReboundAndRollback(t *testing.T) {
	for name, change := range map[string]func(*FloatingIPBinding){
		"same generation recovery": nil,
		"advance generation":       func(b *FloatingIPBinding) { b.Generation++ },
		"rollback":                 func(b *FloatingIPBinding) { b.Generation-- },
		"zero generation":          func(b *FloatingIPBinding) { b.Generation = 0 },
		"rebound address":          func(b *FloatingIPBinding) { b.IPv4 = netip.MustParseAddr("10.0.0.101") },
		"other operator":           func(b *FloatingIPBinding) { b.OwnerUID++ },
		"other network":            func(b *FloatingIPBinding) { b.NetworkID = strings.Repeat("c", 64) },
		"retained owner":           func(b *FloatingIPBinding) { b.OwnerVMID = strings.Repeat("1", 32) },
		"retained incarnation":     func(b *FloatingIPBinding) { b.OwnerLaunchGeneration = 9 },
		"retained duration":        func(b *FloatingIPBinding) { b.LeaseDuration = time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			state := floatingLeaseTestState(t)
			before := cloneIntent(state.intent)
			binding := ownerlessFloatingLease(state.intent.FloatingIPs[0])
			if change != nil {
				change(&binding)
			}
			target, err := state.floatingIPOwnershipIntent(binding, false)
			if name != "same generation recovery" && name != "advance generation" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(binding, target.FloatingIPs[0], cmpopts.EquateComparable(netip.Addr{})); diff != "" {
				t.Errorf("fence binding (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(before, state.intent,
				cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{})); diff != "" {
				t.Errorf("admitted intent mutated (-want +got):\n%s", diff)
			}
			compiled, err := target.compile()
			require.NoError(t, err)
			require.Empty(t, compiled.bridge.secondaryOwners, "fence must remove owner keys")
		})
	}
	t.Run("initial fence", func(t *testing.T) {
		state := floatingLeaseTestState(t)
		binding := ownerlessFloatingLease(state.intent.FloatingIPs[0])
		state.intent.FloatingIPs = nil
		target, err := state.floatingIPOwnershipIntent(binding, false)
		require.NoError(t, err)
		if diff := cmp.Diff([]FloatingIPBinding{binding}, target.FloatingIPs,
			cmpopts.EquateComparable(netip.Addr{})); diff != "" {
			t.Errorf("initial fence (-want +got):\n%s", diff)
		}
	})
}

func TestFloatingIPPublishRequiresExactFenceAndNeverMintsKey(t *testing.T) {
	for _, name := range []string{"publish", "missing fence", "old generation", "already owned", "stopped owner",
		"missing incarnation", "zero duration", "fractional duration", "different address", "unknown owner"} {
		t.Run(name, func(t *testing.T) {
			state := floatingLeaseTestState(t)
			binding := state.intent.FloatingIPs[0]
			state.intent.FloatingIPs[0] = ownerlessFloatingLease(binding)
			switch name {
			case "missing fence":
				state.intent.FloatingIPs = nil
			case "old generation":
				binding.Generation--
			case "already owned":
				state.intent.FloatingIPs[0] = binding
			case "stopped owner":
				state.intent.Endpoints = nil
			case "missing incarnation":
				binding.OwnerLaunchGeneration = 0
			case "zero duration":
				binding.LeaseDuration = 0
			case "fractional duration":
				binding.LeaseDuration += time.Millisecond
			case "different address":
				binding.IPv4 = netip.MustParseAddr("10.0.0.101")
			case "unknown owner":
				binding.OwnerVMID = strings.Repeat("9", 32)
			}
			target, err := state.floatingIPOwnershipIntent(binding, true)
			if name != "publish" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			before, err := state.intent.compile()
			require.NoError(t, err)
			after, err := target.compile()
			require.NoError(t, err)
			if diff := cmp.Diff(secondaryLiveKey(), after.bridge.secondaryOwners[binding.ID]); diff != "" {
				t.Errorf("owner key (-want +got):\n%s", diff)
			}
			connection := &recordingKernelApplicationConnection{}
			require.NoError(t, queueKernelApplicationTable(connection, after.bridge, before.bridge, nil))
			for _, event := range connection.events {
				require.False(t, strings.Contains(event, "secondary key"),
					"metadata publication minted a lease: %s", event)
			}
		})
	}
}

func TestFloatingIPLeaseAuthorityRequiresExactGraphScopeGenerationAndOwner(t *testing.T) {
	for _, name := range []string{"absent", "live", "zero remaining", "wrong generation", "zero generation", "missing",
		"other operator", "other network", "stopped", "foreign key", "wrong timeout",
		"remaining overflow", "foreign graph"} {
		t.Run(name, func(t *testing.T) {
			state := floatingLeaseTestState(t)
			resource := state.intent.FloatingIPs[0]
			observed := kernelObservationFromCompiled(state.compiled)
			id, generation := resource.ID, resource.Generation
			element := secondaryLeaseElement{
				key:       secondaryLiveKey(),
				lifetime:  FloatingIPFinite,
				timeout:   5 * time.Second,
				remaining: time.Second,
			}
			switch name {
			case "live":
				observed.tables[0].secondaryElements = []secondaryLeaseElement{element}
			case "zero remaining":
				element.remaining = 0
				observed.tables[0].secondaryElements = []secondaryLeaseElement{element}
			case "wrong generation":
				generation++
			case "zero generation":
				generation = 0
			case "missing":
				id = strings.Repeat("8", 32)
			case "other operator":
				state.binding.OwnerUID++
			case "other network":
				state.binding.NetworkID = strings.Repeat("d", 64)
			case "stopped":
				delete(state.compiled.bridge.secondaryOwners, id)
			case "foreign key":
				element.key[0] = 'x'
				observed.tables[0].secondaryElements = []secondaryLeaseElement{element}
			case "wrong timeout":
				element.timeout++
				observed.tables[0].secondaryElements = []secondaryLeaseElement{element}
			case "remaining overflow":
				element.remaining = 6 * time.Second
				observed.tables[0].secondaryElements = []secondaryLeaseElement{element}
			case "foreign graph":
				observed.generationID = "foreign"
			}
			got, present, err := state.floatingIPLeaseAuthority(id, generation, observed)
			if name != "absent" && name != "live" && name != "zero remaining" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(resource, got, cmpopts.EquateComparable(netip.Addr{})); diff != "" {
				t.Errorf("resource authority (-want +got):\n%s", diff)
			}
			require.Equal(t, name != "absent", present)
			err = state.verifyFloatingIPLeaseInstalled(id, generation, observed)
			if name == "live" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func floatingLeaseEffectNamespace(state *mutationLeaseState, connection kernelApplicationConnection) {
	identity := generationNamespaceIdentity{device: 41, inode: 73}
	fixture := newKernelApplicationNamespaceFixture(identity, connection)
	state.network = &productionGenerationNetworkAuthority{kernel: &kernelObservationNamespaceLease{
		state: &kernelObservationNamespaceLeaseState{
			deps: fixture.observationDeps(), applicationDeps: fixture.applicationDeps(),
			fd: 20, identity: identity, active: true,
		},
	}}
}

func TestFloatingIPLeaseElementQueuesOnlyExactKeyAndConfiguredTimeout(t *testing.T) {
	for _, insert := range []bool{false, true} {
		t.Run(fmt.Sprintf("insert=%v", insert), func(t *testing.T) {
			state := floatingLeaseTestState(t)
			connection := &recordingKernelApplicationConnection{}
			floatingLeaseEffectNamespace(state, connection)
			resource := state.intent.FloatingIPs[0]
			require.NoError(t, state.mutateFloatingIPLeaseElement(t.Context(), resource, insert))
			operation := fmt.Sprintf("delete secondary key secondary_ipv4/%x", secondaryLiveKey())
			if insert {
				operation = fmt.Sprintf("add secondary key secondary_ipv4/%x timeout=5s", secondaryLiveKey())
			}
			if diff := cmp.Diff([]string{operation, "flush", "close"}, connection.events); diff != "" {
				t.Errorf("element batch (-want +got):\n%s", diff)
			}
			require.Equal(t, 1, connection.flushCalls)
			require.Equal(t, 1, connection.closeCalls)
			require.True(t, state.uncertain, "a flush alone must not authorize handover before readback")
		})
	}
}

func TestFloatingIPLeaseElementRefusesUnadmittedPolicyBeforeEffects(t *testing.T) {
	for _, name := range []string{"timeout", "generation", "address", "owner", "key"} {
		t.Run(name, func(t *testing.T) {
			state := floatingLeaseTestState(t)
			resource := state.intent.FloatingIPs[0]
			switch name {
			case "timeout":
				resource.LeaseDuration += time.Second
			case "generation":
				resource.Generation++
			case "address":
				resource.IPv4 = netip.MustParseAddr("10.0.0.101")
			case "owner":
				resource.OwnerVMID = strings.Repeat("9", 32)
			case "key":
				delete(state.compiled.bridge.secondaryOwners, resource.ID)
			}
			require.Error(t, state.mutateFloatingIPLeaseElement(t.Context(), resource, true))
		})
	}
}

func TestFloatingIPLeaseElementPreservesUnknownOutcomeAndCancels(t *testing.T) {
	for _, name := range []string{"flush failed", "close failed", "cancel in flush"} {
		t.Run(name, func(t *testing.T) {
			state := floatingLeaseTestState(t)
			connection := &recordingKernelApplicationConnection{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch name {
			case "flush failed":
				connection.flushErr = errors.New("unknown commit")
			case "close failed":
				connection.closeErr = errors.New("close failed")
			case "cancel in flush":
				connection = newBlockingKernelApplicationConnection()
			}
			floatingLeaseEffectNamespace(state, connection)
			if name == "cancel in flush" {
				go func() { <-connection.flushStarted; cancel() }()
			}
			err := state.mutateFloatingIPLeaseElement(ctx, state.intent.FloatingIPs[0], true)
			require.Error(t, err)
			require.True(t, state.uncertain)
			require.Equal(t, 1, connection.flushCalls, "never retry an ambiguous insert")
			require.Equal(t, 1, connection.closeCalls)
			if name == "cancel in flush" {
				require.ErrorIs(t, err, context.Canceled)
				if diff := cmp.Diff([]string{"flush", "interrupt", "close"}, connection.commitEvents); diff != "" {
					t.Errorf("cancel effects (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestFloatingIPLeasePublicMethodsRefuseMissingAuthority(t *testing.T) {
	var lease *MutationLease
	binding := secondaryDurableIntent().FloatingIPs[0]
	require.Error(t, lease.FenceFloatingIP(t.Context(), ownerlessFloatingLease(binding)))
	require.Error(t, lease.PublishFloatingIPOwner(t.Context(), binding))
	require.Error(t, lease.ActivateFloatingIPLease(t.Context(), binding.ID, binding.Generation))
	require.Error(t, lease.RenewFloatingIPLease(t.Context(), binding.ID, binding.Generation))
	require.Error(t, lease.ActivateFloatingIPOwnership(t.Context(), binding.ID, binding.Generation))
	lease = &MutationLease{state: &mutationLeaseState{released: true}}
	require.Error(t, lease.ActivateFloatingIPLease(t.Context(), binding.ID, binding.Generation))
}

// Decode the actual pinned google/nftables wire batch: insertion and revocation each emit exactly
// one element command, never a rule/table replacement or a refresh of another resource.
func TestFloatingIPLeaseTypedAdapterUsesExactElementWire(t *testing.T) {
	for _, tc := range []struct{ insert, persistent bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		insert := tc.insert
		t.Run(fmt.Sprintf("insert=%v/persistent=%v", insert, tc.persistent), func(t *testing.T) {
			state := floatingLeaseTestState(t)
			if tc.persistent {
				state = persistentFloatingLeaseTestState(t)
			}
			other := state.intent.FloatingIPs[0]
			other.ID, other.IPv4 = strings.Repeat("5", 32), netip.MustParseAddr("10.0.0.101")
			other.Lifetime, other.LeaseDuration = FloatingIPFinite, 11*time.Second
			state.intent.FloatingIPs = append(state.intent.FloatingIPs, other)
			compiled, err := state.intent.compile()
			require.NoError(t, err)
			state.compiled = compiled
			var batches [][]netlink.Message
			acks := 0
			underlying, err := nftables.New(nftables.AsLasting(), nftables.WithTestDial(
				func(request []netlink.Message) ([]netlink.Message, error) {
					if len(request) != 0 {
						batches = append(batches, slices.Clone(request))
						acks = len(request) - 2
						return nil, nil
					}
					if acks == 0 {
						return nil, errors.New("unexpected receive")
					}
					acks--
					return []netlink.Message{{Header: netlink.Header{Type: netlink.Error}, Data: make([]byte, 4)}}, nil
				}))
			require.NoError(t, err)
			floatingLeaseEffectNamespace(state, &googleNFTablesApplicationConnection{connection: underlying})
			require.NoError(t, state.mutateFloatingIPLeaseElement(t.Context(), state.intent.FloatingIPs[0], insert))
			require.Len(t, batches, 1)
			require.Len(t, batches[0], 3)
			command := batches[0][1]
			wantType := unix.NFT_MSG_DELSETELEM
			if insert {
				wantType = unix.NFT_MSG_NEWSETELEM
			}
			require.Equal(t, kernelApplicationNetlinkMessageType(wantType), command.Header.Type)
			require.Equal(t, byte(nftables.TableFamilyBridge), command.Data[0])
			attrs, err := netlink.UnmarshalAttributes(command.Data[4:])
			require.NoError(t, err)
			for _, attr := range attrs {
				if attr.Type&0x3fff == unix.NFTA_SET_ELEM_LIST_SET_ID {
					require.Len(t, attr.Data, 4)
					require.Zero(t, binary.BigEndian.Uint32(attr.Data), "retained set used a creation-batch ID")
				}
			}
			require.Equal(t, uint32(1), state.compiled.bridge.secondarySet.ID, "compiled metadata must be retained")
			require.Equal(t, "secondary_ipv4",
				kernelApplicationProtocolAttribute(t, attrs, unix.NFTA_SET_ELEM_LIST_SET))
			require.Equal(t, state.compiled.bridge.table.Name,
				kernelApplicationProtocolAttribute(t, attrs, unix.NFTA_SET_ELEM_LIST_TABLE))
			list, err := netlink.UnmarshalAttributes(
				floatingLeaseWireAttribute(t, attrs, unix.NFTA_SET_ELEM_LIST_ELEMENTS))
			require.NoError(t, err)
			require.Len(t, list, 1)
			element, err := netlink.UnmarshalAttributes(list[0].Data)
			require.NoError(t, err)
			key, err := netlink.UnmarshalAttributes(floatingLeaseWireAttribute(t, element, unix.NFTA_SET_ELEM_KEY))
			require.NoError(t, err)
			expected := secondaryLiveKey()
			if diff := cmp.Diff(expected[:], floatingLeaseWireAttribute(t, key, unix.NFTA_DATA_VALUE)); diff != "" {
				t.Errorf("wire key (-want +got):\n%s", diff)
			}
			if insert && !tc.persistent {
				raw := floatingLeaseWireAttribute(t, element, unix.NFTA_SET_ELEM_TIMEOUT)
				require.Equal(t, uint64(5000), binary.BigEndian.Uint64(raw))
			} else {
				for _, attr := range element {
					require.NotEqual(t, uint16(unix.NFTA_SET_ELEM_TIMEOUT), attr.Type&0x3fff)
					require.NotEqual(t, uint16(unix.NFTA_SET_ELEM_EXPIRATION), attr.Type&0x3fff)
				}
			}
		})
	}
}

func floatingLeaseWireAttribute(t *testing.T, attrs []netlink.Attribute, kind uint16) []byte {
	t.Helper()
	for _, attr := range attrs {
		if attr.Type&0x3fff == kind {
			return attr.Data
		}
	}
	t.Fatalf("attribute %d missing", kind)
	return nil
}
