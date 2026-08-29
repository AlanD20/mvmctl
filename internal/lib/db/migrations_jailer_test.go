package db_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rationale: Existing databases must bind each VM to the exact live Jailer
// record matching its persisted Firecracker version, while unmatched pairs fail closed.
func TestMigration002_BackfillsJailerBinaryID(t *testing.T) {
	ctx := context.Background()
	handle := migratePolicyFixtureTo(t, 1)
	database := handle.DB()
	_, err := database.ExecContext(ctx, `
		INSERT INTO networks (id,name,subnet,bridge,ipv4_gateway,bridge_active,nat_enabled,is_default,is_present,created_at,updated_at)
		VALUES ('net','net','10.0.0.0/24','mvm-net','10.0.0.1',1,1,0,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
		INSERT INTO images (id,type,version,name,arch,path,fs_type,original_size,minimum_rootfs_size_mib,pulled_at,is_present)
		VALUES ('img','alpine','1','img','x86_64','/img','ext4',1,1,CURRENT_TIMESTAMP,1);
		INSERT INTO kernels (id,name,base_name,version,arch,type,path,is_present)
		VALUES ('kernel','kernel','kernel','1','x86_64','firecracker','/kernel',1);
		INSERT INTO binaries (id,type,version,full_version,path,is_present,deleted_at) VALUES
			('fc-match','firecracker','1.16.0','1.16.0','/fc-match',1,NULL),
			('jl-match','jailer','1.16.0','1.16.0','/jl-match',1,NULL),
			('fc-missing','firecracker','1.15.0','1.15.0','/fc-missing',1,NULL),
			('fc-deleted-pair','firecracker','1.14.0','1.14.0','/fc-deleted-pair',1,NULL),
			('jl-deleted','jailer','1.14.0','1.14.0','/jl-deleted',1,'2026-08-09T00:00:00Z');
		INSERT INTO vm_instances (id,name,status,pid,ipv4,mac,network_id,tap_device,image_id,kernel_id,binary_id,
			api_socket_path,config_path,cloud_init_mode,vcpu_count,mem_size_mib,disk_size_mib,rootfs_path,rootfs_suffix,
			pci_enabled,nested_virt,remote_exec,lsm_flags,enable_logging,enable_metrics,enable_console,boot_args)
		VALUES
			('vm-match','vm-match','stopped',0,'10.0.0.2','02:00:00:00:00:01','net','tap1','img','kernel','fc-match',
			 '/api1','/cfg1','inject',1,128,64,'/root1','',0,0,0,'',0,0,0,''),
			('vm-missing','vm-missing','stopped',0,'10.0.0.3','02:00:00:00:00:02','net','tap2','img','kernel','fc-missing',
			 '/api2','/cfg2','inject',1,128,64,'/root2','',0,0,0,'',0,0,0,''),
			('vm-deleted-pair','vm-deleted-pair','stopped',0,'10.0.0.4','02:00:00:00:00:03','net','tap3','img','kernel',
			 'fc-deleted-pair','/api3','/cfg3','inject',1,128,64,'/root3','',0,0,0,'',0,0,0,'');
	`)
	require.NoError(t, err)

	applied, err := handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, applied)

	rows := map[string]string{}
	dbRows, err := database.QueryContext(ctx, "SELECT id, jailer_binary_id FROM vm_instances ORDER BY id")
	require.NoError(t, err)
	defer dbRows.Close()
	for dbRows.Next() {
		var id, jailerID string
		require.NoError(t, dbRows.Scan(&id, &jailerID))
		rows[id] = jailerID
	}
	require.NoError(t, dbRows.Err())
	want := map[string]string{
		"vm-match": "jl-match", "vm-missing": "", "vm-deleted-pair": "",
	}
	if diff := cmp.Diff(want, rows); diff != "" {
		t.Errorf("migration backfill mismatch (-want +got):\n%s", diff)
	}

	var schemaVersion int
	require.NoError(t, database.GetContext(ctx, &schemaVersion, "PRAGMA user_version"))
	assert.Equal(t, 4, schemaVersion)
	var indexCount int
	require.NoError(t, database.GetContext(ctx, &indexCount,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_vm_instances_jailer_binary'"))
	assert.Equal(t, 1, indexCount)
}

func TestMigration003_AddsTypedVMCgroupLimitsDefault(t *testing.T) {
	ctx := context.Background()
	handle := migratePolicyFixtureTo(t, 2)
	database := handle.DB()
	_, err := database.ExecContext(ctx, `
		INSERT INTO networks (id,name,subnet,bridge,ipv4_gateway,bridge_active,nat_enabled,is_default,is_present,created_at,updated_at)
		VALUES ('net','net','10.0.0.0/24','mvm-net','10.0.0.1',1,1,0,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
		INSERT INTO images (id,type,version,name,arch,path,fs_type,original_size,minimum_rootfs_size_mib,pulled_at,is_present)
		VALUES ('img','alpine','1','img','x86_64','/img','ext4',1,1,CURRENT_TIMESTAMP,1);
		INSERT INTO kernels (id,name,base_name,version,arch,type,path,is_present)
		VALUES ('kernel','kernel','kernel','1','x86_64','firecracker','/kernel',1);
		INSERT INTO binaries (id,type,version,full_version,path,is_present)
		VALUES ('fc','firecracker','1','1','/fc',1);
		INSERT INTO vm_instances (id,name,status,pid,ipv4,mac,network_id,tap_device,image_id,kernel_id,binary_id,
			jailer_binary_id,api_socket_path,config_path,cloud_init_mode,vcpu_count,mem_size_mib,disk_size_mib,rootfs_path,
			rootfs_suffix,pci_enabled,nested_virt,remote_exec,lsm_flags,enable_logging,enable_metrics,enable_console,boot_args)
		VALUES ('legacy-vm','legacy-vm','stopped',0,'10.0.0.2','02:00:00:00:00:01','net','tap','img','kernel','fc','',
			'/api','/cfg','inject',1,512,64,'/root','',0,0,0,'',0,0,0,'');
	`)
	require.NoError(t, err)

	applied, err := handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, applied)

	var encoded string
	require.NoError(t, database.GetContext(ctx, &encoded,
		"SELECT cgroup_limits FROM vm_instances WHERE id = 'legacy-vm'"))
	assert.JSONEq(t, `{
		"policy_version": 0,
		"cpu_quota_micros": 100000,
		"cpu_period_micros": 100000,
		"cpu_weight": 100,
		"memory_high_bytes": 671088640,
		"memory_max_bytes": 671088640,
		"swap_max_bytes": 0,
		"pids_max": 256
	}`, encoded)
	var schemaVersion int
	require.NoError(t, database.GetContext(ctx, &schemaVersion, "PRAGMA user_version"))
	assert.Equal(t, 4, schemaVersion)
}
