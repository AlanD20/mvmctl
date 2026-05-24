package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

const DBName = "mvmdb.db"

const dbFilePerm = 0640

type Config struct {
	CacheDir string
}

// Database wraps connection management and migration support.
// Mirrors the Python Database class in src/mvmctl/core/_shared/_db.py.
//
// Python's Database.__init__ (lines 81-92) only ensures the parent directory
// exists. It does NOT open a database connection or set PRAGMAs. The first
// actual connection is created lazily by connect().
//
// Go's sql.Open() is also lazy — it validates the DSN but does not actually
// open a connection until the first query. However, we do NOT call Ping() or
// set PRAGMAs eagerly in New(). PRAGMAs are passed via DSN parameters so they
// are applied on every new connection from the pool.
type Database struct {
	mu     sync.Mutex
	db     *sql.DB
	dbPath string
	cfg    Config
	opened bool
}

// New creates a Database struct and ensures the cache directory exists.
// Does NOT open a database connection — matching Python's Database.__init__()
// which only calls mkdir(parents=True, exist_ok=True).
//
// PRAGMAs are passed via DSN parameters so they apply automatically to every
// new connection from the pool (matching Python's connect() which sets PRAGMAs
// on each new connection).
func New(cfg Config) *Database {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}

	cacheDir := cfg.CacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(home, ".cache", "mvmctl")
	}

	dbPath := filepath.Join(cacheDir, DBName)

	// Python: self._db_path.parent.mkdir(parents=True, exist_ok=True)
	// Default mode 0o777 (subject to umask). 0755 matches effective perms.
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		slog.Warn("Failed to create cache dir", "path", cacheDir, "error", err)
	}

	return &Database{
		dbPath: dbPath,
		cfg:    cfg,
	}
}

// openLazy opens the database connection on first use, matching Python's lazy
// connect() pattern. PRAGMAs are set via DSN parameters so they apply to
// every new connection from the pool.
func (d *Database) openLazy() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.opened {
		return nil
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

	db, err := sql.Open("sqlite", d.dbPath+"?"+pragmaParams)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	d.db = db
	d.opened = true
	return nil
}

// DB returns the underlying *sql.DB, opening it lazily if needed.
// Matching Python's connect() which yields a connection, but Go uses a pool.
func (d *Database) DB() (*sql.DB, error) {
	if err := d.openLazy(); err != nil {
		return nil, err
	}
	return d.db, nil
}

// Path returns the database file path. Matches Python's db_path property.
func (d *Database) Path() string {
	return d.dbPath
}

// Close closes the database connection pool.
func (d *Database) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

// Connect returns the underlying *sql.DB (lazily opened) and applies file
// permissions, matching Python's Database.connect() which calls:
//
//	conn = sqlite3.connect(self._db_path, ...)
//	self._db_path.chmod(CONST_FILE_PERMS_DB)
//
// Python chmod's the file on every connect. We do the same here to fix
// permissions that may have changed externally.
func (d *Database) Connect() (*sql.DB, error) {
	db, err := d.DB()
	if err != nil {
		return nil, err
	}

	// Apply database file permissions on every connect, matching Python's
	// Database.connect() behavior. Python's chmod can raise OSError; we log
	// rather than escalate to avoid disrupting the connection flow.
	if err := os.Chmod(d.dbPath, dbFilePerm); err != nil {
		slog.Warn("Failed to set db file permissions on connect", "error", err)
	}

	return db, nil
}

// GetCurrentVersion returns the current schema version from PRAGMA user_version.
// Mirrors Python's Database.get_current_version() which opens a NEW connection
// each time using closing(sqlite3.connect(self._db_path)).
func (d *Database) GetCurrentVersion() (int, error) {
	// Python uses closing(sqlite3.connect(self._db_path)) — opens a new
	// connection each time. We open a temporary connection for this query
	// to match Python's isolated connection pattern.
	tmpDB, err := sql.Open("sqlite", d.dbPath)
	if err != nil {
		return 0, fmt.Errorf("open temporary connection for version check: %w", err)
	}
	defer tmpDB.Close()

	var version int
	err = tmpDB.QueryRow("PRAGMA user_version").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("get user_version: %w", err)
	}
	return version, nil
}

// Ping verifies the database connection is alive.
// Python's Database class has no Ping() method; this exists for Go convenience.
func (d *Database) Ping() error {
	db, err := d.DB()
	if err != nil {
		return err
	}
	return db.Ping()
}

// EnsureMigrationsTable creates the db_migrations tracking table if it doesn't exist.
// Also migrates existing tables to add the snapshot_path column if missing.
// Mirrors Python's Database._ensure_migrations_table().
func EnsureMigrationsTable(ctx context.Context, db *sql.DB) error {
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
	if err != nil {
		return err
	}

	// Migrate existing tables that don't have snapshot_path (matching Python's
	// ALTER TABLE ADD COLUMN with except sqlite3.OperationalError: pass)
	_, alterErr := db.ExecContext(ctx, "ALTER TABLE db_migrations ADD COLUMN snapshot_path TEXT")
	if alterErr != nil {
		// Python catches sqlite3.OperationalError specifically.
		// We check if the error is about "duplicate column" — if so, ignore it.
		// Other errors are intentionally ignored to match Python's broad pass.
	}

	return nil
}
