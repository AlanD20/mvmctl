package privileged

import (
	"context"
	"errors"
	"time"

	"mvmctl/internal/lib/privilegedwire"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

type floatingIPOperatorEffects struct {
	associate func(context.Context, uint32, uint32, network.FloatingIPAssociateRequest) (network.FloatingIPResult, error)
	transfer  func(context.Context, uint32, uint32, network.FloatingIPTransferRequest) (network.FloatingIPResult, error)
	release   func(context.Context, uint32, uint32, network.FloatingIPReleaseRequest) (network.FloatingIPResult, error)
}

func associateOperatorFloatingIP(
	ctx context.Context,
	uid, gid uint32,
	request network.FloatingIPAssociateRequest,
) (network.FloatingIPResult, error) {
	result, err := mutateOperatorFloatingIP(ctx, uid, gid, request, false)
	if err != nil {
		return result, err
	}
	return completeOperatorFloatingIPAliasSync(ctx, uid, gid, request.VMID, result)
}

func transferOperatorFloatingIP(
	ctx context.Context,
	uid, gid uint32,
	request network.FloatingIPTransferRequest,
) (network.FloatingIPResult, error) {
	before, err := network.NewFloatingIPServer().
		GetFloatingIP(ctx, uid, network.FloatingIPGetRequest{ID: request.ID, NetworkID: request.NetworkID})
	if err != nil {
		return network.FloatingIPResult{}, err
	}
	result, err := mutateOperatorFloatingIP(
		ctx,
		uid,
		gid,
		network.FloatingIPAssociateRequest{
			ID:                 request.ID,
			NetworkID:          request.NetworkID,
			VMID:               request.VMID,
			ExpectedGeneration: request.ExpectedGeneration,
		},
		true,
	)
	if err != nil {
		return result, err
	}
	// Old alias removal is hygiene after fencing; target usability never depends on the old guest.
	if before.OwnerVMID != "" && before.OwnerVMID != request.VMID {
		reconcileOperatorAliasCleanup(ctx, uid, gid, before.OwnerVMID)
	}
	return completeOperatorFloatingIPAliasSync(ctx, uid, gid, request.VMID, result)
}

func releaseOperatorFloatingIP(
	ctx context.Context,
	uid, gid uint32,
	request network.FloatingIPReleaseRequest,
) (network.FloatingIPResult, error) {
	before, err := network.NewFloatingIPServer().
		GetFloatingIP(ctx, uid, network.FloatingIPGetRequest{ID: request.ID, NetworkID: request.NetworkID})
	if err != nil {
		return network.FloatingIPResult{}, err
	}
	baseline, baselineErr := operatorFloatingIPAliasBaseline(ctx, uid, gid, before.OwnerVMID)
	result, err := network.NewFloatingIPServer().ReleaseFloatingIP(ctx, uid, request)
	if err != nil {
		return result, err
	}
	if before.OwnerVMID != "" {
		if baselineErr != nil {
			return result, operatorAliasSyncError(result, baselineErr)
		}
		if err := waitOperatorFloatingIPAliasCleanup(ctx, uid, gid, before.OwnerVMID, baseline); err != nil {
			return result, operatorAliasSyncError(result, err)
		}
	}
	result.AliasSynchronization = "synchronized"
	return result, nil
}

func mutateOperatorFloatingIP(
	ctx context.Context,
	uid, gid uint32,
	request network.FloatingIPAssociateRequest,
	transfer bool,
) (_ network.FloatingIPResult, returnErr error) {
	admission, err := jailer.NewFloatingIPAdmissionServer().
		Open(ctx, uid, gid, jailer.FloatingIPAdmissionRequest{VMID: request.VMID})
	if err != nil {
		return network.FloatingIPResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	identity, err := admission.Identity(ctx)
	if err != nil {
		return network.FloatingIPResult{}, err
	}
	if err := admission.RequireRunning(ctx); err != nil {
		return network.FloatingIPResult{}, err
	}
	hold, err := admission.RetainTopologyHold(ctx)
	if err != nil {
		return network.FloatingIPResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, hold.Release(context.WithoutCancel(ctx))) }()
	vm, err := network.NewFloatingIPServer().
		OpenVM(ctx, hold, network.FloatingIPVMIncarnation{BootID: identity.BootID, LaunchGeneration: identity.LaunchGeneration, ExecutionUID: identity.ExecutionUID})
	if err != nil {
		return network.FloatingIPResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, vm.Release(context.WithoutCancel(ctx))) }()
	if err := admission.Recheck(ctx); err != nil {
		return network.FloatingIPResult{}, err
	}
	if transfer {
		return vm.TransferFloatingIP(
			ctx,
			network.FloatingIPTransferRequest{
				ID:                 request.ID,
				NetworkID:          request.NetworkID,
				VMID:               request.VMID,
				ExpectedGeneration: request.ExpectedGeneration,
			},
		)
	}
	return vm.AssociateFloatingIP(ctx, request)
}

func completeOperatorFloatingIPAliasSync(
	ctx context.Context,
	uid, gid uint32,
	vmID string,
	result network.FloatingIPResult,
) (network.FloatingIPResult, error) {
	if err := reconcileGuestControlWorker(ctx, uid, gid, vmID); err != nil {
		return result, operatorAliasSyncError(result, err)
	}
	if err := waitOperatorFloatingIPAliasSync(ctx, uid, gid, vmID); err != nil {
		return result, operatorAliasSyncError(result, err)
	}
	observed, err := network.NewFloatingIPServer().
		GetFloatingIP(ctx, uid, network.FloatingIPGetRequest{ID: result.ID, NetworkID: result.NetworkID})
	if err != nil || observed.Generation != result.Generation || observed.OwnerVMID != vmID ||
		observed.OwnerBootID != result.OwnerBootID ||
		observed.OwnerLaunchGeneration != result.OwnerLaunchGeneration ||
		observed.AuthorityState != "active" {
		return result, operatorAliasSyncError(
			result,
			errors.Join(
				err,
				errs.New(
					errs.CodeValidationFailed,
					"floating-IP ownership changed during alias synchronization",
					errs.WithClass(errs.ClassConflict),
				),
			),
		)
	}
	result.AliasSynchronization = "synchronized"
	return result, nil
}
func operatorAliasSyncError(result network.FloatingIPResult, cause error) error {
	return errs.WrapMsg(
		errs.CodeNetworkError,
		"floating-IP ownership committed but guest alias synchronization failed; inspect current ownership",
		cause,
		errs.WithDetails(
			map[string]any{
				"root_committed":    true,
				"alias_sync_failed": true,
				"id":                result.ID,
				"generation":        result.Generation,
				"owner_vm_id":       result.OwnerVMID,
			},
		),
	)
}
func reconcileOperatorAliasCleanup(ctx context.Context, uid, gid uint32, vmID string) {
	cleanup, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// Fencing already completed. A stopped or unreachable old guest must never obstruct transfer.
	_ = waitOperatorFloatingIPAliasSync(cleanup, uid, gid, vmID)
}

func handleAssociateFloatingIP(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	effects floatingIPOperatorEffects,
	deps controlDeps,
) error {
	action := network.FloatingIPAssociateAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPAssociateRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.associate == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			action,
			errs.New(errs.CodeInternal, "floating-IP ownership handler is unavailable"),
			deps,
		)
	}
	result, err := effects.associate(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleTransferFloatingIP(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	effects floatingIPOperatorEffects,
	deps controlDeps,
) error {
	action := network.FloatingIPTransferAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPTransferRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.transfer == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			action,
			errs.New(errs.CodeInternal, "floating-IP ownership handler is unavailable"),
			deps,
		)
	}
	result, err := effects.transfer(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}

func handleReleaseFloatingIP(
	ctx context.Context,
	endpoint controlEndpoint,
	caller callerIdentity,
	effects floatingIPOperatorEffects,
	deps controlDeps,
) error {
	action := network.FloatingIPReleaseAction
	reader := newRequestReader(
		ctx,
		int(endpoint.Fd()),
		boundedReadDeadline(ctx, deps.now(), requestHeaderReadTimeout),
		deps,
	)
	request, err := privilegedwire.DecodeRequest[network.FloatingIPReleaseRequest](ctx, reader, action, 0)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := request.Body.Validate(); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if err := requireRequestEOF(ctx, reader); err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	if effects.release == nil {
		return deliverOperationError(
			ctx,
			endpoint,
			action,
			errs.New(errs.CodeInternal, "floating-IP ownership handler is unavailable"),
			deps,
		)
	}
	result, err := effects.release(ctx, caller.uid, caller.gid, request.Body)
	if err != nil {
		return deliverOperationError(ctx, endpoint, action, err, deps)
	}
	return deliverOperationSuccess(ctx, endpoint, action, result, deps)
}
