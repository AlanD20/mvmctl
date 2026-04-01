# SQLite Migration Implementation Plan

**Status:** Approved  
**Timeline:** ~1 week (5 days)  
**Scope:** SQLite database module with 10 tables, migration system, and full hash generation

---

## ⚠️ BREAKING CHANGE: EXISTING JSON CODE MUST BE REMOVED

The following files and functions that currently handle JSON must be **REPLACED** with the new SQLite approach:

**Files to Remove/Replace:**
- `src/mvmctl/core/metadata.py` — 749 lines of JSON metadata handling
  - Remove: `read_metadata()`, `write_metadata()`, file locking, caching
  - Replace with: MVMDatabase CRUD operations

- `src/mvmctl/core/vm_manager.py` — JSON VM state management
  - Remove: JSON state file operations
  - Replace with: vm_states table operations

- `src/mvmctl/core/network_manager.py` — JSON network/lease handling
  - Remove: JSON lease tracking
  - Replace with: network_leases table operations

- `src/mvmctl/core/config_state.py` — Hybrid JSON state (partial)
  - Review: Some config may stay JSON, migrate asset tracking to SQLite

- `src/mvmctl/utils/id_prefix.py` — Prefix resolution
  - Remove: Entire file (replaced by mvm_db.find_by_id_prefix())

**Migration Scope:**
This is a complete replacement, not an augmentation. The JSON files 
(~/.cache/mvmctl/metadata.json, state.json, etc.) will be **IGNORED** after 
the SQLite migration is implemented.

---

## Table of Contents

1. [Executive Summary](#executive-summary)
2. [Database Schema](#database-schema)
3. [JSON Field Mappings](#json-field-mappings)
4. [Migration System Architecture](#migration-system-architecture)
5. [File Architecture](#file-architecture)
6. [MVMDatabase Class](#mvmdatabase-class)
7. [Full Hash Generation](#full-hash-generation)
8. [Atomic Commit Strategy](#atomic-commit-strategy)
9. [Acceptance Criteria](#acceptance-criteria)

---

## Executive Summary

### Scope

Implement a production-grade SQLite database module for mvmctl that replaces JSON metadata storage with a proper relational database. This is a **clean slate implementation** — no auto-migration from existing JSON files.

**BREAKING CHANGE:** This is a breaking change for the project. The existing JSON metadata files will NOT be migrated. A fresh SQLite database will be created on first run. Previous JSON state will be ignored and not imported.

### Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| **Backend** | SQLite | Replaces JSON metadata AND VM state AND host state; clean slate approach (no auto-migration) |
| **Database File** | `~/.cache/mvmctl/mvmdb.db` | Single file for all data, WAL mode for concurrent access |
| **Locking** | Row-level only | Critical requirement: UPDATE queries must lock individual rows, never entire tables |
| **Constraints** | Minimal CHECK constraints | Only IPv4 and MAC format checks; no enum constraints on status fields |
| **Migrations** | Manual execution | All migrations run via `mvm db migrate` command, no auto-migration |
| **Primary Keys** | 64-char SHA256 | ALL primary keys (images, kernels, binaries, networks, vm_states) are 64-char SHA256 hashes; UI displays first 12 chars only |
| **SQLite Library** | Python stdlib `sqlite3` | No external dependencies, sufficient for CLI tool |

### Timeline

```
Week 1: SQLite Migration (5 days)
  Day 1-2: Schema design
  Day 3-4: Implementation
  Day 5: Testing & verification
```

### Files Created

**New Files:**
- `src/mvmctl/core/mvm_db.py` — MVMDatabase class (cross-cutting, used across application)
- `src/mvmctl/utils/full_hash.py` — Hash generation functions (MOVED from core/)
- `src/mvmctl/db/__init__.py` — Database module exports
- `src/mvmctl/db/migrations/__init__.py` — Migration system exports
- `src/mvmctl/db/migrations/runner.py` — MigrationRunner class
- `src/mvmctl/db/migrations/001_initial_schema.sql` — Initial schema (10 tables)
- `src/mvmctl/db/models.py` — Dataclass models (Image, Kernel, VMState, etc.)
- `tests/unit/db/test_migration_runner.py` — Migration runner tests
- `tests/unit/core/test_full_hash.py` — Hash generation tests
- `tests/unit/core/test_mvm_db.py` — MVMDatabase tests
- `tests/unit/db/test_db_integration.py` — Integration tests

**Modified Files:**
- `src/mvmctl/constants.py` — Add `MVM_DB_FILENAME` constant
- `src/mvmctl/utils/fs.py` — Add `get_mvm_db_path()` function

---

## Utility Functions

### src/mvmctl/utils/fs.py

#### get_mvm_db_path()

```python
def get_mvm_db_path() -> Path:
    """Return path to the SQLite database file.
    
    Returns ~/.cache/mvmctl/mvmdb.db (or $MVM_CACHE_DIR/mvmdb.db).
    
    IMPORTANT: Handles SUDO_USER correctly — when running under sudo,
    returns the invoking user's cache directory (not root's).
    
    Example:
        sudo mvm host init  # Still uses www's cache dir, not root's
    """
```

**Key Behaviors:**
- Returns `Path` object pointing to `mvmdb.db` in the cache directory
- Respects `$MVM_CACHE_DIR` environment variable if set
- Handles privilege escalation: when running with `sudo`, uses the original user's cache directory (via `$SUDO_USER` lookup) rather than root's `/root/.cache`
- Creates parent directories if they don't exist (via caller)

---

## Database Schema

### Database File

**File:** `~/.cache/mvmctl/mvmdb.db` (SQLite database)

**Naming rationale:** Called `mvmdb.db` (not `metadata.db`) because it stores:
- Image/kernel/binary metadata
- VM runtime states
- Host initialization state
- Network leases

This name accommodates future expansion beyond just metadata.

### Schema Overview (10 Tables)

```sql
-- IMAGES: OS image metadata
-- JSON mappings: internal_id → os_slug, filename → path
CREATE TABLE images (
    id TEXT PRIMARY KEY,               -- 64-character SHA256 hash
    os_slug TEXT NOT NULL UNIQUE,      -- e.g., "alpine-3.21" (maps from JSON internal_id)
    os_name TEXT,                      -- e.g., "Alpine Linux 3.21"
    path TEXT NOT NULL,                -- Full path (maps from JSON filename)
    fs_type TEXT,
    fs_uuid TEXT,
    compressed_size INTEGER,
    original_size INTEGER,
    compression_ratio REAL,
    compressed_format TEXT,
    pulled_at TIMESTAMP,
    is_default BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- KERNELS: Firecracker kernel metadata
-- JSON mappings: filename → path, last_modified → updated_at
CREATE TABLE kernels (
    id TEXT PRIMARY KEY,               -- 64-character SHA256 hash
    name TEXT NOT NULL,
    base_name TEXT,
    version TEXT NOT NULL,
    arch TEXT NOT NULL,
    type TEXT,
    path TEXT NOT NULL,                -- Full path (maps from JSON filename)
    is_default BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP  -- Maps from JSON last_modified
);

-- BINARIES: Firecracker binary metadata
-- JSON mappings: package_version → version
CREATE TABLE binaries (
    id TEXT PRIMARY KEY,               -- 64-character SHA256 hash
    name TEXT NOT NULL,                -- "firecracker" or "jailer"
    version TEXT NOT NULL,             -- Maps from JSON package_version
    full_version TEXT,
    ci_version TEXT,
    path TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- BINARY_DEFAULTS: Default binary paths (replaces symlinks)
-- JSON mappings: full_version → version
CREATE TABLE binary_defaults (
    name TEXT PRIMARY KEY,
    version TEXT NOT NULL,             -- Maps from JSON full_version
    path TEXT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- NETWORKS: Named network definitions
-- JSON mappings: cidr → subnet, gateway → ipv4_gateway
CREATE TABLE networks (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    subnet TEXT NOT NULL,              -- Maps from JSON cidr
    bridge TEXT NOT NULL,
    ipv4_gateway TEXT NOT NULL,        -- Maps from JSON gateway
    bridge_active BOOLEAN DEFAULT FALSE,
    nat_gateways TEXT NULL,            -- Comma-separated list of gateway interface names (e.g., "eth0,wlo1")
    nat_enabled BOOLEAN DEFAULT FALSE,
    is_default BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_networks_name ON networks(name);

-- NETWORK_LEASES: IP allocation tracking
-- JSON mappings: vm_name → vm_id (stores ID hash, not name), ip → ipv4
CREATE TABLE network_leases (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    network_id TEXT NOT NULL,
    ipv4 TEXT NOT NULL CHECK(ipv4 GLOB '[0-9]*.[0-9]*.[0-9]*.[0-9]*'),  -- Maps from JSON ip
    vm_id TEXT NULL,                   -- Maps from JSON vm_name (stores ID hash, not name)
    leased_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    expires_at TIMESTAMP NULL,
    UNIQUE(network_id, ipv4),
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE CASCADE,
    FOREIGN KEY (vm_id) REFERENCES vm_states(id) ON DELETE CASCADE
);
CREATE INDEX idx_leases_network ON network_leases(network_id);
CREATE INDEX idx_leases_vm ON network_leases(vm_id);
CREATE INDEX idx_leases_ipv4 ON network_leases(ipv4);

-- VM_STATES: VM runtime state (includes merged config columns)
-- JSON mappings: socket_path → api_socket_path, ip → ipv4, network_name → network_id (stores ID hash)
CREATE TABLE vm_states (
    id TEXT PRIMARY KEY,               -- Full 64-character SHA256 hash
    name TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL,              -- No CHECK constraint - validated in code
    pid INTEGER,                       -- No CHECK constraint
    ipv4 TEXT CHECK(ipv4 IS NULL OR ipv4 GLOB '[0-9]*.[0-9]*.[0-9]*.[0-9]*'),  -- Maps from JSON ip
    mac TEXT CHECK(mac IS NULL OR mac GLOB '[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]:[0-9A-Fa-f][0-9A-Fa-f]'),
    network_id TEXT,                   -- Maps from JSON network_name (stores ID hash, not name)
    tap_device TEXT,
    image_id TEXT,                     -- FK to images(id)
    kernel_id TEXT,                    -- FK to kernels(id)
    binary_id TEXT,                    -- FK to binaries(id)
    api_socket_path TEXT,              -- Maps from JSON socket_path
    console_socket_path TEXT NULL,
    config_path TEXT,
    cloud_init_mode TEXT,              -- No CHECK constraint - validated in code
    nocloud_net_port INTEGER NULL,     -- No CHECK constraint
    nocloud_server_pid INTEGER NULL,   -- No CHECK constraint
    console_relay_pid INTEGER NULL,    -- No CHECK constraint
    exit_code INTEGER NULL,
    vcpu_count INTEGER,                -- Merged from configs table
    mem_size_mib INTEGER,              -- Merged from configs table
    disk_size_mib INTEGER,             -- Root disk size in MiB
    rootfs_path TEXT,                  -- Path to rootfs file (copied to VM state folder)
    rootfs_suffix TEXT,                -- Suffix of rootfs file (.img, .ext4, etc.)
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (network_id) REFERENCES networks(id) ON DELETE RESTRICT,
    FOREIGN KEY (image_id) REFERENCES images(id) ON DELETE RESTRICT,
    FOREIGN KEY (kernel_id) REFERENCES kernels(id) ON DELETE RESTRICT,
    FOREIGN KEY (binary_id) REFERENCES binaries(id) ON DELETE RESTRICT
);

-- HOST_STATE: Host initialization state (singleton)
CREATE TABLE host_state (
    id INTEGER PRIMARY KEY,            -- No CHECK id=1 constraint
    initialized BOOLEAN DEFAULT FALSE,
    mvm_group_created BOOLEAN DEFAULT FALSE,
    sudoers_configured BOOLEAN DEFAULT FALSE,
    default_network_created BOOLEAN DEFAULT FALSE,
    initialized_at TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- HOST_STATE_CHANGES: Tracks host configuration changes
CREATE TABLE host_state_changes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    init_timestamp TIMESTAMP NOT NULL,
    setting TEXT NOT NULL,
    mechanism TEXT NOT NULL,
    original_value TEXT,
    applied_value TEXT NOT NULL,
    reverted BOOLEAN DEFAULT FALSE,
    reverted_at TIMESTAMP,
    revert_mechanism TEXT,
    change_order INTEGER NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(session_id, change_order)
);

-- DB_MIGRATIONS: Migration history tracking (not version tracking)
CREATE TABLE db_migrations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    version INTEGER NOT NULL UNIQUE,
    name TEXT NOT NULL,
    applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    checksum TEXT
);

-- Indexes
CREATE INDEX idx_images_os_slug ON images(os_slug);
CREATE INDEX idx_images_name ON images(os_name);
CREATE INDEX idx_kernels_name ON kernels(name);
CREATE INDEX idx_kernels_version ON kernels(version);
CREATE INDEX idx_binaries_name ON binaries(name);
CREATE INDEX idx_binaries_version ON binaries(version);
CREATE INDEX idx_vm_states_name ON vm_states(name);
CREATE INDEX idx_vm_states_status ON vm_states(status);
CREATE INDEX idx_host_changes_session ON host_state_changes(session_id);
CREATE INDEX idx_host_changes_setting ON host_state_changes(setting);
CREATE INDEX idx_host_changes_reverted ON host_state_changes(reverted);

-- Critical PRAGMAs - must be executed on every connection
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA wal_autocheckpoint = 1000;   -- Auto-checkpoint every 1000 pages (WAL cleanup)
PRAGMA cache_size = -64000;         -- 64MB page cache (negative = kibibytes)
PRAGMA user_version = 1;
```

### CHECK Constraints Policy

**Only these CHECK constraints are allowed:**
- `ipv4 GLOB '[0-9]*.[0-9]*.[0-9]*.[0-9]*'` — IPv4 format validation
- `mac GLOB '[0-9A-Fa-f][0-9A-Fa-f]:...'` — MAC address format validation

**NOT allowed:**
- `CHECK (id = 1)` on host_state
- `CHECK (status IN (...))` on vm_states
- `CHECK (pid IS NULL OR pid > 0)` on vm_states.pid
- `CHECK (cloud_init_mode IN (...))` on vm_states
- Port range CHECKs on nocloud_net_port
- pid > 0 CHECKs on nocloud_server_pid, console_relay_pid
- `CHECK(change_order >= 0)` on host_state_changes
- Any other enum-style CHECK constraints

Validation for status values, cloud-init modes, pid ranges, and change_order should be done in application code for flexibility.

---

## JSON Field Mappings

This section documents how fields from the legacy JSON metadata files map to the new SQLite database columns.

### Overview

The migration from JSON to SQLite involves field name changes for clarity and consistency. This section serves as the authoritative reference for these mappings.

### Images Table Mappings

| JSON Field | DB Column | Notes |
|------------|-----------|-------|
| `internal_id` | `os_slug` | Short identifier like "alpine-3.21" |
| `filename` | `path` | Full filesystem path to the image file |

### Kernels Table Mappings

| JSON Field | DB Column | Notes |
|------------|-----------|-------|
| `filename` | `path` | Full filesystem path to the kernel file |
| `last_modified` | `updated_at` | Timestamp of last modification |

### Binaries Table Mappings

| JSON Field | DB Column | Notes |
|------------|-----------|-------|
| `package_version` | `version` | Package version string |

### Binary Defaults Table Mappings

| JSON Field | DB Column | Notes |
|------------|-----------|-------|
| `full_version` | `version` | Full version identifier for default binary |

### Networks Table Mappings

| JSON Field | DB Column | Notes |
|------------|-----------|-------|
| `cidr` | `subnet` | Network CIDR notation (e.g., "192.168.1.0/24") |
| `gateway` | `ipv4_gateway` | IPv4 gateway address for the network |

### Network Leases Table Mappings

| JSON Field | DB Column | Notes |
|------------|-----------|-------|
| `vm_name` | `vm_id` | Stores VM ID hash (64-char), not the VM name |
| `ip` | `ipv4` | Leased IPv4 address |

### VM States Table Mappings

| JSON Field | DB Column | Notes |
|------------|-----------|-------|
| `socket_path` | `api_socket_path` | Path to Firecracker API socket |
| `ip` | `ipv4` | Assigned IPv4 address |
| `network_name` | `network_id` | Stores network ID hash (64-char), not the name |

### New Fields (No JSON Equivalent)

The following fields are new in the SQLite schema and have no JSON equivalent:

| Table | Column | Purpose |
|-------|--------|---------|
| `vm_states` | `rootfs_path` | Path to rootfs file (copied to VM state folder) |
| `vm_states` | `rootfs_suffix` | Suffix of rootfs file (.img, .ext4, etc.) |
| `vm_states` | `image_id` | Foreign key to images(id) |
| `vm_states` | `kernel_id` | Foreign key to kernels(id) |
| `vm_states` | `binary_id` | Foreign key to binaries(id) |
| `vm_states` | `vcpu_count` | Number of vCPUs (merged from configs) |
| `vm_states` | `mem_size_mib` | Memory size in MiB (merged from configs) |
| `vm_states` | `disk_size_mib` | Root disk size in MiB |

### Foreign Key Relationships

The following foreign key relationships are enforced in the database:

```
vm_states.network_id → networks.id (ON DELETE RESTRICT)
vm_states.image_id → images.id (ON DELETE RESTRICT)
vm_states.kernel_id → kernels.id (ON DELETE RESTRICT)
vm_states.binary_id → binaries.id (ON DELETE RESTRICT)
network_leases.network_id → networks.id (ON DELETE CASCADE)
network_leases.vm_id → vm_states.id (ON DELETE CASCADE)
```

---

### Row-Level Locking Requirement

**CRITICAL IMPERATIVE:** All UPDATE queries must lock individual rows, never entire tables.

**Correct (row-level lock):**
```python
UPDATE vm_states SET status = ? WHERE id = ?
UPDATE networks SET updated_at = ? WHERE id = ?
UPDATE host_state SET initialized = ? WHERE id = 1
```

**Wrong (table-level lock - NEVER DO THIS):**
```python
UPDATE vm_states SET status = ?  -- NO WHERE clause!
UPDATE networks SET nat_gateways = ?   -- NO WHERE clause!
```

---

## Migration System Architecture

### Migration Philosophy

**BREAKING CHANGE:** This migration represents a clean slate. There is NO migration from existing JSON files. A fresh SQLite database will be created on first run, and previous JSON state will be ignored/not imported.

Even though this is a new project with no existing data, we implement a complete migration system from day one. This ensures:
- Schema version tracking from the start
- Clear history of all schema changes
- Easy rollback capability
- Foundation for future migrations

**Key Constraint:** Migrations do NOT auto-run. Users must explicitly run `mvm db migrate` to apply pending migrations.

### Folder Structure

```
src/mvmctl/
├── core/
│   └── mvm_db.py              # MVMDatabase class (cross-cutting)
├── utils/
│   ├── full_hash.py           # Hash generation functions (MOVED from core/)
│   └── ...
└── db/                        # Internal database implementation only
    ├── __init__.py            # Database module exports
    ├── migrations/
    │   ├── __init__.py        # Migration system exports
    │   ├── runner.py          # MigrationRunner class
    │   └── 001_initial_schema.sql
    ├── models.py              # Dataclass models
    └── ...                    # No full_hash.py here
```

**Layer Separation:**
- `core/mvm_db.py` — Used across the entire application (cross-cutting)
- `utils/full_hash.py` — Hash generation functions, used across application (MOVED from core/)
- `db/` — Internal implementation only, no other files use this directly
- `mvm_db.py` is the ONLY file that imports from `db/` folder
- No other file can import from `db/` directly

### Migration Runner Features

1. Tracks current schema version via `PRAGMA user_version`
2. Detects pending migrations by comparing file names to current version
3. Runs migrations in a single transaction (atomic)
4. Records migration history in `db_migrations` table (for audit trail)
5. Supports both SQL file migrations and Python function migrations
6. **NO auto-migration** — all migrations run manually via `mvm db migrate`

### Migration File Format

```sql
-- migrations/001_initial_schema.sql
-- Version: 1
-- Description: Initial schema with 10 tables

-- All CREATE TABLE statements
CREATE TABLE images (...);
CREATE TABLE kernels (...);
-- ... etc

PRAGMA user_version = 1;
```

### Migration File Execution Best Practice

Use `conn.executescript()` for multi-statement SQL files. The sqlite3 module's 
`execute()` doesn't handle multi-statement strings with parameters well.

Example:
```python
with sqlite3.connect(db_path) as conn:
    # Read entire SQL file
    sql = migration_file.read_text()
    # Execute all statements in the file
    conn.executescript(sql)
```

For files with both DDL (CREATE TABLE) and PRAGMA statements, ensure PRAGMAs 
that must be outside transactions (like `PRAGMA journal_mode`) are handled 
appropriately or placed at the end of the script.

### User Migration Workflow

```bash
# Check current status
mvm db status
# Output: Database version: 1, Pending migrations: 2

# Apply migrations explicitly
mvm db migrate
# Output: Applied 2 migrations (002_add_config.sql, 003_add_bridge_active.sql)

# Check status again
mvm db status
# Output: Database version: 3, Up to date
```

### Migration Failure Handling

If a migration fails:
```bash
mvm db migrate
# [ERROR] Migration 004_add_ipv6.sql failed: duplicate column name: ipv6
# [ERROR] Your database is still at version 3
# [ERROR] Please check the migration file or contact support
```

The database remains at the last successfully applied version (atomic transactions).

### Host State Management

The `host_state_changes` table tracks all configuration changes made during `mvm host init`:

- **Initialization**: `mvm host init` performs host state changes and records each change in `host_state_changes`
- **Reset**: `mvm host reset` reverts all changes tracked in `host_state_changes` table
- **Revert Order**: Changes are reverted in reverse order (LIFO - Last In, First Out)
- **Tracking**: Each change includes session_id, setting, mechanism, original_value, applied_value, and change_order

Example workflow:
```bash
# Initialize host (records changes)
sudo mvm host init
# Changes recorded: bridge creation, group creation, sudoers config, etc.

# Reset host (reverts all changes in reverse order)
sudo mvm host reset
# Reverts: sudoers config → group creation → bridge deletion (reverse of init)
```

---

## File Architecture

**Note on id_prefix.py:**
The existing `src/mvmctl/utils/id_prefix.py` is **REPLACED** by `mvm_db.find_by_id_prefix()`.
The new implementation queries the SQLite database for prefix matching rather 
than scanning JSON files. Remove `id_prefix.py` during implementation.

### src/mvmctl/core/mvm_db.py

The MVMDatabase class that is used across the entire application:

```python
"""SQLite-based database for mvmctl.

File: ~/.cache/mvmctl/mvmdb.db

This module is in core/ because it's cross-cutting - used by multiple layers.
All internal DB implementation is in db/ folder.
"""

import sqlite3
from pathlib import Path
from typing import Optional
from contextlib import contextmanager

from mvmctl.db.migrations import MigrationRunner
from mvmctl.db.models import VMState, Image, Kernel
from mvmctl.utils.fs import get_mvm_db_path
from mvmctl.utils.full_hash import generate_full_hash_image, generate_full_hash_kernel


class MVMDatabase:
    """SQLite database manager for mvmctl.
    
    Database file: ~/.cache/mvmctl/mvmdb.db
    
    CRITICAL DESIGN PRINCIPLE:
    All UPDATE operations MUST use WHERE clause on primary key (full_hash)
    to ensure row-level locking only. Table-level locking is forbidden.
    """
    
    def __init__(self, db_path: Optional[Path] = None):
        self.db_path = db_path or get_mvm_db_path()
        self.db_path.parent.mkdir(parents=True, exist_ok=True)
        
        # Run migrations on first initialization
        runner = MigrationRunner(self.db_path, self._get_migrations_dir())
        # Note: Migrations don't auto-run; user must call mvm db migrate
        # This just validates the schema exists
    
    def _get_migrations_dir(self) -> Path:
        """Get migrations directory (relative to package)."""
        import mvmctl.db
        return Path(mvmctl.db.__file__).parent / "migrations"
    
    @contextmanager
    def _connect(self):
        """Context manager for database connections."""
        conn = sqlite3.connect(
            self.db_path,
            check_same_thread=False,
            isolation_level=None
        )
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA foreign_keys = ON")
        conn.execute("PRAGMA busy_timeout = 5000")
        try:
            yield conn
        finally:
            conn.close()
    
    # CRUD methods for all tables...
```

### src/mvmctl/db/__init__.py

```python
"""Database module for mvmctl.

Internal database implementation - only used via core/mvm_db.py
"""

from .migrations import MigrationRunner
from .models import Image, Kernel, Binary, BinaryDefault, Network, NetworkLease, VMState, HostState, HostStateChange

__all__ = [
    "MigrationRunner",
    "Image",
    "Kernel", 
    "Binary",
    "BinaryDefault",
    "Network",
    "NetworkLease",
    "VMState",
    "HostState",
    "HostStateChange",
]
```

### src/mvmctl/db/models.py

Dataclass models for all database tables:

```python
"""Dataclass models for mvmctl database tables.

All models use dataclasses for immutability and type safety.
"""

from dataclasses import dataclass
from typing import Optional


@dataclass
class Image:
    """OS image metadata."""
    id: str                           # 64-character SHA256 hash
    os_slug: str                      # e.g., "alpine-3.21"
    os_name: Optional[str]            # e.g., "Alpine Linux 3.21"
    path: str                         # Full filesystem path
    fs_type: Optional[str]
    fs_uuid: Optional[str]
    compressed_size: Optional[int]
    original_size: Optional[int]
    compression_ratio: Optional[float]
    compressed_format: Optional[str]
    pulled_at: Optional[str]
    is_default: bool
    created_at: str
    updated_at: str


@dataclass
class Kernel:
    """Firecracker kernel metadata."""
    id: str                           # 64-character SHA256 hash
    name: str
    base_name: Optional[str]
    version: str
    arch: str
    type: Optional[str]
    path: str                         # Full filesystem path
    is_default: bool
    created_at: str
    updated_at: str


@dataclass
class Binary:
    """Firecracker binary metadata."""
    id: str                           # 64-character SHA256 hash
    name: str                         # "firecracker" or "jailer"
    version: str
    full_version: Optional[str]
    ci_version: Optional[str]
    path: str
    created_at: str
    updated_at: str


@dataclass
class BinaryDefault:
    """Default binary paths (replaces symlinks)."""
    name: str                         # "firecracker" or "jailer"
    version: str
    path: str
    updated_at: str


@dataclass
class Network:
    """Named network definition."""
    id: str                           # 64-character SHA256 hash
    name: str
    subnet: str                       # CIDR notation
    bridge: str
    ipv4_gateway: str
    bridge_active: bool
    nat_gateways: Optional[str]
    nat_enabled: bool
    is_default: bool
    created_at: str
    updated_at: str


@dataclass
class NetworkLease:
    """IP allocation tracking."""
    id: int                           # Auto-increment
    network_id: str                   # FK to networks.id
    ipv4: str
    vm_id: Optional[str]              # FK to vm_states.id (stores ID hash)
    leased_at: str
    expires_at: Optional[str]


@dataclass
class VMState:
    """VM runtime state (includes merged config columns)."""
    id: str                           # 64-character SHA256 hash
    name: str
    status: str                       # No enum constraint - validated in code
    pid: Optional[int]
    ipv4: Optional[str]
    mac: Optional[str]
    network_id: Optional[str]         # FK to networks.id (stores ID hash)
    tap_device: Optional[str]
    image_id: Optional[str]           # FK to images.id
    kernel_id: Optional[str]          # FK to kernels.id
    binary_id: Optional[str]           # FK to binaries.id
    api_socket_path: Optional[str]
    console_socket_path: Optional[str]
    config_path: Optional[str]
    cloud_init_mode: Optional[str]    # No enum constraint - validated in code
    nocloud_net_port: Optional[int]
    nocloud_server_pid: Optional[int]
    console_relay_pid: Optional[int]
    exit_code: Optional[int]
    vcpu_count: Optional[int]         # Merged from configs table
    mem_size_mib: Optional[int]       # Merged from configs table
    disk_size_mib: Optional[int]     # Root disk size in MiB
    rootfs_path: Optional[str]       # Path to rootfs file
    rootfs_suffix: Optional[str]     # Suffix of rootfs file
    created_at: str
    updated_at: str


@dataclass
class HostState:
    """Host initialization state (singleton).
    
    This is a singleton table - always has exactly one row with id=1.
    Tracks whether the host has been initialized and which components
    have been configured.
    """
    id: int                           # Always 1
    initialized: bool
    mvm_group_created: bool
    sudoers_configured: bool
    default_network_created: bool
    initialized_at: Optional[str]
    updated_at: str


@dataclass
class HostStateChange:
    """Tracks host configuration changes for reset functionality.
    
    Each change made during `mvm host init` is recorded here to enable
    `mvm host reset` to revert changes in reverse order (LIFO).
    """
    id: int                           # Auto-increment
    session_id: str                   # Unique session identifier
    init_timestamp: str               # When the init session started
    setting: str                      # What was changed (e.g., "bridge", "group")
    mechanism: str                    # How it was changed (e.g., "ip link add")
    original_value: Optional[str]    # Value before change (for restoration)
    applied_value: str               # Value that was applied
    reverted: bool                   # Whether this change has been reverted
    reverted_at: Optional[str]       # When reverted (if applicable)
    revert_mechanism: Optional[str]   # How it was reverted
    change_order: int                 # Order in which change was applied (for LIFO)
    created_at: str                   # When this record was created
```

### src/mvmctl/db/migrations/runner.py

See the full MigrationRunner class implementation in the original plan.
Key features:
- Version detection via PRAGMA user_version
- Pending migration detection
- Atomic transaction execution
- Migration history tracking in db_migrations table
- No auto-migration (manual only)

### src/mvmctl/utils/full_hash.py

```python
"""Full hash generation for database primary keys.

All assets (images, kernels, binaries, VMs, networks) use 64-character SHA256 hashes
as their primary keys in the database.
"""

import hashlib
from pathlib import Path
from typing import Optional


def generate_full_hash_image(
    file_path: Path,
    os_slug: str,
    timestamp: Optional[str] = None
) -> str:
    """Generate 64-character SHA256 hash for image.
    
    Hash includes:
    - File content hash (SHA256 of file)
    - OS slug (e.g., "alpine-3.21")
    - Timestamp (optional, for uniqueness)
    
    Returns:
        64-character hexadecimal SHA256 hash
    """
    file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
    data = f"{file_hash}:{os_slug}:{timestamp or ''}"
    return hashlib.sha256(data.encode()).hexdigest()


def generate_full_hash_kernel(
    file_path: Path,
    version: str,
    arch: str
) -> str:
    """Generate 64-character SHA256 hash for kernel.
    
    Hash includes:
    - File content hash
    - Version string
    - Architecture
    """
    file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
    data = f"{file_hash}:{version}:{arch}"
    return hashlib.sha256(data.encode()).hexdigest()


def generate_full_hash_binary(
    file_path: Path,
    name: str,
    version: str
) -> str:
    """Generate 64-character SHA256 hash for binary."""
    file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
    data = f"{file_hash}:{name}:{version}"
    return hashlib.sha256(data.encode()).hexdigest()


def generate_full_hash_vm(
    name: str,
    image_id: str,
    kernel_id: str,
    created_at: str
) -> str:
    """Generate 64-character SHA256 hash for VM.
    
    VM hash is based on:
    - VM name
    - Image ID (full hash)
    - Kernel ID (full hash)
    - Creation timestamp
    """
    data = f"{name}:{image_id}:{kernel_id}:{created_at}"
    return hashlib.sha256(data.encode()).hexdigest()


def generate_full_hash_network(
    name: str,
    subnet: str,
    created_at: str
) -> str:
    """Generate 64-character SHA256 hash for network."""
    data = f"{name}:{subnet}:{created_at}"
    return hashlib.sha256(data.encode()).hexdigest()
```

---

## MVMDatabase Class

### Key Design Principles

1. **Row-level locking only** — All UPDATE operations use WHERE clause on primary key
2. **WAL mode** — Write-Ahead Logging for concurrent read/write performance
3. **Foreign key enforcement** — PRAGMA foreign_keys = ON on every connection
4. **Parameterized queries** — All queries use parameters to prevent SQL injection
5. **Context managers** — Database connections use context managers for cleanup

### SQLite Library Choice

We use Python's standard library `sqlite3` module:

- **No external dependencies** — `sqlite3` is part of Python stdlib (included with Python)
- **Sufficient for CLI tool** — No async requirements, so no need for `aiosqlite`
- **No ORM needed** — Direct SQL with parameterized queries is cleaner for our use case
- **No SQLAlchemy** — Avoids heavy ORM dependency for simple CRUD operations

The `sqlite3` module provides everything needed:
- Connection pooling via context managers
- Parameterized query support (`?` placeholders)
- Row factory for dict-like access (`sqlite3.Row`)
- Transaction support (implicit with `isolation_level=None`)
- PRAGMA execution for tuning (WAL mode, busy timeout, etc.)

### Transaction Handling

The MVMDatabase class uses `isolation_level=None` which enables **autocommit mode** by default. This has important implications for transaction handling:

**Autocommit Mode (Default):**
- Each individual SQL statement is automatically committed
- Single-row operations (INSERT, UPDATE, DELETE with single row) can use autocommit
- Simpler code for simple operations

**Explicit Transactions (Required for Multi-Row Operations):**
Multi-row operations that require atomicity must use explicit BEGIN/COMMIT:

```python
# Example: acquire_lease() needs atomic check-and-insert
with self._connect() as conn:
    conn.execute("BEGIN")
    # Check if IP is available
    existing = conn.execute(
        "SELECT id FROM network_leases WHERE network_id = ? AND ipv4 = ?",
        (network_id, ipv4)
    ).fetchone()
    if existing:
        conn.execute("ROLLBACK")
        raise LeaseAlreadyExistsError()
    # Insert the lease
    conn.execute(
        "INSERT INTO network_leases (network_id, ipv4, vm_id) VALUES (?, ?, ?)",
        (network_id, ipv4, vm_id)
    )
    conn.execute("COMMIT")

# Example: revert_host_changes() needs batch update
with self._connect() as conn:
    conn.execute("BEGIN")
    changes = conn.execute(
        "SELECT * FROM host_state_changes WHERE session_id = ? ORDER BY change_order DESC",
        (session_id,)
    ).fetchall()
    for change in changes:
        # Revert each change
        conn.execute(
            "UPDATE host_state_changes SET reverted = TRUE, reverted_at = CURRENT_TIMESTAMP WHERE id = ?",
            (change['id'],)
        )
    conn.execute("COMMIT")
```

**Transaction Guidelines:**
- Use autocommit for single-row operations (simpler, sufficient)
- Use explicit BEGIN/COMMIT for multi-row operations requiring atomicity
- Always use ROLLBACK on error before raising exceptions
- Keep transactions short to avoid locking issues

### CRUD Operations

The MVMDatabase class provides CRUD operations for all 10 tables:

**Image Operations:**
- `get_image()`, `set_image()`, `list_images()`, `delete_image()`
- `find_image_by_prefix(prefix)` — Find image by partial hash

**Kernel Operations:**
- `get_kernel()`, `set_kernel()`, `list_kernels()`, `delete_kernel()`
- `find_kernel_by_prefix(prefix)` — Find kernel by partial hash

**Binary Operations:**
- `get_binary()`, `set_binary()`, `list_binaries()`, `delete_binary()`
- `find_binary_by_prefix(prefix)` — Find binary by partial hash

**Binary Defaults Operations:**
- `get_binary_default()`, `set_binary_default()`, `list_binary_defaults()`, `delete_binary_default()`

**Network Operations:**
- `get_network()`, `create_network()`, `delete_network()`, `list_networks()`
- `find_network_by_prefix(prefix)` — Find network by partial hash

**Network Lease Operations:**
- `acquire_lease()`, `release_lease()`, `get_leases()`, `get_lease_by_vm()`, `renew_lease()`

**VM State Operations:**
- `get_vm_state()`, `set_vm_state()`, `list_vm_states()`, `delete_vm_state()`
- `find_vm_by_prefix(prefix)` — Find VM by partial hash
- `update_vm_status()` — Update only status (row-level lock)
- `update_vm_rootfs()` — Update rootfs_path and rootfs_suffix (row-level lock)

**Host State Operations:**
- `get_host_state()`, `set_host_initialized()`, `update_host_component()`

**Host State Changes Operations:**
- `get_host_changes()`, `add_host_change()`, `revert_host_changes()` — Reverts in LIFO order

**Database Migration Operations:**
- `get_db_migrations()`, `record_db_migration()`, `get_pending_migrations()`

**Generic Helper:**
- `find_by_id_prefix(prefix, table)` — Find any resource by partial hash

### Example: VM State Operations

```python
def get_vm_state(self, vm_id: str) -> Optional[VMState]:
    """Get VM state by ID."""
    with self._connect() as conn:
        row = conn.execute(
            "SELECT * FROM vm_states WHERE id = ?", (vm_id,)
        ).fetchone()
        if row:
            return VMState(**{k: row[k] for k in row.keys()})
        return None

def update_vm_status(self, vm_id: str, status: str) -> None:
    """Update only VM status.
    
    CRITICAL: Single column update with primary key WHERE clause.
    Locks only this VM row.
    """
    with self._connect() as conn:
        conn.execute("""
            UPDATE vm_states 
            SET status = ?, updated_at = CURRENT_TIMESTAMP
            WHERE id = ?
        """, (status, vm_id))
```

---

## Full Hash Generation

### Hash Algorithm

**ALL primary keys are 64-character SHA256 hashes:**

1. **Images**: `SHA256(file_hash + os_slug + timestamp)`
2. **Kernels**: `SHA256(file_hash + version + arch)`
3. **Binaries**: `SHA256(file_hash + name + version)`
4. **VMs**: `SHA256(name + image_id + kernel_id + created_at)`
5. **Networks**: `SHA256(name + subnet + created_at)`

All primary keys (images, kernels, binaries, networks, vm_states) use 64-character SHA256 hashes.

### Short Hash Display

- Database stores full 64-character hash
- CLI displays shortened version (first 12 characters only)
- Lookup accepts short prefix; `find_by_id_prefix()` does prefix search
- User can enter partial hash (e.g., "fb" or "fbb" or "fbcdb3b23") to find resources

### Partial Hash Lookup

Since primary keys are 64-character SHA256 hashes but the UI only displays the first 12 characters, we need a helper function to match partial hashes:

```python
def find_by_id_prefix(prefix: str, table: str) -> Optional[str]:
    """Find a full 64-character hash by its prefix.
    
    Args:
        prefix: Partial hash (e.g., "fbcdb3b23" or "fb")
        table: Table name to search (images, kernels, binaries, vm_states, networks)
    
    Returns:
        Full 64-character hash if exactly one match found
        None if no match or multiple matches
    """
    # Query: SELECT id FROM {table} WHERE id LIKE ?
    # Parameter: f"{prefix}%"
    # Return single match or None
```

This function allows users to reference resources by partial hashes in the CLI:
- `mvm vm rm --id fb` — finds VM with hash starting with "fb"
- `mvm image info --id fbcdb3b23` — finds image with hash starting with "fbcdb3b23"

The function supports any length prefix - from a single character up to the full 64-character hash.

---

## Primary Key Exceptions

⚠️ **PRIMARY KEY EXCEPTIONS:**

While most tables use 64-char SHA256 hashes, these tables use different PKs:

| Table | Primary Key | Type | Notes |
|-------|-------------|------|-------|
| `binary_defaults` | `name` | TEXT | Binary name like "firecracker" or "jailer" |
| `network_leases` | `id` | INTEGER AUTOINCREMENT | Lease record ID |
| `host_state` | `id` | INTEGER | Always 1, singleton pattern |
| `host_state_changes` | `id` | INTEGER AUTOINCREMENT | Change record ID |
| `db_migrations` | `id` | INTEGER AUTOINCREMENT | Migration record ID |

All other tables (`images`, `kernels`, `binaries`, `networks`, `vm_states`) use 64-character SHA256 hashes as primary keys.

---

## Model Dependencies

This section documents special relationships and constraints for specific dataclasses.

### HostState (Singleton)

The `HostState` dataclass represents a singleton table - there is always exactly one row with `id=1`.

**Characteristics:**
- **Primary Key:** `id` is always 1 (not an auto-increment)
- **Purpose:** Tracks host initialization status across all components
- **Fields:**
  - `initialized` — Whether `mvm host init` has completed successfully
  - `mvm_group_created` — Whether the `mvm` system group exists
  - `sudoers_configured` — Whether sudoers drop-in is configured
  - `default_network_created` — Whether the default bridge network exists
  - `initialized_at` — Timestamp of first successful initialization
  - `updated_at` — Timestamp of last update

**CRUD Operations:**
- `get_host_state()` → Optional[HostState] — Returns the singleton row (id=1) or None if not initialized
- `set_host_initialized()` → None — Sets initialized=True and initialized_at timestamp
- `update_host_component(component, value)` → None — Updates individual component flags

### HostStateChange (Reset Tracking)

The `HostStateChange` dataclass tracks all configuration changes made during `mvm host init` to enable `mvm host reset` functionality.

**Characteristics:**
- **Primary Key:** `id` is auto-increment
- **Purpose:** Enables reverting host configuration changes in reverse order (LIFO)
- **Key Fields:**
  - `session_id` — Unique identifier for each init session (UUID)
  - `init_timestamp` — When the init session started
  - `setting` — What was changed (e.g., "bridge", "group", "sudoers")
  - `mechanism` — How it was changed (e.g., "ip link add", "groupadd")
  - `original_value` — Value before change (for restoration during reset)
  - `applied_value` — Value that was applied
  - `change_order` — Sequential order of changes (1, 2, 3, ...)
  - `reverted` — Whether this change has been reverted
  - `reverted_at` — When reverted (if applicable)
  - `revert_mechanism` — How it was reverted

**Reset Workflow:**
1. `mvm host init` records each change with incrementing `change_order`
2. `mvm host reset` queries changes by `session_id` ordered by `change_order DESC`
3. Changes are reverted in reverse order (LIFO - Last In, First Out)
4. Each reverted change is marked with `reverted=True` and `reverted_at` timestamp

**CRUD Operations:**
- `get_host_changes(session_id=None)` → List[HostStateChange] — Get all changes or filter by session
- `add_host_change(change)` → None — Record a new configuration change
- `revert_host_changes(session_id)` → None — Revert all changes for a session in reverse order

---

## Phase 1 — System Test Infrastructure

### Test Isolation

All database tests must use pytest's `tmp_path` fixture to create temporary 
database files. Never test against `~/.cache/mvmctl/mvmdb.db` directly.

Example:
```python
def test_vm_crud(tmp_path: Path) -> None:
    db_path = tmp_path / "test.db"
    db = MVMDatabase(db_path)
    # ... test operations
```

---

## Atomic Commit Strategy

```
Commit 1: feat(db): add database module structure and migration system
  - src/mvmctl/db/__init__.py
  - src/mvmctl/db/migrations/__init__.py
  - src/mvmctl/db/migrations/runner.py (MigrationRunner class)
  - tests/unit/db/test_migration_runner.py
  - Verify: uv run pytest tests/unit/db/test_migration_runner.py -v

Commit 2: feat(utils): add hash generation functions
  - src/mvmctl/utils/full_hash.py (generate_full_hash_image, generate_full_hash_kernel, etc.)
  - tests/unit/utils/test_full_hash.py
  - Verify: uv run pytest tests/unit/utils/test_full_hash.py -v

Commit 3: feat(db): add initial schema migration (001_initial_schema.sql)
  - src/mvmctl/db/migrations/001_initial_schema.sql (all 10 tables, minimal CHECK constraints)
  - Verify: Migration runs successfully, creates all tables

Commit 4: feat(constants): add MVM_DB_FILENAME and get_mvm_db_path()
  - src/mvmctl/constants.py: add MVM_DB_FILENAME constant
  - src/mvmctl/utils/fs.py: add get_mvm_db_path() function
  - tests/unit/utils/test_fs.py: test new function
  - Verify: uv run pytest tests/unit/utils/test_fs.py -v

Commit 5: feat(db): add dataclass models
  - src/mvmctl/db/models.py (Image, Kernel, Binary, BinaryDefault, Network, NetworkLease, VMState, HostState, HostStateChange dataclasses)
  - tests/unit/db/test_models.py
  - Verify: uv run pytest tests/unit/db/test_models.py -v

Commit 6a: feat(core): add MVMDatabase asset CRUD operations
  - src/mvmctl/core/mvm_db.py (CRUD for images, kernels, binaries, binary_defaults)
  - tests/unit/core/test_mvm_db_assets.py
  - Verify: uv run pytest tests/unit/core/test_mvm_db_assets.py -v

Commit 6b: feat(core): add MVMDatabase VM and network CRUD operations
  - src/mvmctl/core/mvm_db.py (CRUD for vm_states, networks, network_leases)
  - tests/unit/core/test_mvm_db_vms.py
  - Verify: uv run pytest tests/unit/core/test_mvm_db_vms.py -v

Commit 6c: feat(core): add MVMDatabase host state CRUD operations
  - src/mvmctl/core/mvm_db.py (CRUD for host_state, host_state_changes)
  - tests/unit/core/test_mvm_db_host.py
  - Verify: uv run pytest tests/unit/core/test_mvm_db_host.py -v

Commit 7: test(db): add integration tests for database operations
  - tests/unit/db/test_db_integration.py (FK constraints, ON DELETE, etc.)
  - Verify: uv run pytest tests/unit/db/ -v

Commit 8: feat(cli): add db migrate and db status commands
  - src/mvmctl/cli/db.py (migrate and status commands)
  - Verify: mvm db status shows current version
```

---

## Acceptance Criteria

All criteria must be verifiable via shell commands:

```bash
# 1. Database file is created
ls ~/.cache/mvmctl/mvmdb.db

# 2. All 10 tables exist
sqlite3 ~/.cache/mvmctl/mvmdb.db ".tables" | grep -E "images|kernels|binaries|binary_defaults|networks|network_leases|vm_states|host_state|host_state_changes|db_migrations"

# 3. Migration runner works
uv run pytest tests/unit/db/test_migration_runner.py -v

# 4. Hash generation works (64-char SHA256)
uv run pytest tests/unit/utils/test_full_hash.py -v

# 5. All primary keys are 64-character SHA256 hashes
grep -r "64-character\|64 char\|SHA256" src/mvmctl/utils/full_hash.py

# 6. MVMDatabase CRUD works
uv run pytest tests/unit/core/test_mvm_db.py -v

# 7. Foreign key constraints work
uv run pytest tests/unit/db/test_db_integration.py -v

# 8. Row-level locking only (no table locks)
grep -r "UPDATE.*WHERE" src/mvmctl/core/mvm_db.py  # All UPDATEs have WHERE
grep -r "UPDATE.*SET.*=" src/mvmctl/core/mvm_db.py | grep -v "WHERE"  # Should be empty

# 9. Only allowed CHECK constraints exist (ipv4, mac only)
grep -r "CHECK" src/mvmctl/db/migrations/001_initial_schema.sql | grep -v "ipv4 GLOB" | grep -v "mac GLOB"  # Should be empty

# 10. No auto-migration
grep -r "auto.*migrate\|migrate.*auto" src/mvmctl/  # Should be empty

# 11. find_by_id_prefix() function exists
grep -r "find_by_id_prefix\|find.*_by.*prefix" src/mvmctl/core/mvm_db.py

# 12. All unit tests pass
uv run pytest tests/unit/db/ tests/unit/core/test_mvm_db.py -v
```

---

## Summary of Changes Applied

This document has been updated with the following changes:

1. **Added 3 new tables:**
   - `binary_defaults` — Default binary paths (replaces symlinks)
   - `networks` — Named network definitions
   - `network_leases` — IP allocation tracking with proper FK constraints

2. **Removed configs table:**
   - Deleted entire configs table definition
   - Removed all references to configs in other tables

3. **Merged configs columns into vm_states:**
   - Added `vcpu_count INTEGER` to vm_states
   - Added `mem_size_mib INTEGER` to vm_states

4. **Updated CHECK constraints:**
   - **Kept:** `ipv4 GLOB`, `mac GLOB`
   - **Removed:** status enum, pid range, cloud_init_mode enum, port range, pid > 0 checks, change_order >= 0
   - **Removed:** `CHECK (id = 1)` from host_state

5. **Removed migration tracking tables:**
   - Deleted `migration_state` table
   - Deleted `migrations_run` table
   - Using `PRAGMA user_version` for version tracking
   - Kept `db_migrations` table for migration history (audit trail)

6. **Updated table count:** 10 tables (including db_migrations)
   1. images
   2. kernels
   3. binaries
   4. binary_defaults
   5. networks
   6. network_leases
   7. vm_states
   8. host_state
   9. host_state_changes
   10. db_migrations

7. **Updated vm_states foreign keys:**
   - References `networks(id)` ON DELETE RESTRICT
   - References `images(id)` ON DELETE RESTRICT
   - References `kernels(id)` ON DELETE RESTRICT
   - References `binaries(id)` ON DELETE RESTRICT

8. **Updated all documentation sections:**
    - CHECK Constraints Policy (removed change_order CHECK)
    - CRUD operations list (added binary_defaults, networks, network_leases, find_by_id_prefix)
    - File Architecture (moved full_hash.py to utils/)
    - Full Hash Generation (added find_by_prefix documentation, clarified ALL hashes are 64-char)
    - Migration System (added host reset documentation, breaking change notice)
    - Acceptance Criteria (updated for 10 tables, 64-char hash checks, find_by_id_prefix)

9. **Added JSON Field Mappings section:**
    - Documented all JSON→DB field mappings for each table
    - Added table comments showing mappings in schema
    - Documented new fields with no JSON equivalent
    - Verified all FK relationships are documented

10. **Added rootfs fields to vm_states:**
    - `rootfs_path TEXT` — Path to rootfs file
    - `rootfs_suffix TEXT` — Suffix of rootfs file (.img, .ext4, etc.)
    - Added `update_vm_rootfs()` CRUD operation

11. **Added HostState and HostStateChange dataclasses:**
    - `HostState` — Singleton dataclass (id=1) for host initialization state
    - `HostStateChange` — Tracks configuration changes for `mvm host reset` functionality
    - Updated `db/__init__.py` exports to include both dataclasses
    - Added Model Dependencies section documenting singleton pattern and LIFO reset workflow

12. **Updated dataclass list in documentation:**
    - File Architecture section now includes complete models.py with all 9 dataclasses
    - Commit 5 updated to include HostState and HostStateChange
    - CRUD operations list includes all host state operations
