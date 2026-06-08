package testutil_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/testutil"
)

func TestHostRepo_State(t *testing.T) {
	repo := testutil.NewHostRepo()

	t.Run("uninitialized_returns_nil", func(t *testing.T) {
		got, err := repo.GetState(ctx)
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("initialize_state", func(t *testing.T) {
		state, err := repo.InitializeState(ctx)
		require.NoError(t, err)
		require.NotNil(t, state)
		assert.NotEmpty(t, state.InitializedAt)
	})

	t.Run("set_initialized", func(t *testing.T) {
		now := time.Now().UTC().Format(time.RFC3339)
		err := repo.SetInitialized(ctx, now)
		require.NoError(t, err)

		got, _ := repo.GetState(ctx)
		require.NotNil(t, got)
		assert.Equal(t, now, got.InitializedAt)
	})
}
