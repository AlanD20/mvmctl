package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func newBinary(id, typ, version string) *model.BinaryItem {
	return &model.BinaryItem{ID: id, Type: typ, Version: version, IsPresent: true}
}

func seedBin(t *testing.T, repo *testutil.BinaryRepo, b *model.BinaryItem) {
	t.Helper()
	require.NoError(t, repo.Upsert(ctx, b))
}

func TestBinaryRepo_Get(t *testing.T) {
	repo := testutil.NewBinaryRepo()
	seedBin(t, repo, newBinary("b-1", "firecracker", "1.15.0"))

	t.Run("found", func(t *testing.T) {
		got, err := repo.Get(ctx, "b-1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "firecracker", got.Type)
	})

	t.Run("not_found", func(t *testing.T) {
		got, err := repo.Get(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestBinaryRepo_FindByPrefix(t *testing.T) {
	repo := testutil.NewBinaryRepo()
	seedBin(t, repo, newBinary("abc-1", "firecracker", "1.15.0"))
	seedBin(t, repo, newBinary("xyz-9", "jailer", "1.15.0"))

	got, err := repo.FindByPrefix(ctx, "abc")
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestBinaryRepo_SetDefault(t *testing.T) {
	repo := testutil.NewBinaryRepo()
	seedBin(t, repo, newBinary("b-1", "firecracker", "1.15.0"))
	seedBin(t, repo, newBinary("b-2", "firecracker", "1.14.0"))

	require.NoError(t, repo.SetDefault(ctx, "firecracker", "b-1"))
	found, _ := repo.GetDefault(ctx, "firecracker")
	require.NotNil(t, found)
	assert.Equal(t, "b-1", found.ID)

}

func TestBinaryRepo_ListAll(t *testing.T) {
	repo := testutil.NewBinaryRepo()
	seedBin(t, repo, newBinary("b-1", "firecracker", "1.15.0"))

	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestBinaryRepo_ListByType(t *testing.T) {
	repo := testutil.NewBinaryRepo()
	seedBin(t, repo, newBinary("b-1", "firecracker", "1.15.0"))
	seedBin(t, repo, newBinary("b-2", "firecracker", "1.14.0"))
	seedBin(t, repo, newBinary("b-3", "jailer", "1.15.0"))

	fc, err := repo.ListByType(ctx, "firecracker")
	require.NoError(t, err)
	assert.Len(t, fc, 2)

	j, err := repo.ListByType(ctx, "jailer")
	require.NoError(t, err)
	assert.Len(t, j, 1)
}
