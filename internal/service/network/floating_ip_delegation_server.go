package network

import (
	"context"
	"errors"
	"time"

	"mvmctl/pkg/errs"
)

// GrantFloatingIPDelegation publishes stored permission under genuine retained target VM authority.
// The privileged compositor retains its Jailer parent; this method never starts a worker or grants an address.
func (lease *FloatingIPVMLease) GrantFloatingIPDelegation(
	ctx context.Context, request FloatingIPDelegationGrantRequest,
) (_ FloatingIPDelegationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	if lease == nil || lease.state == nil {
		return FloatingIPDelegationResult{}, networkAuthorityValidationError("VM admission is required")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.readyGuest(ctx); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	principal := state.guestPrincipal()
	if request.NetworkID != principal.networkID || request.VMID != principal.vmID {
		return FloatingIPDelegationResult{}, networkAuthorityValidationError(
			"delegation target differs from admitted VM",
		)
	}
	resources, err := openFloatingIPStore(ctx, state.network.store, false)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, resources.release(context.WithoutCancel(ctx))) }()
	if resources == nil {
		return FloatingIPDelegationResult{}, errs.NotFound(errs.CodeNetworkNotFound, request.ResourceID)
	}
	resource, _, _, found, err := resources.read(ctx, request.ResourceID)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	if !found || resource.Retired || !principal.matchesResource(resource) {
		return FloatingIPDelegationResult{}, networkAuthorityValidationError(
			"delegation resource is unavailable to admitted VM",
		)
	}
	grants, err := openFloatingIPDelegationStore(ctx, state.network.store, true)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, grants.release(context.WithoutCancel(ctx))) }()
	record, err := ensureFloatingIPDelegation(ctx, grants, request.ID, resource, principal,
		floatingIPDelegationMethod(request.Method), request.LeaseDuration)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	return floatingIPDelegationProjection(record), nil
}

func ensureFloatingIPDelegation(
	ctx context.Context, store *floatingIPDelegationStore, id string, resource floatingIPRecord,
	principal floatingIPGuestPrincipal, method floatingIPDelegationMethod, duration time.Duration,
) (floatingIPDelegationRecord, error) {
	if !vmIDPattern.MatchString(id) {
		return floatingIPDelegationRecord{}, networkAuthorityValidationError("grant ID is not canonical")
	}
	candidate, err := newFloatingIPDelegation(resource, principal, method, duration)
	if err != nil {
		return floatingIPDelegationRecord{}, err
	}
	candidate.ID = id
	existing, _, _, found, err := store.read(ctx, id)
	if err != nil {
		return floatingIPDelegationRecord{}, err
	}
	if found {
		if existing != candidate {
			return floatingIPDelegationRecord{}, networkAuthorityValidationError(
				"grant identity is retired, changed, or bound to a different incarnation",
			)
		}
		return existing, nil
	}

	if err := store.persist(ctx, candidate, nil); err != nil {
		return floatingIPDelegationRecord{}, err
	}
	return candidate, nil
}

// RevokeFloatingIPDelegation uses operator/network authority, never a live target VM or guest impersonation.
func (server *FloatingIPServer) RevokeFloatingIPDelegation(
	ctx context.Context, ownerUID uint32, request FloatingIPDelegationRevokeRequest,
) (_ FloatingIPDelegationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(request.NetworkID))
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	if err := admitFloatingIPNetwork(ctx, tx); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	store, err := openFloatingIPDelegationStore(ctx, tx.store, false)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	return revokeStoredFloatingIPDelegation(ctx, tx, store, request)
}

func readSelectedFloatingIPDelegation(
	ctx context.Context, store *floatingIPDelegationStore, networkID, id string,
) (floatingIPDelegationRecord, error) {
	if store == nil {
		return floatingIPDelegationRecord{}, errs.NotFound(errs.CodeNetworkNotFound, id)
	}
	grant, _, _, found, err := store.read(ctx, id)
	if err != nil {
		return floatingIPDelegationRecord{}, err
	}
	if !found || grant.NetworkID != networkID {
		return floatingIPDelegationRecord{}, errs.NotFound(errs.CodeNetworkNotFound, id)
	}
	return grant, nil
}

// CALLER MUST HOLD: the selected network and global firewall mutation authority.
// This operation intentionally needs no live VM: stored provenance selects any ownership requiring fencing.
func revokeStoredFloatingIPDelegation(
	ctx context.Context, tx *managedNetworkTransaction, store *floatingIPDelegationStore,
	request FloatingIPDelegationRevokeRequest,
) (FloatingIPDelegationResult, error) {
	grant, err := readSelectedFloatingIPDelegation(ctx, store, request.NetworkID, request.ID)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	if grant.Revision != request.ExpectedRevision {
		return FloatingIPDelegationResult{}, networkAuthorityValidationError(
			"delegation revision is stale; inspect before retry",
		)
	}
	if err := revokeFloatingIPDelegation(ctx, tx, store, grant); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	grant, err = readSelectedFloatingIPDelegation(ctx, store, request.NetworkID, request.ID)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	return floatingIPDelegationProjection(grant), nil
}

// RemoveOwnedFloatingIPDelegation resumes revocation only for the exact immutable lifetime in the receipt.
// Receipt incarnation fields are comparison guards; authenticated operator ownership supplies authority.
func (server *FloatingIPServer) RemoveOwnedFloatingIPDelegation(
	ctx context.Context, ownerUID uint32, request FloatingIPDelegationRemoveOwnedRequest,
) (_ FloatingIPDelegationResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(request.Grant.NetworkID))
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	if err := admitFloatingIPNetwork(ctx, tx); err != nil {
		return FloatingIPDelegationResult{}, err
	}
	store, err := openFloatingIPDelegationStore(ctx, tx.store, false)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	return removeOwnedStoredFloatingIPDelegation(ctx, tx, store, request)
}

func removeOwnedStoredFloatingIPDelegation(
	ctx context.Context, tx *managedNetworkTransaction, store *floatingIPDelegationStore,
	request FloatingIPDelegationRemoveOwnedRequest,
) (FloatingIPDelegationResult, error) {
	current, err := readSelectedFloatingIPDelegation(ctx, store, request.Grant.NetworkID, request.Grant.ID)
	if err != nil {
		return FloatingIPDelegationResult{}, err
	}
	projected := floatingIPDelegationProjection(current)
	expected := request.Grant
	if expected.Revision > projected.Revision {
		return FloatingIPDelegationResult{}, networkAuthorityValidationError(
			"delegation receipt is ahead of root lifetime",
		)
	}
	expected.Revision, expected.State = projected.Revision, projected.State
	if expected != projected {
		return FloatingIPDelegationResult{}, networkAuthorityValidationError(
			"owned delegation receipt differs from immutable root authority",
		)
	}
	return revokeStoredFloatingIPDelegation(ctx, tx, store, FloatingIPDelegationRevokeRequest{
		NetworkID: current.NetworkID, ID: current.ID, ExpectedRevision: current.Revision})
}
