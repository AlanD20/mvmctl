package testutil_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/testutil"
)

func TestNewInMemoryDB(t *testing.T) {
	t.Parallel()
	db := testutil.NewInMemoryDB(t)

	// Verify that the db_migrations tracking table has records.
	var migrationCount int
	err := db.Get(&migrationCount, `SELECT COUNT(*) FROM db_migrations`)
	require.NoError(t, err)
	assert.Greater(t, migrationCount, 0, "should have at least one migration record")

	// Round-trip a kernel row via raw SQL.
	now := time.Now().Format(time.RFC3339)
	_, err = db.Exec(`
		INSERT INTO kernels (id, name, base_name, version, arch, type, path, is_default, is_present, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, "test-kernel-1", "vmlinux-6.1", "vmlinux-6.1", "6.1", "x86_64", "vmlinux", "/tmp/test-kernel", 0, 1, now, now)
	require.NoError(t, err)

	var name string
	err = db.Get(&name, `SELECT name FROM kernels WHERE id = ?`, "test-kernel-1")
	require.NoError(t, err)
	assert.Equal(t, "vmlinux-6.1", name)
}
