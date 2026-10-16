package privileged

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
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
	identityCalls := 0
	deps := executableDeps{pin: func(got context.Context) (*system.PinnedExecutable, error) {
		assert.Equal(t, "retained", got.Value(key))
		return want, nil
	}, identity: func(got context.Context, capability *system.PinnedExecutable) (system.PinnedExecutableIdentity, error) {
		identityCalls++
		assert.Equal(t, "retained", got.Value(key))
		assert.Same(t, want, capability)
		return system.PinnedExecutableIdentity{SHA256: "trusted"}, nil
	}, release: func(context.Context, *system.PinnedExecutable) error { return nil }}

	got, err := verifySystemExecutable(ctx, deps)
	require.NoError(t, err)
	assert.Same(t, want, got)
	assert.Equal(t, 1, identityCalls)
}

func TestVerifySystemExecutableRejectsMissingOrEmptyAuthority(t *testing.T) {
	t.Parallel()

	tests := map[string]executableDeps{
		"missing_constructor": {},
		"missing_lifecycle": {
			pin: func(context.Context) (*system.PinnedExecutable, error) { return &system.PinnedExecutable{}, nil },
		},
		"missing_authority": {
			pin: func(context.Context) (*system.PinnedExecutable, error) { return nil, nil },
			identity: func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error) {
				return system.PinnedExecutableIdentity{}, nil
			},
			release: func(context.Context, *system.PinnedExecutable) error { return nil },
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

func TestVerifySystemExecutableRejectsInactiveAuthority(t *testing.T) {
	t.Parallel()

	deps := realExecutableDeps()
	deps.pin = func(context.Context) (*system.PinnedExecutable, error) {
		return &system.PinnedExecutable{}, nil
	}
	capability, err := verifySystemExecutable(t.Context(), deps)
	assert.Nil(t, capability)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, errs.CodeInternal, domainErr.Code)
	assert.Equal(t, errs.ClassInternal, domainErr.Class)
	assert.Equal(t, infra.SystemBinaryPath, domainErr.Entity)
}

func TestVerifySystemExecutablePreservesValidationAndCleanupErrors(t *testing.T) {
	t.Parallel()

	want := &system.PinnedExecutable{}
	validationCause := errors.New("inactive authority")
	cleanupCause := errors.New("close inactive authority")
	validationErr := errs.WrapMsg(
		errs.CodePrivilegeRequired,
		"reject inactive authority",
		validationCause,
		errs.WithClass(errs.ClassNeedsInteraction),
		errs.WithEntity(infra.SystemBinaryPath),
	)
	releaseCalls := 0
	capability, err := verifySystemExecutable(t.Context(), executableDeps{
		pin: func(context.Context) (*system.PinnedExecutable, error) { return want, nil },
		identity: func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error) {
			return system.PinnedExecutableIdentity{}, validationErr
		},
		release: func(context.Context, *system.PinnedExecutable) error {
			releaseCalls++
			return cleanupCause
		},
	})
	assert.Nil(t, capability)
	assert.Equal(t, 1, releaseCalls)
	assert.ErrorIs(t, err, validationErr)
	assert.ErrorIs(t, err, validationCause)
	assert.ErrorIs(t, err, cleanupCause)
	domainErr := errs.AsDomainError(err)
	require.NotNil(t, domainErr)
	assert.Equal(t, true, domainErr.Details["pinned_executable_cleanup_failed"])
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
		identity: func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error) {
			return system.PinnedExecutableIdentity{}, nil
		},
		release: func(context.Context, *system.PinnedExecutable) error { return nil },
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
	if diff := cmp.Diff(map[string]any{
		"existing":                         true,
		"pinned_executable_cleanup_failed": true,
	}, domainErr.Details); diff != "" {
		t.Fatalf("combined details mismatch (-want +got):\n%s", diff)
	}
	assert.ErrorIs(t, combined, primaryCause)
	assert.ErrorIs(t, combined, primary)
	assert.ErrorIs(t, combined, cleanupCause)
	if diff := cmp.Diff(map[string]any{"existing": true}, primary.Details); diff != "" {
		t.Fatalf("primary details mutated (-want +got):\n%s", diff)
	}
	assert.Same(t, cleanup, appendPrivilegedExecutableReleaseError(nil, cleanup))
}

func trustedExecutableDeps() executableDeps {
	return executableDeps{
		pin: func(context.Context) (*system.PinnedExecutable, error) {
			return &system.PinnedExecutable{}, nil
		},
		identity: func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error) {
			return system.PinnedExecutableIdentity{SHA256: "test-authority"}, nil
		},
		release: func(context.Context, *system.PinnedExecutable) error { return nil },
	}
}

func rejectedExecutableDeps(message string) executableDeps {
	return executableDeps{
		pin: func(context.Context) (*system.PinnedExecutable, error) {
			return nil, errs.New(
				errs.CodePrivilegeRequired,
				message,
				errs.WithEntity(infra.SystemBinaryPath),
			)
		},
		identity: func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error) {
			return system.PinnedExecutableIdentity{}, nil
		},
		release: func(context.Context, *system.PinnedExecutable) error { return nil },
	}
}
