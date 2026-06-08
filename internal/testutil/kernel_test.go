package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func newKernel(id, version string) *model.KernelItem {
	return &model.KernelItem{ID: id, Version: version, IsPresent: true}
}

func seedKernel(t *testing.T, repo *testutil.KernelRepo, k *model.KernelItem) {
	t.Helper()
	require.NoError(t, repo.Upsert(ctx, k))
}

func TestKernelRepo_Get(t *testing.T) {
	repo := testutil.NewKernelRepo()
	seedKernel(t, repo, newKernel("k-1", "6.1"))

	t.Run("found", func(t *testing.T) {
		got, err := repo.Get(ctx, "k-1")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "6.1", got.Version)
	})

	t.Run("not_found", func(t *testing.T) {
		got, err := repo.Get(ctx, "nonexistent")
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestKernelRepo_FindByPrefix(t *testing.T) {
	repo := testutil.NewKernelRepo()
	seedKernel(t, repo, newKernel("abc-1", "6.1"))
	seedKernel(t, repo, newKernel("xyz-9", "5.15"))

	got, err := repo.FindByPrefix(ctx, "abc")
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestKernelRepo_SetDefault(t *testing.T) {
	repo := testutil.NewKernelRepo()
	seedKernel(t, repo, newKernel("k-1", "6.1"))
	seedKernel(t, repo, newKernel("k-2", "5.15"))

	require.NoError(t, repo.SetDefault(ctx, "k-1"))
	got, _ := repo.GetDefault(ctx)
	require.NotNil(t, got)
	assert.Equal(t, "k-1", got.ID)

	require.NoError(t, repo.SetDefault(ctx, "k-2"))
	got2, _ := repo.GetDefault(ctx)
	assert.Equal(t, "k-2", got2.ID)

	n1, _ := repo.Get(ctx, "k-1")
	assert.False(t, n1.IsDefault)
}

func TestKernelRepo_ListAll(t *testing.T) {
	repo := testutil.NewKernelRepo()
	seedKernel(t, repo, newKernel("k-1", "6.1"))

	got, err := repo.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}

func TestKernelRepo_Count(t *testing.T) {
	repo := testutil.NewKernelRepo()
	require.Equal(t, 0, mustKernelCount(repo))
	seedKernel(t, repo, newKernel("k-1", "6.1"))
	require.Equal(t, 1, mustKernelCount(repo))
}

func mustKernelCount(repo *testutil.KernelRepo) int {
	n, _ := repo.Count(ctx)
	return n
}
