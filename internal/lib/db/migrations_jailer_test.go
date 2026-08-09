package db_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/db"
)

// Rationale: Existing databases must bind each VM to the exact live Jailer
// record matching its persisted Firecracker version, while unmatched pairs fail closed.
func TestMigration002_BackfillsJailerBinaryID(t *testing.T) {
	ctx := context.Background()
	handle := db.New(filepath.Join(t.TempDir(), "migration-002.db"))
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	database := handle.DB()
	_, err := database.ExecContext(ctx, `
		CREATE TABLE binaries (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			version TEXT NOT NULL,
			deleted_at TEXT NULL
		);
		CREATE TABLE vm_instances (
			id TEXT PRIMARY KEY,
			binary_id TEXT NOT NULL
		);
		INSERT INTO binaries (id, type, version, deleted_at) VALUES
			('fc-match', 'firecracker', '1.16.0', NULL),
			('jl-match', 'jailer', '1.16.0', NULL),
			('fc-missing', 'firecracker', '1.15.0', NULL),
			('fc-deleted-pair', 'firecracker', '1.14.0', NULL),
			('jl-deleted', 'jailer', '1.14.0', '2026-08-09T00:00:00Z');
		INSERT INTO vm_instances (id, binary_id) VALUES
			('vm-match', 'fc-match'),
			('vm-missing', 'fc-missing'),
			('vm-deleted-pair', 'fc-deleted-pair');
		PRAGMA user_version = 1;
	`)
	require.NoError(t, err)

	applied, err := handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, applied)

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
	assert.Equal(t, 3, schemaVersion)
	var indexCount int
	require.NoError(t, database.GetContext(ctx, &indexCount,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_vm_instances_jailer_binary'"))
	assert.Equal(t, 1, indexCount)
}

func TestMigration003_AddsTypedVMCgroupLimitsDefault(t *testing.T) {
	ctx := context.Background()
	handle := db.New(filepath.Join(t.TempDir(), "migration-003.db"))
	t.Cleanup(func() { require.NoError(t, handle.Close()) })
	database := handle.DB()
	_, err := database.ExecContext(ctx, `
		CREATE TABLE vm_instances (
			id TEXT PRIMARY KEY,
			vcpu_count INTEGER NOT NULL,
			mem_size_mib INTEGER NOT NULL
		);
		INSERT INTO vm_instances (id, vcpu_count, mem_size_mib) VALUES ('legacy-vm', 1, 512);
		PRAGMA user_version = 2;
	`)
	require.NoError(t, err)

	applied, err := handle.RunMigrationsCtx(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, applied)

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
	assert.Equal(t, 3, schemaVersion)
}
