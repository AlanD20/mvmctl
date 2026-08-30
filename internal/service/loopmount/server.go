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
	optimizeEffects imageOptimizeEffects
	locks           rootfsResourceLocks
}

type rootfsEffects interface {
	ProvisionVM(
		context.Context,
		*rootfsLease,
		Filesystem,
		VMProvisionPlan,
	) (vmProvisionEffectOutcome, error)
	InspectImage(context.Context, *rootfsLease, Filesystem) (imageInspectEffectOutcome, error)
}

type imageOptimizeEffects interface {
	OptimizeImage(
		context.Context,
		*rootfsLease,
		Filesystem,
		ImageOptimizePlan,
	) (imageOptimizeEffectOutcome, error)
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

type vmProvisionEffectState uint8

const (
	vmProvisionEffectStateInvalid vmProvisionEffectState = iota
	vmProvisionEffectStateUnchanged
	vmProvisionEffectStateModified
	vmProvisionEffectStateModifiedDurabilityUncertain
	vmProvisionEffectStateOutcomeUnknown
)

type imageInspectEffectState uint8

const (
	imageInspectEffectStateInvalid imageInspectEffectState = iota
	imageInspectEffectStateUnchanged
	imageInspectEffectStateOutcomeUnknown
)

type imageOptimizeEffectState uint8

const (
	imageOptimizeEffectStateInvalid imageOptimizeEffectState = iota
	imageOptimizeEffectStateUnchanged
	imageOptimizeEffectStateModified
	imageOptimizeEffectStateImageReplaced
	imageOptimizeEffectStateModifiedDurabilityUncertain
	imageOptimizeEffectStateImageReplacedDurabilityUncertain
	imageOptimizeEffectStateOutcomeUnknown
)

type vmProvisionEffectOutcome struct {
	state        vmProvisionEffectState
	filesystem   Filesystem
	rootfsBytes  uint64
	filesWritten uint32
	commandsRun  uint32
	lease        vmProvisionEffectCleanup
}

type imageInspectEffectOutcome struct {
	state  imageInspectEffectState
	osType string
	lease  imageInspectEffectCleanup
}

type imageOptimizeEffectOutcome struct {
	state      imageOptimizeEffectState
	filesystem Filesystem
	imageBytes uint64
	lease      imageOptimizeEffectCleanup
}

type rootfsResourceLocks interface {
	Acquire(context.Context, Caller, rootfsSelection) (rootfsResourceLockLease, error)
}

type rootfsResourceLockLease interface {
	Release(context.Context) error
}

type rootfsEffectEvidence struct {
	rootfsModified      bool
	imageReplaced       bool
	durabilityUncertain bool
	outcomeUnknown      bool
}

// ProvisionVM executes one closed VM provisioning request against its admitted rootfs.
func (server *Server) ProvisionVM(
	ctx context.Context,
	caller Caller,
	request VMProvisionRequest,
) (_ VMProvisionResult, returnErr error) {
	if err := server.validateRootfsEffectsDependencies(); err != nil {
		return VMProvisionResult{}, err
	}
	if err := request.Validate(); err != nil {
		return VMProvisionResult{}, err
	}
	if err := validateRootfsCaller(caller); err != nil {
		return VMProvisionResult{}, err
	}
	var finalEvidence rootfsEffectEvidence
	effectStateObserved := false
	defer func() {
		if returnErr != nil && effectStateObserved {
			returnErr = annotateServerEffectFailure(returnErr, finalEvidence, nil)
		}
	}()
	selection := rootfsSelection{
		kind:          rootfsVMProvisionSelection,
		resourceID:    request.VMID,
		expectedBytes: request.ExpectedRootfsBytes,
		replayBytes:   request.Plan.ResizeBytes,
	}
	lockLease, acquireErr := server.locks.Acquire(ctx, caller, selection)
	if lockLease != nil {
		defer func() {
			returnErr = appendServerCleanupError(
				returnErr,
				"resource_lock_release",
				"release managed VM rootfs lock",
				lockLease.Release(context.WithoutCancel(ctx)),
			)
		}()
	}
	if acquireErr != nil {
		return VMProvisionResult{}, preserveServerError("acquire managed VM rootfs lock", acquireErr)
	}
	if lockLease == nil {
		return VMProvisionResult{}, rootfsAtomicError("managed VM rootfs lock authority is missing", nil)
	}

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

	outcome, effectErr := server.effects.ProvisionVM(ctx, rootfsLease, request.Filesystem, request.Plan)
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"effect_cleanup",
			"clean up managed VM rootfs effects",
			outcome.cleanup(context.WithoutCancel(ctx)),
		)
	}()
	evidence, stateErr := outcome.state.evidence()
	finalEvidence = evidence
	effectStateObserved = true
	if effectErr != nil {
		primary := preserveServerError("provision managed VM rootfs", effectErr)
		return VMProvisionResult{}, annotateServerEffectFailure(primary, evidence, stateErr)
	}
	if stateErr != nil || !outcome.state.allowsSuccess() {
		primary := rootfsAtomicError("managed VM rootfs effect state cannot authorize success", stateErr)
		return VMProvisionResult{}, annotateServerEffectFailure(primary, evidence, stateErr)
	}
	result := VMProvisionResult{
		VMID:         request.VMID,
		Filesystem:   outcome.filesystem,
		RootfsBytes:  outcome.rootfsBytes,
		FilesWritten: outcome.filesWritten,
		CommandsRun:  outcome.commandsRun,
	}
	if err := result.Validate(request); err != nil {
		return VMProvisionResult{}, annotateServerEffectFailure(err, evidence, nil)
	}
	return result, nil
}

// InspectImage executes one read-only inspection against its admitted staging rootfs.
func (server *Server) InspectImage(
	ctx context.Context,
	caller Caller,
	request ImageInspectRequest,
) (_ ImageInspectResult, returnErr error) {
	if err := server.validateRootfsEffectsDependencies(); err != nil {
		return ImageInspectResult{}, err
	}
	if err := request.Validate(); err != nil {
		return ImageInspectResult{}, err
	}
	if err := validateRootfsCaller(caller); err != nil {
		return ImageInspectResult{}, err
	}
	var finalEvidence rootfsEffectEvidence
	effectStateObserved := false
	defer func() {
		if returnErr != nil && effectStateObserved {
			returnErr = annotateServerEffectFailure(returnErr, finalEvidence, nil)
		}
	}()
	selection := rootfsSelection{
		kind:          rootfsImageInspectSelection,
		resourceID:    request.ImageID,
		expectedBytes: request.ExpectedImageBytes,
	}
	lockLease, acquireErr := server.locks.Acquire(ctx, caller, selection)
	if lockLease != nil {
		defer func() {
			returnErr = appendServerCleanupError(
				returnErr,
				"resource_lock_release",
				"release managed image inspection lock",
				lockLease.Release(context.WithoutCancel(ctx)),
			)
		}()
	}
	if acquireErr != nil {
		return ImageInspectResult{}, preserveServerError("acquire managed image inspection lock", acquireErr)
	}
	if lockLease == nil {
		return ImageInspectResult{}, rootfsAtomicError("managed image inspection lock authority is missing", nil)
	}

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

	outcome, effectErr := server.effects.InspectImage(ctx, rootfsLease, request.Filesystem)
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"effect_cleanup",
			"clean up managed image inspection effects",
			outcome.cleanup(context.WithoutCancel(ctx)),
		)
	}()
	evidence, stateErr := outcome.state.evidence()
	finalEvidence = evidence
	effectStateObserved = true
	if effectErr != nil {
		primary := preserveServerError("inspect managed image rootfs", effectErr)
		return ImageInspectResult{}, annotateServerEffectFailure(primary, evidence, stateErr)
	}
	if stateErr != nil || !outcome.state.allowsSuccess() {
		primary := rootfsAtomicError("managed image inspection effect state cannot authorize success", stateErr)
		return ImageInspectResult{}, annotateServerEffectFailure(primary, evidence, stateErr)
	}
	result := ImageInspectResult{ImageID: request.ImageID, OSType: outcome.osType}
	if err := result.Validate(request); err != nil {
		return ImageInspectResult{}, annotateServerEffectFailure(err, evidence, nil)
	}
	return result, nil
}

// OptimizeImage executes one closed mutation plan against its admitted staging rootfs.
// CRITICAL: OptimizeImage cannot be registered until durable replacement and shrink replay authority is implemented.
func (server *Server) OptimizeImage(
	ctx context.Context,
	caller Caller,
	request ImageOptimizeRequest,
) (_ ImageOptimizeResult, returnErr error) {
	if err := server.validateImageOptimizeDependencies(); err != nil {
		return ImageOptimizeResult{}, err
	}
	if err := request.Validate(); err != nil {
		return ImageOptimizeResult{}, err
	}
	if err := validateRootfsCaller(caller); err != nil {
		return ImageOptimizeResult{}, err
	}
	var finalEvidence rootfsEffectEvidence
	effectStateObserved := false
	defer func() {
		if returnErr != nil && effectStateObserved {
			returnErr = annotateServerEffectFailure(returnErr, finalEvidence, nil)
		}
	}()
	selection := rootfsSelection{
		kind:          rootfsImageOptimizeSelection,
		resourceID:    request.ImageID,
		expectedBytes: request.ExpectedImageBytes,
	}
	lockLease, acquireErr := server.locks.Acquire(ctx, caller, selection)
	if lockLease != nil {
		defer func() {
			returnErr = appendServerCleanupError(
				returnErr,
				"resource_lock_release",
				"release managed image optimization lock",
				lockLease.Release(context.WithoutCancel(ctx)),
			)
		}()
	}
	if acquireErr != nil {
		return ImageOptimizeResult{}, preserveServerError("acquire managed image optimization lock", acquireErr)
	}
	if lockLease == nil {
		return ImageOptimizeResult{}, rootfsAtomicError("managed image optimization lock authority is missing", nil)
	}

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

	outcome, effectErr := server.optimizeEffects.OptimizeImage(ctx, rootfsLease, request.Filesystem, request.Plan)
	defer func() {
		returnErr = appendServerCleanupError(
			returnErr,
			"effect_cleanup",
			"clean up managed image optimization effects",
			outcome.cleanup(context.WithoutCancel(ctx)),
		)
	}()
	evidence, stateErr := outcome.state.evidence()
	finalEvidence = evidence
	effectStateObserved = true
	if effectErr != nil {
		primary := preserveServerError("optimize managed image rootfs", effectErr)
		return ImageOptimizeResult{}, annotateServerEffectFailure(primary, evidence, stateErr)
	}
	if stateErr != nil || !outcome.state.allowsSuccess() {
		primary := rootfsAtomicError("managed image optimization effect state cannot authorize success", stateErr)
		return ImageOptimizeResult{}, annotateServerEffectFailure(primary, evidence, stateErr)
	}
	result := ImageOptimizeResult{
		ImageID: request.ImageID, Filesystem: outcome.filesystem, ImageBytes: outcome.imageBytes,
	}
	if err := result.Validate(request); err != nil {
		return ImageOptimizeResult{}, annotateServerEffectFailure(err, evidence, nil)
	}
	return result, nil
}

func (server *Server) validateRootfsEffectsDependencies() error {
	if server == nil || server.effects == nil || server.locks == nil {
		return rootfsAtomicError("loopmount provisioning and inspection dependencies are incomplete", nil)
	}
	return validateRootfsAuthorityDeps(server.rootfsAuthority)
}

func (server *Server) validateImageOptimizeDependencies() error {
	if server == nil || server.optimizeEffects == nil || server.locks == nil {
		return rootfsAtomicError("loopmount image optimization dependencies are incomplete", nil)
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

func (state vmProvisionEffectState) evidence() (rootfsEffectEvidence, error) {
	switch state {
	case vmProvisionEffectStateUnchanged:
		return rootfsEffectEvidence{}, nil
	case vmProvisionEffectStateModified:
		return rootfsEffectEvidence{rootfsModified: true}, nil
	case vmProvisionEffectStateModifiedDurabilityUncertain:
		return rootfsEffectEvidence{rootfsModified: true, durabilityUncertain: true}, nil
	case vmProvisionEffectStateOutcomeUnknown:
		return rootfsEffectEvidence{rootfsModified: true, durabilityUncertain: true, outcomeUnknown: true}, nil
	default:
		return rootfsEffectEvidence{rootfsModified: true, durabilityUncertain: true, outcomeUnknown: true},
			rootfsAtomicError("managed VM rootfs effect state is invalid", nil)
	}
}

func (state vmProvisionEffectState) allowsSuccess() bool {
	return state == vmProvisionEffectStateUnchanged || state == vmProvisionEffectStateModified
}

func (state imageInspectEffectState) evidence() (rootfsEffectEvidence, error) {
	switch state {
	case imageInspectEffectStateUnchanged:
		return rootfsEffectEvidence{}, nil
	case imageInspectEffectStateOutcomeUnknown:
		return rootfsEffectEvidence{outcomeUnknown: true}, nil
	default:
		return rootfsEffectEvidence{outcomeUnknown: true},
			rootfsAtomicError("managed image inspection effect state is invalid", nil)
	}
}

func (state imageInspectEffectState) allowsSuccess() bool {
	return state == imageInspectEffectStateUnchanged
}

func (state imageOptimizeEffectState) evidence() (rootfsEffectEvidence, error) {
	switch state {
	case imageOptimizeEffectStateUnchanged:
		return rootfsEffectEvidence{}, nil
	case imageOptimizeEffectStateModified:
		return rootfsEffectEvidence{rootfsModified: true}, nil
	case imageOptimizeEffectStateImageReplaced:
		return rootfsEffectEvidence{rootfsModified: true, imageReplaced: true}, nil
	case imageOptimizeEffectStateModifiedDurabilityUncertain:
		return rootfsEffectEvidence{rootfsModified: true, durabilityUncertain: true}, nil
	case imageOptimizeEffectStateImageReplacedDurabilityUncertain:
		return rootfsEffectEvidence{rootfsModified: true, imageReplaced: true, durabilityUncertain: true}, nil
	case imageOptimizeEffectStateOutcomeUnknown:
		return rootfsEffectEvidence{
			rootfsModified: true, imageReplaced: true, durabilityUncertain: true, outcomeUnknown: true,
		}, nil
	default:
		return rootfsEffectEvidence{
			rootfsModified: true, imageReplaced: true, durabilityUncertain: true, outcomeUnknown: true,
		}, rootfsAtomicError("managed image optimization effect state is invalid", nil)
	}
}

func (state imageOptimizeEffectState) allowsSuccess() bool {
	return state == imageOptimizeEffectStateUnchanged || state == imageOptimizeEffectStateModified ||
		state == imageOptimizeEffectStateImageReplaced
}

func preserveServerError(message string, cause error) error {
	if errs.AsDomainError(cause) != nil {
		return cause
	}
	return rootfsAtomicError(message, cause)
}

func annotateServerEffectFailure(primary error, evidence rootfsEffectEvidence, stateErr error) error {
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		domainErr = rootfsAtomicError("loopmount effect failed", primary)
	}
	result := *domainErr
	result.Details = maps.Clone(domainErr.Details)
	if result.Details == nil {
		result.Details = make(map[string]any)
	}
	result.Details["rootfs_modified"] = evidence.rootfsModified
	result.Details["image_replaced"] = evidence.imageReplaced
	result.Details["durability_uncertain"] = evidence.durabilityUncertain
	result.Details["outcome_unknown"] = evidence.outcomeUnknown
	if stateErr != nil {
		result.Details["loopmount_effect_state_invalid"] = true
		result.Details["loopmount_effect_state_detail"] = boundedServerCleanupDetail(
			"validate loopmount effect state",
			stateErr,
		)
		result.Err = errors.Join(domainErr.Err, stateErr)
	}
	return &result
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
