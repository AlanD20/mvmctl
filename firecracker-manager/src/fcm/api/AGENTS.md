# fcm/api/ — Public API Layer

**Scope:** Stable Python API boundary between CLI and core  
**Role:** Add privilege checks; delegate to `core/`; export with `__all__`

## STRUCTURE

```
src/fcm/api/
├── vms.py       # VM operations: create, remove, list, get, ssh, logs, cleanup (144 lines)
├── assets.py    # Image/kernel/binary operations (356 lines)
├── host.py      # Host init/reset/status/clean
├── network.py   # Network create/remove/list/inspect
├── keys.py      # SSH key add/create/list/remove
├── config.py    # Config get/set/dump
└── vm_config.py # VM config file load/merge/save (138 lines)
```

## DELEGATION PATTERN

```python
# api/vms.py — typical module
from fcm.core.vm_lifecycle import create_vm as _core_create_vm
from fcm.core.host_privilege import check_privileges

def create_vm(name: str, image: str, ...) -> VMInstance:
    check_privileges("/usr/sbin/ip")       # ← privilege check HERE, not in CLI
    return _core_create_vm(name, image, ...)

__all__ = ["create_vm", "remove_vm", "list_vms", ...]
```

Key behaviors:
- API functions call `check_privileges()` for ops that touch network/host
- They re-export core functions that need no privilege wrapper unchanged
- Return core's return value directly; never reformat output

## API → CORE MAPPING

| API function | Core module | Notes |
|---|---|---|
| `vms.create_vm()` | `vm_lifecycle.create_vm()` | adds privilege check |
| `vms.list_vms()` | `vm_manager.VMManager.list_all()` | filters by `include_stopped` |
| `vms.deregister_vm()` | `vm_manager.VMManager.deregister()` | looks up `vm.id` first |
| `assets.fetch_image()` | `image.fetch_image()` | direct pass-through |
| `assets.fetch_binary()` | `binary_manager.fetch_binary()` | direct |
| `assets.build_kernel_pipeline()` | `kernel.build_kernel_pipeline()` | direct |
| `network.ensure_default_network()` | `network_manager.ensure_default_network()` | direct |
| `host.init_host()` | `host_setup.init_host()` | adds privilege check |
| `vm_config.load_vm_config_file()` | `models/vm_config_file.py` | deserialization only |
| `vm_config.merge_cli_overrides()` | `models/vm_config_file.py` | merges CLI flags into config |

## VM CONFIG FILE (vm_config.py)

`--output-config` and `--import-config` flags in `fcm vm create` are handled here:

```python
# Load config from file, merge CLI overrides (CLI wins):
base = load_vm_config_file(Path("myvm.json"))
merged = merge_cli_overrides(base, name="override-name", vcpus=4)

# Export current params to file:
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
