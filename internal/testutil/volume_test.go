package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func newVolume(id, name string) *model.VolumeItem {
	return &model.VolumeItem{ID: id, Name: name, Status: model.VolumeStatusAvailable}
}

func seedVol(t *testing.T, repo *testutil.VolumeRepo, v *model.VolumeItem) {
	t.Helper()
	require.NoError(t, repo.Upsert(ctx, v))
}

func TestVolumeRepo_Get(t *testing.T) {
	repo := testutil.NewVolumeRepo()
	seedVol(t, repo, newVolume("v-1", "data"))

	t.Run("found", func(t *testing.T) {
		got, err := repo.Get(ctx, "v-1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "data", got.Name)
	})

	t.Run("not_found", func(t *testing.T) {
		got, err := repo.Get(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestVolumeRepo_GetByName(t *testing.T) {
	repo := testutil.NewVolumeRepo()
	seedVol(t, repo, newVolume("v-1", "work"))

	got, err := repo.GetByName(ctx, "work")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "v-1", got.ID)
}

func TestVolumeRepo_ListAll(t *testing.T) {
	repo := testutil.NewVolumeRepo()
	seedVol(t, repo, newVolume("v-1", "a"))
	seedVol(t, repo, newVolume("v-2", "b"))

	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestVolumeRepo_FindByIDs(t *testing.T) {
	repo := testutil.NewVolumeRepo()
	seedVol(t, repo, newVolume("v-1", "a"))
	seedVol(t, repo, newVolume("v-2", "b"))

	got, err := repo.FindByIDs(ctx, []string{"v-1", "v-3"})
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "v-1", got[0].ID)
}

func TestVolumeRepo_Count(t *testing.T) {
	repo := testutil.NewVolumeRepo()
	assert.Equal(t, 0, mustVolCount(repo))
	seedVol(t, repo, newVolume("v-1", "a"))
	assert.Equal(t, 1, mustVolCount(repo))
}

func mustVolCount(repo *testutil.VolumeRepo) int {
	n, _ := repo.Count(ctx)
	return n
}
