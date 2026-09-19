package network

import (
	"bytes"
	"context"
	"errors"
	"net/netip"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/service/firewall"
)

// ForgetVMEndpoint invalidates stopped endpoint policy only during final VM removal.
// It borrows the caller's real VM hold; the caller retains responsibility for releasing that hold afterward.
// Ordinary stop and topology Cleanup deliberately preserve these immutable reservations and grants.
func ForgetVMEndpoint(ctx context.Context, hold *authorityfs.TopologyHold, launchGeneration uint64) (returnErr error) {
	if ctx == nil || hold == nil || launchGeneration == 0 {
		return networkAuthorityValidationError("endpoint invalidation requires VM hold and launch generation")
	}
	owner, vm, err := hold.Binding()
	if err != nil {
		return err
	}
	journal, err := openOwnedTopologyJournal(ctx, owner, VMID(vm.String()))
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "close invalidated endpoint journal", journal.store.Release(context.WithoutCancel(ctx)))
	}()
	record, found, err := journal.read(ctx)
	if err != nil || !found {
		return err
	}
	if record.Lifecycle != topologyCleaned || record.LaunchGeneration != launchGeneration {
		return networkAuthorityCorruptError("endpoint invalidation requires exact cleaned topology generation", nil)
	}
	admittedRaw := append([]byte(nil), journal.raw...)
	key, err := newTopologyKey(owner, vm.String(), record.NetworkID)
	if err != nil {
		return err
	}
	currentBoot, err := readCurrentBootID(ctx)
	if err != nil {
		return err
	}
	var network *managedNetworkLease
	if record.BootID != currentBoot {
		// Only a cleaned historical journal may authorize logical invalidation. The current
		// namespace and links must independently be absent; old kernel identities grant no effect.
		network, err = lockPriorBootNetwork(ctx, key.Network(), record, currentBoot)
	} else {
		var locked NetworkLock
		locked, err = (managedNetworkLocker{cleanup: true}).LockNetwork(ctx, key.Network())
		if err == nil {
			network = locked.(*managedNetworkLease)
		}
	}
	if err != nil {
		return err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "release invalidated endpoint network", network.Release(context.WithoutCancel(ctx)))
	}()
	if network.record.configuration.l3.prefix.value.String() != record.Prefix ||
		network.record.configuration.l3.gateway.value.String() != record.Gateway {
		return networkAuthorityCorruptError("endpoint invalidation network logical identity changed", nil)
	}
	guest, err := netip.ParseAddr(record.GuestIPv4)
	if err != nil {
		return err
	}
	l3, err := deriveEndpointL3(key, network.record.configuration.l3,
		endpointL3Input{guestAddress: guest, requestedMAC: &record.GuestMAC})
	if err != nil {
		return err
	}
	if err := requireFinalEndpointAbsent(ctx, hold, network, key); err != nil {
		return err
	}
	_, namespace, err := network.host.descriptor()
	if err != nil {
		return err
	}
	mutation, err := firewall.BeginPolicyChange(ctx, network.mutation, firewall.PolicyBinding{
		OwnerUID: owner, NetworkID: record.NetworkID, BootID: currentBoot,
		HostNamespace: firewall.NamespaceIdentity{Device: namespace.device, Inode: namespace.inode},
	})
	if err != nil {
		return err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "release invalidated endpoint firewall", mutation.Release(context.WithoutCancel(ctx)))
	}()
	if err := mutation.VerifyRetiredEndpoint(ctx, firewall.EndpointReservation{
		OwnerUID: owner, VMID: record.VMID, NetworkID: record.NetworkID,
		Prefix: l3.network.prefix.value, Gateway: l3.network.gateway.value, IPv4: guest, MAC: [6]byte(l3.mac),
	}); err != nil {
		return err
	}
	if err := requireFinalEndpointAbsent(ctx, hold, network, key); err != nil {
		return err
	}
	_, found, err = journal.read(ctx)
	if err != nil || !found || !bytes.Equal(admittedRaw, journal.raw) {
		return networkAuthorityCorruptError("cleaned endpoint journal changed before final invalidation", err)
	}
	observedBoot, err := readCurrentBootID(ctx)
	if err != nil || observedBoot != currentBoot {
		return networkAuthorityCorruptError("host boot changed before final endpoint invalidation", err)
	}
	return mutation.RemoveVMPolicies(ctx, record.VMID)
}

func requireFinalEndpointAbsent(
	ctx context.Context, hold *authorityfs.TopologyHold, network *managedNetworkLease, key topologyKey,
) error {
	owner, ok := network.owner.(*systemNetworkLockUID)
	if !ok || owner.value == nil {
		return networkAuthorityCorruptError("final endpoint invalidation lacks concrete network owner", nil)
	}
	if err := owner.value.RequireNamespaceAbsent(ctx, hold); err != nil {
		return err
	}
	fd, _, err := network.host.descriptor()
	if err != nil {
		return err
	}
	names := deriveTopologyNames(key)
	route := newDirectRouteNetlink()
	for _, name := range []linkName{names.hostVeth, names.namespaceVeth, names.localBridge, names.tap} {
		link, err := route.inspectLink(ctx, fd, name)
		if err != nil {
			return networkAuthorityEffectError("inspect final VM endpoint link absence", err)
		}
		if link != nil {
			return networkAuthorityCorruptError("final endpoint invalidation found an occupied VM link name", nil)
		}
	}
	return nil
}
