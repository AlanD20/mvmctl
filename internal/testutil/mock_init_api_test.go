package testutil_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

// --- MockInitAPI ---

func TestMockInitAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockInitAPI{}

	t.Run("InitCheckReadiness_returns_nil", func(t *testing.T) {
		result := m.InitCheckReadiness(ctx)
		assert.Nil(t, result)
	})

	t.Run("InitSetupHost_returns_nil", func(t *testing.T) {
		err := m.InitSetupHost(ctx)
		assert.NoError(t, err)
	})

	t.Run("InitRunFull_returns_nil", func(t *testing.T) {
		result := m.InitRunFull(ctx, false, false, false, false, "", "", nil, nil)
		assert.Nil(t, result)
	})
}

func TestMockInitAPI_CustomFunc(t *testing.T) {
	t.Run("InitCheckReadiness_custom_func", func(t *testing.T) {
		expected := &model.ProbeResult{
			Critical: []model.ProbeCheck{
				{Name: "kvm", Passed: true, Message: "KVM available"},
			},
		}
		m := &testutil.MockInitAPI{
			InitCheckReadinessFunc: func(ctx context.Context) *model.ProbeResult {
				return expected
			},
		}
		result := m.InitCheckReadiness(ctx)
		require.NotNil(t, result)
		assert.Len(t, result.Critical, 1)
		assert.Equal(t, "kvm", result.Critical[0].Name)
	})

	t.Run("InitSetupHost_custom_returns_error", func(t *testing.T) {
		m := &testutil.MockInitAPI{
			InitSetupHostFunc: func(ctx context.Context) error {
				return errors.New("host setup failed")
			},
		}
		err := m.InitSetupHost(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "host setup failed")
		return
	})
}
