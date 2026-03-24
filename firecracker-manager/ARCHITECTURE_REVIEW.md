# Architectural Review: firecracker-manager (`fcm`)

**Reviewed:** All source files under `src/fcm/` (cli, api, core, models, utils)  
**Date:** 2026-03-24  
**Scope:** Component boundaries, dependency management, API design, data models, design patterns, architectural consistency, layer violations

---

## Executive Summary

The codebase follows the right *intent* — a clean layered CLI → api → core → models/utils architecture — but the implementation has significant gaps. The api/ layer is largely a facade-with-no-value-add; critical business logic leaks into both api/ and cli/; core models are scattered outside models/; and several utilities exist but are never used. The issues are concentrated and addressable; the overall structure is salvageable with targeted refactoring.

**Severity scale:** 🔴 High · 🟡 Medium · 🟢 Low

---

## 1. Layer Violations

### 1.1 🔴 `cli/key.py` Bypasses the api/ Layer

**Location:** `src/fcm/cli/key.py`, lines 8–14  
**Finding:** `cli/key.py` imports directly from `fcm.core.key_manager` (`add_key`, `create_key`, `inspect_key`, `list_keys`, `remove_key`). The `api/keys.py` module exists but is never called.

```python
# cli/key.py — current (violates architecture)
from fcm.core.key_manager import add_key, create_key, inspect_key, list_keys, remove_key

# should be
from fcm.api.keys import add_key, create_key, inspect_key, list_keys, remove_key
```

**Impact:** Any future change to `core/key_manager.py` signatures must be coordinated with CLI code directly. The api/ stability contract is broken — callers of the Python library API get different behaviour than the CLI.

**Recommendation:** Route all `cli/key.py` imports through `api/keys.py`. No logic changes are needed — `api/keys.py` is already a pass-through.

---

### 1.2 🔴 `cli/host.py` Bypasses the api/ Layer

**Location:** `src/fcm/cli/host.py`  
**Finding:** `cli/host.py` imports directly from `fcm.core.host`, `fcm.core.host_setup`, and `fcm.core.host_privilege`. The `api/host.py` module exists but is not used.

**Impact:** Same as 1.1. The entire host CLI surface (init, reset, clean, ls) is coupled directly to core implementation details.

**Recommendation:** Route all CLI host imports through `api/host.py`.

---

### 1.3 🔴 `cli/network.py` Bypasses the api/ Layer

**Location:** `src/fcm/cli/network.py`  
**Finding:** `cli/network.py` imports `get_iptables_rules_for_bridge` directly from `fcm.core.network`, and calls `fcm.core.network_manager` functions directly — bypassing `api/network.py`.

**Impact:** `get_iptables_rules_for_bridge` is an implementation detail of the network subsystem. Exposing it directly in the CLI creates a hard coupling to the iptables querying mechanism; switching to a different inspection method would require CLI changes.

**Recommendation:** Move the `inspect_network` composite logic (currently partly in `api/network.py` but returning `dict`) into `api/network.py` as a proper typed function, and route the CLI through it exclusively.

---

### 1.4 🔴 `cli/config.py` Calls `core/config.py` Directly

**Location:** `src/fcm/cli/config.py`, line 8  
**Finding:** `cli/config.py` imports `load_config`, `validate_config`, `dump_config` directly from `fcm.core.config`, bypassing any potential api/ layer.

**Impact:** No `api/config.py` wrapper exists, making `core/config.py` directly exposed to CLI. Moderate risk since config operations are read-only, but still violates the stated architecture.

**Recommendation:** Create a thin `api/config.py` wrapper or fold config access into `api/host.py`. The CLI should never import from `core/` directly.

---

### 1.5 🔴 Business Logic in `api/vms.py::cleanup_vms()`

**Location:** `src/fcm/api/vms.py`, `cleanup_vms()` function  
**Finding:** `cleanup_vms()` directly calls `os.kill(pid, signal.SIGKILL)`, `shutil.rmtree()`, `remove_iptables_forward_rules()`, and `delete_tap()`. These are core-level system operations embedded in the api/ layer.

```python
# api/vms.py — should NOT be here
os.kill(pid, signal.SIGKILL)
shutil.rmtree(vm_dir, ignore_errors=True)
remove_iptables_forward_rules(tap_name, bridge_name)
delete_tap(tap_name)
```

**Impact:** The api/ layer is supposed to delegate to core/ for all system operations. Having signal sending, filesystem destruction, and iptables manipulation in api/ makes it untestable without mocking OS-level calls and makes the layer boundary meaningless.

**Recommendation:** Extract cleanup logic into `core/vm_lifecycle.py` as a `cleanup_vm(vm: VMInstance)` function. `api/vms.py::cleanup_vms()` should orchestrate by calling this core function per VM.

---

### 1.6 🟡 Business Logic in `cli/vm.py::cleanup()`

**Location:** `src/fcm/cli/vm.py`, `cleanup()` command  
**Finding:** The CLI `cleanup()` function contains pre-flight filtering logic:

```python
# cli/vm.py — filtering logic in CLI layer
vms_to_clean = [v for v in vms if v.status != VMState.RUNNING]
```

And it also prints each VM during iteration — mixing presentation with flow control.

**Impact:** The "which VMs qualify for cleanup" decision is business logic that belongs in the api/ or core/ layer. If a second caller (e.g. a Python script using the library) calls `api.cleanup_vms()`, it gets different filtering behaviour than the CLI.

**Recommendation:** Move the filtering predicate into `api/vms.py::cleanup_vms()` (as a parameter or default behaviour). CLI should only handle confirmation prompts and output formatting.

---

### 1.7 🟡 `cli/configure.py` Spawns a CLI Subprocess

**Location:** `src/fcm/cli/configure.py`  
**Finding:** The setup wizard calls `subprocess.run(["sudo", fcm_bin, "host", "init"])` — a CLI module spawning another CLI invocation as a subprocess.

**Impact:** This is fragile: it relies on the `fcm` binary being on `$PATH` or correctly resolved, spawns a new process tree, and makes the wizard untestable (any test of the wizard would actually attempt to run `sudo fcm host init`). The correct approach is to call the api/ function directly.

**Recommendation:** Replace the subprocess call with a direct call to `api/host.py::init_host()`. Handle the privilege escalation requirement in the api layer by raising `PrivilegeError` when not root, and let the wizard catch and report it cleanly.

---

### 1.8 🟢 `cli/config.py::dump_vm()` Contains Filesystem Logic

**Location:** `src/fcm/cli/config.py`, lines 71–85  
**Finding:** `dump_vm()` resolves the VM dir via `get_vm_dir(name)`, constructs the config file path, opens and reads the file, and parses JSON — all within the CLI command handler.

**Impact:** Low: this is read-only and simple. But it duplicates filesystem path resolution that belongs in core/ and makes the CLI harder to test without a real filesystem.

**Recommendation:** Add a `get_vm_firecracker_config(name: str) -> dict` function to `core/config_gen.py` or `core/vm_manager.py` and call it from the CLI.

---

## 2. API Layer Design

### 2.1 🔴 Three api/ Modules Are Pure Re-export Facades

**Location:** `src/fcm/api/host.py`, `api/keys.py`, `api/network.py`  
**Finding:** All three modules are structurally identical: they import everything from the corresponding core module and re-export it with no transformation, no composite logic, and no added error handling.

```python
# api/host.py — entire module (50 lines, zero added value)
from fcm.core.host import clean_host, prune_host, reset_host
from fcm.core.host_setup import init_host, get_host_status
from fcm.core.host_privilege import ensure_fcm_group, add_user_to_fcm_group
```

**Impact:** Callers that import from `api/` get no stability guarantee — any `core/` change immediately breaks the api/ surface. The indirection adds maintenance overhead (keeping re-exports in sync) with zero benefit. Yet both `api/assets.py` and `api/vms.py` do add real value — the inconsistency makes the api/ contract unpredictable.

**Recommendation:** Either (a) make every api/ module add substantive value (validation, error translation, composite operations), or (b) collapse the pure re-exports and accept that `core/` is the public API for those subsystems. Option (a) is preferred since the architecture doc commits to a stable api/ layer.

---

### 2.2 🟡 `inspect_network()` Returns `dict` Instead of a Typed Model

**Location:** `src/fcm/api/network.py`  
**Finding:** `inspect_network()` returns a raw `dict` instead of a typed dataclass. All other api/ functions return typed models or primitives.

**Impact:** Callers must guess the dict shape; changes to the dict structure are not caught by mypy; the `--json` flag in the CLI must trust that the dict is JSON-serializable.

**Recommendation:** Define a `NetworkInspection` dataclass in `models/` (or at minimum in `api/network.py`) and return it from `inspect_network()`.

---

### 2.3 🟡 `cleanup_vms()` Return Value Is Ignored by All Callers

**Location:** `src/fcm/api/vms.py`, `src/fcm/cli/vm.py`  
**Finding:** `cleanup_vms()` returns `list[VMInstance]` (the cleaned VMs) but the CLI ignores the return value and generates its own count from a pre-call `vms_to_clean` list.

**Impact:** Caller has to maintain parallel state to know what was actually cleaned. This is also evidence of the CLI duplicating logic that should be in the api.

**Recommendation:** Once the filtering logic is moved into the api layer (see 1.6), have the CLI use the returned list for output formatting.

---

### 2.4 🟢 `api/assets.py` Is Exemplary — Inconsistently Applied

**Location:** `src/fcm/api/assets.py`  
**Finding:** `api/assets.py` is the only api/ module that genuinely orchestrates across core modules — it provides parallel fetching, composite image+kernel operations, and cache-clearing logic. This is exactly what the api/ layer should do.

**Impact:** Positive finding, but highlights how inconsistent the other api/ modules are by contrast.

**Recommendation:** Use `api/assets.py` as the reference implementation when redesigning the other api/ modules.

---

## 3. Data Model Placement

### 3.1 🔴 Core Models Defined Outside `models/`

**Location:** Various  
**Finding:** Multiple domain model dataclasses are defined inside core/ modules rather than in `models/`:

| Dataclass | Current Location | Should Be In |
|-----------|-----------------|--------------|
| `NetworkConfig` | `core/network_manager.py` | `models/network.py` |
| `NetworkLease` | `core/network_manager.py` | `models/network.py` |
| `HostChange` | `core/host_state.py` | `models/host.py` |
| `HostState` | `core/host_state.py` | `models/host.py` |
| `KeyInfo` | `core/key_manager.py` | `models/key.py` |
| `BinaryVersion` | `core/binary_manager.py` | `models/binary.py` |
| `FCMConfig` / sub-configs | `core/config.py` | `models/config.py` |

**Impact:** The models/ directory promises to be the "single source of truth for typed data." When core modules define their own models, consumers of those models must import from core/ rather than models/, creating coupling between consumers and implementation modules. It also makes it impossible to import models without pulling in business logic.

**Recommendation:** Create `models/network.py`, `models/host.py`, `models/key.py`, `models/binary.py`, and `models/config.py`. Move the dataclasses there. Core modules import from models/; models/ imports from nowhere in the project.

---

### 3.2 🟡 `VMInstance.config: VMConfig | None = None` Is a Dead Field

**Location:** `src/fcm/models/vm.py`  
**Finding:** `VMInstance` has a `config: VMConfig | None = None` field. Grepping the codebase shows it is never set to a non-None value after construction. The `VMManager` loads state from JSON and never populates this field.

**Impact:** Any code that reads `instance.config` to make decisions gets `None` and must either fail or fall back. This is dead weight that misleads future developers.

**Recommendation:** Either populate the field by loading `firecracker.json` during `VMManager.get()`/`list()`, or remove it if the Firecracker config is only needed on-demand (via `core/config_gen.py`).

---

### 3.3 🟢 `ImageSpec` in `models/image.py` Is Well-Designed

**Location:** `src/fcm/models/image.py`  
**Finding:** `ImageSpec` is a clean, pure dataclass with no dependencies on core or utils. It correctly belongs in models/.

**Impact:** Positive — no action needed.

---

## 4. Dependency Direction

### 4.1 🔴 `utils/process.py` Is Dead Code

**Location:** `src/fcm/utils/process.py`  
**Finding:** `utils/process.py` defines `run_cmd()` and `stream_cmd()` — typed, error-handling wrappers around `subprocess.run`. However, **zero** core modules import or use it. Every core module calls `subprocess.run()` directly with manual error handling.

```
utils/process.py  ←  (no imports from core/)
core/network.py   →  subprocess.run(...)  (direct, no wrapper)
core/vm_lifecycle.py  →  subprocess.run(...)  (direct)
core/image.py  →  subprocess.run(...)  (direct)
... (all core modules)
```

**Impact:** The abstraction was designed but never adopted. This means subprocess error handling is inconsistent across core modules — some capture stderr, some don't; some set `text=True`, some don't; error messages vary in quality. The utility exists to solve exactly these inconsistencies.

**Recommendation:** Migrate all `subprocess.run()` calls in core/ to use `run_cmd()` / `stream_cmd()`. Delete any duplicated error-handling boilerplate. This is a significant but mechanical refactor with high testability benefits.

---

### 4.2 🟡 `utils/audit.py` Reimplements Cache Dir Resolution

**Location:** `src/fcm/utils/audit.py`  
**Finding:** `audit.py` resolves the audit log path using its own inline logic instead of calling `utils/fs.get_cache_dir()`. If `FCM_CACHE_DIR` or the path derivation logic in `fs.py` ever changes, audit logs will diverge to a different location.

**Impact:** Potential audit log loss (logs written to a different directory than expected) and violation of DRY.

**Recommendation:** Replace the inline path resolution in `audit.py` with a call to `get_cache_dir()` from `utils/fs.py`.

---

### 4.3 🟡 `core/cloud_init.py` Has a Hardcoded Default Gateway

**Location:** `src/fcm/core/cloud_init.py`, line 19  
**Finding:** `write_cloud_init()` has `gateway: str = "10.20.0.1"` as a default parameter. This is the multi-VM bridge IP, which is also defined in `constants.py` / `core/config.py`. The hardcoded default can silently create misconfigured VMs if someone creates a network with a different gateway.

**Impact:** Silent networking misconfiguration. VMs on custom networks (e.g. `192.168.100.0/24`) will have cloud-init network-config pointing to the wrong gateway.

**Recommendation:** Remove the default value and require the caller to always pass the gateway explicitly. The calling site in `core/vm_lifecycle.py` already knows the network's gateway from the `NetworkConfig`.

---

### 4.4 🟢 Dependency Direction Within core/ Is Generally Clean

**Location:** `src/fcm/core/`  
**Finding:** Core modules import from utils/ and models/ correctly. Peer-to-peer imports between core modules (e.g. `ssh.py` importing from `key_manager.py`) are limited and logical.

**Impact:** No action needed.

---

## 5. Design Patterns

### 5.1 🔴 `VMManager` Is Reconstructed on Every Call — No Registry Pattern

**Location:** `src/fcm/api/vms.py`, `src/fcm/core/vm_manager.py`  
**Finding:** Every api/ function that needs the VM registry calls `get_vm_manager()`, which constructs a new `VMManager` instance and re-reads `state.json` from disk. There is no caching, no singleton, and no injection.

```python
# api/vms.py — every function does this
def list_vms(...) -> list[VMInstance]:
    manager = get_vm_manager()   # new instance, new disk read
    return manager.list(...)

def get_vm(name: str) -> VMInstance:
    manager = get_vm_manager()   # another new instance, another disk read
    return manager.get(name)
```

**Impact:** A single `fcm vm ls` invocation reads `state.json` once; a call that lists and then gets a specific VM reads it twice. For now this is fast, but the pattern doesn't scale and creates subtle race conditions: two calls in a pipeline could see different state if a VM is being created/destroyed concurrently.

**Recommendation:** Pass a `VMManager` instance as a parameter to api/ functions, or use a session-scoped singleton (`get_vm_manager()` returns a cached instance per process lifetime). Dependency injection is preferable for testability.

---

### 5.2 🔴 `create_vm()` Is a 200+ Line God Function

**Location:** `src/fcm/core/vm_lifecycle.py`, `create_vm()`  
**Finding:** `create_vm()` is a single 200+ line function that orchestrates: rootfs copy, cloud-init seed file generation, cloud-init ISO creation, cloud-init injection, bridge setup, TAP creation, iptables rules, Firecracker JSON config generation, process launch, PID file writing, VM registration, and audit logging. It has no sub-functions for each phase.

**Impact:** The function is nearly impossible to unit-test meaningfully — you must mock 10+ different subsystems simultaneously to test any one part of it. Any failure partway through leaves partial state with no rollback mechanism. Adding a new VM creation step requires editing this already-complex function.

**Recommendation:** Decompose into named phases:
```python
def create_vm(config: VMConfig) -> VMInstance:
    rootfs = _prepare_rootfs(config)
    cloud_init = _prepare_cloud_init(config)
    network = _setup_network(config)
    fc_config = _generate_firecracker_config(config, rootfs, network)
    process = _launch_firecracker(fc_config)
    return _register_vm(config, process, network)
```
Each phase function is independently testable and independently rollback-able.

---

### 5.3 🟡 `setup_nat()` Uses `shell=True` with f-string Interpolation

**Location:** `src/fcm/core/network.py`, `setup_nat()` and `add_iptables_forward_rules()`  
**Finding:** Several iptables calls use `shell=True` with f-strings:

```python
subprocess.run(
    f"iptables -t nat -A POSTROUTING -s {ip_range} -o {iface} -j MASQUERADE",
    shell=True, ...
)
```

While `ip_range` and `iface` are derived from validated config values (not user input), `shell=True` is an anti-pattern for any subprocess call that accepts external-derived values. The TAP name contains the VM name, which comes from CLI input (validated but still passed through).

**Impact:** Low exploitability given current validation, but shell injection is possible if validation is ever weakened or bypassed. The `shell=True` form also loses the ability to distinguish between "command not found" and "command failed."

**Recommendation:** Replace all `shell=True` iptables calls with list-form arguments:
```python
subprocess.run(
    ["iptables", "-t", "nat", "-A", "POSTROUTING", "-s", ip_range, "-o", iface, "-j", "MASQUERADE"],
    check=True, ...
)
```

---

### 5.4 🟡 Default Network Is Never Persisted — Silent Inconsistency

**Location:** `src/fcm/core/network_manager.py`, `create_network()`  
**Finding:** When `create_network()` is called with `DEFAULT_NETWORK_NAME`, it returns early with a hardcoded `NetworkConfig` and never writes anything to the networks directory. As a result, `list_networks()` will never return the default network.

```python
if name == DEFAULT_NETWORK_NAME:
    # Returns hardcoded config, no disk write
    return NetworkConfig(name=DEFAULT_NETWORK_NAME, ...)
```

**Impact:** `fcm network ls` never shows the default network, even when VMs are using it. Users cannot inspect or manage the default network through the normal network commands. This is a confusing user experience and a silent divergence from the stated behaviour.

**Recommendation:** On first use of the default network (or during `host init`), persist the default `NetworkConfig` to disk. Alternatively, make `list_networks()` explicitly include the default network as a synthetic entry.

---

### 5.5 🟢 `constants.py` Design Is Strong

**Location:** `src/fcm/constants.py`  
**Finding:** Using `lru_cache` + `importlib.metadata` to derive project-wide constants from `pyproject.toml` is an excellent pattern. The single-source-of-truth approach for `CLI_NAME`, `CACHE_DIR`, device prefixes, and env var prefixes is correct and well-executed.

**Minor issue:** `HTTP_USER_AGENT` hardcodes `"0.1.0"` instead of using the resolved `get_version()`:
```python
HTTP_USER_AGENT = f"{CLI_NAME}/0.1.0"  # should be f"{CLI_NAME}/{get_version()}"
```

**Recommendation:** Replace the hardcoded version in `HTTP_USER_AGENT` with `get_version()`.

---

## 6. Architectural Consistency

### 6.1 🔴 Audit Logging Is Inconsistently Applied

**Location:** Various `cli/*.py` files  
**Finding:** Audit logging via `log_audit()` is called in `cli/vm.py` (create, remove) and `cli/host.py` (init, reset) — but is entirely absent from:
- All network operations (`cli/network.py`)
- All key operations (`cli/key.py`)
- All asset operations (`cli/asset.py`)
- VM cleanup, pause, resume, snapshot, load

**Impact:** The audit trail is incomplete and unreliable. An operator using the audit log to reconstruct what happened will see VM creates/removes but miss network changes and key management — precisely the operations most relevant to security audits.

**Recommendation:** Move audit logging into the api/ layer so it fires for every operation regardless of how it is invoked (CLI, Python library, or future REST API). Every api/ function that mutates state should call `log_audit()`.

---

### 6.2 🔴 `cli/configure.py` Contains Substantial Business Logic (382 Lines)

**Location:** `src/fcm/cli/configure.py`  
**Finding:** The wizard contains 382 lines of step orchestration, download decisions, key selection logic, and system state assessment — all in a CLI module. This includes:
- Logic to decide whether host init has been run
- Logic to find the "best" key from the cache
- Logic to enumerate available images and select a default
- Multi-step workflow coordination with state carried between steps

**Impact:** This business logic is not accessible to non-CLI callers. A programmatic setup flow (e.g. a test fixture, a CI bootstrap script, or a future `api/setup.py`) cannot use any of this logic. It also inflates `cli/configure.py` to 7× the recommended CLI module size.

**Recommendation:** Create `api/setup.py` (or `core/setup.py`) with functions:
- `get_setup_status() -> SetupStatus`
- `run_setup(options: SetupOptions) -> SetupResult`

The CLI `configure.py` becomes a thin wrapper that calls these functions and formats output.

---

### 6.3 🟡 Inline IP Regex Duplicated Between `cli/vm.py` and `core/ssh.py`

**Location:** `src/fcm/cli/vm.py` and `src/fcm/core/ssh.py`  
**Finding:** Both modules contain an independent regex for detecting whether a VM identifier is an IP address vs a name:

```python
# cli/vm.py
re.match(r"^\d+\.\d+\.\d+\.\d+$", name)

# core/ssh.py (same pattern)
re.match(r"^\d+\.\d+\.\d+\.\d+$", name)
```

**Impact:** If the detection logic needs to change (e.g. to support IPv6), it must be updated in two places.

**Recommendation:** Consolidate into `utils/validation.py` as `is_ip_address(value: str) -> bool` and import from both callsites.

---

### 6.4 🟡 `cli/config.py` Has an Import Order Violation

**Location:** `src/fcm/cli/config.py`, lines 1–4  
**Finding:**
```python
from fcm.exceptions import FCMError   # line 3
import json                            # line 4 (stdlib should come before project imports)
import typer
```

This is a minor style issue but would fail `ruff` import ordering checks.

**Impact:** CI lint failure.

**Recommendation:** Reorder imports: stdlib first, then third-party, then project-local.

---

### 6.5 🟢 Exception Hierarchy Is Well-Structured

**Location:** `src/fcm/exceptions.py`  
**Finding:** The exception hierarchy (`FCMError → HostError, NetworkError, VMError, ImageError, KernelError, ...`) is comprehensive and correctly used throughout core/. No bare `except:` clauses were found.

**Impact:** Positive — no action needed.

---

## 7. Dependency Management

### 7.1 🟡 `core/config.py` Dataclasses Duplicate Constants

**Location:** `src/fcm/core/config.py`  
**Finding:** `VMDefaultsConfig` hardcodes default values (`vcpu_count=2`, `mem_size_mib=2048`, `boot_args="console=ttyS0 ..."`) that are also present in `constants.py` and `defaults.yaml`. There are now three sources of truth for VM defaults:
1. `constants.py` (e.g. `DEFAULT_VCPU_COUNT`)
2. `core/config.py` dataclass field defaults
3. `src/fcm/assets/defaults.yaml`

**Impact:** If `defaults.yaml` is changed, the dataclass defaults don't automatically update (and vice versa). A developer reading the code can't tell which is authoritative.

**Recommendation:** Remove inline default values from `VMDefaultsConfig` dataclass fields. The dataclass should only carry the *loaded* values; defaults should come exclusively from `defaults.yaml` (loaded by `load_config()`). This makes `constants.py` the only place to reference defaults in code.

---

### 7.2 🟡 `core/kernel.py::run_make()` Ignores Return Code on Non-Capture Path

**Location:** `src/fcm/core/kernel.py`, `run_make()`  
**Finding:**
```python
else:
    returncode = subprocess.run(cmd, cwd=kernel_dir).returncode
    return returncode, "", ""
```

When `capture_output=False`, the return code is returned but stderr is discarded (empty string). The callers check `returncode != 0` and raise `KernelError("Kernel build failed")` — but the user sees no error message because stderr wasn't captured.

**Impact:** Failed kernel builds produce unhelpful error messages ("Kernel build failed" with no context).

**Recommendation:** Always capture stderr. Stream it live to the terminal via `print_info()` if desired, but retain it for error reporting.

---

### 7.3 🟢 `utils/http.py` Is Correctly Shared

**Location:** `src/fcm/utils/http.py`  
**Finding:** `download_file()` is correctly used by `core/image.py`, `core/kernel.py`, and `core/binary_manager.py`. Single implementation, consistent SHA-256 verification, consistent timeout handling.

**Impact:** Positive — good shared utility usage.

---

## Summary Table

| # | Finding | Layer | Severity |
|---|---------|-------|----------|
| 1.1 | `cli/key.py` bypasses api/ layer | Layer Violation | 🔴 High |
| 1.2 | `cli/host.py` bypasses api/ layer | Layer Violation | 🔴 High |
| 1.3 | `cli/network.py` bypasses api/ layer | Layer Violation | 🔴 High |
| 1.4 | `cli/config.py` imports from core/ directly | Layer Violation | 🔴 High |
| 1.5 | Business logic (kill, rmtree, iptables) in `api/vms.py` | Layer Violation | 🔴 High |
| 1.6 | Filtering logic in `cli/vm.py::cleanup()` | Layer Violation | 🟡 Medium |
| 1.7 | `cli/configure.py` spawns CLI subprocess | Layer Violation | 🟡 Medium |
| 1.8 | Filesystem logic in `cli/config.py::dump_vm()` | Layer Violation | 🟢 Low |
| 2.1 | Three api/ modules are pure re-export facades | API Design | 🔴 High |
| 2.2 | `inspect_network()` returns untyped `dict` | API Design | 🟡 Medium |
| 2.3 | `cleanup_vms()` return value ignored by caller | API Design | 🟡 Medium |
| 3.1 | Domain models defined inside core/ modules | Data Model | 🔴 High |
| 3.2 | `VMInstance.config` field is always `None` (dead) | Data Model | 🟡 Medium |
| 4.1 | `utils/process.py` is dead code — never used | Dependencies | 🔴 High |
| 4.2 | `utils/audit.py` reimplements cache dir resolution | Dependencies | 🟡 Medium |
| 4.3 | `cloud_init.py` hardcodes default gateway | Dependencies | 🟡 Medium |
| 5.1 | `VMManager` reconstructed on every api/ call | Design Patterns | 🔴 High |
| 5.2 | `create_vm()` is a 200+ line god function | Design Patterns | 🔴 High |
| 5.3 | `shell=True` with f-string interpolation in iptables calls | Design Patterns | 🟡 Medium |
| 5.4 | Default network never persisted to disk | Design Patterns | 🟡 Medium |
| 5.5 | `HTTP_USER_AGENT` hardcodes version string | Design Patterns | 🟢 Low |
| 6.1 | Audit logging absent from network/key/asset operations | Consistency | 🔴 High |
| 6.2 | `cli/configure.py` has 382 lines of business logic | Consistency | 🔴 High |
| 6.3 | IP regex duplicated in cli/ and core/ | Consistency | 🟡 Medium |
| 6.4 | Import order violation in `cli/config.py` | Consistency | 🟡 Medium |
| 7.1 | VM defaults defined in 3 places | Dependencies | 🟡 Medium |
| 7.2 | `kernel.py::run_make()` discards stderr on non-capture path | Dependencies | 🟡 Medium |

---

## Prioritised Remediation Roadmap

### Phase 1 — Fix Layer Violations (1–2 days)
1. Route `cli/key.py`, `cli/host.py`, `cli/network.py`, `cli/config.py` through their respective api/ modules (findings 1.1–1.4)
2. Move `cleanup_vms()` system operations into `core/vm_lifecycle.py` (finding 1.5)
3. Fix import order in `cli/config.py` (finding 6.4) — CI is likely failing

### Phase 2 — API Layer Remediation (2–3 days)
4. Give `api/host.py`, `api/keys.py`, `api/network.py` substantive value-add (finding 2.1)
5. Define `NetworkInspection` dataclass; type-annotate `inspect_network()` return (finding 2.2)
6. Move audit logging into api/ layer functions (finding 6.1)

### Phase 3 — Model Consolidation (1–2 days)
7. Create `models/network.py`, `models/host.py`, `models/key.py`, `models/binary.py`, `models/config.py` (finding 3.1)
8. Resolve `VMInstance.config` dead field (finding 3.2)

### Phase 4 — Core Refactoring (3–5 days)
9. Decompose `create_vm()` into phases (finding 5.2)
10. Adopt `utils/process.py` across all core/ modules (finding 4.1)
11. Replace `shell=True` iptables calls with list-form arguments (finding 5.3)
12. Fix default network persistence (finding 5.4)
13. Extract `cli/configure.py` business logic into `api/setup.py` (finding 6.2)

### Phase 5 — Polish (1 day)
14. Fix `utils/audit.py` cache dir resolution (finding 4.2)
15. Remove hardcoded gateway default from `cloud_init.py` (finding 4.3)
16. Fix `HTTP_USER_AGENT` version string (finding 5.5)
17. Consolidate IP regex into `utils/validation.py` (finding 6.3)
18. Consolidate VM defaults to single source of truth (finding 7.1)
19. Fix `kernel.py::run_make()` stderr handling (finding 7.2)
