package errs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

// ─── New ───────────────────────────────────────────────────────────────────
// Rationale: New is the primary DomainError constructor. Incorrect Class/Op
// derivation leads to misclassification downstream (retry logic, HTTP status
// mapping, user-facing messages).

func TestNew(t *testing.T) {
	tests := map[string]struct {
		code errs.Code
		msg  string
		opts []errs.ErrorOption
		want errs.DomainError
	}{
		"unknown_code_class_fallback": {
			code: errs.Code("code.unknown"),
			msg:  "something odd",
			want: errs.DomainError{
				Code:    errs.Code("code.unknown"),
				Message: "something odd",
				Class:   errs.ClassUnknown,
				Op:      "",
			},
		},
		"vm_not_found_validation": {
			code: errs.CodeVMNotFound,
			msg:  "vm x not found",
			want: errs.DomainError{
				Code:    errs.CodeVMNotFound,
				Message: "vm x not found",
				Class:   errs.ClassValidation,
				Op:      "vm",
			},
		},
		"vm_create_failed_internal": {
			code: errs.CodeVMCreateFailed,
			msg:  "create failed",
			want: errs.DomainError{
				Code:    errs.CodeVMCreateFailed,
				Message: "create failed",
				Class:   errs.ClassInternal,
				Op:      "vm",
			},
		},
		"network_lease_exhausted_retryable": {
			code: errs.CodeNetworkLeaseExhausted,
			msg:  "no leases",
			want: errs.DomainError{
				Code:    errs.CodeNetworkLeaseExhausted,
				Message: "no leases",
				Class:   errs.ClassRetryable,
				Op:      "network",
			},
		},
		"image_not_found_validation": {
			code: errs.CodeImageNotFound,
			msg:  "image missing",
			want: errs.DomainError{
				Code:    errs.CodeImageNotFound,
				Message: "image missing",
				Class:   errs.ClassValidation,
				Op:      "image",
			},
		},
		"kernel_build_failed_retryable": {
			code: errs.CodeKernelBuildFailed,
			msg:  "build error",
			want: errs.DomainError{
				Code:    errs.CodeKernelBuildFailed,
				Message: "build error",
				Class:   errs.ClassRetryable,
				Op:      "kernel",
			},
		},
		"binary_already_exists_conflict": {
			code: errs.CodeBinaryAlreadyExists,
			msg:  "binary exists",
			want: errs.DomainError{
				Code:    errs.CodeBinaryAlreadyExists,
				Message: "binary exists",
				Class:   errs.ClassConflict,
				Op:      "binary",
			},
		},
		"volume_error_internal": {
			code: errs.CodeVolumeError,
			msg:  "volume error",
			want: errs.DomainError{
				Code:    errs.CodeVolumeError,
				Message: "volume error",
				Class:   errs.ClassInternal,
				Op:      "volume",
			},
		},
		"key_not_found_validation": {
			code: errs.CodeKeyNotFound,
			msg:  "key missing",
			want: errs.DomainError{
				Code:    errs.CodeKeyNotFound,
				Message: "key missing",
				Class:   errs.ClassValidation,
				Op:      "key",
			},
		},
		"privilege_required_interaction": {
			code: errs.CodePrivilegeRequired,
			msg:  "need sudo",
			want: errs.DomainError{
				Code:    errs.CodePrivilegeRequired,
				Message: "need sudo",
				Class:   errs.ClassNeedsInteraction,
				Op:      "host",
			},
		},
		"cloudinit_mode_error_validation": {
			code: errs.CodeCloudInitModeError,
			msg:  "bad mode",
			want: errs.DomainError{
				Code:    errs.CodeCloudInitModeError,
				Message: "bad mode",
				Class:   errs.ClassValidation,
				Op:      "cloudinit",
			},
		},
		"console_not_running_validation": {
			code: errs.CodeConsoleNotRunning,
			msg:  "console not running",
			want: errs.DomainError{
				Code:    errs.CodeConsoleNotRunning,
				Message: "console not running",
				Class:   errs.ClassValidation,
				Op:      "console",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := errs.New(tc.code, tc.msg, tc.opts...)

			if diff := cmp.Diff(tc.want, *got); diff != "" {
				t.Errorf("New() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// Rationale: Options (WithClass, WithEntity, WithDetails) modify the
// auto-derived error. Missing or incorrect option application causes
// downstream consumers to misclassify or lose context.

func TestNew_Options(t *testing.T) {
	t.Run("with_class_override", func(t *testing.T) {
		// CodeVMNotFound defaults to ClassValidation, override to ClassInternal
		got := errs.New(errs.CodeVMNotFound, "msg", errs.WithClass(errs.ClassInternal))
		assert.Equal(t, errs.ClassInternal, got.Class,
			"WithClass must override auto-derived Class")
	})

	t.Run("with_entity", func(t *testing.T) {
		got := errs.New(errs.CodeInternal, "msg", errs.WithEntity("my-vm"))
		assert.Equal(t, "my-vm", got.Entity)
	})

	t.Run("with_details_single", func(t *testing.T) {
		got := errs.New(errs.CodeInternal, "msg", errs.WithDetails(map[string]any{"key": "val"}))
		if diff := cmp.Diff(map[string]any{"key": "val"}, got.Details); diff != "" {
			t.Errorf("WithDetails mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("with_details_merge", func(t *testing.T) {
		got := errs.New(errs.CodeInternal, "msg",
			errs.WithDetails(map[string]any{"a": 1}),
			errs.WithDetails(map[string]any{"b": 2, "a": 99}),
		)
		want := map[string]any{"a": 99, "b": 2}
		if diff := cmp.Diff(want, got.Details); diff != "" {
			t.Errorf("WithDetails merge mismatch (-want +got):\n%s", diff)
		}
	})
}

// ─── Wrap ──────────────────────────────────────────────────────────────────
// Rationale: Wrap preserves the error chain and inherits Class from wrapped
// DomainError. Incorrect inheritance breaks retry logic and classification.

func TestWrap(t *testing.T) {
	t.Run("stdlib_error_inherits_class_unknown", func(t *testing.T) {
		cause := errors.New("stdlib error")
		got := errs.Wrap(errs.CodeInternal, cause)
		assert.Equal(t, errs.ClassUnknown, got.Class,
			"Wrapping stdlib error must get ClassUnknown")
		assert.Equal(t, "stdlib error", got.Message)
		assert.Equal(t, cause, got.Err)
	})

	t.Run("domain_error_inherits_class", func(t *testing.T) {
		inner := errs.New(errs.CodeVMNotFound, "inner")
		got := errs.Wrap(errs.CodeInternal, inner)
		assert.Equal(t, errs.ClassValidation, got.Class,
			"Wrapping DomainError must inherit its Class")
		assert.Equal(t, "inner", got.Message)
		assert.Equal(t, inner, got.Err)
	})

	t.Run("options_still_apply", func(t *testing.T) {
		cause := errors.New("root")
		got := errs.Wrap(errs.CodeInternal, cause,
			errs.WithEntity("my-entity"),
			errs.WithClass(errs.ClassConflict),
		)
		assert.Equal(t, "my-entity", got.Entity)
		assert.Equal(t, errs.ClassConflict, got.Class)
	})

	t.Run("op_derived_from_code", func(t *testing.T) {
		cause := errors.New("root")
		got := errs.Wrap(errs.CodeVMNotFound, cause)
		assert.Equal(t, "vm", got.Op)
	})
}

// ─── WrapMsg ───────────────────────────────────────────────────────────────
// Rationale: WrapMsg separates the user-facing message from the root cause.
// Wrong Class inheritance causes misclassification of wrapped errors.

func TestWrapMsg(t *testing.T) {
	t.Run("stdlib_error_class_unknown", func(t *testing.T) {
		cause := errors.New("root cause")
		got := errs.WrapMsg(errs.CodeDownloadFailed, "user-friendly message", cause)
		assert.Equal(t, errs.ClassUnknown, got.Class)
		assert.Equal(t, "user-friendly message", got.Message)
		assert.Equal(t, cause, got.Err)
	})

	t.Run("domain_error_inherits_class", func(t *testing.T) {
		inner := errs.New(errs.CodeNetworkLeaseExhausted, "no leases")
		got := errs.WrapMsg(errs.CodeInternal, "wrapped", inner)
		assert.Equal(t, errs.ClassRetryable, got.Class)
		assert.Equal(t, "wrapped", got.Message)
	})

	t.Run("options_still_apply", func(t *testing.T) {
		cause := errors.New("root")
		got := errs.WrapMsg(errs.CodeInternal, "msg", cause,
			errs.WithEntity("e"),
			errs.WithDetails(map[string]any{"x": 1}),
		)
		assert.Equal(t, "e", got.Entity)
		if diff := cmp.Diff(map[string]any{"x": 1}, got.Details); diff != "" {
			t.Errorf("WithDetails mismatch (-want +got):\n%s", diff)
		}
	})
}

// ─── NotFound ──────────────────────────────────────────────────────────────
// Rationale: NotFound forces ClassValidation regardless of the code's default
// Class. Without this, a retryable code used with NotFound would misclassify.

func TestNotFound(t *testing.T) {
	t.Run("forces_class_validation", func(t *testing.T) {
		// CodeKernelBuildFailed has ClassRetryable by default
		got := errs.NotFound(errs.CodeKernelBuildFailed, "kernel not found")
		assert.Equal(t, errs.ClassValidation, got.Class,
			"NotFound must force ClassValidation")
	})

	t.Run("op_derived_from_code", func(t *testing.T) {
		got := errs.NotFound(errs.CodeVMNotFound, "vm not found")
		assert.Equal(t, "vm", got.Op)
	})

	t.Run("message_preserved", func(t *testing.T) {
		got := errs.NotFound(errs.CodeBinaryNotFound, "binary missing")
		assert.Equal(t, "binary missing", got.Message)
	})

	t.Run("options_still_apply", func(t *testing.T) {
		got := errs.NotFound(errs.CodeImageNotFound, "img",
			errs.WithEntity("my-image"),
		)
		assert.Equal(t, "my-image", got.Entity)
	})
}

// ─── AlreadyExists ─────────────────────────────────────────────────────────
// Rationale: AlreadyExists forces ClassConflict regardless of the code's
// default Class. Without this, conflict errors could be misclassified.

func TestAlreadyExists(t *testing.T) {
	t.Run("forces_class_conflict", func(t *testing.T) {
		// CodeVMNotFound has ClassValidation by default
		got := errs.AlreadyExists(errs.CodeVMNotFound, "vm exists")
		assert.Equal(t, errs.ClassConflict, got.Class,
			"AlreadyExists must force ClassConflict")
	})

	t.Run("op_derived_from_code", func(t *testing.T) {
		got := errs.AlreadyExists(errs.CodeNetworkAlreadyExists, "net exists")
		assert.Equal(t, "network", got.Op)
	})

	t.Run("message_preserved", func(t *testing.T) {
		got := errs.AlreadyExists(errs.CodeVolumeAlreadyExists, "volume exists")
		assert.Equal(t, "volume exists", got.Message)
	})

	t.Run("options_still_apply", func(t *testing.T) {
		got := errs.AlreadyExists(errs.CodeKeyAlreadyExists, "key",
			errs.WithEntity("my-key"),
		)
		assert.Equal(t, "my-key", got.Entity)
	})
}

// ─── classForCode (indirect via New) ───────────────────────────────────────
// Rationale: classForCode drives error classification. Wrong Class breaks
// IsRetryable, IsNeedsInteraction, and HTTP status mapping.

// Tested indirectly through TestNew — every row in TestNew verifies
// auto-derived Class via the want.Class field.

// ─── opForCode (indirect via New) ──────────────────────────────────────────
// Rationale: opForCode sets the operation name. Wrong Op breaks logging
// and error aggregation by domain.

// Tested indirectly through TestNew — every row verifies auto-derived Op.

// ─── AsType[T] ─────────────────────────────────────────────────────────────
// Rationale: AsType extracts a *DomainError from an error chain using
// generics. A bug here breaks errors.As-based classification everywhere.

func TestAsType(t *testing.T) {
	t.Run("nil_error_returns_false", func(t *testing.T) {
		var nilErr error
		got, ok := errs.AsType[*errs.DomainError](nilErr)
		assert.False(t, ok)
		assert.Nil(t, got)
	})

	t.Run("non_domain_error_returns_false", func(t *testing.T) {
		stdErr := errors.New("standard error")
		got, ok := errs.AsType[*errs.DomainError](stdErr)
		assert.False(t, ok)
		assert.Nil(t, got)
	})

	t.Run("extracts_non_nil_domain_error", func(t *testing.T) {
		de := errs.New(errs.CodeInternal, "test")
		got, ok := errs.AsType[*errs.DomainError](de)
		assert.True(t, ok)
		assert.NotNil(t, got)
		assert.Equal(t, errs.CodeInternal, got.Code)
	})

	t.Run("extracts_first_domain_error_in_chain", func(t *testing.T) {
		// errors.As returns the FIRST *DomainError in the chain,
		// which is the wrapper, not the innermost.
		inner := errs.New(errs.CodeVMNotFound, "inner")
		wrapper := errs.Wrap(errs.CodeInternal, inner)
		got, ok := errs.AsType[*errs.DomainError](wrapper)
		assert.True(t, ok)
		require.NotNil(t, got)
		assert.Equal(t, errs.CodeInternal, got.Code,
			"AsType must return the outer *DomainError, not inner")
	})
}

// ─── AsDomainError ─────────────────────────────────────────────────────────
// Rationale: AsDomainError is the non-generic extraction helper. Used by
// IsNotFound, IsRetryable, IsNeedsInteraction.

func TestAsDomainError(t *testing.T) {
	t.Run("nil_input_returns_nil", func(t *testing.T) {
		got := errs.AsDomainError(nil)
		assert.Nil(t, got)
	})

	t.Run("non_domain_error_returns_nil", func(t *testing.T) {
		got := errs.AsDomainError(errors.New("std"))
		assert.Nil(t, got)
	})

	t.Run("extracts_domain_error", func(t *testing.T) {
		de := errs.New(errs.CodeInternal, "test")
		got := errs.AsDomainError(de)
		require.NotNil(t, got)
		assert.Equal(t, errs.CodeInternal, got.Code)
	})

	t.Run("extracts_from_wrapped_chain", func(t *testing.T) {
		inner := errs.New(errs.CodeInternal, "inner")
		wrapper := fmt.Errorf("wrapped: %w", inner)
		got := errs.AsDomainError(wrapper)
		require.NotNil(t, got)
		assert.Equal(t, "inner", got.Message)
	})
}

// ─── classFrom (indirect via Wrap/WrapMsg) ─────────────────────────────────
// Rationale: classFrom extracts the Class from a wrapped *DomainError.
// Tested indirectly through TestWrap and TestWrapMsg.

// ─── IsNotFound ────────────────────────────────────────────────────────────
// Rationale: IsNotFound is used in API handlers to return 404 status codes.
// Missing a code returns 500 instead of 404.

func TestIsNotFound(t *testing.T) {
	notFoundCodes := []errs.Code{
		errs.CodeVMNotFound,
		errs.CodeNetworkNotFound,
		errs.CodeImageNotFound,
		errs.CodeKernelNotFound,
		errs.CodeBinaryNotFound,
		errs.CodeVolumeNotFound,
		errs.CodeKeyNotFound,
		errs.CodeCPSourceNotFound,
		errs.CodeBundledAssetNotFound,
		errs.CodeFirecrackerSocketNotFound,
		errs.CodeLoopMountBinaryNotFound,
		errs.CodeCPVMNotFound,
	}

	t.Run("all_not_found_codes_return_true", func(t *testing.T) {
		for _, code := range notFoundCodes {
			t.Run(string(code), func(t *testing.T) {
				de := errs.New(code, "not found")
				assert.True(t, errs.IsNotFound(de),
					"IsNotFound must return true for code %q", code)
			})
		}
	})

	t.Run("nil_error_returns_false", func(t *testing.T) {
		assert.False(t, errs.IsNotFound(nil))
	})

	t.Run("non_not_found_code_returns_false", func(t *testing.T) {
		de := errs.New(errs.CodeInternal, "internal error")
		assert.False(t, errs.IsNotFound(de))
	})

	t.Run("non_domain_error_returns_false", func(t *testing.T) {
		assert.False(t, errs.IsNotFound(errors.New("std")))
	})
}

// ─── IsRetryable ───────────────────────────────────────────────────────────
// Rationale: IsRetryable drives automatic retry loops. False negatives cause
// permanent failures on transient errors; false positives cause infinite loops.

func TestIsRetryable(t *testing.T) {
	t.Run("nil_error_returns_false", func(t *testing.T) {
		assert.False(t, errs.IsRetryable(nil))
	})

	t.Run("non_domain_error_returns_false", func(t *testing.T) {
		assert.False(t, errs.IsRetryable(errors.New("std")))
	})

	t.Run("validation_class_returns_false", func(t *testing.T) {
		de := errs.New(errs.CodeVMNotFound, "not found")
		assert.False(t, errs.IsRetryable(de))
	})

	t.Run("kernel_build_failed_is_retryable", func(t *testing.T) {
		de := errs.New(errs.CodeKernelBuildFailed, "build failed")
		assert.True(t, errs.IsRetryable(de))
	})

	t.Run("network_lease_exhausted_is_retryable", func(t *testing.T) {
		de := errs.New(errs.CodeNetworkLeaseExhausted, "no leases")
		assert.True(t, errs.IsRetryable(de))
	})

	t.Run("loop_mount_timeout_is_retryable", func(t *testing.T) {
		de := errs.New(errs.CodeLoopMountTimeout, "timeout")
		assert.True(t, errs.IsRetryable(de))
	})
}

// ─── IsNeedsInteraction ────────────────────────────────────────────────────
// Rationale: IsNeedsInteraction signals the CLI to prompt for sudo/input.
// False negatives cause silent failures; false positives break automation.

func TestIsNeedsInteraction(t *testing.T) {
	t.Run("nil_error_returns_false", func(t *testing.T) {
		assert.False(t, errs.IsNeedsInteraction(nil))
	})

	t.Run("non_domain_error_returns_false", func(t *testing.T) {
		assert.False(t, errs.IsNeedsInteraction(errors.New("std")))
	})

	t.Run("validation_class_returns_false", func(t *testing.T) {
		de := errs.New(errs.CodeVMNotFound, "not found")
		assert.False(t, errs.IsNeedsInteraction(de))
	})

	t.Run("privilege_required_returns_true", func(t *testing.T) {
		de := errs.New(errs.CodePrivilegeRequired, "need sudo")
		assert.True(t, errs.IsNeedsInteraction(de))
	})
}

// ─── FormatExceptionDebug ──────────────────────────────────────────────────
// Rationale: FormatExceptionDebug is used in logs and error reports. Without
// stack, it returns the error string. With stack, it includes a full trace.

func TestFormatExceptionDebug(t *testing.T) {
	t.Run("without_stack_returns_message", func(t *testing.T) {
		de := errs.New(errs.CodeInternal, "something went wrong")
		got := errs.FormatExceptionDebug(de, false)
		assert.Equal(t, "something went wrong", got)
	})

	t.Run("with_stack_contains_goroutine", func(t *testing.T) {
		de := errs.New(errs.CodeInternal, "debug me")
		got := errs.FormatExceptionDebug(de, true)
		assert.Contains(t, got, "debug me")
		assert.Contains(t, got, "goroutine")
	})
}

// ─── DomainError.Error ─────────────────────────────────────────────────────
// Rationale: Error() is the standard error interface. If it returns something
// other than Message, errors.As/Is and user display break.

func TestDomainError_Error(t *testing.T) {
	de := errs.New(errs.CodeInternal, "user message")
	assert.Equal(t, "user message", de.Error())
}

// ─── DomainError.Unwrap ────────────────────────────────────────────────────
// Rationale: Unwrap enables errors.Is/As chain walking. If it returns the
// wrong error, the entire classification chain breaks.

func TestDomainError_Unwrap(t *testing.T) {
	t.Run("returns_err_field", func(t *testing.T) {
		cause := errors.New("original cause")
		de := errs.Wrap(errs.CodeInternal, cause)
		assert.Equal(t, cause, de.Unwrap())
	})

	t.Run("returns_nil_when_no_cause", func(t *testing.T) {
		de := errs.New(errs.CodeInternal, "no cause")
		assert.Nil(t, de.Unwrap())
	})
}
