# mvmctl/db/migrations/ — Database Schema Migrations

**Scope:** SQL-based schema versioning for SQLite mvmdb.db
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** SQL files are the canonical schema; runner.py applies them transactionally

## OVERVIEW

SQLite migration system using plain SQL files. MigrationRunner applies versioned schema changes transactionally. Schema version tracking in `_migrations` table.

## STRUCTURE

```
src/mvmctl/db/migrations/
├── __init__.py              # Package marker
├── 001_initial_schema.sql   # Initial schema (images, kernels, binaries, networks, VMs, iptables)
└── runner.py               # MigrationRunner class
```

## WHERE TO LOOK

### 001_initial_schema.sql
- Creates all tables: `images`, `kernels`, `binaries`, `networks`, `vm_instances`, `iptables_rules`
- Each asset table has `is_default INTEGER` column for default tracking
- Foreign keys link VMs to assets and networks
- `_migrations` table tracks applied versions

### runner.py — MigrationRunner
- `run_migrations(db_path)` — applies pending SQL files in version order
- `get_current_version(db_path)` — returns max applied version
- Transactions: each migration runs in a transaction (rollback on error)
- Idempotent: re-running skips already-applied versions

## CONVENTIONS

- **SQL Naming:** `NNN_description.sql` (zero-padded version prefix)
- **No Down Migrations:** Schema evolves forward only
- **Transactions:** Each `.sql` file wrapped in BEGIN/COMMIT by runner
- **Idempotency:** Migrations use `CREATE TABLE IF NOT EXISTS`
- **Version Tracking:** `_migrations(version INTEGER PRIMARY KEY, applied_at TIMESTAMP)`

## ADDING NEW MIGRATIONS

1. Create `002_new_feature.sql` with version prefix
2. Include `CREATE TABLE IF NOT EXISTS` or `ALTER TABLE` statements
3. Runner auto-detects and applies on next `MVMDatabase()` init

## NOTES

- **No Alembic:** Plain SQL keeps dependencies minimal
- **Schema is Source:** The `.sql` file is the canonical schema definition
- **Test Isolation:** Tests use `_setup_database` fixture that runs migrations on tmp_path
