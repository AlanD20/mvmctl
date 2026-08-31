package network_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/network"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

func seedTrafficPolicyResources(t *testing.T) (*sqlx.DB, network.TrafficPolicyRepository) {
	t.Helper()
	database := testutil.NewInMemoryDB(t)
	_, err := database.Exec(`
		INSERT INTO networks
			(id,name,subnet,bridge,ipv4_gateway,bridge_active,nat_enabled,is_default,is_present)
		VALUES
			('net-a','net-a','10.1.0.0/24','mvm-a','10.1.0.1',1,1,0,1),
			('net-b','net-b','10.2.0.0/24','mvm-b','10.2.0.1',1,1,0,1),
			('net-c','net-c','10.3.0.0/24','mvm-c','10.3.0.1',1,1,0,1);
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
			('vm-b','vm-b','stopped',0,'10.2.0.2','02:00:00:00:00:02','net-b','tap-b',
			 'image','kernel','firecracker','/api-b','/config-b','inject',1,128,64,'/root-b','',
			 0,0,0,'',0,0,0,'');
	`)
	require.NoError(t, err)
	return database, network.NewTrafficPolicyRepository(database)
}

func TestTrafficPolicySQLiteCRUDAndDuplicates(t *testing.T) {
	ctx := t.Context()
	_, repo := seedTrafficPolicyResources(t)
	networkIntent := &model.NetworkToVMTrafficAllowIntent{
		ID: strings.Repeat("a", 64), SourceNetworkID: "net-a", DestinationVMID: "vm-b",
		Protocol: model.TrafficProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443,
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:01Z",
	}
	vmIntent := &model.VMToVMTrafficAllowIntent{
		ID: strings.Repeat("b", 64), SourceVMID: "vm-a", DestinationVMID: "vm-b",
		Protocol: model.TrafficProtocolUDP, DestinationPortStart: 5000, DestinationPortEnd: 5005,
		CreatedAt: "2026-01-02T00:00:00Z", UpdatedAt: "2026-01-02T00:00:01Z",
	}

	require.NoError(t, repo.CreateNetworkToVM(ctx, networkIntent))
	require.NoError(t, repo.CreateVMToVM(ctx, vmIntent))

	gotNetwork, err := repo.GetNetworkToVM(ctx, networkIntent.ID)
	require.NoError(t, err)
	if diff := cmp.Diff(networkIntent, gotNetwork); diff != "" {
		t.Errorf("network traffic allow intent readback mismatch (-want +got):\n%s", diff)
	}
	gotVM, err := repo.GetVMToVM(ctx, vmIntent.ID)
	require.NoError(t, err)
	if diff := cmp.Diff(vmIntent, gotVM); diff != "" {
		t.Errorf("VM traffic allow intent readback mismatch (-want +got):\n%s", diff)
	}

	missing, err := repo.GetNetworkToVM(ctx, "missing")
	require.NoError(t, err)
	assert.Nil(t, missing)
	byIdentity, err := repo.GetVMByIdentity(ctx, vmIntent)
	require.NoError(t, err)
	require.NotNil(t, byIdentity)
	assert.Equal(t, vmIntent.ID, byIdentity.ID)

	duplicateNetwork := *networkIntent
	duplicateNetwork.ID = strings.Repeat("c", 64)
	assert.Error(t, repo.CreateNetworkToVM(ctx, &duplicateNetwork))
	duplicateVM := *vmIntent
	duplicateVM.ID = strings.Repeat("d", 64)
	assert.Error(t, repo.CreateVMToVM(ctx, &duplicateVM))
	service := network.NewTrafficPolicyService(repo)
	err = service.CreateNetworkToVM(ctx, &duplicateNetwork)
	var domainErr *errs.DomainError
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, errs.CodePolicyAlreadyExists, domainErr.Code)

	require.NoError(t, repo.DeleteNetworkToVM(ctx, []string{networkIntent.ID}))
	require.NoError(t, repo.DeleteVMToVM(ctx, []string{vmIntent.ID}))
	allowIntents, err := service.AllowIntents(ctx)
	require.NoError(t, err)
	if diff := cmp.Diff(&network.TrafficPolicyAllowIntents{}, allowIntents); diff != "" {
		t.Errorf("traffic policy allow intents after delete mismatch (-want +got):\n%s", diff)
	}
}

func TestTrafficPolicySQLiteInvalidationByVMIsAtomic(t *testing.T) {
	ctx := t.Context()
	database, repo := seedTrafficPolicyResources(t)
	require.NoError(t, repo.CreateNetworkToVM(ctx, &model.NetworkToVMTrafficAllowIntent{
		ID: strings.Repeat("a", 64), SourceNetworkID: "net-a", DestinationVMID: "vm-b",
		Protocol: model.TrafficProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443,
	}))
	require.NoError(t, repo.CreateVMToVM(ctx, &model.VMToVMTrafficAllowIntent{
		ID: strings.Repeat("b", 64), SourceVMID: "vm-a", DestinationVMID: "vm-b",
		Protocol: model.TrafficProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443,
	}))
	_, err := database.Exec(`CREATE TRIGGER reject_vm_policy_invalidation
		BEFORE DELETE ON vm_traffic_policies
		BEGIN SELECT RAISE(ABORT, 'injected invalidation failure'); END`)
	require.NoError(t, err)

	err = repo.InvalidateByVM(ctx, "vm-b")
	require.Error(t, err)
	fromNetworks, err := repo.ListNetworkToVM(ctx)
	require.NoError(t, err)
	fromVMs, err := repo.ListVMToVM(ctx)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{strings.Repeat("a", 64)}, trafficNetworkIntentIDs(fromNetworks)); diff != "" {
		t.Errorf("network intents after rollback mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{strings.Repeat("b", 64)}, trafficVMIntentIDs(fromVMs)); diff != "" {
		t.Errorf("VM intents after rollback mismatch (-want +got):\n%s", diff)
	}
}

func TestTrafficPolicySQLiteForeignKeyCascades(t *testing.T) {
	t.Run("destination VM", func(t *testing.T) {
		ctx := t.Context()
		database, repo := seedTrafficPolicyResources(t)
		require.NoError(t, repo.CreateNetworkToVM(ctx, &model.NetworkToVMTrafficAllowIntent{
			ID: strings.Repeat("a", 64), SourceNetworkID: "net-a", DestinationVMID: "vm-b",
			Protocol: model.TrafficProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443,
		}))
		require.NoError(t, repo.CreateVMToVM(ctx, &model.VMToVMTrafficAllowIntent{
			ID: strings.Repeat("b", 64), SourceVMID: "vm-a", DestinationVMID: "vm-b",
			Protocol: model.TrafficProtocolUDP, DestinationPortStart: 53, DestinationPortEnd: 53,
		}))

		_, err := database.ExecContext(ctx, "DELETE FROM vm_instances WHERE id = 'vm-b'")
		require.NoError(t, err)
		fromNetworks, err := repo.ListNetworkToVM(ctx)
		require.NoError(t, err)
		fromVMs, err := repo.ListVMToVM(ctx)
		require.NoError(t, err)
		assert.Empty(t, fromNetworks)
		assert.Empty(t, fromVMs)
	})
	t.Run("source network", func(t *testing.T) {
		ctx := t.Context()
		database, repo := seedTrafficPolicyResources(t)
		require.NoError(t, repo.CreateNetworkToVM(ctx, &model.NetworkToVMTrafficAllowIntent{
			ID: strings.Repeat("a", 64), SourceNetworkID: "net-c", DestinationVMID: "vm-b",
			Protocol: model.TrafficProtocolTCP, DestinationPortStart: 443, DestinationPortEnd: 443,
		}))

		_, err := database.ExecContext(ctx, "DELETE FROM networks WHERE id = 'net-c'")
		require.NoError(t, err)
		fromNetworks, err := repo.ListNetworkToVM(ctx)
		require.NoError(t, err)
		assert.Empty(t, fromNetworks)
	})
}

func trafficNetworkIntentIDs(intents []*model.NetworkToVMTrafficAllowIntent) []string {
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		ids = append(ids, intent.ID)
	}
	return ids
}

func trafficVMIntentIDs(intents []*model.VMToVMTrafficAllowIntent) []string {
	ids := make([]string, 0, len(intents))
	for _, intent := range intents {
		ids = append(ids, intent.ID)
	}
	return ids
}

func TestTrafficPolicyServiceClassifiesRepositoryErrors(t *testing.T) {
	ctx := context.Background()
	database, repo := seedTrafficPolicyResources(t)
	service := network.NewTrafficPolicyService(repo)
	require.NoError(t, database.Close())

	tests := map[string]struct {
		call func() error
		code errs.Code
	}{
		"create": {call: func() error {
			return service.CreateNetworkToVM(ctx, &model.NetworkToVMTrafficAllowIntent{ID: "policy"})
		}, code: errs.CodePolicyCreateFailed},
		"list": {call: func() error {
			_, err := service.AllowIntents(ctx)
			return err
		}, code: errs.CodePolicyListFailed},
		"remove": {call: func() error {
			return service.RemoveNetworkToVM(ctx, []string{"policy"})
		}, code: errs.CodePolicyRemoveFailed},
		"invalidate": {call: func() error {
			return service.InvalidateByVM(ctx, "vm-a")
		}, code: errs.CodePolicyRemoveFailed},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := tc.call()
			var domainErr *errs.DomainError
			require.ErrorAs(t, err, &domainErr)
			assert.Equal(t, tc.code, domainErr.Code)
		})
	}
}
