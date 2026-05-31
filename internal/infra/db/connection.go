package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/errs"

	_ "modernc.org/sqlite"
)

type Handle struct {
	mu     sync.Mutex
	db     *sqlx.DB
	dbPath string
	opened bool
}

// DBExists returns true if the mvm database file exists in the given cache
// directory. This is a filesystem-only check — no connection is opened.
// Matching Python's CacheUtils.get_mvm_db_path().exists().
func DBExists(cacheDir string) bool {
	_, err := os.Stat(filepath.Join(cacheDir, infra.MVMDBFilename))
	return err == nil
}

// New creates a Handle for the given dbPath.
// Does NOT open a database connection — matching Python's Database.__init__().
// The caller is responsible for ensuring the parent directory exists.
//
// PRAGMAs are passed via DSN parameters so they apply automatically to every
// new connection from the pool (matching Python's connect() which sets PRAGMAs
// on each new connection).
func New(dbPath string) *Handle {
	return &Handle{
		dbPath: dbPath,
	}
}

// openLazy opens the database connection on first use, matching Python's lazy
// connect() pattern. PRAGMAs are set via DSN parameters so they apply to
// every new connection from the pool.
//
// Panics on failure — a database connection is required for the application
// to function and failures are unrecoverable.
func (d *Handle) openLazy() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.opened {
		return
	}

	// PRAGMAs passed in DSN — applied to every new connection from the pool,
	// matching Python's connect() which sets PRAGMAs on each new connection.
	// These match Python's Database.connect() PRAGMA list:
	//   - foreign_keys = ON
	//   - journal_mode = WAL
	//   - synchronous = NORMAL
	//   - busy_timeout = 5000
	//   - wal_autocheckpoint = 1000
	//   - cache_size = -64000
	//
	// NOTE: SetMaxOpenConns(1) and SetMaxIdleConns(1) serialize writes to match
	// SQLite's single-writer semantics. This mirrors Python's serialized access
	// pattern even though Python creates fresh connections each time.
	pragmaParams := "_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode=WAL" +
		"&_pragma=synchronous=NORMAL" +
		"&_pragma=busy_timeout=5000" +
		"&_pragma=wal_autocheckpoint=1000" +
		"&_pragma=cache_size=-64000"

	db, err := sqlx.Open("sqlite", d.dbPath+"?"+pragmaParams)
	if err != nil {
		panic(fmt.Sprintf("failed to open database: %v", err))
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// Set file permissions when the pool first opens. The file is either
	// newly created by sql.Open or already exists. This runs once — not on
	// every Connect() call — because permissions don't change between calls.
	if err := os.Chmod(d.dbPath, infra.DBFilePerm); err != nil {
		slog.Warn("Failed to set db file permissions", "path", d.dbPath, "error", err)
	}

	d.db = db
	d.opened = true
}

// DB returns the underlying *sqlx.DB, opening it lazily if needed.
// Matching Python's connect() which yields a connection, but Go uses a pool.
func (d *Handle) DB() *sqlx.DB {
	d.openLazy()
	return d.db
}

// Path returns the database file path. Matches Python's db_path property.
func (d *Handle) Path() string {
	return d.dbPath
}

// Close closes the database connection pool.
func (d *Handle) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.db != nil {
		d.opened = false
		return d.db.Close()
	}
	return nil
}

// restoreFromSnapshot overwrites the database at dbPath with a consistent copy
// from snapshotPath using VACUUM INTO. All existing pool connections to the old
// file become stale (new inode on Linux). The caller must ensure the pool is
// closed before calling this, or accept that existing connections read old data.
func restoreFromSnapshot(snapshotPath, dbPath string) error {
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		return errs.MigrationError(
			fmt.Sprintf("Snapshot not found: %s", snapshotPath))
	}

	srcDB, err := sql.Open("sqlite", snapshotPath)
	if err != nil {
		return fmt.Errorf("open snapshot db for restore: %w", err)
	}
	defer srcDB.Close()

	if _, err := srcDB.Exec(fmt.Sprintf("VACUUM INTO '%s'", dbPath)); err != nil {
		return errs.MigrationError(
			fmt.Sprintf("Failed to restore from snapshot: %v", err))
	}

	// VACUUM INTO creates a new file (new inode) with default permissions.
	if err := os.Chmod(dbPath, infra.DBFilePerm); err != nil {
		slog.Warn("Failed to set permissions on restored database", "path", dbPath, "error", err)
	}

	return nil
}

// RestoreFromSnapshot restores the database from a snapshot file.
// Mirrors Python's Database._restore_from_snapshot().
//
// VACUUM INTO creates a NEW inode on Linux, so existing connections holding
// file descriptors to the old inode would continue serving stale data.
// To prevent this, RestoreFromSnapshot closes the connection pool before
// the restore. The pool reopens lazily on the next DB() call, getting the
// new inode with all PRAGMAs applied.
func (d *Handle) RestoreFromSnapshot(snapshotPath string) error {
	// Close the pool so existing connections don't hold stale page cache
	// from the old inode.
	if err := d.Close(); err != nil {
		return fmt.Errorf("close database before restore: %w", err)
	}
	return restoreFromSnapshot(snapshotPath, d.dbPath)
}

// Connect returns the underlying *sqlx.DB (lazily opened).
// Permissions are set once in openLazy() when the pool first opens, and
// in RestoreFromSnapshot() after a restore creates a new file.
func (d *Handle) Connect() *sqlx.DB {
	return d.DB()
}

// readCurrentVersion queries the current schema version from PRAGMA user_version.
// Shared by Handle.GetCurrentVersion and RunMigrationsCtx to avoid duplicating
// the same PRAGMA query.
func readCurrentVersion(db *sqlx.DB) (int, error) {
	var version int
	err := db.QueryRow("PRAGMA user_version").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("get user_version: %w", err)
	}
	return version, nil
}

// GetCurrentVersion returns the current schema version from PRAGMA user_version.
func (d *Handle) GetCurrentVersion() (int, error) {
	return readCurrentVersion(d.DB())
}

// Ping verifies the database connection is alive.
// Python's Database class has no Ping() method; this exists for Go convenience.
func (d *Handle) Ping() error {
	return d.DB().Ping()
}

// EnsureMigrationsTable creates the db_migrations tracking table if it doesn't exist.
// The db_migrations table must be bootstrapped before running any migrations
// because migration files record themselves in this table.
func EnsureMigrationsTable(ctx context.Context, db *sqlx.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS db_migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version INTEGER NOT NULL UNIQUE,
			name TEXT NOT NULL,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			checksum TEXT,
			snapshot_path TEXT
		)
	`)
	return err
}
