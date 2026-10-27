package db_test

import (
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostPolicyMigrationPreservesPublishedBaselineState(t *testing.T) {
	// Rationale: Existing v0.3 operators must gain host policies without reinitializing their VM database.
	ctx := t.Context()
	handle := newMigrationDatabase(t)
	baseline, err := os.ReadFile("migrations/001_baseline_0_3.sql")
	require.NoError(t, err)
	_, err = handle.DB().ExecContext(ctx, string(baseline))
	require.NoError(t, err)
	const checksum = "c395825ee4155a34c200435b74f19be6df279dd7bb9400e460897322ab0be69c"
	_, err = handle.DB().ExecContext(ctx,
		`INSERT INTO db_migrations(version,name,checksum) VALUES(1,'001_baseline_0_3.sql',?); PRAGMA user_version=1`,
		checksum)
	require.NoError(t, err)
	seedTypedPolicyResources(t, ctx, handle)
	policyID := strings.Repeat("a", 64)
	_, err = handle.DB().ExecContext(ctx, `INSERT INTO vm_traffic_policies
		(id,source_vm_id,destination_vm_id,protocol,destination_port_start,destination_port_end)
		VALUES(?,'vm-a','vm-b','tcp',443,443)`, policyID)
	require.NoError(t, err)
	pending, err := handle.GetPendingMigrations(ctx)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"002_host_traffic_policies.sql"}, pending); diff != "" {
		t.Fatalf("additive migration selection (-want +got):\n%s", diff)
	}
	applied, err := handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, applied)
	var storedChecksum string
	require.NoError(t, handle.DB().GetContext(ctx, &storedChecksum,
		`SELECT checksum FROM db_migrations WHERE version=1`))
	assert.Equal(t, checksum, storedChecksum)
	var endpoints []string
	require.NoError(t, handle.DB().SelectContext(ctx, &endpoints,
		`SELECT id || ':' || name || ':' || rootfs_path FROM vm_instances ORDER BY id`))
	if diff := cmp.Diff([]string{"vm-a:vm-a:/root-a", "vm-b:vm-b:/root-b"}, endpoints); diff != "" {
		t.Errorf("preserved VM backing identity (-want +got):\n%s", diff)
	}
	var oldGrant string
	require.NoError(t, handle.DB().GetContext(ctx, &oldGrant,
		`SELECT source_vm_id || ':' || destination_vm_id || ':' || protocol || ':' || destination_port_start
		FROM vm_traffic_policies WHERE id=?`, policyID))
	assert.Equal(t, "vm-a:vm-b:tcp:443", oldGrant)
	for _, statement := range []string{
		`INSERT INTO vm_host_traffic_policies
		(id,source_vm_id,protocol,destination_port_start,destination_port_end)
		VALUES(?,'vm-a','udp',65535,65535)`,
		`INSERT INTO network_host_traffic_policies
		(id,source_network_id,protocol,destination_port_start,destination_port_end)
		VALUES(?,'net-a','tcp',1,65535)`,
	} {
		_, err := handle.DB().ExecContext(ctx, statement, policyID)
		require.NoError(t, err)
	}
	applied, err = handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Zero(t, applied)
}
