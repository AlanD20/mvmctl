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

func TestRepositoryReleaseSlotIncludesArchitecture(t *testing.T) {
	ctx := context.Background()
	repo := binary.NewRepository(testutil.NewInMemoryDB(t))

	x8664 := &model.BinaryItem{
		ID:           "x86-firecracker",
		Type:         "firecracker",
		Architecture: "x86_64",
		Version:      "1.16.1",
		FullVersion:  "v1.16.1",
		Path:         "/var/lib/mvmctl/binaries/x86_64/1.16.1/firecracker",
		IsPresent:    true,
	}
	aarch64 := &model.BinaryItem{
		ID:           "arm-firecracker",
		Type:         "firecracker",
		Architecture: "aarch64",
		Version:      "1.16.1",
		FullVersion:  "v1.16.1",
		Path:         "/var/lib/mvmctl/binaries/aarch64/1.16.1/firecracker",
		IsPresent:    true,
	}
	require.NoError(t, repo.Upsert(ctx, x8664))
	require.NoError(t, repo.Upsert(ctx, aarch64))

	gotX8664, err := repo.GetByTypeAndVersion(ctx, "firecracker", "x86_64", "1.16.1")
	require.NoError(t, err)
	require.NotNil(t, gotX8664)
	assert.Equal(t, "x86-firecracker", gotX8664.ID)

	gotAarch64, err := repo.GetByTypeAndVersion(ctx, "firecracker", "aarch64", "1.16.1")
	require.NoError(t, err)
	require.NotNil(t, gotAarch64)
	assert.Equal(t, "arm-firecracker", gotAarch64.ID)
}
