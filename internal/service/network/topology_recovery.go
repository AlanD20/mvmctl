package network

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

// Recover consumes the current VM hold and reopens only the same-boot topology in its root-owned journal.
// The caller supplies the launch generation from its admitted root VM record. Recovery never adopts a path or link
// discovered from user configuration and returns the retained lease with every post-consumption error.
func Recover(ctx context.Context, hold *authorityfs.TopologyHold, launchGeneration uint64) (*VMTopologyLease, error) {
	if ctx == nil || hold == nil || launchGeneration == 0 {
		return nil, networkAuthorityValidationError("topology recovery requires VM hold and launch generation")
	}
	owner, vm, err := hold.Binding()
	if err != nil {
		return nil, err
	}
	consumed, err := hold.Transfer(ctx)
	if err != nil {
		return nil, err
	}
	lease := &VMTopologyLease{hold: consumed, effects: newTopologyEffect(), recoveryPending: true}
	if err := lease.recover(ctx, owner, VMID(vm.String()), launchGeneration); err != nil {
		if errors.Is(err, errTopologyJournalAbsent) {
			var closeErr error
			if lease.journal != nil {
				closeErr = lease.journal.store.Release(context.WithoutCancel(ctx))
			}
			closeErr = errors.Join(closeErr, lease.hold.Release(context.WithoutCancel(ctx)))
			lease.recoveryPending, lease.cleaned, lease.closeErr = false, true, closeErr
			if closeErr == nil {
				return nil, nil
			}
			return lease, closeErr
		}
		return lease, err
	}
	lease.recoveryPending = false
	return lease, nil
}

var errTopologyJournalAbsent = errors.New("root topology journal is absent")

func (lease *VMTopologyLease) recover(ctx context.Context, owner uint32, vmID VMID, generation uint64) error {
	journal, err := openOwnedTopologyJournal(ctx, owner, vmID)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return errTopologyJournalAbsent
		}
		return err
	}
	lease.journal = journal
	record, found, err := journal.read(ctx)
	if err == nil && !found {
		return errTopologyJournalAbsent
	}
	if err != nil {
		return networkAuthorityCorruptError("root topology journal is unavailable for VM recovery", err)
	}
	if record.LaunchGeneration != generation {
		return networkAuthorityCorruptError("topology launch generation differs from root VM authority", nil)
	}
	lease.key, err = newTopologyKey(owner, string(vmID), record.NetworkID)
	if err != nil {
		return err
	}
	bootID, err := readCurrentBootID(ctx)
	if err != nil {
		return err
	}
	if record.BootID != bootID {
		return networkAuthorityCorruptError("prior-boot topology requires process and namespace absence proof", nil)
	}
	if record.Lifecycle == topologyCleaned {
		lease.reserved = true
		return nil
	}
	admittedRaw := append([]byte(nil), journal.raw...)
	network, err := (managedNetworkLocker{cleanup: true}).LockNetwork(ctx, lease.key.Network())
	if err != nil {
		return err
	}
	lease.network = network.(*managedNetworkLease)
	rawNetwork, err := encodeNetworkAuthorityRecord(lease.network.record)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(rawNetwork)
	if hex.EncodeToString(digest[:]) != record.NetworkDigest {
		return networkAuthorityCorruptError("topology managed-network record binding changed", nil)
	}
	guest, err := netip.ParseAddr(record.GuestIPv4)
	if err != nil {
		return err
	}
	lease.l3, err = deriveEndpointL3(lease.key, lease.network.record.configuration.l3,
		endpointL3Input{guestAddress: guest, requestedMAC: &record.GuestMAC})
	if err != nil {
		return err
	}
	topology, err := recoverTopologyValue(lease.key, record.Topology, record.Progress)
	if err != nil {
		return err
	}
	lease.preparation = &topologyPreparation{effects: lease.effects, lock: lease.network, topology: topology,
		state: topologyPreparationCleaning, journal: journal, keepNetworkLock: true}
	lease.binding = lease.endpointBinding()
	current, found, err := journal.read(ctx)
	if err != nil || !found || !bytes.Equal(journal.raw, admittedRaw) {
		return networkAuthorityCorruptError("topology journal changed before recovery admission", err)
	}
	if !current.Progress.Namespace {
		return networkAuthorityCorruptError("incomplete namespace creation has no exact recovery identity", nil)
	}
	pin, err := lease.effects.namespace.(*namespaceEffect).recoverNamespace(ctx, lease.key,
		topology.handle, namespaceTargetFromJournal(current.NamespaceTarget), lease.hold)
	if err != nil {
		return err
	}
	preparation := lease.preparation
	preparation.pin = pin
	if pin.Handle() != topology.handle && (current.Lifecycle != topologyCleaning ||
		current.Progress.LocalBridge || current.Progress.TAP || current.Progress.Veth) {
		return errors.Join(networkAuthorityCorruptError("missing namespace cannot authorize live-link topology recovery", nil),
			pin.discardRecovery(context.WithoutCancel(ctx)))
	}
	preparation.namespaceCreated = current.Progress.Namespace
	preparation.localBridgeMade = current.Progress.LocalBridge
	preparation.tapMade = current.Progress.TAP
	preparation.vethMade = current.Progress.Veth
	lease.reserved = true
	lease.firewallAttempted = current.Progress.L3
	lease.activationStarted = current.Progress.LoopbackUp || current.Progress.BridgeUp || current.Progress.TAPUp ||
		current.Progress.PeerUp || current.Progress.HostUp
	return nil
}

func openOwnedTopologyJournal(ctx context.Context, owner uint32, vmID VMID) (_ *topologyJournal, returnErr error) {
	root, err := openNetworkStoreRoot(ctx, realNetworkStoreDeps(), productionNetworkStorePolicy())
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "close topology recovery root", root.Release(context.WithoutCancel(ctx)))
	}()
	uid, err := root.openUID(ctx, owner, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "close topology recovery owner", uid.Release(context.WithoutCancel(ctx)))
	}()
	return openTopologyJournal(ctx, uid, vmID)
}

func recoverTopologyValue(key topologyKey, record TopologyRecord, progress topologyProgress) (Topology, error) {
	if record.OwnerUID != uint32(key.ownerUID) || record.VMID != string(key.vmID) || record.NetworkID != string(key.networkID) {
		return Topology{}, networkAuthorityCorruptError("persisted topology identity differs from VM authority", nil)
	}
	topology := Topology{key: key,
		handle: namespaceHandle{path: namespaceHandlePath(record.Namespace.Path),
			identity: namespaceIdentity{device: record.Namespace.Identity.Device, inode: record.Namespace.Identity.Inode},
			ownerUID: record.Namespace.OwnerUID, ownerGID: record.Namespace.OwnerGID, mode: record.Namespace.Mode},
		managedBridge: importLinkIdentity(record.ManagedBridge), localBridge: importLinkIdentity(record.LocalBridge),
		tap:  importLinkIdentity(record.TAP),
		veth: vethPairIdentity{host: importLinkIdentity(record.VethPair.Host), peer: importLinkIdentity(record.VethPair.Peer)}}
	names := deriveTopologyNames(key)
	if err := validateNetworkLock(key.Network(), topology.managedBridge, key.Network(), names.managedBridge); err != nil {
		return Topology{}, err
	}
	if progress.Namespace {
		if err := validateNamespaceHandle(topology.handle, names.namespaceHandle); err != nil {
			return Topology{}, err
		}
	}
	if progress.LocalBridge {
		if err := validateLinkIdentity(topology.localBridge, names.localBridge, topology.handle.identity, "local bridge"); err != nil {
			return Topology{}, err
		}
	}
	if progress.TAP {
		if err := validateLinkIdentity(topology.tap, names.tap, topology.handle.identity, "TAP"); err != nil {
			return Topology{}, err
		}
	}
	if progress.Veth {
		if err := validateVethPair(topology.veth, names, topology.managedBridge.namespace, topology.handle.identity); err != nil {
			return Topology{}, err
		}
	}
	return topology, nil
}

func importLinkIdentity(link LinkIdentity) linkIdentity {
	return linkIdentity{name: linkName(link.Name), ifIndex: link.IfIndex,
		namespace: namespaceIdentity{device: link.Namespace.Device, inode: link.Namespace.Inode}}
}

func namespaceTargetFromJournal(target topologyNamespaceTarget) namespaceTargetIdentity {
	return namespaceTargetIdentity{directoryDevice: target.DirectoryDevice, directoryInode: target.DirectoryInode,
		directoryMount:    namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityKind(target.DirectoryMountKind), value: target.DirectoryMount},
		placeholderDevice: target.PlaceholderDevice, placeholderInode: target.PlaceholderInode,
		placeholderMount: namespaceTargetMountIdentity{kind: namespaceTargetMountIdentityKind(target.PlaceholderMountKind), value: target.PlaceholderMount}}
}

// DetachActive closes operation-local authority after durable active handoff and successful exact reopen.
// The namespace mount, links, firewall endpoint, and root journals remain intact for a future Recover operation.
func (lease *VMTopologyLease) DetachActive(ctx context.Context) error {
	if lease == nil {
		return networkAuthorityCorruptError("VM topology lease is unavailable", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.detached {
		return lease.closeErr
	}
	if !lease.active || lease.cleaned || !lease.completed || lease.journal == nil || lease.preparation == nil {
		return networkAuthorityCorruptError("topology is not durably active at detach checkpoint", nil)
	}
	if err := lease.reacquireCleanup(ctx); err != nil {
		return err
	}
	if err := lease.revalidateNetwork(ctx); err != nil {
		return err
	}
	if err := lease.mutation.VerifyVMEndpoint(ctx, lease.endpoint, lease.binding); err != nil {
		return err
	}
	if err := lease.verifyActivationLinks(ctx); err != nil {
		return err
	}
	expected := append([]byte(nil), lease.journal.raw...)
	record, found, err := lease.journal.read(ctx)
	if err != nil || !found || !bytes.Equal(expected, lease.journal.raw) || record.Lifecycle != topologyActive || record.Pending != "" {
		return networkAuthorityCorruptError("active topology journal changed before detach", err)
	}
	effect := lease.effects.namespace.(*namespaceEffect)
	probe, err := effect.recoverNamespace(ctx, lease.key, lease.preparation.topology.handle,
		namespaceTargetFromJournal(record.NamespaceTarget), lease.hold)
	if err != nil {
		return err
	}
	if err := probe.detachActive(ctx); err != nil {
		return err
	}
	pin, ok := lease.preparation.pin.(*namespacePin)
	if !ok {
		return networkAuthorityCorruptError("active topology lacks concrete namespace pin", nil)
	}
	if err := pin.detachActive(ctx); err != nil {
		return err
	}
	cleanup := context.WithoutCancel(ctx)
	lease.closeErr = errors.Join(lease.mutation.Release(cleanup), lease.network.Release(cleanup),
		lease.journal.store.Release(cleanup), lease.hold.Release(cleanup))
	lease.active, lease.detached = false, true
	return lease.closeErr
}
