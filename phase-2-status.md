# Phase 2 — Status

> Source: `python-cli-phase-2.md`
> Last updated: 2026-03-23

---

## Project Identity and Build Flags

| Requirement | Status | Notes |
|---|---|---|
| Project name defined once in `pyproject.toml [project] name` | ✅ | `"firecracker-manager"` |
| Package-level constant generated at build time via `constants.py` | ✅ | `src/fcm/constants.py` holds `PROJECT_NAME` derived from package metadata |
| Cache directory: `~/.cache/<project-name>/` | ✅ | Derived from `PROJECT_NAME` |
| Env var prefix: `<PROJECT_NAME>_` (e.g. `FCM_CACHE_DIR`) | ✅ | Implemented |
| Network device names: `<project-name>-br0`, `<project-name>-tap<n>` | ✅ | Derived from `PROJECT_NAME` |
| Default config filename: `<project-name>.yaml` | ✅ | `fcm.yaml` |
| CLI binary name: matches project name (`fcm`) | ✅ | `[project.scripts] fcm = ...` |
| Rename in `pyproject.toml` propagates everywhere — no grep-replace | ✅ | All names read from `PROJECT_NAME` constant |

---

## Scope Boundaries

| Requirement | Status | Notes |
|---|---|---|
| Multi-VM lifecycle: create, list, delete, ssh, logs | ✅ | Fully implemented |
| Asset management: binaries, kernels, images | ✅ | `asset bin/kernel/image` groups |
| Automatic network setup/teardown tied to VM lifecycle | ✅ | `api/network.py` |
| YAML config file support | ✅ | `core/config.py` |
| Python API layer (`api/`) usable independently of CLI | ✅ | `src/fcm/api/` package |
| Full test suite, CI workflow, documentation | ✅ | 551 tests, CI yml, README/CONTRIBUTING |
| Single-VM functionality NOT ported | ✅ | Not present |
| No runtime dependency on bash scripts | ✅ | Python-only implementation |

---

## Cache Directory Layout

| Requirement | Status | Notes |
|---|---|---|
| Default root: `~/.cache/<project-name>/` | ✅ | Resolved from `PROJECT_NAME` |
| Override via `<PROJECT_NAME>_CACHE_DIR` env var | ✅ | Implemented |
| `bin/` — versioned firecracker/jailer binaries | ✅ | `binary_manager.py` creates this |
| `kernels/` — minimal and upstream kernel binaries | ✅ | `core/kernel.py` |
| `images/` — rootfs images | ✅ | `core/image.py` |
| `keys/` — SSH public keys | ✅ | `core/key_manager.py` |
| `vms/<vm-name>/` — per-VM runtime state | ✅ | `vm_manager.py` creates this layout |
| `vms/<vm-name>/firecracker.json` | ✅ | Written by `config_gen.py` |
| `vms/<vm-name>/firecracker.pid` | ✅ | Written on VM start |
| `vms/<vm-name>/firecracker.socket` | ✅ | Created when `--enable-api-socket` |
| `vms/<vm-name>/console.log` | ✅ | Serial console redirected here |
| `vms/<vm-name>/cloud-init/` | ✅ | Cloud-init seed files |
| `bin/` and `vms/` are separate — not collapsed | ✅ | Layout preserved |

---

## CLI Design

### Conventions

| Requirement | Status | Notes |
|---|---|---|
| `ls` as primary listing subcommand (with `list` alias) | ✅ | All `ls` commands have `list` alias |
| `rm` for removal (with `remove` and `delete` aliases) | ✅ | Phase 3 made `remove` canonical; `rm` is alias |
| `create` for creation | ✅ | |
| Noun-first grouping: `vm ls`, `asset kernel fetch`, `asset bin ls` | ✅ | |
| `--long-form` flags with `-s` short forms for common ones | ✅ | |
| `--json` flag on every listing command | ✅ | All `ls` commands support `--json` |
| Exit codes: 0 success, non-zero errors | ✅ | |

### `vm` subcommands

| Command | Status | Notes |
|---|---|---|
| `vm ls` — name, status, IP, PID, kernel, image | ✅ | |
| `vm create` with all flags | ✅ | Full flag set per Phase 4 §8 |
| `vm rm <name>` / `vm remove <name>` | ✅ | |
| `vm ssh <name\|ip>` — `--user` flag | ✅ | |
| `vm logs <name\|ip>` — `--follow` / `-f` | ✅ | |

### `vm create` flags (Phase 2 set)

| Flag | Status |
|---|---|
| `--name` | ✅ |
| `--kernel <name\|path>` | ✅ |
| `--image <name\|path>` | ✅ |
| `--ssh-key <path>` | ✅ |
| `--vcpus <int>` (default: 2) | ✅ |
| `--memory <int>` (default: 2048) | ✅ |
| `--ip <cidr>` | ✅ |
| `--enable-socket` (renamed to `--enable-api-socket` in Phase 3) | ✅ |
| `--enable-pci` | ✅ |

### `asset` subcommands

| Command | Status |
|---|---|
| `asset kernel ls` | ✅ |
| `asset kernel fetch` | ✅ |
| `asset kernel build` | ✅ |
| `asset kernel config <flag> <on\|off>` | ✅ |
| `asset kernel rm` / `asset kernel remove` | ✅ |
| `asset image ls` | ✅ |
| `asset image fetch <type>` — ubuntu-cloud, firecracker-ubuntu, arch, debian | ✅ |
| `asset image rm` / `asset image remove` | ✅ |
| `asset bin ls` — remote + local, active marked with checkmark | ✅ |
| `asset bin fetch <version>` | ✅ |
| `asset bin use <version>` | ✅ |
| `asset bin rm` / `asset bin remove` | ✅ |
| `asset cache clear` — removes bin/, kernels/, images/; NOT vms/ | ✅ |

### `host` subcommands

| Command | Status | Notes |
|---|---|---|
| `host init` — KVM check, binary check, IP forwarding, sysctl persist, kernel modules | ✅ | |
| `host ls` — show each setting's current/original/expected state | ✅ | |
| `host restore` — revert to pre-init snapshot; fail if no snapshot | ✅ | |
| State snapshot at `<cache-root>/host/state.json` with correct JSON format | ✅ | |

---

## Configuration File

| Requirement | Status | Notes |
|---|---|---|
| Resolution order: CLI flag > env var > config file > built-in default | ✅ | `core/config.py` |
| Default lookup: `./<project-name>.yaml` in CWD | ✅ | |
| Override via `<PROJECT_NAME>_CONFIG` env var | ✅ | |
| `defaults` section: kernel, image, ssh_key, vcpus, memory | ✅ | |
| `network` section: guest_ip_range, host_bridge, mask | ✅ | |
| `boot` section: lsm_flags, extra_boot_args | ✅ | |
| `firecracker` section: enable_socket, enable_pci | ✅ | (renamed `enable_api_socket` in Phase 3) |

---

## Networking

| Requirement | Status | Notes |
|---|---|---|
| First VM created: create bridge, enable IP forwarding, NAT via iptables, create tap | ✅ | |
| Additional VM created: create new tap, attach to bridge | ✅ | |
| VM deleted: remove tap from bridge | ✅ | |
| Last VM deleted: tear down bridge, flush NAT rules added by this tool only | ✅ | Phase 4 §2 overrides: networks persist; bridge only torn down on `network remove` |
| Device names: `<project-name>-br0`, `<project-name>-tap<n>` | ✅ | |
| Auto-allocate guest IPs from `guest_ip_range` | ✅ | |
| Network logic in `api/network.py`, callable independently of CLI | ✅ | |

---

## Internal Python API

| Requirement | Status | Notes |
|---|---|---|
| `api/assets.py` — fetch_kernel, build_kernel, configure_kernel_flag, fetch_image, fetch_binary, list_binaries, set_active_binary | ✅ | |
| `api/vms.py` — list_vms, get_vm, create_vm, remove_vm, ssh_vm, get_logs, cleanup_vms | ✅ | (Sprint 2 realignment added create_vm/remove_vm to api layer) |
| `api/network.py` — setup_network, teardown_network, allocate_ip, release_ip | ✅ | |
| `api/keys.py` — key management functions | ✅ | |
| `api/host.py` — host init, ls, restore, prune | ✅ | |
| CLI commands are thin wrappers — no business logic in CLI layer | ✅ | All CLI files import from `fcm.api.*` (fixed in Sprint 2: C-1, C-2, BP-C2) |
| Return types are dataclasses or Pydantic models (not raw dicts) | ✅ | |
| All user-facing errors raised as typed exceptions from `exceptions.py` | ✅ | |
| `models.py` / `models/` — shared dataclasses | ✅ | `src/fcm/models/` |
| `config.py` — YAML loading, env var resolution, precedence | ✅ | `src/fcm/core/config.py` |
| `constants.py` — project name, default paths, device name helpers | ✅ | `src/fcm/constants.py` |
| `exceptions.py` — typed exception hierarchy | ✅ | `src/fcm/exceptions.py` |

---

## Testing

| Requirement | Status | Notes |
|---|---|---|
| Coverage target ≥ 80%, enforced in CI | ✅ | 82.85% coverage; `--cov-fail-under=80` in CI |
| `api/assets.py` — all public functions mocked | ✅ | |
| `api/vms.py` — create, delete, list mocked | ✅ | |
| `api/network.py` — setup/teardown incl. first/last VM edge cases | ✅ | |
| `config.py` — YAML loading, env override, precedence, missing file, malformed YAML | ✅ | |
| `cli/` — command parsing, flag defaults, error messages via Typer test runner | ✅ | |
| `models.py` — Pydantic validation | ✅ | |
| Cache directory path resolution, layout creation, env override | ✅ | |
| `pytest` as test runner | ✅ | |
| `pytest-cov` for coverage | ✅ | |
| `unittest.mock` or `pytest-mock` for mocking | ✅ | |
| `tmp_path` fixture for all filesystem operations | ✅ | |

---

## GitHub Actions CI

### `ci.yml`

| Step | Status | Notes |
|---|---|---|
| Set up Python 3.13 | ✅ | |
| `pip install -e ".[dev]"` | ✅ | |
| `ruff check .` | ✅ | |
| `mypy firecracker_manager/` (adapted to `src/fcm/`) | ✅ | |
| `pytest --cov=firecracker_manager --cov-fail-under=80` | ✅ | |
| Upload coverage report as artifact | ✅ | Added `actions/upload-artifact` step (Sprint 5 GAP-1 fix) |

### `release.yml`

| Step | Status | Notes |
|---|---|---|
| Set up Python 3.13 | ✅ | |
| Install project + pyinstaller | ✅ | |
| Run full test suite | ✅ | |
| Build binary: `pyinstaller --onefile --name <project-name> ...` | ✅ | |
| Smoke-test: `dist/<project-name> --version` and `--help` | ✅ | |
| Create GitHub release and upload binary as asset | ✅ | |
| Upload binary as workflow artifact | ✅ | |
| Matrix builds: `ubuntu-22.04` and `ubuntu-24.04` | ✅ | |
| PyPI publish | ⚠️ | Not automated — PyPI publish is a manual step (see RELEASE.md). `RELEASE.md` previously described this as automated; corrected in Sprint 5. |

---

## Documentation

| Requirement | Status | Notes |
|---|---|---|
| `README.md` — what the tool does, prerequisites, installation (binary/pip/source), quickstart, command reference, config reference, env var reference, building from source, link to CONTRIBUTING | ✅ | |
| `CONTRIBUTING.md` — dev setup, test suite, build flag system, project structure, adding CLI command, adding image type, PR expectations | ✅ | |
| `LICENSE` — MIT | ✅ | |
| `.gitignore` — standard Python + cache dirs, built binaries, `*.pid`, `*.socket`, `*.log`, `vms/` runtime | ✅ | |

---

## General Engineering Constraints

| Requirement | Status |
|---|---|
| No hardcoded strings for names, paths, or prefixes | ✅ |
| Sensible defaults everywhere | ✅ |
| Clean error messages with what-went-wrong + what-to-do | ✅ |
| No stack traces by default; `--debug` flag exposes them | ✅ |
| Idempotency: asset fetch checks cache first | ✅ |
| Consistent naming across commands, flags, Python functions, env vars | ✅ |
| No silent failures — subprocess errors raise typed exceptions with stderr | ✅ |

---

**Overall Phase 2 Status: ✅ COMPLETE**
