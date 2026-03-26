# Firecracker Manager CLI — Migration Plan

> Convert the existing bash-based tooling into a structured Python CLI application using `uv` and Python 3.13.

---

## Table of Contents

1. [Goals and Principles](#1-goals-and-principles)
2. [Project Structure](#2-project-structure)
3. [Tooling and Environment](#3-tooling-and-environment)
4. [Configuration Design](#4-configuration-design)
5. [CLI Command Specification](#5-cli-command-specification)
6. [Core Modules](#6-core-modules)
7. [Assets Pipeline](#7-assets-pipeline)
8. [Kernel Build Pipeline](#8-kernel-build-pipeline)
9. [Error Handling and Logging](#9-error-handling-and-logging)
10. [Implementation Phases](#10-implementation-phases)
11. [Dependency Rationale](#11-dependency-rationale)

---

## 1. Goals and Principles

- **Single entrypoint.** Everything that was a bash script becomes a subcommand under `mvm`.
- **Minimal dependencies.** Only pull in a library if it meaningfully reduces code complexity. No heavy frameworks.
- **Config lives in YAML.** Assets, image sources, and kernel config are declared in files under `assets/`. Everything else is a CLI flag with a sensible default.
- **No magic paths.** The tool must work from any working directory. Paths to the Firecracker binary, socket directory, and run directory are resolved from config or explicit flags — never hardcoded.
- **Composable.** Commands produce clean stdout (text or JSON), making them scriptable and pipe-friendly.
- **Idempotent operations.** `create`, `cleanup`, and asset fetching should be safe to re-run.

---

## 2. Project Structure

```
firecracker-manager/
├── pyproject.toml               # uv project manifest, entry point, deps
├── uv.lock                      # locked dependency graph
├── .python-version              # pins 3.13
├── README.md
├── PLAN.md                      # this file
│
├── assets/                      # YAML-driven config (committed to git)
│   ├── images.yaml              # image sources, formats, conversion params
│   ├── kernel.yaml              # kernel version, config flags, build options
│   └── defaults.yaml            # default VM sizes, network settings, etc.
│
├── src/
│   └── mvm/
│       ├── __init__.py
│       ├── main.py              # Typer app root, registers command groups
│       │
│       ├── cli/                 # One file per command group
│       │   ├── __init__.py
│       │   ├── vm.py            # create, delete, list, ssh, logs, cleanup
│       │   ├── image.py         # fetch, convert, list-local
│       │   ├── kernel.py        # build, list-local
│       │   └── config.py        # show, validate, dump-template
│       │
│       ├── core/                # Business logic, no CLI concerns
│       │   ├── __init__.py
│       │   ├── vm_manager.py    # create/delete/list VM state and sockets
│       │   ├── firecracker.py   # spawn process, API client over Unix socket
│       │   ├── config_gen.py    # generate Firecracker JSON config from params
│       │   ├── image.py         # download, verify, convert images
│       │   ├── kernel.py        # clone kernel, apply config, build
│       │   └── ssh.py           # construct and exec ssh command
│       │
│       ├── models/              # Dataclasses and typed structures
│       │   ├── __init__.py
│       │   ├── vm.py            # VMConfig, VMState, VMInstance
│       │   └── image.py         # ImageSpec, KernelSpec
│       │
│       └── utils/
│           ├── __init__.py
│           ├── console.py       # Rich console wrapper (tables, spinners)
│           ├── process.py       # subprocess helpers with streaming output
│           └── fs.py            # path helpers, temp dir management
│
└── tests/
    ├── unit/
    └── integration/
```

---

## 3. Tooling and Environment

### uv setup

```bash
# Bootstrap the project
uv init firecracker-manager
cd firecracker-manager
uv python pin 3.13

# Add runtime dependencies
uv add typer rich pyyaml

# Add dev dependencies
uv add --dev pytest pytest-cov ruff mypy
```

### `pyproject.toml` — key sections

```toml
[project]
name = "firecracker-manager"
version = "0.1.0"
requires-python = ">=3.13"
dependencies = [
    "typer>=0.12",
    "rich>=13",
    "pyyaml>=6",
]

[project.scripts]
mvm = "mvm.main:app"

[build-system]
requires = ["hatchling"]
build-backend = "hatchling.build"

[tool.ruff]
line-length = 100
target-version = "py313"

[tool.mypy]
python_version = "3.13"
strict = true
```

### Running locally

```bash
uv run mvm --help
uv run mvm vm list
uv run mvm image fetch ubuntu-22.04
```

---

## 4. Configuration Design

### Principle

CLI flags override YAML config, which overrides built-in defaults. No environment variable magic unless it is a standard convention (e.g. `HOME`).

### `assets/defaults.yaml`

```yaml
firecracker:
  binary: /usr/local/bin/firecracker
  socket_dir: /tmp/mvm/sockets
  run_dir: /tmp/mvm/run
  log_dir: /tmp/mvm/logs

vm_defaults:
  vcpu_count: 2
  mem_size_mib: 512
  network_interface: eth0
  boot_args: "console=ttyS0 reboot=k panic=1 pci=off"
```

### `assets/images.yaml`

```yaml
images:
  - id: ubuntu-22.04
    source: https://cloud-images.ubuntu.com/jammy/current/jammy-server-cloudimg-amd64.img
    format: qcow2
    convert_to: ext4
    size_mib: 1024
    sha256: <checksum>

  - id: alpine-3.19
    source: https://dl-cdn.alpinelinux.org/alpine/v3.19/releases/x86_64/alpine-minirootfs-3.19.0-x86_64.tar.gz
    format: rootfs-tar
    convert_to: ext4
    size_mib: 512
    sha256: <checksum>
```

### `assets/kernel.yaml`

```yaml
kernel:
  version: "6.1.102"
  source: https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.1.102.tar.xz
  sha256: <checksum>
  config_fragments:
    - assets/kernel-configs/firecracker-base.config
  output_name: vmlinux
  build_dir: /tmp/mvm/kernel-build
```

### Config loading in code

All YAML files are loaded once at startup by a `ConfigLoader` class in `core/`. There is no global mutable config object — the loaded config is passed explicitly to commands that need it. This makes testing straightforward.

---

## 5. CLI Command Specification

All commands follow the pattern: `mvm <group> <action> [options]`

### 5.1 `mvm vm`

| Command | Description | Key flags |
|---|---|---|
| `mvm vm create` | Spawn a new Firecracker VM | `--name`, `--kernel`, `--rootfs`, `--cpu`, `--mem`, `--tap`, `--mac`, `--config` |
| `mvm vm rm` | Stop and remove a VM | `--name`, `--force` |
| `mvm vm list` | Show running and stopped VMs | `--json`, `--all` |
| `mvm vm ssh` | Open an SSH session into a VM | `--name`, `--user`, `--key`, `--cmd` |
| `mvm vm logs` | Print VM serial console output | `--name`, `--follow`, `--lines` |
| `mvm vm prune` | Remove stopped VMs and stale sockets | `--all`, `--name`, `--dry-run` |
| `mvm vm snapshot` | Snapshot VM memory and disk state | `--name`, `--out` |

**Example**

```bash
# Minimal — uses defaults from assets/defaults.yaml
mvm vm create --name dev-01 --rootfs ubuntu-22.04 --kernel 6.1.102

# Explicit
mvm vm create \
  --name dev-01 \
  --kernel /opt/kernels/vmlinux \
  --rootfs /opt/images/ubuntu.ext4 \
  --cpu 4 \
  --mem 1024

# SSH into a running VM
mvm vm ssh --name dev-01

# Tail logs
mvm vm logs --name dev-01 --follow

# Clean everything up
mvm vm cleanup --all --dry-run
mvm vm cleanup --all
```

### 5.2 `mvm image`

| Command | Description | Key flags |
|---|---|---|
| `mvm image fetch` | Download and convert an image | `--id`, `--out`, `--force` |
| `mvm image fetch-all` | Fetch all images in `images.yaml` | `--force` |
| `mvm image list` | Show locally available images | `--json` |
| `mvm image convert` | Convert an existing image file | `--src`, `--dst`, `--format`, `--size` |
| `mvm image delete` | Remove a local image | `--id` |

**Example**

```bash
mvm image fetch ubuntu-22.04
mvm image fetch-all
mvm image list
```

### 5.3 `mvm kernel`

| Command | Description | Key flags |
|---|---|---|
| `mvm kernel build` | Download and compile the kernel | `--version`, `--config`, `--jobs`, `--out` |
| `mvm kernel list` | Show locally built kernels | `--json` |
| `mvm kernel clean` | Remove kernel build artifacts | `--version` |

**Example**

```bash
mvm kernel build
mvm kernel build --version 6.1.102 --jobs 8
mvm kernel list
```

### 5.4 `mvm config`

| Command | Description | Key flags |
|---|---|---|
| `mvm config show` | Print resolved config (defaults + YAML) | `--section` |
| `mvm config validate` | Validate all YAML config files | _(none)_ |
| `mvm config dump-vm` | Print the Firecracker JSON config for a VM | `--name` |

---

## 6. Core Modules

### `core/firecracker.py` — API client

Firecracker is controlled via a REST API over a Unix domain socket. This module wraps those HTTP calls using only the Python standard library (`http.client` with a custom `UnixSocketHTTPConnection`). No `requests` or `httpx` needed.

Responsibilities:
- `spawn(config_path, socket_path, log_path)` — launch the Firecracker process
- `put_boot_source(socket, kernel_path, boot_args)`
- `put_drive(socket, drive_id, path, read_only)`
- `put_network_interface(socket, iface_id, tap_name, mac)`
- `start_instance(socket)`
- `create_snapshot(socket, mem_path, snapshot_path)`

### `core/vm_manager.py` — VM state

Manages a lightweight state file (JSON) at `{run_dir}/state.json` to track VM metadata: name, PID, socket path, IP, creation time, status.

Responsibilities:
- `register(vm_instance)` — write new VM to state
- `get(name)` → `VMInstance`
- `list_all()` → `list[VMInstance]`
- `deregister(name)` — remove from state, clean up socket

### `core/config_gen.py` — config generation

Translates a `VMConfig` dataclass into the Firecracker JSON config format. Keeps the JSON schema knowledge in one place. Can also write the config to a temp file for `--config` flag inspection.

### `core/image.py` — image pipeline

Responsibilities:
- Download with progress bar (using `urllib` + Rich progress)
- SHA-256 verification
- `qcow2 → raw → ext4` conversion via `qemu-img` (subprocess call)
- `tar rootfs → ext4` via `dd` + `mkfs.ext4` + `tar` (subprocess)

`qemu-img` is the only external binary dependency for this module. Its presence is checked at startup when image commands are used.

### `core/ssh.py`

Builds the `ssh` command from VM state (IP, user, key path) and either `exec`s it directly (replacing the process) or runs it as a subprocess when `--cmd` is passed.

---

## 7. Assets Pipeline

The image pipeline is one of the more involved parts of the original bash scripts. The Python version structures it as a repeatable, resumable pipeline per image:

```
Download (with resume)
  → Verify SHA-256
    → Convert format (qemu-img or manual ext4 creation)
      → Resize if needed
        → Write to output path
          → Update local image index
```

Each stage is a function that takes the current artifact path and returns the new path. If a stage has already completed and the output exists, it is skipped (idempotent). This replaces the ad-hoc `if [ -f ... ]` guards scattered through the bash scripts.

---

## 8. Kernel Build Pipeline

```
Read kernel.yaml
  → Download tarball (with SHA-256 verify)
    → Extract to build_dir
      → Copy + merge config fragments
        → make olddefconfig
          → make vmlinux -j{N}
            → Copy vmlinux to output path
              → Register in local kernel index
```

`jobs` defaults to `os.cpu_count()`. All `make` invocations stream output in real time via `process.py` helpers rather than buffering and printing at the end.

---

## 9. Error Handling and Logging

### User-facing errors

Typer + Rich handles this well. Define a small set of exception types in `mvm/exceptions.py`:

```python
class FCMError(Exception): ...
class VMNotFoundError(FCMError): ...
class VMAlreadyExistsError(FCMError): ...
class FirecrackerAPIError(FCMError): ...
class ImageNotFoundError(FCMError): ...
class ChecksumMismatchError(FCMError): ...
```

The CLI layer catches `FCMError` subclasses and prints a clean Rich error panel, then exits with a non-zero code. Unexpected exceptions bubble up and print a traceback (or a condensed version with a `--debug` flag).

### Verbosity

- Default: only meaningful output (table of VMs, success/failure message).
- `--verbose` / `-v`: show subprocess commands being run, API calls made.
- `--debug`: full tracebacks, raw API responses.

---

## 10. Implementation Phases

### Phase 1 — Scaffold and VM lifecycle

- [ ] `uv` project setup, `pyproject.toml`, `.python-version`
- [ ] `main.py` with Typer app and command group registration
- [ ] `assets/defaults.yaml` and `ConfigLoader`
- [ ] `core/firecracker.py` — Unix socket API client
- [ ] `core/vm_manager.py` — state file CRUD
- [ ] `core/config_gen.py` — Firecracker JSON generation
- [ ] `cli/vm.py` — `create`, `delete`, `list`, `cleanup`
- [ ] `utils/console.py` — Rich table for `vm list`

### Phase 2 — SSH and logs

- [ ] `core/ssh.py`
- [ ] `cli/vm.py` — `ssh`, `logs --follow`
- [ ] `utils/process.py` — streaming subprocess output for logs

### Phase 3 — Image pipeline

- [ ] `assets/images.yaml`
- [ ] `core/image.py` — download, verify, convert
- [ ] `cli/image.py` — `fetch`, `fetch-all`, `list`, `convert`

### Phase 4 — Kernel build

- [ ] `assets/kernel.yaml`
- [ ] `core/kernel.py` — download, extract, build
- [ ] `cli/kernel.py` — `build`, `list`

### Phase 5 — Polish

- [ ] `cli/config.py` — `show`, `validate`, `dump-vm`
- [ ] VM snapshot
- [ ] `--json` flag on all list commands
- [ ] `--dry-run` on destructive commands
- [ ] Unit tests for `config_gen`, `vm_manager`, `image` pipeline
- [ ] `ruff` + `mypy` clean

---

## 11. Dependency Rationale

| Package | Why | Alternative considered |
|---|---|---|
| `typer` | Declarative CLI from type hints, automatic `--help` | `argparse` — too verbose; `click` — Typer is a thin wrapper that adds type safety |
| `rich` | Tables, progress bars, spinners, error panels with zero effort | `tabulate` — less capable; rolling our own — not worth it |
| `pyyaml` | Read `assets/*.yaml` config files | `tomllib` (stdlib) — TOML is less natural for lists of objects like image specs |

Everything else — HTTP over Unix sockets, subprocess management, file I/O, SHA-256 hashing — uses the Python standard library only. No `requests`, no `httpx`, no `paramiko`, no Docker SDK.

`qemu-img` is the only external binary required for the image pipeline. Its absence is caught at runtime with a clear error message pointing to how to install it.
