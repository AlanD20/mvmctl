# CLI Consistency Refactor — Layer Compliance & Code Clarity

**Date:** 2026-04-08
**Status:** In progress
**Goal:** Enforce simple, consistent, readable patterns across all `cli/` files — top-level imports, model types, linear flow, no nested closures, no business logic.

---

## Background: What Is Wrong

### Finding 1 — Inline imports scattered inside function bodies

Every `cli/` file has imports inside function bodies that should be at module top-level. This makes it impossible to reason about module dependencies at a glance.

| File | Function | Inline import |
|------|----------|---------------|
| `cli/vm.py` | `_get_vm_defaults()` | `from mvmctl.api.config import load_config`, `from mvmctl.utils.fs import get_assets_dir` |
| `cli/vm.py` | `_resolve_active_firecracker_bin()` | `from mvmctl.api.assets import get_binary_path`, `from mvmctl.exceptions import AssetNotFoundError` |
| `cli/vm.py` | `rm()` | `from mvmctl.api.vms import get_vm_manager as _get_vm_manager` |
| `cli/vm.py` | `snapshot()` | `from mvmctl.utils.validation import validate_entity_name` |
| `cli/vm.py` | `load()` | `from mvmctl.utils.validation import validate_entity_name` |
| `cli/vm.py` | `inspect()` | `from mvmctl.api.vms import inspect_vm` |
| `cli/vm.py` | `_print_vm_details()` | `from mvmctl.utils.console import (format_timestamp, print_inspect_header, print_key_value, print_section_header)` |
| `cli/vm.py` | `_print_vm_details_tree()` | `from datetime import datetime` |
| `cli/vm.py` | `export_vm()` | `from mvmctl.exceptions import MVMError` |
| `cli/bin.py` | `bin_ls()` | `import json` |
| `cli/key.py` | `ls()` | `from dataclasses import asdict` |
| `cli/key.py` | `add()` | `import os` |
| `cli/key.py` | `inspect()` | `from datetime import datetime` |
| `cli/network.py` | `ls()` | `from dataclasses import asdict` |
| `cli/config.py` | `dump_vm()` | `from mvmctl.api.vms import get_vm_manager`, `from mvmctl.utils.fs import get_vm_dir_by_hash` |
| `cli/host.py` | `_abort_if_vms_running()` | `from mvmctl.models.vm import VMStatus` |
| `cli/host.py` | `init_cmd()` | `import subprocess` |
| `cli/host.py` | `ls_cmd()` | `import json` |
| `cli/ssh.py` | `_resolve_ssh_key_for_vm()` | `from mvmctl.utils.fs import get_keys_dir` |
| `cli/ssh.py` | `_get_vm_defaults()` | `from mvmctl.api.config import load_config`, `from mvmctl.utils.fs import get_assets_dir` |
| `cli/image.py` | `image_inspect()` | `from datetime import datetime` |

### Finding 2 — Business logic in CLI instead of API or core

| File | Function | Problem | Belongs in |
|------|----------|---------|-----------|
| `cli/vm.py` | `create()` (386 lines, 27 params) | All config resolution, validation, cloud-init mode logic | Split: API + `core/` |
| `cli/vm.py` | `rm()` | Direct `manager.find_by_id_prefix()` call, deduplication logic | `api/vms.py` |
| `cli/vm.py` | `ls_vms()` | VM status checking (dir + process state) | `api/vms.py` |
| `cli/vm.py` | `_print_vm_details()` | Disk size calculation from `Path.stat()` | `utils/` or pre-compute in API |
| `cli/vm.py` | `_print_vm_details_tree()` | Date parsing, disk size calc | `utils/` or pre-compute in API |
| `cli/console.py` | `_do_attach()` (86 lines) | Terminal raw mode, socket I/O, escape sequence processing | `core/console.py` |
| `cli/cache.py` | `cache_prune()` (116 lines, 6 params) | Resource-type branching with per-type deletion logic | `api/cache.py` |
| `cli/key.py` | `add()` (78 lines) | File existence/readability validation, `.pub` extension check | `api/keys.py` |
| `cli/ssh.py` | `_find_ssh_key_from_path()` | Filesystem traversal for key files | `core/ssh.py` or `api/keys.py` |
| `cli/ssh.py` | `_resolve_ssh_key_for_vm()` | Key resolution with multiple fallback paths | `api/keys.py` |
| `cli/host.py` | `_abort_if_vms_running()` | VM status checking | `api/vms.py` |
| `cli/host.py` | `init_cmd()` | Sudo restart subprocess orchestration | `api/host.py` |
| `cli/config.py` | `dump_vm()` | File reading and JSON parsing | `api/vm_config.py` |

### Finding 3 — Raw dicts instead of model types

| File | Function | Current | Should be |
|------|----------|---------|-----------|
| `cli/image.py` | `_output_local_images()` | `dict[str, dict[str, Any]]` from `list_images_metadata()` | `dict[str, ImageRecord]` |
| `cli/image.py` | `_output_remote_images()` | Raw dicts built inline | `list[RemoteImage]` (already typed from YAML) |
| `cli/image.py` | `image_inspect()` | Inline dict construction (`info = {...}`) | Return `ImageRecord` from API |
| `cli/image.py` | `_print_image_details()` | Raw dict `info: dict[str, Any]` | `ImageRecord` |
| `cli/image.py` | `_print_image_details_tree()` | Raw dict | `ImageRecord` |
| `cli/vm.py` | `ls_vms()` | Inline dict building per VM | `VMInstance` (already imported) |
| `cli/vm.py` | `_print_vm_details()` | Raw `info: dict[str, Any]` | `VMInstance` |
| `cli/vm.py` | `_print_vm_details_tree()` | Raw dict | `VMInstance` |
| `cli/kernel.py` | `_print_kernel_details()` | Raw dict | `KernelRecord` |
| `cli/network.py` | `ls()` | Inline dict from `list_networks()` | `NetworkConfig` (already in `models/network.py`) |

### Finding 4 — Functions doing too much

| File | Function | Lines | Problem |
|------|----------|-------|---------|
| `cli/vm.py` | `create()` | 386 | Config import, merge, validation, resolution, cloud-init logic, API calls — all in one |
| `cli/console.py` | `_do_attach()` | 86 | Terminal setup, socket connect, read loop, escape handling |
| `cli/cache.py` | `cache_prune()` | 116 | One massive switch on resource type with per-case logic |
| `cli/bin.py` | `bin_ls()` | 76 | Row categorization and formatting mixed together |
| `cli/key.py` | `add()` | 78 | File validation + key add orchestration |

### Finding 5 — Duplicate helper logic across files

| Pattern | Files that have it | Should be |
|---------|-------------------|-----------|
| `_get_vm_defaults()` | `cli/vm.py`, `cli/ssh.py` | `cli/_helpers.py` — shared |
| `_resolve_active_firecracker_bin()` | `cli/vm.py` | Already in `api/assets.py` (`get_binary_path`) — just use it |
| Disk size calculation (`Path.stat()`) | `cli/vm.py`, `cli/image.py` | `utils/disk_size.py` already has `format_bytes_human_readable` — needs `get_file_size_from_path()` |
| Date parsing for `pulled_at` | `cli/image.py` | `utils/time.py` already has `format_timestamp` — use consistently |

---

## Design Principles Going Forward

### Principle 1 — Top-level imports only

```python
# CORRECT
import json
from pathlib import Path
from datetime import datetime

from mvmctl.api.vms import get_vm_manager
from mvmctl.utils.console import print_success

# INCORRECT
def some_command():
    import json  # Never inline stdlib or mvmctl imports
```

### Principle 2 — CLI is a thin shell

```
CLI: parse → call ONE api function → format output
API: orchestrate → call core → return data
Core: execute → return data
```

CLI functions should never contain:
- Validation logic (except user-input shape checks)
- Business logic (status checking, path resolution, config merging)
- Data transformation (dict building from DB records)

### Principle 3 — Use model types, not dicts

```python
# CORRECT
info: VMInstance = get_vm(name)
print_key_value("Name", info.name)

# INCORRECT
info: dict[str, Any] = get_vm(name)  # returns VMInstance!
print_key_value("Name", info.get("name"))
```

### Principle 4 — One function, one job

```python
# CORRECT
def _resolve_image_path(image: str | None) -> Path:
    ...

def _resolve_kernel_path(kernel: str | None) -> Path:
    ...

def _build_vm_create_params(...) -> CreateParams:
    ...

# INCORRECT — one function doing resolution + validation + defaults
def create(...):
    # 386 lines of everything
```

### Principle 5 — No closures passed as arguments

```python
# CORRECT — pure function with explicit parameters
def _prompt_partition_selection(partitions: list[PartitionInfo]) -> int:
    ...

# ACCEPTABLE — typer callback with explicit params
def _cloud_init_iso_callback(ctx: typer.Context, param: typer.Parameter, value: Path | None) -> Path | None:
    ...

# INCORRECT — closure over external scope
def create(...):
    def nested_callback(...):  # No!
        use_external_var
```

---

## Execution Plan

### Pass 1 — Inline imports → top-level (Zero risk)

Move all inline imports to module top-level. No logic changes. This is the baseline that makes all subsequent passes readable.

**Files affected:**
- `cli/vm.py` — 9 inline import sites
- `cli/bin.py` — 1 inline `import json`
- `cli/key.py` — 3 inline imports
- `cli/network.py` — 1 inline import
- `cli/config.py` — 2 inline imports
- `cli/host.py` — 3 inline imports
- `cli/ssh.py` — 3 inline imports
- `cli/image.py` — 1 inline `from datetime import datetime`

**Commit:** "refactor(cli): move all inline imports to module top-level"

---

### Pass 2 — Consolidate duplicate helpers into `cli/_helpers.py`

Create a single shared helper module for cross-cutting CLI utilities.

**New in `cli/_helpers.py`:**
```python
def get_vm_defaults() -> VMDefaultsConfig:
    """Shared VM defaults loader — used by vm.py and ssh.py."""
    from mvmctl.api.config import load_config
    from mvmctl.utils.fs import get_assets_dir
    return load_config(get_assets_dir(), build_mvm_defaults())
```

**Changes:**
- `cli/vm.py`: replace `_get_vm_defaults()` with `from cli._helpers import get_vm_defaults`
- `cli/ssh.py`: replace `_get_vm_defaults()` with `from cli._helpers import get_vm_defaults`

**Commit:** "refactor(cli): consolidate duplicate _get_vm_defaults into _helpers"

---

### Pass 3 — Model adoption for display helpers

Replace `dict[str, Any]` parameters with actual model types where models already exist.

**`cli/image.py`:**
- `image_inspect()`: build `ImageRecord` from API result, pass to `_print_image_details` / `_print_image_details_tree` instead of raw dict
- `_output_local_images()`: use `ImageRecord` from `list_images_metadata()` — already returns the right shape
- `_print_image_details()`: parameter type `ImageRecord` instead of `dict[str, Any]`
- `_print_image_details_tree()`: parameter type `ImageRecord`

**`cli/vm.py`:**
- `ls_vms()`: use `VMInstance` attributes directly instead of building inline dicts
- `_print_vm_details()`: parameter type `VMInstance` instead of `dict[str, Any]`
- `_print_vm_details_tree()`: parameter type `VMInstance`

**`cli/kernel.py`:**
- Already clean from previous session's work

**Commit:** "refactor(cli): use model types in display helpers instead of raw dicts"

---

### Pass 4 — Move business logic out of CLI into API

**4a — `cli/vm.py` `rm()`: API wrap VM resolution**
- Currently: `manager.find_by_id_prefix()` called directly in CLI
- Fix: Add `resolve_vm_by_prefix(prefix)` to `api/vms.py` that returns `VMInstance`
- CLI: just call `resolve_vm_by_prefix()` and `remove_vm()` — no manager instantiation

**4b — `cli/vm.py` `ls_vms()`: Move status checking to API**
- Currently: CLI checks `is_file_missing(vm_dir)` and `is_vm_process_running(pid)` per VM
- Fix: `list_vms()` in API already computes status — enrich `VMInstance` with `is_missing` field
- CLI: just iterate `VMInstance` objects and print

**4c — `cli/vm.py `create()`: Decompose into phases**
Phase 1 (new API): `resolve_vm_config()` — takes all CLI args, returns resolved `VMConfig`
Phase 2 (existing): `create_vm()` — takes resolved `VMConfig`, creates VM
Phase 3 (CLI): `create()` command — orchestrates the two API calls, handles output

**4d — `cli/console.py` `_do_attach()`: Move terminal logic to `core/console.py`**
- Currently: 86 lines of raw PTY/socket handling in CLI
- Fix: `core/console.py` gets `attach_console(name: str)` and `detach_console(name: str)`
- CLI: just calls `attach_console()` — no socket/PTY knowledge

**4e — `cli/cache.py` `cache_prune()`: API handles branching**
- Currently: 116-line function with switch on resource type
- Fix: `api/cache.py` gets `prune_cache(resource, include_stopped, include_running, dry_run)` returning structured results
- CLI: just calls API and formats output

**4f — `cli/key.py` `add()`: Move validation to API**
- Currently: file existence, readability, `.pub` extension checks in CLI
- Fix: `api/keys.py` validates in `add_key()` — CLI just passes path
- `cli/ssh.py` `_find_ssh_key_from_path()`: move to `core/key_manager.py` as `find_key_in_dir()`

**4g — `cli/host.py` `_abort_if_vms_running()`: API check**
- Currently: CLI imports `VMStatus` and checks running VMs
- Fix: `api/host.py` gets `check_vms_running() -> bool` — CLI just calls it

**4h — `cli/config.py` `dump_vm()`: Move I/O to API**
- Currently: file reading + JSON parsing in CLI
- Fix: `api/vm_config.py` gets `dump_vm_config(name) -> dict` — CLI just formats

**Commit series:** One commit per sub-item above (4a–4h), each verified with CI

---

### Pass 5 — Remove dead/near-dead helper functions

After Pass 4, audit all `_`-prefixed functions in each `cli/` file:
- If it's only used by one command and does simple formatting → keep in CLI
- If it's a duplicate of something in `utils/` → use `utils/` version
- If it does business logic → move to API

---

## Execution Order

```
Pass 1  ──► Pass 2 ──► Pass 3 ──► Pass 4 ──► Pass 5
(imports)      (shared    (model    (biz logic   (cleanup)
                  helpers)   types)    moves)
```

**Each pass:**
1. Implement changes
2. Run CI: `uv run ruff check src/ && uv run ruff format --check src/ && uv run mypy src/ && uv run pytest tests/ -q --cov=src/mvmctl --cov-fail-under=80`
3. Commit with descriptive message
4. Only proceed when CI is green

---

## Files in Scope

### `cli/` (primary)
- `vm.py` — highest priority (most violations)
- `console.py` — `_do_attach()` terminal logic
- `cache.py` — `cache_prune()` branching
- `key.py` — `add()` validation
- `ssh.py` — key resolution logic
- `host.py` — `_abort_if_vms_running()`, `init_cmd()`
- `config.py` — `dump_vm()` I/O
- `bin.py` — `bin_ls()` display logic
- `network.py` — mostly clean
- `image.py` — already well-refactored from previous session
- `kernel.py` — already clean
- `logs.py` — clean
- `_helpers.py` — becomes shared utility sink

### `api/` (new wrappers needed)
- `api/vms.py` — `resolve_vm_by_prefix()`, enriched `list_vms()`
- `api/console.py` — new module for `attach_console()` core logic
- `api/cache.py` — `prune_cache()`
- `api/keys.py` — `add_key()` validation
- `api/host.py` — `check_vms_running()`
- `api/vm_config.py` — `dump_vm_config()`

### `core/` (new functions)
- `core/console.py` — `attach_console()`, `detach_console()` (from `cli/console.py`)
- `core/key_manager.py` — `find_key_in_dir()` (from `cli/ssh.py`)

### `utils/` (potential additions)
- `utils/disk_size.py` — `get_file_size_from_path(path: Path) -> int` wrapper around `Path.stat()`

---

## Anti-Patterns to Eliminate

| Pattern | Replace with |
|---------|-------------|
| `info: dict[str, Any]` in display helpers | Model type (`VMInstance`, `ImageRecord`) |
| `import json` inside function | Top-level `import json` |
| `from mvmctl.api import func` inside function | Top-level import |
| `def create(...27 params...)` | Split: CLI collects → API resolves → Core executes |
| `manager.find_by_id_prefix()` in CLI | `api/vms.resolve_vm_by_prefix()` |
| `Path.stat()` for disk size in CLI | Pre-computed in API, passed as field |
| `_get_vm_defaults()` duplicated | Single `cli/_helpers.get_vm_defaults()` |
| Terminal socket code in CLI | `core/console.attach_console()` |
| 116-line `cache_prune()` | `api/cache.prune_cache()` returning structured results |
| File validation in CLI `add()` | `api/keys.add_key()` validates |

---

## Verification Checklist (After each pass)

- [ ] `uv run ruff check src/mvmctl/cli/` — zero errors
- [ ] `uv run ruff format --check src/mvmctl/cli/` — zero diffs
- [ ] `uv run mypy src/mvmctl/cli/` — zero errors
- [ ] `uv run pytest tests/unit/test_cli_*.py -q` — all pass
- [ ] No `dict[str, Any]` as return types or parameter types in display helpers
- [ ] No inline imports from `mvmctl.api` or `mvmctl.utils` inside function bodies
- [ ] No `manager.find_by_id_prefix()` or `get_vm_manager()` calls in CLI
- [ ] Behavioral contract: `mvm vm create --name x --image y` produces identical VM as before

---

## Commits on Branch

```
1fb807e fix(cli/vm): remove DB calls from inspect display helpers
8e51f2b fix: complete layer compliance for CLI — API encapsulation of all DB operations
43c044a refactor: extract image API and CLI from cli/bin.py, slim bin.py to binary-only
94bf158 refactor(api): replace binary update calls with register_binary()
cc9ad55 refactor: split kernel CLI into separate module and API layer
3c93d57 refactor: fetch image function cleanup
```
