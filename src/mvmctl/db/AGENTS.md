# mvmctl/db/ — SQLite Schema & Migrations

**Scope:** SQLite database schema, migration runner, and ORM dataclasses
**Status:** Canonical source of truth for all binary/kernel/image defaults and state
**Rule:** Schema lives in SQL files; runner applies migrations; models define row dataclasses

## STRUCTURE

```
src/mvmctl/db/
├── __init__.py              # Package marker
├── models.py                # ORM dataclasses: Image, Kernel, Binary, VM, Network, HostState, etc.
└── migrations/
    ├── __init__.py          # Package marker
    ├── 001_initial_schema.sql  # Full schema: binaries, images, kernels, networks, host_state, etc.
    └── runner.py            # Migration runner: apply missing migrations, record in db_migrations
```

## WHERE TO LOOK

| Task | Module | Key entry point |
|------|--------|-----------------|
| Schema definitions | `migrations/001_initial_schema.sql` | CREATE TABLE statements |
| Run migrations | `migrations/runner.py` | `MigrationRunner(db_path).migrate()` |
| ORM dataclasses | `models.py` | `Binary`, `Image`, `Kernel`, `Network`, `HostState`, `HostStateChange`, `DBMigration` |

## SCHEMA OVERVIEW

### Core Tables
| Table | Purpose |
|-------|---------|
| `binaries` | Binary entries (firecracker, jailer) with `name`, `version`, `path`, `is_default` |
| `images` | Image entries with hash, os_slug, internal_id, path, `is_default` |
| `kernels` | Kernel entries with version, path, `is_default` |
| `networks` | Named network configs with subnet, gateway, interface, `is_default` |
| `host_state` | Host state snapshots for rollback |
| `host_state_changes` | Individual changes within a host state snapshot |
| `db_migrations` | Migration tracking: version, name, applied_at |

### Key Constraints
- `binaries(name, version)` — unique composite key
- `images(hash)` — unique; `images(os_slug, is_default)` — partial unique for defaults
- `networks(name)` — unique
- Foreign keys: `host_state_changes.state_id → host_state.id`

## CONVENTIONS

### Migration Runner
```python
from mvmctl.db.migrations.runner import MigrationRunner
runner = MigrationRunner(db_path)
runner.migrate()  # Applies all pending migrations in order
```
- Migrations are numbered SQL files in `migrations/` directory
- Runner reads `db_migrations` table to track applied versions
- Uses `executescript()` for schema changes (auto-commits)
- Records each applied migration in `db_migrations` table

### ORM Dataclasses
All dataclasses in `models.py` are pure containers — no methods with side effects:
```python
@dataclass
class Binary:
    id: int | None
    name: str
    version: str
    path: str
    is_default: bool
    created_at: str | None
    updated_at: str | None
```

## STATE QUERY PREFERENCE

**SQLite is the canonical source of truth for all binary/kernel/image defaults and state.**

When determining which binary/kernel/image is "active" or "default":
1. Query `MVMDatabase` first (e.g. `db.get_default_binary("firecracker")`)
2. Verify the returned path still exists on disk (stale-entry guard)
3. Do NOT read filesystem symlinks to derive state

The `firecracker` symlink in `bin/` is a **side-effect** of `set_active_version()` for shell/script compatibility — it is NOT the source of truth.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Raw `sqlite3.connect()` without explicit close | Use `MVMDatabase` context manager or explicit `conn.close()` |
| Hardcoded SQL in core/ modules | Use schema in `migrations/*.sql` |
| Direct DB writes bypassing `MVMDatabase` | Use `MVMDatabase` methods for all CRUD |
| Reading symlinks for binary state | Query `db.get_default_binary("firecracker")` |
