# mvmctl/api/ — Public API Layer

**Scope:** Stable Python API boundary between CLI and core  
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.  
**Role:** Add privilege checks; query database for defaults; delegate to `core/`; export with `__all__`

---

## STRUCTURE

```
src/mvmctl/api/
├── __init__.py       # Package exports (lazy imports)
├── vm/              # VM lifecycle package: create, remove, list, ssh, logs, cleanup
│   ├── __init__.py  # Package exports
│   ├── create.py    # VM creation orchestration
│   ├── remove.py    # VM removal orchestration
│   ├── lifecycle.py # Start, stop, pause, resume, reboot
│   ├── console.py   # Console attachment and relay management
│   ├── ssh.py       # SSH connection handling
│   ├── logs.py      # Log retrieval
│   ├── snapshot.py  # Snapshot creation and loading
│   ├── inspect.py   # VM inspection and config export
│   ├── list.py      # VM listing
│   └── cleanup.py   # VM cleanup operations
├── network.py       # Network management: create, remove, IP allocation, iptables (1,036 lines)
├── kernel.py        # Kernel fetch/build orchestration (640 lines)
├── assets.py        # Binary/asset management (491 lines)
├── image.py         # Image fetch/import orchestration (478 lines)
├── cache.py         # Cache pruning operations (438 lines)
├── host.py          # Host init/reset/clean/prune (549 lines)
├── vm_config.py     # VM config file handling (328 lines)
├── metadata.py      # Metadata query wrappers (209 lines)
├── network_sync.py  # iptables rule synchronization (201 lines)
├── keys.py          # SSH key management (161 lines)
├── config.py        # Config with DB resolution (129 lines)
└── init.py          # Database initialization (21 lines)
```

**Total: 6,973 lines across 14 modules**

---

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
- **Pass ALL params explicitly to core** — **NEVER pass `None` to Core for required params**
- **NOT use `DEFAULT_*` constants** for DB-backed defaults — CLI sends pre-resolved values for those

**DB-backed params API resolves** (when CLI passes `None`):
- `image` → `db.get_default_image()` or `db.get_image_by_os_slug(slug)` → Image record
- `kernel` → `db.get_default_kernel()` or lookup by version/arch/type → Kernel record
- `binary` → `db.get_default_binary("firecracker")` → Binary record
- `network` → `db.get_default_network()` or `db.get_network_by_name(name)` → NetworkConfig

**Violation = CI failure.** Enforced by `tests/layer_compliance/test_imports.py` and `tests/layer_compliance/test_privilege.py`.

---

## ORCHESTRATION ARCHITECTURE (The Burger Analogy)

Think of the system as a burger:

```
user input → CLI (validate, apply constants defaults)
               ↓
           API Layer (the "bun" — orchestrates everything)
           ├── calls core/network.py (setup network)
           ├── calls core/vm_lifecycle.py (start VM)
           ├── calls core/metadata.py (store metadata)
           ├── calls core/cloud_init.py (write cloud-init)
           └── returns result to CLI
               ↑
           Core Modules (isolated "ingredients")
           Each module does ONE thing, receives explicit inputs,
           does NOT import from other core/ modules.
```

**Key principle**: Core modules are **ISOLATED**. They do not call each other. The **API layer is the ONLY entity** that calls multiple core modules and sequences them together.

### Orchestration Patterns

#### 1. Full Orchestration (vms.create_vm, network.create_network)

```python
# api/vm/ package — orchestration example
def create_vm(input: VMCreateInput, ...) -> VMInstance:
    # 1. Privilege check (API responsibility)
    check_privileges_interactive("/usr/sbin/ip", f"create VM '{name}'")
    
    # 2. DB resolution for defaults (API responsibility)
    if image is None and image_path is None:
        db = MVMDatabase()
        default_image = db.get_default_image()
        ...
    
    # 3. Sequence core modules (isolated ingredients)
    net_config = get_network(network_name)           # api/network
    resolved_image_path = resolve_image_multi_strategy(image)  # api/assets
    
    # 4. Core operations (no cross-core imports)
    setup_nocloud_input_chain()                      # core/firewall
    create_tap(tap_name, bridge=bridge)              # core/network
    
    # 5. Metadata persistence (API responsibility)
    manager.register(vm_instance, binary_id)         # core/vm_manager
    
    return vm_instance
```

#### 2. Simple Delegation (keys, config)

```python
# api/keys.py — delegation example
def add_key(name: str, pub_key_path: str | Path, ...) -> KeyInfo:
    # Validation at API layer
    if not path_obj.exists():
        raise MVMKeyError(f"File not found: {pub_key_path}")
    
    # Direct delegation to core
    result = _core_add_key(name, pub_key_path, overwrite)
    
    # Audit logging (API responsibility)
    log_audit("key.add", f"name={result.name}")
    
    return result
```

#### 3. Atomic DB + System Operations (network iptables)

```python
# api/network.py — atomic operation example
def create_iptables_rule(rule: IPTablesRule, ...) -> IPTablesRule:
    # Step 1: Create iptables rule via Core
    result = tracker.ensure_rule(...)
    
    # Step 2: Write to database (API responsibility)
    try:
        stored_rule = db.record_iptables_rule(result.rule)
    except Exception as e:
        # Rollback: Delete the iptables rule
        rollback_result = tracker.remove_rule(result.rule)
        raise NetworkError(f"Failed to store rule: {e}")
    
    return stored_rule
```

---

## API → CORE MAPPING

### VM Operations (vm/ package)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| `create_vm()` | vm_lifecycle, network, cloud_init, firewall, metadata | **Orchestration** |
| `remove_vm()` | vm_process, network, firewall, mvm_db | **Orchestration** |
| `start_vm()`, `stop_vm()`, `pause_vm()`, `resume_vm()` | vm_process, vm_manager, firecracker | **Delegation** |
| `snapshot_vm()`, `load_snapshot()` | firecracker | **Direct** |
| `ssh_vm()` | ssh | **Direct** |
| `get_logs()` | logs | **Direct** |
| `cleanup_vms()` | vm_manager, network, firewall | **Orchestration** |
| `list_vms()`, `get_vm()` | vm_manager, vm_monitor | **Delegation** |
| `inspect_vm()`, `export_vm_config()` | vm_manager, metadata | **Orchestration** |

### Network Operations (network.py)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| `create_network()`, `remove_network()` | network, network_manager, mvm_db | **Orchestration** |
| `ensure_default_network()`, `restore_networks()` | network, network_manager | **Orchestration** |
| `list_networks()`, `get_network()` | mvm_db | **DB query** |
| `allocate_network_ip()`, `release_network_ip()` | network_manager, mvm_db | **Delegation + DB** |
| `create_iptables_rule()`, `remove_iptables_rule()` | iptables_tracker, mvm_db | **Atomic operation** |
| `sync_iptables_rules()` | iptables_tracker, mvm_db | **Synchronization** |

### Asset Operations (assets.py)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| `fetch_binary()` | binary_manager, metadata, mvm_db | **Orchestration** |
| `register_binary()`, `ensure_default_binary()` | metadata, mvm_db | **DB operations** |
| `get_binary_path()`, `list_local_versions()` | binary_manager, mvm_db | **DB + Core** |
| `set_active_version()`, `remove_version()` | binary_manager, mvm_db | **Orchestration** |
| `resolve_image_*()` | metadata | **Delegation** |

### Image Operations (image.py)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| `fetch_image_and_register()`, `import_image_and_register()` | image, metadata | **Orchestration** |
| `register_fetched_image()` | metadata | **Delegation** |
| `set_default_image()`, `remove_image()` | metadata | **Delegation** |

### Kernel Operations (kernel.py)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| `fetch_kernel()` | kernel | **Orchestration** |
| `register_fetched_kernel()` | metadata, kernel | **Orchestration** |
| `list_kernels()`, `set_default_kernel()` | kernel, metadata | **Delegation** |
| `resolve_kernel_path()`, `remove_kernel()` | metadata, vm_manager | **Delegation** |

### Host Operations (host.py)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| `init_host()` | host_setup, host_privilege, network, mvm_db | **Orchestration** |
| `restore_host()`, `clean_host()`, `reset_host()`, `prune_host()` | host_state, network, mvm_db | **Orchestration** |
| `get_host_state()` | host_state, mvm_db | **Delegation** |

### Key Operations (keys.py)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| All functions | key_manager | **Delegation** (with validation) |

### Config Operations (config.py)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| `get_firecracker_config()`, `get_defaults_config()` | config_state, mvm_db | **DB + Delegation** |
| `set_defaults_value()` | config_state, mvm_db | **DB update + Delegation** |
| Other functions | config, user_config | **Direct pass-through** |

### Cache Operations (cache.py)

| API Function | Pattern |
|--------------|---------|
| `prune_vms()`, `prune_networks()`, `prune_images()`, `prune_kernels()` | **Orchestration** |
| `prune_all()` | **Master orchestration** |

### Metadata Operations (metadata.py)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| All functions | metadata, mvm_db | **Delegation / DB query** |

### Network Sync Operations (network_sync.py)

| API Function | Core Module(s) | Pattern |
|--------------|----------------|---------|
| `IPTablesSynchronizer.sync_network()` | iptables_tracker, mvm_db, network | **Orchestration** |
| `IPTablesSynchronizer.sync_all_networks()` | mvm_db | **DB query + loop** |

---

## PRIVILEGE CHECKS

Only these API functions call `check_privileges_interactive()`:

| Module | Functions |
|--------|-----------|
| `vm/` | `create_vm()`, `remove_vm()`, `cleanup_vms()` |
| `network.py` | `create_network()`, `remove_network()` |
| `host.py` | `init_host()`, `restore_host()`, `clean_host()`, `reset_host()`, `prune_host()` |
| `cache.py` | `prune_vms()`, `prune_networks()`, `prune_all()` |

**Pattern:**
```python
from mvmctl.api.host import check_privileges_interactive
check_privileges_interactive("/usr/sbin/ip", "operation description")
```

---

## DEFAULT VALUE POLICY

The **API layer MUST NOT have default values in function parameters**. All API functions must receive explicit values from the CLI layer.

### Database Query Responsibility (CRITICAL RULE)

**The API layer is EXCLUSIVELY and SOLELY responsible for all database queries.**

| Layer | Database Access Policy |
|-------|----------------------|
| **CLI** | **NO database queries** — passes `None` or explicit values |
| **API** | **MUST query database** when CLI passes `None` for DB-backed values |
| **Core** | **NO database queries** — receives explicit values from API |

### Example: Correct API-to-Core Data Flow

**Step 1: CLI passes None (correct)**
```python
# cli/vm.py (CORRECT)
def create(
    image: Optional[str] = typer.Option(None, "--image"),
    kernel: Optional[str] = typer.Option(None, "--kernel"),
):
    # image/kernel are DB-backed — pass None to API for resolution
    create_vm(image=image, kernel=kernel, ...)
```

**Step 2: API queries DB and passes explicit values (correct)**
```python
# api/vm/ package (CORRECT)
def create_vm(image: Optional[str] = None, ...) -> VMInstance:
    db = MVMDatabase()
    
    # Resolve DB-backed defaults
    if image is None:
        image_entry = db.get_default_image()
        image = image_entry.os_slug  # ✅ Now explicit
    
    # Pass EXPLICIT values to Core
    return _core_create_vm(image=image, ...)
```

**Step 3: Core receives explicit values (correct)**
```python
# core/vm_lifecycle.py (CORRECT)
def create_vm(image: str, ...) -> VMInstance:
    # NO database queries — values are explicit
    # Just execute business logic
    ...
```

---

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Format or print output | Return data; let CLI format |
| Business logic beyond privilege + delegation | Move to `core/` |
| Skip `__all__` | Always declare public surface |
| Import from `cli/` | One-way dependency: `cli` → `api` → `core` |
| **Default values in function parameters** | API receives explicit values from CLI |
| Core module importing another core module | API orchestrates; core stays isolated |

---

## VERIFICATION CHECKLIST

Before submitting any API change:
- [ ] **NO default values in API function parameters**
- [ ] API functions accept `Optional[T]` for DB-backed defaults
- [ ] When CLI passes `None`, API queries database via `MVMDatabase`
- [ ] **API never passes `None` to Core for required parameters**
- [ ] API adds `check_privileges_interactive()` before privileged operations
- [ ] All database queries happen in API layer, never in CLI or Core
- [ ] All core modules remain isolated (no cross-core imports)

### Enforcement

CI checks will reject PRs containing:
- Default values in API function parameters
- CLI code that queries the database
- Core code that queries the database (except `mvm_db.py` interface)
- API functions that pass `None` to Core for required parameters
- Core modules that import from other core modules

**NO EXCEPTIONS. NO WORKAROUNDS. NO DISCUSSION.**
