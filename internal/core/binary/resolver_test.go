package binary_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func TestResolverScopesEveryLookupToArchitecture(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewBinaryRepo()
	items := []*model.BinaryItem{
		{ID: "shared-prefix-x86", Type: "firecracker", Architecture: "x86_64", Version: "1.16.0", IsPresent: true},
		{ID: "shared-prefix-arm", Type: "firecracker", Architecture: "aarch64", Version: "1.16.1", IsPresent: true},
		{ID: "x86-latest", Type: "firecracker", Architecture: "x86_64", Version: "1.16.1", IsPresent: true},
	}
	for _, item := range items {
		require.NoError(t, repo.Upsert(ctx, item))
	}
	require.NoError(t, repo.SetDefault(ctx, "firecracker", "x86_64", "shared-prefix-x86"))
	require.NoError(t, repo.SetDefault(ctx, "firecracker", "aarch64", "shared-prefix-arm"))

	resolver := binary.NewResolver(repo, "x86_64")
	byID, err := resolver.ByID(ctx, "shared-prefix")
	require.NoError(t, err)
	assert.Equal(t, "shared-prefix-x86", byID.ID)
	byVersion, err := resolver.ByTypeVersion(ctx, "firecracker", "1.16.1")
	require.NoError(t, err)
	assert.Equal(t, "x86-latest", byVersion.ID)
	latest, err := resolver.ByTypeLatest(ctx, "firecracker")
	require.NoError(t, err)
	assert.Equal(t, "x86-latest", latest.ID)
	defaultItem, err := resolver.GetDefault(ctx, "firecracker")
	require.NoError(t, err)
	require.NotNil(t, defaultItem)
	assert.Equal(t, "shared-prefix-x86", defaultItem.ID)
}
