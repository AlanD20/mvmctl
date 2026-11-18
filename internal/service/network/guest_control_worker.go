package network

import (
	"context"
	"errors"
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
