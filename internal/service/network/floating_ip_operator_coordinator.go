package network

import (
	"context"
	"errors"
	"time"

	"mvmctl/internal/service/firewall"
	"mvmctl/pkg/errs"
)

// AssociateFloatingIP consumes retained target authority; it never borrows a guest grant.
func (lease *FloatingIPVMLease) AssociateFloatingIP(
	ctx context.Context,
	request FloatingIPAssociateRequest,
) (FloatingIPResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	return lease.operatorFloatingIP(ctx, request, false)
}

// TransferFloatingIP is an explicit operator takeover, including a healthy delegated owner.
func (lease *FloatingIPVMLease) TransferFloatingIP(
	ctx context.Context,
	request FloatingIPTransferRequest,
) (FloatingIPResult, error) {
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	return lease.operatorFloatingIP(
		ctx,
		FloatingIPAssociateRequest{
			ID:                 request.ID,
			NetworkID:          request.NetworkID,
			VMID:               request.VMID,
			ExpectedGeneration: request.ExpectedGeneration,
		},
		true,
	)
}

func (lease *FloatingIPVMLease) operatorFloatingIP(
	ctx context.Context,
	request FloatingIPAssociateRequest,
	transfer bool,
) (_ FloatingIPResult, returnErr error) {
	publicationAttempted := false
	defer func() {
		if publicationAttempted && returnErr != nil {
			returnErr = errs.WrapMsg(
				errs.CodeNetworkError,
				"floating-IP ownership outcome may have changed; inspect before retrying",
				returnErr,
				errs.WithDetails(map[string]any{"outcome_unknown": true, "id": request.ID}),
			)
		}
	}()
	if lease == nil || lease.state == nil {
		return FloatingIPResult{}, networkAuthorityValidationError("floating-IP target admission is unavailable")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.readyGuest(ctx); err != nil {
		return FloatingIPResult{}, err
	}
	principal := state.guestPrincipal()
	if request.VMID != principal.vmID || request.NetworkID != principal.networkID {
		return FloatingIPResult{}, networkAuthorityValidationError("floating-IP target differs from retained authority")
	}
	store, err := openFloatingIPStore(ctx, state.network.store, false)
	if err != nil {
		return FloatingIPResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	if store == nil {
		return FloatingIPResult{}, networkAuthorityValidationError("floating-IP reservation is missing")
	}
	record, _, _, found, err := store.read(ctx, request.ID)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if !found || record.OwnerUID != principal.ownerUID || record.NetworkID != principal.networkID {
		return FloatingIPResult{}, networkAuthorityValidationError(
			"floating-IP reservation differs from operator scope",
		)
	}
	if err := admitFloatingIPOperatorGeneration(record, request.ExpectedGeneration); err != nil {
		return FloatingIPResult{}, err
	}
	observed, err := state.firewall.ObserveFloatingIPLease(ctx, record.ID, record.Generation)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if observed.Binding != floatingIPFirewallBinding(record, record.Owner) {
		return FloatingIPResult{}, networkAuthorityCorruptError("floating-IP ownership projection differs", nil)
	}
	if record.Owner != (floatingIPOwner{}) && principal.owns(record.Owner) {
		return FloatingIPResult{}, networkAuthorityValidationError(
			"floating IP already names this VM incarnation; release before changing ownership mode",
		)
	}
	if !transfer && (record.Owner.Lifetime == firewall.FloatingIPPersistent || observed.Present) {
		return FloatingIPResult{}, networkAuthorityValidationError(
			"floating IP has current ownership; use explicit transfer",
		)
	}
	if transfer && record.Phase != floatingIPOwned {
		return FloatingIPResult{}, networkAuthorityValidationError("floating-IP transfer requires existing ownership")
	}
	next := record
	next.Revision++
	next.Generation++
	next.Phase = floatingIPFencing
	next.Operation = floatingIPAssociate
	next.BeforeOwner = record.Owner
	next.Owner = floatingIPOwner{}
	next.TargetOwner = floatingIPOwner{
		Origin:           floatingIPOperatorOrigin,
		Lifetime:         firewall.FloatingIPPersistent,
		BootID:           principal.bootID,
		VMID:             principal.vmID,
		LaunchGeneration: principal.launchGeneration,
		ExecutionUID:     principal.executionUID,
	}
	// CRITICAL: an error after this publication attempt cannot promise that ownership was unchanged.
	publicationAttempted = true
	if err := store.persist(ctx, next, &record); err != nil {
		return FloatingIPResult{}, err
	}
	tx := borrowFloatingIPTransaction(state.network, state.firewall)
	if err := state.firewall.FenceFloatingIP(ctx, floatingIPFirewallBinding(next, floatingIPOwner{})); err != nil {
		return FloatingIPResult{}, err
	}
	if err := tx.invalidateFloatingIPNetworkState(ctx, next); err != nil {
		return FloatingIPResult{}, err
	}
	next, err = advanceFloatingIPPhase(ctx, store, next, floatingIPFenced)
	if err != nil {
		return FloatingIPResult{}, err
	}
	next, err = advanceFloatingIPPhase(ctx, store, next, floatingIPGranting)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if err := state.readyGuest(ctx); err != nil {
		return FloatingIPResult{}, err
	}
	if err := state.firewall.PublishFloatingIPOwner(ctx, floatingIPFirewallBinding(next, next.TargetOwner)); err != nil {
		return FloatingIPResult{}, err
	}
	if err := state.firewall.ActivateFloatingIPOwnership(ctx, next.ID, next.Generation); err != nil {
		return FloatingIPResult{}, err
	}
	record, err = commitFloatingIPOwned(ctx, store, next)
	if err != nil {
		return FloatingIPResult{}, err
	}
	observed, err = state.firewall.ObserveFloatingIPLease(ctx, record.ID, record.Generation)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if !observed.Present || observed.Binding != floatingIPFirewallBinding(record, record.Owner) {
		return FloatingIPResult{}, networkAuthorityCorruptError(
			"floating-IP persistent authority acknowledgement differs",
			nil,
		)
	}
	result := record.result()
	result.AuthorityState = "active"
	result.AliasSynchronization = "pending"
	return result, nil
}

// ReleaseFloatingIP fences root ownership even after the old VM is gone.
func (server *FloatingIPServer) ReleaseFloatingIP(
	ctx context.Context,
	ownerUID uint32,
	request FloatingIPReleaseRequest,
) (_ FloatingIPResult, returnErr error) {
	publicationAttempted := false
	// Register before resource cleanup so late close/release failures retain the same outcome classification.
	defer func() { returnErr = operatorFloatingIPReleaseOutcome(request.ID, publicationAttempted, returnErr) }()
	if err := request.Validate(); err != nil {
		return FloatingIPResult{}, err
	}
	tx, err := acquireManagedNetworkTransaction(ctx, ownerUID, NetworkID(request.NetworkID))
	if err != nil {
		return FloatingIPResult{}, err
	}
	defer func() { returnErr = tx.release(ctx, returnErr) }()
	if err := admitFloatingIPNetwork(ctx, tx); err != nil {
		return FloatingIPResult{}, err
	}
	store, err := openFloatingIPStore(ctx, tx.store, false)
	if err != nil {
		return FloatingIPResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.release(context.WithoutCancel(ctx))) }()
	if store == nil {
		return FloatingIPResult{}, networkAuthorityValidationError("floating-IP reservation is missing")
	}
	record, _, _, found, err := store.read(ctx, request.ID)
	if err != nil {
		return FloatingIPResult{}, err
	}
	if !found || record.NetworkID != request.NetworkID {
		return FloatingIPResult{}, networkAuthorityValidationError("floating-IP reservation is missing")
	}
	if err := admitFloatingIPOperatorGeneration(record, request.ExpectedGeneration); err != nil {
		return FloatingIPResult{}, err
	}
	if record.Phase != floatingIPOwned {
		return FloatingIPResult{}, networkAuthorityValidationError("floating IP is already unassociated")
	}
	next := record
	next.Revision++
	next.Generation++
	next.Phase = floatingIPFencing
	next.Operation = floatingIPRelease
	next.BeforeOwner = record.Owner
	next.Owner = floatingIPOwner{}
	publicationAttempted = true
	record, err = publishOperatorFloatingIPRelease(ctx, tx, store, record, next)
	if err != nil {
		return FloatingIPResult{}, err
	}
	result := record.result()
	result.AuthorityState = "absent"
	result.AliasSynchronization = "pending"
	return result, nil
}

// operatorFloatingIPReleaseOutcome preserves pre-effect validation classes and never claims a committed release.
func operatorFloatingIPReleaseOutcome(id string, publicationAttempted bool, cause error) error {
	if !publicationAttempted || cause == nil {
		return cause
	}
	return errs.WrapMsg(errs.CodeNetworkError,
		"floating-IP release outcome may have changed; inspect before retrying", cause,
		errs.WithDetails(map[string]any{"outcome_unknown": true, "id": id}))
}

func publishOperatorFloatingIPRelease(
	ctx context.Context,
	tx *managedNetworkTransaction,
	store *floatingIPStore,
	before, next floatingIPRecord,
) (floatingIPRecord, error) {
	if err := store.persist(ctx, next, &before); err != nil {
		return before, err
	}
	return fenceFloatingIPOwnership(ctx, tx, store, next)
}

func observedFloatingIPResult(
	record floatingIPRecord,
	observed firewall.FloatingIPLeaseObservation,
) (FloatingIPResult, error) {
	if observed.Binding != floatingIPFirewallBinding(record, record.Owner) {
		return FloatingIPResult{}, networkAuthorityCorruptError("floating-IP root and kernel projection differ", nil)
	}
	result := record.result()
	result.AuthorityState = "absent"
	if observed.Present && (record.Owner.Lifetime == firewall.FloatingIPPersistent || observed.Remaining > 0) {
		result.AuthorityState = "active"
		if observed.Remaining > 0 {
			result.LeaseRemainingMillis = uint64(observed.Remaining / time.Millisecond)
		}
	}
	return result, nil
}
