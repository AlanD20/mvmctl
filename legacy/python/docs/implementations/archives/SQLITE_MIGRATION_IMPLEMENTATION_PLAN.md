# SQLite Migration Implementation Plan

> ## ✅ COMPLETED — This migration was fully implemented.
>
> **Schema:** `src/mvmctl/db/migrations/001_initial_schema.sql` (14 tables)
> **Database class:** `src/mvmctl/core/_shared/_db.py` — `Database` class with connection management and migration support
> **Migration system:** Full migration runner with snapshot-based rollback
>
> The actual implementation went beyond the original plan:
> - Added `volumes`, `ssh_keys`, `user_settings`, and `iptables_rules` tables (14 total vs 10 planned)
> - Migration runner supports rollback via SQLite backup API snapshots
> - Uses `PRAGMA user_version` for version tracking with `db_migrations` table for audit trail
> - File permissions: `CONST_FILE_PERMS_DB = 0o640` applied on every connection

**Status:** ✅ COMPLETED — ALL items implemented  
**Timeline:** Completed  
**Scope:** SQLite database module with 14 tables, migration system, and full hash generation

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

**Result:** ✅ DONE — All JSON metadata files replaced by SQLite. Database file lives at `~/.cache/mvmctl/mvmdb.db`.

---

## Implementation Status Summary

| Section | Status | Notes |
|---------|--------|-------|
| Database Schema (10 tables) | ✅ Expanded to 14 tables | Added `volumes`, `ssh_keys`, `user_settings`, `iptables_rules` |
| Migration System | ✅ Done | `Database.migrate()` in `_shared/_db.py` |
| Migration Runner | ✅ Done with snapshot rollback | SQLite backup API for online snapshots |
| Dataclass Models (9 dataclasses) | ✅ Done | In `db/models.py` originally; Item classes now per-domain in `models/` |
| MVMDatabase CRUD | ✅ Done | Per-repository classes across all domains |
| Hash Generation | ✅ Done | `full_hash.py` in utils |
| Config Domain | ✅ Done | SQLite-backed via `SettingsRepository` |
| CLI Commands | ✅ Done | `mvm init` runs migrations; `mvm config` stores in DB |

The remainder of this document is preserved as an historical record of the migration planning process.
