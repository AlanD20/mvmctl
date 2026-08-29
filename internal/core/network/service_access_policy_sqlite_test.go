package network_test

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
)

func seedPolicyResources(
	t *testing.T,
) (*model.ServiceAccessPolicy, *model.ServiceAccessPolicy, network.ServiceAccessPolicyRepository, *sqlx.DB) {
	t.Helper()
	database := testutil.NewInMemoryDB(t)
	_, err := database.Exec(`
		INSERT INTO networks (id,name,subnet,bridge,ipv4_gateway,bridge_active,nat_enabled,is_default,is_present,created_at,updated_at)
		VALUES ('net-a','a','10.1.0.0/24','mvm-a','10.1.0.1',1,1,0,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
		       ('net-b','b','10.2.0.0/24','mvm-b','10.2.0.1',1,1,0,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
		INSERT INTO images (id,type,version,name,arch,path,fs_type,original_size,minimum_rootfs_size_mib,pulled_at,is_present)
		VALUES ('img','alpine','1','img','x86_64','/img','ext4',1,1,CURRENT_TIMESTAMP,1);
		INSERT INTO kernels (id,name,base_name,version,arch,type,path,is_present)
		VALUES ('kernel','kernel','kernel','1','x86_64','firecracker','/kernel',1);
		INSERT INTO binaries (id,type,version,full_version,path,is_present)
		VALUES ('fc','firecracker','1','1','/fc',1), ('jailer','jailer','1','1','/jailer',1);
		INSERT INTO vm_instances (
			id,name,status,pid,ipv4,mac,network_id,tap_device,image_id,kernel_id,binary_id,jailer_binary_id,
			api_socket_path,config_path,cloud_init_mode,vcpu_count,mem_size_mib,disk_size_mib,rootfs_path,
			rootfs_suffix,pci_enabled,nested_virt,remote_exec,lsm_flags,enable_logging,enable_metrics,
			enable_console,boot_args)
		VALUES ('vm-a','vm-a','stopped',0,'10.2.0.2','02:00:00:00:00:01','net-b','tap-a','img','kernel','fc',
			'jailer','/api','/config','inject',1,128,64,'/rootfs','',0,0,0,'',0,0,0,'');
	`)
	require.NoError(t, err)
	repo := network.NewServiceAccessPolicyRepository(database)
	p1 := &model.ServiceAccessPolicy{ID: "abc-111", SourceNetworkID: "net-a", DestinationVMID: "vm-a",
		Protocol: model.ServiceAccessPolicyProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443,
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	p2 := &model.ServiceAccessPolicy{ID: "abc-222", SourceNetworkID: "net-a", DestinationVMID: "vm-a",
		Protocol: model.ServiceAccessPolicyProtocolUDP, DestinationPortStart: 5000, DestinationPortEnd: 5005,
		CreatedAt: "2026-01-02T00:00:00Z", UpdatedAt: "2026-01-02T00:00:00Z"}
	return p1, p2, repo, database
}

func TestServiceAccessPolicySQLiteCRUD(t *testing.T) {
	ctx := context.Background()
	p1, p2, repo, _ := seedPolicyResources(t)
	require.NoError(t, repo.Create(ctx, p2))
	require.NoError(t, repo.Create(ctx, p1))

	got, err := repo.Get(ctx, p1.ID)
	require.NoError(t, err)
	assert.Equal(t, p1, got)
	missing, err := repo.Get(ctx, "missing")
	require.NoError(t, err)
	assert.Nil(t, missing)
	identity, err := repo.GetByIdentity(ctx, p2)
	require.NoError(t, err)
	assert.Equal(t, p2.ID, identity.ID)

	matches, err := repo.FindByPrefix(ctx, "abc-")
	require.NoError(t, err)
	assert.Equal(t, []string{p1.ID, p2.ID}, []string{matches[0].ID, matches[1].ID})
	listed, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{p1.ID, p2.ID}, []string{listed[0].ID, listed[1].ID})

	require.Error(t, repo.Create(ctx, p1), "primary-key and typed identity duplicates must be rejected")
	duplicateIdentity := *p1
	duplicateIdentity.ID = "different-id"
	require.Error(t, repo.Create(ctx, &duplicateIdentity), "unique policy identity must be enforced")

	require.NoError(t, repo.Delete(ctx, p1.ID))
	got, err = repo.Get(ctx, p1.ID)
	require.NoError(t, err)
	assert.Nil(t, got)
	require.NoError(t, repo.DeleteMany(ctx, nil))
	require.NoError(t, repo.DeleteMany(ctx, []string{p2.ID}))
	listed, err = repo.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, listed)
}

func TestServiceAccessPolicySQLiteDeleteManyIsAtomic(t *testing.T) {
	ctx := context.Background()
	p1, p2, repo, database := seedPolicyResources(t)
	require.NoError(t, repo.Create(ctx, p1))
	require.NoError(t, repo.Create(ctx, p2))

	_, err := database.Exec(`CREATE TRIGGER reject_second_policy_delete BEFORE DELETE ON service_access_policies
		WHEN OLD.id = 'abc-222' BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`)
	require.NoError(t, err)
	require.Error(t, repo.DeleteMany(ctx, []string{p1.ID, p2.ID}))
	items, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{p1.ID, p2.ID}, []string{items[0].ID, items[1].ID},
		"a failed multi-delete must roll back every selected policy")
}

func TestServiceAccessPolicySQLiteCascadesAndExplicitInvalidation(t *testing.T) {
	ctx := context.Background()
	tests := map[string]struct {
		delete func(context.Context, network.ServiceAccessPolicyRepository) error
	}{
		"destination VM": {delete: func(ctx context.Context, repo network.ServiceAccessPolicyRepository) error {
			return repo.DeleteByVM(ctx, "vm-a")
		}},
		"source network": {delete: func(ctx context.Context, repo network.ServiceAccessPolicyRepository) error {
			return repo.DeleteBySourceNetwork(ctx, "net-a")
		}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			p1, _, repo, _ := seedPolicyResources(t)
			require.NoError(t, repo.Create(ctx, p1))
			require.NoError(t, tc.delete(ctx, repo))
			items, err := repo.List(ctx)
			require.NoError(t, err)
			assert.Empty(t, items)
		})
	}
}

func TestServiceAccessPolicySQLiteForeignKeyCascades(t *testing.T) {
	ctx := context.Background()
	tests := map[string]struct{ statement string }{
		"destination VM deletion": {statement: "DELETE FROM vm_instances WHERE id = 'vm-a'"},
		"source network deletion": {statement: "DELETE FROM networks WHERE id = 'net-a'"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			p1, _, repo, database := seedPolicyResources(t)
			require.NoError(t, repo.Create(ctx, p1))
			_, err := database.ExecContext(ctx, tc.statement)
			require.NoError(t, err)
			items, err := repo.List(ctx)
			require.NoError(t, err)
			assert.Empty(t, items)
		})
	}
}
