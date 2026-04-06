# mvmctl/core/ — Business Logic Layer

**Scope:** All subprocess calls, privilege checks, VM lifecycle, network, image, kernel
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** Return data or raise typed exceptions — NEVER format output here

## RESOLUTION LAYER MANDATE (MANDATORY — NO EXCEPTIONS)

| Layer | Resolves | How |
|-------|----------|-----|
| **CLI** | User input + constants-backed defaults | `DEFAULT_*` from `constants.py`. |
| **API** | DB-backed defaults | SQLite queries. `is_default=1` is canonical. |
| **Core** | **NOTHING** | Receives ALL explicit, pre-resolved values. |

### Core Layer Database Enforcement (CRITICAL)

**ONLY `mvm_db.py` is allowed to use `MVMDatabase` in the core layer.**

All other core modules MUST NOT import, instantiate, or use `MVMDatabase`. They must receive explicit values from the API layer.

**Core files that MUST NOT use MVMDatabase:**

| File | Status | Notes |
|------|--------|-------|
| `core/vm_manager.py` | MUST NOT use DB | Receives VM state from API |
| `core/vm_lifecycle.py` | MUST NOT use DB | Receives image/kernel paths from API |
| `core/vm_monitor.py` | MUST NOT use DB | Receives VM data from API |
| `core/network.py` | MUST NOT use DB | Receives network config from API |
| `core/network_manager.py` | MUST NOT use DB | Receives network defaults from API |
| `core/host.py` | MUST NOT use DB | Receives host state from API |
| `core/host_setup.py` | MUST NOT use DB | Receives setup config from API |
| `core/host_privilege.py` | MUST NOT use DB | Receives privilege config from API |
| `core/host_state.py` | MUST NOT use DB | Receives state data from API |
| `core/image.py` | MUST NOT use DB | Receives image paths from API |
| `core/kernel.py` | MUST NOT use DB | Receives kernel paths from API |
| `core/binary_manager.py` | MUST NOT use DB | Receives binary paths from API |
| `core/metadata.py` | MUST NOT use DB | Receives metadata from API |
| `core/config_state.py` | MUST NOT use DB | Receives config from API |
| `core/cache_manager.py` | MUST NOT use DB | Receives cache config from API |
| `core/config_gen.py` | MUST NOT use DB | Receives config values from API |
| `core/firecracker.py` | MUST NOT use DB | Receives socket paths from API |
| `core/firewall.py` | MUST NOT use DB | Receives firewall rules from API |
| `core/ssh.py` | MUST NOT use DB | Receives SSH config from API |
| `core/key_manager.py` | MUST NOT use DB | Receives key paths from API |
| `core/cloud_init.py` | MUST NOT use DB | Receives cloud-init config from API |
| `core/cloud_init_status.py` | MUST NOT use DB | Receives status config from API |
| `core/console.py` | MUST NOT use DB | Receives console config from API |
| `core/download_engine.py` | MUST NOT use DB | Receives download config from API |
| `core/partition_detection.py` | MUST NOT use DB | Receives detection params from API |
| `core/rootfs_injector.py` | MUST NOT use DB | Receives injection config from API |
| `core/logs.py` | MUST NOT use DB | Receives log paths from API |
| `core/config.py` | MUST NOT use DB | Receives config data from API |
| `core/user_config.py` | MUST NOT use DB | Receives user config from API |

**The ONLY exempt file:**

| File | Status | Purpose |
|------|--------|---------|
| `core/mvm_db.py` | **ALLOWED** | Defines `MVMDatabase` class and all DB operations. This is the sole database interface for the entire codebase. |

**Core MUST:**
- Receive `image_path: Path` (not `image: str`) — path resolved by API before calling core
- Receive `kernel_path: Path | None` (not `kernel: str`) — resolved by API
- Receive `firecracker_binary_path: str` — resolved by API
- Have NO default values for operationally significant parameters
- Have NO `Optional[T]` for required params — API guarantees they are always set

**Core MUST NOT:**
- Import or use `MVMDatabase` (except in `mvm_db.py`)
- Call `db.get_default_*()` or any SQLite method
- Use `DEFAULT_*` constants as parameter defaults
- Resolve image/kernel/network names to paths — that is the API layer's job

**The `_resolve_image_path()` function does NOT exist in core.** It was moved to `api/vms.py`.

**Violation = CI failure.** Enforced by `tests/layer_compliance/test_imports.py:test_core_does_not_import_from_db()`.

## STRUCTURE

```
src/mvmctl/core/
├── vm_lifecycle.py      # VM create/start/stop/remove (1782 lines)
├── vm_manager.py        # VM registry; state.json keyed by full 16-char hash (285 lines)
├── vm_monitor.py        # VM reconciliation; detects/cleans orphaned VMs
├── network.py           # Low-level: bridge, TAP, NAT, iptables (1339 lines)
├── network_manager.py   # Named networks with IP lease tracking (890 lines)
├── host.py              # Host orchestration: clean/prune/reset (219 lines)
├── host_setup.py        # Host init: KVM, sysctl, binary checks (403 lines)
├── host_privilege.py    # Group/sudoers management; check_privileges() (331 lines)
├── host_state.py        # Host state snapshots for rollback (232 lines)
├── image.py             # Image download, QCOW2→raw conversion, partition extract (1739 lines)
├── kernel.py            # Kernel fetch (FC CI S3) + build-from-source pipeline (1338 lines)
├── binary_manager.py    # Firecracker/jailer version management (436 lines)
├── mvm_db.py            # SQLite ORM — MVMDatabase class; canonical DB interface (868 lines)
├── metadata.py          # SQLite-backed metadata helpers for images/kernels/binaries (559 lines)
├── config_state.py      # config.json persistence + SQLite-backed default accessors (264 lines)
├── config_gen.py        # Generates Firecracker boot JSON (319 lines)
├── firecracker.py       # HTTP API client for live VM control (298 lines)
├── firewall.py          # iptables nocloud input chain management for cloud-init security
├── ssh.py               # SSH command building + key resolution (211 lines)
├── key_manager.py       # SSH key import/create/registry (557 lines)
├── cloud_init.py        # cloud-init ISO creation (178 lines)
├── cloud_init_status.py # Cloud-init boot status polling/wait logic
├── console.py           # Console relay connection management (connect/disconnect/read/write)
├── download_engine.py   # Unified download engine with temp staging, resume, retry
├── cache_manager.py     # Cache init/prune for VMs, images, kernels, networks
├── partition_detection.py # Root partition detection with weighted heuristics
├── rootfs_injector.py   # Inject cloud-init into rootfs via libguestfs
├── logs.py              # VM log retrieval (149 lines)
├── config.py            # YAML config loading (210 lines)
└── user_config.py       # User-specific config get/set (85 lines)
```

## WHERE TO LOOK

| Task | Module | Key entry point |
|------|--------|-----------------|
| Create VM | `vm_lifecycle.py` | `create_vm()` |
| Resolve image by ID/hash | `vm_lifecycle.py` | `_resolve_image_path()` |
| Remove VM | `vm_lifecycle.py` | `remove_vm()` |
| VM registry (CRUD) | `vm_manager.py` | `VMManager` class |
| Orphaned VM cleanup | `vm_monitor.py` | reconciliation helpers |
| Bridge/TAP/NAT | `network.py` | `setup_bridge()`, `create_tap()`, `setup_nat()` |
| iptables chains | `network.py` | `setup_mvm_chains()`, `teardown_mvm_chains()` |
| Named networks | `network_manager.py` | `create_network()`, `ensure_default_network()` |
| Host init | `host_setup.py` | `init_host()` |
| Privilege check | `host_privilege.py` | `check_privileges(binary_path)` |
| Image download/convert | `image.py` | `fetch_image()`, `import_image()` |
| Kernel fetch/build | `kernel.py` | `download_firecracker_kernel()`, `build_kernel_pipeline()` |
| Firecracker binary | `binary_manager.py` | `fetch_binary()`, `set_active_version()`, `get_binary_path()` |
| Binary default lookup | `mvm_db.py` | `db.get_default_binary("firecracker")` — SQLite is canonical; do NOT read `firecracker` symlink |
| Asset metadata helpers | `metadata.py` | `find_images_by_id_prefix()`, `update_kernel_entry()` |
| SQLite ORM (canonical) | `mvm_db.py` | `MVMDatabase` class — single source for all DB queries |
| Firecracker HTTP API | `firecracker.py` | `FirecrackerClient` |
| iptables nocloud rules | `firewall.py` | nocloud input chain management |
| Console relay | `console.py` | `connect()`, `disconnect()`, `read()`, `write()` |
| Cloud-init status | `cloud_init_status.py` | status polling, wait-for-done |
| Download (resumable) | `download_engine.py` | `DownloadEngine` |
| Cache prune | `cache_manager.py` | `prune_cache()`, `init_cache()` |
| Partition detection | `partition_detection.py` | heuristic root partition detection |
| Cloud-init inject | `rootfs_injector.py` | inject via libguestfs |
| Config dataclass | `config.py` | `MVMConfig`, `load_config()` |

## STATE SCHEMAS

**VM state** (`$MVM_CACHE_DIR/vms/state.json`):
```json
{ "vms": { "<full-16-char-sha256>": { "id": "...", "name": "myvm", "pid": 1234, ... } } }
```
- Key = full 16-char hash generated by `generate_vm_id(name)` at creation
- `VMManager.get(name)` searches by name; `find_by_id_prefix(prefix)` searches by hash prefix
- Migration: old name-keyed state auto-migrates on first load

**Asset metadata** (SQLite `$MVM_CACHE_DIR/mvmdb.db` — canonical; `metadata.json` is a legacy compatibility shim):
- Use `find_images_by_id_prefix(cache_dir, "abc123")` for prefix lookup
- Images downloaded via `mvm image fetch` store `internal_id` to link back to images.yaml
- Exactly one entry per section should carry `is_default: 1` when a default is set

**Config** (`$MVM_CONFIG_DIR/config.json`):
- Image/kernel/binary defaults are SQLite-backed (not stored under `config.json.defaults`)

**Network state** (`$MVM_CACHE_DIR/networks/{name}/config.json` + `leases.json`):
- `NetworkConfig` dataclass persisted per network
- `NetworkLease` list tracks IP → VM mappings

## CONVENTIONS

### Subprocess Handling
```python
try:
    subprocess.run(["ip", "link", "add", ...], capture_output=True, text=True, check=True)
except subprocess.CalledProcessError as e:
    raise NetworkError(f"Bridge creation failed: {e.stderr}") from e
except FileNotFoundError:
    raise NetworkError("'ip' binary not found — install iproute2")
```
- Always list form (not shell string)
- Capture stderr; include in exception message
- Raise typed exception from `mvmctl.exceptions`

### Privilege Checks
```python
from mvmctl.core.host_privilege import check_privileges
check_privileges("/usr/sbin/ip")  # validates mvm group membership
```
Called in `api/` layer before entering core, or explicitly in core for ops needing root.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| `print()` or `console.print()` | Raise exception or return data; let CLI format |
| Hardcoded `"/usr/sbin/ip"` | `PRIVILEGED_BINARIES` from constants |
| `except Exception: pass` | Catch specific type, re-raise as MVMError subclass |
| Large functions (>100 lines) | Extract helpers; early returns to reduce nesting |
| `subprocess.run(..., shell=True)` | Always use list form |
| **Core using `MVMDatabase` or `db.get_default_*()`** | API queries DB, passes explicit values to Core |
| **Default values in function parameters** | Core receives explicit values from API; never use `def func(arg=DEFAULT_VALUE)` |

## DEFAULT VALUE POLICY

The **Core layer MUST NOT have default values in function parameters**. Core functions must operate on explicit values passed from the API layer. 

### Database Query Boundary (CRITICAL RULE)

**Core layer MUST NEVER query the database.** This is a strict architectural boundary:

| Layer | Database Access | Policy |
|-------|----------------|--------|
| **CLI** | NO | Passes `None` or explicit values to API |
| **API** | YES | Queries DB when CLI passes `None` for DB-backed defaults |
| **Core** | **NO** — ABSOLUTELY FORBIDDEN | Receives explicit values from API only |

### Data Flow for Database-Backed Defaults

For values that live in the database (default image, kernel, binary, network):

```
User Input → CLI Layer → API Layer (queries DB) → Core Layer (explicit values)
     ↓            ↓              ↓                        ↓
  --image    image=None    db.get_default_image()   image="/path/to/img"
```

**Core receives explicit values only.** API layer handles all database resolution.

### Example: Correct Data Flow

**API layer (resolves from DB):**
```python
# api/vms.py — API resolves DB defaults
def create_vm(image: Optional[str] = None, ...) -> VMInstance:
    if image is None:
        # API queries database
        db = MVMDatabase()
        default = db.get_default_image()
        if default:
            image = default.path
        else:
            raise AssetNotFoundError("No default image")
    
    # Pass explicit value to Core
    return _core_create_vm(image=image, ...)  # image is NEVER None here
```

**Core layer (explicit values only):**
```python
# core/vm_lifecycle.py — Core receives explicit values
def create_vm(image: str, ...) -> VMInstance:  # str, not Optional[str]
    # image is guaranteed to be set by API layer
    # Core focuses on business logic only
    ...
```

### What Core Layer Receives

Core functions receive:
- **Explicit values from API** — never `None` for required parameters
- **No database queries** — Core has no DB dependencies
- **No default resolution** — API resolves all defaults before calling Core

### Why This Matters

Core functions should never:
- Query the database — this is API's responsibility
- Provide fallback defaults — API resolves all defaults
- Handle `None` for required DB-backed parameters — API ensures values are set

Core layer violations create:
- Hidden dependencies on database state
- Testing difficulties (need to mock DB in Core tests)
- Architectural boundary violations
- Duplicated default logic

### Verification Checklist

Before submitting Core changes:
- [ ] **NO imports from `mvmctl.core.mvm_db`** in any core file except `mvm_db.py` itself
- [ ] **NO `MVMDatabase()` instantiation** in any core file except `mvm_db.py`
- [ ] **NO `db.get_default_*()` calls** in any core file except `mvm_db.py`
- [ ] Function parameters receive explicit values, not `Optional[T]` for required params
- [ ] API layer guarantees required values are set before calling Core
- [ ] Core raises typed exceptions, never returns None for required data

### Enforcement

CI checks will reject PRs containing:
- Core code (except `mvm_db.py`) that imports from `mvmctl.core.mvm_db`
- Core code (except `mvm_db.py`) that instantiates `MVMDatabase()`
- Core code (except `mvm_db.py`) that calls `db.get_default_*()` methods
- Core functions with default parameter values for DB-backed params
- Core functions that handle `None` for required DB-backed parameters

**NO EXCEPTIONS. NO WORKAROUNDS. NO DISCUSSION.**

## KNOWN VIOLATIONS

- `host_privilege.py:check_privileges_interactive()` — interactive messaging in core layer is an intentional exception for privilege setup UX.

## CORE LAYER OUTPUT RULE

The core layer **must not** produce console output. All output formatting belongs in the CLI layer (`cli/`).

**Exception:** `check_privileges_interactive()` in `host_privilege.py` is allowed to print because it's part of the first-time setup wizard (`mvm init`) where immediate user feedback is essential.

## KEY MODULES

### vm_lifecycle.py (1782 lines)
- `_resolve_image_path(image)` — checks all extensions + metadata ID prefix lookup
- `generate_vm_id(name)` — `sha256(name:timestamp).hexdigest()[:16]`
- `create_vm()` — full orchestration: image→rootfs copy, cloud-init, config, network, process, register
- TAP naming: `mvm-{net[:3]}-{vm[:3]}-{rand3}` (15-char Linux IFNAMSIZ limit)

### mvm_db.py (868 lines) — SOLE DB INTERFACE FOR ENTIRE CODEBASE

**This is the ONLY file in the core layer allowed to use `MVMDatabase`.**

All other core modules must receive database-resolved values from the API layer.

- `MVMDatabase` class — single entry point for all SQLite operations
- `get_default_binary(name)` → `BinaryRecord | None`
- `get_default_image()`, `get_default_kernel()`
- `list_binaries()`, `list_images()`, `list_kernels()`
- Do NOT bypass this with raw sqlite3 calls from `core/` or above
- API layer imports from here: `from mvmctl.core.mvm_db import MVMDatabase`

### network_manager.py (890 lines)
- `NetworkConfig` + `NetworkLease` dataclasses; persisted as JSON under `$MVM_CACHE_DIR/networks/`
- Bridge = `mvm-{network_name}` (e.g. `mvm-default`)
- `ensure_default_network()` — idempotent; called at VM create and host init

### kernel.py (1338 lines)
- `fetch_kernel_sha256(version)` — fetches `.sha256` sidecar before download
- `build_kernel_pipeline()` — auto-fetches sha256, downloads tarball, patches config, builds, returns `KernelPipelineResult`
- `download_firecracker_kernel()` — downloads prebuilt from Firecracker CI S3
- Implements config fragments merging and `--clean-build` cache bypassing logic.

### image.py (1739 lines)
- `fetch_image(spec, out, force)` — download + sha256 verify + optional QCOW2 convert
- `import_image(spec, output_dir)` — local file conversion to ext4/btrfs
- `_detect_and_rename_fs(path)` — uses `blkid` to detect FS, renames `.img` → `.ext4` etc.

### metadata.py (559 lines)
- `find_images_by_id_prefix(cache_dir, prefix)` → `list[tuple[str, dict]]` (full_key, meta)
- `find_kernels_by_id_prefix(cache_dir, prefix)` → same
- `update_kernel_entry()`, `update_image_entry()` — upsert by full key
- `MetadataCache` class with LRU cache and TTL for read performance
