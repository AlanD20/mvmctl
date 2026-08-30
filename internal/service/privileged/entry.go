package privileged

import (
	"context"
	"fmt"
	"log/slog"
	"os"

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
	loopMount := loopmount.NewProvisionInspectServer()
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

func run(ctx context.Context, args []string, endpoint controlEndpoint, deps runtimeDeps) error {
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
	pinnedExecutable, err := verifySystemExecutable(deps.executable)
	if err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, deps.control)
	}
	defer func() {
		if err := pinnedExecutable.close(); err != nil {
			slog.Warn("close privileged executable descriptors", "error", err)
		}
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
