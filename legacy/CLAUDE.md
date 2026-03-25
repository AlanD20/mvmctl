# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

This repository contains a **Firecracker microVM management system** with two components:

1. **`firecracker-manager/`** — A production-grade Python CLI (`mvm`) for managing Firecracker microVMs. This is the primary component.
2. **`single-vm/` and `multi-vm/`** — Legacy bash-based scripts for simpler use cases.

## Development (firecracker-manager)

All commands should be run from the `firecracker-manager/` directory. The project uses `uv` for package management.

```bash
cd firecracker-manager
uv sync --group dev       # Install all dependencies including dev tools
```

### Testing

```bash
uv run pytest tests/ -v                                              # All tests
uv run pytest tests/unit/test_vm_manager.py -v                      # Single test file
uv run pytest tests/ --cov=src/mvmctl --cov-fail-under=79             # With coverage (79% min required)
```

### Linting & Type Checking

```bash
uv run ruff check src/         # Lint
uv run ruff format src/        # Format
uv run mypy src/               # Type checking (strict mode)
```

### Building the Binary

```bash
pip install -e ".[dev]" pyinstaller
pyinstaller --onefile --name mvm src/mvmctl/main.py
# Output: dist/mvm
```

## Architecture

The `mvm` CLI follows a strict layered pattern:

```
User → cli/ → api/ → core/ → models/ + utils/
```

- **`cli/`** — Thin Typer command definitions; only arg parsing and output formatting. Each subcommand group has its own file (`vm.py`, `host.py`, `network.py`, etc.).
- **`api/`** — Stable public Python API; delegates to `core/`. Use this layer when writing new features.
- **`core/`** — All business logic, system operations, privilege checks, and Firecracker interaction.
- **`models/`** — Dataclasses for type-safe data (`VMInstance`, `VMConfig`, `ImageSpec`).
- **`utils/`** — Shared helpers: `console.py` (Rich output), `process.py` (subprocess), `fs.py` (filesystem), `http.py` (downloads), `audit.py` (audit logging).
- **`constants.py`** — Single source of truth for project identity. The project name here drives CLI name, env var prefix (`MVM_`), cache dirs, and device name prefixes.
- **`exceptions.py`** — Custom exception hierarchy (`HostError`, `PrivilegeError`, etc.).

## Key Design Concepts

**Privilege Model**: `sudo mvm host init` is run once to create the `mvm` group and write a sudoers drop-in. After that, no `sudo` is needed for regular VM operations.

**Configuration Layering** (lowest to highest priority):
1. Defaults in `constants.py`
2. User config: `~/.config/mvmctl/config.yaml`
3. Environment variables (`MVM_` prefix)
4. CLI flags

**Host State Snapshots**: `mvm host init` snapshots pre-change state so `mvm host reset` can perform a full rollback.

**System Requirements**: Linux with KVM, and system binaries `ip`, `iptables`, `mkisofs`/`genisoimage`, `qemu-img` must be present.

## CI/CD

GitHub Actions pipelines are in `.github/workflows/`:
- `ci.yml` — Runs ruff lint, ruff format check, mypy strict, pytest with 79% coverage minimum (Python 3.13)
- `release.yml` — Builds and publishes `mvm` binary releases via PyInstaller
