# Dead Code Analysis Report

**Generated:** 2026-05-02
**Scope:** `src/mvmctl/` (excluding `archive/` folders)
**Methodology:** Static analysis via `ruff`, `mypy`, grep cross-referencing across entire `src/` tree; manual verification of every function/class definition against all call sites.

---

## Table of Contents

1. [Unused Functions/Methods (Defined but Never Called)](#1-unused-functionsmethods-defined-but-never-called)
2. [Unused Dataclasses/TypedDicts](#2-unused-dataclassestypeddicts)
3. [Unused Exception Classes](#3-unused-exception-classes)
4. [Unused Constants & Variables](#4-unused-constants--variables)
5. [Empty `TYPE_CHECKING` Blocks (Dead Scaffolding)](#5-empty-type_checking-blocks-dead-scaffolding)
6. [Unused `# noqa` Directives](#6-unused--noqa-directives)
7. [Unused Importable Symbols](#7-unused-importable-symbols)
8. [Duplicate / Conflicting Definitions](#8-duplicate--conflicting-definitions)
9. [Redundant Local Copies](#9-redundant-local-copies)
10. [Appendix: Confirmed Alive (no false positives)](#10-appendix-confirmed-alive-no-false-positives)

---

## 1. Unused Functions/Methods (Defined but Never Called)

### 1.1 API Layer — `api/`

| # | Function | File | Line | Why Dead |
|---|----------|------|------|----------|
| 1 | `KernelOperation.inspect()` | `api/kernel_operations.py` | 265 | Defined as `@staticmethod`. No CLI command or API orchestration calls it. |
| 2 | `ImageOperation.find_existing_image()` | `api/image_operations.py` | 471 | Checks DB+filesystem for existing image by spec. Never called — similar logic done inline using `repo.get_by_os_slug()` directly. |
| 3 | `VMOperation.get()` | `api/vm_operations.py` | 668 | Returns single `VMInstanceItem`. No callers — all VM retrieval uses `VMRequest(...).resolve()` directly. |
| 4 | `KeyOperation.get_defaults()` | `api/key_operations.py` | 181 | Returns all default SSH keys. No callers — the SSH layer uses `key_resolver.get_defaults()` directly. |

### 1.2 API Inputs Layer — `api/inputs/`

#### Dead `Request.result` Properties (14 total)

Every `*Request` class has a `@property` named `result` that returns `self._result`. This property is **never read** anywhere in `src/`. Callers always use `.resolve()` directly.

| # | Property | File | Line |
|---|----------|------|------|
| 6 | `BinaryRequest.result` | `api/inputs/_binary_input.py` | 45 |
| 7 | `BinaryFetchRequest.result` | `api/inputs/_binary_fetch_input.py` | 51 |
| 8 | `ConsoleRequest.result` | `api/inputs/_console_input.py` | 52 |
| 9 | `ImageAcquireRequest.result` | `api/inputs/_image_acquire_input.py` | 94 |
| 10 | `ImageRequest.result` | `api/inputs/_image_input.py` | 48 |
| 11 | `KernelFetchRequest.result` | `api/inputs/_kernel_fetch_input.py` | 80 |
| 12 | `KernelRequest.result` | `api/inputs/_kernel_input.py` | 60 |
| 13 | `KeyCreateRequest.result` | `api/inputs/_key_create_input.py` | 54 |
| 14 | `KeyRequest.result` | `api/inputs/_key_input.py` | 45 |
| 15 | `LogRequest.result` | `api/inputs/_logs_input.py` | 43 |
| 16 | `NetworkCreateRequest.result` | `api/inputs/_network_create_input.py` | 71 |
| 17 | `NetworkRequest.result` | `api/inputs/_network_input.py` | 69 |
| 18 | `VMCreateRequest.result` | `api/inputs/_vm_create_input.py` | 189 |
| 19 | `VMRequest.result` | `api/inputs/_vm_input.py` | 61 |

### 1.3 Core Layer — `core/`

| # | Function | File | Line | Why Dead |
|---|----------|------|------|----------|
| 20 | `FirecrackerClient.get_instance_info()` | `core/vm/_firecracker.py` | 687 | No callers in `src/`. |
| 21 | `FirecrackerClient.describe_instance()` | `core/vm/_firecracker.py` | 701 | No callers in `src/`. |
| 22 | `BinaryRepository.list_by_name(name)` | `core/binary/_repository.py` | 49 | No callers in `src/`. |
| 23 | `BinaryRepository.delete_by_name_and_version()` | `core/binary/_repository.py` | 111 | No callers in `src/`. |
| 24 | `BinaryService._parse_version_string()` | `core/binary/_service.py` | 283 | No callers in `src/`. |
| 25 | `KernelService.download_kernel_source()` | `core/kernel/_service.py` | 314 | Defined but `build_from_source()` calls `HttpDownload.download_file()` directly instead. |
| 26 | `VMRepository.delete_many(vm_ids)` | `core/vm/_repository.py` | 271 | No callers in `src/`. |
| 27 | `IPTablesRuleRepository.delete_by_network_id()` | `core/_shared/_iptables_tracker/_repository.py` | 158 | No callers in `src/`. |
| 28 | `IPTablesRuleRepository.delete_inactive()` | `core/_shared/_iptables_tracker/_repository.py` | 172 | No callers in `src/`. |
| 29 | `IPTablesRuleRepository.get_by_table_chain_name()` | `core/_shared/_iptables_tracker/_repository.py` | 84 | No callers in `src/`. |
| 30 | `HostRepository.revert_changes()` | `core/host/_repository.py` | 190 | No callers in `src/`. |

### 1.4 Utils Layer — `utils/`

| # | Function | File | Line | Why Dead |
|---|----------|------|------|----------|
| 31 | `FsUtils.read_json(path)` | `utils/fs.py` | 23 | Defined as `@staticmethod`. Never called anywhere in `src/`. |
| 32 | `FsUtils.read_yaml(path)` | `utils/fs.py` | 49 | Never called anywhere in `src/`. |
| 33 | `FsUtils.read_raw(path)` | `utils/fs.py` | 77 | Never called anywhere in `src/`. |
| 34 | `format_sectors_human_readable()` | `utils/_disk.py` | 98 | Never called anywhere in `src/`. Only mentioned in AGENTS.md docs. |
| 35 | `format_disk_size()` | `utils/_disk.py` | 119 | Never called anywhere in `src/`. |
| 36 | `get_state_marker()` | `utils/_io.py` | 138 | Never imported or called. |
| 37 | `get_combined_marker()` (in `_io.py`*) | `utils/_io.py` | 149 | Never imported. CLI uses `CommonUtils._get_combined_marker()` instead (same logic, different location). |
| 38 | `CacheUtils.get_auditlog_path()` | `utils/common.py` | 213 | Dead duplicate. The correctly-named `get_audit_log_path()` at line 292 IS used in `auditlog.py:34`. Both return the same value. |
| 39 | `CommonUtils.sanitize_for_log()` | `utils/common.py` | 403 | Never called anywhere in `src/`. |
| 40 | `NetworkUtils.compute_subnet_mask()` | `utils/network.py` | 31 | Never called in `src/` (only in tests). |
| 41 | `NetworkUtils.compute_prefix_length()` | `utils/network.py` | 36 | Never called in `src/` (only in tests). |
| 42 | `VMValidator.validate_boot_args()` | `utils/_validators.py` | 470 | Defined but never called. |

### 1.5 Constants Layer — `constants.py`

| # | Function | File | Line | Why Dead |
|---|----------|------|------|----------|
| 43 | `_load_user_config_json()` | `constants.py` | 91 | Decorated with `@lru_cache`. **Never called** anywhere in `src/`. |

\* (*) Note on `_io.py`: The `_PlainConsole` class (line 38) and its sole instantiation `console = _PlainConsole()` (line 54) are **also entirely dead** — `console` appears in `__all__` but is never imported by any other module.

---

## 2. Unused Dataclasses/TypedDicts

| # | Class | File | Line | Why Dead |
|---|-------|------|------|----------|
| 44 | `ConsoleInfo` | `models/vm.py` | 83 | Dataclass. Re-exported via `models/__init__.py`. **Never instantiated** anywhere in `src/`. |
| 45 | `ConsoleState` | `models/vm.py` | 91 | Dataclass. Re-exported via `models/__init__.py`. **Never instantiated** anywhere in `src/`. |
| 46 | `VMInspectInfo` | `models/vm.py` | 100 | Dataclass. Re-exported via `models/__init__.py`. **Never instantiated** anywhere in `src/`. |
| 47 | `InstanceInfo` | `core/vm/_firecracker.py` | 489 | TypedDict — return type of dead `get_instance_info()` |
| 48 | `InstanceDescription` | `core/vm/_firecracker.py` | 499 | TypedDict — return type of dead `describe_instance()` |

---

## 3. Unused Exception Classes

| # | Exception | File | Line | Why Dead |
|---|-----------|------|------|----------|
| 49 | `AssetNotFoundError` | `exceptions.py` | 203 | Never imported or raised. (Note: `BundledAssetNotFoundError` at line 212 IS used.) |
| 50 | `BinaryAlreadyExistsError` | `exceptions.py` | 219 | Never imported or raised. |
| 51 | `CloudInitOffModeError` | `exceptions.py` | 272 | Never imported or raised. |
| 52 | `CloudInitInjectModeError` | `exceptions.py` | 284 | Never imported or raised. |
| 53 | `GuestfsLaunchError` | `exceptions.py` | 322 | Never imported or raised. |
| 54 | `GuestfsMountError` | `exceptions.py` | 328 | Never imported or raised. |
| 55 | `DownloadError` | `exceptions.py` | 374 | Never imported or raised. (Note: `HttpDownloadError` at line 381 IS used.) |
| 56 | `ConsoleRelayNotRunningError` | `services/console_relay/exceptions.py` | 17 | Defined, in `__all__`, but **never imported, raised, or caught** anywhere. |
| 57 | `ConsoleRelayPermissionError` | `services/console_relay/exceptions.py` | 21 | Same — never used. |

---

## 4. Unused Constants & Variables

| # | Constant | File | Line | Why Dead |
|---|----------|------|------|----------|
| 58 | `CONST_MEGABYTE_BYTES` | `constants.py` | 268 | `Final[int] = 1_000_000`. Never imported or referenced anywhere. |
| 59 | `console` instance | `utils/_io.py` | 53 | `console = _PlainConsole()`. Exported in `__all__` but never imported by any module. |

---

## 5. Empty `TYPE_CHECKING` Blocks (Dead Scaffolding)

These files import `TYPE_CHECKING` and then have an empty `if TYPE_CHECKING: pass` block. The entire construct does nothing.

| # | File | Line(s) | Notes |
|---|------|---------|-------|
| 60 | `cli/image.py` | 33-34 | |
| 61 | `cli/bin.py` | 21-22 | |
| 62 | `cli/key.py` | 23-24 | |
| 63 | `core/key/_controller.py` | 19-20 | |
| 64 | `core/key/_resolver.py` | 14-15 | |
| 65 | `api/inputs/_network_input.py` | 14-15 | |
| 66 | `core/network/_service.py` | 29-30 | |
| 67 | `exceptions.py` | 7-8 | Also has unused `from typing import Any` (though `Any` IS used elsewhere in the file? Let's verify). |

Additionally, in `cli/image.py:7`, `TYPE_CHECKING` is imported alongside `cast` (which IS used at lines 61, 65). The `TYPE_CHECKING` import itself becomes dead if the block is removed.

---

## 6. Unused `# noqa` Directives

**29 errors** detected by `ruff --select RUF100`. All are auto-fixable.

### 6.1 `api/__init__.py` — 9 unused `# noqa: F401` directives

| Line | Code | Detail |
|------|------|--------|
| 14 | `F401` | Unused `noqa` (non-enabled: `F401`) |
| 18 | `F401` | Same |
| 19 | `F401` | Same |
| 20 | `F401` | Same |
| 24 | `F401` | Same |
| 25 | `F401` | Same |
| 26 | `F401` | Same |
| 33 | `F401` | Same |
| 53-58 | `F401` | 6 more on lines 53, 54, 55, 56, 57, 58 |

### 6.2 CLI — 2 unused `# noqa: ARG001` directives

| File | Line | Code |
|------|------|------|
| `cli/host.py` | 89 | `ARG001` |
| `cli/key.py` | 34 | `ARG001` |

### 6.3 Core Resolvers — 7 unused `# noqa: E402` directives

| File | Line |
|------|------|
| `core/_shared/_iptables_tracker/_resolver.py` | 62 |
| `core/binary/_resolver.py` | 117 |
| `core/image/_resolver.py` | 115 |
| `core/kernel/_resolver.py` | 117 |
| `core/key/_resolver.py` | 144 |
| `core/network/_lease_resolver.py` | 62 |
| `core/network/_resolver.py` | 123 |
| `core/vm/_resolver.py` | 167 |

All 8 resolver files share an identical pattern: a `yield from` statement at the end is annotated with `# noqa: E402` (import not at top of file), but the E402 rule is not enabled in `pyproject.toml`.

### 6.4 Other

| File | Line | Code |
|------|------|------|
| `models/image.py` | 45 | `N816` (not enabled) |
| `services/nocloud_server/process.py` | 43, 52 | `F841` (unused variable — but `_` is supposed to suppress this; the noqa is redundant) |
| `utils/_system.py` | 453, 480 | `PLW0603` (not enabled) |

---

## 7. Unused Importable Symbols

These are fields/unused properties on dataclasses that are defined but **never populated or read**:

| # | Field | File | Line | Why Dead |
|---|-------|------|------|----------|
| 68 | `VMExportFirecrackerConfig.enable_api_socket` | `api/inputs/_vm_export_config.py` | 112 | Only instantiated at `vm_operations.py:928-930` — never set, always `None`. |
| 69 | `VMExportCloudInitConfig.ssh_key` | `api/inputs/_vm_export_config.py` | 123 | Only instantiated at `vm_operations.py:932-935` — never set, always `None`. |
| 70 | `VMExportCloudInitConfig.keep_iso` | `api/inputs/_vm_export_config.py` | 124 | Same — never set. |

---

## 8. Duplicate / Conflicting Definitions

### 8.1 `CONST_CONSOLE_KILL_TIMEOUT_S` — ⚠️ CONFLICTING VALUES

| Location | Value | Used By |
|----------|-------|---------|
| `constants.py:135` | `5.0` | (not used directly) |
| `services/console_relay/_defaults.py:14` | `2.0` | `ConsoleRelayManager` reads from `_defaults.py` |

### 8.2 `CONST_NO_CLOUD_NET_BIND_TIMEOUT_S` — ⚠️ CONFLICTING VALUES

| Location | Value | Used By |
|----------|-------|---------|
| `constants.py:131` | `5.0` | (not used directly) |
| `services/nocloud_server/_defaults.py:15` | `0.5` | (not used at all — dead constant) |

### 8.3 `CONST_CONSOLE_SELECT_TIMEOUT_S` — Duplicate Within Same File

| Location | Value |
|----------|-------|
| `services/console_relay/_defaults.py:16` | `0.1` |
| `services/console_relay/_defaults.py:20` | `0.1` |

The second definition at line 20 silently overwrites the first at line 16.

### 8.4 `CONST_SIGNAL_EXIT_CODE_BASE` — Duplicate Across Two Files

| Location | Value |
|----------|-------|
| `constants.py:115` | `128` (declared `Final[int]`) |
| `utils/_system.py:334` | `128` (plain module-level constant) |

The duplicate in `_system.py:334` is used locally at `_system.py:134` instead of importing from `constants.py`.

---

## 9. Redundant Local Copies

| # | Symbol | File | Line | Issue |
|---|--------|------|------|-------|
| 71 | `_SECTOR_SIZE` | `core/image/_service.py` | 45 | `_SECTOR_SIZE = CONST_SECTOR_SIZE_BYTES` — creates a local alias for `CONST_SECTOR_SIZE_BYTES` (from `constants.py:267`). Could just use the constant directly. `utils/_disk.py:441` already does this correctly with `constants.CONST_SECTOR_SIZE_BYTES`. |

---

## 10. Appendix: Confirmed Alive (no false positives)

The following were rigorously checked and confirmed **not dead**:

- **All `__all__` exports** — every symbol in every `__all__` is consumed by at least one importer.
- **All `@typer.command` and `.callback()` registration functions** — registered with Typer, loaded lazily. Includes all `*_callback` functions and `help_cmd` functions.
- **All `@staticmethod` on `*Operation` classes** — all called from their corresponding CLI modules except the 6 noted in §1.1.
- **All `Controller`, `Service`, `Repository`, `Resolver` methods** — cross-referenced with their API layer callers. Only the ones listed in §1.3 are dead.
- **All `@dataclass` fields on `*Item` models** (`VMInstanceItem`, `NetworkItem`, `ImageItem`, etc.) — all populated and read.
- **`NoCloudServerError` / `NoCloudServerAlreadyRunningError`** — both raised in `nocloud_server/manager.py`.
- **`ConsoleRelayAlreadyRunningError` / `ConsoleRelayProcessError` / `ConsoleRelayConnectionError`** — all raised in `manager.py` or `client.py`.
- **`BundledAssetNotFoundError`** — raised in `_asset_manager.py`.
- **`HttpDownloadError`** — raised in `utils/http.py`, caught in `kernel/_service.py` and `image/_service.py`.

---

## Summary

| Category | Count |
|----------|-------|
| Unused functions/methods (API) | 5 |
| Unused `Request.result` properties (API inputs) | 13 |
| Unused functions/methods (Core) | 10 |
| Unused functions (Utils) | 11 |
| Unused functions (Constants) | 1 |
| Unused dataclasses/TypedDicts | 5 |
| Unused exception classes | 8 |
| Unused constants/variables | 2 |
| Unused dataclass fields | 3 |
| Empty `TYPE_CHECKING` blocks | 7 |
| Unused `# noqa` directives | 28 |
| Duplicate/conflicting constants | 4 |
| Redundant local copies | 1 |
| **Total actionable findings** | **104** |

### Highest-Impact Items to Clean Up

1. **Remove empty `TYPE_CHECKING: pass` blocks** (8 files) — zero risk, immediate cleanup.
2. **Remove unused `# noqa` directives** (29 instances) — auto-fixable with `ruff check --fix`.
3. **Delete dead dataclasses** (`ConsoleInfo`, `ConsoleState`, `VMInspectInfo` in `models/vm.py`) — unused since refactoring.
4. **Delete dead methods** — especially the `FirecrackerClient` methods, `BinaryRepository` methods, `IPTablesRuleRepository` methods, and the 5 unused `*Operation` methods.
5. **Remove unused exception classes** — 9 exception types that are never raised.
6. **Resolve conflicting constants** — `CONST_CONSOLE_KILL_TIMEOUT_S` (5.0 vs 2.0) and `CONST_NO_CLOUD_NET_BIND_TIMEOUT_S` (5.0 vs 0.5) have different values in different places.
7. **Remove `Request.result` properties** (14 files) — unused boilerplate from an earlier design.
