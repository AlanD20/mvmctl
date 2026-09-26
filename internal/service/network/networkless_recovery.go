package network

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
	"mvmctl/internal/lib/system"
)

// RecoverNetworkless re-admits only an exact same-boot namespace recorded for this VM generation.
func RecoverNetworkless(
	ctx context.Context,
	hold *authorityfs.TopologyHold,
	generation uint64,
) (*NetworklessNamespaceLease, error) {
	return recoverNetworkless(ctx, hold, generation, nil)
}

// RecoverNetworklessWithPriorBootAbsence retires old durable authority after root proves the VM and namespace absent.
func RecoverNetworklessWithPriorBootAbsence(
	ctx context.Context, hold *authorityfs.TopologyHold, generation uint64, proof *system.PriorBootVMAbsence,
) (*NetworklessNamespaceLease, error) {
	if proof == nil {
		return nil, networkAuthorityValidationError("prior-boot networkless recovery requires VM absence proof")
	}
	return recoverNetworkless(ctx, hold, generation, proof)
}

func recoverNetworkless(
	ctx context.Context, hold *authorityfs.TopologyHold, generation uint64, proof *system.PriorBootVMAbsence,
) (result *NetworklessNamespaceLease, returnErr error) {
	if ctx == nil || hold == nil || generation == 0 {
		return nil, networkAuthorityValidationError("networkless recovery requires VM hold and launch generation")
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
	lease := &NetworklessNamespaceLease{hold: consumed, key: key, generation: generation,
		effect: newNamespaceEffect(), recoveryPending: true}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, lease.discardRejectedRecovery(context.WithoutCancel(ctx)))
			result = nil
		}
	}()
	if err := lease.openJournal(ctx, false); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return lease.finishAbsentRecovery(ctx)
		}
		return lease, err
	}
	record, found, err := lease.journal.read(ctx)
	if err != nil {
		return lease, err
	}
	if !found {
		return lease.finishAbsentRecovery(ctx)
	}
	admitted := append([]byte(nil), lease.journal.raw...)
	if record.LaunchGeneration != generation {
		return lease, networkAuthorityCorruptError("networkless generation differs from root VM authority", nil)
	}
	bootID, err := readCurrentBootID(ctx)
	if err != nil {
		return lease, err
	}
	if record.BootID != bootID {
		if proof == nil {
			return lease, networkAuthorityCorruptError("prior-boot networkless state requires VM absence proof", nil)
		}
		if err := lease.retirePriorBoot(ctx, record, bootID, proof); err != nil {
			return lease, err
		}
		return lease.finishAbsentRecovery(ctx)
	}
	if record.Lifecycle == networklessCleaned {
		return lease.finishAbsentRecovery(ctx)
	}
	var token [32]byte
	decoded, err := hex.DecodeString(record.Token)
	if err != nil || len(decoded) != len(token) {
		return lease, networkAuthorityCorruptError("networkless token is invalid", err)
	}
	copy(token[:], decoded)
	if record.Namespace == (NamespaceHandle{}) {
		if record.Target == (topologyNamespaceTarget{}) {
			if err := requireNetworklessNamespaceAbsent(ctx, lease.hold); err != nil {
				return lease, err
			}
		} else {
			value, err := lease.recoverPlaceholder(ctx, namespaceTargetFromJournal(record.Target), token)
			if err != nil {
				return lease, err
			}
			lease.target = &networklessTargetLease{value: value}
		}
		if err := requireNetworklessHostEffectsAbsent(ctx, lease.key, lease.effect); err != nil {
			return lease, err
		}
		lease.recoveryPending = false
		if err := lease.Cleanup(ctx); err != nil {
			return lease, err
		}
		return nil, nil
	}
	handle := namespaceHandle{path: namespaceHandlePath(record.Namespace.Path),
		identity: namespaceIdentity{device: record.Namespace.Identity.Device, inode: record.Namespace.Identity.Inode},
		ownerUID: record.Namespace.OwnerUID, ownerGID: record.Namespace.OwnerGID, mode: record.Namespace.Mode}
	pin, err := lease.effect.recoverNamespace(ctx, key, handle, namespaceTargetFromJournal(record.Target), lease.hold)
	if err != nil {
		return lease, err
	}
	lease.pin = pin
	pin.state.mu.Lock()
	pin.state.networklessMountID = record.NamespaceMountID
	pin.state.mu.Unlock()
	recovered, ok := pin.state.target.(*recoveredNamespaceTarget)
	if !ok || recovered.value == nil {
		return lease, networkAuthorityCorruptError("networkless recovery lacks concrete target", nil)
	}
	if err := recovered.value.ArmNetworklessEvidence(ctx, token, record.NamespaceMountID); err != nil {
		discardErr := pin.discardRecovery(context.WithoutCancel(ctx))
		lease.pin = nil
		return lease, errors.Join(err, discardErr)
	}
	if record.Lifecycle == networklessActive && pin.Handle() == (namespaceHandle{}) {
		return lease, networkAuthorityCorruptError("active networkless namespace mount is absent", nil)
	}
	if pin.Handle() != (namespaceHandle{}) {
		if _, err := inspectNetworklessNamespace(ctx, lease.effect, pin, true); err != nil {
			if record.LoopbackUp {
				return lease, err
			}
			if _, downErr := inspectNetworklessNamespace(ctx, lease.effect, pin, false); downErr != nil {
				return lease, errors.Join(err, downErr)
			}
		}
	}
	if err := requireNetworklessHostEffectsAbsent(ctx, lease.key, lease.effect); err != nil {
		return lease, err
	}
	_, found, err = lease.journal.read(ctx)
	if err != nil || !found || !bytes.Equal(admitted, lease.journal.raw) {
		return lease, networkAuthorityCorruptError("networkless journal changed before recovery admission", err)
	}
	lease.active = record.Lifecycle == networklessActive
	lease.recoveryPending = false
	return lease, nil
}

// discardRejectedRecovery closes only retained recovery authority. The durable record and all host effects remain
// untouched, so a later recovery under a new VM hold can inspect the same evidence again.
func (lease *NetworklessNamespaceLease) discardRejectedRecovery(ctx context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.detached {
		return lease.closeErr
	}
	var closeErr error
	if lease.pin != nil {
		closeErr = errors.Join(closeErr, lease.pin.discardRecovery(ctx))
		lease.pin = nil
	}
	if lease.target != nil {
		closeErr = errors.Join(closeErr, lease.target.value.Discard(ctx))
		lease.target = nil
	}
	lease.closeErr = errors.Join(closeErr, lease.releaseAuthorities(ctx))
	lease.active, lease.recoveryPending, lease.detached = false, false, true
	return lease.closeErr
}

func (lease *NetworklessNamespaceLease) recoverPlaceholder(
	ctx context.Context, target namespaceTargetIdentity, token [32]byte,
) (_ *authorityfs.NetworklessNamespaceTarget, returnErr error) {
	root, err := authorityfs.OpenNetwork(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Release(context.WithoutCancel(ctx))) }()
	owner, err := root.OpenUID(ctx, uint32(lease.key.ownerUID))
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, owner.Release(context.WithoutCancel(ctx))) }()
	receipt := authorityfs.NetworkNamespaceTargetIdentity{
		DirectoryDevice: target.directoryDevice,
		DirectoryInode:  target.directoryInode,
		DirectoryMount: authorityfs.NetworkNamespaceMountIdentity{
			Kind:  authorityfs.NetworkNamespaceMountIdentityKind(target.directoryMount.kind),
			Value: target.directoryMount.value,
		},
		PlaceholderDevice: target.placeholderDevice,
		PlaceholderInode:  target.placeholderInode,
		PlaceholderMount: authorityfs.NetworkNamespaceMountIdentity{
			Kind:  authorityfs.NetworkNamespaceMountIdentityKind(target.placeholderMount.kind),
			Value: target.placeholderMount.value,
		},
	}
	return owner.OpenNetworklessPlaceholderRecovery(ctx, lease.hold, receipt, token)
}

func (lease *NetworklessNamespaceLease) finishAbsentRecovery(ctx context.Context) (*NetworklessNamespaceLease, error) {
	if err := requireNetworklessNamespaceAbsent(ctx, lease.hold); err != nil {
		return lease, err
	}
	if err := requireNetworklessHostEffectsAbsent(ctx, lease.key, lease.effect); err != nil {
		return lease, err
	}
	if lease.journal != nil && lease.journal.raw != nil {
		admitted := append([]byte(nil), lease.journal.raw...)
		_, found, err := lease.journal.read(ctx)
		if err != nil || !found || !bytes.Equal(admitted, lease.journal.raw) {
			return lease, networkAuthorityCorruptError("networkless journal changed during recovery", err)
		}
	}
	lease.cleaned, lease.recoveryPending = true, false
	lease.closeErr = lease.releaseAuthorities(context.WithoutCancel(ctx))
	if lease.closeErr != nil {
		return lease, lease.closeErr
	}
	return nil, nil
}

func (lease *NetworklessNamespaceLease) retirePriorBoot(
	ctx context.Context, record networklessJournalRecord, currentBoot string, proof *system.PriorBootVMAbsence,
) error {
	verify := func() error {
		observed, err := proof.Verify(ctx, lease.hold, record.BootID, record.LaunchGeneration)
		if err != nil || observed != currentBoot || currentBoot == record.BootID {
			return networkAuthorityCorruptError("prior-boot VM absence proof changed", err)
		}
		return nil
	}
	if err := verify(); err != nil {
		return err
	}
	requireAbsent := func() (returnErr error) {
		if err := requireNetworklessNamespaceAbsent(ctx, lease.hold); err != nil {
			return err
		}
		return requireNetworklessHostEffectsAbsent(ctx, lease.key, lease.effect)
	}
	if err := requireAbsent(); err != nil {
		return err
	}
	if err := verify(); err != nil {
		return err
	}
	if err := requireAbsent(); err != nil {
		return err
	}
	record.Lifecycle, record.Pending = networklessCleaned, ""
	// Old inode numbers are historical evidence. No old-boot kernel identity is ever used for deletion.
	return lease.journal.persist(ctx, record)
}

func requireNetworklessNamespaceAbsent(ctx context.Context, hold *authorityfs.TopologyHold) (returnErr error) {
	ownerUID, _, err := hold.Binding()
	if err != nil {
		return err
	}
	root, err := authorityfs.OpenNetwork(ctx)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, root.Release(context.WithoutCancel(ctx))) }()
	owner, err := root.OpenUID(ctx, ownerUID)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, owner.Release(context.WithoutCancel(ctx))) }()
	return owner.RequireNamespaceAbsent(ctx, hold)
}

func requireNetworklessHostEffectsAbsent(
	ctx context.Context,
	key namespaceKey,
	effect *namespaceEffect,
) (returnErr error) {
	retained, err := retainHostLinkNamespace(ctx, effect.deps, effect.policy)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, retained.close(context.WithoutCancel(ctx))) }()
	fd, _, err := retained.descriptor()
	if err != nil {
		return err
	}
	route := newDirectRouteNetlink()
	for _, role := range []struct{ prefix, name string }{
		{"mvh", "host-veth"}, {"mvn", "namespace-veth"}, {"mvl", "local-bridge"}, {"mvt", "tap"},
	} {
		name := deriveVMLinkName(role.prefix, key.ownerUID, key.vmID, role.name)
		link, err := route.inspectLink(ctx, fd, name)
		if err != nil {
			return err
		}
		if link != nil {
			return networkAuthorityCorruptError("networkless VM has an mvm-owned host endpoint effect", nil)
		}
	}
	return nil
}

// DetachActive retains the durable record and mounted namespace for a later exact RecoverNetworkless call.
func (lease *NetworklessNamespaceLease) DetachActive(ctx context.Context) error {
	if lease == nil {
		return networkAuthorityCorruptError("networkless namespace lease is unavailable", nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.detached {
		return lease.closeErr
	}
	if err := lease.admitHandoff(ctx); err != nil {
		return err
	}
	record := lease.journal.record
	probe, err := lease.effect.recoverNamespace(ctx, lease.key, namespaceHandle{
		path:     namespaceHandlePath(record.Namespace.Path),
		identity: namespaceIdentity{device: record.Namespace.Identity.Device, inode: record.Namespace.Identity.Inode},
		ownerUID: record.Namespace.OwnerUID, ownerGID: record.Namespace.OwnerGID, mode: record.Namespace.Mode,
	}, namespaceTargetFromJournal(record.Target), lease.hold)
	if err != nil {
		return err
	}
	var token [32]byte
	decoded, err := hex.DecodeString(record.Token)
	if err != nil || len(decoded) != len(token) {
		return errors.Join(networkAuthorityCorruptError("networkless detach token is invalid", err),
			probe.discardRecovery(context.WithoutCancel(ctx)))
	}
	copy(token[:], decoded)
	recovered, ok := probe.state.target.(*recoveredNamespaceTarget)
	if !ok || recovered.value == nil {
		return errors.Join(networkAuthorityCorruptError("networkless detach probe is incomplete", nil),
			probe.discardRecovery(context.WithoutCancel(ctx)))
	}
	if err := recovered.value.ArmNetworklessEvidence(ctx, token, record.NamespaceMountID); err != nil {
		return errors.Join(err, probe.discardRecovery(context.WithoutCancel(ctx)))
	}
	if err := probe.detachActive(ctx); err != nil {
		return errors.Join(err, probe.discardRecovery(context.WithoutCancel(ctx)))
	}
	detachErr := lease.pin.detachNetworklessActive(ctx)
	lease.pin.state.mu.Lock()
	released := lease.pin.state.released
	lease.pin.state.mu.Unlock()
	if !released {
		return detachErr
	}
	// A terminal descriptor-close error does not leave a usable pin behind. Keep the durable
	// active journal for later recovery, but retire this in-process capability exactly once.
	lease.closeErr = errors.Join(detachErr, lease.releaseAuthorities(context.WithoutCancel(ctx)))
	lease.active, lease.detached = false, true
	return lease.closeErr
}
