package db_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/db"
)

func migratePolicyFixtureTo(t *testing.T, target int) *db.Handle {
	t.Helper()
	handle := db.New(filepath.Join(t.TempDir(), "policy-v3.db"))
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	names := []string{
		"001_initial_schema.sql",
		"002_jailer_launch.sql",
		"003_vm_cgroup_limits.sql",
		"004_typed_policies.sql",
	}
	for _, name := range names[:target] {
		contents, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "migrations", name))
		require.NoError(t, err)
		_, err = handle.DB().Exec(string(contents))
		require.NoError(t, err)
	}
	return handle
}

func migratePolicyFixtureToV3(t *testing.T) *db.Handle { return migratePolicyFixtureTo(t, 3) }

func migratePolicyFixtureToV4(t *testing.T) *db.Handle { return migratePolicyFixtureTo(t, 4) }

func migrateLegacyPolicyFixtureToV4(t *testing.T) *db.Handle {
	t.Helper()
	handle := migratePolicyFixtureToV3(t)
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	contents, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "testdata", "004_typed_policies_legacy_v4.sql"))
	require.NoError(t, err)
	_, err = handle.DB().Exec(string(contents))
	require.NoError(t, err)
	return handle
}

func TestMigration005RejectsLegacyV4Schema(t *testing.T) {
	ctx := t.Context()
	handle := migrateLegacyPolicyFixtureToV4(t)

	var legacyTableCount, legacyColumnCount int
	require.NoError(t, handle.DB().GetContext(ctx, &legacyTableCount,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'service_access_policies'"))
	require.NoError(t, handle.DB().GetContext(ctx, &legacyColumnCount,
		"SELECT COUNT(*) FROM pragma_table_info('vm_instances') WHERE name = 'remote_exec'"))
	assert.Equal(t, 1, legacyTableCount)
	assert.Equal(t, 1, legacyColumnCount)

	applied, err := handle.RunMigrationsCtx(ctx)
	require.Error(t, err)
	assert.Zero(t, applied)
	var version int
	require.NoError(t, handle.DB().GetContext(ctx, &version, "PRAGMA user_version"))
	assert.Equal(t, 4, version)
}

func TestMigration005AcceptsFreshSchema(t *testing.T) {
	ctx := t.Context()
	handle := db.New(filepath.Join(t.TempDir(), "policy-fresh.db"))
	t.Cleanup(func() { require.NoError(t, handle.Close()) })

	applied, err := handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, applied)
	var version int
	require.NoError(t, handle.DB().GetContext(ctx, &version, "PRAGMA user_version"))
	assert.Equal(t, 5, version)
}

func TestMigration004RejectsLegacyFirewallState(t *testing.T) {
	ctx := context.Background()
	handle := migratePolicyFixtureToV3(t)
	database := handle.DB()
	_, err := database.ExecContext(ctx, `
		INSERT INTO networks (id,name,subnet,bridge,ipv4_gateway,bridge_active,nat_enabled,is_default,is_present,created_at,updated_at)
		VALUES ('source','source','10.1.0.0/24','mvm-source','10.1.0.1',1,1,0,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
		       ('destination','destination','10.2.0.0/24','mvm-dest','10.2.0.1',1,1,0,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
		INSERT INTO iptables_rules (table_name,chain_name,rule_type,protocol,source,destination,in_interface,
			out_interface,target,sport,dport,network_id,comment_tag,command_string,is_active)
		VALUES ('filter','MVM-FORWARD','forward_out','tcp','10.1.0.0/24','0.0.0.0/0','mvm-source',
			'eth0','ACCEPT',0,443,'source','legacy-iptables','iptables legacy',1);
		INSERT INTO nftables_rules (chain,rule_type,table_name,protocol,source,destination,in_interface,
			out_interface,target,sport,dport,network_id,nft_handle,comment_tag,command_string,is_active)
		VALUES ('MVM-FORWARD','forward_out','filter','udp','10.1.0.0/24','0.0.0.0/0','mvm-source',
			'eth0','ACCEPT',0,53,'source',42,'legacy-nftables','nft legacy',1);
	`)
	require.NoError(t, err)

	applied, err := handle.RunMigrationsCtx(ctx)
	require.Error(t, err)
	assert.Zero(t, applied)
	var version int
	require.NoError(t, handle.DB().GetContext(ctx, &version, "PRAGMA user_version"))
	assert.Equal(t, 3, version)
}

func seedTypedPolicyResources(t *testing.T, ctx context.Context, handle *db.Handle) {
	t.Helper()
	_, err := handle.DB().ExecContext(ctx, `
		INSERT INTO networks
			(id,name,subnet,bridge,ipv4_gateway,bridge_active,nat_enabled,is_default,is_present)
		VALUES ('net-a','net-a','10.1.0.0/24','mvm-a','10.1.0.1',1,1,0,1);
		INSERT INTO images
			(id,type,version,name,arch,path,fs_type,original_size,minimum_rootfs_size_mib,pulled_at,is_present)
		VALUES ('image','alpine','1','image','x86_64','/image','ext4',1,1,CURRENT_TIMESTAMP,1);
		INSERT INTO kernels (id,name,base_name,version,arch,type,path,is_present)
		VALUES ('kernel','kernel','kernel','1','x86_64','firecracker','/kernel',1);
		INSERT INTO binaries (id,type,architecture,version,full_version,path,is_present)
		VALUES ('firecracker','firecracker','x86_64','1','1','/firecracker',1);
		INSERT INTO vm_instances
			(id,name,status,pid,ipv4,mac,network_id,tap_device,image_id,kernel_id,binary_id,
			 api_socket_path,config_path,cloud_init_mode,vcpu_count,mem_size_mib,disk_size_mib,
			 rootfs_path,rootfs_suffix,pci_enabled,nested_virt,remote_exec,lsm_flags,
			 enable_logging,enable_metrics,enable_console,boot_args)
		VALUES
			('vm-a','vm-a','stopped',0,'10.1.0.2','02:00:00:00:00:01','net-a','tap-a',
			 'image','kernel','firecracker','/api-a','/config-a','inject',1,128,64,'/root-a','',
			 0,0,0,'',0,0,0,''),
			('vm-b','vm-b','stopped',0,'10.1.0.3','02:00:00:00:00:02','net-a','tap-b',
			 'image','kernel','firecracker','/api-b','/config-b','inject',1,128,64,'/root-b','',
			 0,0,0,'',0,0,0,'');
	`)
	require.NoError(t, err)
}

func TestMigration005RejectsLegacyAuthorityState(t *testing.T) {
	tests := map[string]func(*testing.T, context.Context, *db.Handle){
		"remote exec grant": func(t *testing.T, ctx context.Context, handle *db.Handle) {
			_, err := handle.DB().ExecContext(ctx, "UPDATE vm_instances SET remote_exec = 1 WHERE id = 'vm-a'")
			require.NoError(t, err)
		},
		"service access policy": func(t *testing.T, ctx context.Context, handle *db.Handle) {
			_, err := handle.DB().ExecContext(ctx, `INSERT INTO service_access_policies
				(id,source_network_id,destination_vm_id,protocol,destination_port_start,destination_port_end)
				VALUES ('legacy','net-a','vm-b','tcp',443,443)`)
			require.NoError(t, err)
		},
	}
	for name, seedLegacyAuthority := range tests {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			handle := migratePolicyFixtureToV4(t)
			seedTypedPolicyResources(t, ctx, handle)
			seedLegacyAuthority(t, ctx, handle)

			applied, err := handle.RunMigrationsCtx(ctx)
			require.Error(t, err)
			assert.Zero(t, applied)
			var version int
			require.NoError(t, handle.DB().GetContext(ctx, &version, "PRAGMA user_version"))
			assert.Equal(t, 4, version)
		})
	}
}

func TestMigration004RejectsLegacyRemoteExecGrant(t *testing.T) {
	ctx := t.Context()
	handle := migratePolicyFixtureToV3(t)
	seedTypedPolicyResources(t, ctx, handle)
	_, err := handle.DB().ExecContext(ctx, "UPDATE vm_instances SET remote_exec = 1 WHERE id = 'vm-a'")
	require.NoError(t, err)

	applied, err := handle.RunMigrationsCtx(ctx)
	require.Error(t, err)
	assert.Zero(t, applied)
	var version int
	require.NoError(t, handle.DB().GetContext(ctx, &version, "PRAGMA user_version"))
	assert.Equal(t, 3, version)
}

func TestMigration004TypedPolicyConstraints(t *testing.T) {
	ctx := t.Context()
	handle := migratePolicyFixtureToV3(t)
	database := handle.DB()
	applied, err := handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, applied)
	seedTypedPolicyResources(t, ctx, handle)

	validPolicyID := strings.Repeat("a", 64)
	tests := map[string]string{
		"network source missing": fmt.Sprintf(`INSERT INTO network_traffic_policies
			(id,source_network_id,destination_vm_id,protocol,destination_port_start,destination_port_end)
			VALUES ('%s','missing','vm-b','tcp',443,443)`, validPolicyID),
		"network protocol invalid": fmt.Sprintf(`INSERT INTO network_traffic_policies
			(id,source_network_id,destination_vm_id,protocol,destination_port_start,destination_port_end)
			VALUES ('%s','net-a','vm-b','icmp',443,443)`, validPolicyID),
		"network port invalid": fmt.Sprintf(`INSERT INTO network_traffic_policies
			(id,source_network_id,destination_vm_id,protocol,destination_port_start,destination_port_end)
			VALUES ('%s','net-a','vm-b','tcp',5000,4999)`, validPolicyID),
		"network policy ID noncanonical": `INSERT INTO network_traffic_policies
			(id,source_network_id,destination_vm_id,protocol,destination_port_start,destination_port_end)
			VALUES ('ABC','net-a','vm-b','tcp',443,443)`,
		"VM self grant": fmt.Sprintf(`INSERT INTO vm_traffic_policies
			(id,source_vm_id,destination_vm_id,protocol,destination_port_start,destination_port_end)
			VALUES ('%s','vm-a','vm-a','tcp',443,443)`, validPolicyID),
		"VM destination missing": fmt.Sprintf(`INSERT INTO vm_traffic_policies
			(id,source_vm_id,destination_vm_id,protocol,destination_port_start,destination_port_end)
			VALUES ('%s','vm-a','missing','tcp',443,443)`, validPolicyID),
		"exec self grant": fmt.Sprintf(`INSERT INTO exec_policies
			(id,source_vm_id,destination_vm_id,target_user) VALUES ('%s','vm-a','vm-a','app')`, validPolicyID),
		"exec root user": fmt.Sprintf(`INSERT INTO exec_policies
			(id,source_vm_id,destination_vm_id,target_user) VALUES ('%s','vm-a','vm-b','root')`, validPolicyID),
		"exec empty user": fmt.Sprintf(`INSERT INTO exec_policies
			(id,source_vm_id,destination_vm_id,target_user) VALUES ('%s','vm-a','vm-b','')`, validPolicyID),
		"exec uppercase user": fmt.Sprintf(`INSERT INTO exec_policies
			(id,source_vm_id,destination_vm_id,target_user) VALUES ('%s','vm-a','vm-b','App')`, validPolicyID),
		"exec numeric-leading user": fmt.Sprintf(`INSERT INTO exec_policies
			(id,source_vm_id,destination_vm_id,target_user) VALUES ('%s','vm-a','vm-b','1app')`, validPolicyID),
		"exec user with spaces": fmt.Sprintf(`INSERT INTO exec_policies
			(id,source_vm_id,destination_vm_id,target_user) VALUES ('%s','vm-a','vm-b','app user')`, validPolicyID),
		"exec user too long": fmt.Sprintf(`INSERT INTO exec_policies
			(id,source_vm_id,destination_vm_id,target_user)
			VALUES ('%s','vm-a','vm-b','this-user-name-is-longer-than-32-characters')`, validPolicyID),
		"exec source missing": fmt.Sprintf(`INSERT INTO exec_policies
			(id,source_vm_id,destination_vm_id,target_user) VALUES ('%s','missing','vm-b','app')`, validPolicyID),
	}
	for name, statement := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := database.ExecContext(ctx, statement)
			assert.Error(t, err)
		})
	}
}
