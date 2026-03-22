# Phase 4 Architecture Design

## Overview

Phase 4 is a refinement and polish release. It does not introduce new subsystems; it
corrects behavioural ambiguities found through real usage, tightens the CLI contract,
adds distribution packaging, and creates the developer API reference. The changes span
all eight requirement sections and touch the CLI layer, two core modules, one model, the
build configuration, a new GitHub Actions workflow, and a new docs directory.

---

## 1. Existing State Assessment

### Already implemented and conformant

| Area | Status | Notes |
|------|--------|-------|
| `key ls` table (Name, Algorithm, Fingerprint, Comment, Added) | Done | Column order differs from spec — spec wants `Name, Fingerprint, Algorithm, Comment, Date Added` |
| `key add` basic flow | Done | Missing `--overwrite` flag |
| `key create` ED25519 via ssh-keygen, `--output`, `--comment`, `--overwrite` | Done | Behavioural gap: registry check fires before file existence when `--overwrite` is given |
| `key remove` / `key rm` | Done | Conforms |
| `--ssh-key` name-first, path fallback | Done | Missing clear error listing available names when resolution fails |
| `network ls` table | Done | Missing VM Count column; no "default" marker |
| `network create` with optional `--subnet` | Done | Phase 4 makes `--cidr` required (rename from `--subnet`), renames field `cidr` in serialised JSON, `--gateway` defaults to .1 already |
| CIDR overlap check | Done | Implemented in `_validate_subnet_no_overlap` |
| `network remove/rm` — fail if VMs attached | Done | Conforms |
| `network inspect` | Done | Missing iptables rule dump |
| Default network auto-creation | Done | `ensure_default_network()` exists, called during `vm create` |
| Lease table at `networks/<name>/leases.json` | Done | Conforms |
| VM `remove` — kill sequence | Partial | Current: SIGTERM → wait 5 s → SIGKILL. Missing: SendCtrlAltDel first, missing `ssh-keygen -R <ip>` at end |
| VM `pause` / `resume` as pass-through to Firecracker API | Done | Phase 4 requires these to print "not supported" instead of attempting the API call |
| `vm setup` subcommand | Present | Must be removed |
| Random locally-administered MAC | Partial | `_deterministic_mac()` uses SHA256 of name; Phase 4 requires random MAC (using `generate_mac()` which already exists in `core/network.py`) |
| `--user-data` validate exists/readable, warn on non-`#cloud-config` | Done | Conforms |
| `--user-data` merge SSH key in memory | Done | Conforms |
| `host prune` command | Missing | Does not exist |
| `host init` idempotency | Done | `init_host()` returns empty list if no changes needed; CLI already prints "nothing to do" in that case |
| Help consistency `cmd help` / `cmd -h` / `cmd --help` / `help cmd` | Partial | Typer default; needs audit |
| `docs/API.md` | Missing | Directory does not exist |
| `pyproject.toml` hatchling build backend | Done | Already using hatchling |
| Runtime deps pinned | Missing | Dependencies use `>=` lower bounds only |
| Dev extras with pyinstaller | Missing | `pyinstaller` not in dev extras |
| `release.yml` PyInstaller builds | Missing | No `.github/workflows/` directory |
| `README.md` Installation section | Unknown | Needs audit |
| `vm create` flag set completeness | Partial | Needs `--network` alias `--net`, `--disk-size`, possibly others per Section 8 |

### Known structural issues to address

- `vm.py` imports `BRIDGE_NAME` from `core/network.py` (old global-bridge approach) alongside the named-network system; the `setup` subcommand still references the old system. Removing `vm setup` untangles this.
- `_deterministic_mac()` in `vm.py` is a private function duplicating intent of `generate_mac()` in `core/network.py`. Phase 4 should switch to calling `generate_mac()`.
- `core/firecracker.py` calls `print_error` / `print_success` (Rich console helpers) directly — a layer violation. Phase 4 does not require fixing this globally but the `pause_vm` / `resume_vm` code paths will be bypassed, reducing exposure.

---

## 2. Component Changes Per Section

### Section 1 — `key` SSH Key Management

#### `src/fcm/cli/key.py`

**`ls` command**
- Reorder table columns to: Name, Fingerprint, Algorithm, Comment, Date Added.
- The existing `table.add_column` calls need reordering and the `add_row` call must match.

**`add` command**
- Add `overwrite: bool = typer.Option(False, "--overwrite", help="Overwrite existing key")` parameter.
- Pass `overwrite=overwrite` to `add_key()` (core change required, see below).

**`_resolve_ssh_key` (in `vm.py`)**
- When `ssh_key` is given but not found in cache or on filesystem, instead of printing a generic warning, enumerate `list_keys()` and include available key names in the error message.
- Raise `typer.Exit(code=1)` rather than silently returning `None`.

#### `src/fcm/core/key_manager.py`

**`add_key()`**
- Add `overwrite: bool = False` parameter.
- When `overwrite=True` and name already in registry: remove old `.pub` file and registry entry before re-adding (do not raise).

**`create_key()`**
- Current logic: checks registry for duplicate, then checks file. When `overwrite=True` the registry check raises unconditionally, which contradicts the `--overwrite` intent.
- Fix: when `overwrite=True`, silently remove the existing registry entry if present before proceeding.

---

### Section 2 — `network` Management

#### `src/fcm/cli/network.py`

**`ls` command**
- Add VM Count column: call `get_network_leases(n.name)` for each network and render `len(leases)`.
- Mark the default network: append `" (default)"` to the Name cell when `n.name == DEFAULT_NETWORK_NAME`, or use a Rich style marker.
- JSON output: add `"vm_count"` field.

**`create` command**
- Rename `--subnet` to `--cidr`; make it `required=True` (remove `None` default, add `...` as default to Typer option or use `typer.Option(...)`).
- Update help text accordingly.
- `--gateway` behaviour is already defaulting to `.1` of the subnet via `_gateway_for_subnet()` — no core change needed.
- `--no-nat` already exists — no change.

**`inspect` command**
- Add iptables rule dump: call a new helper `get_iptables_rules_for_bridge(bridge)` (new function in `core/network.py`) and render the output in the CLI.
- The attached VM list already comes from leases — no core change.

#### `src/fcm/core/network_manager.py`

- Rename `subnet` field on `NetworkConfig` to `cidr`. Update all internal references: `_load_config`, `_save_config`, `_gateway_for_subnet` call sites, `_validate_subnet_no_overlap`, `allocate_network_ip`, `list_networks` callers.
- Add a migration path: when loading a `config.json` that has a `"subnet"` key but no `"cidr"` key, remap it transparently (read `subnet`, write back as `cidr` on next save). This avoids breaking existing installations.
- `ensure_default_network()` — no change required; it already creates the default network if absent.

#### `src/fcm/core/network.py`

- Add `get_iptables_rules_for_bridge(bridge: str) -> list[str]`: runs `iptables -L FORWARD --line-numbers -n` and `iptables -t nat -L POSTROUTING --line-numbers -n`, filters lines referencing the bridge name, returns them as a list of strings.

#### `src/fcm/models/` — no direct model file for NetworkConfig

`NetworkConfig` is defined as a dataclass in `core/network_manager.py`, not in `models/`. The rename from `subnet` to `cidr` is entirely within `core/network_manager.py`.

#### `host init` / `configure`

- `ensure_default_network()` should be called from `core/host.py::init_host()` so that the default network is created at host-init time, not only lazily at `vm create` time. Add a call after the sysctl and module steps; wrap in `try/except NetworkError` and treat failure as a warning rather than aborting init (bridge might not yet be needed if firecracker binary is absent).

---

### Section 3 — `vm` Revised Behaviours

#### `src/fcm/cli/vm.py`

**Remove `setup` subcommand**
- Delete the `setup()` function and its `@app.command()` decorator entirely.
- Remove the now-unused import of `BRIDGE_NAME`, `setup_bridge`, `setup_nat` from `fcm.core.network` (keep `create_tap`, `delete_tap`, `add_iptables_forward_rules`, `remove_iptables_forward_rules`).

**`remove` command — graceful shutdown sequence**
Replace the current `os.kill(vm.pid, signal.SIGTERM)` block with a new `_graceful_shutdown(pid, socket_path, vm_ip)` helper:

```
1. If socket_path is not None and socket exists:
   a. client = FirecrackerClient(socket_path)
   b. client.send_ctrl_alt_del()            # best-effort, ignore return value
   c. client.close()
   d. Wait up to 5 seconds polling os.kill(pid, 0)
2. If process still alive: os.kill(pid, signal.SIGTERM), wait 1 second
3. If process still alive: os.kill(pid, signal.SIGKILL)
4. After process confirmed dead or not found:
   a. Proceed with TAP teardown and lease release (existing logic)
   b. If vm_ip is not None: subprocess.run(["ssh-keygen", "-R", vm_ip], ...)
      — capture stderr, ignore non-zero exit (key may not be in known_hosts)
```

The helper lives in `vm.py` as `_graceful_shutdown(pid: int, socket_path: Path | None, vm_ip: str | None) -> None`.

**`pause` and `resume` commands**
Replace the current implementation bodies with:
```python
print_info("VM pause/resume is not supported by this version of fcm.")
raise typer.Exit(code=0)
```
Keep the command declarations (`@app.command()`) so the commands are registered and `fcm vm pause --help` still works.

**MAC address — random instead of deterministic**
In the `create` command, replace:
```python
guest_mac = mac if mac else _deterministic_mac(name)
```
with:
```python
guest_mac = mac if mac else generate_mac()
```
`generate_mac()` already exists in `core/network.py` and is already imported in `vm.py`. Delete `_deterministic_mac()`.

**`--user-data` validation** — already conforms. No change.

**`--ssh-key` resolution error message**
In `_resolve_ssh_key()`:
- When a name was given but not found, call `list_keys()` and include their names in the error.
- Change the function to raise a new `FCMKeyError` (from `fcm.exceptions`) instead of returning `None` when the key is explicitly named but not found.
- The caller in `create` must catch this and exit with code 1.
- When `ssh_key is None`, preserve the current silent fallback to any key in cache.

---

### Section 4 — `host` Revised

#### `src/fcm/cli/host.py`

**Add `prune` command**
New subcommand `@app.command(name="prune")`:

```
1. Check that no VMs are running: VMManager().list_all() filtered to VMState.RUNNING.
   If any: print_error listing them, raise typer.Exit(code=1).
2. If not --force: print warning describing what will be torn down (all bridges, TAP devices,
   NAT rules, network state), typer.confirm(..., abort=True).
3. Call core/host.py::prune_host(cache_dir) (new function).
4. Print success.
```

Parameters: `force: bool = typer.Option(False, "--force", help="Skip confirmation")`.

**`init` command — idempotency output**
Already handled: `init_host()` returns `[]` when nothing needed, and the CLI already prints "Host already configured — nothing to do." and exits 0. No change needed.

#### `src/fcm/core/host.py`

**Add `prune_host(cache_dir: Path) -> list[str]` function**

Steps:
1. Call `list_networks()` from `core/network_manager`.
2. For each network: call `remove_network(name)` (which tears down bridge and NAT). Collect network names for the return value.
3. Call `restore_host(cache_dir)` to revert sysctl changes using the saved snapshot.
4. Update the host state snapshot file to reflect the pruned state (or delete it).

Returns a list of summary strings for the CLI to print.

This function must import from `network_manager` at call time (not at module top level) to avoid circular imports; `network_manager` already imports from `network` and `fs`, not from `host`.

---

### Section 5 — Help Consistency

Typer with `rich_markup_mode="rich"` already routes `-h`, `--help` to the same help output. The `cmd help` form (`fcm vm help`) is **not** natively supported by Typer. The `help cmd` form (`fcm help vm`) is also not standard.

**Implementation approach:**
- Add a `help` subcommand to each sub-Typer app (vm, network, key, host) that prints the app's own help text.
- In `main.py`, add a top-level `help` command that accepts an optional group name and re-invokes `app.info.help` or calls `typer.echo(ctx.get_help())`.

The cleanest mechanism is to add a `no_args_is_help=True` kwarg to each `typer.Typer()` constructor. This covers the zero-argument case. For `fcm vm help`, register a hidden command named `help` inside each sub-app that calls `ctx.info_name` and exits after printing.

The Typer `invoke_without_command=True` plus `no_args_is_help=True` combination on sub-typers is the correct low-code solution. Audit each `typer.Typer(...)` call and add both kwargs:

Files to update:
- `src/fcm/cli/vm.py`: `app = typer.Typer(help="...", no_args_is_help=True, invoke_without_command=True)`
- `src/fcm/cli/network.py`: same
- `src/fcm/cli/key.py`: same
- `src/fcm/cli/host.py`: same
- `src/fcm/cli/image.py`: same
- `src/fcm/cli/kernel.py`: same

For `fcm help vm` (positional topic on the root `help` command), add a root-level command:

```python
@app.command(name="help")
def help_cmd(topic: str = typer.Argument(None)) -> None:
    """Show help for a topic or command group."""
    ...
```

---

### Section 6 — `docs/API.md`

Create directory `firecracker-manager/docs/` and file `docs/API.md`.

The document must cover the public Python API surface of the `fcm.core.*` modules (callable without Typer), organised by module. Each entry requires: function signature with type annotations, description, parameters table, return value, and exceptions raised. The key modules to document are:

- `fcm.core.key_manager`: `list_keys`, `get_key`, `add_key`, `create_key`, `remove_key`, `inspect_key`
- `fcm.core.network_manager`: `list_networks`, `get_network`, `create_network`, `remove_network`, `inspect_network`, `allocate_network_ip`, `release_network_ip`, `ensure_default_network`, `get_network_leases`
- `fcm.core.network`: `setup_bridge`, `teardown_bridge`, `setup_nat`, `teardown_nat`, `create_tap`, `delete_tap`, `allocate_ip`, `generate_mac`, `bridge_exists`, `tap_exists`, `get_tap_devices`, `get_default_interface`
- `fcm.core.vm_manager`: public methods of `VMManager`
- `fcm.core.firecracker`: `FirecrackerClient` methods
- `fcm.core.host`: `init_host`, `restore_host`, `prune_host`, `get_host_state`, `check_kvm_access`, `check_required_binaries`
- `fcm.models.vm`: `VMConfig`, `VMInstance`, `VMState`
- `fcm.models.image`: `ImageSpec`
- `fcm.exceptions`: full hierarchy

This is a pure documentation deliverable — no source code changes.

---

### Section 7 — Distribution

#### `pyproject.toml`

**Pin runtime dependencies** — change from lower-bound-only to pinned ranges:

```toml
dependencies = [
    "typer>=0.12,<0.13",
    "rich>=13,<14",
    "pyyaml>=6,<7",
]
```

Exact upper bounds should be validated against the current `uv.lock` resolution before committing.

**Add `pyinstaller` to dev extras**

The project currently has two `[dependency-groups]` dev and `[project.optional-dependencies]` dev entries — a duplication that should be resolved. The correct approach for `uv` projects is to use `[dependency-groups]` only:

```toml
[dependency-groups]
dev = [
    "mypy>=1.19",
    "pytest>=9",
    "pytest-cov>=7",
    "ruff>=0.15",
    "types-PyYAML>=6.0",
    "pyinstaller>=6,<7",
]
```

Remove `[project.optional-dependencies]` dev section to eliminate the conflict.

#### `.github/workflows/release.yml`

Create `.github/workflows/release.yml`. Triggered on `push` to tags matching `v*.*.*`.

Build matrix: `ubuntu-22.04` and `ubuntu-24.04`.

```yaml
name: release
on:
  push:
    tags: ['v*.*.*']

jobs:
  build:
    runs-on: ${{ matrix.os }}
    strategy:
      matrix:
        os: [ubuntu-22.04, ubuntu-24.04]
    steps:
      - uses: actions/checkout@v4
      - uses: astral-sh/setup-uv@v3
        with:
          python-version: "3.13"
      - name: Install dependencies
        working-directory: firecracker-manager
        run: uv sync --group dev
      - name: Build binary
        working-directory: firecracker-manager
        run: |
          uv run pyinstaller \
            --onefile \
            --name fcm-${{ matrix.os }} \
            src/fcm/main.py
      - name: Upload artifact
        uses: actions/upload-artifact@v4
        with:
          name: fcm-${{ matrix.os }}
          path: firecracker-manager/dist/fcm-${{ matrix.os }}
      - name: Attach to release
        uses: softprops/action-gh-release@v2
        with:
          files: firecracker-manager/dist/fcm-${{ matrix.os }}
```

Note: `actions/upload-artifact` and `softprops/action-gh-release` pin to major versions; pin to exact SHAs for supply-chain security in a production context.

#### `README.md`

Add an `## Installation` section covering three methods:
1. Pre-built binary (download from GitHub Releases, `chmod +x`, move to `$PATH`).
2. From PyPI via `pip install firecracker-manager` (once published).
3. From source via `uv sync && uv run fcm`.

---

### Section 8 — `vm create` Final Flag Set

The current flag set in `cli/vm.py::create()` is:
`--name`, `--image`, `--kernel`, `--vcpus`, `--mem`, `--ip`, `--network`, `--mac`, `--ssh-key`, `--user-data`, `--user`, `--enable-api-socket`, `--enable-pci`, `--firecracker-bin`.

Phase 4 requirements (Section 8) confirm this as the complete set with the following adjustments:

| Flag | Change |
|------|--------|
| `--network` | Add short alias `--net` |
| `--vcpus` | Add alias `--cpus` |
| `--firecracker-bin` | No change |
| `--mac` | Validation: must match regex `^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$` if provided |

No new flags are added. No existing flags are removed. The `--disk-size` flag is **not** in the Section 8 spec; do not add it.

The `--network` alias addition requires adding a Typer `Option` with multiple names: `typer.Option(DEFAULT_NETWORK_NAME, "--network", "--net", help="...")`.

---

## 3. Data Model Changes

### `NetworkConfig` in `core/network_manager.py`

| Field | Before | After |
|-------|--------|-------|
| `subnet: str` | Used as the CIDR notation string | Renamed to `cidr: str` |

All callers within `network_manager.py`, `cli/network.py`, and `cli/vm.py` must use `cidr` after the rename. The `_write_cloud_init` function in `vm.py` hardcodes `"10.20.0.1"` as the gateway in network-config — this should reference `net_config.gateway` instead (separate correctness fix).

**Backward-compatibility migration strategy for `config.json` on disk:**

In `_load_config()`:

```python
def _load_config(network_dir: Path) -> NetworkConfig | None:
    config_file = network_dir / "config.json"
    if not config_file.exists():
        return None
    data = json.loads(config_file.read_text())
    # Migrate legacy 'subnet' key to 'cidr'
    if "subnet" in data and "cidr" not in data:
        data["cidr"] = data.pop("subnet")
        # Write migrated config back
        config_file.write_text(json.dumps(data, indent=2))
    return NetworkConfig(**data)
```

This is a one-time in-place migration and requires no offline migration tooling.

### `VMInstance` in `models/vm.py`

No changes required. The model already stores `mac`, `ip`, `socket_path`, and `status`.

### `VMConfig` in `models/vm.py`

No changes required. All Phase 4 `vm create` flag changes are handled at the CLI layer; the core model is unchanged.

### `HostState` / `HostChange` in `core/host.py`

No structural changes. The `prune_host` function operates on these types but does not modify their definition.

---

## 4. Security Considerations

### MAC Address Validation (`--mac` flag)

When a user provides `--mac`, validate before use:

```python
import re
MAC_RE = re.compile(r'^[0-9a-fA-F]{2}(:[0-9a-fA-F]{2}){5}$')

def _validate_mac(mac: str) -> str:
    if not MAC_RE.match(mac):
        raise ValueError(f"Invalid MAC address format: {mac!r}")
    return mac.lower()
```

Place this in `core/network.py` as a module-level function. Call it from `vm.py::create()` immediately after the `mac` option is received.

Locally administered MACs have the second-least-significant bit of the first octet set. When generating random MACs via `generate_mac()`, this is already enforced via the `02:FC:` prefix. When accepting user-supplied MACs, do not enforce the locally-administered bit — document in help text that the `02:` prefix is recommended for locally administered addresses.

### CIDR Validation

`ipaddress.IPv4Network(subnet, strict=False)` is already used throughout and will raise `ValueError` on malformed input. The CLI `create` command should catch `ValueError` from ipaddress and surface a friendly error before calling the core function:

```python
try:
    ipaddress.IPv4Network(cidr, strict=False)
except ValueError:
    print_error(f"Invalid CIDR notation: {cidr!r}")
    raise typer.Exit(code=1)
```

### IP Address Validation

`vm create --ip` already validates that the IP is within the network subnet. Wrap `ipaddress.IPv4Address(ip)` parsing in a `ValueError` handler with a friendly message.

### File Path Validation (`--user-data`, `--output`)

`--user-data` already validates `user_data.exists()`. Add an explicit `os.access(user_data, os.R_OK)` check to catch permission errors separately from missing files. Provide distinct error messages:

- "user-data file not found: ..." (path does not exist)
- "user-data file is not readable: ..." (exists but no read permission)

Reject paths where the resolved absolute path escapes the filesystem in ways that `open()` would follow symlinks to privileged files. In practice, since this is a locally-administered CLI tool, symlink traversal is acceptable — document but do not add TOCTOU-proof checks.

`--output` (key create) resolves to `Path(output_dir)`. Validate `output_dir` is a directory (not a file) after `mkdir` returns, using `output_dir.is_dir()`.

### SSH Key Content Validation

`add_key()` currently calls `_compute_fingerprint()` which will raise `ValueError` / `binascii.Error` on invalid base64. Wrap this in `FCMKeyError` with a message like "File does not appear to be a valid SSH public key".

### Cloud-Init User-Data Injection

The `_write_cloud_init` function appends `ssh_authorized_keys` content from cache. The key content is read from a file the user previously imported, so it was already validated at import time. No additional escaping is needed since YAML does not require escaping for the single-line key blob.

### `prune_host` Confirmation

The `host prune` command has potentially irreversible effects (tears down all networking). The confirmation prompt must clearly state what will be destroyed. The `--force` flag bypasses only the interactive confirmation, not the running-VM check — running VMs must always block pruning.

### Command Injection via VM Name

The VM name is used to construct TAP device names (`fc-{name}-0`) and filesystem paths. The TAP name is passed directly to `ip` subprocess calls. Linux interface names are limited to 15 characters and may not contain `/` or null bytes; `ip` will reject invalid names. Validate VM names at the `create` command entry point:

```python
VM_NAME_RE = re.compile(r'^[a-zA-Z0-9][a-zA-Z0-9_-]{0,28}$')
```

This limits names to 30 characters, alphanumeric plus hyphen/underscore, starting with alphanumeric. The resulting TAP name `fc-{name}-0` would be at most 34 characters, which exceeds the 15-char Linux limit. Adjust: limit VM names to 8 characters for the TAP device name to fit: `fc-{name[:8]}-0` is 13 chars. **Or** use a different TAP naming scheme such as a fixed-length hash suffix. This is an existing architectural issue that Phase 4 should document in a comment if not immediately fixed.

---

## 5. Implementation Order

The sections have the following dependency graph:

```
Section 7 (pyproject.toml)    — independent, do first
Section 7 (release.yml)        — independent, do first
Section 3 (remove vm setup)    — independent, no downstream deps
Section 1 (key add --overwrite) — core change, independent
Section 1 (key ls column order) — cosmetic, independent
Section 1 (ssh-key error msg)  — depends on list_keys(), already available
Section 2 (network create --cidr required) — rename in NetworkConfig
Section 2 (NetworkConfig subnet->cidr rename + migration) — must precede network ls and inspect changes
Section 2 (network ls VM count + default marker) — depends on rename being done
Section 2 (network inspect iptables rules) — depends on new helper in network.py
Section 3 (graceful shutdown) — depends on FirecrackerClient.send_ctrl_alt_del() (already exists)
Section 3 (pause/resume "not supported") — independent
Section 3 (random MAC) — independent (generate_mac() already exists)
Section 4 (host prune) — depends on network_manager.list_networks(), remove_network(); those must be stable first
Section 4 (host init default network) — depends on ensure_default_network() being correct
Section 5 (help consistency) — independent, cosmetic
Section 6 (docs/API.md) — depends on all code changes being finalised
Section 8 (vm create flag set) — independent additions
```

**Recommended sequence:**

1. `pyproject.toml` — pin deps, add pyinstaller, resolve duplicate dev groups.
2. `.github/workflows/release.yml` — new file, no code deps.
3. Remove `vm setup` subcommand and clean up its imports in `cli/vm.py`.
4. `NetworkConfig.subnet` → `cidr` rename with backward-compat migration in `_load_config`.
5. `network create --cidr` (required), update CLI, update all call sites.
6. `network ls` — add VM count column, add default marker.
7. `get_iptables_rules_for_bridge()` in `core/network.py`, then `network inspect` update.
8. `key add --overwrite` — core + CLI.
9. `key ls` column reorder.
10. `_resolve_ssh_key` error message improvement.
11. `generate_mac()` substitution for `_deterministic_mac()`, add MAC validation helper.
12. Graceful shutdown sequence in `vm remove`.
13. `pause` / `resume` "not supported" change.
14. `host prune` — core `prune_host()`, then CLI command.
15. `host init` — call `ensure_default_network()`.
16. Help consistency audit — add `no_args_is_help=True` / `invoke_without_command=True`.
17. `vm create` flag aliases (`--net`, `--cpus`).
18. `README.md` Installation section.
19. `docs/API.md` — written last, after all code changes are stable.

---

## 6. Risk Assessment

### Risk 1: `NetworkConfig.subnet` → `cidr` rename breaks existing installations

**Likelihood:** High — any user with existing named networks has `config.json` files using `"subnet"`.

**Impact:** Medium — `NetworkConfig(**data)` will raise `TypeError` (unexpected keyword argument) causing all network commands to fail.

**Mitigation:** The in-place migration in `_load_config()` described in Section 3 handles this transparently. Write a unit test that loads a legacy `config.json` with `"subnet"` key and asserts the returned `NetworkConfig.cidr` is correct and the file is updated.

### Risk 2: Removing `vm setup` breaks user scripts

**Likelihood:** Low — `vm setup` was already superseded by the named-network system. Any automation using it would break.

**Impact:** Low — the subcommand is not mentioned in current docs as the primary workflow.

**Mitigation:** In the commit message and release notes, explicitly list `vm setup` as removed. Consider printing a deprecation error for one release before removal if backwards compatibility is a concern; Phase 4 spec says "no vm setup subcommand" so removal is correct.

### Risk 3: Switching from deterministic to random MAC breaks cloud-init network configs

**Likelihood:** Low — cloud-init uses the `network-config` file's IP address, not the MAC, for static assignment. Firecracker VirtIO devices don't persist their MAC across VM recreations anyway.

**Impact:** Low — DHCP-based setups might have issues if MAC was used for DHCP reservations, but the tool uses static IP assignment via cloud-init.

**Mitigation:** Document the change. Since the tool creates a new rootfs for each VM, MAC persistence between creations is not meaningful.

### Risk 4: `host prune` accidentally called while VMs are running

**Likelihood:** Low — the command checks `VMManager().list_all()` for running VMs first.

**Impact:** High — would tear down bridge devices that VMs depend on, causing loss of network connectivity without killing the VMs.

**Mitigation:** The running-VM check must happen before the confirmation prompt, not after. The check must not be bypassable by `--force`. Test this path explicitly in unit tests by mocking `VMManager.list_all()` to return running VMs and asserting `Exit(code=1)`.

### Risk 5: `ssh-keygen -R <ip>` failure corrupts shutdown

**Likelihood:** Low — `ssh-keygen -R` failing (e.g., `~/.ssh/known_hosts` doesn't exist) should not block VM cleanup.

**Impact:** Medium — if the cleanup function raises due to subprocess error, the VM state and TAP device cleanup may be skipped.

**Mitigation:** Always call `ssh-keygen -R` with `check=False`. Log `stderr` at DEBUG level. Never raise on failure.

### Risk 6: PyInstaller binary includes secrets or credentials from build environment

**Likelihood:** Low — the tool has no embedded credentials. PyInstaller bundles the Python stdlib and installed packages.

**Impact:** Medium — if a dev dependency (e.g., a config file) is accidentally bundled.

**Mitigation:** The `release.yml` workflow runs on a fresh GitHub Actions runner with no ambient credentials. Add a `--exclude-module` list to the PyInstaller invocation to exclude test frameworks (`pytest`, `_pytest`) and type stubs (`mypy`). Verify the bundle size is reasonable (should be under 30 MB).

### Risk 7: `core/host.py` calling `network_manager.remove_network()` in `prune_host`

**Likelihood:** Medium — `core/network_manager.py` imports from `core/network.py` and `utils/fs.py` but not from `core/host.py`. Adding the reverse import (host → network_manager) creates no circular import.

**Impact:** Low if import is done locally within the function body, which is already the pattern used elsewhere (`from fcm.core.network_manager import ...` inside the function).

**Mitigation:** Use a function-scoped import for `network_manager` in `prune_host` to make the dependency explicit and safe.

### Risk 8: Typer help consistency regression

**Likelihood:** Medium — adding `no_args_is_help=True` to sub-typers changes behaviour when a sub-group is invoked without arguments (previously might have been a no-op or shown a different message).

**Impact:** Low — this is a UX improvement, not a functional regression.

**Mitigation:** Manually test all top-level command groups after the change. Automated test: call each sub-app Typer runner with no arguments and assert the exit code is 0 with help content in stdout.

### Risk 9: `--cidr` as required option breaks backward compatibility in scripts

**Likelihood:** Medium — any user script calling `fcm network create mynet` without `--cidr` previously relied on auto-allocation. After the change, this will fail with a missing-required-option error.

**Impact:** Medium — existing automation silently gets a different subnet each time (unpredictable), so requiring explicit `--cidr` is actually a correctness improvement.

**Mitigation:** The auto-allocation code (`_auto_allocate_subnet()`) can remain in the codebase as a utility. If the spec requires strict removal of auto-allocation, document it in release notes. Otherwise, consider keeping auto-allocation but printing a deprecation warning when `--cidr` is not provided.

---

## Appendix A: Files Changed Summary

| File | Change Type | Section(s) |
|------|-------------|-----------|
| `src/fcm/cli/vm.py` | Modify | 3, 8 |
| `src/fcm/cli/key.py` | Modify | 1, 5 |
| `src/fcm/cli/network.py` | Modify | 2, 5 |
| `src/fcm/cli/host.py` | Modify | 4, 5 |
| `src/fcm/cli/image.py` | Modify | 5 |
| `src/fcm/cli/kernel.py` | Modify | 5 |
| `src/fcm/core/key_manager.py` | Modify | 1 |
| `src/fcm/core/network_manager.py` | Modify | 2 |
| `src/fcm/core/network.py` | Modify | 2, 3 |
| `src/fcm/core/host.py` | Modify | 4 |
| `src/fcm/main.py` | Modify | 5 |
| `pyproject.toml` | Modify | 7 |
| `README.md` | Modify | 7 |
| `.github/workflows/release.yml` | Create | 7 |
| `docs/API.md` | Create | 6 |

No changes to `src/fcm/models/vm.py`, `src/fcm/models/image.py`, `src/fcm/constants.py`, `src/fcm/exceptions.py`, or any test files (test updates are implied but out of scope for this architecture document).

---

## Appendix B: Test Coverage Targets

Each behaviour change requires a corresponding unit test. The following test cases are the minimum required additions:

| Test | File | Covers |
|------|------|--------|
| `test_add_key_overwrite_replaces_existing` | `tests/unit/test_key_manager.py` | Section 1 |
| `test_add_key_overwrite_false_raises_on_duplicate` | `tests/unit/test_key_manager.py` | Section 1 |
| `test_list_keys_column_order` | `tests/unit/test_key_cli.py` | Section 1 |
| `test_resolve_ssh_key_lists_available_on_miss` | `tests/unit/test_vm_cli.py` | Section 1 |
| `test_network_config_legacy_subnet_migration` | `tests/unit/test_network_manager.py` | Section 2 |
| `test_network_create_cidr_required` | `tests/unit/test_network_cli.py` | Section 2 |
| `test_network_ls_shows_vm_count` | `tests/unit/test_network_cli.py` | Section 2 |
| `test_vm_remove_calls_send_ctrl_alt_del` | `tests/unit/test_vm_cli.py` | Section 3 |
| `test_vm_remove_ssh_keygen_R_called` | `tests/unit/test_vm_cli.py` | Section 3 |
| `test_vm_pause_prints_not_supported` | `tests/unit/test_vm_cli.py` | Section 3 |
| `test_vm_resume_prints_not_supported` | `tests/unit/test_vm_cli.py` | Section 3 |
| `test_vm_create_uses_random_mac` | `tests/unit/test_vm_cli.py` | Section 3 |
| `test_vm_create_validates_mac_format` | `tests/unit/test_vm_cli.py` | Section 3 |
| `test_host_prune_refuses_with_running_vms` | `tests/unit/test_host_cli.py` | Section 4 |
| `test_host_prune_tears_down_networks` | `tests/unit/test_host_core.py` | Section 4 |
| `test_host_init_idempotent_exits_clean` | `tests/unit/test_host_cli.py` | Section 4 |
