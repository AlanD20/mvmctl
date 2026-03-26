# mvmctl/models/ — Domain Dataclasses

**Scope:** Pure data containers; no subprocess, no I/O, no side effects (except `VMConfig.__post_init__` validation)
**Status:** Pre-production project — refactoring MUST NOT create legacy migration logic.
**Rule:** `@dataclass` only; no methods with business logic

## STRUCTURE

```
src/mvmctl/models/
├── __init__.py       # Exports: VMState, VMConfig, VMInstance, ImageSpec
│                     # NOT exported: ImageImportSpec, VMCreateConfigFile
├── vm.py             # VMState (StrEnum), VMConfig, VMInstance
├── image.py          # ImageSpec, ImageImportSpec
└── vm_config_file.py # VMCreateConfigFile — JSON config file schema
```

## MODELS

### VMState (StrEnum) — `vm.py`
Values: `RUNNING`, `STOPPED`, `ERROR`

### VMConfig — `vm.py`
Fields: `name`, `vcpu_count`, `mem_size_mib`, `kernel_path`, `rootfs_path`, `guest_ip`, `guest_mac`, `gateway`, `subnet_mask`, `tap_device`, `boot_args`, `enable_api_socket`, `enable_pci`, `lsm_flags`

**`__post_init__` validation:** vCPU 1–32; mem 128–65536 MiB — the only behavioral logic on a model.

### VMInstance — `vm.py`
Fields: `name`, `id` (64-char SHA256), `pid`, `socket_path`, `ip`, `mac`, `network_name`, `tap_device`, `created_at`, `status` (VMState), `config` (VMConfig)

### ImageSpec — `image.py`
Fields: `id`, `name`, `source` (URL), `format`, `convert_to`, `size_mib`, `sha256`, `sha256_url`
Used for YAML-defined images in `images.yaml`.

### ImageImportSpec — `image.py`
Fields: `id`, `name`, `source_path` (local), `format`, `convert_to`, `size_mib`
**Not in `models/__init__.__all__`** — import directly from `mvmctl.models.image`.

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
