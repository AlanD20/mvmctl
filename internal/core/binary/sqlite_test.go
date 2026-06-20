package binary_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/binary"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func TestBinaryL1_ListAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.NewInMemoryDB(t)
	repo := binary.NewRepository(db)

	now := time.Now().Format(time.RFC3339)

	// Seed two binaries with distinct types.
	b1 := &model.BinaryItem{
		ID:          "bin-1111111111111111111111111111111111111111111111111111111111111111",
		Type:        "firecracker",
		Version:     "1.3.0",
		FullVersion: "firecracker-v1.3.0",
		Path:        "/var/lib/mvm/binaries/firecracker-v1.3.0",
		IsPresent:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	b2 := &model.BinaryItem{
		ID:          "bin-2222222222222222222222222222222222222222222222222222222222222222",
		Type:        "jailer",
		Version:     "1.3.0",
		FullVersion: "jailer-v1.3.0",
		Path:        "/var/lib/mvm/binaries/jailer-v1.3.0",
		IsPresent:   true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	require.NoError(t, repo.Upsert(ctx, b1))
	require.NoError(t, repo.Upsert(ctx, b2))

	// List all and verify count and field values.
	items, err := repo.ListAll(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2)

	// ORDER BY created_at — same timestamp so insertion order applies.
	assert.Equal(t, "firecracker", items[0].Type)
	assert.Equal(t, "1.3.0", items[0].Version)
	assert.Equal(t, "firecracker-v1.3.0", items[0].FullVersion)
	assert.Equal(t, "/var/lib/mvm/binaries/firecracker-v1.3.0", items[0].Path)

	assert.Equal(t, "jailer", items[1].Type)
	assert.Equal(t, "1.3.0", items[1].Version)
	assert.Equal(t, "jailer-v1.3.0", items[1].FullVersion)
	assert.Equal(t, "/var/lib/mvm/binaries/jailer-v1.3.0", items[1].Path)
}
