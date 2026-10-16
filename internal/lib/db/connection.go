package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/jmoiron/sqlx"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"

	sqlite "modernc.org/sqlite"
)

type Handle struct {
	mu     sync.Mutex // guards db and opened (lazy init)
	db     *sqlx.DB
	dbPath string
	opened bool
}

// DBExists returns true if the mvm database file exists in the given cache
// directory. This is a filesystem-only check — no connection is opened.
func DBExists(cacheDir string) bool {
	_, err := os.Stat(filepath.Join(cacheDir, infra.MVMDBFilename))
	return err == nil
}

// New creates a Handle for the given dbPath.
// Does NOT open a database connection — call DB() or Connect() to open lazily.
// The caller is responsible for ensuring the parent directory exists.
//
// PRAGMAs are passed via DSN parameters so they apply automatically to every
// new connection from the pool.
func New(dbPath string) *Handle {
	return &Handle{
		dbPath: dbPath,
	}
}

// openLazy opens the database connection on first use.
// PRAGMAs are set via DSN parameters so they apply to every new connection
// from the pool.
//
// Panics on failure — a database connection is required for the application
// to function and failures are unrecoverable.
func (d *Handle) openLazy() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.opened {
		return
	}

	// PRAGMAs passed in DSN — applied to every new connection from the pool:
	//   - foreign_keys = ON
	//   - journal_mode = WAL
	//   - synchronous = NORMAL
	//   - busy_timeout = 5000
	//   - wal_autocheckpoint = 1000
	//   - cache_size = -64000
	//
	// NOTE: SetMaxOpenConns(1) and SetMaxIdleConns(1) serialize writes to match
	// SQLite's single-writer semantics.
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

	// Ensure the DB file exists with correct permissions before our chmod runs.
	// sqlx.Open is lazy — it doesn't create the file until the first query.
	f, err := os.OpenFile(d.dbPath, os.O_CREATE|os.O_RDONLY, infra.DBFilePerm)
	if err != nil {
		slog.Warn("Failed to create db file", "path", d.dbPath, "error", err)
	} else {
		f.Close()
	}

	// Set file permissions explicitly. If the file was just created by
	// OpenFile above, permissions are already correct; this catches the
	// case where the file already existed with wrong permissions.
	if err := os.Chmod(d.dbPath, infra.DBFilePerm); err != nil {
		slog.Warn("Failed to set db file permissions", "path", d.dbPath, "error", err)
	}

	d.db = db
	d.opened = true
}

// DB returns the underlying *sqlx.DB, opening it lazily if needed.
func (d *Handle) DB() *sqlx.DB {
	d.openLazy()
	return d.db
}

// Path returns the database file path.
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

// RestoreFromSnapshot restores the database from a snapshot file.
//
// The restore uses SQLite's backup API against the existing database file so
// its WAL and shared-memory files stay paired with the same inode.
func (d *Handle) RestoreFromSnapshot(ctx context.Context, snapshotPath string) (restoreErr error) {
	// Keep this Handle from reopening while the dedicated restore connection owns
	// the destination database.
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.opened {
		d.opened = false
		if err := d.db.Close(); err != nil {
			return fmt.Errorf("close database before restore: %w", err)
		}
	}
	d.db = nil

	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		return errs.New(errs.CodeMigrationFailed,
			fmt.Sprintf("Snapshot not found: %s", snapshotPath))
	}

	destination, err := sql.Open("sqlite", d.dbPath+"?_pragma=busy_timeout=5000")
	if err != nil {
		return errs.WrapMsg(errs.CodeMigrationFailed, "open database for restore", err)
	}
	defer func() {
		if err := destination.Close(); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("close restored database: %w", err))
		}
	}()
	connection, err := destination.Conn(ctx)
	if err != nil {
		return errs.WrapMsg(errs.CodeMigrationFailed, "connect to database for restore", err)
	}
	defer func() {
		if err := connection.Close(); err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("release restore connection: %w", err))
		}
	}()
	if err := connection.Raw(func(driverConnection any) error {
		restorer, ok := driverConnection.(interface {
			NewRestore(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("sqlite restore API is unavailable")
		}
		backup, err := restorer.NewRestore(snapshotPath)
		if err != nil {
			return err
		}
		more, stepErr := backup.Step(-1)
		finishErr := backup.Finish()
		if more && stepErr == nil {
			stepErr = errors.New("sqlite restore did not finish copying the snapshot")
		}
		return errors.Join(stepErr, finishErr)
	}); err != nil {
		return errs.WrapMsg(errs.CodeMigrationFailed, "restore database from snapshot", err)
	}
	if err := os.Chmod(d.dbPath, infra.DBFilePerm); err != nil {
		return errs.WrapMsg(errs.CodeMigrationFailed, "set restored database permissions", err)
	}

	return nil
}

// Connect returns the underlying *sqlx.DB (lazily opened).
// Permissions are set once in openLazy() when the pool first opens, and
// in RestoreFromSnapshot() after SQLite restores the existing database.
func (d *Handle) Connect() *sqlx.DB {
	return d.DB()
}

// GetCurrentVersion returns the current schema version from PRAGMA user_version.
func (d *Handle) GetCurrentVersion() (int, error) {
	var version int
	err := d.DB().QueryRow("PRAGMA user_version").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("get user_version: %w", err)
	}
	return version, nil
}

// Ping verifies the database connection is alive.
func (d *Handle) Ping() error {
	return d.DB().Ping()
}

// ensureMigrationsTable creates the db_migrations tracking table if it doesn't exist.
func (d *Handle) ensureMigrationsTable(ctx context.Context) error {
	_, err := d.DB().ExecContext(ctx, `
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
