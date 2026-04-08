# mvmctl/models/ — Domain Dataclasses

**Scope:** Pure data containers; no subprocess, no I/O, no side effects (except `VMConfig.__post_init__` validation)
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** `@dataclass` only; no methods with business logic; **NO default values for config-backed fields**

## NAMING CONVENTION (MANDATORY — ALL NEW CODE)

| Suffix | Meaning | Examples |
|--------|---------|----------|
| `Input` | API creation/update input — what you pass to create something | `VMCreateInput`, `ImageFetchInput`, `ImageImportInput`, `NetworkCreateInput` |
| `Item` | Persisted DB record | `ImageItem`, `KernelItem`, `BinaryItem`, `NetworkItem` |
| `Spec` | YAML source definition (remote or local) | `ImageSpec`, `KernelSpec` |
| `Config` | Runtime configuration (not persisted as a DB row) | `VMConfig`, `CloudInitConfig`, `NetworkConfig`, `SystemDefaultsConfig` |
| `Instance` | Active/runtime entity | `VMInstance` |
| `State` | Snapshot of current state | `HostState` |
| `Result` | Operation output (synthetic, not stored) | `KernelFetchResult` |
| `*Mode` / `*Status` | StrEnum for constrained string values | `VMStatus`, `CloudInitMode`, `CloudInitStatus` |

**Legacy names to NEVER use:** `*Record` (use `*Item`), `ImageImportSpec` (use `ImageImportInput`)

## RESOLUTION LAYER MANDATE (MANDATORY — NO EXCEPTIONS)

**Models are pure data containers. They resolve nothing.**

| Layer | Resolves | How |
|-------|----------|-----|
| **CLI** | Constants-backed defaults | `DEFAULT_*` from `constants.py`. |
| **API** | DB-backed defaults | `MVMDatabase` queries. |
| **Core** | Nothing | Receives ALL explicit values. |
| **Models** | **Nothing** | Store exactly what they're given. |

**Models MUST NOT:**
- Have default values for config-backed fields (`vcpu_count`, `mem_size_mib`, `enable_pci`, `cloud_init_mode`, etc.)
- Import `DEFAULT_*` constants from `constants.py` as field defaults
- Import `MVMDatabase` or perform any DB operations
- Use `default_factory` for config-backed fields

**Field defaults policy:**
- Technical/structural fields with non-config defaults (`vm_id=""`, `extra_drives=field(default_factory=list)`, `schema_version="1.0"`) → **allowed**
- Config-backed fields (`vcpu_count`, `mem_size_mib`, `cloud_init_mode`, etc.) → **NO DEFAULTS**
- Truly optional nullable fields (`ip`, `mac`, `ssh_key`) → `= None` **allowed** (semantically optional, not a fallback)

**Violation = CI failure.** Enforced by `tests/layer_compliance/test_constants.py` and `tests/unit/test_vm_models.py`.

## STRUCTURE

```
src/mvmctl/models/
├── __init__.py       # Exports: VMStatus, VMConfig, VMInstance, ImageSpec, KernelSpec,
│                     #          CloudInitConfig, CloudInitMode, CloudInitStatus
├── vm.py             # VMStatus (StrEnum), VMConfig, VMInstance
├── image.py          # ImageSpec, ImageImportInput, ImageItem
├── kernel.py         # KernelSpec, KernelItem
├── cloud_init.py     # CloudInitMode, CloudInitStatus, CloudInitConfig
├── network.py        # NetworkLease, NetworkConfig, NetworkItem
├── host.py           # HostStateChange, HostState
├── binary.py         # BinaryItem
└── vm_config_file.py # VMCreateConfigFile — JSON config file schema
```

## DEFAULT VALUE POLICY

**Models MUST NOT have default values for config-backed fields.** Default values belong **ONLY** in the CLI layer.

### Why This Rule Exists

Models are the data boundary between layers. If models have default values:
1. They bypass the CLI's runtime configuration resolution
2. They create hidden behavior that ignores user settings
3. They make it unclear what value is actually being used
4. They duplicate default logic that should be centralized in CLI only

### Correct Pattern

```python
# WRONG — Model with config-backed default
@dataclass
class VMConfig:
    vcpu_count: int = 2  # DON'T DO THIS — default belongs in CLI only

# CORRECT — Model requires explicit values
@dataclass
class VMConfig:
    vcpu_count: int  # Required — CLI layer resolves and passes explicit value

# CORRECT — Model uses Optional for truly optional fields
@dataclass  
class CloudInitConfig:
    iso_path: Path | None = None  # None means "not provided" — CLI decides default behavior
```

### Layer Responsibility

| Layer | Default Policy |
|-------|----------------|
| **CLI** | Runtime resolution via `_get_vm_defaults()` → passes explicit values to API |
| **API** | Receives explicit values from CLI → passes to Core |
| **Core** | Receives explicit values from API → operates on what it's given |
| **Models** | Store exactly what they're given — no default value injection |

### What About `__post_init__` Validation?

`VMConfig.__post_init__` is the **only** behavioral logic allowed on models. It validates ranges (vCPU 1–32, mem 128–65536 MiB) but does NOT provide default values. It raises exceptions for invalid values — it never substitutes defaults.

## MODELS

### VMStatus (StrEnum) — `vm.py`
Values: `STARTING`, `RUNNING`, `PAUSED`, `STOPPING`, `STOPPED`, `CRASHED`, `ERROR`
(Replaced the old `VMState` with 3 values — do NOT use `VMState`)

### VMConfig — `vm.py`
Fields: `name`, `vm_id`, `vcpu_count`, `mem_size_mib`, `kernel_path`, `rootfs_path`, `boot_args`, `root_uuid`, `root_fs_type`, `enable_api_socket`, `enable_pci`, `lsm_flags`, `extra_drives`, `enable_logging`, `enable_metrics`, `enable_console`, `cloud_init_mode` (CloudInitMode), `cloud_init_iso_path`, `keep_cloud_init_iso`, `nocloud_net_url`

**`__post_init__` validation:** vCPU 1–32; mem 128–65536 MiB — the only behavioral logic on a model.
Methods: `to_dict()`, `from_dict()`

### VMInstance — `vm.py`
Fields: `name`, `id` (16-char hex), `pid`, `api_socket_path`, `console_socket_path`, `ipv4`, `mac`, `network_name`, `tap_device`, `ipv4_gateway`, `subnet_mask`, `created_at`, `status` (VMStatus), `config` (VMConfig), `exit_code`, `nocloud_net_port`, `nocloud_server_pid`, `console_relay_pid`, `rootfs_suffix`, `kernel_id`, `image_id`

### CloudInitMode (StrEnum) — `cloud_init.py`
Values: `INJECT` ("inject"), `NET` ("net"), `OFF` ("off"), `ISO` ("iso")

### CloudInitStatus (StrEnum) — `cloud_init.py`
Values: `PENDING`, `RUNNING`, `DONE`, `ERROR`

### CloudInitConfig — `cloud_init.py`
Fields: `mode` (CloudInitMode), `iso_path` (Path|None), `keep_iso` (bool), `nocloud_net_url` (str|None)

**Note:** No default values for config-backed fields. CLI layer resolves defaults at runtime.
Methods: `to_dict()`, `from_dict()`

### ImageSpec — `image.py`
Fields: `id`, `name`, `source` (URL), `format`, `convert_to`, `minimum_rootfs_size`, `sha256`, `sha256_url`
Used for YAML-defined images in `images.yaml`.

### ImageImportInput — `image.py`
Fields: `id`, `name`, `source_path` (local), `format`, `convert_to`, `minimum_rootfs_size`
**Not in `models/__init__.__all__`** — import directly from `mvmctl.models.image`.

### KernelSpec — `kernel.py`
Fields: `id`, `name`, `source` (URL), `version`, `arch`
Used for YAML-defined kernels in `kernels.yaml`.

### KernelItem — `kernel.py`
Fields: `id`, `name`, `version`, `path`, `arch`, `fs_type`, `is_default`, `created_at`, `updated_at`
Methods: `from_db(record)`, `to_dict()`

### ImageItem — `image.py`
Fields: `id`, `os_slug`, `path`, `os_name`, `fs_type`, `fs_uuid`, `arch`, `is_default`, `created_at`, `updated_at`
Methods: `from_db(record)`, `to_dict()`

### NetworkLease — `network.py`
Fields: `vm_id`, `ipv4`

### NetworkConfig — `network.py`
Fields: `name`, `subnet`, `ipv4_gateway`, `bridge`, `nat_enabled`, `nat_gateways`, `created_at`, `is_default`

### NetworkItem — `network.py`
Fields: `id`, `name`, `subnet`, `bridge`, `ipv4_gateway`, `bridge_active`, `nat_gateways`, `nat_enabled`, `is_default`, `created_at`, `updated_at`
Methods: `from_db(record)`, `to_dict()`

### HostStateChange — `host.py`
Fields: `setting`, `original_value`, `applied_value`, `mechanism`

### HostState — `host.py`
Fields: `init_timestamp`, `changes` (list[HostStateChange])

### BinaryItem — `binary.py`
Fields: `id`, `name`, `version`, `path`, `full_version`, `ci_version`, `is_default`, `created_at`, `updated_at`
Methods: `from_db(record)`, `to_dict()`

### VMCreateConfigFile — `vm_config_file.py`
Fields: all `mvm vm create` options + `firecracker_config` (embedded Firecracker boot JSON)
Methods: `from_dict()`, `from_json_file()`, `to_json_file()`, `to_dict()`
**Not in `models/__init__.__all__`** — imported by `api/vm_config.py` directly.

## ANTI-PATTERNS

| Forbidden | Correct |
|-----------|---------|
| Business logic in model methods | Move to `core/`; models hold data only |
| subprocess or I/O in any model | Raise to `core/` layer |
| Add fields with `default_factory` side effects | Pure defaults only |
| Import from `core/`, `api/`, or `cli/` | Models are leaf nodes — no upward deps |
| Reference `VMState` | Use `VMStatus` — `VMState` no longer exists |
| **Default values for config-backed fields** | CLI layer resolves at runtime; models receive explicit values |
