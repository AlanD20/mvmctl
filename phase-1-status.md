# Phase 1 — Status

> Source: `python-cli-phase-1.md`
> Last updated: 2026-03-23

---

## §2 Project Structure

| Requirement | Status | Notes |
|---|---|---|
| `pyproject.toml` — uv project manifest, entry point, deps | ✅ | Present and complete |
| `uv.lock` — locked dependency graph | ✅ | Present |
| `.python-version` — pins 3.13 | ✅ | Present |
| `README.md` | ✅ | Present |
| `assets/images.yaml` | ✅ | Present |
| `assets/kernel.yaml` | ✅ | Present |
| `assets/defaults.yaml` | ✅ | Present |
| `src/fcm/__init__.py` | ✅ | Present |
| `src/fcm/main.py` — Typer root, registers command groups | ✅ | Present |
| `src/fcm/cli/__init__.py` | ✅ | Present |
| `src/fcm/cli/vm.py` — create, delete, list, ssh, logs, cleanup | ✅ | Present (uses Phase 2/3/4 naming) |
| `src/fcm/cli/image.py` — fetch, convert, list-local | ✅ | Implemented as `asset image` group |
| `src/fcm/cli/kernel.py` — build, list-local | ✅ | Implemented as `asset kernel` group |
| `src/fcm/cli/config.py` — show, validate, dump-template | ✅ | Present |
| `src/fcm/core/__init__.py` | ✅ | Present |
| `src/fcm/core/vm_manager.py` | ✅ | Present |
| `src/fcm/core/firecracker.py` | ✅ | Present |
| `src/fcm/core/config_gen.py` | ✅ | Present |
| `src/fcm/core/image.py` | ✅ | Present |
| `src/fcm/core/kernel.py` | ✅ | Present |
| `src/fcm/core/ssh.py` | ✅ | Present |
| `src/fcm/models/__init__.py` | ✅ | Present |
| `src/fcm/models/vm.py` — VMConfig, VMState, VMInstance | ✅ | Present |
| `src/fcm/models/image.py` — ImageSpec, KernelSpec | ✅ | Present |
| `src/fcm/utils/__init__.py` | ✅ | Present |
| `src/fcm/utils/console.py` — Rich wrapper | ✅ | Present |
| `src/fcm/utils/process.py` — subprocess helpers | ✅ | Present |
| `src/fcm/utils/fs.py` — path helpers | ✅ | Present |
| `tests/unit/` | ✅ | Present, 551 tests pass |
| `tests/integration/` | ✅ | Directory present |

---

## §3 Tooling and Environment

| Requirement | Status | Notes |
|---|---|---|
| `pyproject.toml [project] name = "firecracker-manager"` | ✅ | Set |
| `pyproject.toml [project] version = "0.1.0"` | ✅ | Set |
| `pyproject.toml requires-python = ">=3.13"` | ✅ | Set |
| `dependencies: typer>=0.12, rich>=13, pyyaml>=6` | ✅ | All pinned in pyproject.toml |
| `[project.scripts] fcm = "fcm.main:app"` | ✅ | Set |
| `[build-system] hatchling` | ✅ | Set |
| `[tool.ruff] line-length = 100, target-version = "py313"` | ✅ | Set |
| `[tool.mypy] python_version = "3.13", strict = true` | ✅ | Set |
| Dev deps: pytest, pytest-cov, ruff, mypy | ✅ | In `[project.optional-dependencies] dev` |

---

## §4 Configuration Design

| Requirement | Status | Notes |
|---|---|---|
| `assets/defaults.yaml` — firecracker binary path, socket_dir, run_dir, log_dir | ✅ | Present |
| `assets/defaults.yaml` — vm_defaults: vcpu_count, mem_size_mib, network_interface, boot_args | ✅ | Present |
| `assets/images.yaml` — ubuntu-22.04, alpine-3.19 entries with source/format/size/sha256 | ✅ | Present |
| `assets/kernel.yaml` — version, source, sha256, config_fragments, output_name, build_dir | ✅ | Present |
| `ConfigLoader` class in `core/` — loads YAML once at startup, passed explicitly | ✅ | Implemented in `core/config.py` |

---

## §5 CLI Command Specification

### 5.1 `fcm vm`

| Command | Status | Notes |
|---|---|---|
| `fcm vm create` | ✅ | Implemented with all Phase 1–4 flags |
| `fcm vm delete` / `vm remove` | ✅ | Phase 3/4 canonical verb is `remove` |
| `fcm vm list` / `vm ls` | ✅ | Both aliases work |
| `fcm vm ssh` | ✅ | Implemented |
| `fcm vm logs` | ✅ | Implemented with `--follow` |
| `fcm vm cleanup` | ✅ | Implemented |
| `fcm vm pause` | ✅ | Removed per Phase 4 §3; responds with message |
| `fcm vm resume` | ✅ | Removed per Phase 4 §3; responds with message |
| `fcm vm snapshot` | ✅ | Implemented |

### 5.2 `fcm image` (as `fcm asset image`)

| Command | Status | Notes |
|---|---|---|
| `fcm asset image fetch <type>` | ✅ | Implemented |
| `fcm asset image ls` | ✅ | Implemented |
| `fcm asset image remove` | ✅ | Implemented |

### 5.3 `fcm kernel` (as `fcm asset kernel`)

| Command | Status | Notes |
|---|---|---|
| `fcm asset kernel build` | ✅ | Implemented |
| `fcm asset kernel ls` | ✅ | Implemented |
| `fcm asset kernel fetch` | ✅ | Implemented |
| `fcm asset kernel remove` | ✅ | Implemented |

### 5.4 `fcm config`

| Command | Status | Notes |
|---|---|---|
| `fcm config show` | ✅ | Implemented |
| `fcm config validate` | ✅ | Implemented |
| `fcm config dump-vm` | ✅ | Implemented |

---

## §6 Core Modules

| Module | Requirement | Status |
|---|---|---|
| `core/firecracker.py` | `spawn()`, `put_boot_source()`, `put_drive()`, `put_network_interface()`, `start_instance()`, `pause_vm()`, `resume_vm()`, `create_snapshot()` | ✅ |
| `core/vm_manager.py` | `register()`, `get()`, `list_all()`, `deregister()`, state file at `{run_dir}/state.json` | ✅ |
| `core/config_gen.py` | Translate `VMConfig` → Firecracker JSON | ✅ |
| `core/image.py` | Download w/ progress, SHA-256, qcow2→raw→ext4, tar→ext4 | ✅ |
| `core/ssh.py` | Build SSH command from VM state, exec or subprocess | ✅ |

---

## §9 Error Handling and Logging

| Requirement | Status | Notes |
|---|---|---|
| `FCMError`, `VMNotFoundError`, `VMAlreadyExistsError`, `FirecrackerAPIError`, `ImageNotFoundError`, `ChecksumMismatchError` in `exceptions.py` | ✅ | All present in `src/fcm/exceptions.py` |
| CLI catches `FCMError`, prints Rich error panel, exits non-zero | ✅ | Implemented |
| `--verbose` / `-v` flag | ✅ | Implemented |
| `--debug` flag — full tracebacks | ✅ | Implemented |

---

## §10 Implementation Phases (Phase 1 checklist)

| Item | Status |
|---|---|
| `uv` project setup, `pyproject.toml`, `.python-version` | ✅ |
| `main.py` with Typer app and command group registration | ✅ |
| `assets/defaults.yaml` and `ConfigLoader` | ✅ |
| `core/firecracker.py` — Unix socket API client | ✅ |
| `core/vm_manager.py` — state file CRUD | ✅ |
| `core/config_gen.py` — Firecracker JSON generation | ✅ |
| `cli/vm.py` — `create`, `delete`, `list`, `cleanup` | ✅ |
| `utils/console.py` — Rich table for `vm list` | ✅ |
| `core/ssh.py` | ✅ |
| `cli/vm.py` — `ssh`, `logs --follow` | ✅ |
| `utils/process.py` — streaming subprocess output | ✅ |
| `assets/images.yaml` | ✅ |
| `core/image.py` — download, verify, convert | ✅ |
| `cli/image.py` (as `asset image`) — fetch, list, convert | ✅ |
| `assets/kernel.yaml` | ✅ |
| `core/kernel.py` — download, extract, build | ✅ |
| `cli/kernel.py` (as `asset kernel`) — build, list | ✅ |
| `cli/config.py` — show, validate, dump-vm | ✅ |
| `--json` flag on all list commands | ✅ |
| `--dry-run` on destructive commands | ✅ |
| Unit tests | ✅ 551 tests, 82.85% coverage |
| `ruff` clean | ✅ |
| `mypy` clean | ✅ |

---

**Overall Phase 1 Status: ✅ COMPLETE**
