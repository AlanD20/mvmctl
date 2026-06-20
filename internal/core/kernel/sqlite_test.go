package kernel_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/kernel"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func TestKernelL1_ListAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := testutil.NewInMemoryDB(t)
	repo := kernel.NewRepository(db)

	now := time.Now().Format(time.RFC3339)

	// Seed two kernels.
	k1 := &model.KernelItem{
		ID:        "kern-1111111111111111111111111111111111111111111111111111111111111111",
		Name:      "vmlinux-6.1",
		BaseName:  "vmlinux-6.1",
		Version:   "6.1",
		Arch:      "x86_64",
		Type:      "vmlinux",
		Path:      "/var/lib/mvm/kernels/vmlinux-6.1",
		IsPresent: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	k2 := &model.KernelItem{
		ID:        "kern-2222222222222222222222222222222222222222222222222222222222222222",
		Name:      "vmlinux-6.2",
		BaseName:  "vmlinux-6.2",
		Version:   "6.2",
		Arch:      "x86_64",
		Type:      "vmlinux",
		Path:      "/var/lib/mvm/kernels/vmlinux-6.2",
		IsPresent: true,
		CreatedAt: now,
		UpdatedAt: now,
	}

	require.NoError(t, repo.Upsert(ctx, k1))
	require.NoError(t, repo.Upsert(ctx, k2))

	// List all and verify count and field values.
	items, err := repo.ListAll(ctx)
	require.NoError(t, err)
	require.Len(t, items, 2)

	assert.Equal(t, "vmlinux-6.1", items[0].Name)
	assert.Equal(t, "6.1", items[0].Version)
	assert.Equal(t, "x86_64", items[0].Arch)
	assert.Equal(t, "vmlinux", items[0].Type)

	assert.Equal(t, "vmlinux-6.2", items[1].Name)
	assert.Equal(t, "6.2", items[1].Version)
	assert.Equal(t, "x86_64", items[1].Arch)
	assert.Equal(t, "vmlinux", items[1].Type)
}
