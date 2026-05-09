# mvmctl/db/ — SQLite Schema & Migrations

**Scope:** SQLite database schema, migration SQL, and canonical state persistence
**Status:** Schema definitions and migration SQL live here; row models live in `src/mvmctl/models/`
**Rule:** Schema lives in SQL files; migrations are managed by `Database` class in `core/_shared/_db.py`; canonical row dataclasses are in `src/mvmctl/models/`

## RESOLUTION LAYER MANDATE (MANDATORY — NO EXCEPTIONS)

**DB access follows the Repository pattern.** Each core domain owns its data through domain-specific Repository classes. The API layer orchestrates which repositories to call and when.

| Layer | DB Access |
|-------|-----------|
| **CLI** | **FORBIDDEN** — never queries SQLite |
| **API** | **REQUIRED** — resolves DB-backed defaults; orchestrates Core Repository calls |
| **Core** | **VIA REPOSITORY** — `core/{domain}/_repository.py` classes own DB access for their domain using `core/_shared/_db.Database`. Core domains NEVER access DB directly outside their Repository. |
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
└── migrations/
    ├── __init__.py           # Package marker
    └── 001_initial_schema.sql  # Full schema: 12 tables + db_migrations tracking
```

The canonical row models (`VMInstanceItem`, `NetworkItem`, `ImageItem`, `KernelItem`, `BinaryItem`, `HostStateItem`, `IPTablesRuleItem`, etc.) live in `src/mvmctl/models/`.

## WHERE TO LOOK

| Task | Module | Key entry point |
|------|--------|-----------------|
| Schema definitions | `migrations/001_initial_schema.sql` | 12 CREATE TABLE statements |
| Run migrations | `core/_shared/_db.py` | `Database.migrate()` or `Database._run_migrations()` |
| Domain row dataclasses | `src/mvmctl/models/` | `*Item` dataclass per domain |

## SCHEMA OVERVIEW

### Core Tables
| Table | Purpose |
|-------|---------|
| `binaries` | Binary entries (firecracker, jailer) with `name`, `version`, `path`, `is_default` |
| `images` | Image entries with hash, os_slug, path, arch, `is_default`, `minimum_rootfs_size_mib` |
| `kernels` | Kernel entries with version, path, `is_default` |
| `volumes` | Volume entries with name, path, size, backing, vm_id |
| `networks` | Named network configs with subnet, gateway, bridge, `is_default` |
| `network_leases` | IP lease records with network_id, ipv4, vm_id, expiry |
| `vm_instances` | VM runtime state with all config, PIDs, sockets, status |
| `host_state` | Host initialization state (singleton id=1) |
| `host_state_changes` | Host config changes for rollback tracking |
| `iptables_rules` | Tracked iptables rules with parameters, network_id, lifecycle |
| `ssh_keys` | SSH key entries with name, public_key, fingerprint, `is_default` |
| `user_settings` | User-level configuration key/value pairs |
| `db_migrations` | Migration tracking: version, name, applied_at |

### Key Constraints
- `images(os_slug)` — UNIQUE
- `kernels(name)` — UNIQUE
- `binaries(name, version)` — UNIQUE composite
- `volumes(name)` — UNIQUE
- `networks(name)` — UNIQUE
- `network_leases(network_id, ipv4)` — UNIQUE composite
- `vm_instances(name)` — UNIQUE
- `host_state(id=1)` — Singleton enforced in code
- `host_state_changes(session_id, change_order)` — UNIQUE composite
- `iptables_rules` — Complex unique index on active rules
- `ssh_keys(name)` — UNIQUE
- `ssh_keys(fingerprint)` — UNIQUE
- `db_migrations(version)` — UNIQUE
- Foreign keys enabled via `PRAGMA foreign_keys = ON`

## KNOWN EXCEPTIONS

These are intentional deviations from the layer architecture:

| File | Deviation | Reason |
|------|-----------|--------|
| *(No current exceptions)* | | All known deviations have been resolved. |

## CONVENTIONS

### Migration Management

Migrations are managed by the `Database` class in `core/_shared/_db.py`:

```python
from mvmctl.core._shared._db import Database

db = Database()
db.migrate()  # Applies all pending migrations in order
```

- Migrations are numbered SQL files in `migrations/` directory
- `Database.migrate()` reads the `db_migrations` table to track applied versions
- Uses `executescript()` for schema changes (auto-commits)
- Records each applied migration in `db_migrations` table

### Domain Models

The canonical row dataclasses live in `src/mvmctl/models/`, not in `db/`. Each domain model maps to a DB table and follows the `*Item` naming convention:

```python
@dataclass
class BinaryItem:
    id: int | None
    name: str
    version: str
    path: str
    is_default: bool
    created_at: str | None
    updated_at: str | None
```

These are pure containers — no methods with side effects.

## STATE QUERY PREFERENCE

**SQLite is the canonical source of truth for all binary/kernel/image/network defaults and state.**

### Layer Responsibility for Database Queries

**Database access follows the Repository pattern.** Each core domain owns its data through a domain-specific Repository class. The API layer orchestrates these repositories.

| Layer | Database Query Policy | Rationale |
|-------|----------------------|-----------|
| **CLI** | **FORBIDDEN** — CLI passes `None` or explicit values to API | CLI is a client layer; DB access is an implementation detail |
| **API** | **ORCHESTRATES** — API creates `Database()` instances and passes them to Core Resolvers/Repositories | API owns the orchestration boundary; resolves DB-backed defaults before calling Core Controllers |
| **Core** | **VIA REPOSITORY** — Each domain's `_repository.py` accesses DB using `core/_shared/_db.Database` | Core domains own their data persistence; Repository is the single entry point for all queries |
| **DB** | **DEFINITION ONLY** — Schema and migration SQL; no business logic | Database layer provides schema only |

### Correct Query Pattern

Domain-specific repositories (e.g., `VMRepository` in `core/vm/_repository.py`) handle all DB queries:

```python
# CORRECT — Repository in core/{domain}/_repository.py
from mvmctl.core._shared._db import Database
from mvmctl.models.vm import VMInstanceItem

class VMRepository:
    def __init__(self, db: Database | None = None) -> None:
        self._db = db or Database()

    def get(self, vm_id: str) -> VMInstanceItem | None:
        with self._db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM vm_instances WHERE id = ?", (vm_id,)
            ).fetchone()
        if row is None:
            return None
        return VMInstanceItem(**dict(row))
```

API layer orchestrates:
```python
# CORRECT — API layer orchestrates
from mvmctl.core._shared._db import Database
from mvmctl.core.vm._repository import VMRepository

db = Database()
repo = VMRepository(db)
vm = repo.get(vm_id)
```

### Anti-Patterns

| Forbidden | Why It's Wrong | Correct Approach |
|-----------|----------------|----------------|
| CLI calling any DB method directly | CLI is a client; DB is implementation detail | CLI imports from `mvmctl.api` only |
| Core accessing DB outside its Repository | Bypasses domain encapsulation | Use the domain's `_repository.py` |
| CLI resolving defaults before calling API | Duplicates logic, bypasses API boundary | API resolves all DB-backed defaults |
| Raw `sqlite3.connect()` without using `Database` | Bypasses connection management | Use `Database` from `core/_shared/_db.py` |
| Hardcoded SQL outside Repository classes | Scatters query logic | All SQL in `_repository.py` per domain |
| `from __future__ import annotations` absent | Breaks PEP 563 postponed evaluation | Include in every Python file |

### SQLite is Canonical

When determining which binary/kernel/image is "active" or "default":
1. Query through the domain-specific Repository (e.g., `BinaryRepository.get_default()`)
2. Verify the returned path still exists on disk (stale-entry guard)
3. Do NOT read filesystem symlinks (`firecracker` → `firecracker-v1.15.0`) to derive state

The `firecracker` symlink in `bin/` is a **side-effect** of `set_active_version()` for shell/script compatibility — it is NOT the source of truth. The symlink may be absent or stale; SQLite `is_default=1` is always authoritative.

### Verification Checklist

Before submitting changes:
- [ ] **NO CLI code imports from `mvmctl.db` or `mvmctl.core.*`** — CLI only imports from `mvmctl.api`
- [ ] **Core code accesses DB through domain `_repository.py` files** — never directly
- [ ] **Repository classes use `Database` from `core/_shared/_db.py`** — not raw `sqlite3`
- [ ] **API orchestrates but does not duplicate Repository logic** — calls Core Repositories
- [ ] **API never passes `None` to Core Controller for required DB-backed parameters**
- [ ] **Models layer provides canonical row dataclasses (`*Item`) but should not query DB**

### Enforcement

CI checks (`tests/layer_compliance/test_imports.py`) enforce:
- CLI code does NOT import from `core/` directly
- API layer is the only consumer of multiple core modules
- Repository pattern is followed (queries in `_repository.py`, not scattered)
