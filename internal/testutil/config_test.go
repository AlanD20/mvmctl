package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/testutil"
)

func TestConfigRepo_CRUD(t *testing.T) {
	repo := testutil.NewConfigRepo()

	t.Run("get_nonexistent_returns_nil", func(t *testing.T) {
		got, err := repo.Get(ctx, "settings", "unknown")
		require.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("set_and_get", func(t *testing.T) {
		err := repo.Set(ctx, "defaults.vm", "vcpu_count", 4)
		require.NoError(t, err)

		got, err := repo.Get(ctx, "defaults.vm", "vcpu_count")
		require.NoError(t, err)
		assert.Equal(t, float64(4), got, "config stores values as float64")
	})

	t.Run("delete_key", func(t *testing.T) {
		deleted, err := repo.Delete(ctx, "defaults.vm", "vcpu_count")
		require.NoError(t, err)
		assert.True(t, deleted)
	})

	t.Run("delete_nonexistent_returns_false", func(t *testing.T) {
		deleted, err := repo.Delete(ctx, "nonexistent", "key")
		require.NoError(t, err)
		assert.False(t, deleted)
	})

	t.Run("delete_category", func(t *testing.T) {
		_ = repo.Set(ctx, "test.cat", "a", 1)
		_ = repo.Set(ctx, "test.cat", "b", 2)

		count, err := repo.DeleteByCategory(ctx, "test.cat")
		require.NoError(t, err)
		assert.Equal(t, 2, count)

		cat := "test.cat"
		entries, _ := repo.ListByCategory(ctx, &cat)
		assert.Empty(t, entries)
	})

	t.Run("count", func(t *testing.T) {
		n, err := repo.Count(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, n, 0)
	})
}
