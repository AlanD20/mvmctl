package privileged

import (
	"context"
	"errors"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

type executableDeps struct {
	pin func(context.Context) (*system.PinnedExecutable, error)
}

func verifySystemExecutable(
	ctx context.Context,
	deps executableDeps,
) (*system.PinnedExecutable, error) {
	if deps.pin == nil {
		return nil, missingPinnedExecutableError("pinned executable constructor is unavailable")
	}
	capability, err := deps.pin(ctx)
	if err != nil {
		return nil, err
	}
	if capability == nil {
		return nil, missingPinnedExecutableError("pinned executable authority is missing")
	}
	return capability, nil
}

func missingPinnedExecutableError(message string) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeInternal,
		message,
		errors.New(message),
		errs.WithClass(errs.ClassInternal),
		errs.WithEntity(infra.SystemBinaryPath),
	)
}

func realExecutableDeps() executableDeps {
	return executableDeps{pin: system.PinSystemExecutable}
}
