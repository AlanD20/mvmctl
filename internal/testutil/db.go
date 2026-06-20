package testutil

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/lib/db"
)

// NewInMemoryDB creates a temporary SQLite database for testing.
// It runs all pending migrations, registers cleanup with t.Cleanup, and
// returns the *sqlx.DB handle.
func NewInMemoryDB(t *testing.T) *sqlx.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "mvmctl-test.db")
	handle := db.New(dbPath)

	ctx := context.Background()
	if _, err := handle.RunMigrationsCtx(ctx); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	t.Cleanup(func() {
		if err := handle.Close(); err != nil {
			t.Logf("failed to close test DB: %v", err)
		}
	})

	return handle.DB()
}
