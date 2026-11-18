package network

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"mvmctl/internal/service/firewall"
)

// GuestInspect is read-only, including when a previous mutation has an unknown outcome.
func (lease *FloatingIPVMLease) GuestInspect(
	ctx context.Context, resourceID string, expectedGeneration uint64,
) (FloatingIPGuestResult, error) {
	return lease.guestFloatingIP(ctx, resourceID, expectedGeneration, floatingIPDelegationInspect)
}

// GuestAssociate may replace only expired authority. The privileged compositor must first
// call Jailer.RequireRunning and retain that same Jailer parent through network lease Release.
func (lease *FloatingIPVMLease) GuestAssociate(
	ctx context.Context, resourceID string, expectedGeneration uint64,
) (FloatingIPGuestResult, error) {
	return lease.guestFloatingIP(ctx, resourceID, expectedGeneration, floatingIPDelegationAssociate)
}

// GuestRenew retains the ownership generation and exact lease policy, recording the sustaining grant.
// The privileged compositor must first call Jailer.RequireRunning on the retained parent admission.
func (lease *FloatingIPVMLease) GuestRenew(
	ctx context.Context, resourceID string, expectedGeneration uint64,
) (FloatingIPGuestResult, error) {
	return lease.guestFloatingIP(ctx, resourceID, expectedGeneration, floatingIPDelegationRenew)
}

// GuestRelease releases only the exact admitted owner's incarnation.
func (lease *FloatingIPVMLease) GuestRelease(
	ctx context.Context, resourceID string, expectedGeneration uint64,
) (FloatingIPGuestResult, error) {
	return lease.guestFloatingIP(ctx, resourceID, expectedGeneration, floatingIPDelegationRelease)
}

func (lease *FloatingIPVMLease) guestFloatingIP(
	ctx context.Context, resourceID string, generation uint64, method floatingIPDelegationMethod,
) (_ FloatingIPGuestResult, returnErr error) {
	if lease == nil || lease.state == nil {
		return FloatingIPGuestResult{}, networkAuthorityValidationError("floating-IP VM lease is required")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.readyGuest(ctx); err != nil {
		return FloatingIPGuestResult{}, err
	}
	principal := state.guestPrincipal()
	store, err := openFloatingIPStore(ctx, state.network.store, false)
	if err != nil {
		return FloatingIPGuestResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	if store == nil {
		return FloatingIPGuestResult{}, networkAuthorityValidationError("floating-IP reservation is missing")
	}
	record, _, _, found, err := store.read(ctx, resourceID)
	if err != nil {
		return FloatingIPGuestResult{}, err
	}
	if !found || record.Retired || record.NetworkID != principal.networkID || record.OwnerUID != principal.ownerUID {
		return FloatingIPGuestResult{}, networkAuthorityValidationError(
			"floating-IP reservation is unavailable to this guest")
	}
	grants, err := openFloatingIPDelegationStore(ctx, state.network.store, false)
	if err != nil {
		return FloatingIPGuestResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, grants.release(context.WithoutCancel(ctx))) }()
	grant, err := findFloatingIPGuestGrant(ctx, grants, principal, record, method)
	if err != nil {
		return FloatingIPGuestResult{}, err
	}
	if method != floatingIPDelegationInspect && (record.Generation != generation || generation == 0) {
		return FloatingIPGuestResult{}, networkAuthorityValidationError("floating-IP generation is stale")
	}
	if record.Phase != floatingIPOwned && record.Phase != floatingIPUnassociated {
		return FloatingIPGuestResult{}, networkAuthorityCorruptError("floating-IP transition requires root recovery", nil)
	}
	observed, err := state.firewall.ObserveFloatingIPLease(ctx, record.ID, record.Generation)
	if err != nil {
		return FloatingIPGuestResult{}, err
	}
	result, err := floatingIPGuestResult(record, principal, observed)
	if err != nil {
		return FloatingIPGuestResult{}, err
	}
	if method == floatingIPDelegationInspect {
		return result, nil
	}
	if err := admitFloatingIPGuestMutation(record, principal, grant, method, observed); err != nil {
		return FloatingIPGuestResult{}, err
	}
	tx := borrowFloatingIPTransaction(state.network, state.firewall)
	if method == floatingIPDelegationRelease {
		next := record
		next.Revision++
		next.Generation++
		next.Phase = floatingIPFencing
		next.Operation = floatingIPRelease
		next.Owner = floatingIPOwner{}
		next.BeforeOwner = record.Owner
		next.TargetOwner = floatingIPOwner{}
		if err = store.persist(ctx, next, &record); err != nil {
			return FloatingIPGuestResult{}, err
		}
		record, err = fenceFloatingIPOwnership(ctx, tx, store, next)
	} else if method == floatingIPDelegationRenew {
		next := record
		next.Revision++
		next.Phase = floatingIPRenewing
		next.Operation = floatingIPRenew
		next.Owner = floatingIPOwner{}
		next.BeforeOwner = record.Owner
		next.TargetOwner = principal.owner(grant.LeaseDuration, grant.ID, grant.Revision)
		if err = store.persist(ctx, next, &record); err != nil {
			return FloatingIPGuestResult{}, err
		}
		record = next
		// BeforeOwner survives until the one-shot renewal is acknowledged or recovery fences both histories.
		if err = state.firewall.RenewFloatingIPLease(ctx, record.ID, record.Generation); err == nil {
			record, err = commitFloatingIPOwned(ctx, store, record)
		}
	} else {
		next := record
		next.Revision++
		next.Generation++
		next.Phase = floatingIPFencing
		next.Operation = floatingIPAssociate
		next.Owner = floatingIPOwner{}
		next.BeforeOwner = record.Owner
		next.TargetOwner = principal.owner(grant.LeaseDuration, grant.ID, grant.Revision)
		if err = store.persist(ctx, next, &record); err != nil {
			return FloatingIPGuestResult{}, err
		}
		record = next
		if err = tx.firewall.FenceFloatingIP(ctx, floatingIPFirewallBinding(record, floatingIPOwner{})); err != nil {
			return FloatingIPGuestResult{}, err
		}
		if err = tx.invalidateFloatingIPNetworkState(ctx, record); err != nil {
			return FloatingIPGuestResult{}, err
		}
		record, err = advanceFloatingIPPhase(ctx, store, record, floatingIPFenced)
		if err != nil {
			return FloatingIPGuestResult{}, err
		}
		record, err = advanceFloatingIPPhase(ctx, store, record, floatingIPGranting)
		if err != nil {
			return FloatingIPGuestResult{}, err
		}
		if err = state.readyGuest(ctx); err != nil {
			return FloatingIPGuestResult{}, err
		}
		if _, err = findFloatingIPGuestGrant(ctx, grants, principal, record, method); err != nil {
			return FloatingIPGuestResult{}, err
		}
		binding := floatingIPFirewallBinding(record, record.TargetOwner)
		if err = state.firewall.PublishFloatingIPOwner(ctx, binding); err != nil {
			return FloatingIPGuestResult{}, err
		}
		if err = state.firewall.ActivateFloatingIPLease(ctx, record.ID, record.Generation); err == nil {
			record, err = commitFloatingIPOwned(ctx, store, record)
		}
	}
	if err != nil {
		return FloatingIPGuestResult{}, err
	}
	observed, err = state.firewall.ObserveFloatingIPLease(ctx, record.ID, record.Generation)
	if err != nil {
		return FloatingIPGuestResult{}, err
	}
	result, err = floatingIPGuestResult(record, principal, observed)
	if err != nil {
		return FloatingIPGuestResult{}, err
	}
	if method != floatingIPDelegationRelease && result.Ownership != FloatingIPGuestSelf {
		return FloatingIPGuestResult{}, networkAuthorityCorruptError("floating-IP lease expired before acknowledgement", nil)
	}
	return result, nil
}

func (state *floatingIPVMLeaseState) guestPrincipal() floatingIPGuestPrincipal {
	return floatingIPGuestPrincipal{ownerUID: uint32(state.key.ownerUID), networkID: string(state.key.networkID),
		vmID: string(state.key.vmID), bootID: state.incarnation.BootID, launchGeneration: state.incarnation.LaunchGeneration,
		executionUID: state.incarnation.ExecutionUID}
}
func (principal floatingIPGuestPrincipal) owns(owner floatingIPOwner) bool {
	return owner.BootID == principal.bootID && owner.VMID == principal.vmID &&
		owner.LaunchGeneration == principal.launchGeneration && owner.ExecutionUID == principal.executionUID
}

func (principal floatingIPGuestPrincipal) owner(
	duration time.Duration, grantID string, revision uint64,
) floatingIPOwner {
	return floatingIPOwner{
		Origin: floatingIPDelegatedOrigin, GrantID: grantID, GrantRevision: revision, BootID: principal.bootID,
		VMID: principal.vmID, LaunchGeneration: principal.launchGeneration, ExecutionUID: principal.executionUID,
		LeaseDuration: duration,
	}
}
func (state *floatingIPVMLeaseState) readyGuest(ctx context.Context) error {
	if ctx == nil || state.released || state.hold == nil || state.network == nil || state.firewall == nil ||
		state.pin == nil {
		return networkAuthorityValidationError("floating-IP retained VM authority is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	uid, vm, err := state.hold.Binding()
	if err != nil || uid != uint32(state.key.ownerUID) || vm.String() != string(state.key.vmID) {
		return networkAuthorityCorruptError("floating-IP retained VM binding changed", err)
	}
	if err := state.network.mutation.Verify(ctx); err != nil {
		return err
	}
	boot, err := readCurrentBootID(ctx)
	if err != nil || boot != state.incarnation.BootID {
		return networkAuthorityCorruptError("floating-IP VM boot changed", err)
	}
	journal, found, err := state.journal.read(ctx)
	if err != nil || !found {
		return networkAuthorityCorruptError("floating-IP topology journal is unavailable", err)
	}
	if _, err := admitFloatingIPVMJournal(journal, uid, VMID(vm.String()), state.incarnation, boot); err != nil {
		return err
	}
	if err := state.resolveNetwork(journal); err != nil {
		return err
	}
	return nil
}
func findFloatingIPGuestGrant(
	ctx context.Context, store *floatingIPDelegationStore, principal floatingIPGuestPrincipal,
	record floatingIPRecord, method floatingIPDelegationMethod,
) (floatingIPDelegationRecord, error) {
	if store == nil {
		return floatingIPDelegationRecord{}, networkAuthorityValidationError("guest has no floating-IP delegation")
	}
	grants, err := store.list(ctx)
	if err != nil {
		return floatingIPDelegationRecord{}, err
	}
	for _, grant := range grants {
		if grant.ResourceID == record.ID && grant.VMID == principal.vmID && grant.Method == method &&
			grant.State == floatingIPDelegationActive {
			return grant, admitFloatingIPDelegation(principal, record, grant, method)
		}
	}
	return floatingIPDelegationRecord{}, networkAuthorityValidationError("guest floating-IP method is not delegated")
}
func admitFloatingIPGuestMutation(
	record floatingIPRecord, principal floatingIPGuestPrincipal, grant floatingIPDelegationRecord,
	method floatingIPDelegationMethod, observed firewall.FloatingIPLeaseObservation,
) error {
	if method == floatingIPDelegationAssociate {
		if record.Owner != (floatingIPOwner{}) && principal.owns(record.Owner) {
			return networkAuthorityValidationError("current floating-IP owner must renew instead of associate")
		}
		if observed.Present && observed.Remaining > 0 {
			return networkAuthorityValidationError("floating IP has a healthy current owner")
		}
	} else {
		if record.Phase != floatingIPOwned || !principal.owns(record.Owner) {
			return networkAuthorityValidationError("guest does not own this floating-IP incarnation")
		}
		if method == floatingIPDelegationRenew && record.Owner.LeaseDuration != grant.LeaseDuration {
			return networkAuthorityValidationError("delegated renewal cannot change lease policy")
		}
	}
	return nil
}
func floatingIPFirewallBinding(record floatingIPRecord, owner floatingIPOwner) firewall.FloatingIPBinding {
	return firewall.FloatingIPBinding{OwnerUID: record.OwnerUID, ID: record.ID, NetworkID: record.NetworkID,
		IPv4: netip.MustParseAddr(record.IPv4), Generation: record.Generation, OwnerVMID: owner.VMID,
		OwnerLaunchGeneration: owner.LaunchGeneration, LeaseDuration: owner.LeaseDuration}
}
func borrowFloatingIPTransaction(
	network *managedNetworkLease, mutation *firewall.MutationLease,
) *managedNetworkTransaction {
	return &managedNetworkTransaction{key: network.key, mutation: network.mutation, store: network.store,
		host: network.host, firewall: mutation, route: newDirectRouteNetlink(), record: network.record, found: true}
}
func advanceFloatingIPPhase(
	ctx context.Context, store *floatingIPStore, record floatingIPRecord, phase floatingIPPhase,
) (floatingIPRecord, error) {
	next := record
	next.Revision++
	next.Phase = phase
	if err := store.persist(ctx, next, &record); err != nil {
		return record, err
	}
	return next, nil
}
func commitFloatingIPOwned(
	ctx context.Context, store *floatingIPStore, record floatingIPRecord,
) (floatingIPRecord, error) {
	next := record
	next.Revision++
	next.Phase = floatingIPOwned
	next.Operation = ""
	next.Owner = record.TargetOwner
	next.BeforeOwner = floatingIPOwner{}
	next.TargetOwner = floatingIPOwner{}
	if err := store.persist(ctx, next, &record); err != nil {
		return record, err
	}
	return next, nil
}
