package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/system"
	"mvmctl/internal/service/firewall"
)

// retirePriorBoot performs no link, namespace, or old-generation kernel deletion. A reboot invalidates
// old ifindices and inode numbers; only current absence can retire their recorded ownership.
func (lease *VMTopologyLease) retirePriorBoot(
	ctx context.Context, record topologyJournalRecord, currentBoot string, proof *system.PriorBootVMAbsence,
) error {
	if err := lease.verifyPriorBootProof(ctx, record, currentBoot, proof); err != nil {
		return err
	}
	admittedRaw := append([]byte(nil), lease.journal.raw...)
	network, err := lockPriorBootNetwork(ctx, lease.key.Network(), record, currentBoot)
	if err != nil {
		return err
	}
	lease.network, lease.networkRecord = network, network.record
	if err := lease.requirePriorBootTopologyAbsent(ctx); err != nil {
		return err
	}
	_, host, err := network.host.descriptor()
	if err != nil {
		return err
	}
	lease.mutation, err = firewall.BeginPolicyChange(ctx, network.mutation, firewall.PolicyBinding{
		OwnerUID: record.OwnerUID, NetworkID: record.NetworkID, BootID: currentBoot,
		HostNamespace: firewall.NamespaceIdentity{Device: host.device, Inode: host.inode},
	})
	if err != nil {
		return err
	}
	prefix, err := netip.ParsePrefix(record.Prefix)
	if err != nil {
		return err
	}
	gateway, err := netip.ParseAddr(record.Gateway)
	if err != nil {
		return err
	}
	guest, err := netip.ParseAddr(record.GuestIPv4)
	if err != nil {
		return err
	}
	l3, err := deriveEndpointL3(lease.key, network.record.configuration.l3,
		endpointL3Input{guestAddress: guest, requestedMAC: &record.GuestMAC})
	if err != nil {
		return err
	}
	if err := lease.mutation.VerifyRetiredEndpoint(ctx, firewall.EndpointReservation{
		OwnerUID: record.OwnerUID, VMID: record.VMID, NetworkID: record.NetworkID,
		Prefix: prefix, Gateway: gateway, IPv4: guest, MAC: [6]byte(l3.mac),
	}); err != nil {
		return err
	}
	// These checks surround firewall recovery and the durable journal publication. Administrator
	// mutations detected here fail closed; old evidence never authorizes removing a new object.
	if err := lease.requirePriorBootTopologyAbsent(ctx); err != nil {
		return err
	}
	if err := lease.verifyPriorBootProof(ctx, record, currentBoot, proof); err != nil {
		return err
	}
	_, found, err := lease.journal.read(ctx)
	if err != nil || !found || !bytes.Equal(admittedRaw, lease.journal.raw) {
		return networkAuthorityCorruptError("prior-boot topology changed before retirement", err)
	}
	record.Lifecycle, record.Pending, record.Progress = topologyCleaned, "", topologyProgress{}
	if err := lease.journal.persist(ctx, record); err != nil {
		return err
	}
	// Keep the old boot and exact historical identities as evidence. There are no admitted kernel
	// effects to clean; Cleanup only releases the held suffix, journal, and VM authority.
	lease.reserved = false
	return nil
}

func (lease *VMTopologyLease) verifyPriorBootProof(
	ctx context.Context, record topologyJournalRecord, currentBoot string, proof *system.PriorBootVMAbsence,
) error {
	observed, err := proof.Verify(ctx, lease.hold, record.BootID, record.LaunchGeneration)
	if err != nil || observed != currentBoot || currentBoot == record.BootID {
		return networkAuthorityCorruptError("prior-boot VM absence proof is unavailable or changed", err)
	}
	return nil
}

func (lease *VMTopologyLease) requirePriorBootTopologyAbsent(ctx context.Context) error {
	owner, ok := lease.network.owner.(*systemNetworkLockUID)
	if !ok || owner.value == nil {
		return networkAuthorityCorruptError("prior-boot topology lacks concrete network owner", nil)
	}
	if err := owner.value.RequireNamespaceAbsent(ctx, lease.hold); err != nil {
		return err
	}
	fd, _, err := lease.network.host.descriptor()
	if err != nil {
		return err
	}
	names := deriveTopologyNames(lease.key)
	route := newDirectRouteNetlink()
	for _, name := range []linkName{names.hostVeth, names.namespaceVeth, names.localBridge, names.tap} {
		link, err := route.inspectLink(ctx, fd, name)
		if err != nil {
			return networkAuthorityEffectError("inspect prior-boot VM link absence", err)
		}
		if link != nil {
			return networkAuthorityCorruptError("prior-boot VM link name is occupied; refusing adoption or deletion", nil)
		}
	}
	return nil
}

// lockPriorBootNetwork retains a concrete mutation lock without admitting an old bridge as live.
// A freshly rebootstrapped root record may replace old physical observations, but not logical L3 identity.
func lockPriorBootNetwork(
	ctx context.Context, key networkKey, topology topologyJournalRecord, currentBoot string,
) (_ *managedNetworkLease, returnErr error) {
	lease := &managedNetworkLease{key: key}
	defer func() {
		if returnErr != nil {
			returnErr = appendNetworkAuthorityError(returnErr, "release rejected prior-boot network", lease.Release(ctx))
		}
	}()
	var err error
	lease.root, err = (systemNetworkAuthorityTree{}).openNetwork(ctx)
	if err != nil {
		return nil, err
	}
	lease.owner, err = lease.root.openUID(ctx, uint32(key.ownerUID))
	if err != nil {
		return nil, err
	}
	owner, ok := lease.owner.(*systemNetworkLockUID)
	if !ok || owner.value == nil {
		return nil, networkAuthorityCorruptError("prior-boot network lacks concrete mutation owner", nil)
	}
	lockKey, err := authorityfs.NewNetworkKey(string(key.networkID))
	if err != nil {
		return nil, err
	}
	lease.mutation, err = owner.value.AcquireMutation(ctx, lockKey)
	lease.lock = lease.mutation
	if err != nil {
		return nil, err
	}
	lease.storeRoot, err = openNetworkStoreRoot(ctx, realNetworkStoreDeps(), productionNetworkStorePolicy())
	if err != nil {
		return nil, err
	}
	lease.store, err = lease.storeRoot.openUID(ctx, uint32(key.ownerUID), false)
	if err != nil {
		return nil, err
	}
	record, found, err := lease.store.readRecord(ctx, key.networkID)
	if err != nil || !found {
		return nil, networkAuthorityCorruptError("prior-boot network authority is unavailable", err)
	}
	if record.schemaVersion != managedNetworkSchemaVersion || record.configuration == nil ||
		(record.bootID != topology.BootID && record.bootID != currentBoot) ||
		record.configuration.l3.prefix.value.String() != topology.Prefix ||
		record.configuration.l3.gateway.value.String() != topology.Gateway {
		return nil, networkAuthorityCorruptError("prior-boot network logical authority differs", nil)
	}
	if record.bootID == topology.BootID {
		raw, err := encodeNetworkAuthorityRecord(record)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) != topology.NetworkDigest {
			return nil, networkAuthorityCorruptError("prior-boot network record digest differs", nil)
		}
	}
	lease.record = record
	namespace := newNamespaceEffect()
	lease.host, err = retainHostLinkNamespace(ctx, namespace.deps, namespace.policy)
	if err != nil {
		return nil, err
	}
	return lease, nil
}
