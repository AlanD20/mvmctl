package vm_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/go-cmp/cmp"
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
		IPv4: "10.0.0.2", MAC: "02:00:00:00:00:02", NetworkPlacement: model.VMNetworkPlacementManaged,
		NetworkID: "network-id", TapDevice: "tap-test",
		ImageID: "image-id", KernelID: "kernel-id", BinaryID: "firecracker-id", JailerBinaryID: "jailer-id",
		APISocketPath: "/tmp/api.socket", ConfigPath: "/tmp/config.json", CloudInitMode: "off",
		VCPUCount: 2, MemSizeMiB: 512, DiskSizeMiB: 1024, RootfsPath: "/tmp/rootfs", RootfsSuffix: ".ext4",
		CgroupLimits: model.NewVMCgroupLimits(2, 512, model.VMCgroupPolicy{
			VMMHeadroomMiB: 128, CPUWeight: 100, PIDsMax: 256, SwapMaxBytes: 0,
		}),
		CreatedAt: "2026-08-09T00:00:00Z", UpdatedAt: "2026-08-09T00:00:00Z",
	}

	require.NoError(t, repo.Upsert(ctx, item))
	got, err := repo.Get(ctx, item.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "firecracker-id", got.BinaryID)
	assert.Equal(t, "jailer-id", got.JailerBinaryID)
	assert.Equal(t, item.CgroupLimits, got.CgroupLimits)

	item.CgroupLimits = model.NewVMCgroupLimits(4, 1024, model.VMCgroupPolicy{
		VMMHeadroomMiB: 64, CPUWeight: 500, PIDsMax: 512, SwapMaxBytes: 4096,
	})
	require.NoError(t, repo.Upsert(ctx, item))
	updated, err := repo.Get(ctx, item.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	assert.Equal(t, item.CgroupLimits, updated.CgroupLimits)

	byJailer, err := repo.FindByBinaryID(ctx, "jailer-id")
	require.NoError(t, err)
	require.Len(t, byJailer, 1)
	assert.Equal(t, item.ID, byJailer[0].ID)
	byBoth, err := repo.GetByBinaryIDs(ctx, []string{"firecracker-id", "jailer-id"})
	require.NoError(t, err)
	require.Len(t, byBoth, 1)
	assert.Equal(t, item.ID, byBoth[0].ID)
}

// A networkless VM has a genuine absent FK, while a managed VM still requires a real network row.
func TestRepositoryPersistsClosedVMNetworkPlacement(t *testing.T) {
	ctx := t.Context()
	database := testutil.NewInMemoryDB(t)
	_, err := database.ExecContext(ctx, `
		INSERT INTO images (id,type,version,name,arch,path,fs_type,original_size,minimum_rootfs_size_mib,pulled_at)
		VALUES ('img','alpine','1','img','x86_64','/img','ext4',1,1,CURRENT_TIMESTAMP);
		INSERT INTO kernels (id,name,base_name,version,arch,type,path)
		VALUES ('kernel','kernel','kernel','1','x86_64','firecracker','/kernel');
		INSERT INTO binaries (id,type,architecture,version,full_version,path)
		VALUES ('fc','firecracker','x86_64','1','1','/fc');
		INSERT INTO networks (id,name,subnet,bridge,ipv4_gateway)
		VALUES ('net','net','10.0.0.0/24','mvm-net','10.0.0.1');
	`)
	require.NoError(t, err)
	repo := vm.NewRepository(database)
	managed := &model.VMItem{
		ID: "managed", Name: "managed", Status: model.VMStatusStopped,
		NetworkPlacement: model.VMNetworkPlacementManaged, NetworkID: "net",
		IPv4: "10.0.0.2", MAC: "02:00:00:00:00:02", TapDevice: "tap-managed",
		ImageID: "img", KernelID: "kernel", BinaryID: "fc", CloudInitMode: "off",
		APISocketPath: "/api", ConfigPath: "/config", RootfsPath: "/rootfs", RootfsSuffix: ".ext4",
		VCPUCount: 1, MemSizeMiB: 128, DiskSizeMiB: 64,
	}
	require.NoError(t, repo.Upsert(ctx, managed))

	networkless := *managed
	networkless.ID, networkless.Name = "networkless", "networkless"
	networkless.NetworkPlacement = model.VMNetworkPlacementNetworkless
	networkless.NetworkID, networkless.IPv4, networkless.MAC, networkless.TapDevice = "", "", "", ""
	require.NoError(t, repo.Upsert(ctx, &networkless))
	got, err := repo.Get(ctx, networkless.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	wantPlacement := struct {
		Placement model.VMNetworkPlacement
		NetworkID model.VMNetworkID
		IPv4      string
		MAC       string
		TAP       string
	}{model.VMNetworkPlacementNetworkless, "", "", "", ""}
	gotPlacement := struct {
		Placement model.VMNetworkPlacement
		NetworkID model.VMNetworkID
		IPv4      string
		MAC       string
		TAP       string
	}{got.NetworkPlacement, got.NetworkID, got.IPv4, got.MAC, got.TapDevice}
	if diff := cmp.Diff(wantPlacement, gotPlacement); diff != "" {
		t.Errorf("networkless placement mismatch (-want +got):\n%s", diff)
	}
	var storedNetworkID sql.NullString
	require.NoError(t, database.GetContext(ctx, &storedNetworkID,
		"SELECT network_id FROM vm_instances WHERE id = ?", networkless.ID))
	assert.False(t, storedNetworkID.Valid, "networkless placement must store SQL NULL, not a fabricated network ID")

	invalid := map[string]struct {
		base    *model.VMItem
		change  func(*model.VMItem)
		wantErr string
	}{
		"networkless_with_network": {&networkless, func(item *model.VMItem) { item.NetworkID = "net" }, "CHECK"},
		"networkless_with_ipv4":    {&networkless, func(item *model.VMItem) { item.IPv4 = "10.0.0.3" }, "CHECK"},
		"networkless_with_mac": {
			&networkless,
			func(item *model.VMItem) { item.MAC = "02:00:00:00:00:03" },
			"CHECK",
		},
		"networkless_with_tap": {
			&networkless,
			func(item *model.VMItem) { item.TapDevice = "tap-invalid" },
			"CHECK",
		},
		"networkless_with_nocloud_net": {
			&networkless,
			func(item *model.VMItem) { item.CloudInitMode = "net" },
			"CHECK",
		},
		"managed_without_network": {managed, func(item *model.VMItem) { item.NetworkID = "" }, "CHECK"},
		"managed_with_foreign_network": {managed,
			func(item *model.VMItem) { item.NetworkID = "foreign" }, "FOREIGN KEY"},
	}
	for name, tc := range invalid {
		t.Run(name, func(t *testing.T) {
			item := *tc.base
			item.ID, item.Name = name, name
			tc.change(&item)
			err := repo.Upsert(ctx, &item)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			stored, readErr := repo.Get(ctx, item.ID)
			require.NoError(t, readErr)
			assert.Nil(t, stored)
		})
	}
}
