# fcm/core/ — Business Logic Layer

**Scope:** Core business logic, system operations, Firecracker interaction  
**Rules:** All subprocess calls, privilege checks, and VM lifecycle logic live here

## STRUCTURE

```
src/fcm/core/
├── __init__.py
├── vm_lifecycle.py      # VM create/start/stop/remove (405 lines)
├── network.py           # Bridge, TAP, NAT, iptables (409 lines)
├── network_manager.py   # Named network management
├── host*.py             # Host initialization (split into 4 modules)
│   ├── host.py          # Orchestration (clean_host, prune_host, reset_host)
│   ├── host_setup.py    # KVM, sysctl, modules
│   ├── host_state.py    # State snapshots, restore
│   └── host_privilege.py # Group/sudoers management
├── image.py             # Image download, conversion, partition extraction
├── kernel.py            # Kernel download/build pipeline
├── firecracker.py       # Firecracker API client
├── vm_manager.py        # VM registry, state management
├── key_manager.py       # SSH key generation, caching
├── binary_manager.py    # Firecracker binary management
├── config_gen.py        # Firecracker JSON config generation
├── cloud_init.py        # cloud-init ISO creation
├── logs.py              # VM log retrieval
├── ssh.py               # SSH command building/execution
└── config.py            # YAML config loading
```

## WHERE TO LOOK

| Task | Module | Function/Class |
|------|--------|----------------|
| Create VM | `vm_lifecycle.py` | `create_vm()` |
| Stop/remove VM | `vm_lifecycle.py` | `remove_vm()`, `cleanup_tap()` |
| Setup bridge | `network.py` | `setup_bridge()`, `setup_nat()` |
| Create TAP | `network.py` | `create_tap()` |
| Manage networks | `network_manager.py` | `create_network()`, `remove_network()` |
| Host init | `host_setup.py` | `init_host()` |
| Reset host | `host.py` | `reset_host()` |
| Fetch image | `image.py` | `fetch_image()` |
| Build kernel | `kernel.py` | `build_kernel_pipeline()` |
| VM registry | `vm_manager.py` | `VMManager.register()`, `.get()` |
| SSH access | `ssh.py` | `ssh_vm()`, `build_ssh_command()` |
| Firecracker API | `firecracker.py` | `FirecrackerClient` |

## CONVENTIONS

### Subprocess Handling
- ALWAYS check return codes
- Capture stderr for error messages
- Use typed exceptions from `fcm.exceptions`
- Mock in tests (tests must not require root)

### Error Handling Pattern
```python
from fcm.exceptions import NetworkError, HostError

try:
    subprocess.run(cmd, capture_output=True, text=True, check=True)
except subprocess.CalledProcessError as e:
    raise NetworkError(f"Failed to setup bridge: {e}\n{e.stderr}") from e
except FileNotFoundError as e:
    raise NetworkError(f"ip command not found") from e
```

### Privilege Checks
- Use `check_privileges(binary_path)` before privileged operations
- Binary paths from `PRIVILEGED_BINARIES` constant
- NOT root → check group membership

### State Management
- Host state: Snapshots saved to `cache_dir/host/state.json`
- VM state: Registry in `cache_dir/vms/{name}/`
- Changes tracked via `HostChange` dataclass for rollback

## ANTI-PATTERNS

### NEVER
- **Direct `print()`** — Use `fcm.utils.console` (Rich) for output
- **Hardcoded binary paths** — Use `PRIVILEGED_BINARIES`
- **Bare subprocess calls without error handling** — Always wrap in try/except
- **Skip privilege checks** — Always validate before privileged ops
- **Modify CLI output here** — Return data/exceptions; formatting in CLI layer

### Code Smells to Avoid
- Large functions (>100 lines) — Already have several; avoid adding more
- Deep nesting (>3 levels) — Use early returns
- String concatenation for commands — Use lists for subprocess args

## KEY MODULES

### vm_lifecycle.py (405 lines)
VM creation orchestration:
- Copies rootfs from image
- Generates cloud-init ISO
- Creates Firecracker JSON config
- Sets up network (bridge, TAP, iptables)
- Starts Firecracker process
- Registers VM in manager

### network.py (409 lines)
Network infrastructure:
- Bridge: `setup_bridge()`, `teardown_bridge()`
- TAP: `create_tap()`, `delete_tap()`
- NAT: `setup_nat()`, `teardown_nat()`
- iptables: `add_iptables_forward_rules()`, `remove_iptables_forward_rules()`
- Uses `ip -batch` for batch operations

### host_*.py (4 modules, ~520 lines total)
Split from original monolithic host.py:
- `host_setup.py` — KVM check, sysctl, module loading
- `host_privilege.py` — Group creation, sudoers management
- `host_state.py` — State snapshots, JSON serialization
- `host.py` — Orchestration (clean, prune, reset)

### image.py (422 lines)
Image handling:
- Download with SHA256 verification
- QCOW2 → raw conversion via `qemu-img`
- Partition extraction via `dd` (bs=1M)
- Filesystem detection via `blkid`

## NOTES

- **Complexity hotspots:** `vm_lifecycle.py`, `network.py`, `image.py` are all >400 lines
- **Subprocess-heavy:** All modules use subprocess for system operations
- **Privilege-aware:** Most functions require root or fcm group membership
- **Test isolation:** All subprocess calls must be mockable for unit tests
