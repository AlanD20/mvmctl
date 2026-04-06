# Cross-Core Refactor Map (Wave 1)

**Purpose:** Comprehensive reference for refactoring functions from `core/` to `api/` layer during Wave 1 consolidation.

**Status:** Pre-production refactor — no legacy migration logic. All changes are architectural improvements.

**Generated:** 2026-04-06

---

## Section 1: Full Caller Map

### core/vm_lifecycle.py::create_vm

**Function Signature:**
```python
def create_vm(
    name: str,
    image_path: Path,
    vcpus: int,
    mem: int,
    network_name: str,
    user: str,
    enable_api_socket: bool,
    enable_pci: bool,
    enable_console: bool,
    firecracker_bin: str,
    lsm_flags: str,
    enable_logging: bool,
    enable_metrics: bool,
    kernel: str | None = None,
    kernel_path: Path | None = None,
    disk_size: str | None = None,
    ip: str | None = None,
    mac: str | None = None,
    ssh_key: str | None = None,
    user_data: Path | None = None,
    cloud_init_mode: CloudInitMode = CloudInitMode.INJECT,
    cloud_init_iso_path: Path | None = None,
    keep_cloud_init_iso: bool = False,
    vm_manager: VMManager | None = None,
    nocloud_net_port: int = 0,
    image_fs_uuid: str | None = None,
    image_fs_type: str | None = None,
    image_hash: str | None = None,
    binary_id: str | None = None,
) -> VMInstance
```

**Callers:**
- `api/vms.py:297` → API orchestrator (will remain here as wrapper)
- `src/mvmctl/core/cache_manager.py:23` → imports for `remove_vm` (not direct caller)
- `tests/unit/test_api_vms.py:*` → test mocks (will migrate to test_api_vms.py)
- `tests/unit/test_vm_lifecycle.py:*` → unit tests (will migrate to test_api_vms.py)

**Current Flow:**
```
cli/vm.py → api/vms.py:create_vm() → core/vm_lifecycle.py:create_vm()
```

**Post-Refactor Flow:**
```
cli/vm.py → api/vms.py:create_vm() [ORCHESTRATOR]
           ├─ core/network_manager.py:ensure_default_network()
           ├─ core/network_manager.py:get_network()
           ├─ core/network_manager.py:allocate_network_ip()
           ├─ core/image.py:resolve_image_path()
           ├─ core/kernel.py:resolve_kernel_path()
           ├─ core/vm_lifecycle.py:create_vm() [EXECUTION ONLY]
           └─ core/vm_manager.py:register()
```

---

### core/vm_lifecycle.py::remove_vm

**Function Signature:**
```python
def remove_vm(name: str, vm_manager: VMManager | None = None) -> None
```

**Callers:**
- `api/vms.py:332` → API orchestrator (will remain here as wrapper)
- `src/mvmctl/core/cache_manager.py:23` → imports for cleanup
- `tests/unit/test_api_vms.py:*` → test mocks

**Current Flow:**
```
cli/vm.py → api/vms.py:remove_vm() → core/vm_lifecycle.py:remove_vm()
```

**Post-Refactor Flow:**
```
cli/vm.py → api/vms.py:remove_vm() [ORCHESTRATOR]
           ├─ core/vm_manager.py:get()
           ├─ core/network_manager.py:get_network()
           ├─ core/network_manager.py:release_network_ip()
           ├─ core/vm_lifecycle.py:remove_vm() [EXECUTION ONLY]
           └─ core/vm_manager.py:deregister()
```

---

### core/vm_lifecycle.py::start_vm

**Function Signature:**
```python
def start_vm(name: str, vm_manager: VMManager | None = None, binary_id: str | None = None) -> None
```

**Callers:**
- `api/vms.py:365` → API orchestrator (lazy import, will remain)
- `tests/unit/test_api_vms.py:470` → test mock

**Current Flow:**
```
cli/vm.py → api/vms.py:start_vm() → core/vm_lifecycle.py:start_vm()
```

**Post-Refactor Flow:**
```
cli/vm.py → api/vms.py:start_vm() [ORCHESTRATOR]
           ├─ core/vm_manager.py:get()
           ├─ core/vm_lifecycle.py:start_vm() [EXECUTION ONLY]
           └─ core/vm_manager.py:register()
```

---

### core/vm_lifecycle.py::stop_vm

**Function Signature:**
```python
def stop_vm(name: str, vm_manager: VMManager | None = None, force: bool = False) -> None
```

**Callers:**
- `api/vms.py:357` → API orchestrator (lazy import, will remain)
- `tests/unit/test_api_vms.py:456` → test mock

**Current Flow:**
```
cli/vm.py → api/vms.py:stop_vm() → core/vm_lifecycle.py:stop_vm()
```

**Post-Refactor Flow:**
```
cli/vm.py → api/vms.py:stop_vm() [ORCHESTRATOR]
           ├─ core/vm_manager.py:get()
           ├─ core/vm_lifecycle.py:stop_vm() [EXECUTION ONLY]
           └─ core/vm_manager.py:update_status()
```

---

### core/network_manager.py::ensure_default_network

**Function Signature:**
```python
def ensure_default_network() -> NetworkConfig
```

**Callers:**
- `api/network.py:6` → API layer (will remain)
- `cli/host.py:166` → CLI (should call api/network.py instead)
- `cli/init.py:46, 95` → CLI init wizard (should call api/network.py)
- `core/vm_lifecycle.py:1056` → VM creation fallback (will move to api/vms.py)

**Current Flow:**
```
cli/host.py → api/network.py:ensure_default_network() → core/network_manager.py:ensure_default_network()
```

**Post-Refactor Flow:**
```
cli/host.py → api/network.py:ensure_default_network() [ORCHESTRATOR]
             ├─ core/network_manager.py:get_network()
             ├─ core/network.py:setup_bridge()
             ├─ core/network.py:setup_nat()
             └─ core/network_manager.py:create_network()
```

---

### core/network_manager.py::allocate_network_ip

**Function Signature:**
```python
def allocate_network_ip(network_name: str, vm_name: str) -> str
```

**Callers:**
- `core/vm_lifecycle.py:1071` → VM creation (will move to api/vms.py)
- `tests/unit/test_network_manager.py:319` → unit tests

**Current Flow:**
```
core/vm_lifecycle.py → core/network_manager.py:allocate_network_ip()
```

**Post-Refactor Flow:**
```
api/vms.py → core/network_manager.py:allocate_network_ip() [DIRECT CALL]
```

---

### core/network_manager.py::release_network_ip

**Function Signature:**
```python
def release_network_ip(network_name: str, vm_name: str) -> None
```

**Callers:**
- `core/vm_lifecycle.py:800` → cleanup during failed creation
- `core/vm_lifecycle.py:1492` → VM removal
- `tests/unit/test_network_manager.py:*` → unit tests

**Current Flow:**
```
core/vm_lifecycle.py → core/network_manager.py:release_network_ip()
```

**Post-Refactor Flow:**
```
api/vms.py → core/network_manager.py:release_network_ip() [DIRECT CALL]
```

---

### core/network_manager.py::get_network

**Function Signature:**
```python
def get_network(name: str) -> NetworkConfig | None
```

**Callers:**
- `api/vms.py:33` → API layer (will remain)
- `core/vm_lifecycle.py:1053` → VM creation (will move to api/vms.py)
- `core/vm_lifecycle.py:1435` → VM removal (will move to api/vms.py)
- `core/cache_manager.py:*` → cache operations
- `cli/network.py:*` → network listing

**Current Flow:**
```
core/vm_lifecycle.py → core/network_manager.py:get_network()
```

**Post-Refactor Flow:**
```
api/vms.py → core/network_manager.py:get_network() [DIRECT CALL]
```

---

### core/kernel.py::resolve_kernel_path

**Function Signature:**
```python
def resolve_kernel_path(kernel: str) -> Path
```

**Callers:**
- `api/vms.py:29` → API layer (will remain)
- `core/vm_lifecycle.py:1019` → VM creation (will move to api/vms.py)
- `tests/unit/test_api_vms.py:*` → tests

**Current Flow:**
```
core/vm_lifecycle.py → core/kernel.py:resolve_kernel_path()
```

**Post-Refactor Flow:**
```
api/vms.py → core/kernel.py:resolve_kernel_path() [DIRECT CALL]
```

---

### core/image.py::resolve_image_path

**Function Signature:**
```python
def resolve_image_path(image: str) -> Path
```

**Callers:**
- `api/vms.py:23` → API layer (will remain)
- `tests/unit/test_api_vms.py:*` → tests

**Current Flow:**
```
api/vms.py → core/image.py:resolve_image_path()
```

**Post-Refactor Flow:**
```
api/vms.py → core/image.py:resolve_image_path() [DIRECT CALL]
```

---

### core/config_state.py::All Public Functions

**Public Functions:**
- `get_firecracker_config(binary_record: Any | None = None) -> dict[str, str]`
- `initialize_default_config() -> dict[str, Any]`
- `get_assets_config() -> dict[str, str]`
- `set_default_image(image_id: str) -> None`
- `set_default_kernel(kernel_id: str) -> None`
- `set_default_binary(binary_name: str, binary_id: str) -> None`

**Callers:**
- `api/config.py:11-20` → API layer (will remain)
- `cli/configure.py:*` → CLI (should call api/config.py)
- `tests/unit/test_config_state.py:*` → unit tests

**Current Flow:**
```
cli/configure.py → api/config.py → core/config_state.py
```

**Post-Refactor Flow:**
```
cli/configure.py → api/config.py [ORCHESTRATOR]
                  └─ core/config_state.py [EXECUTION]
```

---

## Section 2: Function-to-Destination Table

| Source | Destination | Rationale |
|--------|-------------|-----------|
| `core/vm_lifecycle.py::create_vm` | `api/vms.py::create_vm` | API is the orchestrator; core is execution-only |
| `core/vm_lifecycle.py::remove_vm` | `api/vms.py::remove_vm` | API orchestrates cleanup sequence |
| `core/vm_lifecycle.py::start_vm` | `api/vms.py::start_vm` | API manages VM state transitions |
| `core/vm_lifecycle.py::stop_vm` | `api/vms.py::stop_vm` | API manages VM state transitions |
| `core/network_manager.py::ensure_default_network` | `api/network.py::ensure_default_network` | API orchestrates bridge/NAT setup |
| `core/network_manager.py::allocate_network_ip` | Remains in `core/network_manager.py` | Pure data operation; no orchestration needed |
| `core/network_manager.py::release_network_ip` | Remains in `core/network_manager.py` | Pure data operation; no orchestration needed |
| `core/network_manager.py::get_network` | Remains in `core/network_manager.py` | Pure data lookup; no orchestration needed |
| `core/kernel.py::resolve_kernel_path` | Remains in `core/kernel.py` | Pure path resolution; no orchestration needed |
| `core/image.py::resolve_image_path` | Remains in `core/image.py` | Pure path resolution; no orchestration needed |
| `core/config_state.py::*` | Remains in `core/config_state.py` | Pure state persistence; no orchestration needed |

---

## Section 3: Signal Handling Spec

### SIGTERM Handler Code Path

**Location:** `core/vm_lifecycle.py:850-905` (`graceful_shutdown()` function)

**Exact Code Path:**
```python
def graceful_shutdown(pid: int | None, socket_path: Path | None, force: bool = False) -> None:
    """
    1. If force=True:
       - Skip graceful shutdown
       - Send SIGTERM immediately
       - Wait FIRECRACKER_SIGTERM_WAIT_S seconds
       - Send SIGKILL if still alive
       - Return
    
    2. If force=False (graceful):
       - Try Firecracker HTTP API: client.send_ctrl_alt_del()
       - Poll for FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S seconds
       - If still alive after timeout:
         - Send SIGTERM
         - Wait FIRECRACKER_SIGTERM_WAIT_S seconds
       - If still alive:
         - Send SIGKILL
    """
```

**Signal Sequence (Graceful):**
1. **HTTP API Call** (lines 877-883): `FirecrackerClient.send_ctrl_alt_del()` via socket
2. **Poll Loop** (lines 884-891): Wait up to 100 seconds (100ms steps) for process exit
3. **SIGTERM** (lines 893-898): If still alive, send `signal.SIGTERM`
4. **Wait** (line 898): Sleep `FIRECRACKER_SIGTERM_WAIT_S` seconds
5. **SIGKILL** (lines 900-904): If still alive, send `signal.SIGKILL`

**Signal Sequence (Force):**
1. **SIGTERM** (lines 864-867): Send immediately
2. **Wait** (line 868): Sleep `FIRECRACKER_SIGTERM_WAIT_S` seconds
3. **SIGKILL** (lines 870-874): If still alive, send `signal.SIGKILL`

**Constants Used:**
- `FIRECRACKER_GRACEFUL_SHUTDOWN_TIMEOUT_S` = 100 seconds
- `FIRECRACKER_SHUTDOWN_POLL_INTERVAL_S` = 0.1 seconds (100ms)
- `FIRECRACKER_SIGTERM_WAIT_S` = 3 seconds
- `CONST_POLL_STEP_SECONDS` = 0.1 seconds

**MUST Be Preserved in `api/vms.py`:**
- Exact signal sequence (HTTP → SIGTERM → SIGKILL)
- Timeout values (100s graceful, 3s SIGTERM wait)
- Poll interval (100ms)
- Force flag behavior (skip HTTP, go straight to SIGTERM)
- Process alive check logic (`os.kill(pid, 0)`)

**Called From:**
- `core/vm_lifecycle.py:1443` → `remove_vm()` (graceful shutdown)
- `core/vm_lifecycle.py:1639` → `stop_vm()` (graceful shutdown with force option)

---

## Section 4: resources_created Rollback Spec

### Resource Tracking Dictionary

**Location:** `core/vm_lifecycle.py:954-961`

**Dictionary Structure:**
```python
resources_created = {
    "vm_dir": False,           # VM directory created
    "tap": False,              # TAP device created
    "network_ip": False,       # IP allocated from network
    "nocloud_server": False,   # NoCloud-net server started
    "firewall_rule": False,    # iptables firewall rule added
    "console_relay": False,    # Console relay service started
}
```

### Creation Order

1. **vm_dir** (line 1012): `_secure_mkdir_vm(vm_dir, name)` → `secure_mkdir()`
2. **network_ip** (line 1072): `allocate_network_ip(network_name, name)`
3. **tap** (line 1285): `create_tap(tap_name, bridge=bridge)`
4. **nocloud_server** (line 1202): `net_manager.start_server(...)` (if mode == NET)
5. **firewall_rule** (line 1205): `add_nocloud_input_rule(...)` (if mode == NET)
6. **console_relay** (line 1346): `relay_mgr.start_relay(...)` (if enable_console)

### Cleanup Function

**Location:** `core/vm_lifecycle.py:747-828` (`_cleanup_vm_creation_resources()`)

**Cleanup Sequence (Reverse Order):**

| Resource | Cleanup Function | Line | Condition |
|----------|------------------|------|-----------|
| `console_relay` | `relay_mgr.stop_relay(name, vm_id)` | 807 | `resources_created["console_relay"] and relay_mgr and vm_id` |
| `firewall_rule` | `remove_nocloud_input_rule(guest_ip, name, port)` | 788 | `resources_created["firewall_rule"] and guest_ip` |
| `nocloud_server` | `net_manager.stop_server(name, vm_id)` | 782 | `resources_created["nocloud_server"] and net_manager and vm_id` |
| `tap` | `cleanup_tap(tap_name, bridge=...)` | 794 | `resources_created["tap"] and tap_name` |
| `network_ip` | `release_network_ip(net_name, name)` | 800 | `resources_created["network_ip"]` |
| `vm_dir` | `shutil.rmtree(vm_dir)` | 825 | `resources_created["vm_dir"] and vm_dir.exists()` |

### Cleanup Helpers

**`cleanup_tap()` (lines 907-912):**
```python
def cleanup_tap(tap_name: str, bridge: str | None = None) -> None:
    try:
        remove_iptables_forward_rules(tap_name, bridge=bridge or BRIDGE_NAME)
        delete_tap(tap_name)
    except NetworkError:
        pass
```

**`_cleanup_vm_creation_resources()` Error Handling:**
- Logs warnings for each cleanup failure
- **Never raises** — cleanup is best-effort
- Continues cleanup even if one step fails
- Closes file handles (log_fp, console_fp) first
- Closes PTY file descriptors (pty_master_fd, pty_slave_fd) last

### MUST Be Replicated in `api/vms.py`

When moving `create_vm()` to API layer:

1. **Preserve resource tracking dictionary** with all 6 keys
2. **Preserve creation order** — resources must be created in exact sequence
3. **Preserve cleanup sequence** — reverse order, with exact same conditions
4. **Preserve error handling** — log warnings, never raise during cleanup
5. **Preserve file handle cleanup** — close log/console files before PTY
6. **Preserve network cleanup** — release IP before removing TAP
7. **Preserve firewall cleanup** — remove rules before TAP deletion

**Critical:** The cleanup function is called in 3 places:
- Line 1368: Typed exceptions (VMCreateError, NetworkError, CloudInitError, MVMError)
- Line 1387: FileNotFoundError
- Line 1406: Unexpected exceptions

All three must call `_cleanup_vm_creation_resources()` with identical parameters.

---

## Section 5: Network Manager Flattening Diagram

### BEFORE Refactor (Current State)

```
┌─────────────────────────────────────────────────────────────────┐
│                         api/vms.py                              │
│                    (VM Orchestrator)                            │
└─────────────────────────────────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│                   core/vm_lifecycle.py                          │
│              (VM Creation/Removal Logic)                        │
│                                                                 │
│  Calls:                                                         │
│  - ensure_default_network()                                    │
│  - get_network()                                               │
│  - allocate_network_ip()                                       │
│  - release_network_ip()                                        │
└─────────────────────────────────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│                 core/network_manager.py                         │
│            (Network Registry + IP Lease Tracking)              │
│                                                                 │
│  Calls:                                                         │
│  - setup_bridge()                                              │
│  - setup_nat()                                                 │
│  - teardown_bridge()                                           │
│  - teardown_nat()                                              │
│  - allocate_ip()                                               │
└─────────────────────────────────────────────────────────────────┘
                               │
                               ▼
┌─────────────────────────────────────────────────────────────────┐
│                    core/network.py                              │
│         (Low-Level: Bridge, TAP, NAT, iptables)               │
│                                                                 │
│  Functions:                                                     │
│  - setup_bridge()                                              │
│  - setup_nat()                                                 │
│  - create_tap()                                                │
│  - delete_tap()                                                │
│  - add_iptables_forward_rules()                                │
│  - remove_iptables_forward_rules()                             │
└─────────────────────────────────────────────────────────────────┘
```

**Problem:** `core/vm_lifecycle.py` imports from `core/network_manager.py`, which violates core module isolation. `vm_lifecycle` should not know about network manager internals.

---

### AFTER Refactor (Wave 1 Target)

```
┌─────────────────────────────────────────────────────────────────┐
│                         api/vms.py                              │
│                    (VM Orchestrator)                            │
│                                                                 │
│  Calls:                                                         │
│  - ensure_default_network()                                    │
│  - get_network()                                               │
│  - allocate_network_ip()                                       │
│  - release_network_ip()                                        │
│  - create_tap()                                                │
│  - delete_tap()                                                │
│  - add_iptables_forward_rules()                                │
│  - remove_iptables_forward_rules()                             │
└─────────────────────────────────────────────────────────────────┘
                    │                          │
        ┌───────────┴──────────┬───────────────┴──────────┐
        ▼                      ▼                          ▼
┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐
│ core/network_    │  │ core/network.py  │  │ core/vm_         │
│ manager.py       │  │                  │  │ lifecycle.py     │
│                  │  │ (Low-Level Ops)  │  │                  │
│ (Registry +      │  │                  │  │ (VM Creation/    │
│  IP Leases)      │  │ - setup_bridge() │  │  Removal Only)   │
│                  │  │ - setup_nat()    │  │                  │
│ - get_network()  │  │ - create_tap()   │  │ - create_vm()    │
│ - allocate_ip()  │  │ - delete_tap()   │  │ - remove_vm()    │
│ - release_ip()   │  │ - add_rules()    │  │ - start_vm()     │
│                  │  │ - remove_rules() │  │ - stop_vm()      │
└──────────────────┘  └──────────────────┘  └──────────────────┘
```

**Benefits:**
1. **API is the sole orchestrator** — sequences all network and VM operations
2. **Core modules are isolated** — `vm_lifecycle` no longer imports from `network_manager`
3. **Clear separation of concerns:**
   - `network_manager.py` = registry + IP lease tracking (metadata)
   - `network.py` = low-level bridge/TAP/NAT/iptables operations
   - `vm_lifecycle.py` = VM creation/removal logic (execution only)
4. **Easier testing** — API layer can mock all core modules independently
5. **Clearer data flow** — API passes explicit values to core modules

---

### Detailed Call Graph: VM Creation

**BEFORE (Current):**
```
api/vms.py:create_vm()
  └─ core/vm_lifecycle.py:create_vm()
      ├─ core/network_manager.py:ensure_default_network()
      │   ├─ core/network.py:setup_bridge()
      │   └─ core/network.py:setup_nat()
      ├─ core/network_manager.py:get_network()
      ├─ core/network_manager.py:allocate_network_ip()
      │   └─ core/network.py:allocate_ip()
      ├─ core/network.py:create_tap()
      ├─ core/network.py:add_iptables_forward_rules()
      └─ [VM process creation]
```

**AFTER (Wave 1):**
```
api/vms.py:create_vm()
  ├─ core/network_manager.py:ensure_default_network()
  │   ├─ core/network.py:setup_bridge()
  │   └─ core/network.py:setup_nat()
  ├─ core/network_manager.py:get_network()
  ├─ core/network_manager.py:allocate_network_ip()
  │   └─ core/network.py:allocate_ip()
  ├─ core/vm_lifecycle.py:create_vm()
  │   ├─ core/network.py:create_tap()
  │   ├─ core/network.py:add_iptables_forward_rules()
  │   └─ [VM process creation]
  └─ core/vm_manager.py:register()
```

**Key Difference:** API layer calls network setup functions directly, then passes network config to `core/vm_lifecycle.py:create_vm()`. Core no longer imports from `network_manager`.

---

### Detailed Call Graph: VM Removal

**BEFORE (Current):**
```
api/vms.py:remove_vm()
  └─ core/vm_lifecycle.py:remove_vm()
      ├─ core/network_manager.py:get_network()
      ├─ core/network_manager.py:release_network_ip()
      ├─ core/network.py:remove_iptables_forward_rules()
      ├─ core/network.py:delete_tap()
      └─ core/vm_manager.py:deregister()
```

**AFTER (Wave 1):**
```
api/vms.py:remove_vm()
  ├─ core/vm_manager.py:get()
  ├─ core/network_manager.py:get_network()
  ├─ core/vm_lifecycle.py:remove_vm()
  │   ├─ core/network.py:remove_iptables_forward_rules()
  │   ├─ core/network.py:delete_tap()
  │   └─ [cleanup operations]
  ├─ core/network_manager.py:release_network_ip()
  └─ core/vm_manager.py:deregister()
```

**Key Difference:** API layer orchestrates the sequence; core modules are called in isolation.

---

### Import Changes Required

**Remove from `core/vm_lifecycle.py`:**
```python
# REMOVE these imports
from mvmctl.core.network_manager import (
    allocate_network_ip,
    ensure_default_network,
    get_network,
    release_network_ip,
)
```

**Add to `api/vms.py`:**
```python
# ADD these imports
from mvmctl.core.network_manager import (
    allocate_network_ip,
    ensure_default_network,
    get_network,
    release_network_ip,
)
from mvmctl.core.network import (
    add_iptables_forward_rules,
    create_tap,
    delete_tap,
    remove_iptables_forward_rules,
    setup_bridge,
    setup_nat,
    teardown_nat,
)
```

---

## Implementation Checklist

### Phase 1: API Layer Refactoring

- [ ] Move `create_vm()` logic to `api/vms.py:create_vm()`
  - [ ] Add network setup orchestration
  - [ ] Add resource tracking and cleanup
  - [ ] Preserve signal handling code path
  - [ ] Preserve resource cleanup sequence
- [ ] Move `remove_vm()` logic to `api/vms.py:remove_vm()`
  - [ ] Add network cleanup orchestration
  - [ ] Preserve graceful shutdown sequence
- [ ] Move `start_vm()` logic to `api/vms.py:start_vm()`
- [ ] Move `stop_vm()` logic to `api/vms.py:stop_vm()`
- [ ] Update `api/network.py:ensure_default_network()` to orchestrate setup

### Phase 2: Core Layer Cleanup

- [ ] Remove network manager imports from `core/vm_lifecycle.py`
- [ ] Remove network setup calls from `core/vm_lifecycle.py:create_vm()`
- [ ] Remove network cleanup calls from `core/vm_lifecycle.py:remove_vm()`
- [ ] Verify `core/vm_lifecycle.py` receives explicit network config from API

### Phase 3: Testing

- [ ] Migrate `tests/unit/test_vm_lifecycle.py` to `tests/unit/test_api_vms.py`
- [ ] Update mocks to patch `api/vms.py` functions instead of core
- [ ] Add integration tests for API orchestration
- [ ] Verify layer compliance tests pass

### Phase 4: Verification

- [ ] Run `uv run ruff check src/`
- [ ] Run `uv run mypy src/`
- [ ] Run `uv run pytest tests/ -q --cov=src/mvmctl --cov-fail-under=80`
- [ ] Verify no cross-core imports remain
- [ ] Verify API layer is sole orchestrator

---

## References

- **VM Lifecycle:** `src/mvmctl/core/vm_lifecycle.py` (1762 lines)
- **Network Manager:** `src/mvmctl/core/network_manager.py` (758 lines)
- **Network Core:** `src/mvmctl/core/network.py` (1280 lines)
- **API VMs:** `src/mvmctl/api/vms.py` (927 lines)
- **API Network:** `src/mvmctl/api/network.py`
- **Config State:** `src/mvmctl/core/config_state.py` (243 lines)
- **Layer Compliance Tests:** `tests/layer_compliance/test_imports.py`

---

**Document Version:** 1.0  
**Last Updated:** 2026-04-06  
**Status:** Ready for implementation
