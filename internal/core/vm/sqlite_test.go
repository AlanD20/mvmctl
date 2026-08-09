package vm_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/vm"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func TestRepository_PersistsJailerBinaryID(t *testing.T) {
	ctx := context.Background()
	database := testutil.NewInMemoryDB(t)
	_, err := database.ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	require.NoError(t, err)
	repo := vm.NewRepository(database)
	item := &model.VMItem{
		ID: "0123456789abcdef0123456789abcdef", Name: "persisted-jailer", Status: model.VMStatusStopped,
		IPv4: "10.0.0.2", MAC: "02:00:00:00:00:02", NetworkID: "network-id", TapDevice: "tap-test",
		ImageID: "image-id", KernelID: "kernel-id", BinaryID: "firecracker-id", JailerBinaryID: "jailer-id",
		APISocketPath: "/tmp/api.socket", ConfigPath: "/tmp/config.json", CloudInitMode: "off",
		VCPUCount: 2, MemSizeMiB: 512, DiskSizeMiB: 1024, RootfsPath: "/tmp/rootfs", RootfsSuffix: ".ext4",
		CreatedAt: "2026-08-09T00:00:00Z", UpdatedAt: "2026-08-09T00:00:00Z",
	}

	require.NoError(t, repo.Upsert(ctx, item))
	got, err := repo.Get(ctx, item.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "firecracker-id", got.BinaryID)
	assert.Equal(t, "jailer-id", got.JailerBinaryID)

	byJailer, err := repo.FindByBinaryID(ctx, "jailer-id")
	require.NoError(t, err)
	require.Len(t, byJailer, 1)
	assert.Equal(t, item.ID, byJailer[0].ID)
	byBoth, err := repo.GetByBinaryIDs(ctx, []string{"firecracker-id", "jailer-id"})
	require.NoError(t, err)
	require.Len(t, byBoth, 1)
	assert.Equal(t, item.ID, byBoth[0].ID)
}
