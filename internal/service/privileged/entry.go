package privileged

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/hostadmission"
	"mvmctl/internal/lib/vmexecsessionwire"
	"mvmctl/internal/service/agent"
	"mvmctl/internal/service/firewall"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/loopmount"
	"mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

type runtimeDeps struct {
	identity               identityDeps
	environment            environmentDeps
	executable             executableDeps
	authorization          authorizationDeps
	control                controlDeps
	trustedRelease         trustedReleaseHandlerEffects
	runtimeStatus          runtimeStatusObserve
	runtimeOwnerInventory  runtimeOwnerInventoryObserve
	loopMount              loopMountHandlerEffects
	vmExecSession          vmExecSessionHandlerEffects
	vmExecConnectionReader vmExecConnectionReaderDeps
}

type dispatchDisposition uint8

const (
	dispatchUnhandled dispatchDisposition = iota
	dispatchHandled
)

// Run validates and routes one privileged protocol invocation.
// CRITICAL: This entry point accepts only the reserved versioned marker and fixed action switch.
func Run(ctx context.Context, args []string) error {
	trustedRelease := jailer.NewTrustedReleaseServer()
	runtimeStatus := jailer.NewRuntimeStatusServer()
	runtimeOwnerInventory := jailer.NewRuntimeOwnerInventoryServer()
	loopMount := loopmount.NewProvisionInspectServer(systemLoopMountAgentBinary)
	imageShrink := loopmount.NewImageShrinkServer()
	vmExecSession, err := jailer.NewVMExecSessionServer()
	if err != nil {
		return err
	}
	return run(ctx, args, newStdinControlEndpoint(os.Stdin), runtimeDeps{
		identity:               realIdentityDeps(),
		environment:            realEnvironmentDeps(),
		executable:             realExecutableDeps(),
		authorization:          realAuthorizationDeps(),
		control:                realControlDeps(),
		vmExecSession:          newVMExecSessionHandlerEffects(vmExecSession),
		vmExecConnectionReader: realVMExecConnectionReaderDeps(),
		runtimeStatus:          runtimeStatus.Observe,
		runtimeOwnerInventory:  runtimeOwnerInventory.Observe,
		trustedRelease: trustedReleaseHandlerEffects{
			install: trustedRelease.Install,
			remove:  trustedRelease.Remove,
		},
		loopMount: loopMountHandlerEffects{
			provisionVM:   loopMount.ProvisionVM,
			inspectImage:  loopMount.InspectImage,
			optimizeImage: imageShrink.OptimizeImage,
		},
	})
}

func systemLoopMountAgentBinary(ctx context.Context) ([]byte, error) {
	if err := validateLoopMountAgentBinaryContext(ctx); err != nil {
		return nil, err
	}
	return copyEmbeddedLoopMountAgentBinary(ctx, agent.AgentBinary())
}

func copyEmbeddedLoopMountAgentBinary(ctx context.Context, embedded []byte) ([]byte, error) {
	if err := validateLoopMountAgentBinaryContext(ctx); err != nil {
		return nil, err
	}
	if len(embedded) == 0 {
		return nil, errs.New(
			errs.CodeLoopMountError,
			"embedded managed VM agent binary is unavailable",
			errs.WithClass(errs.ClassInternal),
		)
	}
	return bytes.Clone(embedded), nil
}

func validateLoopMountAgentBinaryContext(ctx context.Context) error {
	if ctx == nil {
		return errs.New(errs.CodeInternal, "loopmount agent binary context is required")
	}
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(
			errs.CodeLoopMountError,
			"load embedded managed VM agent binary",
			err,
			errs.WithClass(errs.ClassInternal),
		)
	}
	return nil
}

func run(
	ctx context.Context,
	args []string,
	endpoint controlEndpoint,
	deps runtimeDeps,
) (returnErr error) {
	if ctx == nil {
		return errs.New(errs.CodeInternal, "privileged request context is required")
	}
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeInternal, "privileged request context ended", err)
	}
	request, err := parseInvocation(args)
	if err != nil {
		return err
	}
	peer, err := admitControlEndpoint(ctx, endpoint, deps.control)
	if err != nil {
		return err
	}
	caller, err := invokingIdentity(deps.identity)
	if err != nil {
		return deliverPreDispatchError(ctx, endpoint, request.action, err, deps.control)
	}
	if err := authenticateControlPeer(caller, peer); err != nil {
		return deliverPreDispatchError(ctx, endpoint, request.action, err, deps.control)
	}
	if err := sanitizeEnvironment(deps.environment); err != nil {
		return deliverPreDispatchError(ctx, endpoint, request.action, err, deps.control)
	}
	pinnedExecutable, err := verifySystemExecutable(ctx, deps.executable)
	if err != nil {
		return deliverPreDispatchError(ctx, endpoint, request.action, err, deps.control)
	}
	defer func() {
		returnErr = appendPrivilegedExecutableReleaseError(
			returnErr,
			deps.executable.release(context.WithoutCancel(ctx), pinnedExecutable),
		)
	}()
	admission, err := hostadmission.AcquireOperation(ctx)
	if err != nil {
		return deliverPreDispatchError(ctx, endpoint, request.action, err, deps.control)
	}
	defer func() {
		returnErr = appendHostAdmissionReleaseError(
			returnErr, admission.Release(context.WithoutCancel(ctx)),
		)
	}()
	// CRITICAL: queued requests must re-authorize after the admission lock.
	// Reset retains exclusive admission and leaves its durable state disabled.
	if err := authorizeCaller(caller, deps.authorization); err != nil {
		return deliverPreDispatchError(ctx, endpoint, request.action, err, deps.control)
	}
	disposition, err := dispatch(ctx, request, endpoint, caller, deps)
	if err != nil && disposition == dispatchUnhandled {
		return deliverOperationError(ctx, endpoint, request.action, err, deps.control)
	}
	return err
}

func deliverPreDispatchError(
	ctx context.Context,
	endpoint controlEndpoint,
	action string,
	operationErr error,
	deps controlDeps,
) error {
	if action == vmexecsessionwire.Action {
		return deliverVMExecFailure(ctx, endpoint, operationErr, deps)
	}
	return deliverOperationError(ctx, endpoint, action, operationErr, deps)
}

func appendPrivilegedExecutableReleaseError(primary error, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	domain := errs.AsDomainError(primary)
	if domain == nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			primary.Error(),
			errors.Join(primary, cleanup),
			errs.WithClass(errs.ClassInternal),
			errs.WithEntity(infra.SystemBinaryPath),
			errs.WithDetails(map[string]any{"pinned_executable_cleanup_failed": true}),
		)
	}
	result := *domain
	result.Details = maps.Clone(domain.Details)
	if result.Details == nil {
		result.Details = make(map[string]any)
	}
	result.Details["pinned_executable_cleanup_failed"] = true
	result.Err = errors.Join(primary, cleanup)
	return &result
}

func dispatch(
	ctx context.Context,
	request invocation,
	endpoint controlEndpoint,
	caller callerIdentity,
	deps runtimeDeps,
) (dispatchDisposition, error) {
	switch request.action {
	case firewall.ReadinessAction:
		return dispatchHandled, handleFirewallReadiness(ctx, endpoint, deps.control)
	case network.HostServiceInspectAction:
		return dispatchHandled, handleInspectHostService(ctx, endpoint, caller, deps.control)
	case network.HostServiceApplyAction:
		return dispatchHandled, handleApplyHostService(ctx, endpoint, caller, deps.control)
	case network.HostServiceRemoveAction:
		return dispatchHandled, handleRemoveHostService(ctx, endpoint, caller, deps.control)
	case network.ManagedNetworkCreateAction:
		return dispatchHandled, handleCreateManagedNetwork(ctx, endpoint, caller, deps.control)
	case network.ManagedNetworkRemoveAction:
		return dispatchHandled, handleRemoveManagedNetwork(ctx, endpoint, caller, deps.control)
	case jailer.VMLaunchAction:
		return dispatchHandled, handleVMLaunch(ctx, endpoint, caller, deps.control)
	case jailer.VMStopAction:
		return dispatchHandled, handleVMStop(ctx, endpoint, caller, deps.control)
	case jailer.VMVolumeAttachAction:
		return dispatchHandled, handleVMVolumeAttach(ctx, endpoint, caller, deps.control)
	case jailer.VMVolumeDetachAction:
		return dispatchHandled, handleVMVolumeDetach(ctx, endpoint, caller, deps.control)
	case jailer.VMPauseAction, jailer.VMResumeAction, jailer.VMStartInstanceAction:
		return dispatchHandled, handleVMAPIBroker(ctx, endpoint, caller, request.action, deps.control)
	case jailer.HostVsockOpenAction:
		return dispatchHandled, handleHostVsockOpen(ctx, endpoint, caller, deps.control)
	case jailer.VMAbortAction, jailer.VMCleanupAction, jailer.VMRemoveAction:
		return dispatchHandled, handleVMCleanup(ctx, endpoint, caller, request.action, deps.control)
	case network.NetworkTrafficPolicyApplyAction:
		return dispatchHandled, handleApplyNetworkTrafficPolicy(ctx, endpoint, caller, deps.control)
	case network.VMTrafficPolicyApplyAction:
		return dispatchHandled, handleApplyVMTrafficPolicy(ctx, endpoint, caller, deps.control)
	case network.NetworkTrafficPolicyRemoveAction:
		return dispatchHandled, handleRemoveNetworkTrafficPolicy(ctx, endpoint, caller, deps.control)
	case network.VMTrafficPolicyRemoveAction:
		return dispatchHandled, handleRemoveVMTrafficPolicy(ctx, endpoint, caller, deps.control)
	case jailer.RuntimeStatusAction:
		return dispatchHandled, handleRuntimeStatus(ctx, endpoint, caller, deps.runtimeStatus, deps.control)
	case jailer.RuntimeOwnerInventoryAction:
		return dispatchHandled, handleRuntimeOwnerInventory(
			ctx,
			endpoint,
			caller,
			deps.runtimeOwnerInventory,
			deps.control,
		)
	case jailer.TrustedReleaseInstallAction:
		return dispatchHandled, handleTrustedReleaseInstall(ctx, endpoint, deps.trustedRelease, deps.control)
	case jailer.TrustedReleaseRemoveAction:
		return dispatchHandled, handleTrustedReleaseRemove(ctx, endpoint, deps.trustedRelease, deps.control)
	case loopmount.VMProvisionAction:
		return dispatchHandled, handleLoopMountProvisionVM(
			ctx,
			endpoint,
			loopmount.Caller{UID: caller.uid, GID: caller.gid},
			deps.loopMount,
			deps.control,
		)
	case loopmount.ImageInspectAction:
		return dispatchHandled, handleLoopMountInspectImage(
			ctx,
			endpoint,
			loopmount.Caller{UID: caller.uid, GID: caller.gid},
			deps.loopMount,
			deps.control,
		)
	case loopmount.ImageOptimizeAction:
		return dispatchHandled, handleLoopMountOptimizeImage(
			ctx,
			endpoint,
			loopmount.Caller{UID: caller.uid, GID: caller.gid},
			deps.loopMount,
			deps.control,
		)
	case vmexecsessionwire.Action:
		return dispatchHandled, handleVMExecSession(
			ctx,
			endpoint,
			caller,
			deps.vmExecSession,
			vmExecSessionHandlerDeps{
				control:          deps.control,
				connectionReader: deps.vmExecConnectionReader,
			},
		)
	default:
		return dispatchUnhandled, errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("unknown privileged action %q", request.action),
		)
	}
}
