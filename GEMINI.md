# mvmctl

## Project Overview
`mvmctl` (`mvm`) is a production-grade Python CLI application for managing the complete lifecycle of [Firecracker](https://firecracker-microvm.github.io/) microVMs on Linux. It handles everything from downloading official kernels and root filesystem images to setting up bridge networking, creating/destroying VMs, SSH access, log streaming, and cleanup.

**Tech Stack:**
- **Language:** Python 3.13+
- **CLI Framework:** Typer (with a custom lazy-loaded `click.Group` in `main.py`), Rich
- **Package Management:** `uv`
- **Testing & Linting:** `pytest`, `ruff`, `mypy`

## Architecture
The project strictly adheres to a layered architecture to separate concerns. Data flows sequentially: `User -> mvm -> main.py -> cli/*.py -> api/*.py -> core/*.py -> models/ + utils/`.

- **`cli/`**: Command definitions, argument parsing, and formatting output. No business logic.
- **`api/`**: Stable public Python API boundary. Performs privilege checks before delegating to `core/`.
- **`core/`**: All business logic, filesystem operations, subprocesses, and Firecracker interactions. Returns data or raises typed exceptions (`MVMError`).
- **`models/`**: Pure `@dataclass` objects containing domain data (e.g., `VMInstance`, `VMConfig`). No side effects.
- **`utils/`**: Shared helpers (console, process, fs, http, audit, validation) with no domain knowledge.

## Building and Running
The project uses `uv` for dependency management.

**Setup Development Environment:**
```bash
uv sync --group dev
```

**Run the CLI (Development):**
```bash
uv run mvm --help
```

**Building a Standalone Binary:**
```bash
pip install -e ".[dev]" pyinstaller
pyinstaller --onefile --name mvm src/mvmctl/main.py
# The output will be located at dist/mvm
```

## Testing and Quality Gates
All checks are enforced in CI and must pass before opening a PR.

```bash
# Tests (Must mock all subprocess calls; no root/KVM/real network required)
uv run pytest tests/ -x -q

# Linting & Formatting
uv run ruff check src/
uv run ruff format --check src/

# Type Checking (Strict Mode - no `type: ignore` allowed)
uv run mypy src/
```
*Note: A minimum of 80% branch coverage is strictly enforced.*

## Development Conventions
- **Defaults:** Never hardcode defaults in function parameters. Fallback defaults reside in `constants.py` with a `FALLBACK_` prefix. User-facing defaults are resolved from `~/.config/mvmctl/config.json` or `MVM_*` environment variables.
- **Privilege Model:** `sudo mvm host init` is run once to set up the host (mvm group, sudoers). Normal commands run rootless and validate privileges via the `mvm` group.
- **Asset ID System:** Downloaded or imported assets (images, kernels) use a full 64-character SHA256 hash as their persistent ID. The CLI displays and accepts the first 6 characters as a prefix.
- **Error Handling:** Avoid bare `except:` blocks. Catch specific domain exceptions derived from `exceptions.py`.
