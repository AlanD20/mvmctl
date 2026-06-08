package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func newKey(id, name string) *model.SSHKeyItem {
	return &model.SSHKeyItem{ID: id, Name: name, Fingerprint: "SHA256:" + id}
}

func seedKey(t *testing.T, repo *testutil.KeyRepo, k *model.SSHKeyItem) {
	t.Helper()
	require.NoError(t, repo.Upsert(ctx, k))
}

func TestKeyRepo_GetByName(t *testing.T) {
	repo := testutil.NewKeyRepo()
	seedKey(t, repo, newKey("k-1", "my-key"))

	t.Run("found", func(t *testing.T) {
		got, err := repo.GetByName(ctx, "my-key")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "k-1", got.ID)
	})

	t.Run("not_found", func(t *testing.T) {
		got, err := repo.GetByName(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestKeyRepo_FindByPrefix(t *testing.T) {
	repo := testutil.NewKeyRepo()
	seedKey(t, repo, newKey("abc-1", "alpha"))
	seedKey(t, repo, newKey("xyz-9", "omega"))

	got, err := repo.FindByPrefix(ctx, "abc")
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestKeyRepo_List(t *testing.T) {
	repo := testutil.NewKeyRepo()
	seedKey(t, repo, newKey("k-1", "a"))
	seedKey(t, repo, newKey("k-2", "b"))

	got, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestKeyRepo_Count(t *testing.T) {
	repo := testutil.NewKeyRepo()
	assert.Equal(t, 0, mustKeyCount(repo))
	seedKey(t, repo, newKey("k-1", "a"))
	assert.Equal(t, 1, mustKeyCount(repo))
}

func mustKeyCount(repo *testutil.KeyRepo) int {
	n, _ := repo.Count(ctx)
	return n
}

func TestKeyRepo_SetDefault(t *testing.T) {
	repo := testutil.NewKeyRepo()
	seedKey(t, repo, newKey("k-1", "primary"))
	seedKey(t, repo, newKey("k-2", "secondary"))

	require.NoError(t, repo.SetDefault(ctx, "k-1"))
	defaults, err := repo.GetDefaults(ctx)
	require.NoError(t, err)
	assert.Len(t, defaults, 1)
	assert.Equal(t, "k-1", defaults[0].ID)
}

func TestKeyRepo_ClearDefaults(t *testing.T) {
	repo := testutil.NewKeyRepo()
	seedKey(t, repo, newKey("k-1", "a"))
	require.NoError(t, repo.SetDefault(ctx, "k-1"))

	require.NoError(t, repo.ClearDefaults(ctx))
	got, _ := repo.GetDefaults(ctx)
	assert.Empty(t, got)
}
