# mvmctl/db/ — SQLite Schema & Migrations

**Scope:** SQLite database schema, migration runner, and ORM dataclasses
**Status:** Canonical source of truth for all binary/kernel/image defaults and state
**Rule:** Schema lives in SQL files; runner applies migrations; models define row dataclasses

## RESOLUTION LAYER MANDATE (MANDATORY — NO EXCEPTIONS)

**The DB layer is only accessed by the API layer. No exceptions.**

| Layer | DB Access |
|-------|-----------|
| **CLI** | **FORBIDDEN** — never queries SQLite |
| **API** | **REQUIRED** — only layer that instantiates `MVMDatabase` |
| **Core** | **FORBIDDEN** — receives resolved values from API |
| **Models** | **FORBIDDEN** — pure data containers |

**SQLite (`mvmdb.db`) is the canonical source of truth for:**
- Default image (`is_default=1` in `images` table)
- Default kernel (`is_default=1` in `kernels` table)
- Default binary per name (`is_default=1` in `binaries` table)
- Default network (`is_default=1` in `networks` table)

**`metadata.json` is a legacy compatibility shim. Never canonical. Never query it for defaults.**

**Portable reference fields** (used in `VMExportConfig` for export/import — never internal SHA256 IDs):
- Images: `(os_slug, arch)` — unique identifier across environments
- Kernels: `(version, arch, type)` — unique identifier across environments
- Binaries: `(name, version)` — unique identifier across environments
- Networks: `name` — unique identifier (subnet/gateway are hints for auto-recreation)

**Violation = CI failure.** Enforced by `tests/layer_compliance/test_imports.py`.

## STRUCTURE

```
src/mvmctl/db/
├── __init__.py              # Package marker
├── models.py                # ORM dataclasses: IPTablesRule, Image, Kernel, Binary, Network, NetworkLease, VMInstance, HostState, HostStateChange
└── migrations/
    ├── __init__.py          # Package marker
    ├── 001_initial_schema.sql  # Full schema: 9 tables + db_migrations tracking
    └── runner.py            # Migration runner: applies migrations, tracks via PRAGMA user_version
```

## WHERE TO LOOK

| Task | Module | Key entry point |
|------|--------|-----------------|
| Schema definitions | `migrations/001_initial_schema.sql` | 10 CREATE TABLE statements |
| Run migrations | `migrations/runner.py` | `MigrationRunner(db_path).migrate()` |
| ORM dataclasses | `models.py` | `IPTablesRule`, `Image`, `Kernel`, `Binary`, `Network`, `NetworkLease`, `VMInstance`, `HostState`, `HostStateChange` |

## SCHEMA OVERVIEW

### Core Tables
| Table | Purpose |
|-------|---------|
| `binaries` | Binary entries (firecracker, jailer) with `name`, `version`, `path`, `is_default` |
| `images` | Image entries with hash, os_slug, path, arch, `is_default`, `minimum_rootfs_size_mib` |
| `kernels` | Kernel entries with version, path, `is_default` |
| `networks` | Named network configs with subnet, gateway, bridge, `is_default` |
| `network_leases` | IP lease records with network_id, ipv4, vm_id, expiry |
| `vm_instances` | VM runtime state with all config, PIDs, sockets, status |
| `host_state` | Host initialization state (singleton id=1) |
| `host_state_changes` | Host config changes for rollback tracking |
| `iptables_rules` | Tracked iptables rules with parameters, network_id, lifecycle |
| `db_migrations` | Migration tracking: version, name, applied_at |

### Key Constraints
- `images(os_slug)` — UNIQUE
- `networks(name)` — UNIQUE
- `network_leases(network_id, ipv4)` — UNIQUE composite
- `vm_instances(name)` — UNIQUE
- `host_state(id=1)` — Singleton enforced in code
- `host_state_changes(session_id, change_order)` — UNIQUE composite
- `iptables_rules` — Complex unique index on active rules
- `db_migrations(version)` — UNIQUE
- Foreign keys enabled via `PRAGMA foreign_keys = ON`

## KNOWN EXCEPTIONS

These are intentional deviations from the layer architecture:

| File | Deviation | Reason |
|------|-----------|--------|
| `cli/bin.py` | Imports `core/metadata` directly | Asset management needs direct metadata access for bulk operations |
| `core/mvm_db.py` | Implements `MVMDatabase` class | Core module that provides DB access methods (used by API) |
| `core/metadata.py` | Imports `mvmctl.db` for asset registration | Discovers and registers assets in DB during downloads |
| `models/*.py` | Import ORM dataclasses from `mvmctl.db.models` | Domain models extend/reuse DB dataclasses for consistency |

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
- [ ] **NO CLI code imports from `mvmctl.db` or `mvmctl.core.mvm_db`** (except known exceptions)
- [ ] **NO Core code imports from `mvmctl.db`** (except `mvm_db.py` and `metadata.py` for asset registration)
- [ ] **ONLY API layer creates `MVMDatabase()` instances** (except core/mvm_db.py)
- [ ] All database queries happen in API layer functions (or core/mvm_db.py methods called by API)
- [ ] API never passes `None` to Core for required DB-backed parameters
- [ ] Models layer may import DB dataclasses for consistency but should not query DB

### Enforcement

CI checks will reject PRs containing:
- CLI code that imports from `mvmctl.db` or `mvmctl.core.mvm_db`
- Core code that queries the database (except for asset discovery/registration)
- Any layer other than API directly instantiating `MVMDatabase`

**NO EXCEPTIONS. NO WORKAROUNDS. NO DISCUSSION.**
