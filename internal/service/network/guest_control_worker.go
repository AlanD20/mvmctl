package network

import (
	"context"
	"errors"

	"mvmctl/internal/lib/guestcontrolwire"
)

// HasActiveGuestControlDelegations observes only grants for this genuinely admitted incarnation.
// The caller releases this lease before starting or waiting for a worker.
func (lease *FloatingIPVMLease) HasActiveGuestControlDelegations(ctx context.Context) (_ bool, returnErr error) {
	if lease == nil || lease.state == nil {
		return false, networkAuthorityValidationError("worker grant admission is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.readyGuest(ctx); err != nil {
		return false, err
	}
	principal := state.guestPrincipal()
	grants, err := openFloatingIPDelegationStore(ctx, state.network.store, false)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, grants.release(context.WithoutCancel(ctx))) }()
	if aliases, err := state.operatorFloatingIPAliases(ctx); err != nil {
		return false, err
	} else if len(aliases) > 0 {
		return true, nil
	}
	if grants == nil {
		return false, nil
	}
	records, err := grants.list(ctx)
	if err != nil {
		return false, err
	}
	resources, err := openFloatingIPStore(ctx, state.network.store, false)
	if err != nil {
		return false, err
	}
	defer func() { returnErr = errors.Join(returnErr, resources.release(context.WithoutCancel(ctx))) }()
	if resources == nil {
		return false, nil
	}
	for _, grant := range records {
		if grant.State != floatingIPDelegationActive || grant.OwnerUID != principal.ownerUID ||
			grant.NetworkID != principal.networkID ||
			grant.VMID != principal.vmID ||
			grant.BootID != principal.bootID ||
			grant.LaunchGeneration != principal.launchGeneration ||
			grant.ExecutionUID != principal.executionUID {
			continue
		}
		resource, _, _, found, err := resources.read(ctx, grant.ResourceID)
		if err != nil {
			return false, err
		}
		if !found || resource.Retired {
			continue
		}
		if err := admitFloatingIPDelegation(principal, resource, grant, grant.Method); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// OperatorFloatingIPAliases returns only freshly observed operator authority for this admitted incarnation.
// Its value is a synchronization payload, never transferable permission to mutate the host.
func (lease *FloatingIPVMLease) OperatorFloatingIPAliases(
	ctx context.Context,
) ([]guestcontrolwire.FloatingIPAlias, error) {
	if lease == nil || lease.state == nil {
		return nil, networkAuthorityValidationError("alias synchronization requires VM admission")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.readyGuest(ctx); err != nil {
		return nil, err
	}
	return state.operatorFloatingIPAliases(ctx)
}

func (state *floatingIPVMLeaseState) operatorFloatingIPAliases(
	ctx context.Context,
) (_ []guestcontrolwire.FloatingIPAlias, returnErr error) {
	store, err := openFloatingIPStore(ctx, state.network.store, false)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	records, err := store.list(ctx)
	if err != nil {
		return nil, err
	}
	aliases := make([]guestcontrolwire.FloatingIPAlias, 0)
	principal := state.guestPrincipal()
	for _, record := range records {
		if record.Retired || record.OwnerUID != principal.ownerUID || record.NetworkID != principal.networkID ||
			record.Phase != floatingIPOwned ||
			record.Owner.Origin != floatingIPOperatorOrigin ||
			!principal.owns(record.Owner) {
			continue
		}
		observed, err := state.firewall.ObserveFloatingIPLease(ctx, record.ID, record.Generation)
		if err != nil {
			return nil, err
		}
		if !observed.Present || observed.Binding != floatingIPFirewallBinding(record, record.Owner) {
			return nil, networkAuthorityCorruptError("operator alias has no matching packet authority", nil)
		}
		aliases = append(
			aliases,
			guestcontrolwire.FloatingIPAlias{ResourceID: record.ID, IPv4: record.IPv4, Generation: record.Generation},
		)
	}
	return aliases, nil
}
