package privileged

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"mvmctl/pkg/errs"
)

type runtimeDeps struct {
	identity      identityDeps
	environment   environmentDeps
	executable    executableDeps
	authorization authorizationDeps
}

// Run validates and routes one privileged protocol invocation.
// CRITICAL: This entry point accepts only the reserved versioned marker and fixed action switch.
func Run(ctx context.Context, args []string, input io.Reader) error {
	return run(ctx, args, input, runtimeDeps{
		identity:      realIdentityDeps(),
		environment:   realEnvironmentDeps(),
		executable:    realExecutableDeps(),
		authorization: realAuthorizationDeps(),
	})
}

func run(ctx context.Context, args []string, input io.Reader, deps runtimeDeps) error {
	if err := ctx.Err(); err != nil {
		return errs.WrapMsg(errs.CodeInternal, "privileged request context ended", err)
	}
	request, err := parseInvocation(args)
	if err != nil {
		return err
	}
	caller, err := invokingIdentity(deps.identity)
	if err != nil {
		return err
	}
	if err := sanitizeEnvironment(deps.environment); err != nil {
		return err
	}
	pinnedExecutable, err := verifySystemExecutable(deps.executable)
	if err != nil {
		return err
	}
	defer func() {
		if err := pinnedExecutable.close(); err != nil {
			slog.Warn("close privileged executable descriptors", "error", err)
		}
	}()
	if err := authorizeCaller(caller, deps.authorization); err != nil {
		return err
	}
	return dispatch(ctx, request, caller, input)
}

func dispatch(_ context.Context, request invocation, _ callerIdentity, _ io.Reader) error {
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
