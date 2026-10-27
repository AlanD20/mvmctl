package db_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/db"
)

func newMigrationDatabase(t *testing.T) *db.Handle {
	t.Helper()
	handle := db.New(filepath.Join(t.TempDir(), "migration.db"))
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	return handle
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
			 config_path,cloud_init_mode,vcpu_count,mem_size_mib,disk_size_mib,
			 rootfs_path,rootfs_suffix,pci_enabled,nested_virt,lsm_flags,
			 enable_logging,enable_metrics,enable_console,boot_args)
		VALUES
			('vm-a','vm-a','stopped',0,'10.1.0.2','02:00:00:00:00:01','net-a','tap-a',
			 'image','kernel','firecracker','/config-a','inject',1,128,64,'/root-a','',
			 0,0,'',0,0,0,''),
			('vm-b','vm-b','stopped',0,'10.1.0.3','02:00:00:00:00:02','net-a','tap-b',
			 'image','kernel','firecracker','/config-b','inject',1,128,64,'/root-b','',
			 0,0,'',0,0,0,'');
	`)
	require.NoError(t, err)
}

func TestBaselineTypedPolicyConstraints(t *testing.T) {
	ctx := t.Context()
	handle := newMigrationDatabase(t)
	database := handle.DB()
	_, err := handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	seedTypedPolicyResources(t, ctx, handle)

	validPolicyID := strings.Repeat("a", 64)
	tests := map[string]string{
		"VM host source missing": fmt.Sprintf(`INSERT INTO vm_host_traffic_policies
			(id,source_vm_id,protocol,destination_port_start,destination_port_end)
			VALUES ('%s','missing','tcp',443,443)`, validPolicyID),
		"VM host port omitted": fmt.Sprintf(`INSERT INTO vm_host_traffic_policies
			(id,source_vm_id,protocol,destination_port_start,destination_port_end)
			VALUES ('%s','vm-a','tcp',0,0)`, validPolicyID),
		"network host protocol invalid": fmt.Sprintf(`INSERT INTO network_host_traffic_policies
			(id,source_network_id,protocol,destination_port_start,destination_port_end)
			VALUES ('%s','net-a','icmp',443,443)`, validPolicyID),
		"network host descending ports": fmt.Sprintf(`INSERT INTO network_host_traffic_policies
			(id,source_network_id,protocol,destination_port_start,destination_port_end)
			VALUES ('%s','net-a','udp',54,53)`, validPolicyID),
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
