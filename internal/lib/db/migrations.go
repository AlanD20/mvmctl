package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

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

// RunMigrationsCtx atomically applies pending SQL, migration receipts and schema version.
func (d *Handle) RunMigrationsCtx(ctx context.Context) (applied int, runErr error) {
	files, err := listMigrationFiles()
	if err != nil {
		return 0, err
	}
	tx, err := d.DB().BeginTxx(ctx, nil)
	if err != nil {
		return 0, errs.WrapMsg(errs.CodeMigrationFailed, "begin migrations", err)
	}
	defer func() {
		// Rollback is also required after admission refuses an existing database.
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			runErr = errors.Join(runErr, errs.WrapMsg(errs.CodeMigrationFailed, "rollback migrations", err))
		}
	}()
	currentVersion, err := admittedMigrationVersion(ctx, tx, files)
	if err != nil {
		return 0, err
	}
	for _, f := range files[currentVersion:] {
		if _, err := tx.ExecContext(ctx, f.sql); err != nil {
			return 0, errs.WrapMsg(errs.CodeMigrationFailed, "apply migration "+f.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO db_migrations (version, name, applied_at, checksum, snapshot_path) VALUES (?, ?, ?, ?, '')",
			f.version, f.name, time.Now().Format(time.RFC3339), migrationChecksum(f),
		); err != nil {
			return 0, errs.WrapMsg(errs.CodeMigrationFailed, "record migration "+f.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", f.version)); err != nil {
			return 0, errs.WrapMsg(errs.CodeMigrationFailed, "record schema version", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, errs.WrapMsg(errs.CodeMigrationFailed, "commit migrations", err)
	}
	for _, f := range files[currentVersion:] {
		slog.Info("Applied migration", "name", f.name)
	}
	return len(files) - currentVersion, nil
}

// GetPendingMigrations admits the database without modifying its schema or migration history.
func (d *Handle) GetPendingMigrations(ctx context.Context) ([]string, error) {
	files, err := listMigrationFiles()
	if err != nil {
		return nil, err
	}
	currentVersion, err := admittedMigrationVersion(ctx, d.DB(), files)
	if err != nil {
		return nil, err
	}
	var pending []string
	for _, f := range files[currentVersion:] {
		pending = append(pending, f.name)
	}
	return pending, nil
}

func migrationChecksum(f migrationFile) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(f.sql)))
}

// Version numbers alone cannot distinguish the fresh baseline from earlier development databases.
// Receipts are compatibility identity, not an authorization boundary for caller-owned SQLite.
func admittedMigrationVersion(ctx context.Context, query sqlx.QueryerContext, files []migrationFile) (int, error) {
	var version int
	if err := sqlx.GetContext(ctx, query, &version, "PRAGMA user_version"); err != nil {
		return 0, errs.WrapMsg(errs.CodeMigrationFailed, "read schema version", err)
	}
	var objects int
	if err := sqlx.GetContext(ctx, query, &objects,
		"SELECT COUNT(*) FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'"); err != nil {
		return 0, errs.WrapMsg(errs.CodeMigrationFailed, "inspect database schema", err)
	}
	if version == 0 && objects == 0 {
		return 0, nil
	}
	refuse := func() error {
		return errs.New(errs.CodeMigrationFailed,
			"clean installation required: database does not match the v0.3.0 baseline; "+
				"preserve desired artifacts and initialize a fresh state directory",
			errs.WithClass(errs.ClassValidation))
	}
	if version < 1 || version > len(files) {
		return 0, refuse()
	}
	var receipts []struct {
		Version  int    `db:"version"`
		Name     string `db:"name"`
		Checksum string `db:"checksum"`
	}
	if err := sqlx.SelectContext(ctx, query, &receipts,
		"SELECT version, name, COALESCE(checksum, '') AS checksum FROM db_migrations ORDER BY version"); err != nil {
		if ctx.Err() != nil {
			return 0, errs.WrapMsg(errs.CodeMigrationFailed, "read migration history", ctx.Err())
		}
		return 0, refuse()
	}
	if len(receipts) != version {
		return 0, refuse()
	}
	for i, receipt := range receipts {
		f := files[i]
		if receipt.Version != f.version || receipt.Name != f.name || receipt.Checksum != migrationChecksum(f) {
			return 0, refuse()
		}
	}
	return version, nil
}
