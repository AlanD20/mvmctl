package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"sync"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/service/firewall"
)

// VMIntent contains only immutable guest intent; ownership and VM identity come from the retained Jailer hold.
type VMIntent struct {
	NetworkID        string
	GuestIPv4        netip.Addr
	MAC              *string
	LaunchGeneration uint64
}

// VMTopologyLease retains exact topology and cleanup authority for one VM lifetime.
// THREAD-SAFE: mu serializes activation checkpoints, namespace handoff, and cleanup.
type VMTopologyLease struct {
	// mu guards every retained capability and lifecycle transition.
	mu                sync.Mutex
	hold              *authorityfs.TopologyHold
	key               topologyKey
	network           *managedNetworkLease
	mutation          *firewall.MutationLease
	endpoint          *firewall.VerifiedVMEndpointLease
	binding           firewall.EndpointBinding
	l3                endpointL3
	effects           *topologyEffect
	preparation       *topologyPreparation
	journal           *topologyJournal
	active            bool
	completed         bool
	reserved          bool
	firewallAttempted bool
	activationStarted bool
	cleaned           bool
	closeErr          error
}

type retainedNetworkLocker struct{ lease *managedNetworkLease }

func (locker retainedNetworkLocker) LockNetwork(ctx context.Context, key networkKey) (NetworkLock, error) {
	if locker.lease == nil || locker.lease.key != key {
		return nil, networkAuthorityCorruptError("retained topology network lock does not match", nil)
	}
	if err := locker.lease.mutation.Verify(ctx); err != nil {
		return nil, err
	}
	return locker.lease, nil
}

// Prepare consumes the real VM hold, durably reserves its endpoint, and creates and activates exact topology.
// Every failure after consumption returns its non-nil cleanup lease. The caller must call Cleanup on failure and
// CompleteActivation only after its blocked Jailer child reaches the launch checkpoint.
func Prepare(ctx context.Context, hold *authorityfs.TopologyHold, intent VMIntent) (*VMTopologyLease, error) {
	if ctx == nil || hold == nil || intent.LaunchGeneration == 0 {
		return nil, networkAuthorityValidationError("VM topology context, hold, and launch generation are required")
	}
	owner, vm, err := hold.Binding()
	if err != nil {
		return nil, err
	}
	key, err := newTopologyKey(owner, vm.String(), intent.NetworkID)
	if err != nil {
		return nil, err
	}
	consumed, err := hold.Transfer(ctx)
	if err != nil {
		return nil, err
	}
	lease := &VMTopologyLease{hold: consumed, key: key, effects: newTopologyEffect()}
	if err := lease.prepare(ctx, intent); err != nil {
		if lease.activationStarted && lease.preparation != nil {
			cleanup := context.WithoutCancel(ctx)
			lowerErr := lease.effects.link.(*linkEffect).LowerHostVeth(cleanup, lease.preparation.pin, lease.preparation.topology)
			persistErr := lease.persistStage(cleanup, "activation_failed", topologyCleaning)
			return lease, errors.Join(err, lowerErr, persistErr)
		}
		return lease, err
	}
	return lease, nil
}

func (lease *VMTopologyLease) prepare(ctx context.Context, intent VMIntent) error {
	network, err := (managedNetworkLocker{}).LockNetwork(ctx, lease.key.Network())
	if err != nil {
		return err
	}
	lease.network = network.(*managedNetworkLease)
	lease.l3, err = deriveEndpointL3(lease.key, lease.network.record.configuration.l3, endpointL3Input{
		guestAddress: intent.GuestIPv4, requestedMAC: intent.MAC,
	})
	if err != nil {
		return err
	}
	lease.binding = lease.endpointBinding()
	lease.mutation, err = firewall.BeginVMEndpoint(ctx, lease.network.mutation, lease.binding)
	if err != nil {
		return err
	}
	if err := reserveTopologyEndpoint(ctx, lease.network.storeRoot, lease.key, lease.l3); err != nil {
		return err
	}
	lease.journal, err = openTopologyJournal(ctx, lease.network.store, lease.key.vmID)
	if err != nil {
		return err
	}
	prior, found, err := lease.journal.read(ctx)
	if err != nil {
		return err
	}
	if found && (prior.Lifecycle != topologyCleaned || prior.LaunchGeneration >= intent.LaunchGeneration) {
		return networkAuthorityCorruptError("existing VM topology requires ownership-bound reconciliation", nil)
	}
	rawNetwork, err := encodeNetworkAuthorityRecord(lease.network.record)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(rawNetwork)
	record := topologyJournalRecord{
		Schema: topologyJournalSchema, Lifecycle: topologyPreparing, OwnerUID: uint32(lease.key.ownerUID),
		NetworkID: string(lease.key.networkID), VMID: string(lease.key.vmID), BootID: lease.network.record.bootID,
		LaunchGeneration: intent.LaunchGeneration, NetworkDigest: hex.EncodeToString(digest[:]),
		Prefix: lease.l3.network.prefix.value.String(), Gateway: lease.l3.network.gateway.value.String(),
		GuestIPv4: lease.l3.guestAddress.value.String(), GuestMAC: lease.l3.mac.String(),
		EndpointID: deriveTopologyNames(lease.key).hostVeth.String(),
	}
	persistErr := lease.journal.persist(ctx, record)
	lease.reserved = lease.journal.raw != nil && lease.journal.record.LaunchGeneration == intent.LaunchGeneration
	if persistErr != nil {
		return persistErr
	}
	service := newService(retainedNetworkLocker{lease: lease.network}, lease.effects)
	service.journal = lease.journal
	lease.preparation, err = service.prepare(ctx, &vmLaunchLease{
		ownerUID: lease.key.ownerUID, vmID: lease.key.vmID, hold: lease.hold,
	}, lease.key.networkID)
	if err != nil {
		return err
	}
	if err := lease.persistStage(ctx, "configure_l3", topologyPreparing); err != nil {
		return err
	}
	link := lease.effects.link.(*linkEffect)
	if err := link.ConfigureL3Down(ctx, lease.preparation.pin, lease.preparation.topology, lease.l3); err != nil {
		return err
	}
	lease.journal.record.Progress.L3 = true
	lease.binding = lease.endpointBinding()
	if err := lease.revalidateNetwork(ctx); err != nil {
		return err
	}
	if err := lease.persistStage(ctx, "apply_firewall", topologyActivating); err != nil {
		return err
	}
	lease.firewallAttempted = true
	lease.endpoint, err = lease.mutation.ApplyVMEndpoint(ctx, lease.binding, lease.firewallNetworkIntent())
	if err != nil {
		return err
	}
	return lease.activate(ctx, link)
}

func (lease *VMTopologyLease) activate(ctx context.Context, link *linkEffect) error {
	lease.activationStarted = true
	topology, pin := lease.preparation.topology, lease.preparation.pin
	steps := []struct {
		name  string
		raise func(context.Context, NamespacePin, Topology) error
		mark  *bool
	}{
		{"raise_loopback", link.RaiseLoopback, &lease.journal.record.Progress.LoopbackUp},
		{"raise_local_bridge", link.RaiseLocalBridge, &lease.journal.record.Progress.BridgeUp},
		{"raise_tap", link.RaiseTAP, &lease.journal.record.Progress.TAPUp},
		{"raise_namespace_veth", link.RaiseNamespaceVeth, &lease.journal.record.Progress.PeerUp},
		{"raise_host_veth", link.RaiseHostVeth, &lease.journal.record.Progress.HostUp},
	}
	for _, step := range steps {
		if err := lease.revalidateNetwork(ctx); err != nil {
			return err
		}
		if err := lease.mutation.VerifyVMEndpoint(ctx, lease.endpoint, lease.binding); err != nil {
			return err
		}
		if err := lease.persistStage(ctx, step.name, topologyActivating); err != nil {
			return err
		}
		mutationErr := step.raise(ctx, pin, topology)
		*step.mark = true
		if err := lease.verifyActivationLinks(context.WithoutCancel(ctx)); err != nil {
			*step.mark = false
			return errors.Join(mutationErr, err)
		}
		if err := errors.Join(mutationErr, lease.persistStage(context.WithoutCancel(ctx), "", topologyActivating)); err != nil {
			return err
		}
	}
	if err := lease.persistStage(ctx, "", topologyActive); err != nil {
		return err
	}
	lease.active = true
	return nil
}

// CompleteActivation releases the later mutation locks after the caller's launch checkpoint.
// The VM hold, namespace pin, endpoint authority, and durable topology remain retained through active lifetime.
func (lease *VMTopologyLease) CompleteActivation(ctx context.Context) error {
	if lease == nil {
		return networkAuthorityCorruptError("VM topology lease is unavailable", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.completed {
		return nil
	}
	if !lease.active || lease.cleaned || lease.mutation == nil || lease.network == nil {
		return networkAuthorityCorruptError("VM topology is not at the activation checkpoint", nil)
	}
	if err := lease.mutation.VerifyVMEndpoint(ctx, lease.endpoint, lease.binding); err != nil {
		return err
	}
	if err := lease.revalidateNetwork(ctx); err != nil {
		return err
	}
	if err := lease.verifyActivationLinks(ctx); err != nil {
		return err
	}
	mutationErr := lease.mutation.Release(ctx)
	lease.mutation = nil
	if mutationErr != nil {
		return mutationErr
	}
	networkErr := lease.network.Release(ctx)
	lease.network = nil
	lease.preparation.lock = nil
	if networkErr != nil {
		return networkErr
	}
	lease.completed = true
	return nil
}

// Cleanup removes exposure first, then the exact firewall endpoint, then topology, and finally the retained VM hold.
// Cancellation never abandons an owned cleanup; failures preserve the exact lease and durable progress for retry.
func (lease *VMTopologyLease) Cleanup(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.cleaned {
		return lease.closeErr
	}
	if ctx == nil {
		return networkAuthorityValidationError("VM topology cleanup context is required")
	}
	cleanup := context.WithoutCancel(ctx)
	lease.active = false
	if lease.journal != nil && lease.journal.ambiguous {
		return networkAuthorityCorruptError("topology publication is ambiguous; retained authority requires reconciliation", nil)
	}
	if lease.reserved {
		if err := lease.persistStage(cleanup, "begin_cleanup", topologyCleaning); err != nil {
			return err
		}
	}
	if lease.preparation != nil && lease.preparation.state != topologyPreparationCleaned {
		if err := lease.reacquireCleanup(cleanup); err != nil {
			return err
		}
		if lease.activationStarted {
			link := lease.effects.link.(*linkEffect)
			if err := link.LowerHostVeth(cleanup, lease.preparation.pin, lease.preparation.topology); err != nil {
				return err
			}
			if lease.journal != nil {
				lease.journal.record.Progress.HostUp = false
				if err := lease.persistStage(cleanup, "remove_firewall", topologyCleaning); err != nil {
					return err
				}
			}
		}
		if lease.firewallAttempted {
			if err := lease.mutation.RemoveEndpoint(cleanup, lease.binding); err != nil {
				return err
			}
			lease.firewallAttempted = false
		}
		if lease.activationStarted {
			if err := lease.lowerRemainingLinks(cleanup); err != nil {
				return err
			}
			lease.activationStarted = false
		}
		if err := lease.preparation.Cleanup(cleanup); err != nil {
			return err
		}
	}
	if lease.reserved && lease.journal.record.Lifecycle != topologyCleaned {
		record := lease.journal.record
		record.Lifecycle, record.Pending, record.Progress = topologyCleaned, "", topologyProgress{}
		if err := lease.journal.persist(cleanup, record); err != nil {
			return err
		}
	}
	// All owned kernel effects are proven absent before any terminal close is attempted.
	var closeErr error
	if lease.mutation != nil {
		closeErr = errors.Join(closeErr, lease.mutation.Release(cleanup))
	}
	if lease.network != nil {
		closeErr = errors.Join(closeErr, lease.network.Release(cleanup))
	}
	if lease.journal != nil {
		closeErr = errors.Join(closeErr, lease.journal.store.Release(cleanup))
	}
	if lease.hold != nil {
		closeErr = errors.Join(closeErr, lease.hold.Release(cleanup))
	}
	lease.cleaned, lease.closeErr = true, closeErr
	return closeErr
}

func (lease *VMTopologyLease) lowerRemainingLinks(ctx context.Context) error {
	link := lease.effects.link.(*linkEffect)
	for _, lower := range []func(context.Context, NamespacePin, Topology) error{
		link.LowerNamespaceVeth, link.LowerTAP, link.LowerLocalBridge, link.LowerLoopback,
	} {
		if err := lower(ctx, lease.preparation.pin, lease.preparation.topology); err != nil {
			return err
		}
	}
	progress := &lease.journal.record.Progress
	progress.LoopbackUp, progress.BridgeUp, progress.TAPUp, progress.PeerUp, progress.HostUp = false, false, false, false, false
	return lease.persistStage(ctx, "", topologyCleaning)
}

func (lease *VMTopologyLease) reacquireCleanup(ctx context.Context) error {
	if lease.network == nil {
		network, err := (managedNetworkLocker{}).LockNetwork(ctx, lease.key.Network())
		if err != nil {
			return err
		}
		lease.network = network.(*managedNetworkLease)
		lease.preparation.lock = lease.network
	}
	if lease.mutation == nil {
		mutation, err := firewall.BeginVMEndpoint(ctx, lease.network.mutation, lease.binding)
		if err != nil {
			return err
		}
		lease.mutation = mutation
	}
	return nil
}

func (lease *VMTopologyLease) persistStage(ctx context.Context, pending string, lifecycle topologyLifecycle) error {
	record := lease.journal.record
	record.Pending, record.Lifecycle = pending, lifecycle
	return lease.journal.persist(ctx, record)
}

func (lease *VMTopologyLease) revalidateNetwork(ctx context.Context) error {
	if lease.network == nil || lease.network.released {
		return networkAuthorityCorruptError("topology network mutation authority is unavailable", nil)
	}
	if err := lease.network.mutation.Verify(ctx); err != nil {
		return err
	}
	fd, identity, err := lease.network.host.descriptor()
	if err != nil || identity != lease.network.record.managedBridge.namespace {
		return networkAuthorityCorruptError("topology host namespace changed", err)
	}
	if err := inspectManagedBridgeL3(ctx, newDirectRouteNetlink(), fd, lease.network.record); err != nil {
		return err
	}
	if lease.network.uplink != nil {
		return lease.network.uplink.revalidate(ctx)
	}
	return nil
}

func (lease *VMTopologyLease) endpointBinding() firewall.EndpointBinding {
	record := lease.network.record
	host := linkIdentity{name: deriveTopologyNames(lease.key).hostVeth, namespace: record.managedBridge.namespace}
	if lease.preparation != nil {
		host = lease.preparation.topology.veth.host
	}
	return firewall.EndpointBinding{OwnerUID: uint32(lease.key.ownerUID), VMID: string(lease.key.vmID),
		NetworkID: string(lease.key.networkID), BootID: record.bootID,
		HostNamespace: firewall.NamespaceIdentity{Device: record.managedBridge.namespace.device, Inode: record.managedBridge.namespace.inode},
		Prefix:        lease.l3.network.prefix.value, Gateway: lease.l3.network.gateway.value,
		IPv4: lease.l3.guestAddress.value, MAC: [6]byte(lease.l3.mac),
		ManagedBridge: firewallLinkIdentity(record.managedBridge), HostVeth: firewallLinkIdentity(host)}
}

func firewallLinkIdentity(link linkIdentity) firewall.LinkIdentity {
	return firewall.LinkIdentity{Name: link.name.String(), IfIndex: link.ifIndex,
		NamespaceDevice: link.namespace.device, NamespaceInode: link.namespace.inode}
}

func (lease *VMTopologyLease) firewallNetworkIntent() firewall.NetworkIntent {
	record := lease.network.record
	intent := firewall.NetworkIntent{OwnerUID: record.ownerUID, NetworkID: string(record.networkID),
		HostBridge: record.managedBridge.name.String(), Subnet: record.configuration.l3.prefix.value,
		HostIPv4: record.configuration.l3.gateway.value, Masquerade: record.configuration.nat}
	if record.configuration.uplink != nil {
		intent.UplinkInterface = record.configuration.uplink.link.name.String()
	}
	return intent
}
