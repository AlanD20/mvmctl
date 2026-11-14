package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/system"
)

// NetworklessNamespaceLease retains the VM hold, exact namespace pin, and a distinct root-owned lifecycle record.
// Every error after hold transfer returns the lease for retryable cleanup or recovery.
type NetworklessNamespaceLease struct {
	mu              sync.Mutex
	hold            *authorityfs.TopologyHold
	key             namespaceKey
	generation      uint64
	effect          *namespaceEffect
	storeRoot       *networkStoreRoot
	storeUID        *networkStoreUID
	journal         *networklessJournal
	target          *networklessTargetLease
	pin             *namespacePin
	active          bool
	cleaned         bool
	detached        bool
	recoveryPending bool
	closeErr        error
}

// NetworklessJailerNamespaceLease borrows the active lease and never exposes a namespace path or descriptor.
type NetworklessJailerNamespaceLease struct{ owner *NetworklessNamespaceLease }

// NetworklessNamespaceBinding is the exact VM and nsfs identity admitted for the blocked Jailer child.
type NetworklessNamespaceBinding struct {
	OwnerUID        uint32
	VMID            string
	NamespaceDevice uint64
	NamespaceInode  uint64
}

type networklessTargetLease struct {
	value *authorityfs.NetworklessNamespaceTarget
}

func (target *networklessTargetLease) targetIdentity(ctx context.Context) (namespaceTargetIdentity, error) {
	if target == nil || target.value == nil {
		return namespaceTargetIdentity{}, errors.New("networkless target is unavailable")
	}
	receipt, err := target.value.LinkedIdentity(ctx)
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	return mapNetworklessTargetReceipt(receipt)
}

func (target *networklessTargetLease) remove(ctx context.Context) error {
	return target.value.Remove(ctx)
}
func (target *networklessTargetLease) release(ctx context.Context) error {
	return target.value.Release(ctx)
}

func mapNetworklessTargetReceipt(receipt authorityfs.NetworkNamespaceTargetIdentity) (namespaceTargetIdentity, error) {
	directoryMount, err := mapNamespaceTargetMountIdentity(receipt.DirectoryMount)
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	placeholderMount, err := mapNamespaceTargetMountIdentity(receipt.PlaceholderMount)
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	return namespaceTargetIdentity{directoryDevice: receipt.DirectoryDevice, directoryInode: receipt.DirectoryInode,
		directoryMount: directoryMount, placeholderDevice: receipt.PlaceholderDevice,
		placeholderInode: receipt.PlaceholderInode, placeholderMount: placeholderMount}, nil
}

type networklessPublication struct {
	target  *networklessTargetLease
	journal *networklessJournal
	key     namespaceKey
}

func (publication *networklessPublication) recordDetachedMount(
	ctx context.Context, identity namespaceIdentity, mountID uint64,
) error {
	if publication == nil || publication.journal == nil || publication.target == nil || mountID == 0 ||
		!validNamespaceIdentity(identity) || publication.journal.record.Pending != "create_namespace" {
		return networkAuthorityCorruptError("networkless detached mount checkpoint is invalid", nil)
	}
	record := publication.journal.record
	record.Namespace = exportNamespaceHandle(namespaceHandle{path: deriveNamespaceHandle(publication.key),
		identity: identity, ownerUID: 0, ownerGID: 0, mode: namespaceHandleMode})
	record.NamespaceMountID = mountID
	record.Pending = "publish_mount"
	return publication.journal.persist(ctx, record)
}

// PrepareNetworkless consumes the owner-bound VM hold and creates one loopback-only per-VM namespace.
func PrepareNetworkless(
	ctx context.Context,
	hold *authorityfs.TopologyHold,
	generation uint64,
) (*NetworklessNamespaceLease, error) {
	if ctx == nil || hold == nil || generation == 0 {
		return nil, networkAuthorityValidationError("networkless preparation requires VM hold and launch generation")
	}
	owner, vm, err := hold.Binding()
	if err != nil {
		return nil, err
	}
	key, err := newNamespaceKey(owner, vm.String())
	if err != nil {
		return nil, err
	}
	consumed, err := hold.Transfer(ctx)
	if err != nil {
		return nil, err
	}
	lease := &NetworklessNamespaceLease{hold: consumed, key: key, generation: generation, effect: newNamespaceEffect()}
	if err := lease.prepare(ctx); err != nil {
		return lease, err
	}
	return lease, nil
}

func (lease *NetworklessNamespaceLease) openJournal(ctx context.Context, create bool) error {
	var err error
	lease.storeRoot, err = openNetworkStoreRoot(ctx, realNetworkStoreDeps(), productionNetworkStorePolicy())
	if err != nil {
		return err
	}
	lease.storeUID, err = lease.storeRoot.openUID(ctx, uint32(lease.key.ownerUID), create)
	if err != nil {
		return err
	}
	lease.journal, err = openNetworklessJournal(ctx, lease.storeUID, lease.key.vmID, create)
	return err
}

func (lease *NetworklessNamespaceLease) prepare(ctx context.Context) error {
	if err := lease.openJournal(ctx, true); err != nil {
		return err
	}
	if err := requireNetworklessHostEffectsAbsent(ctx, lease.key, lease.effect); err != nil {
		return err
	}
	prior, found, err := lease.journal.read(ctx)
	if err != nil {
		return err
	}
	if found && (prior.Lifecycle != networklessCleaned || prior.LaunchGeneration >= lease.generation) {
		return networkAuthorityCorruptError("existing networkless namespace requires reconciliation", nil)
	}
	bootID, err := readCurrentBootID(ctx)
	if err != nil {
		return err
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	record := networklessJournalRecord{Schema: networklessJournalSchema, Lifecycle: networklessPreparing,
		OwnerUID: uint32(lease.key.ownerUID), VMID: string(lease.key.vmID), BootID: bootID,
		LaunchGeneration: lease.generation, Token: hex.EncodeToString(token[:]), Pending: "prepare_target"}
	if err := lease.journal.persist(ctx, record); err != nil {
		return err
	}
	receipt, err := lease.prepareTarget(ctx, token)
	if err != nil {
		return err
	}
	record = lease.journal.record
	record.Target = topologyNamespaceTarget{DirectoryDevice: receipt.directoryDevice,
		DirectoryInode: receipt.directoryInode, DirectoryMountKind: uint8(receipt.directoryMount.kind),
		DirectoryMount: receipt.directoryMount.value, PlaceholderDevice: receipt.placeholderDevice,
		PlaceholderInode: receipt.placeholderInode, PlaceholderMountKind: uint8(receipt.placeholderMount.kind),
		PlaceholderMount: receipt.placeholderMount.value}
	record.Pending = "link_target"
	if err := lease.journal.persist(ctx, record); err != nil {
		return err
	}
	if err := requireNetworklessMountAPIs(); err != nil {
		return err
	}
	if err := lease.target.value.Link(ctx); err != nil {
		return err
	}
	record = lease.journal.record
	record.Pending = "create_namespace"
	if err := lease.journal.persist(ctx, record); err != nil {
		return err
	}
	pin, effectErr := lease.effect.CreateNetworklessNamespace(ctx, namespaceCreateSpec{key: lease.key},
		&networklessPublication{target: lease.target, journal: lease.journal, key: lease.key})
	if concrete, ok := pin.(*namespacePin); ok {
		lease.pin = concrete
	}
	if effectErr != nil {
		return effectErr
	}
	if lease.pin == nil {
		return networkAuthorityCorruptError("networkless namespace effect returned no pin", nil)
	}
	if err := lease.verifyPin(ctx); err != nil {
		return err
	}
	if err := lease.persistPin(ctx, "raise_loopback"); err != nil {
		return err
	}
	if err := lease.raiseLoopback(ctx); err != nil {
		return err
	}
	if _, err := inspectNetworklessNamespace(ctx, lease.effect, lease.pin, true); err != nil {
		return err
	}
	if err := requireNetworklessHostEffectsAbsent(ctx, lease.key, lease.effect); err != nil {
		return err
	}
	record = lease.journal.record
	record.Lifecycle, record.Pending, record.LoopbackUp = networklessActive, "", true
	if err := lease.journal.persist(ctx, record); err != nil {
		return err
	}
	lease.active = true
	return nil
}

func (lease *NetworklessNamespaceLease) prepareTarget(
	ctx context.Context, token [32]byte,
) (_ namespaceTargetIdentity, returnErr error) {
	root, err := authorityfs.OpenNetwork(ctx)
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Release(context.WithoutCancel(ctx))) }()
	owner, err := root.OpenUID(ctx, uint32(lease.key.ownerUID))
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, owner.Release(context.WithoutCancel(ctx))) }()
	value, err := owner.PrepareNetworklessNamespaceTarget(ctx, lease.hold, token)
	if value != nil {
		lease.target = &networklessTargetLease{value: value}
	}
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	if lease.target == nil {
		return namespaceTargetIdentity{}, networkAuthorityCorruptError("anonymous networkless target is absent", nil)
	}
	receipt, err := value.TargetIdentity(ctx)
	if err != nil {
		return namespaceTargetIdentity{}, err
	}
	return mapNetworklessTargetReceipt(receipt)
}

func requireNetworklessMountAPIs() error {
	if _, err := unix.OpenTree(-1, "", uint(namespaceOpenTreeFlags)); !errors.Is(err, unix.EBADF) {
		return networkAuthorityEffectError("open_tree feature check failed", err)
	}
	if err := unix.MoveMount(-1, "", -1, "", namespaceMoveMountFlags); !errors.Is(err, unix.EBADF) {
		return networkAuthorityEffectError("move_mount feature check failed", err)
	}
	return nil
}

func (lease *NetworklessNamespaceLease) persistPin(ctx context.Context, pending string) error {
	state := lease.pin.state
	state.mu.Lock()
	handle, target := state.handle, state.targetID
	state.mu.Unlock()
	if err := validateNamespaceHandle(handle, deriveNamespaceHandle(lease.key)); err != nil {
		return err
	}
	record := lease.journal.record
	if record.Pending != "publish_mount" || record.NamespaceMountID == 0 ||
		record.Namespace != exportNamespaceHandle(handle) || target != namespaceTargetFromJournal(record.Target) {
		return networkAuthorityCorruptError("networkless pin differs from prepublication receipt", nil)
	}
	record.Pending = pending
	return lease.journal.persist(ctx, record)
}

func (lease *NetworklessNamespaceLease) raiseLoopback(ctx context.Context) (returnErr error) {
	retained, err := lease.effect.retainLinkNamespace(ctx, lease.pin)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, retained.close(context.WithoutCancel(ctx))) }()
	fd, _, err := retained.descriptor()
	if err != nil {
		return err
	}
	route := newDirectRouteNetlink()
	links, err := route.listLinks(ctx, fd)
	if err != nil {
		return err
	}
	if len(links) != 1 || validateExactLoopback(links[0]) != nil || links[0].flags&unix.IFF_UP != 0 {
		return networkAuthorityCorruptError("new networkless namespace is not exactly one down loopback", nil)
	}
	return route.setLinkUp(ctx, fd, links[0].ifIndex, true)
}

// JailerNamespace returns handoff authority only after durable active state and complete loopback inspection.
func (lease *NetworklessNamespaceLease) JailerNamespace(ctx context.Context) (*NetworklessJailerNamespaceLease, error) {
	if lease == nil {
		return nil, networkAuthorityCorruptError("networkless namespace lease is unavailable", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.admitHandoff(ctx); err != nil {
		return nil, err
	}
	return &NetworklessJailerNamespaceLease{owner: lease}, nil
}

func (lease *NetworklessNamespaceLease) admitHandoff(ctx context.Context) error {
	if ctx == nil || !lease.active || lease.cleaned || lease.detached || lease.recoveryPending ||
		lease.pin == nil || lease.journal == nil || lease.journal.record.Lifecycle != networklessActive {
		return networkAuthorityCorruptError("networkless namespace is not at the Jailer handoff checkpoint", nil)
	}
	if _, _, err := lease.hold.Binding(); err != nil {
		return err
	}
	if err := lease.verifyPin(ctx); err != nil {
		return err
	}
	if err := requireNetworklessHostEffectsAbsent(ctx, lease.key, lease.effect); err != nil {
		return err
	}
	_, err := inspectNetworklessNamespace(ctx, lease.effect, lease.pin, true)
	return err
}

func (lease *NetworklessNamespaceLease) verifyPin(ctx context.Context) error {
	state := lease.pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if lease.journal == nil || lease.journal.record.NamespaceMountID == 0 ||
		state.networklessMountID != lease.journal.record.NamespaceMountID {
		return networkAuthorityCorruptError("networkless pin lacks recorded mount identity", nil)
	}
	return verifyPublishedNamespace(ctx, state)
}

func (handoff *NetworklessJailerNamespaceLease) Binding(ctx context.Context) (NetworklessNamespaceBinding, error) {
	if handoff == nil || handoff.owner == nil {
		return NetworklessNamespaceBinding{}, networkAuthorityCorruptError(
			"networkless Jailer capability is unavailable",
			nil,
		)
	}
	lease := handoff.owner
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.admitHandoff(ctx); err != nil {
		return NetworklessNamespaceBinding{}, err
	}
	handle := lease.pin.Handle()
	return NetworklessNamespaceBinding{OwnerUID: uint32(lease.key.ownerUID), VMID: string(lease.key.vmID),
		NamespaceDevice: handle.identity.device, NamespaceInode: handle.identity.inode}, nil
}

func (handoff *NetworklessJailerNamespaceLease) SendToJailer(
	ctx context.Context,
	target *system.JailerChildNamespaceTarget,
) error {
	if handoff == nil || handoff.owner == nil || target == nil {
		return networkAuthorityCorruptError("networkless Jailer capability or target is unavailable", nil)
	}
	lease := handoff.owner
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.admitHandoff(ctx); err != nil {
		return err
	}
	state := lease.pin.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return target.SendNetworkNamespace(ctx, state.netnsFD)
}

func (handoff *NetworklessJailerNamespaceLease) VerifyJailerNamespace(ctx context.Context, device, inode uint64) error {
	if handoff == nil || handoff.owner == nil {
		return networkAuthorityCorruptError("networkless Jailer capability is unavailable", nil)
	}
	lease := handoff.owner
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.admitHandoff(ctx); err != nil {
		return err
	}
	if lease.pin.Handle().identity != (namespaceIdentity{device: device, inode: inode}) {
		return networkAuthorityCorruptError("Jailer child namespace differs from networkless pin", nil)
	}
	return nil
}

// Cleanup removes only the exact pinned namespace after the caller has torn down the child and sockets.
func (lease *NetworklessNamespaceLease) Cleanup(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.cleaned {
		return lease.closeErr
	}
	if ctx == nil || lease.detached || lease.recoveryPending {
		return networkAuthorityCorruptError("networkless cleanup authority is unavailable", nil)
	}
	cleanup := context.WithoutCancel(ctx)
	expectUp := lease.active || (lease.journal != nil && lease.journal.record.LoopbackUp)
	if lease.journal != nil && lease.journal.ambiguous {
		return networkAuthorityCorruptError("networkless journal publication requires recovery", nil)
	}
	if lease.journal != nil && lease.journal.raw != nil && lease.journal.record.Lifecycle != networklessCleaned {
		record := lease.journal.record
		record.Lifecycle, record.Pending = networklessCleaning, "delete_namespace"
		if err := lease.journal.persist(cleanup, record); err != nil {
			return err
		}
	}
	var terminalCloseErr error
	if lease.pin != nil {
		if lease.pin.Handle() != (namespaceHandle{}) {
			if _, err := inspectNetworklessNamespace(cleanup, lease.effect, lease.pin, true); err != nil {
				if expectUp {
					return err
				}
				if _, downErr := inspectNetworklessNamespace(cleanup, lease.effect, lease.pin, false); downErr != nil {
					return errors.Join(err, downErr)
				}
			}
		}
		if err := lease.effect.DeleteNamespace(cleanup, lease.pin); err != nil {
			return err
		}
		terminalCloseErr = lease.pin.Release(cleanup)
		// Release is close-once even when its first close reports an error. Deletion is already confirmed.
		lease.pin = nil
		lease.target = nil
	} else if lease.target != nil {
		if err := lease.target.remove(cleanup); err != nil {
			return err
		}
		terminalCloseErr = lease.target.release(cleanup)
		lease.target = nil
	}
	if lease.journal != nil && lease.journal.raw != nil && lease.journal.record.Lifecycle != networklessCleaned {
		record := lease.journal.record
		record.Lifecycle, record.Pending = networklessCleaned, ""
		if err := lease.journal.persist(cleanup, record); err != nil {
			return errors.Join(terminalCloseErr, err)
		}
	}
	lease.active = false
	lease.cleaned = true
	lease.closeErr = errors.Join(terminalCloseErr, lease.releaseAuthorities(cleanup))
	return lease.closeErr
}

func (lease *NetworklessNamespaceLease) releaseAuthorities(ctx context.Context) error {
	var result error
	if lease.journal != nil {
		result = errors.Join(result, lease.journal.store.Release(ctx))
	}
	if lease.storeUID != nil {
		result = errors.Join(result, lease.storeUID.Release(ctx))
	}
	if lease.storeRoot != nil {
		result = errors.Join(result, lease.storeRoot.Release(ctx))
	}
	if lease.hold != nil {
		result = errors.Join(result, lease.hold.Release(ctx))
	}
	return result
}
