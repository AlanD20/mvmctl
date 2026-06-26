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

// --- MockNetworkAPI ---
// Rationale: MockNetworkAPI implements api.NetworkAPI for testing. These tests
// verify default zero-value returns and custom function routing.

func TestMockNetworkAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockNetworkAPI{}

	t.Run("NetworkCreate_returns_nil_nil", func(t *testing.T) {
		result, err := m.NetworkCreate(ctx, inputs.NetworkCreateInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("NetworkRemove_returns_nil", func(t *testing.T) {
		err := m.NetworkRemove(ctx, inputs.NetworkInput{}, false)
		assert.NoError(t, err)
	})

	t.Run("NetworkListAll_returns_nil_nil", func(t *testing.T) {
		result, err := m.NetworkListAll(ctx)
		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("NetworkGet_returns_nil_nil", func(t *testing.T) {
		result, err := m.NetworkGet(ctx, inputs.NetworkInput{})
		assert.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMockNetworkAPI_CustomFunc(t *testing.T) {
	t.Run("NetworkCreate_custom_func", func(t *testing.T) {
		expected := &model.NetworkItem{ID: "net-1", Name: "test-net", Subnet: "10.0.0.0/24"}
		m := &testutil.MockNetworkAPI{
			NetworkCreateFunc: func(ctx context.Context, input inputs.NetworkCreateInput) (*model.NetworkItem, error) {
				return expected, nil
			},
		}
		result, err := m.NetworkCreate(ctx, inputs.NetworkCreateInput{Name: "test-net"})
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("NetworkCreate() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("NetworkRemove_custom_func", func(t *testing.T) {
		m := &testutil.MockNetworkAPI{
			NetworkRemoveFunc: func(ctx context.Context, input inputs.NetworkInput, force bool) error {
				return nil
			},
		}
		err := m.NetworkRemove(ctx, inputs.NetworkInput{Identifiers: []string{"test-net"}}, true)
		assert.NoError(t, err)
	})

	t.Run("NetworkListAll_custom_func", func(t *testing.T) {
		expected := []*model.NetworkItem{
			{ID: "net-1", Name: "alpha"},
			{ID: "net-2", Name: "beta"},
		}
		m := &testutil.MockNetworkAPI{
			NetworkListAllFunc: func(ctx context.Context) ([]*model.NetworkItem, error) {
				return expected, nil
			},
		}
		result, err := m.NetworkListAll(ctx)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("NetworkListAll() mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("NetworkCreateDefaultNetwork_custom_func", func(t *testing.T) {
		expected := &model.NetworkItem{ID: "default-net", Name: "default"}
		m := &testutil.MockNetworkAPI{
			NetworkCreateDefaultNetworkFunc: func(ctx context.Context) (*model.NetworkItem, error) {
				return expected, nil
			},
		}
		result, err := m.NetworkCreateDefaultNetwork(ctx)
		require.NoError(t, err)
		if diff := cmp.Diff(expected, result); diff != "" {
			t.Errorf("NetworkCreateDefaultNetwork() mismatch (-want +got):\n%s", diff)
		}
	})
}
