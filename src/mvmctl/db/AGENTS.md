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
- `images(os_slug)` — UNIQUE; `is_default` is a plain column (no partial index)
- `networks(name)` — UNIQUE
- `network_leases(network_id, ipv4)` — UNIQUE composite; FK → `networks(id)` + FK → `vm_instances(id)`
- `vm_instances(name)` — UNIQUE
- `db_migrations(version)` — UNIQUE
- Foreign keys enabled via `PRAGMA foreign_keys = ON`

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

**SQLite is the canonical source of truth for all binary/kernel/image/network defaults and state.**

### Layer Responsibility for Database Queries

**CRITICAL: Only the API layer may query the database.** This is an architectural boundary violation if any other layer queries the database:

| Layer | Database Query Policy | Rationale |
|-------|----------------------|-----------|
| **CLI** | **FORBIDDEN** — CLI passes `None` or explicit values to API | CLI is a client layer; DB access is an implementation detail |
| **API** | **REQUIRED** — API queries DB when CLI passes `None` for DB-backed defaults | API owns the database boundary; resolves defaults before calling Core |
| **Core** | **FORBIDDEN** — Core receives explicit values from API | Core operates on business logic only; no DB dependencies |
| **DB** | **DEFINITION ONLY** — Schema and ORM models; no business logic | Database layer provides schema and models only |

### Correct Query Pattern

When determining which binary/kernel/image/network is "active" or "default":

```python
# CORRECT — Only in API layer
from mvmctl.core.mvm_db import MVMDatabase

def get_active_vm_config(image: Optional[str] = None) -> VMConfig:
    # API resolves from database
    if image is None:
        db = MVMDatabase()
        default_image = db.get_default_image()
        if default_image and Path(default_image.path).exists():
            image = default_image.path
        else:
            raise AssetNotFoundError("No default image set")
    
    # Pass explicit value to Core
    return _core_create_vm_config(image=image)
```

### Anti-Patterns

| Forbidden | Why It's Wrong | Correct Approach |
|-----------|----------------|----------------|
| CLI calling `get_default_image_entry()` or any DB query | CLI is a client; DB is implementation detail | CLI passes `None`, API queries DB |
| Core calling `MVMDatabase()` directly | Core should not depend on DB | API queries DB, passes explicit values to Core |
| CLI resolving defaults before calling API | Duplicates logic, bypasses API boundary | API resolves all DB-backed defaults |
| Raw `sqlite3.connect()` without explicit close | Resource leak | Use `MVMDatabase` context manager |
| Hardcoded SQL in core/ modules | Bypasses schema management | Use `MVMDatabase` methods |
| Direct DB writes bypassing `MVMDatabase` | Inconsistent state | Always use `MVMDatabase` class |
| Reading symlinks for binary state | Symlinks are side-effects | Query `db.get_default_binary("firecracker")` |

### SQLite is Canonical

When determining which binary/kernel/image is "active" or "default":
1. Query `MVMDatabase` **in the API layer only** (e.g., `db.get_default_binary("firecracker")`)
2. Verify the returned path still exists on disk (stale-entry guard)
3. Do NOT read filesystem symlinks (`firecracker` → `firecracker-v1.15.0`) to derive state

The `firecracker` symlink in `bin/` is a **side-effect** of `set_active_version()` for shell/script compatibility — it is NOT the source of truth. The symlink may be absent or stale; SQLite `is_default=1` is always authoritative.

### Verification Checklist

Before submitting changes:
- [ ] **NO CLI code imports from `mvmctl.core.mvm_db`**
- [ ] **NO Core code imports from `mvmctl.core.mvm_db`** (unless registering discovered assets)
- [ ] **ONLY API layer creates `MVMDatabase()` instances**
- [ ] All database queries happen in API layer functions
- [ ] API never passes `None` to Core for required DB-backed parameters

### Enforcement

CI checks will reject PRs containing:
- CLI code that imports from `mvmctl.db` or `mvmctl.core.mvm_db`
- Core code that queries the database (except for asset discovery/registration)
- Any layer other than API directly instantiating `MVMDatabase`

**NO EXCEPTIONS. NO WORKAROUNDS. NO DISCUSSION.**
