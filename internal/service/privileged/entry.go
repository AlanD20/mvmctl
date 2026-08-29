package privileged

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"mvmctl/pkg/errs"
)

type runtimeDeps struct {
	identity      identityDeps
	environment   environmentDeps
	executable    executableDeps
	authorization authorizationDeps
	control       controlDeps
}

// Run validates and routes one privileged protocol invocation.
// CRITICAL: This entry point accepts only the reserved versioned marker and fixed action switch.
func Run(ctx context.Context, args []string) error {
	return run(ctx, args, newStdinControlEndpoint(os.Stdin), runtimeDeps{
		identity:      realIdentityDeps(),
		environment:   realEnvironmentDeps(),
		executable:    realExecutableDeps(),
		authorization: realAuthorizationDeps(),
		control:       realControlDeps(),
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
	responseDeadline := boundedResponseDeadline(ctx, deps.control.now())
	peer, err := admitControlEndpoint(ctx, endpoint, deps.control)
	if err != nil {
		return err
	}
	caller, err := invokingIdentity(deps.identity)
	if err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, responseDeadline, deps.control)
	}
	if err := authenticateControlPeer(caller, peer); err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, responseDeadline, deps.control)
	}
	if err := sanitizeEnvironment(deps.environment); err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, responseDeadline, deps.control)
	}
	pinnedExecutable, err := verifySystemExecutable(deps.executable)
	if err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, responseDeadline, deps.control)
	}
	defer func() {
		if err := pinnedExecutable.close(); err != nil {
			slog.Warn("close privileged executable descriptors", "error", err)
		}
	}()
	if err := authorizeCaller(caller, deps.authorization); err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, responseDeadline, deps.control)
	}
	if err := dispatch(ctx, request, caller); err != nil {
		return deliverOperationError(ctx, endpoint, request.action, err, responseDeadline, deps.control)
	}
	return nil
}

func dispatch(_ context.Context, request invocation, _ callerIdentity) error {
	// The action switch intentionally starts empty. Capability-specific typed
	// handlers are added only as their migrations land and receive abuse tests.
	switch request.action {
	default:
		return errs.New(
			errs.CodeValidationFailed,
			fmt.Sprintf("unknown privileged action %q", request.action),
		)
	}
}
