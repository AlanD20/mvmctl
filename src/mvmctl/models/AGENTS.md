# mvmctl/models/ — Domain Dataclasses

**Scope:** Pure data containers; no subprocess, no I/O, no side effects (except `VMConfig.__post_init__` validation)
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** `@dataclass` only; no methods with business logic

## STRUCTURE

```
src/mvmctl/models/
├── __init__.py       # Exports: VMStatus, VMConfig, VMInstance, ImageSpec, KernelSpec,
│                     #          CloudInitConfig, CloudInitMode, CloudInitStatus
├── vm.py             # VMStatus (StrEnum), VMConfig, VMInstance
├── image.py          # ImageSpec, ImageImportSpec
├── kernel.py         # KernelSpec
├── cloud_init.py     # CloudInitMode, CloudInitStatus, CloudInitConfig
├── network.py        # NetworkLease, NetworkConfig, NetworkRecord
├── host.py           # HostStateChange, HostState
├── binary.py         # BinaryRecord
└── vm_config_file.py # VMCreateConfigFile — JSON config file schema
```

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
Fields: `mode` (CloudInitMode, default INJECT), `iso_path` (Path|None), `keep_iso` (bool), `nocloud_net_url` (str|None)
Methods: `to_dict()`, `from_dict()`

### ImageSpec — `image.py`
Fields: `id`, `name`, `source` (URL), `format`, `convert_to`, `minimum_rootfs_size`, `sha256`, `sha256_url`
Used for YAML-defined images in `images.yaml`.

### ImageImportSpec — `image.py`
Fields: `id`, `name`, `source_path` (local), `format`, `convert_to`, `minimum_rootfs_size`
**Not in `models/__init__.__all__`** — import directly from `mvmctl.models.image`.

### KernelSpec — `kernel.py`
Fields: `id`, `name`, `source` (URL), `version`, `arch`
Used for YAML-defined kernels in `kernels.yaml`.

### NetworkLease — `network.py`
Fields: `vm_id`, `ipv4`

### NetworkConfig — `network.py`
Fields: `name`, `subnet`, `ipv4_gateway`, `bridge`, `nat_enabled`, `nat_gateways`, `created_at`, `is_default`

### NetworkRecord — `network.py`
Fields: `id`, `name`, `subnet`, `bridge`, `ipv4_gateway`, `bridge_active`, `nat_gateways`, `nat_enabled`, `is_default`, `created_at`, `updated_at`
Methods: `from_db(record)`, `to_dict()`

### HostStateChange — `host.py`
Fields: `setting`, `original_value`, `applied_value`, `mechanism`

### HostState — `host.py`
Fields: `init_timestamp`, `changes` (list[HostStateChange])

### BinaryRecord — `binary.py`
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
