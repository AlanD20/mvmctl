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

	// Get still returns soft-deleted items
	got, _ := repo.Get(ctx, "img-1")
	require.NotNil(t, got)
	assert.False(t, got.IsPresent)
	assert.NotNil(t, got.DeletedAt)

	// ListAll includes soft-deleted items
	list, _ := repo.ListAll(ctx)
	assert.Len(t, list, 1)
}

func TestImageRepo_EmptyState(t *testing.T) {
	repo := testutil.NewImageRepo()
	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func newVersionedImage(id, name, imgType, version string) *model.ImageItem {
	return &model.ImageItem{ID: id, Name: name, Type: imgType, Version: version, IsPresent: true}
}

func TestImageRepo_GetByVersionAndType_Match(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newVersionedImage("img-v1", "k8s-node", "k8s-node", "v1"))
	seedImg(t, repo, newVersionedImage("img-v2", "k8s-node", "k8s-node", "v2"))

	got, err := repo.GetByVersionAndType(ctx, "v1", "k8s-node")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "img-v1", got.ID)

	got, err = repo.GetByVersionAndType(ctx, "v2", "k8s-node")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "img-v2", got.ID)
}

func TestImageRepo_GetByVersionAndType_WrongVersion(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newVersionedImage("img-v1", "k8s-node", "k8s-node", "v1"))

	// Same type, different version → not found
	got, err := repo.GetByVersionAndType(ctx, "v2", "k8s-node")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestImageRepo_GetByVersionAndType_WrongType(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newVersionedImage("img-v1", "k8s-node", "k8s-node", "v1"))

	// Same version, different type → not found
	got, err := repo.GetByVersionAndType(ctx, "v1", "other-type")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestImageRepo_GetByVersionAndType_SoftDeleted(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newVersionedImage("img-v1", "k8s-node", "k8s-node", "v1"))
	require.NoError(t, repo.SoftDelete(ctx, "img-v1"))

	// Soft-deleted images should not be returned
	got, err := repo.GetByVersionAndType(ctx, "v1", "k8s-node")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestImageRepo_GetByType_VersionIsolation(t *testing.T) {
	repo := testutil.NewImageRepo()
	seedImg(t, repo, newVersionedImage("img-v1", "k8s-node", "k8s-node", "v1"))
	seedImg(t, repo, newVersionedImage("img-v2", "k8s-node", "k8s-node", "v2"))

	// GetByType returns only one image per type (newest first) —
	// both versions share the same type so GetByType returns one of them.
	got, err := repo.GetByType(ctx, "k8s-node")
	require.NoError(t, err)
	require.NotNil(t, got)
	// Either v1 or v2 is fine — the point is that one of them is returned
	// and the other still exists separately.
	assert.Contains(t, []string{"img-v1", "img-v2"}, got.ID)

	// Verify both still exist independently via GetByVersionAndType
	v1, _ := repo.GetByVersionAndType(ctx, "v1", "k8s-node")
	v2, _ := repo.GetByVersionAndType(ctx, "v2", "k8s-node")
	require.NotNil(t, v1, "v1 should still exist")
	require.NotNil(t, v2, "v2 should still exist")
	assert.NotEqual(t, v1.ID, v2.ID, "v1 and v2 must be distinct records")
}
