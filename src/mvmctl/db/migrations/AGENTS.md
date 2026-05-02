# mvmctl/db/migrations/ — Database Schema Migrations

**Scope:** SQL-based schema versioning for SQLite mvmdb.db
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** SQL files are the canonical schema; `Database.migrate()` applies them.

## OVERVIEW

SQLite migration system using plain SQL files. The `Database` class in `core/_shared/_db.py` applies versioned schema changes. Each migration runs as a single `executescript()` call. Schema version tracking uses both `PRAGMA user_version` (current version number) and a `db_migrations` table (full history with snapshots for rollback).

## STRUCTURE

```
src/mvmctl/db/migrations/
├── __init__.py              # Package marker (docstring: "Database migration SQL files.")
└── 001_initial_schema.sql   # Full schema: 12 tables + PRAGMA user_version
```

## WHERE TO LOOK

### 001_initial_schema.sql
- Creates all tables: `images`, `kernels`, `binaries`, `networks`, `network_leases`, `vm_instances`, `host_state`, `host_state_changes`, `iptables_rules`, `ssh_keys`, `user_settings`
- Each asset table has `is_default INTEGER` column for default tracking
- Foreign keys link VMs to assets and networks
- The `db_migrations` tracking table is created at runtime by `Database._ensure_migrations_table()` (not in SQL)
- Sets `PRAGMA user_version = 1` at end of file

### core/_shared/_db.py — Database class (migration logic)
- `migrate()` — applies pending SQL files in version order; returns count applied
- `get_current_version()` — returns current schema version from `PRAGMA user_version`
- `get_pending_migrations()` — lists SQL files with version > current, validates no gaps
- `validate_migrations()` — validates migration file sequence without applying
- `rollback(steps=1)` — restores from pre-migration snapshots
- `_extract_version(path)` — parses version from `NNN_description.sql` filename
- `_ensure_migrations_table(conn)` — creates/upgrades `db_migrations` tracking table
- `_take_snapshot(version)` — creates online snapshot before migration (safe with concurrent connections)
- `_restore_from_snapshot(path)` — restores database from snapshot file

### How migrations are triggered
- **API layer** calls `Database().migrate()` during init/host operations:
  - `api/host_operations.py:50` — `Database().migrate()`
  - `api/init_operations.py:51` — `db.migrate()`

## CONVENTIONS

- **SQL Naming:** `NNN_description.sql` (zero-padded version prefix, e.g., `001_initial_schema.sql`)
- **No Down Migrations:** Schema evolves forward only
- **Execution:** Each `.sql` file is executed via `conn.executescript()` (auto-committing)
- **Idempotency:** Migrations use `CREATE TABLE IF NOT EXISTS`
- **Version Tracking:** `PRAGMA user_version` tracks current version; `db_migrations` table records full history (`version`, `name`, `applied_at`, `checksum`, `snapshot_path`)
- **Pre-migration Snapshots:** Before applying any migration with version > 1, `Database.migrate()` takes an online snapshot for rollback support

## ADDING NEW MIGRATIONS

1. Create `002_new_feature.sql` with version prefix in this directory
2. Include `CREATE TABLE IF NOT EXISTS` or `ALTER TABLE` statements
3. Set `PRAGMA user_version = N` at the end (where N is the new version number)
4. `Database.migrate()` auto-detects and applies on next API-initiated migration

## NOTES

- **No Alembic:** Plain SQL keeps dependencies minimal
- **Schema is Source:** The `.sql` file is the canonical schema definition
- **Test Isolation:** Tests use `_setup_database` fixture that runs migrations on `tmp_path`
- **Rollback Support:** `Database.rollback(steps=N)` restores from pre-migration snapshots (snapshots stored as `{db_path}.v{version}.snap` files)
- **Gap Detection:** `get_pending_migrations()` validates that migration versions have no gaps and raises `MigrationError` if any are missing
