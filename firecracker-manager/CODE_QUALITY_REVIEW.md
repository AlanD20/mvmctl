# Code Quality Review — firecracker-manager

**Scope:** All source files in `src/fcm/`  
**Date:** 2026-03-24  
**Baseline:** 764 tests passing, 83.09% branch coverage, mypy strict clean, ruff clean  
**Focus:** Real bugs, correctness issues, and maintainability risks — not cosmetic style

---

## Summary

| Severity | Count |
|----------|-------|
| Critical | 2 |
| High | 6 |
| Medium | 13 |
| Low | 7 |
| **Total** | **28** |

---

## Critical

### C-1 · Hardcoded TAP prefix causes silent network leak on `cleanup_vms()`

**File:** `src/fcm/api/vms.py:103`

```python
tap_name = f"fc-{v.name}-0"   # BUG: prefix is "fc-", not the correct "fcm-tap"
```

`cleanup_vms()` constructs the TAP device name using the hardcoded prefix `fc-`. The actual prefix defined in `constants.py` is `TAP_PREFIX = "fcm-tap"`, and devices are created as `fcm-tap-{name}-0`. Because the names never match, `cleanup_vms()` silently skips every TAP device deletion — leaking kernel network interfaces on every cleanup call. The leak persists until reboot or manual `ip link delete`.

**Fix:**
```python
from fcm.constants import TAP_PREFIX
tap_name = f"{TAP_PREFIX}-{v.name}-0"
```

---

### C-2 · File handles leak on `Popen` failure path in `create_vm()`

**File:** `src/fcm/core/vm_lifecycle.py:302–318`

```python
log_fp = open(log_path, "w")      # opened
console_fp = open(console_path, "w")  # opened
try:
    proc = subprocess.Popen([...], stdout=log_fp, stderr=log_fp)
except (FileNotFoundError, OSError) as e:
    shutil.rmtree(vm_dir, ignore_errors=True)
    raise VMError(...) from e      # log_fp and console_fp never closed
```

If `Popen` raises (e.g., Firecracker binary not found), the cleanup block removes the VM directory but never closes the two file objects. On CPython the garbage collector will eventually collect them, but under resource pressure or in tight loops this leaks file descriptors and may leave files locked.

**Fix:** Use a `try/finally` or nest as context managers:
```python
with open(log_path, "w") as log_fp, open(console_path, "w") as console_fp:
    try:
        proc = subprocess.Popen([...], stdout=log_fp, stderr=log_fp)
    except (FileNotFoundError, OSError) as e:
        shutil.rmtree(vm_dir, ignore_errors=True)
        raise VMError(...) from e
```

---

## High

### H-1 · Default network never persisted — `ensure_default_network()` is a no-op

**File:** `src/fcm/core/network_manager.py:179–186`

```python
def create_network(name: str, ...) -> NetworkConfig:
    if name == DEFAULT_NETWORK_NAME:
        return NetworkConfig(name=name, cidr=cidr, ...)  # ← returns here
    # _save_config() and _save_leases() are only reached for non-default networks
    ...
    _save_config(name, config)
    _save_leases(name, [])
    return config
```

When `name == DEFAULT_NETWORK_NAME`, `create_network()` returns a freshly constructed `NetworkConfig` without ever writing it to disk. As a result:
- `get_network("default")` reads from disk, finds nothing, and returns `None`.
- `ensure_default_network()` calls `create_network("default")` again every time it runs.
- The default network object is constructed on every call but never owned or persisted.

Any code path relying on `get_network("default")` returning a real config (e.g., IP allocation) silently operates on `None` and will crash or silently allocate from nowhere.

**Fix:** Remove the early-return branch; let the default network follow the same persist-then-return path as all other networks.

---

### H-2 · Race condition: concurrent IP allocation can assign duplicate IPs

**File:** `src/fcm/core/network_manager.py` — `allocate_network_ip()` and `release_network_ip()`

Both functions read leases from disk, mutate the in-memory list, and write back — with no file locking. Two concurrent `fcm vm create` invocations can both read the same leases state, both allocate the same next IP, and both write back, resulting in two VMs with identical IPs on the same bridge.

**Fix:** Wrap both functions with a `fcntl.flock`-based advisory lock on the leases file:
```python
import fcntl

with open(leases_path, "r+") as f:
    fcntl.flock(f, fcntl.LOCK_EX)
    leases = json.load(f)
    # mutate ...
    f.seek(0)
    json.dump(leases, f)
    f.truncate()
```

---

### H-3 · `shell=True` with user-controlled data in NAT and forwarding rule setup

**File:** `src/fcm/core/network.py:184–189`, `305–309`

`setup_nat()` and `add_iptables_forward_rules()` construct multi-line shell scripts via f-strings containing `host_iface`, `bridge`, and `tap_name`, then execute them with `subprocess.run(..., shell=True, executable="/bin/bash")`. `tap_name` is derived from the user-supplied VM name; `host_iface` is parsed from `ip route` output.

The name validation regex (`^[a-z0-9][a-z0-9._-]{0,30}$`) prevents obvious injection, but:
1. The pattern is a belt-and-suspenders guarantee, not an architectural one — a future regex relaxation silently re-opens injection.
2. `host_iface` from route output is never validated against the name regex.

**Fix:** Replace the shell-script approach with per-rule `iptables` calls using list arguments:
```python
subprocess.run(
    ["iptables", "-t", "nat", "-A", "POSTROUTING",
     "-s", cidr, "-o", host_iface, "-j", "MASQUERADE"],
    check=True,
)
```

---

### H-4 · Bridge name collision with no detection

**File:** `src/fcm/core/network_manager.py:50–57`

```python
truncated = network_name[:8]
bridge_name = f"fcm-{truncated}"  # e.g. "fcm-mysuper" for "mysupernetwork"
```

Two different network names that share the first 8 characters (e.g., `mysupernetworkA` and `mysupernetworkB`) produce the same bridge name `fcm-mysuper`. There is no collision check before writing the config or creating the bridge device. The second `create_network()` call will silently overwrite the first network's config file and attempt to create a bridge that already exists.

**Fix:** After computing `bridge_name`, check for collisions across existing network configs before proceeding:
```python
existing = [n for n in list_networks() if n.bridge_name == bridge_name]
if existing:
    raise NetworkError(
        f"Bridge name '{bridge_name}' already in use by network '{existing[0].name}'. "
        "Choose a shorter or different network name."
    )
```

---

### H-5 · `assert` in production code stripped by `-O`

**File:** `src/fcm/core/image.py:261`

```python
assert isinstance(parsed, tuple)
```

`assert` statements are removed when Python is run with the `-O` (optimize) flag. PyInstaller-built binaries frequently use `-O` optimizations. If `parsed` is not a `tuple` in a production binary, this silently continues rather than raising, leading to an `AttributeError` or wrong data downstream.

**Fix:**
```python
if not isinstance(parsed, tuple):
    raise ImageError(f"Unexpected parse result type: {type(parsed).__name__}")
```

---

### H-6 · Inconsistent memory limit: `VMConfig` rejects what `create_vm()` allows

**File:** `src/fcm/models/vm.py:59` vs `src/fcm/core/vm_lifecycle.py:148`

`VMConfig.__post_init__` enforces `mem_size_mib <= 32768` (32 GiB). `create_vm()` enforces `mem <= 65536` (64 GiB). A user requesting `--mem 40000` passes the `create_vm()` guard but fails inside `VMConfig.__post_init__` with a confusing error, never reaching the Firecracker config stage. The two limits are contradictory and the lower one is the effective cap, but it is buried in a model rather than documented or surfaced to the user.

**Fix:** Align both checks to the same value (the real hardware/Firecracker limit), and document it. Either raise the `VMConfig` limit to 65536 or lower the `create_vm()` guard to 32768 and add a comment explaining why.

---

## Medium

### M-1 · Swallowed YAML parse error gives user no feedback

**File:** `src/fcm/core/cloud_init.py:53–54`

```python
try:
    extra = yaml.safe_load(user_data_content) or {}
except yaml.YAMLError:
    extra = {}  # silently ignored
```

A user who passes a malformed `--user-data` file gets no indication that their file was invalid. The VM starts with default cloud-init config, which may look like success. At minimum this should `logger.warning(...)` the parse error; ideally it should raise `ConfigError` to fail fast.

**Fix:**
```python
except yaml.YAMLError as exc:
    raise ConfigError(f"Invalid YAML in user-data file: {exc}") from exc
```

---

### M-2 · `utils/audit.py` duplicates `get_cache_dir()` logic

**File:** `src/fcm/utils/audit.py:12–20`

`_get_audit_log_path()` manually reconstructs the environment variable name and cache directory fallback path:
```python
cache_dir_str = os.environ.get("FCM_CACHE_DIR", "")
...
path = Path.home() / ".cache" / "firecracker-manager"
```

This is exact duplication of the logic in `utils/fs.py:get_cache_dir()`, including the `FCM_CACHE_DIR` variable name and the `~/.cache/firecracker-manager` default. If the project name or env var prefix is ever changed in `constants.py`, `audit.py` will silently diverge.

**Fix:**
```python
from fcm.utils.fs import get_cache_dir

def _get_audit_log_path() -> Path:
    return get_cache_dir() / "audit.log"
```

---

### M-3 · Dead code: redundant double-None check after `_connect()`

**File:** `src/fcm/core/firecracker.py:84–87`

```python
if not self.conn:
    self._connect()
if self.conn is None:           # ← unreachable
    raise FirecrackerError(...)
```

`_connect()` either sets `self.conn` to a non-None value or raises `FirecrackerError`. The second `if self.conn is None` check can never be true. This dead code misleads readers into thinking `_connect()` might silently fail.

**Fix:** Remove the second guard, or collapse both into:
```python
if self.conn is None:
    self._connect()
```
(Since `_connect()` raises on failure, no second check is needed.)

---

### M-4 · Dead code: `except subprocess.CalledProcessError` catches the wrong exception type

**File:** `src/fcm/core/host.py:91`, `host.py:97`

```python
try:
    networks = list_networks()
except subprocess.CalledProcessError:   # list_networks() never raises this
    networks = []
```

`list_networks()` handles its own errors internally and returns `[]` on failure. It never propagates `subprocess.CalledProcessError`. Similarly at line 97, `remove_network()` raises `NetworkError`, not `subprocess.CalledProcessError`. Both `except` clauses are dead code. If `list_networks()` or `remove_network()` were ever refactored to let subprocess errors propagate, these catches would still silently swallow them as an empty list instead of an error.

**Fix:** Remove both dead `except subprocess.CalledProcessError` blocks. If defensive handling is wanted, catch `NetworkError`.

---

### M-5 · Scattered deferred imports — not justified by circular dependencies

**Files:** Multiple

Deferred (in-function) imports appear throughout the codebase in places where they do not prevent circular imports:

| Location | Import |
|----------|--------|
| `cli/key.py:38` | `from dataclasses import asdict` |
| `cli/configure.py:41` | `from fcm.api.network import ensure_default_network` |
| `cli/configure.py:52–54` | `import shutil, subprocess, sys` |
| `cli/configure.py:338` | `from fcm.utils.console import console` (inside a loop) |
| `main.py:64` | `from fcm.utils.console import console` (inside a conditional) |
| `api/assets.py:113–114` | `from fcm.utils.fs import get_cache_dir` |
| `api/assets.py:182` | `from concurrent.futures import ...` |

Deferred imports are appropriate only to break circular imports or avoid loading heavy optional dependencies. Standard-library imports (`shutil`, `subprocess`, `sys`, `dataclasses`) and unconditionally-used first-party imports should be at module level. The `configure.py:338` case is particularly wasteful — it runs inside a `for` loop on every iteration.

**Fix:** Move all non-circular deferred imports to module level.

---

### M-6 · `path.startswith()` string comparison for path safety is incorrect

**File:** `src/fcm/utils/fs.py:21`

```python
if not (str(resolved).startswith(str(home)) or str(resolved).startswith("/tmp")):
```

String prefix matching on paths is broken: if `home = /home/user`, then `/home/usermalicious/evil` passes the check. Python 3.9+ (and this project requires 3.13) has `Path.is_relative_to()` for this exact purpose.

**Fix:**
```python
if not (resolved.is_relative_to(home) or resolved.is_relative_to(Path("/tmp"))):
```

---

### M-7 · `_locked()` yields an unused file handle

**File:** `src/fcm/core/vm_manager.py:39–53`

```python
@contextmanager
def _locked(self) -> Generator[IO[str], None, None]:
    with open(self._lock_file, "w") as f:
        fcntl.flock(f, fcntl.LOCK_EX)
        yield f           # ← yielded but never used
        fcntl.flock(f, fcntl.LOCK_UN)
```

Every call site uses `with self._locked():` (no `as f`). The yielded file handle is never consumed. This creates a misleading API — callers look like they *should* use it, and future callers may wonder if they need to. The return type `Generator[IO[str], ...]` is also unnecessarily specific.

**Fix:** Change the yield to `yield None` and update the type annotation to `Generator[None, None, None]`.

---

### M-8 · `getattr()` on a statically-typed dataclass field

**File:** `src/fcm/core/config_gen.py:96`, `157`

```python
lsm_flags = getattr(self.vm_config, "lsm_flags", None)
```

`VMConfig` has `lsm_flags: str` as a proper typed field. Using `getattr(obj, "field", default)` implies the attribute might not exist, which mypy strict would normally catch — but it is suppressed here because `getattr` with a default bypasses attribute checking. This hides potential future regressions where the field is renamed or removed.

**Fix:**
```python
lsm_flags = self.vm_config.lsm_flags if self.vm_config.lsm_flags else None
```

---

### M-9 · `image_ls` checks filesystem before checking the config

**File:** `src/fcm/cli/asset.py:172–179`

`image_ls` iterates the `images.yaml` config and checks for `.ext4` / `.btrfs` files per image — but `image_fetch` (line 197) silently returns `None` on failure and the caller emits no error:

```python
result = fetch_image(spec, out, force)
if result:
    print_success(f"Image ready: {result}")
    raise typer.Exit(code=0)
else:
    raise typer.Exit(code=1)   # no error message printed before exit
```

When `fetch_image` fails the user sees the process exit with code 1 and no message explaining why.

**Fix:** Print an error message before the failing exit:
```python
else:
    print_error(f"Failed to download image '{id}'")
    raise typer.Exit(code=1)
```

---

### M-10 · `cli/config.py:dump_vm` reads file as string then parses — double work

**File:** `src/fcm/cli/config.py:79–82`

```python
with open(config_file, "r") as f:
    content = f.read()
    data = json.loads(content)
```

Reading the full file into a string and then parsing it is equivalent to `json.load(f)` but allocates an extra intermediate string. The `FileNotFoundError` catch at line 86 is also unreachable because `config_file.exists()` was already checked at line 74.

**Fix:**
```python
with open(config_file) as f:
    data = json.load(f)
```
Remove the unreachable `except FileNotFoundError` block.

---

### M-11 · `bare except Exception` in `list_assets()` swallows all errors silently

**File:** `src/fcm/api/assets.py:302`

```python
except Exception as e:
    logger.warning("Failed to parse images.yaml for list_assets: %s", e)
```

This catch is broader than the docstring implies. A `PermissionError`, `MemoryError`, or any unexpected exception from inside `load_images_config` is silently logged at WARNING and the images section of the asset list is silently omitted. The caller has no way to distinguish "no images configured" from "images config failed to load".

**Fix:** Catch only the specific expected exceptions (`ConfigError`, `yaml.YAMLError`, `OSError`).

---

### M-12 · Process-global config cache is never invalidated

**File:** `src/fcm/core/config.py:68`

```python
_config_cache: dict[Path, FCMConfig] = {}
```

The module-level `_config_cache` persists for the lifetime of the process. During tests that write config files to `tmp_path`, if two test cases happen to use the same `config_dir` path (unlikely with `tmp_path` but possible with parametrize), the second test gets the first test's cached config. More importantly, the cache makes it impossible for application code to reload a changed config without restarting the process.

**Fix:** Either add a `clear_config_cache()` function (used in test teardown) or make the TTL explicit by storing the file's `mtime` alongside the cached value.

---

### M-13 · `validate_entity_name` called twice for `create` and other VM commands

**Files:** `src/fcm/cli/vm.py:90`, `src/fcm/core/vm_lifecycle.py:136`; same pattern in `cli/key.py`

`validate_entity_name(name, "VM")` is called at the CLI layer (line 90 of `cli/vm.py`) and again inside `create_vm()` in `core/vm_lifecycle.py:136`. The duplication is harmless but establishes conflicting ownership: the CLI layer thinks it owns validation, but the core layer also validates. If the CLI check is ever removed assuming the core validates, or vice versa, the other half is silently lost.

**Fix:** Validate at the core layer only (the authoritative location). Remove the CLI-layer `validate_entity_name` call, as the core will raise `ValidationError` with a clear message.

---

## Low

### L-1 · `KeyboardInterrupt` caught and immediately re-raised — dead code

**File:** `src/fcm/core/logs.py:98–99`

```python
except KeyboardInterrupt:
    raise
```

Catching an exception only to immediately re-raise it is identical to not catching it. This is dead code.

**Fix:** Remove the `except KeyboardInterrupt: raise` block entirely.

---

### L-2 · Magic number `524288` repeated three times in `binary_manager.py`

**File:** `src/fcm/core/binary_manager.py:170`, `212` (and implicitly in download logic)

```python
for chunk in iter(lambda: f.read(524288), b""):
    sha256.update(chunk)
```

The 512 KiB chunk size `524288` appears as a magic number in multiple places. A future change to the chunk size requires hunting all occurrences.

**Fix:**
```python
_CHUNK_SIZE = 512 * 1024  # 512 KiB

for chunk in iter(lambda: f.read(_CHUNK_SIZE), b""):
```

---

### L-3 · `main.py` imports all CLI modules eagerly at startup

**File:** `src/fcm/main.py:9–17`

All seven CLI submodules are imported unconditionally at startup, even when the user only runs `fcm vm ls`. Each CLI module in turn imports from `core/` and `api/`. This pulls in PyYAML, Rich table objects, and all business logic even for a simple `--version` check. The codebase even acknowledges this with a `TODO` comment:

```python
from fcm.cli import (vm, config, asset, host, network, key, configure,)
# TODO: P-M8 — lazy-load CLI modules when startup time matters
```

**Fix:** Use Typer's lazy-loading pattern or `importlib` to defer each submodule until the matching subcommand is invoked.

---

### L-4 · `_step_summary` imports `console` inside a loop

**File:** `src/fcm/cli/configure.py:338`

```python
for label, ok in checks:
    status = "..."
    from fcm.utils.console import console   # ← inside loop body
    console.print(...)
```

`from fcm.utils.console import console` is executed on every loop iteration. Python caches module imports after the first call so this is not a correctness issue, but it is unnecessary noise and signals a copy-paste error.

**Fix:** Move the import to the top of `_step_summary()` (or the module).

---

### L-5 · `print_error` uses a Rich Panel — visually inconsistent with `print_warning`

**File:** `src/fcm/utils/console.py:21–23`

```python
def print_error(message: str) -> None:
    console.print(Panel(Text(message, style="red"), title="Error", border_style="red"))
```

`print_error` renders a full bordered panel. `print_warning` renders a plain inline string. In dense output (e.g., `fcm config validate` listing multiple errors), each error becomes a large bordered box, creating visually noisy output compared to the compact warning style.

**Fix:** Use a consistent inline prefix:
```python
def print_error(message: str) -> None:
    console.print(f"[red]✗ {message}[/red]")
```

---

### L-6 · `api/host.py:__all__` exports `default_cache_dir` but not `HostChange`/`HostState`

**File:** `src/fcm/api/host.py:23–37`

`default_cache_dir` is a thin wrapper that simply calls `get_cache_dir()`, yet it is in `__all__` and has a full docstring. Meanwhile `HostChange` and `HostState` — the types that consumers actually need when calling `init_host()` and inspecting results — are also in `__all__` but the `default_cache_dir` wrapper is redundant. Callers can (and should) get the cache dir via `utils/fs.get_cache_dir()` directly.

**Fix:** Remove `default_cache_dir` from `api/host.py` and its entry in `__all__`. Consumers should import `get_cache_dir` from `fcm.utils.fs` directly.

---

### L-7 · `configure.py:_step_image` has incorrect operator precedence in boolean condition

**File:** `src/fcm/cli/configure.py:205–210`

```python
if (
    images_dir.exists()
    and any(images_dir.glob("*.ext4"))
    or (images_dir.exists() and any(images_dir.glob("*.btrfs")))
):
```

Due to Python's operator precedence (`and` binds tighter than `or`), this parses as:
```
(images_dir.exists() and any(*.ext4)) or (images_dir.exists() and any(*.btrfs))
```

This happens to be logically equivalent to the intent, but the parenthesization is misleading — the outer parentheses suggest `images_dir.exists()` applies to both branches, which it does by coincidence. If the logic is ever touched (e.g., adding a third format), it is easy to introduce a bug.

**Fix:** Make the intent explicit:
```python
if images_dir.exists() and (
    any(images_dir.glob("*.ext4")) or any(images_dir.glob("*.btrfs"))
):
```

---

## Cross-Cutting Observations

### Deferred Imports Pattern

The codebase has a consistent pattern of deferring imports inside function bodies, appearing in at least 10 places. The most harmful instance is `configure.py:338` (inside a loop). The others are harmless but add noise. A project-wide sweep to hoist all non-circular imports to module level would eliminate this pattern.

### Validation Ownership

Name validation (`validate_entity_name`) is called at both the CLI and core layers in multiple command flows (`create vm`, `add key`, `remove key`, `inspect key`). This creates ambiguity about which layer owns validation. A clear policy — validate at the core layer, let the CLI pass through — would simplify both layers.

### Error Swallowing in API Layer

`api/assets.py:list_assets()` and `core/cloud_init.py` both silently swallow exceptions that the caller cannot distinguish from a legitimate empty result. This is the most common category of correctness issue in the codebase: silent degradation instead of explicit failure. Callers (including the wizard in `configure.py`) make decisions based on these results (e.g., "no image available") without knowing the information was suppressed.

---

## Appendix: File Coverage

| File | Lines | Findings |
|------|-------|----------|
| `api/assets.py` | 343 | M-11 |
| `api/host.py` | 50 | L-6 |
| `api/keys.py` | 23 | — |
| `api/network.py` | 31 | — |
| `api/vms.py` | 122 | C-1 |
| `cli/_helpers.py` | 14 | — |
| `cli/asset.py` | 359 | M-9 |
| `cli/config.py` | 88 | M-10 |
| `cli/configure.py` | 382 | L-4, L-7 |
| `cli/host.py` | 297 | — |
| `cli/key.py` | 187 | M-5 |
| `cli/network.py` | 202 | — |
| `cli/vm.py` | 374 | M-13 |
| `constants.py` | 84 | — |
| `core/binary_manager.py` | 257 | L-2 |
| `core/cloud_init.py` | 130 | M-1 |
| `core/config.py` | 188 | M-12 |
| `core/config_gen.py` | 203 | M-8 |
| `core/firecracker.py` | 308 | M-3 |
| `core/host.py` | 142 | M-4 |
| `core/host_privilege.py` | 247 | — |
| `core/host_setup.py` | 226 | — |
| `core/host_state.py` | 163 | — |
| `core/image.py` | 440 | H-5 |
| `core/kernel.py` | 381 | — |
| `core/key_manager.py` | 306 | — |
| `core/logs.py` | 150 | L-1 |
| `core/network.py` | 411 | H-3 |
| `core/network_manager.py` | 357 | H-1, H-2, H-4 |
| `core/ssh.py` | 211 | — |
| `core/vm_lifecycle.py` | 405 | C-2, M-13 |
| `core/vm_manager.py` | 173 | M-7 |
| `exceptions.py` | 65 | — |
| `main.py` | 126 | L-3 |
| `models/image.py` | 16 | — |
| `models/vm.py` | 89 | H-6 |
| `utils/audit.py` | 53 | M-2 |
| `utils/console.py` | 38 | L-5 |
| `utils/fs.py` | 77 | M-6 |
| `utils/http.py` | 87 | — |
| `utils/process.py` | 91 | — |
| `utils/validation.py` | 49 | — |
