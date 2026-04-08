# Model Input/Output Refactoring Plan

## Goal

Replace flat `dict` and `tuple` returns from API and Core functions with properly-typed model dataclasses. This enforces the architectural rule that models are the data contract between layers.

## Naming Convention (already established)

| Suffix | Meaning | Examples |
|--------|---------|----------|
| `Input` | API creation/update input | `VMCreateInput`, `ImageFetchInput` |
| `Item` | Persisted DB record | `ImageItem`, `KernelItem`, `BinaryItem`, `NetworkItem` |
| `Spec` | YAML source definition | `ImageSpec`, `KernelSpec` |
| `Config` | Runtime configuration | `VMConfig`, `CloudInitConfig` |
| `Instance` | Active/runtime entity | `VMInstance` |
| `Info` | Read-only inspection output | `VMInspectInfo`, `NetworkInspectInfo`, `ConsoleInfo` |
| `State` | Snapshot | `HostState` |
| `Result` | Operation output | `KernelFetchResult` |
| `TypedDict` | Dict shape contract (no behavior) | `InstanceInfo`, `LeaseEntry` |

---

## Corrections to Prior Version

| What Was Wrong | Correction |
|----------------|------------|
| `create_vm` has 28 params | Actual count: **31 params** — `VMCreateInput` needs 31 fields |
| `KernelFetchResult` needs to be created | **Already exists** in `models/kernel.py` with `path`, `version`, `arch`, `kernel_type`, `warnings`, `info_messages` fields |
| Phase 1 `get_network_leases()` | `api/network.py get_network_leases()` already returns `list[NetworkLease]`; only `cache.py` version needs fixing |
| Missing `import_image_and_register` in Phase 3 | Added `import_image_and_register(...)` → `ImageImportInput` to Phase 3d (api/image.py); `ImageImportInput` already exists |
| Phase 3 only covered `api/metadata.py` | **`core/metadata.py` is missing entirely** — 12 functions need fixing |
| Phase 3 missing `core/cloud_init.py` | **`write_cloud_init` with 9 params** is in `core/`, not `api/` |
| Phase 3 missing `core/network_manager.py` | Conversion functions take/return `dict[str, Any]` — need TypedDict |
| Phase 3 missing `core/firecracker.py` | `get_instance_info()` and `describe_instance()` return `dict[str, object]` — need TypedDict |
| Phase 3 missing `api/kernel.py list_kernels()` | Returns `list[dict[str, str]]` — should be `list[KernelItem]` |
| Phase 3 missing `api/network.py reconcile_networks()` | Returns `list[dict[str, Any]]` — needs model |
| Phase 3 missing `api/network.py get_default_network_entry()` | Returns `tuple[str, dict[str, Any]] \| None` — should be `NetworkItem \| None` |

---

## Phase 0: Fix `core/metadata.py` — The Biggest Offender

### Problem
`core/metadata.py` has **12 functions** returning `dict[str, Any]` instead of proper Item models. This is the worst typing gap in the codebase.

### Changes

| Function | Current Return | New Return |
|----------|---------------|------------|
| `get_kernel_entry()` | `dict[str, Any]` | `KernelItem` |
| `list_kernel_entries()` | `dict[str, dict[str, Any]]` | `dict[str, KernelItem]` |
| `get_image_entry()` | `dict[str, Any]` | `ImageItem` |
| `list_image_entries()` | `dict[str, dict[str, Any]]` | `dict[str, ImageItem]` |
| `find_image_by_id_prefix()` | `tuple[str, dict[str, Any]] \| None` | `ImageItem \| None` (drop tuple) |
| `find_images_by_id_prefix()` | `list[tuple[str, dict[str, Any]]]` | `list[ImageItem]` (drop tuple wrapper) |
| `get_binary_entry()` | `dict[str, Any]` | `BinaryItem` |
| `list_binary_entries()` | `dict[str, dict[str, Any]]` | `dict[str, BinaryItem]` |
| `get_network_entry()` | `dict[str, Any]` | `NetworkItem` |
| `list_network_entries()` | `dict[str, dict[str, Any]]` | `dict[str, NetworkItem]` |
| `update_kernel_entry(**fields: Any)` | `None` | Keep `**fields` but validate against `KernelItem` fields |
| `update_image_entry(**fields: Any)` | `None` | Keep `**fields` but validate against `ImageItem` fields |
| `update_binary_entry(**fields: Any)` | `None` | Keep `**fields` but validate against `BinaryItem` fields |
| `update_network_entry(**fields: Any)` | `None` | Keep `**fields` but validate against `NetworkItem` fields |

### Files to modify
- `src/mvmctl/core/metadata.py` — update all return types
- `src/mvmctl/models/image.py` — ensure `ImageItem.from_db()` returns `ImageItem`
- `src/mvmctl/models/kernel.py` — ensure `KernelItem.from_db()` returns `KernelItem`
- `src/mvmctl/models/binary.py` — ensure `BinaryItem.from_db()` returns `BinaryItem`
- `src/mvmctl/models/network.py` — ensure `NetworkItem.from_db()` returns `NetworkItem`
- `src/mvmctl/api/metadata.py` — update wrappers to pass through Item models

### Note on `find_*_by_id_prefix` change
Current pattern (tuple-wrapped):
```python
result = find_images_by_id_prefix(prefix="abc")
for id_, entry in result:  # entry is dict
    print(entry["os_slug"])
```
New pattern (flat list of Items):
```python
result = find_images_by_id_prefix(prefix="abc")
for item in result:  # item is ImageItem
    print(item.os_slug)
```

---

## Phase 1: Fix `cache.py` and `api/metadata.py` return types

### Problem
`cache.py` wraps `core/metadata.py` but returns `tuple[str, dict[str, Any]]` — callers must unpack manually and access dict keys. Same issue in `api/metadata.py`.

### Changes

| Function | Current Return | New Return |
|----------|---------------|------------|
| `api/cache.py get_default_image_entry()` | `tuple[str, dict[str, Any]] \| None` | `ImageItem \| None` |
| `api/cache.py get_default_kernel_entry()` | `tuple[str, dict[str, Any]] \| None` | `KernelItem \| None` |
| `api/cache.py get_network_leases()` | `list[Any]` | `list[NetworkLease]` |
| `api/cache.py list_networks()` | `list[Any]` | `list[NetworkConfig]` |
| `api/cache.py init_all()` | `dict[str, str]` | `dict[str, str]` (acceptable — simple init result) |
| `api/cache.py prune_all()` | `dict[str, list[str] \| bool]` | Create `PruneAllResult` dataclass |
| `api/metadata.py get_default_image_entry()` | `tuple[str, dict[str, Any]] \| None` | `ImageItem \| None` |
| `api/metadata.py get_default_kernel_entry()` | `tuple[str, dict[str, Any]] \| None` | `KernelItem \| None` |
| `api/metadata.py get_default_binary_entry()` | `tuple[str, dict[str, Any]] \| None` | `BinaryItem \| None` |
| `api/metadata.py get_default_network_entry()` | `tuple[str, dict[str, Any]] \| None` | `NetworkItem \| None` |

### Files to modify
- `src/mvmctl/api/cache.py`
- `src/mvmctl/api/metadata.py`
- `src/mvmctl/models/__init__.py`

### Note
`cache.py prune_images()` and `prune_kernels()` currently unpack the tuple:
```python
default_entry = get_default_image_entry()
default_id = default_entry[0] if default_entry else None
```
With the new return type:
```python
default_item = get_default_image_entry()
default_id = default_item.id if default_item else None
```

---

## Phase 2: Create missing model dataclasses

### Models ALREADY existing (do NOT create)

| Model | File | Status |
|-------|------|--------|
| `KernelFetchResult` | `models/kernel.py` | EXISTS — has `path`, `version`, `arch`, `kernel_type`, `warnings`, `info_messages`, `name` property, `exists()` |
| `ImageItem` | `models/image.py` | EXISTS — has `from_db()`, `to_dict()` |
| `KernelItem` | `models/kernel.py` | EXISTS — has `from_db()`, `to_dict()` |
| `BinaryItem` | `models/binary.py` | EXISTS — has `from_db()`, `to_dict()` |
| `NetworkItem` | `models/network.py` | EXISTS — has `from_db()`, `to_dict()` |
| `NetworkLease` | `models/network.py` | EXISTS |
| `NetworkConfig` | `models/network.py` | EXISTS |
| `CloudInitConfig` | `models/cloud_init.py` | EXISTS — has `to_dict()`, `from_dict()` |
| `VMConfig` | `models/vm.py` | EXISTS — has `to_dict()`, `from_dict()` |
| `VMInstance` | `models/vm.py` | EXISTS — has `to_dict()`, `from_dict()` |
| `ImageImportInput` | `models/image.py` | EXISTS — fields: id, name, source_path, format, convert_to, minimum_rootfs_size, disabled_detectors |

### New models to create

#### `VMCreateInput` — `src/mvmctl/models/vm.py`
All VM creation parameters bundled into one model. Replaces **31** function parameters (not 28).

```python
@dataclass
class VMCreateInput:
    name: str
    vcpus: int
    mem: int
    user: str
    enable_api_socket: bool
    enable_pci: bool
    enable_console: bool
    firecracker_bin: str
    lsm_flags: str
    enable_logging: bool
    enable_metrics: bool
    # Optional (DB-backed at API layer)
    image: str | None = None
    kernel: str | None = None
    image_path: Path | None = None
    kernel_path: Path | None = None
    disk_size: str | None = None
    ip: str | None = None
    network_name: str | None = None
    mac: str | None = None
    ssh_key: str | None = None
    user_data: Path | None = None
    cloud_init_mode: CloudInitMode = CloudInitMode.INJECT
    cloud_init_iso_path: Path | None = None
    keep_cloud_init_iso: bool = False
    nocloud_net_port: int = 0
    # Additional discovered params
    image_fs_uuid: str | None = None
    image_fs_type: str | None = None
    image_hash: str | None = None
    binary_id: str | None = None
```

#### `ConsoleInfo` — `src/mvmctl/models/vm.py`
```python
@dataclass
class ConsoleInfo:
    socket_path: Path
    vm_name: str
```

#### `ConsoleState` — `src/mvmctl/models/vm.py`
```python
@dataclass
class ConsoleState:
    running: bool
    pid: int | None
    socket_path: str | None
```

#### `VMInspectInfo` — `src/mvmctl/models/vm.py`
All the data currently returned by `_gather_vm_details()`:
```python
@dataclass
class VMInspectInfo:
    id: str
    name: str
    status: str
    created_at: str | None
    pid: int | None
    ip: str | None
    mac: str | None
    network_name: str | None
    tap_device: str | None
    cloud_init_mode: str
    image_id: str | None
    image_name: str | None
    kernel_id: str | None
    kernel_name: str | None
    paths: dict[str, str | None]  # vm_dir, rootfs, rootfs_source, config
    features: dict[str, bool]  # api_socket, console, nocloud_net
    nocloud_net: dict[str, Any] | None = None
    console: dict[str, Any] | None = None
```

#### `NetworkInspectInfo` — `src/mvmctl/models/network.py`
```python
@dataclass
class NetworkInspectInfo:
    name: str
    subnet: str
    ipv4_gateway: str
    bridge: str
    nat_enabled: bool
    nat_gateways: list[str]
    created_at: str
    bridge_exists: bool
    vms: list[dict[str, Any]]  # vm_id, ipv4, status, pid
```

#### `ImageFetchInput` — `src/mvmctl/models/image.py`
```python
@dataclass
class ImageFetchInput:
    spec: ImageSpec
    output_dir: Path
    force: bool = False
    partition: int | None = None
    skip_optimization: bool = False
```

#### `KernelFetchInput` — `src/mvmctl/models/kernel.py`
```python
@dataclass
class KernelFetchInput:
    kernel_type: str
    version: str | None
    arch: str
    output_dir: Path
    output_name: str | None = None
    output_path: Path | None = None
    jobs: int | None = None
    keep_build_dir: bool = False
    clean_build: bool = False
    kernel_config: Path | None = None
```

#### `KeyCreateInput` — `src/mvmctl/models/key.py` (new file)
```python
@dataclass
class KeyCreateInput:
    name: str
    output_dir: Path | None = None
    comment: str | None = None
    overwrite: bool = False
```

#### `PruneAllResult` — `src/mvmctl/models/cache.py` (new)
```python
@dataclass
class PruneAllResult:
    pruned_vms: list[str]
    pruned_networks: list[str]
    pruned_images: list[str]
    pruned_kernels: list[str]
    had_running_vms: bool
```

#### `CloudInitWriteConfig` — `src/mvmctl/models/cloud_init.py`
Input model for `core/cloud_init.py write_cloud_init()` — **9 params** consolidated:
```python
@dataclass
class CloudInitWriteConfig:
    cloud_init_dir: Path
    vm_name: str
    guest_ip: str
    user: str
    ipv4_gateway: str
    ssh_pub_key: str | list[str] | None
    custom_user_data: Path | None = None
    prefix_len: int = 24
    skip_network_config: bool = False
```

### TypedDict declarations (no behavior, just shape contracts)

#### `InstanceInfo` — `src/mvmctl/models/firecracker.py`
Return type for `core/firecracker.py get_instance_info()`:
```python
class InstanceInfo(TypedDict):
    id: str
    state: str
    vcpu_count: int
    mem_size_mib: int
    boot_time: str | None
```

#### `InstanceDescription` — `src/mvmctl/models/firecracker.py`
Return type for `core/firecracker.py describe_instance()`:
```python
class InstanceDescription(TypedDict):
    id: str
    state: str
    vcpu_count: int
    mem_size_mib: int
    flags: list[str]
    if_addr: dict[str, str]
    used_block_devices: list[str]
```

#### `LeaseEntry` — `src/mvmctl/models/network.py`
Input type for `core/network_manager.py leases_from_entry()`:
```python
class LeaseEntry(TypedDict):
    vm_id: str
    ipv4: str
    assigned_at: str | None
```

#### `NetworkEntry` — `src/mvmctl/models/network.py`
Return type for `core/network_manager.py config_to_network_entry()`:
```python
class NetworkEntry(TypedDict):
    name: str
    subnet: str
    ipv4_gateway: str
    bridge: str
    nat_enabled: bool
    nat_gateways: list[str]
    created_at: str
    is_default: bool
```

---

## Phase 3: Update function signatures

### 3a. `api/vms.py`

| Function | Current | New |
|----------|---------|-----|
| `create_vm(...)` 31 args | `def create_vm(name, vcpus, mem, ...)` | `def create_vm(input: VMCreateInput) -> VMInstance` |
| `attach_console(name)` | `-> dict[str, Any]` | `-> ConsoleInfo` |
| `get_console_state(name)` | `-> dict[str, Any]` | `-> ConsoleState` |
| `inspect_vm(name)` | `-> dict[str, Any]` | `-> VMInspectInfo` |

### 3b. `api/network.py`

| Function | Current | New |
|----------|---------|-----|
| `inspect_network(name)` | `-> dict[str, Any]` | `-> NetworkInspectInfo` |
| `get_default_network_entry(cache_dir)` | `-> tuple[str, dict[str, Any]] \| None` | `-> NetworkItem \| None` (drop tuple) |
| `reconcile_networks()` | `-> list[dict[str, Any]]` | `-> list[NetworkInspectInfo]` |

### 3c. `api/kernel.py`

| Function | Current | New |
|----------|---------|-----|
| `fetch_kernel(...)` 10 args | `def fetch_kernel(kernel_type, version, arch, ...)` | `def fetch_kernel(input: KernelFetchInput) -> KernelFetchResult` |
| `list_kernels(kernels_dir)` | `-> list[dict[str, str]]` | `-> list[KernelItem]` |

### 3d. `api/image.py`

| Function | Current | New |
|----------|---------|-----|
| `fetch_image_and_register(...)` 5 args | `def fetch_image_and_register(spec, output_dir, ...)` | `def fetch_image_and_register(input: ImageFetchInput)` |
| `import_image_and_register(...)` | `def import_image_and_register(spec: Any, ...)` | `def import_image_and_register(input: ImageImportInput)` |

### 3e. `api/keys.py`

| Function | Current | New |
|----------|---------|-----|
| `create_key(...)` 4 args | `def create_key(name, output_dir, comment, overwrite)` | `def create_key(input: KeyCreateInput)` |

### 3f. `core/cloud_init.py`

| Function | Current | New |
|----------|---------|-----|
| `write_cloud_init(...)` 9 args | `def write_cloud_init(cloud_init_dir, vm_name, guest_ip, ...)` | `def write_cloud_init(config: CloudInitWriteConfig)` |

### 3g. `core/network_manager.py`

| Function | Current | New |
|----------|---------|-----|
| `network_entry_to_config(name, entry: dict)` | `entry: dict[str, Any]` | `entry: NetworkEntry` (TypedDict) |
| `leases_from_entry(entry: dict)` | `entry: dict[str, Any]` | `entry: LeaseEntry` (TypedDict) |
| `config_to_network_entry(config)` | `-> dict[str, Any]` | `-> NetworkEntry` (TypedDict) |

### 3h. `core/firecracker.py`

| Function | Current | New |
|----------|---------|-----|
| `get_instance_info()` | `-> dict[str, object] \| None` | `-> InstanceInfo \| None` (TypedDict) |
| `describe_instance()` | `-> dict[str, object] \| None` | `-> InstanceDescription \| None` (TypedDict) |

---

## Phase 4: Update all callers

### 4a. API → Core callers (when Core returns proper models)

| Caller File | Function | Callee | Change |
|-------------|----------|--------|--------|
| `api/vms.py` | `create_vm()` | `core/vm_lifecycle.create_vm()` | Receives `VMCreateInput`, unpacks to `VMConfig` + `VMInstance` |
| `api/vms.py` | `inspect_vm()` | `core/vm_lifecycle` + `core/metadata` | Uses `VMInspectInfo` |
| `api/network.py` | `inspect_network()` | `core/network_manager` | Uses `NetworkInspectInfo` |
| `api/network.py` | various | `core/metadata` | Uses `NetworkItem`, `NetworkLease` |
| `api/kernel.py` | `list_kernels()` | `core/metadata` | Returns `list[KernelItem]` |
| `api/image.py` | various | `core/metadata` | Returns `ImageItem` |

### 4b. CLI callers

| File | Function | Calls | Action |
|------|----------|-------|--------|
| `cli/vm.py` | `vm_create()` | `create_vm(...)` 31 args | Build `VMCreateInput`, pass as single arg |
| `cli/vm.py` | `vm_inspect()` | `inspect_vm()` | Use `VMInspectInfo` fields |
| `cli/vm.py` | `console_attach()` | `attach_console()` | Use `ConsoleInfo` fields |
| `cli/image.py` | `image_fetch()` | `fetch_image_and_register()` | Build `ImageFetchInput` |
| `cli/image.py` | `image_import()` | `import_image_and_register()` | Build `ImageImportInput` (already exists in models/) |
| `cli/kernel.py` | `kernel_fetch()` | `fetch_kernel()` | Build `KernelFetchInput` |
| `cli/key.py` | `key_create()` | `create_key()` | Build `KeyCreateInput` |
| `cli/network.py` | `network_inspect()` | `inspect_network()` | Use `NetworkInspectInfo` fields |
| `cli/cache.py` | multiple | `get_default_image_entry()` | Use `.id` and `.to_dict()` |
| `cli/cache.py` | `cache_prune_all()` | `prune_all()` | Use `PruneAllResult` fields |

### 4c. Core → Core callers

| File | Function | Callee | Change |
|------|----------|--------|--------|
| `core/vm_lifecycle.py` | `create_vm()` | `core/cloud_init.write_cloud_init()` | Build `CloudInitWriteConfig` |

### 4d. Import verification (after Phase 2)

Run after all new models are created, before Phase 3:

```bash
uv run python -c "from mvmctl.models import *; print('Models import OK')"
uv run python -c "from mvmctl.api import *; print('API import OK')"
uv run python -c "from mvmctl.core import *; print('Core import OK')"
uv run python -c "from mvmctl.cli import *; print('CLI import OK')"
```

### 4e. Documentation updates

After all phases complete, update:
- `docs/API.md` — add new model references and update function signatures
- Model docstrings — add class-level docstrings to all new dataclasses

---

## Implementation Order

### Step 0: Phase 0 (`core/metadata.py`)
Fix all return types to return Item models first — this is foundational.

### Step 1: Phase 1 (`cache.py` + `api/metadata.py`)
Update wrappers to pass through Item models from fixed Phase 0.

### Step 2: Phase 2 (new dataclasses)
Create in dependency order:
1. `VMCreateInput` — highest impact
2. `ConsoleInfo`, `ConsoleState` — no dependencies
3. `VMInspectInfo` — no dependencies
4. `NetworkInspectInfo` — no dependencies
5. `PruneAllResult` — no dependencies
6. TypedDicts (`InstanceInfo`, `InstanceDescription`, `LeaseEntry`, `NetworkEntry`) — no dependencies
7. `CloudInitWriteConfig` — no dependencies
8. `ImageFetchInput` — depends on `ImageSpec`
9. `KernelFetchInput` — no dependencies
10. `KeyCreateInput` — no dependencies
11. (ImageImportInput already exists — see "Models ALREADY existing" table)

### Step 3: Phase 3 (API signatures)
Do one module at a time, updating signature AND internal implementation:
1. `api/vms.py` — highest value, most changes
2. `api/network.py`
3. `api/kernel.py`
4. `api/image.py`
5. `api/keys.py`
6. `core/cloud_init.py`
7. `core/network_manager.py`
8. `core/firecracker.py`

### Step 4: Phase 4 (callers)
After each API module is updated, update all its callers:
1. Update CLI callers (most numerous, use CliRunner tests)
2. Update core callers
3. Update test files

---

## Test Updates Required

Every changed function signature requires updating test mocks:

### Pattern: Before → After

```python
# Before: mock returns dict
mock_api = mocker.patch("mvmctl.api.vms.inspect_vm")
mock_api.return_value = {"name": "vm", "status": "running", ...}

# After: mock returns proper model
mock_api = mocker.patch("mvmctl.api.vms.inspect_vm")
mock_api.return_value = VMInspectInfo(name="vm", status="running", ...)

# Before: mock returns tuple
mock_api = mocker.patch("mvmctl.api.cache.get_default_image_entry")
mock_api.return_value = ("abc123", {"os_slug": "ubuntu-24.04", ...})

# After: mock returns Item
mock_api = mocker.patch("mvmctl.api.cache.get_default_image_entry")
mock_api.return_value = ImageItem(id="abc123", os_slug="ubuntu-24.04", ...)
```

### Files to check for test updates

| Test File | Functions to Update |
|-----------|---------------------|
| `tests/unit/test_api_vms.py` | `inspect_vm`, `attach_console`, `get_console_state`, `create_vm` |
| `tests/unit/test_cli_vm.py` | `vm_inspect()`, `console_attach()` |
| `tests/unit/test_api_network.py` | `inspect_network`, `get_default_network_entry`, `reconcile_networks` |
| `tests/unit/test_api_kernel.py` | `fetch_kernel`, `list_kernels` |
| `tests/unit/test_api_image.py` | `fetch_image_and_register` |
| `tests/unit/test_api_keys.py` | `create_key` |
| `tests/unit/test_cache.py` | `get_default_image_entry`, `get_default_kernel_entry`, `prune_all` |
| `tests/unit/test_core_metadata.py` | ALL functions |
| `tests/unit/test_core_network_manager.py` | `network_entry_to_config`, `leases_from_entry` |
| `tests/unit/test_core_firecracker.py` | `get_instance_info`, `describe_instance` |

---

## Verification Checklist

After each step:
- [ ] `uv run ruff check src/mvmctl/` — clean
- [ ] `uv run ruff format --check src/mvmctl/` — clean
- [ ] `uv run mypy src/mvmctl/` — clean (strict mode)
- [ ] `uv run pytest tests/unit/test_api_*.py -v` — all pass
- [ ] `uv run pytest tests/unit/test_cli_*.py -v` — all pass
- [ ] `uv run pytest tests/ -q --cov=src/mvmctl --cov-fail-under=80` — 80%+ coverage
- [ ] Import verification (after Phase 2):
  - `uv run python -c "from mvmctl.models import *; print('OK')"`
  - `uv run python -c "from mvmctl.api import *; print('OK')"`
  - `uv run python -c "from mvmctl.core import *; print('OK')"`
  - `uv run python -c "from mvmctl.cli import *; print('OK')"`

---

## Anti-Patterns to Avoid

1. **Don't create models that mirror existing ones** — if `ImageSpec` or `KernelSpec` already exist and have the right fields, reuse them
2. **Don't return `dict[str, Any]` from API or Core** — always return a typed model
3. **Don't unpack models in API** — return the model, let caller access fields
4. **Don't mix `Input` and `Config`** — `Input` is for creation, `Config` is for runtime settings
5. **Don't add business logic to models** — models are data containers only
6. **Don't use `**fields: Any`** — use TypedDict field groups for update operations
7. **Don't wrap Items in tuples** — return the Item directly; use `NetworkItem | None`, not `tuple[str, NetworkItem] | None`
8. **Don't return `list[dict]`** — return `list[ModelItem]`

---

## Scope Summary

| Category | Count | Notes |
|----------|-------|-------|
| Functions returning `dict[str, Any]` | ~30 | All to be converted to typed models |
| Functions taking `dict[str, Any]` params | ~8 | All to use TypedDict or dataclass |
| New dataclasses to create | 9 | `VMCreateInput`, `ConsoleInfo`, `ConsoleState`, `VMInspectInfo`, `NetworkInspectInfo`, `ImageFetchInput`, `KernelFetchInput`, `KeyCreateInput`, `PruneAllResult` |
| Models confirmed existing | 10 | Do NOT recreate |
| Files needing changes | ~20 | api/, core/, cli/, models/, tests/ |
