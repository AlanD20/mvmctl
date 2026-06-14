package errs_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/pkg/errs"
)

// ─── OperationResult.IsOK ──────────────────────────────────────────────────
// Rationale: IsOK gates success-path decisions in the API layer. A wrong
// classification causes the caller to treat errors as success or vice versa.

func TestOperationResult_IsOK(t *testing.T) {
	t.Run("error_status_returns_false", func(t *testing.T) {
		r := &errs.OperationResult{Status: "error"}
		assert.False(t, r.IsOK())
	})

	t.Run("failure_status_returns_false", func(t *testing.T) {
		r := &errs.OperationResult{Status: "failure"}
		assert.False(t, r.IsOK())
	})

	t.Run("success_status_returns_true", func(t *testing.T) {
		r := &errs.OperationResult{Status: "success"}
		assert.True(t, r.IsOK())
	})

	t.Run("skipped_status_returns_true", func(t *testing.T) {
		r := &errs.OperationResult{Status: "skipped"}
		assert.True(t, r.IsOK())
	})

	t.Run("warning_status_returns_true", func(t *testing.T) {
		r := &errs.OperationResult{Status: "warning"}
		assert.True(t, r.IsOK())
	})
}

// ─── OperationResult.IsError ───────────────────────────────────────────────
// Rationale: IsError gates error handling paths. Symmetric counterpart to
// IsOK; both must agree on which statuses are success vs error.

func TestOperationResult_IsError(t *testing.T) {
	t.Run("success_returns_false", func(t *testing.T) {
		r := &errs.OperationResult{Status: "success"}
		assert.False(t, r.IsError())
	})

	t.Run("skipped_returns_false", func(t *testing.T) {
		r := &errs.OperationResult{Status: "skipped"}
		assert.False(t, r.IsError())
	})

	t.Run("warning_returns_false", func(t *testing.T) {
		r := &errs.OperationResult{Status: "warning"}
		assert.False(t, r.IsError())
	})

	t.Run("error_status_returns_true", func(t *testing.T) {
		r := &errs.OperationResult{Status: "error"}
		assert.True(t, r.IsError())
	})

	t.Run("failure_status_returns_true", func(t *testing.T) {
		r := &errs.OperationResult{Status: "failure"}
		assert.True(t, r.IsError())
	})
}

// ─── OperationResult.ToError ───────────────────────────────────────────────
// Rationale: ToError converts OperationResult back to *DomainError for
// uniform error handling. Losing the DomainError type breaks classification.

func TestOperationResult_ToError(t *testing.T) {
	t.Run("nil_receiver_returns_nil", func(t *testing.T) {
		var r *errs.OperationResult
		got := r.ToError()
		assert.Nil(t, got)
	})

	t.Run("success_status_returns_nil", func(t *testing.T) {
		r := &errs.OperationResult{Status: "success", Code: "vm.created"}
		got := r.ToError()
		assert.Nil(t, got)
	})

	t.Run("skipped_status_returns_nil", func(t *testing.T) {
		r := &errs.OperationResult{Status: "skipped"}
		got := r.ToError()
		assert.Nil(t, got)
	})

	t.Run("error_status_converts_to_domain_error", func(t *testing.T) {
		r := &errs.OperationResult{
			Status:    "error",
			Code:      "vm.not_found",
			Message:   "VM not found",
			Exception: errors.New("root cause"),
		}
		got := r.ToError()
		require.NotNil(t, got)
		assert.Equal(t, errs.CodeVMNotFound, got.Code)
		assert.Equal(t, "VM not found", got.Message)
		assert.Equal(t, "vm", got.Op)
		assert.Equal(t, errs.ClassValidation, got.Class)
		assert.Equal(t, "root cause", got.Err.Error())
	})

	t.Run("failure_status_converts_to_domain_error", func(t *testing.T) {
		r := &errs.OperationResult{
			Status:  "failure",
			Code:    string(errs.CodeVMCreateFailed),
			Message: "creation failed",
		}
		got := r.ToError()
		require.NotNil(t, got)
		assert.Equal(t, errs.CodeVMCreateFailed, got.Code)
		assert.Equal(t, "vm", got.Op)
		assert.Equal(t, errs.ClassInternal, got.Class)
	})
}

// ─── OperationResult.MarshalJSON ───────────────────────────────────────────
// Rationale: JSON serialization is used by the API layer to return results
// to callers. Missing fields or wrong null/empty handling breaks clients.

func TestOperationResult_MarshalJSON(t *testing.T) {
	t.Run("exception_serialized_as_string_when_non_nil", func(t *testing.T) {
		r := &errs.OperationResult{
			Status:    "error",
			Code:      "vm.not_found",
			Message:   "not found",
			Exception: errors.New("VM missing"),
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Equal(t, "VM missing", result["exception"])
	})

	t.Run("exception_serialized_as_null_when_nil", func(t *testing.T) {
		r := &errs.OperationResult{
			Status:  "success",
			Code:    "vm.created",
			Message: "created",
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Nil(t, result["exception"])
	})

	t.Run("metadata_defaults_to_empty_dict_when_nil", func(t *testing.T) {
		r := &errs.OperationResult{
			Status:  "success",
			Code:    "vm.created",
			Message: "ok",
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))
		want := map[string]any{}
		if diff := cmp.Diff(want, result["metadata"]); diff != "" {
			t.Errorf("metadata mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("warnings_defaults_to_empty_list_when_nil", func(t *testing.T) {
		r := &errs.OperationResult{
			Status:  "success",
			Code:    "vm.created",
			Message: "ok",
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))
		want := []any{}
		if diff := cmp.Diff(want, result["warnings"]); diff != "" {
			t.Errorf("warnings mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("all_struct_fields_present", func(t *testing.T) {
		r := &errs.OperationResult{
			Status:  "success",
			Code:    "vm.created",
			Message: "created",
			Item:    "my-vm",
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)

		var result map[string]any
		require.NoError(t, json.Unmarshal(data, &result))
		assert.Equal(t, "success", result["status"])
		assert.Equal(t, "vm.created", result["code"])
		assert.Equal(t, "created", result["message"])
		assert.Equal(t, "my-vm", result["item"])
	})
}

// ─── NeedsInteraction.Error ────────────────────────────────────────────────
// Rationale: NeedsInteraction implements the error interface for flow through
// (T, error) return patterns. If Error() returns the wrong string, error
// messages to users are misleading.

func TestNeedsInteraction_Error(t *testing.T) {
	n := &errs.NeedsInteraction{
		Code:    "sudo.required",
		Message: "sudo password required",
	}
	assert.Equal(t, "sudo password required", n.Error())
}

// ─── BatchResult.Errors ──────────────────────────────────────────────────
// Rationale: Errors() filters batch results to failed items. Missing an
// error item means the caller proceeds as if the batch fully succeeded.

func TestBatchResult_Errors(t *testing.T) {
	t.Run("returns_only_error_and_failure_items", func(t *testing.T) {
		br := &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "success", Code: "a"},
				{Status: "error", Code: "b"},
				{Status: "failure", Code: "c"},
				{Status: "skipped", Code: "d"},
				{Status: "warning", Code: "e"},
			},
		}
		got := br.Errors()
		require.Len(t, got, 2)
		assert.Equal(t, "b", got[0].Code)
		assert.Equal(t, "c", got[1].Code)
	})

	t.Run("all_success_returns_empty", func(t *testing.T) {
		br := &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "success", Code: "a"},
				{Status: "skipped", Code: "b"},
			},
		}
		got := br.Errors()
		assert.Empty(t, got)
	})
}

// ─── BatchResult.HasErrors ────────────────────────────────────────────────
// Rationale: HasErrors is a quick check before error handling. Wrong result
// causes the caller to skip error handling or re-process a failed batch.

func TestBatchResult_HasErrors(t *testing.T) {
	t.Run("all_success_returns_false", func(t *testing.T) {
		br := &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "success", Code: "a"},
			},
		}
		assert.False(t, br.HasErrors())
	})

	t.Run("error_item_returns_true", func(t *testing.T) {
		br := &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "success", Code: "a"},
				{Status: "error", Code: "b"},
			},
		}
		assert.True(t, br.HasErrors())
	})

	t.Run("failure_item_returns_true", func(t *testing.T) {
		br := &errs.BatchResult{
			Items: []errs.OperationResult{
				{Status: "failure", Code: "c"},
			},
		}
		assert.True(t, br.HasErrors())
	})
}
