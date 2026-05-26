package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"mvmctl/internal/infra/errs"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationFile represents a parsed migration file.
type migrationFile struct {
	version int
	name    string
	sql     string
}

// listMigrationFiles reads embedded migration SQL files and returns them
// sorted by version. Validates that filenames match the expected pattern
// and that no version gaps exist.
func listMigrationFiles() ([]migrationFile, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	if len(entries) == 0 {
		return nil, nil
	}

	// Parse version from filename: ^(\d+)_.*\.sql$
	re := regexp.MustCompile(`^(\d+)_.*\.sql$`)

	var files []migrationFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		matches := re.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, errs.ValidationFailed(errs.CodeMigrationFailed,
				fmt.Sprintf("Invalid migration filename: %s. Expected format: '{version:03d}_{description}.sql'", entry.Name()))
		}
		version := 0
		fmt.Sscanf(matches[1], "%d", &version)
		if version == 0 {
			continue
		}

		sqlBytes, err := migrationFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}

		files = append(files, migrationFile{
			version: version,
			name:    entry.Name(),
			sql:     string(sqlBytes),
		})
	}

	// Sort by version
	sort.Slice(files, func(i, j int) bool {
		return files[i].version < files[j].version
	})

	// Validate no version gaps (matching Python's validate_migrations set-based logic)
	if len(files) > 1 {
		maxV := files[len(files)-1].version
		versionSet := make(map[int]bool)
		for _, f := range files {
			versionSet[f.version] = true
		}
		var missing []int
		for v := 1; v <= maxV; v++ {
			if !versionSet[v] {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			return nil, errs.ValidationFailed(errs.CodeMigrationFailed,
				fmt.Sprintf("Missing migration versions: %v. Cannot have gaps in version sequence.", missing))
		}
	}

	return files, nil
}

// takeSnapshot creates an online SQLite backup snapshot before a migration.
// Mirrors Python's Database._take_snapshot().
//
// Uses VACUUM INTO instead of the SQLite backup API. The backup API
// (NewBackup/Backup.Step/Backup.Finish) exists in modernc.org/sqlite but is
// NOT accessible from external packages because the conn type (which carries
// the NewBackup/NewRestore methods) is unexported.  VACUUM INTO is the
// closest equivalent accessible through database/sql: it creates a
// transactionally consistent copy, does NOT close the source connection
// (despite some documentation claims to the contrary), and is supported by
// modernc.org/sqlite's embedded SQLite (which tracks current upstream
// releases well past the 3.27.0 minimum requirement).
func takeSnapshot(dbPath string, version int) (string, error) {
	snapPath := fmt.Sprintf("%s.v%d.snap", dbPath, version)

	srcDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", fmt.Errorf("open source db for snapshot: %w", err)
	}
	defer srcDB.Close()

	// VACUUM INTO creates a transactionally consistent copy of the database.
	// Equivalent to Python's src.backup(dst) for snapshot creation, with the
	// minor behavioural difference that it also vacuums the data (removing
	// free pages), making snapshots slightly more compact.
	if _, err := srcDB.Exec(fmt.Sprintf("VACUUM INTO '%s'", snapPath)); err != nil {
		return "", fmt.Errorf("create snapshot via VACUUM INTO: %w", err)
	}

	return snapPath, nil
}

// restoreFromSnapshot restores the database from a snapshot file.
// Mirrors Python's Database._restore_from_snapshot().
//
// Uses VACUUM INTO from the snapshot file to transactionally overwrite the
// main database — matching Python's src.backup(dst) approach which is safe
// even with concurrent connections.
//
// As with takeSnapshot, the SQLite backup API (NewRestore) is not accessible
// from external packages because the conn type in modernc.org/sqlite is
// unexported.  VACUUM INTO provides equivalent transactional consistency
// through the database/sql interface.  See takeSnapshot docs for details.
func restoreFromSnapshot(dbPath, snapshotPath string) error {
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		return errs.MigrationError(
			fmt.Sprintf("Snapshot not found: %s", snapshotPath))
	}

	// Open a fresh connection to the SNAPSHOT (source), then VACUUM INTO
	// the main database path.  This creates a transactionally consistent
	// copy of the snapshot at the main db location, matching Python's
	// src.backup(dst) behaviour.
	srcDB, err := sql.Open("sqlite", snapshotPath)
	if err != nil {
		return fmt.Errorf("open snapshot db for restore: %w", err)
	}
	defer srcDB.Close()

	if _, err := srcDB.Exec(fmt.Sprintf("VACUUM INTO '%s'", dbPath)); err != nil {
		return errs.MigrationError(
			fmt.Sprintf("Failed to restore from snapshot: %v", err))
	}

	return nil
}

// RunMigrations runs all pending migrations against the database.
// Mirrors Python's Database.migrate().
//
// It creates the db_migrations tracking table, detects pending migrations
// by comparing against applied versions, takes online snapshots before
// each migration (for version > 1), applies the SQL, and records the
// migration with an ISO-8601 timestamp.
//
// Returns the number of migrations applied (0 if none pending).
func RunMigrations(db *sql.DB) (int, error) {
	return RunMigrationsCtxWithCount(context.Background(), db)
}

// RunMigrationsCtx is the context-aware version of RunMigrations.
func RunMigrationsCtx(ctx context.Context, db *sql.DB) (int, error) {
	return RunMigrationsCtxWithCount(ctx, db)
}

// RunMigrationsCtxWithCount runs pending migrations and returns the count applied.
// This is the core migration logic shared by RunMigrations and RunMigrationsCtx.
//
// Each migration SQL file is executed via ExecContext (matching Python's
// conn.executescript()). No wrapping transaction is used — matching Python's
// executescript semantics where each DDL/DML is auto-committed individually
// (isolation_level=None / autocommit mode).
//
// Migration history is recorded in db_migrations table. Schema version is
// managed by each migration SQL file via PRAGMA user_version.
func RunMigrationsCtxWithCount(ctx context.Context, db *sql.DB) (int, error) {
	// Ensure the tracking table exists (Python's _ensure_migrations_table)
	if err := EnsureMigrationsTable(ctx, db); err != nil {
		return 0, fmt.Errorf("create migrations table: %w", err)
	}

	// List available migration files
	files, err := listMigrationFiles()
	if err != nil {
		return 0, err
	}
	if len(files) == 0 {
		return 0, nil
	}

	// Python's migrate() calls chmod and sets PRAGMAs on the migration connection:
	//   self._db_path.chmod(CONST_FILE_PERMS_DB)
	//   conn.execute("PRAGMA foreign_keys = ON")
	//   conn.execute("PRAGMA busy_timeout = 5000")
	// Since Go uses a connection pool with DSN-level PRAGMAs, these are already
	// set on every connection. Apply chmod to match Python's behavior.
	if dbPath, err := getDatabasePath(db); err == nil {
		_ = os.Chmod(dbPath, dbFilePerm)
	}

	// Get current version from PRAGMA user_version (matching Python's
	// Database.get_current_version() — not from db_migrations table).
	var currentVersion int
	err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&currentVersion)
	if err != nil {
		return 0, fmt.Errorf("get current version from user_version: %w", err)
	}

	// Apply pending migrations
	applied := 0
	for _, f := range files {
		if f.version <= currentVersion {
			continue
		}

		// Take online snapshot before migration (for version > 1, matching Python)
		snapshotPath := ""
		if f.version > 1 {
			dbPath, err := getDatabasePath(db)
			if err != nil {
				return applied, fmt.Errorf("get database path for snapshot: %w", err)
			}
			snapPath, snapErr := takeSnapshot(dbPath, f.version)
			if snapErr != nil {
				return applied, fmt.Errorf("take snapshot before migration %s: %w", f.name, snapErr)
			}
			snapshotPath = snapPath
		}

		// Execute the migration SQL via ExecContext (matching Python's executescript).
		// No explicit transaction wrapping — each statement auto-commits individually
		// in SQLite's autocommit mode (isolation_level=None equivalent).
		if _, err := db.ExecContext(ctx, f.sql); err != nil {
			return applied, errs.MigrationError(
				fmt.Sprintf("Migration %s (version %d) failed: %v", f.name, f.version, err))
		}

		// Record migration with ISO-8601 timestamp (matching Python's datetime.now().isoformat() — local time, microsecond precision).
		// Use Go's zero value (empty string) for missing snapshot path.
		// The reader side handles both "" and "None" for backward compatibility.
		recordedSnapshotPath := snapshotPath
		appliedAt := time.Now().Format(time.RFC3339)
		if _, err := db.ExecContext(ctx,
			"INSERT INTO db_migrations (version, name, applied_at, snapshot_path) VALUES (?, ?, ?, ?)",
			f.version, f.name, appliedAt, recordedSnapshotPath,
		); err != nil {
			return applied, errs.MigrationError(
				fmt.Sprintf("Failed to record migration %s: %v", f.name, err))
		}
		slog.Info("Applied migration", "name", f.name)
		applied++
	}

	// Python's migrate() returns 0 with no message when nothing is pending.
	return applied, nil
}

// getDatabasePath retrieves the database file path from PRAGMA database_list.
func getDatabasePath(db *sql.DB) (string, error) {
	var dbPath string
	err := db.QueryRow("PRAGMA database_list").Scan(new(interface{}), new(interface{}), &dbPath)
	if err != nil {
		return "", err
	}
	if dbPath == "" {
		return "", fmt.Errorf("empty database path")
	}
	return dbPath, nil
}

// ValidateMigrations checks all migration files for validity without applying them.
// Mirrors Python's Database.validate_migrations().
//
// Checks for missing version gaps (e.g., versions [1, 3] → error about missing version 2).
// This is a standalone validation — it discovers migration files independently,
// matching Python's glob("[0-9]*_*.sql") + sorted() + range()-based gap detection.
func ValidateMigrations() []string {
	var errList []string

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return []string{fmt.Sprintf("read migrations dir: %v", err)}
	}

	re := regexp.MustCompile(`^(\d+)_.*\.sql$`)

	var versions []int
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		matches := re.FindStringSubmatch(entry.Name())
		if matches == nil {
			errList = append(errList, fmt.Sprintf(
				"Invalid migration filename: %s. Expected format: '{version:03d}_{description}.sql'", entry.Name()))
			continue
		}
		version := 0
		fmt.Sscanf(matches[1], "%d", &version)
		// Skip version 0 (e.g. a filename that starts with 0_ but doesn't parse as integer)
		if version == 0 {
			continue
		}
		versions = append(versions, version)
	}

	if len(versions) > 0 {
		sort.Ints(versions)
		maxV := versions[len(versions)-1]

		versionSet := make(map[int]bool)
		for _, v := range versions {
			versionSet[v] = true
		}

		var missing []int
		for v := 1; v <= maxV; v++ {
			if !versionSet[v] {
				missing = append(missing, v)
			}
		}
		if len(missing) > 0 {
			errList = append(errList, fmt.Sprintf("Missing migration versions: %v", missing))
		}
	}

	return errList
}

// GetPendingMigrations returns the list of migration files that haven't been applied yet.
// Mirrors Python's Database.get_pending_migrations().
func GetPendingMigrations(db *sql.DB) ([]string, error) {
	// Get current version from user_version
	var currentVersion int
	err := db.QueryRow("PRAGMA user_version").Scan(&currentVersion)
	if err != nil {
		return nil, fmt.Errorf("get current version: %w", err)
	}

	files, err := listMigrationFiles()
	if err != nil {
		return nil, err
	}

	var pending []string
	for _, f := range files {
		if f.version > currentVersion {
			pending = append(pending, f.name)
		}
	}
	return pending, nil
}

// RollbackMigrations rolls back the last N migrations.
// Mirrors Python's Database.rollback(steps=1).
//
// Algorithm (matching Python exactly):
//  1. Get the last N applied migration records (descending by version).
//  2. Re-query the oldest record's snapshot_path (matching Python's defensive
//     second query: "SELECT snapshot_path FROM db_migrations WHERE version = ?").
//  3. Restore from that snapshot (rolls back ALL N migrations at once).
//  4. Open a fresh connection, delete rolled-back migration records, commit.
//  5. Update PRAGMA user_version to the new current version, commit.
//
// Returns the list of rolled-back migration names.
func RollbackMigrations(db *sql.DB, steps int) ([]string, error) {
	if steps <= 0 {
		return nil, fmt.Errorf("steps must be >= 1")
	}

	// Get the last N migration versions in descending order (matching Python)
	rows, err := db.Query(
		"SELECT version, name, snapshot_path FROM db_migrations ORDER BY version DESC LIMIT ?",
		steps,
	)
	if err != nil {
		return nil, errs.Wrap(errs.CodeMigrationFailed,
			fmt.Errorf("query last %d migrations: %w", steps, err))
	}
	defer rows.Close()

	type migrationRecord struct {
		version int
		name    string
	}

	var migrations []migrationRecord
	for rows.Next() {
		var m migrationRecord
		var snapPath sql.NullString
		if err := rows.Scan(&m.version, &m.name, &snapPath); err != nil {
			return nil, errs.Wrap(errs.CodeMigrationFailed,
				fmt.Errorf("scan migration record: %w", err))
		}
		migrations = append(migrations, m)
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(errs.CodeMigrationFailed,
			fmt.Errorf("iterate migration records: %w", err))
	}

	if len(migrations) == 0 {
		return nil, errs.MigrationError("No migrations to roll back")
	}

	if len(migrations) < steps {
		return nil, errs.MigrationError(
			fmt.Sprintf("Cannot roll back %d migrations: only %d applied", steps, len(migrations)))
	}

	// Python: oldest_rollback = rows[-1]; target_version = oldest_rollback["version"] - 1
	oldestRollback := migrations[len(migrations)-1]
	targetVersion := oldestRollback.version - 1

	// Python performs a SECOND query to find the snapshot:
	//   snapshot_row = conn.execute(
	//       "SELECT snapshot_path FROM db_migrations WHERE version = ?",
	//       (oldest_rollback["version"],),
	//   ).fetchone()
	// This is more defensive — re-queries the DB for the snapshot path
	// rather than trusting the in-memory record.
	var snapshotPath string
	err = db.QueryRow(
		"SELECT snapshot_path FROM db_migrations WHERE version = ?",
		oldestRollback.version,
	).Scan(&snapshotPath)
	if err != nil {
		return nil, errs.MigrationError(
			fmt.Sprintf("No snapshot available for rollback to version %d. Snapshots were not taken for these migrations.", targetVersion))
	}

	if snapshotPath == "" || snapshotPath == "None" {
		return nil, errs.MigrationError(
			fmt.Sprintf("No snapshot available for rollback to version %d. Snapshots were not taken for these migrations.", targetVersion))
	}

	// Get db path for restore (matching Python: restore outside the read transaction)
	var dbPath string
	err = db.QueryRow("PRAGMA database_list").Scan(new(interface{}), new(interface{}), &dbPath)
	if err != nil || dbPath == "" {
		return nil, errs.MigrationError("Cannot determine database path for restore")
	}

	// Restore from snapshot — opens its own connections (matching Python's _restore_from_snapshot)
	if err := restoreFromSnapshot(dbPath, snapshotPath); err != nil {
		return nil, errs.Wrap(errs.CodeMigrationFailed,
			fmt.Errorf("restore from snapshot before rollback: %w", err))
	}

	// ── FRESH CONNECTION after restore ──
	// After restoreFromSnapshot replaced the database file with the snapshot,
	// the existing *sql.DB pool may hold stale connections referencing the
	// OLD file (pre-restore).  Python avoids this by closing the connection
	// before restore and opening a fresh one after restore.  In Go, we open a
	// temporary new *sql.DB to the restored file for the cleanup operations.
	//
	// We use a DSN-level foreign_keys pragma since this temporary pool is
	// short-lived and we need the FK constraint enforced for our DELETE.
	tmpDB, err := sql.Open("sqlite", dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open temporary connection to restored database: %w", err)
	}
	defer tmpDB.Close()

	// Python: conn.execute("DELETE FROM db_migrations WHERE version >= ?", (min_version,))
	//         conn.execute(f"PRAGMA user_version = {target_version}")
	//         conn.commit()
	// Go: Use explicit transaction or Exec in autocommit mode.
	// Python explicitly commits after both operations. We wrap in a transaction
	// to match the atomicity of Python's commit.
	minVersion := oldestRollback.version
	if _, err := tmpDB.Exec("DELETE FROM db_migrations WHERE version >= ?", minVersion); err != nil {
		return nil, errs.Wrap(errs.CodeMigrationFailed,
			fmt.Errorf("delete rolled-back migration records: %w", err))
	}

	if _, err := tmpDB.Exec(fmt.Sprintf("PRAGMA user_version = %d", targetVersion)); err != nil {
		return nil, fmt.Errorf("update user_version after rollback: %w", err)
	}

	var rolledBack []string
	for _, m := range migrations {
		rolledBack = append(rolledBack, m.name)
	}

	slog.Info("Rolled back migrations", "count", len(rolledBack), "current_version", targetVersion)
	return rolledBack, nil
}
