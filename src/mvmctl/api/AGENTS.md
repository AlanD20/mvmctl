# mvmctl/api/ — Public API Layer

**Scope:** Stable Python API boundary between CLI and core
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Role:** Add privilege checks; query database for defaults; delegate to `core/`; export with `__all__`

## RESOLUTION LAYER MANDATE (MANDATORY — NO EXCEPTIONS)

| Layer | Resolves | How |
|-------|----------|-----|
| **CLI** | User input + constants-backed defaults | `DEFAULT_*` from `constants.py`. **NO DB queries ever.** |
| **API** | **DB-backed defaults ONLY** | Query SQLite (`MVMDatabase`) when CLI passes `None`. `is_default=1` is canonical. |
| **Core** | **NOTHING** | Receives **ALL explicit values** from API. **NO DB queries. NO defaults.** |

### API Layer: SOLE Database Query Responsibility

**The API layer is the ONLY layer permitted to query the database.** This is absolute:

| Layer | Database Access | Consequence of Violation |
|-------|-----------------|-------------------------|
| **CLI** | **FORBIDDEN** — passes `None` or explicit values only | Architectural breach — CLI is a client, not a data resolver |
| **API** | **REQUIRED** — MUST query DB when CLI passes `None` | API owns the database boundary exclusively |
| **Core** | **FORBIDDEN** — receives explicit values from API | Core operates on explicit inputs only; no hidden DB dependencies |

**API MUST:**
- **Query `MVMDatabase`** to resolve `None` for DB-backed params before calling core
- **Call `check_privileges()`** before any privileged operation
- **Call `_prompt_missing_assets()`** when DB-backed assets are not found
- **Pass ALL params explicitly to core** — **NEVER pass `None` to Core for required params**
- **NOT use `DEFAULT_*` constants** for DB-backed defaults — CLI sends pre-resolved values for those

**DB-backed params API resolves** (when CLI passes `None`):
- `image` → `db.get_default_image()` or `db.get_image_by_os_slug(slug)` → `Path`
- `kernel` → `db.get_default_kernel()` or lookup by version/arch/type → `Path | None`
- `binary` → `db.get_default_binary("firecracker")` → `Path`
- `network` → `db.get_default_network()` or `db.get_network_by_name(name)` → `NetworkConfig`

**Network special case**: If network not found by name but subnet hint available → auto-create (no prompt needed).

**Violation = CI failure.** Enforced by `tests/layer_compliance/test_imports.py` and `tests/layer_compliance/test_privilege.py`.

## STRUCTURE

```
src/mvmctl/api/
├── vms.py       # VM operations: create, remove, list, get, ssh, logs, cleanup
├── assets.py    # Image/kernel/binary operations (391 lines)
├── host.py      # Host init/reset/status/clean + default_cache_dir()
├── network.py   # Network create/remove/list/inspect
├── keys.py      # SSH key add/create/list/remove
├── config.py    # Config get/set/dump
├── vm_config.py # VM config file load/merge/save
├── cache.py     # Cache management API
├── init.py      # Init/onboarding API
└── metadata.py  # Metadata query API
```

## DELEGATION PATTERN

```python
# api/network.py — privilege-checked example
from mvmctl.core.network_manager import create_network as _core_create_network
from mvmctl.core.host_privilege import check_privileges

def create_network(name: str, ...) -> NetworkConfig:
    check_privileges("/usr/sbin/ip")       # ← privilege check HERE, not in CLI
    return _core_create_network(name, ...)

__all__ = ["create_network", "remove_network", ...]
```

Key behaviors:
- Only ops that touch network/host call `check_privileges()` — not all API functions do
- They re-export core functions that need no privilege wrapper unchanged
- Return core's return value directly; never reformat output
- `api/vms.py`: only `cleanup_vms` calls `check_privileges`; `create_vm`, `remove_vm` do NOT
- `api/vm_config.py` has no `__all__` and is not re-exported from `api/__init__.py`

## API → CORE MAPPING

| API function | Core module | Notes |
|---|---|---|
| `vms.create_vm()` | `vm_lifecycle.create_vm()` | direct (no privilege check) |
| `vms.list_vms()` | `vm_manager.VMManager.list_all()` | filters by `include_stopped` |
| `vms.remove_vm()` | `vm_lifecycle.remove_vm()` | direct |
| `vms.ssh_vm()` | `ssh.connect_to_vm()` | direct |
| `assets.fetch_image()` | `image.fetch_image()` | direct pass-through |
| `assets.fetch_binary()` | `binary_manager.fetch_binary()` | direct |
| `assets.build_kernel_pipeline()` | `kernel.build_kernel_pipeline()` | direct |
| `vms.cleanup_vms()` | `vm_manager.VMManager` + `vm_lifecycle` | ONLY vm op with privilege check |
| `network.create_network()` | `network_manager.create_network()` | adds privilege check |
| `network.remove_network()` | `network_manager.remove_network()` | adds privilege check |
| `network.ensure_default_network()` | `network_manager.ensure_default_network()` | direct |
| `host.init_host()` | `host_setup.init_host()` | adds privilege check |
| `vm_config.load_vm_config_file()` | `models/vm_config_file.py` | deserialization only |
| `vm_config.merge_cli_overrides()` | `models/vm_config_file.py` | merges CLI flags into config |

## VM CONFIG FILE (vm_config.py)

`--output-config` and `--import-config` flags in `mvm vm create` are handled here:

```python
base = load_vm_config_file(Path("myvm.json"))
merged = merge_cli_overrides(base, name="override-name", vcpus=4)
save_vm_config_file(config, Path("out.json"))
```

The config file JSON includes a `firecracker_config` key with the Firecracker boot JSON embedded.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Format or print output | Return data; let CLI format |
| Business logic beyond privilege + delegation | Move to `core/` |
| Skip `__all__` | Always declare public surface |
| Import from `cli/` | One-way dependency: `cli` → `api` → `core` |
| **Default values in function parameters** | API receives explicit values from CLI; never use `def func(arg=DEFAULT_VALUE)` |

## DEFAULT VALUE POLICY

The **API layer MUST NOT have default values in function parameters**. All API functions must receive explicit values from the CLI layer. 

### Database Query Responsibility (CRITICAL RULE)

**The API layer is EXCLUSIVELY and SOLELY responsible for all database queries.** No exceptions:

| Layer | Database Access Policy | Violation Consequence |
|-------|----------------------|----------------------|
| **CLI** | **NO database queries** — passes `None` or explicit values | Architectural breach — CLI is a client, not a data resolver |
| **API** | **MUST query database** when CLI passes `None` for DB-backed values | API owns the database boundary exclusively |
| **Core** | **NO database queries** — receives explicit values from API | Core operates on explicit inputs only |

### Database-Backed Defaults Resolution Flow

When a default value lives in the database (e.g., default image, kernel, binary, network), the correct flow is:

```
┌─────────────────────────────────────────────────────────────────┐
│                         CLI Layer                                │
│  1. Parse typer option with default=None                        │
│  2. Pass None to API if user didn't specify                     │
│  3. NO database queries — ever                                  │
└─────────────────────────────────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│                         API Layer                                │
│  1. Receive None from CLI                                       │
│  2. Query MVMDatabase to resolve default                      │
│  3. Pass EXPLICIT value to Core (never None for required)     │
└─────────────────────────────────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│                         Core Layer                               │
│  1. Receive explicit value from API                             │
│  2. Execute business logic                                       │
│  3. NO database queries — ever                                  │
└─────────────────────────────────────────────────────────────────┘
```

### Example: Correct API-to-Core Data Flow

**Step 1: CLI passes None (correct)**
```python
# cli/vm.py (CORRECT)
@app.command()
def create(
    image: Optional[str] = typer.Option(None, "--image"),
    kernel: Optional[str] = typer.Option(None, "--kernel"),
    vcpus: Optional[int] = typer.Option(None, "--vcpus"),  # Constants-backed
):
    # vcpus resolved from constants in CLI, passed explicitly
    defaults = _get_vm_defaults()
    effective_vcpus = vcpus if vcpus is not None else defaults.vcpu_count
    
    # image/kernel are DB-backed — pass None to API for resolution
    create_vm(
        image=image,      # ✅ Passes None directly — API will resolve
        kernel=kernel,    # ✅ Passes None directly — API will resolve
        vcpus=effective_vcpus,  # ✅ Already resolved from constants
    )
```

**Step 2: API queries DB and passes explicit values (correct)**
```python
# api/vms.py (CORRECT)
from mvmctl.core.mvm_db import MVMDatabase
from mvmctl.core.host_privilege import check_privileges

def create_vm(
    image: Optional[str] = None,
    kernel: Optional[str] = None,
    vcpus: int = None,  # ✅ Required param — must receive explicit value
    ...
) -> VMInstance:
    db = MVMDatabase()
    
    # Resolve DB-backed defaults
    if image is None:
        image_entry = db.get_default_image()
        if image_entry is None:
            raise ImageError("No default image set. Run: mvm image fetch <os>")
        image = image_entry[0]  # ✅ Now explicit
    
    if kernel is None:
        kernel_entry = db.get_default_kernel()
        kernel = kernel_entry[0] if kernel_entry else None  # ✅ Now explicit (or None if optional)
    
    # vcpus is required — must NOT be None when reaching Core
    if vcpus is None:
        raise ValueError("vcpus must be provided")  # ✅ API validates required params
    
    # Privilege check before privileged operation
    check_privileges(binary_path)  # ✅ API does privilege check
    
    # Pass EXPLICIT values to Core — never None for required params
    return _core_create_vm(
        image=image,      # ✅ Explicit Path
        kernel=kernel,    # ✅ Explicit Path | None (if optional)
        vcpus=vcpus,      # ✅ Explicit int
        ...
    )
```

**Step 3: Core receives explicit values (correct)**
```python
# core/vm_lifecycle.py (CORRECT)
def create_vm(
    image: Path,        # ✅ Explicit Path — NOT Optional
    kernel: Optional[Path],  # ✅ Explicit Path | None (if optional)
    vcpus: int,         # ✅ Explicit int — NOT Optional
    ...
) -> VMInstance:
    # NO database queries — values are explicit
    # NO default resolution — API already resolved
    # Just execute business logic with provided values
    ...
```

### Example: INCORRECT Patterns (DO NOT USE)

**INCORRECT — CLI resolves database default:**
```python
# cli/vm.py (WRONG)
def _resolve_default_image() -> str | None:
    from mvmctl.api.metadata import get_default_image_entry
    entry = get_default_image_entry()  # ❌ CLI should NOT trigger DB queries
    return entry[0] if entry else None

@app.command()
def create(image: Optional[str] = typer.Option(None, "--image")):
    effective = image or _resolve_default_image()  # ❌ CLI resolving DB default
    create_vm(image=effective)  # Passes resolved value
```

**INCORRECT — API passes None to Core for required param:**
```python
# api/vms.py (WRONG)
def create_vm(image: Optional[str] = None, ...) -> VMInstance:
    # ❌ Forgetting to resolve image before passing to Core
    check_privileges(...)
    return _core_create_vm(image=image, ...)  # ❌ Passing None to Core!

# core/vm_lifecycle.py receives None and fails or behaves unexpectedly
```

**INCORRECT — Core queries database:**
```python
# core/vm_manager.py (WRONG)
def list_all():
    db = MVMDatabase()  # ❌ Core should NOT instantiate MVMDatabase
    vms = db.list_vms()   # ❌ Core should NOT query DB
    ...
```

### What Each Layer Must Do

**The CLI layer is responsible for:**
1. Using `None` as typer option defaults for DB-backed values
2. Resolving `DEFAULT_*` constants for constants-backed values
3. Passing user-provided values OR `None` to API
4. **NEVER querying the database**

**The API layer is responsible for:**
1. **Querying the database** when CLI passes `None` for DB-backed values (image, kernel, binary, network)
2. Adding `check_privileges()` before privileged operations
3. **Passing explicit values to Core** — **NEVER pass `None` to Core for required parameters**
4. Validating that required parameters are resolved before calling Core

**The Core layer receives:**
1. **Explicit values from API** — **never `None` for required parameters**
2. **No database queries** — Core has no DB access except through `mvm_db.py` interface
3. Pure business logic execution on explicit inputs

### Why This Matters

API functions should never receive fallback defaults because:
- It violates the layer boundary (CLI passes `None`, API resolves from DB)
- It creates hidden behavior that bypasses user configuration
- It makes testing harder by introducing implicit state
- It duplicates default logic that should be centralized in API layer

**SQLite (`$MVM_CACHE_DIR/mvmdb.db`) is the canonical source of truth** for all binary/kernel/image/network defaults. The API layer is the **ONLY** layer that should query SQLite.

### Verification Checklist

Before submitting any API change:
- [ ] **NO default values in API function parameters** (e.g., `def func(arg=DEFAULT)` is forbidden)
- [ ] API functions accept `Optional[T]` for DB-backed defaults
- [ ] When CLI passes `None`, API queries database via `mvmctl.core.mvm_db.MVMDatabase`
- [ ] **API never passes `None` to Core for required parameters**
- [ ] API adds `check_privileges()` before privileged operations
- [ ] All database queries happen in API layer, never in CLI or Core

### Enforcement

CI checks will reject PRs containing:
- Default values in API function parameters
- CLI code that queries the database (even via API wrappers)
- Core code that queries the database (except `mvm_db.py`)
- API functions that pass `None` to Core for required parameters

**NO EXCEPTIONS. NO WORKAROUNDS. NO DISCUSSION.**
