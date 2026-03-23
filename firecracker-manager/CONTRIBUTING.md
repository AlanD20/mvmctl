# Contributing to firecracker-manager

Thanks for wanting to contribute. This guide covers everything you need to get set up and productive.

## Prerequisites

- **Python 3.13+** — check with `python3 --version`
- **uv** — the package manager used for this project. Install with:
  ```bash
  curl -LsSf https://astral.sh/uv/install.sh | sh
  ```
- **Linux** — the project targets Linux with KVM. Most development and testing works on any modern Linux distro.
- **git**

## Development Setup

```bash
git clone https://github.com/your-org/firecracker-manager
cd firecracker-manager

# Install all dependencies including dev tools
uv sync --group dev

# Verify the CLI is available
uv run fcm --help
```

This creates a `.venv/` directory and installs everything there. You don't need to activate it manually; `uv run` handles that.

## Project Structure

```
firecracker-manager/
├── src/fcm/
│   ├── api/          # Public Python API (vms.py, host.py, assets.py, network.py, keys.py)
│   ├── cli/          # Typer command groups (vm.py, network.py, key.py, asset.py, host.py)
│   ├── core/         # Business logic (vm_lifecycle.py, network_manager.py, etc.)
│   ├── models/       # Dataclass models (VMInstance, VMConfig, ImageSpec)
│   └── utils/        # Shared helpers (fs.py, console.py, process.py)
├── tests/
│   ├── unit/         # Pure unit tests (no root, no KVM)
│   └── integration/  # Tests that need system resources
├── pyproject.toml
└── README.md
```

Three tiers: `cli/` stays thin (arg parsing + output). `api/` is the stable public interface. `core/` holds business logic. CLI modules call into `api/`, not `core/` directly.

## Running Tests

```bash
# Run all tests
uv run pytest tests/ -v

# Run a specific file
uv run pytest tests/unit/test_vm_manager.py -v

# Run tests matching a name pattern
uv run pytest tests/ -v -k "test_create"

# Run with coverage
uv run pytest tests/ --cov=src/fcm --cov-report=term-missing
```

Unit tests don't need root or KVM. Integration tests might — check their docstrings.

## Code Style

This project uses **ruff** for linting and formatting, and **mypy** for type checking.

```bash
# Check for lint issues
uv run ruff check src/

# Auto-fix what ruff can fix
uv run ruff check src/ --fix

# Format code
uv run ruff format src/

# Type check
uv run mypy src/
```

All code should pass ruff with no errors. mypy is configured with `--strict`; new code should aim for full type annotations.

## Adding a New Command

1. Find the right group file in `src/fcm/cli/` (e.g., `vm.py` for `fcm vm ...`).
2. Add a new Typer command function. Follow the existing pattern:
   ```python
   @app.command()
   def my_command(
       name: str = typer.Option(..., "--name", help="VM name"),
   ) -> None:
       """Short description shown in --help."""
       manager = get_vm_manager()
       result = manager.do_thing(name)
       console.print(result)
   ```
3. Put the actual logic in the corresponding `core/` module.
4. Add a test in `tests/unit/`.

For entirely new command groups, create both `src/fcm/cli/mygroup.py` and register the Typer app in `src/fcm/cli/__init__.py` or `main.py`.

## Adding a Test

Tests live in `tests/unit/` for pure logic and `tests/integration/` for anything touching the filesystem or system calls.

```python
# tests/unit/test_my_feature.py
import pytest
from fcm.core.my_module import MyClass


def test_something_works() -> None:
    obj = MyClass(config={"key": "value"})
    result = obj.do_thing()
    assert result == expected_value


def test_something_fails_gracefully() -> None:
    obj = MyClass(config={})
    with pytest.raises(ValueError, match="config key missing"):
        obj.do_thing()
```

Use `pytest.fixture` for shared setup. Keep unit tests fast; avoid `sleep()` or network calls.

## Commit Conventions

This project follows [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add fcm vm pause command
fix: handle missing kernel path gracefully
test: add unit tests for image converter
docs: update quick start in README
chore: bump ruff to 0.4.0
refactor: extract network helpers to utils/networking.py
```

Keep the subject line under 72 characters. Add a body if the change needs explanation.

## Pull Request Process

1. Fork the repo and create a branch from `main`:
   ```bash
   git checkout -b feat/my-feature
   ```
2. Make your changes. Keep commits focused.
3. Run tests and linting:
   ```bash
   uv run ruff check src/ && uv run pytest tests/ -v
   ```
4. Push your branch and open a PR against `main`.
5. Fill in the PR description with what changed and why.
6. A maintainer will review and merge once tests pass.

Don't force-push to a PR branch after review starts. If you need to rebase, coordinate with the reviewer.

## Environment Variables

All FCM environment variables use the `FCM_` prefix.

| Variable | Description | Default |
|---|---|---|
| `FCM_CACHE_DIR` | Override the cache directory for images, kernels, and VM state | `~/.cache/firecracker-manager` |
| `FCM_CONFIG_FILE` | Override the config file path | `~/.config/fcm/config.yaml` |
| `FCM_LOG_LEVEL` | Set log verbosity (`DEBUG`, `INFO`, `WARNING`, `ERROR`) | `INFO` |
| `FCM_FIRECRACKER_BIN` | Path to the Firecracker binary | auto-detected from `$PATH` |

When writing code that reads config or paths, always go through the settings module rather than reading env vars directly. That keeps everything in one place and makes testing easier.

## Build System

### Project Name (Single Source of Truth)

The project name is defined once in `pyproject.toml` under `[project] name`. Changing it there automatically propagates to:

- The CLI entry point name (via `[project.scripts]`)
- The environment variable prefix (`FCM_` — derived from `constants.py` which reads from `pyproject.toml`)
- The cache directory name (`~/.cache/firecracker-manager/`)
- The network device prefixes (`fcm-br0`, `fc-<name>-0`)

To rename the project, update `pyproject.toml` and re-run the PyInstaller command with `--name <new-name>`. No grep-and-replace needed.

### Building the Standalone Binary

The project ships a self-contained single-file binary built with PyInstaller. The binary bundles all runtime dependencies and requires no Python installation on the target machine.

```bash
git clone https://github.com/your-org/firecracker-manager
cd firecracker-manager

pip install -e ".[dev]" pyinstaller

pyinstaller --onefile --name fcm src/fcm/main.py

./dist/fcm --version
./dist/fcm --help
```

The GitHub Actions `release.yml` workflow runs this automatically on every tagged release and uploads the binary as a release asset. Two binaries are produced — one built on `ubuntu-22.04` and one on `ubuntu-24.04` — because glibc version differences mean a binary from 24.04 will not run on 22.04.

## Privileged Operations

Networking operations (bridge/TAP setup, iptables, sysctl) require elevated privileges.
Rather than requiring `sudo` for every command, fcm uses a privilege delegation model:

1. **`sudo fcm host init`** creates a system group (`fcm`) and a sudoers drop-in file
   (`/etc/sudoers.d/fcm`) that grants members of the `fcm` group passwordless access
   to a specific set of binaries defined in `fcm.constants.PRIVILEGED_BINARIES`.

2. **`PRIVILEGED_BINARIES`** is the single source of truth for which system binaries
   the sudoers file grants access to:
   - `/usr/sbin/ip` (iproute2)
   - `/usr/sbin/iptables`, `/usr/sbin/iptables-restore`, `/usr/sbin/iptables-save`
   - `/usr/sbin/sysctl` (procps)

3. **`check_privileges(binary)`** (in `fcm.api.host`) verifies that the current user
   can invoke a given binary with elevated privileges. It checks:
   - The binary exists on the host.
   - The user is either root or a member of the `fcm` group.
   Raises `PrivilegeError` (a subclass of `HostError`) on failure.

4. **Sudoers generation** is handled by `_generate_sudoers_content()` and
   `_write_sudoers()` in `core/host.py`. The generated file is validated with
   `visudo -c` before being written to the final location.

5. **Cleanup**: `fcm host reset` removes the sudoers drop-in and the `fcm` group,
   fully reverting the privilege setup. `fcm host clean` only tears down networking
   without touching the privilege model.

When adding a new binary that needs elevated privileges, add it to
`PRIVILEGED_BINARIES` in `constants.py` and update `_validate_sudoers_binaries()`
in `core/host.py` if the binary belongs to a specific package.

## Bumping the Version

The project version is defined in exactly one place: the `version` field under `[project]` in `pyproject.toml`. There is no separate version constant to update — `importlib.metadata` reads it at runtime, and `__version__` in `src/fcm/__init__.py` exists only as a fallback for editable installs.

To cut a release:

1. Edit `pyproject.toml` and update `version` (e.g., `"0.1.0"` to `"0.2.0"`).
2. Update the matching `__version__` in `src/fcm/__init__.py` to the same value.
3. Commit the change: `git commit -m "chore: bump version to 0.2.0"`.
4. Tag the commit: `git tag -a v0.2.0 -m "Release v0.2.0"`.
5. Push the tag: `git push origin v0.2.0`.

Pushing the tag triggers the `release.yml` GitHub Actions workflow, which builds binaries, publishes to PyPI, and creates a GitHub release automatically.

See `docs/RELEASE.md` for the full release process, including hotfix and yank procedures.

## Questions

Open an issue if something in this guide is unclear or out of date.
