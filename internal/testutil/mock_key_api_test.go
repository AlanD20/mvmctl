package testutil_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
)

// --- MockKeyAPI ---
// Rationale: MockKeyAPI implements api.KeyAPI for testing. These tests verify
// default zero-value returns and custom function routing.

func TestMockKeyAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockKeyAPI{}

	t.Run("KeyCreate_returns_nil_nil", func(t *testing.T) {
		result, err := m.KeyCreate(ctx, inputs.KeyCreateInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("KeyListAll_returns_nil_nil", func(t *testing.T) {
		result, err := m.KeyListAll(ctx)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("KeyRemove_returns_nil", func(t *testing.T) {
		result := m.KeyRemove(ctx, inputs.KeyInput{}, false)
		assert.Nil(t, result)
	})

	t.Run("KeyGet_returns_nil_nil", func(t *testing.T) {
		result, err := m.KeyGet(ctx, inputs.KeyInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMockKeyAPI_CustomFunc(t *testing.T) {
	t.Run("KeyCreate_custom_func", func(t *testing.T) {
		expected := &model.SSHKeyItem{ID: "key-1", Name: "my-ssh-key", Fingerprint: "SHA256:abc123"}
		m := &testutil.MockKeyAPI{
			KeyCreateFunc: func(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error) {
				return expected, nil
			},
		}
		result, err := m.KeyCreate(ctx, inputs.KeyCreateInput{Name: "my-ssh-key"})
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("KeyCreate() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("KeyListAll_custom_func", func(t *testing.T) {
		expected := []*model.SSHKeyItem{
			{ID: "key-1", Name: "alpha", Fingerprint: "SHA256:aaa"},
			{ID: "key-2", Name: "beta", Fingerprint: "SHA256:bbb"},
		}
		m := &testutil.MockKeyAPI{
			KeyListAllFunc: func(ctx context.Context) ([]*model.SSHKeyItem, error) {
				return expected, nil
			},
		}
		result, err := m.KeyListAll(ctx)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("KeyListAll() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("KeyImport_custom_func", func(t *testing.T) {
		expected := &model.SSHKeyItem{ID: "key-3", Name: "imported-key"}
		m := &testutil.MockKeyAPI{
			KeyImportFunc: func(ctx context.Context, input inputs.KeyImportInput) (*model.SSHKeyItem, error) {
				return expected, nil
			},
		}
		result, err := m.KeyImport(ctx, inputs.KeyImportInput{})
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("KeyImport() mismatch (-want +got):\n%s", diff)
		}
	})
}
