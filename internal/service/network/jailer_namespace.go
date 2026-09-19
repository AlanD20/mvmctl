package network

import (
	"context"
	"net/netip"

	"mvmctl/internal/lib/system"
)

// JailerNamespaceLease is a closed handoff capability for an already active exact VM topology.
// It borrows the topology lifetime and becomes unusable as soon as that topology enters cleanup.
type JailerNamespaceLease struct{ owner *VMTopologyLease }

// JailerNetworkConfig is a root-derived child configuration projection, not namespace or link authority.
type JailerNetworkConfig struct {
	OwnerUID        uint32
	VMID            string
	NetworkID       string
	NamespaceDevice uint64
	NamespaceInode  uint64
	TAPName         string
	GuestMAC        string
	GuestIPv4       netip.Addr
	Prefix          netip.Prefix
	Gateway         netip.Addr
}

// JailerNamespace returns a closed capability only after firewall and every ordered link activation succeeded.
func (lease *VMTopologyLease) JailerNamespace(ctx context.Context) (*JailerNamespaceLease, error) {
	if lease == nil {
		return nil, networkAuthorityCorruptError("VM topology lease is unavailable", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.admitJailerHandoff(ctx); err != nil {
		return nil, err
	}
	return &JailerNamespaceLease{owner: lease}, nil
}

// Binding returns only derived configuration and observed identity while the retained topology is active.
func (lease *JailerNamespaceLease) Binding(ctx context.Context) (JailerNetworkConfig, error) {
	if lease == nil || lease.owner == nil {
		return JailerNetworkConfig{}, networkAuthorityCorruptError("Jailer namespace capability is unavailable", nil)
	}
	owner := lease.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.active || owner.cleaned || owner.preparation == nil {
		return JailerNetworkConfig{}, networkAuthorityCorruptError("Jailer namespace topology is not active", nil)
	}
	if err := ctx.Err(); err != nil {
		return JailerNetworkConfig{}, err
	}
	topology := owner.preparation.topology
	return JailerNetworkConfig{OwnerUID: uint32(owner.key.ownerUID), VMID: string(owner.key.vmID),
		NetworkID: string(owner.key.networkID), NamespaceDevice: topology.handle.identity.device,
		NamespaceInode: topology.handle.identity.inode, TAPName: topology.tap.name.String(),
		GuestMAC: owner.l3.mac.String(), GuestIPv4: owner.l3.guestAddress.value,
		Prefix: owner.l3.network.prefix.value, Gateway: owner.l3.network.gateway.value}, nil
}

// SendToJailer transfers exactly the retained namespace into the authenticated blocked Jailer child channel.
// CRITICAL: No descriptor or path is returned to callers, and the namespace lifecycle remains locked during send.
func (lease *JailerNamespaceLease) SendToJailer(ctx context.Context, target *system.JailerChildNamespaceTarget) error {
	if lease == nil || lease.owner == nil || target == nil {
		return networkAuthorityCorruptError("Jailer namespace capability or child target is unavailable", nil)
	}
	owner := lease.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if err := owner.admitJailerHandoff(ctx); err != nil {
		return err
	}
	pin, ok := owner.preparation.pin.(*namespacePin)
	if !ok || pin.state == nil {
		return networkAuthorityCorruptError("Jailer namespace pin has no concrete authority", nil)
	}
	state := pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := verifyPublishedNamespace(ctx, state); err != nil {
		return networkAuthorityEffectError("verify pinned Jailer namespace before transfer", err)
	}
	return target.SendNetworkNamespace(ctx, state.netnsFD)
}

// VerifyJailerNamespace compares the child observation with the still-pinned exact namespace.
func (lease *JailerNamespaceLease) VerifyJailerNamespace(ctx context.Context, device, inode uint64) error {
	if lease == nil || lease.owner == nil {
		return networkAuthorityCorruptError("Jailer namespace capability is unavailable", nil)
	}
	owner := lease.owner
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if !owner.active || owner.cleaned || owner.preparation == nil ||
		owner.preparation.topology.handle.identity != (namespaceIdentity{device: device, inode: inode}) {
		return networkAuthorityCorruptError("Jailer child namespace does not match retained topology", nil)
	}
	pin, ok := owner.preparation.pin.(*namespacePin)
	if !ok || pin.state == nil {
		return networkAuthorityCorruptError("Jailer namespace pin has no concrete authority", nil)
	}
	pin.state.mu.Lock()
	defer pin.state.mu.Unlock()
	return verifyPublishedNamespace(ctx, pin.state)
}

func (lease *VMTopologyLease) admitJailerHandoff(ctx context.Context) error {
	if !lease.active || lease.cleaned || lease.detached ||
		lease.preparation == nil || lease.journal == nil || lease.journal.record.Lifecycle != topologyActive {
		return networkAuthorityCorruptError("VM topology is not at the retained Jailer handoff checkpoint", nil)
	}
	if lease.completed {
		if _, _, err := lease.hold.Binding(); err != nil {
			return err
		}
		return lease.verifyActivationLinks(ctx)
	}
	if lease.mutation == nil || lease.network == nil {
		return networkAuthorityCorruptError("Jailer handoff mutation authority is unavailable", nil)
	}
	if err := lease.revalidateNetwork(ctx); err != nil {
		return err
	}
	if err := lease.mutation.VerifyVMEndpoint(ctx, lease.endpoint, lease.binding); err != nil {
		return err
	}
	return lease.verifyActivationLinks(ctx)
}
