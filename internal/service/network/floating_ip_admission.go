package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"sync"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/service/firewall"
)

// FloatingIPVMIncarnation comes only from retained root Jailer admission, never caller JSON.
// The Jailer parent remains owned by the caller until this lease has been released.
type FloatingIPVMIncarnation struct {
	BootID           string
	LaunchGeneration uint64
	ExecutionUID     uint32
}

// FloatingIPVMLease retains target-VM, network, namespace and global firewall admission.
// Copies share release state. It exposes no raw transaction or caller-selected network authority.
type FloatingIPVMLease struct{ state *floatingIPVMLeaseState }

type floatingIPVMLeaseState struct {
	// mu serializes retained capabilities and cached close-once release across copies.
	mu          sync.Mutex
	hold        *authorityfs.TopologyHold
	key         topologyKey
	incarnation FloatingIPVMIncarnation
	journal     *topologyJournal
	network     *managedNetworkLease
	pin         *namespacePin
	firewall    *firewall.MutationLease
	topology    Topology
	l3          endpointL3
	binding     firewall.EndpointBinding
	released    bool
	releaseErr  error
}

// OpenVM consumes one genuine child hold after checking root incarnation input. The caller keeps
// its Jailer parent alive until Release returns. Every rejected admission releases acquired children;
// no namespace, link, endpoint or durable topology lifecycle is removed or repaired.
func (server *FloatingIPServer) OpenVM(
	ctx context.Context, hold *authorityfs.TopologyHold, incarnation FloatingIPVMIncarnation,
) (_ *FloatingIPVMLease, returnErr error) {
	if ctx == nil || hold == nil || !bootIDPattern.MatchString(incarnation.BootID) ||
		incarnation.LaunchGeneration == 0 || incarnation.ExecutionUID == 0 {
		return nil, networkAuthorityValidationError("floating-IP VM admission requires root incarnation and hold")
	}
	owner, vm, err := hold.Binding()
	if err != nil {
		return nil, err
	}
	boot, err := readCurrentBootID(ctx)
	if err != nil {
		return nil, err
	}
	if boot != incarnation.BootID || owner == incarnation.ExecutionUID {
		return nil, networkAuthorityCorruptError("floating-IP VM root incarnation is not current and isolated", nil)
	}
	consumed, err := hold.Transfer(ctx)
	if err != nil {
		return nil, err
	}
	state := &floatingIPVMLeaseState{hold: consumed, incarnation: incarnation}
	lease := &FloatingIPVMLease{state: state}
	defer func() {
		if returnErr != nil {
			returnErr = appendNetworkAuthorityError(returnErr, "release rejected floating-IP VM admission",
				lease.Release(context.WithoutCancel(ctx)))
		}
	}()
	state.journal, err = openOwnedTopologyJournal(ctx, owner, VMID(vm.String()))
	if err != nil {
		return nil, err
	}
	record, found, err := state.journal.read(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, networkAuthorityCorruptError("floating-IP VM has no root topology journal", nil)
	}
	state.key, err = admitFloatingIPVMJournal(record, owner, VMID(vm.String()), incarnation, boot)
	if err != nil {
		return nil, err
	}
	admittedRaw := bytes.Clone(state.journal.raw)
	// The genuine VM hold is already retained. Acquire only the later network suffix, never index/VM again.
	locked, err := (managedNetworkLocker{}).LockNetwork(ctx, state.key.Network())
	if err != nil {
		return nil, err
	}
	var ok bool
	state.network, ok = locked.(*managedNetworkLease)
	if !ok {
		return nil, errors.Join(networkAuthorityCorruptError("floating-IP network lock is not concrete", nil),
			locked.Release(context.WithoutCancel(ctx)))
	}
	if err := state.resolveNetwork(record); err != nil {
		return nil, err
	}
	state.firewall, err = firewall.BeginVMEndpoint(ctx, state.network.mutation, state.binding)
	if err != nil {
		return nil, err
	}
	effects := newTopologyEffect()
	namespaceKey, err := newNamespaceKey(owner, vm.String())
	if err != nil {
		return nil, err
	}
	state.pin, err = effects.namespace.(*namespaceEffect).recoverNamespace(ctx, namespaceKey,
		state.topology.handle, namespaceTargetFromJournal(record.NamespaceTarget), consumed)
	if err != nil {
		return nil, err
	}
	if state.pin.Handle() != state.topology.handle {
		return nil, networkAuthorityCorruptError(
			"floating-IP target namespace is not the exact live journal namespace", nil)
	}
	// This read-only view borrows capabilities. It never owns/release them or invokes primary cleanup.
	inspection := &VMTopologyLease{
		effects: effects, preparation: &topologyPreparation{pin: state.pin, topology: state.topology},
		journal: state.journal, networkRecord: state.network.record,
	}
	if err := inspection.verifyActivationLinks(ctx); err != nil {
		return nil, err
	}
	if state.network.uplink != nil {
		if err := state.network.uplink.revalidate(ctx); err != nil {
			return nil, err
		}
	}
	if err := state.firewall.VerifyExistingVMEndpoint(ctx, state.binding); err != nil {
		return nil, err
	}
	current, found, err := state.journal.read(ctx)
	if err != nil || !found || !bytes.Equal(admittedRaw, state.journal.raw) {
		return nil, networkAuthorityCorruptError("floating-IP target topology changed during admission", err)
	}
	if _, err := admitFloatingIPVMJournal(current, owner, VMID(vm.String()), incarnation, boot); err != nil {
		return nil, err
	}
	boundOwner, boundVM, err := consumed.Binding()
	if err != nil || boundOwner != owner || boundVM != vm {
		return nil, networkAuthorityCorruptError("floating-IP target VM hold changed during admission", err)
	}
	currentBoot, err := readCurrentBootID(ctx)
	if err != nil || currentBoot != boot {
		return nil, networkAuthorityCorruptError("floating-IP target boot changed during admission", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, networkAuthorityEffectError("complete floating-IP VM admission", err)
	}
	return lease, nil
}

func admitFloatingIPVMJournal(
	record topologyJournalRecord, owner uint32, vm VMID, incarnation FloatingIPVMIncarnation, boot string,
) (topologyKey, error) {
	if err := validateTopologyJournal(record); err != nil {
		return topologyKey{}, err
	}
	complete := topologyProgress{Namespace: true, LocalBridge: true, TAP: true, Veth: true, Attached: true,
		L3: true, LoopbackUp: true, BridgeUp: true, TAPUp: true, PeerUp: true, HostUp: true}
	if record.OwnerUID != owner || record.VMID != string(vm) || record.BootID != boot ||
		incarnation.BootID != boot || record.LaunchGeneration != incarnation.LaunchGeneration ||
		record.Topology.TAPExecutionUID != incarnation.ExecutionUID ||
		record.Lifecycle != topologyActive || record.Pending != "" || record.Progress != complete {
		return topologyKey{}, networkAuthorityCorruptError(
			"floating-IP target topology is not the exact active incarnation", nil)
	}
	return newTopologyKey(owner, string(vm), record.NetworkID)
}

func (state *floatingIPVMLeaseState) resolveNetwork(record topologyJournalRecord) error {
	network := state.network.record
	if network.schemaVersion != managedNetworkSchemaVersion || network.configuration == nil ||
		network.ownerUID != uint32(state.key.ownerUID) || network.networkID != state.key.networkID ||
		network.bootID != state.incarnation.BootID ||
		record.Topology.ManagedBridge != exportLinkIdentity(network.managedBridge) {
		return networkAuthorityCorruptError("floating-IP topology differs from retained root network identity", nil)
	}
	raw, err := encodeNetworkAuthorityRecord(network)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if record.NetworkDigest != hex.EncodeToString(digest[:]) ||
		record.Prefix != network.configuration.l3.prefix.value.String() ||
		record.Gateway != network.configuration.l3.gateway.value.String() {
		return networkAuthorityCorruptError("floating-IP topology differs from retained root network L3", nil)
	}
	state.topology, err = recoverTopologyValue(state.key, record.Topology, record.Progress)
	if err != nil {
		return err
	}
	guest, err := netip.ParseAddr(record.GuestIPv4)
	if err != nil {
		return err
	}
	state.l3, err = deriveEndpointL3(state.key, network.configuration.l3,
		endpointL3Input{guestAddress: guest, requestedMAC: &record.GuestMAC})
	if err != nil {
		return err
	}
	state.binding = firewall.EndpointBinding{
		OwnerUID: uint32(state.key.ownerUID), VMID: string(state.key.vmID), NetworkID: string(state.key.networkID),
		BootID: network.bootID, HostNamespace: firewall.NamespaceIdentity{
			Device: network.managedBridge.namespace.device, Inode: network.managedBridge.namespace.inode,
		},
		Prefix: state.l3.network.prefix.value, Gateway: state.l3.network.gateway.value,
		IPv4: state.l3.guestAddress.value, MAC: [6]byte(state.l3.mac),
		ManagedBridge: firewallLinkIdentity(network.managedBridge),
		HostVeth:      firewallLinkIdentity(state.topology.veth.host),
	}
	return nil
}

// Release closes operation-local authority only: global mutation, reopened descriptors, network,
// then genuine VM child. The active topology and endpoint remain intact. Copies share the result.
func (lease *FloatingIPVMLease) Release(ctx context.Context) error {
	if lease == nil || lease.state == nil {
		return nil
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if ctx == nil {
		return networkAuthorityValidationError("floating-IP VM release context is required")
	}
	cleanup := context.WithoutCancel(ctx)
	var result error
	if state.firewall != nil {
		result = errors.Join(result, state.firewall.Release(cleanup))
	}
	if state.pin != nil {
		result = errors.Join(result, state.pin.discardRecovery(cleanup))
	}
	if state.journal != nil {
		result = errors.Join(result, state.journal.store.Release(cleanup))
	}
	if state.network != nil {
		result = errors.Join(result, state.network.Release(cleanup))
	}
	result = errors.Join(result, state.hold.Release(cleanup))
	state.released, state.releaseErr = true, result
	return result
}
