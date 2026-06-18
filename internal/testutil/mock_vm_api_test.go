package testutil_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/errs"
)

// --- MockVMAPI -----------------------------------------------------------------
// Rationale: MockVMAPI implements api.VMAPI for testing. These tests verify the
// mock's two behaviors: (1) when no function field is set, the method returns
// nil/zero; (2) when a function field is set, the method routes to it and
// returns the function's result.

func TestMockVMAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockVMAPI{}

	t.Run("VMCreate_returns_nil_nil", func(t *testing.T) {
		result, err := m.VMCreate(ctx, inputs.VMCreateInput{}, nil)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("VMRemove_returns_nil", func(t *testing.T) {
		result := m.VMRemove(ctx, inputs.VMInput{})
		assert.Nil(t, result)
	})

	t.Run("VMStart_returns_nil", func(t *testing.T) {
		result := m.VMStart(ctx, inputs.VMInput{})
		assert.Nil(t, result)
	})

	t.Run("VMGet_returns_nil_nil", func(t *testing.T) {
		result, err := m.VMGet(ctx, inputs.VMInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMockVMAPI_CustomFunc(t *testing.T) {
	t.Run("VMCreate_custom_func", func(t *testing.T) {
		expected := []*model.VM{{ID: "vm-1", Name: "test"}}
		m := &testutil.MockVMAPI{
			VMCreateFunc: func(ctx context.Context, input inputs.VMCreateInput, onProgress event.OnProgressCallback) ([]*model.VM, error) {
				return expected, nil
			},
		}
		result, err := m.VMCreate(ctx, inputs.VMCreateInput{}, nil)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("VMCreate() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("VMRemove_custom_func", func(t *testing.T) {
		expected := &errs.BatchResult{Items: []errs.OperationResult{
			{Status: "success", Code: "vm.removed", Message: "VM removed"},
		}}
		m := &testutil.MockVMAPI{
			VMRemoveFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				return expected
			},
		}
		result := m.VMRemove(ctx, inputs.VMInput{Identifiers: []string{"vm-1"}})
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("VMRemove() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("VMStart_custom_func", func(t *testing.T) {
		expected := &errs.BatchResult{}
		m := &testutil.MockVMAPI{
			VMStartFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				return expected
			},
		}
		result := m.VMStart(ctx, inputs.VMInput{Identifiers: []string{"vm-1"}})
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("VMStart() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("VMStop_custom_func", func(t *testing.T) {
		expected := &errs.BatchResult{}
		m := &testutil.MockVMAPI{
			VMStopFunc: func(ctx context.Context, input inputs.VMInput) *errs.BatchResult {
				return expected
			},
		}
		result := m.VMStop(ctx, inputs.VMInput{Identifiers: []string{"vm-1"}})
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("VMStop() mismatch (-want +got):\n%s", diff)
		}
	})
}
