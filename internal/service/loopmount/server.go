package loopmount

import (
	"context"
	"errors"
	"maps"

	"mvmctl/pkg/errs"
)

const maxServerCleanupDetailBytes = 512

// Server coordinates one admitted LoopMount operation without exposing effect or descriptor authority.
// CRITICAL: Server is an inert receiver foundation. It has no production constructor or dispatcher registration.
type Server struct {
	rootfsAuthority rootfsAuthorityDeps
	effects         rootfsEffects
	locks           rootfsResourceLocks
}

type rootfsEffects interface {
	ProvisionVM(context.Context, *rootfsLease, VMProvisionRequest) (vmProvisionEffectOutcome, error)
	InspectImage(context.Context, *rootfsLease, ImageInspectRequest) (imageInspectEffectOutcome, error)
	OptimizeImage(context.Context, *rootfsLease, ImageOptimizeRequest) (imageOptimizeEffectOutcome, error)
}

type vmProvisionEffectCleanup interface {
	CleanupProvisionVM(context.Context) error
}

type imageInspectEffectCleanup interface {
	CleanupInspectImage(context.Context) error
}

type imageOptimizeEffectCleanup interface {
	CleanupOptimizeImage(context.Context) error
}

type vmProvisionEffectOutcome struct {
	result VMProvisionResult
	lease  vmProvisionEffectCleanup
}

type imageInspectEffectOutcome struct {
	result ImageInspectResult
	lease  imageInspectEffectCleanup
}

type imageOptimizeEffectOutcome struct {
	result ImageOptimizeResult
	lease  imageOptimizeEffectCleanup
}

type rootfsResourceLocks interface {
	Acquire(context.Context, Caller, rootfsSelection) (rootfsResourceLockLease, error)
}

type rootfsResourceLockLease interface {
	Release(context.Context) error
}

// ProvisionVM executes one closed VM provisioning request against its admitted rootfs.
func (server *Server) ProvisionVM(
	ctx context.Context,
	caller Caller,
	request VMProvisionRequest,
) (_ VMProvisionResult, returnErr error) {
	if err := server.validateDependencies(); err != nil {
		return VMProvisionResult{}, err
	}
	if err := request.Validate(); err != nil {
		return VMProvisionResult{}, err
	}
	if err := validateRootfsCaller(caller); err != nil {
		return VMProvisionResult{}, err
	}
	selection := rootfsSelection{
		kind:          rootfsVMProvisionSelection,
		resourceID:    request.VMID,
		expectedBytes: request.ExpectedRootfsBytes,
	}
	lockLease, err := server.locks.Acquire(ctx, caller, selection)
	if err != nil {
		return VMProvisionResult{}, preserveServerError("acquire managed VM rootfs lock", err)
	}
	if lockLease == nil {
		return VMProvisionResult{}, rootfsAtomicError("managed VM rootfs lock authority is missing", nil)
	}
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"resource_lock_release",
			"release managed VM rootfs lock",
			lockLease.Release(context.WithoutCancel(ctx)),
		)
	}()

	rootfsLease, err := admitVMProvisionRootfs(ctx, server.rootfsAuthority, caller, request)
	if err != nil {
		return VMProvisionResult{}, err
	}
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"rootfs_release",
			"release managed VM rootfs authority",
			rootfsLease.Release(context.WithoutCancel(ctx)),
		)
	}()

	outcome, effectErr := server.effects.ProvisionVM(ctx, rootfsLease, request)
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"effect_cleanup",
			"clean up managed VM rootfs effects",
			outcome.cleanup(context.WithoutCancel(ctx)),
		)
	}()
	if effectErr != nil {
		return VMProvisionResult{}, preserveServerError("provision managed VM rootfs", effectErr)
	}
	if err := outcome.result.Validate(request); err != nil {
		return VMProvisionResult{}, err
	}
	return outcome.result, nil
}

// InspectImage executes one read-only inspection against its admitted staging rootfs.
func (server *Server) InspectImage(
	ctx context.Context,
	caller Caller,
	request ImageInspectRequest,
) (_ ImageInspectResult, returnErr error) {
	if err := server.validateDependencies(); err != nil {
		return ImageInspectResult{}, err
	}
	if err := request.Validate(); err != nil {
		return ImageInspectResult{}, err
	}
	if err := validateRootfsCaller(caller); err != nil {
		return ImageInspectResult{}, err
	}
	selection := rootfsSelection{
		kind:          rootfsImageInspectSelection,
		resourceID:    request.ImageID,
		expectedBytes: request.ExpectedImageBytes,
	}
	lockLease, err := server.locks.Acquire(ctx, caller, selection)
	if err != nil {
		return ImageInspectResult{}, preserveServerError("acquire managed image inspection lock", err)
	}
	if lockLease == nil {
		return ImageInspectResult{}, rootfsAtomicError("managed image inspection lock authority is missing", nil)
	}
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"resource_lock_release",
			"release managed image inspection lock",
			lockLease.Release(context.WithoutCancel(ctx)),
		)
	}()

	rootfsLease, err := admitImageInspectRootfs(ctx, server.rootfsAuthority, caller, request)
	if err != nil {
		return ImageInspectResult{}, err
	}
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"rootfs_release",
			"release managed image inspection authority",
			rootfsLease.Release(context.WithoutCancel(ctx)),
		)
	}()

	outcome, effectErr := server.effects.InspectImage(ctx, rootfsLease, request)
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"effect_cleanup",
			"clean up managed image inspection effects",
			outcome.cleanup(context.WithoutCancel(ctx)),
		)
	}()
	if effectErr != nil {
		return ImageInspectResult{}, preserveServerError("inspect managed image rootfs", effectErr)
	}
	if err := outcome.result.Validate(request); err != nil {
		return ImageInspectResult{}, err
	}
	return outcome.result, nil
}

// OptimizeImage executes one closed mutation plan against its admitted staging rootfs.
func (server *Server) OptimizeImage(
	ctx context.Context,
	caller Caller,
	request ImageOptimizeRequest,
) (_ ImageOptimizeResult, returnErr error) {
	if err := server.validateDependencies(); err != nil {
		return ImageOptimizeResult{}, err
	}
	if err := request.Validate(); err != nil {
		return ImageOptimizeResult{}, err
	}
	if err := validateRootfsCaller(caller); err != nil {
		return ImageOptimizeResult{}, err
	}
	selection := rootfsSelection{
		kind:          rootfsImageOptimizeSelection,
		resourceID:    request.ImageID,
		expectedBytes: request.ExpectedImageBytes,
	}
	lockLease, err := server.locks.Acquire(ctx, caller, selection)
	if err != nil {
		return ImageOptimizeResult{}, preserveServerError("acquire managed image optimization lock", err)
	}
	if lockLease == nil {
		return ImageOptimizeResult{}, rootfsAtomicError("managed image optimization lock authority is missing", nil)
	}
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"resource_lock_release",
			"release managed image optimization lock",
			lockLease.Release(context.WithoutCancel(ctx)),
		)
	}()

	rootfsLease, err := admitImageOptimizeRootfs(ctx, server.rootfsAuthority, caller, request)
	if err != nil {
		return ImageOptimizeResult{}, err
	}
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"rootfs_release",
			"release managed image optimization authority",
			rootfsLease.Release(context.WithoutCancel(ctx)),
		)
	}()

	outcome, effectErr := server.effects.OptimizeImage(ctx, rootfsLease, request)
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"effect_cleanup",
			"clean up managed image optimization effects",
			outcome.cleanup(context.WithoutCancel(ctx)),
		)
	}()
	if effectErr != nil {
		return ImageOptimizeResult{}, preserveServerError("optimize managed image rootfs", effectErr)
	}
	if err := outcome.result.Validate(request); err != nil {
		return ImageOptimizeResult{}, err
	}
	return outcome.result, nil
}

func (server *Server) validateDependencies() error {
	if server == nil || server.effects == nil || server.locks == nil {
		return rootfsAtomicError("loopmount server dependencies are incomplete", nil)
	}
	return validateRootfsAuthorityDeps(server.rootfsAuthority)
}

func (outcome vmProvisionEffectOutcome) cleanup(ctx context.Context) error {
	if outcome.lease == nil {
		return rootfsAtomicError("managed VM rootfs effect cleanup authority is missing", nil)
	}
	return outcome.lease.CleanupProvisionVM(ctx)
}

func (outcome imageInspectEffectOutcome) cleanup(ctx context.Context) error {
	if outcome.lease == nil {
		return rootfsAtomicError("managed image inspection cleanup authority is missing", nil)
	}
	return outcome.lease.CleanupInspectImage(ctx)
}

func (outcome imageOptimizeEffectOutcome) cleanup(ctx context.Context) error {
	if outcome.lease == nil {
		return rootfsAtomicError("managed image optimization cleanup authority is missing", nil)
	}
	return outcome.lease.CleanupOptimizeImage(ctx)
}

func preserveServerError(message string, cause error) error {
	if errs.AsDomainError(cause) != nil {
		return cause
	}
	return rootfsAtomicError(message, cause)
}

func appendServerCleanupError(primary error, annotation, message string, cause error) error {
	if cause == nil {
		return primary
	}
	detail := boundedServerCleanupDetail(message, cause)
	if primary == nil {
		domainErr := rootfsAtomicError(detail, cause)
		domainErr.Details = map[string]any{
			"loopmount_" + annotation + "_failed": true,
			"loopmount_" + annotation + "_detail": detail,
		}
		return domainErr
	}

	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		domainErr = rootfsAtomicError("loopmount operation failed", primary)
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	if result.Details == nil {
		result.Details = make(map[string]any)
	}
	result.Details["loopmount_"+annotation+"_failed"] = true
	result.Details["loopmount_"+annotation+"_detail"] = detail
	result.Err = errors.Join(domainErr.Err, cause)
	return &result
}

func boundedServerCleanupDetail(message string, cause error) string {
	detail := message
	if cause != nil {
		detail += ": " + cause.Error()
	}
	if len(detail) <= maxServerCleanupDetailBytes {
		return detail
	}
	return detail[:maxServerCleanupDetailBytes-3] + "..."
}
