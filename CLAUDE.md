# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

This repo contains a Python CLI tool (`firecracker-manager`) for managing Firecracker microVMs on Linux, along with legacy bash reference scripts (`single-vm/`, `multi-vm/`). The Python CLI is the active project; the bash scripts are reference only.

All Python work lives under `firecracker-manager/`.

## Commands

All commands must be run from inside `firecracker-manager/`.

```bash
# Install dependencies
uv sync --group dev

# Run CLI from source
uv run fcm --help

# Run all tests
uv run pytest tests/ -v

# Run a single test file or pattern
uv run pytest tests/unit/test_vm_manager.py -v
uv run pytest tests/ -k "test_create" -v

# Run tests with coverage
uv run pytest tests/ --cov=src/fcm

# Lint
uv run ruff check src/
uv run ruff check src/ --fix

# Format
uv run ruff format src/

# Type check
uv run mypy src/
```

## Architecture

### Layer Separation

The codebase uses a strict two-layer architecture:

- **`src/fcm/cli/`** — Thin CLI handlers (Typer commands). Parses args, calls core, formats output with Rich. No business logic.
- **`src/fcm/core/`** — All business logic. No CLI or Rich concerns. Callable as a library.
- **`src/fcm/models/`** — Pure dataclasses (`VMConfig`, `VMInstance`, `VMState`, `ImageSpec`).
- **`src/fcm/utils/`** — Shared helpers (subprocess, filesystem, console wrappers).
- **`src/fcm/assets/`** — Committed YAML configs (`defaults.yaml`, `images.yaml`, `kernel.yaml`).

### Key Core Modules

| Module | Responsibility |
|--------|---------------|
| `core/vm_manager.py` | JSON-based VM state persistence under `~/.cache/firecracker-manager/vms/` |
| `core/firecracker.py` | Spawn Firecracker process + Unix socket API client |
| `core/network.py` | Bridge (`fc-br0`), TAP device, NAT, IP pool allocation |
| `core/config_gen.py` | Generate Firecracker JSON config from `VMConfig` |
| `core/config.py` | Load YAML → dataclasses, config hierarchy |
| `core/image.py` | Download, verify, convert root filesystem images |
| `core/kernel.py` | Clone and build kernels from source |
| `core/binary_manager.py` | Download versioned Firecracker releases |

### Config Hierarchy

CLI flags > YAML config > `assets/defaults.yaml` built-in defaults

### Single Source of Truth

`pyproject.toml` defines the project name. All derived values (CLI entry point name, env var prefix `FCM_*`, cache directory `~/.cache/firecracker-manager/`, network device names) flow from that single definition via `src/fcm/constants.py`.

### Runtime Cache Layout

```
~/.cache/firecracker-manager/
├── bin/          # Firecracker/jailer binaries by version
├── kernels/      # Compiled vmlinux files
├── images/       # Root filesystem images (.ext4)
├── keys/         # SSH keypairs
├── host/         # Pre-init host state snapshot
└── vms/<name>/   # Per-VM: rootfs, config JSON, logs, PID, socket, cloud-init/
```

### VM Lifecycle Data Flow

1. `vm create` CLI handler → `core/network.py` (TAP setup) → `core/config_gen.py` (generate JSON) → `core/firecracker.py` (spawn process) → `core/vm_manager.py` (register state)
2. `vm delete` → kill process → teardown TAP → deregister state

### Cloud-init

Each VM gets a cloud-init ISO (meta-data, network-config, user-data) generated at create time. This injects SSH keys, hostname, and network config into the guest at first boot.

## Testing Notes

- Unit tests live in `tests/unit/` and are pure logic tests — no root access or KVM required.
- Tests cover CLI commands, core modules, models, and utils.
- There are no integration tests (those require system resources).
