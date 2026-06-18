package testutil_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
)

// --- MockCPAPI ---
// Rationale: MockCPAPI satisfies api.CPAPI for testing upper layers. CPCopy is
// the only method; tests verify default return and custom function routing.

func TestMockCPAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockCPAPI{}

	result, err := m.CPCopy(ctx, inputs.CPInput{}, nil)
	assert.NoError(t, err)
	assert.Nil(t, result)
}

func TestMockCPAPI_CustomFunc(t *testing.T) {
	t.Run("custom_returns_result", func(t *testing.T) {
		expected := &results.CPCopyResult{
			Bytes:   1024,
			Message: "copy complete",
		}
		m := &testutil.MockCPAPI{
			CPCopyFunc: func(ctx context.Context, input inputs.CPInput, onProgress event.OnDownloadCallback) (*results.CPCopyResult, error) {
				return expected, nil
			},
		}
		result, err := m.CPCopy(ctx, inputs.CPInput{}, nil)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, expected.Bytes, result.Bytes)
		assert.Equal(t, expected.Message, result.Message)
	})

	t.Run("custom_returns_error", func(t *testing.T) {
		m := &testutil.MockCPAPI{
			CPCopyFunc: func(ctx context.Context, input inputs.CPInput, onProgress event.OnDownloadCallback) (*results.CPCopyResult, error) {
				return nil, errors.New("copy failed")
			},
		}
		_, err := m.CPCopy(ctx, inputs.CPInput{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "copy failed")
		return
	})
}
