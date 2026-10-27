package db_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/db"
	"mvmctl/pkg/errs"
)

func TestBaselineInitializesOnce(t *testing.T) {
	ctx := t.Context()
	handle := newMigrationDatabase(t)
	pending, err := handle.GetPendingMigrations(ctx)
	require.NoError(t, err)
	if diff := cmp.Diff([]string{"001_baseline_0_3.sql", "002_host_traffic_policies.sql"}, pending); diff != "" {
		t.Fatalf("pending migrations (-want +got):\n%s", diff)
	}
	applied, err := handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, applied)
	seedTypedPolicyResources(t, ctx, handle)

	var stored struct {
		Jailer    string `db:"jailer_binary_id"`
		Limits    string `db:"cgroup_limits"`
		Placement string `db:"network_placement"`
	}
	require.NoError(t, handle.DB().GetContext(ctx, &stored,
		"SELECT jailer_binary_id, cgroup_limits, network_placement FROM vm_instances WHERE id = 'vm-a'"))
	assert.Empty(t, stored.Jailer)
	assert.Equal(t, "managed", stored.Placement)
	assert.JSONEq(
		t,
		`{"policy_version":0,"cpu_quota_micros":100000,"cpu_period_micros":100000,"cpu_weight":100,"memory_high_bytes":671088640,"memory_max_bytes":671088640,"swap_max_bytes":0,"pids_max":256}`,
		stored.Limits,
	)
	_, err = handle.DB().ExecContext(ctx, "UPDATE vm_instances SET jailer_binary_id = 'exact-jailer' WHERE id = 'vm-a'")
	require.NoError(t, err)
	require.NoError(t, handle.Close())

	// Rationale: Reopening must recognize its own receipt without replaying or changing user data.
	applied, err = handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Zero(t, applied)
	pending, err = handle.GetPendingMigrations(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending)
	var version int
	require.NoError(t, handle.DB().GetContext(ctx, &version, "PRAGMA user_version"))
	assert.Equal(t, 2, version)
	var jailer string
	require.NoError(t, handle.DB().GetContext(ctx, &jailer,
		"SELECT jailer_binary_id FROM vm_instances WHERE id = 'vm-a'"))
	assert.Equal(t, "exact-jailer", jailer)
	var retired []string
	require.NoError(t, handle.DB().SelectContext(ctx, &retired,
		"SELECT name FROM sqlite_master WHERE name IN ('iptables_rules','nftables_rules','service_access_policies')"))
	assert.Empty(t, retired)
}

func TestBaselineRejectsUnrecognizedDatabaseWithoutChanges(t *testing.T) {
	checks := map[string]func(context.Context, *db.Handle) error{
		"pending": func(ctx context.Context, handle *db.Handle) error {
			_, err := handle.GetPendingMigrations(ctx)
			return err
		},
		"apply": func(ctx context.Context, handle *db.Handle) error {
			_, err := handle.RunMigrationsCtx(ctx)
			return err
		},
	}
	fixtures := map[string]string{
		"unversioned existing schema": "PRAGMA user_version = 0",
		"old version one":             "PRAGMA user_version = 1",
		"old version nine":            "PRAGMA user_version = 9",
		"unrecognized newer version":  "PRAGMA user_version = 10",
		"old baseline receipt": `CREATE TABLE db_migrations (version INTEGER, name TEXT, checksum TEXT);
			INSERT INTO db_migrations VALUES (1, '001_initial_schema.sql', NULL); PRAGMA user_version = 1`,
		"new name without checksum": `CREATE TABLE db_migrations (version INTEGER, name TEXT, checksum TEXT);
			INSERT INTO db_migrations VALUES (1, '001_baseline_0_3.sql', NULL); PRAGMA user_version = 1`,
	}
	for name, fixture := range fixtures {
		for checkName, check := range checks {
			t.Run(name+"/"+checkName, func(t *testing.T) {
				handle := newMigrationDatabase(t)
				_, err := handle.DB().ExecContext(t.Context(),
					"CREATE TABLE preserved (value TEXT); INSERT INTO preserved VALUES ('keep-me');"+fixture)
				require.NoError(t, err)
				var beforeSchema, afterSchema []string
				require.NoError(t, handle.DB().SelectContext(t.Context(), &beforeSchema,
					"SELECT sql FROM sqlite_master ORDER BY name"))
				beforeVersion, err := handle.GetCurrentVersion()
				require.NoError(t, err)
				err = check(t.Context(), handle)
				require.Error(t, err)
				var domainErr *errs.DomainError
				require.True(t, errors.As(err, &domainErr))
				assert.Equal(t, errs.CodeMigrationFailed, domainErr.Code)
				assert.Equal(t, errs.ClassValidation, domainErr.Class)
				require.NoError(t, handle.DB().SelectContext(t.Context(), &afterSchema,
					"SELECT sql FROM sqlite_master ORDER BY name"))
				if diff := cmp.Diff(beforeSchema, afterSchema); diff != "" {
					t.Errorf("refusal changed schema (-before +after):\n%s", diff)
				}
				afterVersion, err := handle.GetCurrentVersion()
				require.NoError(t, err)
				assert.Equal(t, beforeVersion, afterVersion)
				var value string
				require.NoError(t, handle.DB().GetContext(t.Context(), &value, "SELECT value FROM preserved"))
				assert.Equal(t, "keep-me", value)
			})
		}
	}
}

func TestBaselineRejectsAlteredReceipt(t *testing.T) {
	for name, mutation := range map[string]string{
		"checksum":         "UPDATE db_migrations SET checksum = 'wrong'",
		"missing receipt":  "DELETE FROM db_migrations",
		"version mismatch": "PRAGMA user_version = 0",
		"extra receipt":    "INSERT INTO db_migrations (version,name,checksum) VALUES (3,'other','other')",
	} {
		t.Run(name, func(t *testing.T) {
			handle := newMigrationDatabase(t)
			_, err := handle.RunMigrationsCtx(t.Context())
			require.NoError(t, err)
			_, err = handle.DB().ExecContext(t.Context(), mutation)
			require.NoError(t, err)
			_, err = handle.GetPendingMigrations(t.Context())
			assert.Error(t, err)
			_, err = handle.RunMigrationsCtx(t.Context())
			assert.Error(t, err)
		})
	}
}

func TestBaselineRollsBackPartialSchemaAndCanRetry(t *testing.T) {
	// Rationale: Disk exhaustion after several CREATE statements must not leave
	// a half-created schema, tracking row or advanced version that prevents retry.
	handle := newMigrationDatabase(t)
	_, err := handle.DB().ExecContext(t.Context(), "PRAGMA max_page_count = 10")
	require.NoError(t, err)
	applied, err := handle.RunMigrationsCtx(t.Context())
	require.Error(t, err)
	assert.Zero(t, applied)
	var sqliteErr interface {
		error
		Code() int
	}
	require.ErrorAs(t, err, &sqliteErr)
	assert.Equal(t, 13, sqliteErr.Code()) // SQLITE_FULL: the intended partial-write failure.
	var schema []string
	require.NoError(t, handle.DB().SelectContext(t.Context(), &schema,
		"SELECT name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'"))
	assert.Empty(t, schema)
	version, err := handle.GetCurrentVersion()
	require.NoError(t, err)
	assert.Zero(t, version)
	_, err = handle.DB().ExecContext(t.Context(), "PRAGMA max_page_count = 10000")
	require.NoError(t, err)
	applied, err = handle.RunMigrationsCtx(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, applied)
	var receipt string
	require.NoError(t, handle.DB().GetContext(t.Context(), &receipt,
		"SELECT name FROM db_migrations WHERE version = 1"))
	assert.Equal(t, "001_baseline_0_3.sql", receipt)
}

func TestBaselineCancellationLeavesDatabaseEmpty(t *testing.T) {
	handle := newMigrationDatabase(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := handle.RunMigrationsCtx(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	var names []string
	require.NoError(t, handle.DB().SelectContext(t.Context(), &names,
		"SELECT name FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'"))
	assert.Empty(t, names, fmt.Sprintf("cancelled initialization persisted %v", names))
}
