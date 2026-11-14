package execpolicy_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/execpolicy"
	"mvmctl/internal/lib/model"
	"mvmctl/internal/testutil"
	"mvmctl/pkg/errs"
)

func seedExecPolicyResources(t *testing.T) (*sqlx.DB, execpolicy.Repository) {
	t.Helper()
	database := testutil.NewInMemoryDB(t)
	_, err := database.Exec(`
		INSERT INTO networks
			(id,name,subnet,bridge,ipv4_gateway,bridge_active,nat_enabled,is_default,is_present)
		VALUES ('net','net','10.1.0.0/24','mvm-net','10.1.0.1',1,1,0,1);
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
			('vm-a','vm-a','stopped',0,'10.1.0.2','02:00:00:00:00:01','net','tap-a',
			 'image','kernel','firecracker','/config-a','inject',1,128,64,'/root-a','',
			 0,0,'',0,0,0,''),
			('vm-b','vm-b','stopped',0,'10.1.0.3','02:00:00:00:00:02','net','tap-b',
			 'image','kernel','firecracker','/config-b','inject',1,128,64,'/root-b','',
			 0,0,'',0,0,0,'');
	`)
	require.NoError(t, err)
	return database, execpolicy.NewRepository(database)
}

func TestExecPolicySQLiteCRUDDirectionAndExactUser(t *testing.T) {
	ctx := t.Context()
	_, repo := seedExecPolicyResources(t)
	intent := &model.VMToVMExecAllowIntent{
		ID: strings.Repeat("a", 64), SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
		CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:01Z",
	}
	require.NoError(t, repo.Create(ctx, intent))

	got, err := repo.Get(ctx, intent.ID)
	require.NoError(t, err)
	if diff := cmp.Diff(intent, got); diff != "" {
		t.Errorf("exec allow intent readback mismatch (-want +got):\n%s", diff)
	}
	missing, err := repo.Get(ctx, "missing")
	require.NoError(t, err)
	assert.Nil(t, missing)

	allowed, err := repo.Allows(ctx, "vm-a", "vm-b", "app")
	require.NoError(t, err)
	assert.True(t, allowed)
	for name, identity := range map[string][3]string{
		"reverse direction": {"vm-b", "vm-a", "app"},
		"different user":    {"vm-a", "vm-b", "other"},
	} {
		t.Run(name, func(t *testing.T) {
			allowed, err := repo.Allows(ctx, identity[0], identity[1], identity[2])
			require.NoError(t, err)
			assert.False(t, allowed)
		})
	}

	duplicate := *intent
	duplicate.ID = strings.Repeat("b", 64)
	assert.Error(t, repo.Create(ctx, &duplicate))
	require.NoError(t, repo.DeleteMany(ctx, []string{intent.ID}))
	listed, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, listed)
}

func TestExecPolicySQLiteForeignKeyCascade(t *testing.T) {
	ctx := t.Context()
	database, repo := seedExecPolicyResources(t)
	require.NoError(t, repo.Create(ctx, &model.VMToVMExecAllowIntent{
		ID: strings.Repeat("a", 64), SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
	}))
	_, err := database.ExecContext(ctx, "DELETE FROM vm_instances WHERE id = 'vm-a'")
	require.NoError(t, err)
	listed, err := repo.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, listed)
}

func TestExecPolicyServiceDuplicateAndAuthorizationErrorCodes(t *testing.T) {
	ctx := context.Background()
	database, repo := seedExecPolicyResources(t)
	service := execpolicy.NewService(repo)
	intent := &model.VMToVMExecAllowIntent{
		ID: strings.Repeat("a", 64), SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
	}
	require.NoError(t, service.Create(ctx, intent))
	err := service.Create(ctx, &model.VMToVMExecAllowIntent{
		ID: strings.Repeat("b", 64), SourceVMID: "vm-a", DestinationVMID: "vm-b", TargetUser: "app",
	})
	var domainErr *errs.DomainError
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, errs.CodePolicyAlreadyExists, domainErr.Code)

	require.NoError(t, database.Close())
	_, err = service.Allows(ctx, "vm-a", "vm-b", "app")
	require.ErrorAs(t, err, &domainErr)
	assert.Equal(t, errs.CodePolicyExecAuthorizeFailed, domainErr.Code)
}
