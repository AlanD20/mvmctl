package testutil_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/testutil"
	"mvmctl/pkg/api/inputs"
)

// ─── MockLogAPI ───────────────────────────────────────────────────────────────
// Rationale: MockLogAPI satisfies api.LogAPI for testing upper layers. Tests
// verify default zero returns and custom function routing for LogStream.

func TestMockLogAPI_DefaultReturnsNil(t *testing.T) {
	m := &testutil.MockLogAPI{}

	t.Run("LogStream_returns_nil", func(t *testing.T) {
		err := m.LogStream(ctx, inputs.LogInput{}, nil)
		assert.NoError(t, err)
	})

	t.Run("LogStreamChannel_returns_nil_nil_nil", func(t *testing.T) {
		lineCh, errCh, err := m.LogStreamChannel(ctx, inputs.LogInput{})
		assert.NoError(t, err)
		assert.Nil(t, lineCh)
		assert.Nil(t, errCh)
	})
}

func TestMockLogAPI_CustomFunc(t *testing.T) {
	t.Run("LogStream_custom_returns_error", func(t *testing.T) {
		m := &testutil.MockLogAPI{
			LogStreamFunc: func(ctx context.Context, input inputs.LogInput, callback func(string) error) error {
				return errors.New("log stream failed")
			},
		}
		err := m.LogStream(ctx, inputs.LogInput{}, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "log stream failed")
		return
	})
}
