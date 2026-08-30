package privileged

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

func TestVerifySystemExecutableReturnsConcretePinnedAuthority(t *testing.T) {
	t.Parallel()

	type contextKey string
	const key contextKey = "receiver"
	ctx := context.WithValue(t.Context(), key, "retained")
	want := &system.PinnedExecutable{}
	deps := executableDeps{pin: func(got context.Context) (*system.PinnedExecutable, error) {
		assert.Equal(t, "retained", got.Value(key))
		return want, nil
	}}

	got, err := verifySystemExecutable(ctx, deps)
	require.NoError(t, err)
	assert.Same(t, want, got)
}

func TestVerifySystemExecutableRejectsMissingOrEmptyAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]executableDeps{
		"missing_constructor": {},
		"missing_authority": {
			pin: func(context.Context) (*system.PinnedExecutable, error) { return nil, nil },
		},
	}
	for name, deps := range tests {
		t.Run(name, func(t *testing.T) {
			capability, err := verifySystemExecutable(t.Context(), deps)
			assert.Nil(t, capability)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeInternal, domainErr.Code)
			assert.Equal(t, errs.ClassInternal, domainErr.Class)
			assert.Equal(t, infra.SystemBinaryPath, domainErr.Entity)
		})
	}
}

func TestVerifySystemExecutablePreservesAdmissionError(t *testing.T) {
	t.Parallel()

	cause := errors.New("canonical image rejected")
	want := errs.WrapMsg(
		errs.CodePrivilegeRequired,
		"reject system executable",
		cause,
		errs.WithClass(errs.ClassNeedsInteraction),
		errs.WithEntity(infra.SystemBinaryPath),
	)
	capability, err := verifySystemExecutable(t.Context(), executableDeps{
		pin: func(context.Context) (*system.PinnedExecutable, error) { return nil, want },
	})
	assert.Nil(t, capability)
	assert.Same(t, want, err)
	assert.ErrorIs(t, err, cause)
}

func TestAppendPrivilegedExecutableReleaseErrorPreservesPrimaryContract(t *testing.T) {
	t.Parallel()

	primaryCause := errors.New("dispatch failed")
	cleanupCause := errors.New("descriptor close failed")
	primary := errs.WrapMsg(
		errs.CodeValidationFailed,
		"reject action",
		primaryCause,
		errs.WithClass(errs.ClassValidation),
		errs.WithEntity("action"),
		errs.WithDetails(map[string]any{"existing": true}),
	)
	cleanup := errs.WrapMsg(
		errs.CodeInternal,
		"close authority",
		cleanupCause,
		errs.WithClass(errs.ClassInternal),
	)

	combined := appendPrivilegedExecutableReleaseError(primary, cleanup)
	domainErr := errs.AsDomainError(combined)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeValidationFailed, domainErr.Code)
	assert.Equal(t, errs.ClassValidation, domainErr.Class)
	assert.Equal(t, "action", domainErr.Entity)
	assert.Equal(t, map[string]any{
		"existing":                         true,
		"pinned_executable_cleanup_failed": true,
	}, domainErr.Details)
	assert.ErrorIs(t, combined, primaryCause)
	assert.ErrorIs(t, combined, cleanupCause)
	assert.Equal(t, map[string]any{"existing": true}, primary.Details)
	assert.Same(t, cleanup, appendPrivilegedExecutableReleaseError(nil, cleanup))
}

func trustedExecutableDeps() executableDeps {
	return executableDeps{pin: func(context.Context) (*system.PinnedExecutable, error) {
		return &system.PinnedExecutable{}, nil
	}}
}

func rejectedExecutableDeps(message string) executableDeps {
	return executableDeps{pin: func(context.Context) (*system.PinnedExecutable, error) {
		return nil, errs.New(
			errs.CodePrivilegeRequired,
			message,
			errs.WithEntity(infra.SystemBinaryPath),
		)
	}}
}
