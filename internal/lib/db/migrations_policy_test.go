package db_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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
	names := []string{"001_initial_schema.sql", "002_jailer_launch.sql", "003_vm_cgroup_limits.sql"}
	for _, name := range names[:target] {
		contents, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "migrations", name))
		require.NoError(t, err)
		_, err = handle.DB().Exec(string(contents))
		require.NoError(t, err)
	}
	return handle
}

func migratePolicyFixtureToV3(t *testing.T) *db.Handle { return migratePolicyFixtureTo(t, 3) }

func TestMigration004FromValidV3Schema(t *testing.T) {
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
	require.NoError(t, err)
	assert.Equal(t, 1, applied)
	var version int
	require.NoError(t, database.GetContext(ctx, &version, "PRAGMA user_version"))
	assert.Equal(t, 4, version)

	for table, comment := range map[string]string{
		"iptables_rules": "legacy-iptables", "nftables_rules": "legacy-nftables",
	} {
		var count, dportEnd int
		require.NoError(t, database.QueryRowxContext(ctx,
			"SELECT COUNT(*), MAX(dport_end) FROM "+table+" WHERE comment_tag = ?", comment).Scan(&count, &dportEnd))
		assert.Equal(t, 1, count, table+" legacy row")
		assert.Zero(t, dportEnd, table+" migrated range end")
	}

	for _, check := range []struct{ table, column string }{
		{"iptables_rules", "dport_end"}, {"iptables_rules", "service_access_policy_id"},
		{"nftables_rules", "dport_end"}, {"nftables_rules", "service_access_policy_id"},
	} {
		var count int
		require.NoError(t, database.GetContext(ctx, &count,
			"SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", check.table, check.column))
		assert.Equal(t, 1, count, check.table+"."+check.column)
	}
	for _, index := range []string{
		"idx_service_access_policies_source", "idx_service_access_policies_destination_vm",
		"idx_iptables_rules_policy", "idx_iptables_rules_unique_active",
		"idx_nftables_rules_policy", "idx_nftables_rules_unique_active",
	} {
		var count int
		require.NoError(t, database.GetContext(ctx, &count,
			"SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?", index))
		assert.Equal(t, 1, count, index)
	}

	_, err = database.ExecContext(ctx, `INSERT INTO service_access_policies
		(id,source_network_id,destination_vm_id,protocol,destination_port_start,destination_port_end)
		VALUES ('bad','source','missing','icmp',0,65536)`)
	require.Error(t, err, "protocol, port, and foreign-key constraints must reject invalid intent")
}
