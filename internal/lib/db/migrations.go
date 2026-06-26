package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"mvmctl/pkg/errs"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationFileRegex matches migration filenames like "001_initial_schema.sql"
// and captures the version number in group 1.
var migrationFileRegex = regexp.MustCompile(`^(\d+)_.*\.sql$`)

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

	var files []migrationFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		matches := migrationFileRegex.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, errs.New(errs.CodeMigrationFailed,
				fmt.Sprintf(
					"Invalid migration filename: %s. Expected format: '{version:03d}_{description}.sql'",
					entry.Name(),
				),
				errs.WithClass(errs.ClassValidation))
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil || version == 0 {
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

	// Validate no version gaps: after sorting, files[i].version must be i+1.
	for i, f := range files {
		if f.version != i+1 {
			return nil, errs.New(errs.CodeMigrationFailed,
				fmt.Sprintf(
					"Missing migration version %d: found version %d in file %s. Versions must be sequential starting from 1.",
					i+1,
					f.version,
					f.name,
				),
				errs.WithClass(errs.ClassValidation))
		}
	}

	return files, nil
}

// takeSnapshot creates an online SQLite backup snapshot before a migration.
// Uses VACUUM INTO to create a transactionally consistent copy.
func (d *Handle) takeSnapshot(version int) (string, error) {
	snapPath := fmt.Sprintf("%s.v%d.snap", d.Path(), version)

	srcDB, err := sql.Open("sqlite", d.Path())
	if err != nil {
		return "", fmt.Errorf("open source db for snapshot: %w", err)
	}
	defer srcDB.Close()

	if _, err := srcDB.Exec(fmt.Sprintf("VACUUM INTO '%s'", snapPath)); err != nil {
		return "", fmt.Errorf("create snapshot via VACUUM INTO: %w", err)
	}

	return snapPath, nil
}

// RunMigrationsCtx runs pending migrations against the database.
//
// Each migration SQL file is executed via ExecContext. No wrapping transaction
// is used — each statement auto-commits individually in SQLite's autocommit mode.
//
// Migration history is recorded in db_migrations table. Schema version is
// managed by each migration SQL file via PRAGMA user_version.
//
// Returns the number of migrations applied (0 if none pending).
func (d *Handle) RunMigrationsCtx(ctx context.Context) (int, error) {
	sqlDB := d.DB()

	// Ensure the tracking table exists
	if err := d.ensureMigrationsTable(ctx); err != nil {
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

	// Get current version from PRAGMA user_version
	var currentVersion int
	if err := sqlDB.QueryRow("PRAGMA user_version").Scan(&currentVersion); err != nil {
		return 0, fmt.Errorf("get current version from user_version: %w", err)
	}

	// Apply pending migrations
	applied := 0
	for _, f := range files {
		if f.version <= currentVersion {
			continue
		}

		// Take online snapshot before migration (for version > 1)
		snapshotPath := ""
		if f.version > 1 {
			snapPath, snapErr := d.takeSnapshot(f.version)
			if snapErr != nil {
				return applied, fmt.Errorf("take snapshot before migration %s: %w", f.name, snapErr)
			}
			snapshotPath = snapPath
		}

		// Execute the migration SQL via ExecContext.
		// No explicit transaction wrapping — each statement auto-commits individually
		// in SQLite's autocommit mode.
		if _, err := sqlDB.ExecContext(ctx, f.sql); err != nil {
			// Migration failed — restore the snapshot taken before it so the
			// database is left in a clean pre-migration state rather than broken.
			if snapshotPath != "" {
				if restoreErr := d.RestoreFromSnapshot(snapshotPath); restoreErr != nil {
					slog.Error("failed to restore snapshot after migration failure",
						"version", f.version, "snapshot", snapshotPath, "error", restoreErr)
				}
			}
			return applied, errs.New(errs.CodeMigrationFailed,
				fmt.Sprintf("Migration %s (version %d) failed: %v", f.name, f.version, err))
		}

		// Record migration with ISO-8601 timestamp.
		// Use Go's zero value (empty string) for missing snapshot path.
		appliedAt := time.Now().Format(time.RFC3339)
		if _, err := sqlDB.ExecContext(ctx,
			"INSERT INTO db_migrations (version, name, applied_at, snapshot_path) VALUES (?, ?, ?, ?)",
			f.version, f.name, appliedAt, snapshotPath,
		); err != nil {
			return applied, errs.New(errs.CodeMigrationFailed,
				fmt.Sprintf("Failed to record migration %s: %v", f.name, err))
		}
		slog.Info("Applied migration", "name", f.name)
		applied++
	}

	return applied, nil
}

// GetPendingMigrations returns the list of migration files that haven't been applied yet.
func (d *Handle) GetPendingMigrations(ctx context.Context) ([]string, error) {
	var currentVersion int
	if err := d.DB().QueryRow("PRAGMA user_version").Scan(&currentVersion); err != nil {
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
