package privileged

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"

	"mvmctl/internal/infra"
	"mvmctl/internal/service/agent"
	"mvmctl/internal/service/jailer"
	"mvmctl/internal/service/loopmount"
	"mvmctl/pkg/errs"
)

type runtimeDeps struct {
	identity       identityDeps
	environment    environmentDeps
	executable     executableDeps
	authorization  authorizationDeps
	control        controlDeps
	trustedRelease trustedReleaseHandlerEffects
	loopMount      loopMountHandlerEffects
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
	loopMount := loopmount.NewProvisionInspectServer(systemLoopMountAgentBinary)
	return run(ctx, args, newStdinControlEndpoint(os.Stdin), runtimeDeps{
		identity:      realIdentityDeps(),
		environment:   realEnvironmentDeps(),
		executable:    realExecutableDeps(),
		authorization: realAuthorizationDeps(),
		control:       realControlDeps(),
		trustedRelease: trustedReleaseHandlerEffects{
			install: trustedRelease.Install,
			remove:  trustedRelease.Remove,
		},
		loopMount: loopMountHandlerEffects{
			provisionVM:  loopMount.ProvisionVM,
			inspectImage: loopMount.InspectImage,
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
		return deliverOperationError(ctx, endpoint, request.action, err, deps.control)
	}
	if err := authenticateControlPeer(caller, peer); err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, deps.control)
	}
	if err := sanitizeEnvironment(deps.environment); err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, deps.control)
	}
	pinnedExecutable, err := verifySystemExecutable(ctx, deps.executable)
	if err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, deps.control)
	}
	defer func() {
		returnErr = appendPrivilegedExecutableReleaseError(
			returnErr,
			deps.executable.release(context.WithoutCancel(ctx), pinnedExecutable),
		)
	}()
	if err := authorizeCaller(caller, deps.authorization); err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, deps.control)
	}
	disposition, err := dispatch(ctx, request, endpoint, caller, deps)
	if err != nil && disposition == dispatchUnhandled {
		return deliverOperationError(ctx, endpoint, request.action, err, deps.control)
	}
	return err
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
	default:
		return dispatchUnhandled, errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("unknown privileged action %q", request.action),
		)
	}
}
