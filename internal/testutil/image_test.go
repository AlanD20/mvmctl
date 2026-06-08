package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func newImage(id, name, imgType string) *model.ImageItem {
	return &model.ImageItem{ID: id, Name: name, Type: imgType, IsPresent: true}
}

func seedImg(t *testing.T, repo *testutil.ImageRepo, img *model.ImageItem) {
	t.Helper()
	require.NoError(t, repo.Upsert(ctx, img))
}

func TestImageRepo_Get(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newImage("img-1", "alpine", "alpine"))

	t.Run("found", func(t *testing.T) {
		got, err := repo.Get(ctx, "img-1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "alpine", got.Name)
	})

	t.Run("not_found", func(t *testing.T) {
		got, err := repo.Get(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestImageRepo_FindByPrefix(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newImage("abc-1", "a", "ubuntu"))
	seedImg(t, repo, newImage("abc-2", "b", "alpine"))
	seedImg(t, repo, newImage("xyz-9", "c", "debian"))

	got, err := repo.FindByPrefix(ctx, "abc")
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

func TestImageRepo_ListAll(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newImage("img-1", "a", "alpine"))

	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestImageRepo_SetDefault(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newImage("img-1", "first", "alpine"))
	seedImg(t, repo, newImage("img-2", "second", "ubuntu"))

	require.NoError(t, repo.SetDefault(ctx, "img-1"))

	got, _ := repo.GetDefault(ctx)
	require.NotNil(t, got)
	assert.Equal(t, "img-1", got.ID)

	require.NoError(t, repo.SetDefault(ctx, "img-2"))
	got2, _ := repo.GetDefault(ctx)
	assert.Equal(t, "img-2", got2.ID)

	n1, _ := repo.Get(ctx, "img-1")
	assert.False(t, n1.IsDefault)
}

func TestImageRepo_SoftDelete(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newImage("img-1", "del", "alpine"))
	require.NoError(t, repo.SoftDelete(ctx, "img-1"))

	// Get still returns soft-deleted items (matching Python's behavior)
	got, _ := repo.Get(ctx, "img-1")
	require.NotNil(t, got)
	assert.False(t, got.IsPresent)
	assert.NotNil(t, got.DeletedAt)

	// But ListAll filters them out
	list, _ := repo.ListAll(ctx)
	assert.Empty(t, list)
}

func TestImageRepo_EmptyState(t *testing.T) {
	repo := testutil.NewImageRepo()
	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}
