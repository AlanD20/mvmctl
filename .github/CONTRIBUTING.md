# Contributing to mvmctl

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
git clone https://github.com/your-org/mvmctl
cd mvmctl

# Install all dependencies including dev tools
uv sync --group dev

# Verify the CLI is available
uv run mvm --help
```

This creates a `.venv/` directory and installs everything there. You don't need to activate it manually; `uv run` handles that.

## Project Structure

```
mvmctl/
├── src/mvmctl/
│   ├── api/          # Public Python API — orchestration entry points
│   │   ├── vm_operations.py, network_operations.py, host_operations.py
│   │   ├── image_operations.py, kernel_operations.py, binary_operations.py
│   │   ├── key_operations.py, console_operations.py, logs_operations.py
│   │   ├── cache_operations.py, config_operations.py, ssh_operations.py
│   │   ├── init_operations.py, volume_operations.py
│   │   └── inputs/   # Request schema factory
│   ├── cli/          # Typer command groups (thin — no business logic)
│   │   ├── vm.py, network.py, host.py, image.py, kernel.py, bin.py
│   │   ├── key.py, console.py, logs.py, cache.py, config.py
│   │   ├── ssh.py, init.py, volume.py
│   ├── core/         # Isolated domain logic (no cross-domain imports)
│   │   ├── vm/       # _controller.py, _service.py, _repository.py, _resolver.py, _firecracker.py
│   │   ├── network/  # _controller.py, _service.py, _repository.py, _resolver.py, _lease_service.py, _lease_resolver.py
│   │   ├── host/     # _controller.py, _service.py, _repository.py, _helper.py
│   │   ├── image/    # _controller.py, _service.py, _repository.py, _resolver.py
│   │   ├── kernel/   # _controller.py, _service.py, _repository.py, _resolver.py
│   │   ├── key/      # _controller.py, _service.py, _repository.py, _resolver.py
│   │   ├── binary/   # _controller.py, _service.py, _repository.py, _resolver.py
│   │   ├── cache/    # _service.py, _repository.py
│   │   ├── config/   # _service.py, _repository.py, _constraints.py
│   │   ├── console/  # _controller.py
│   │   ├── ssh/      # _service.py
│   │   ├── cloudinit/ # _provisioner.py, _manager.py
│   │   ├── logs/     # _controller.py, _service.py
│   │   ├── volume/   # _controller.py, _service.py, _repository.py, _resolver.py
│   │   └── _shared/  # Shared infra: _db.py (Database), _asset_manager.py, _iptables_tracker/, _guestfs/, etc.
│   ├── models/       # Pure @dataclass models (VMInstanceItem, NetworkItem, ImageSpec, FirecrackerConfig, etc.)
│   ├── utils/        # Shared helpers (fs.py, _system.py, http.py, network.py, crypto.py, template.py, yaml.py, etc.)
│   ├── assets/       # Bundled YAML configs (images.yaml, kernels.yaml) + JSON templates (firecracker.template.json, cloud-init.template.yaml)
│   └── services/     # Runtime subprocess services (console_relay, nocloud_server)
├── tests/
│   ├── unit/                # Unit tests — 118 files
│   ├── integration/         # Workflow tests — 18 files
│   ├── system/              # Full-stack tests — 19 files (KVM/root not required)
│   └── layer_compliance/    # Architecture constraint verification — 7 files
├── pyproject.toml
└── README.md
```

Three-tier architecture: **CLI → API → Core**. `cli/` stays thin (arg parsing + output). `api/` is the stable public interface and sole orchestrator of core modules. `core/` holds isolated domain logic in subdirectories. CLI modules call into `api/`, never `core/` directly.

## Running Tests

```bash
# Run all tests
uv run pytest tests/ -v

# Run a specific file
uv run pytest tests/integration/test_vm_lifecycle.py -v

# Run tests matching a name pattern
uv run pytest tests/ -v -k "test_create"

# Run with coverage
uv run pytest tests/ -v --cov-report=term-missing
```

Tests don't need root or KVM. Integration and system tests mock all subprocess calls — check their docstrings.

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

1. Find the right group file in `src/mvmctl/cli/` (e.g., `vm.py` for `mvm vm ...`).
2. Add a new Typer command function. Follow the existing pattern:
   ```python
   @app.command()
   def my_command(
       name: str = typer.Option(..., "--name", help="VM name"),
   ) -> None:
       """Short description shown in --help."""
       from mvmctl.api import VMOperation, VMCreateInput

       result = VMOperation.create(VMCreateInput(name=name, ...))
       console.print(result)
   ```
3. Put the actual business logic in a `core/{domain}/` module and orchestrate it through the corresponding `api/{domain}_operations.py`.
4. Add a test in `tests/integration/` or `tests/system/`.

For entirely new command groups, create a `src/mvmctl/cli/mygroup.py` Typer app and register it in
`src/mvmctl/main.py` in the `_COMMAND_SPECS` dict (following the `"vm": _LazyCommandSpec(...)` pattern).

## Adding a New Image Type

Image specifications are defined in YAML config files and loaded using the `ImageSpec` model
(`src/mvmctl/models/image.py`). Each entry describes where to download an image, its source
format, and how to convert it for use with Firecracker.

**Steps to add a new image:**

1. Open (or create) the images YAML config at `src/mvmctl/assets/images.yaml`.
2. Add an entry following the `ImageSpec` schema:

   ```yaml
   images:
     - id: debian-12
       name: "Debian 12 (Bookworm)"
       source: "https://example.com/debian-12-rootfs.tar.gz"
       format: tar-rootfs
       convert_to: ext4
       size_mib: 2048
       sha256: "abc123..."   # optional but recommended
   ```

3. Add tests in `tests/integration/` or `tests/system/` covering the new handler or any conversion logic.

## Adding a Test

Tests live in `tests/integration/` for workflow coverage and `tests/system/` for full-stack testing.

```python
# tests/integration/test_my_feature.py
import pytest
from mvmctl.api import VMOperation, VMCreateInput


def test_something_works() -> None:
    result = VMOperation.create(VMCreateInput(name="test-vm", ...))
    assert result.success


def test_something_fails_gracefully() -> None:
    with pytest.raises(ValueError, match="invalid input"):
        VMOperation.create(VMCreateInput(name="", ...))
```

Use `pytest.fixture` for shared setup. Keep unit tests fast; avoid `sleep()` or network calls.

## Development Guidelines

- **Tests must not require root, KVM, or a real network.** Mock all subprocess calls.
- **Coverage gate:** 80% branch coverage minimum. Dropping coverage will fail CI.
- **Architecture layers:** `cli/` → `api/` → `core/` — three-tier, no skipping layers. `api/` is the only layer that imports multiple core domains. Core domains are isolated — never import one domain from another. See [`AGENTS.md`](AGENTS.md) for the full architecture reference.
- **Lazy imports in `__init__.py`:** All package `__init__.py` files MUST use PEP 562 `__getattr__` lazy imports via `resolve_lazy()`. Eager imports at package level are forbidden — they cascade-load all submodules even when only one class is needed, adding ~230ms+ to CLI startup time.
  ```python
  from __future__ import annotations
  from mvmctl.utils._lazy_import import resolve_lazy

  __all__ = ["ExportedClass1", "ExportedClass2"]

  _LAZY_MAP: dict[str, tuple[str, str]] = {
      "ExportedClass1": ("module.path._submodule", "ExportedClass1"),
      ...
  }

  def __getattr__(name: str) -> object:
      return resolve_lazy(name, _LAZY_MAP, __name__)

  def __dir__() -> list[str]:
      return __all__
  ```
- **`from __future__ import annotations`:** Every Python file MUST include `from __future__ import annotations` as its first import (after the file docstring). This enables PEP 604 union syntax (`str | None` instead of `Optional[str]`) and defers annotation evaluation, improving startup time.
- **Controller convention:** `Controller` classes handle state management only — `start()`, `stop()`, `pause()`, `resume()`, `snapshot()`. They do NOT have `create()`, `remove()`, `list()`, or `inspect()` methods. Creation/removal belongs in the `Service` layer, cross-domain orchestration in the `API`/`Operation` layer.
- **No hardcoded defaults** — use the `OVERRIDABLE_DEFAULTS` dict in `constants.py`.
- **Strict mypy** — no `type: ignore` suppressions.
- One feature or fix per PR; write a clear description of *why*, not just *what*.

## Commit Conventions

This project follows [Conventional Commits](https://www.conventionalcommits.org/):

```
feat: add mvm vm pause command
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

All MVM environment variables use the `MVM_` prefix.

| Variable | Description | Default |
|---|---|---|
| `MVM_CACHE_DIR` | Override the cache directory for images, kernels, and VM state | `~/.cache/mvmctl` |
| `MVM_CONFIG_DIR` | Override the config directory | `~/.config/mvmctl` |
| `MVM_LOG_LEVEL` | Set log verbosity (`DEBUG`, `INFO`, `WARNING`, `ERROR`) | `WARNING` |
| `MVM_FIRECRACKER_BIN` | Path to the Firecracker binary | auto-detected from metadata |

When writing code that reads config or paths, always go through the settings module rather than reading env vars directly. That keeps everything in one place and makes testing easier.

## Build System

### Project Name (Single Source of Truth)

The project name is defined once in `pyproject.toml` under `[project] name`. Changing it there automatically propagates to:

- The CLI entry point name (via `[project.scripts]`)
- The environment variable prefix (`MVM_` — derived from `constants.py` which reads from `pyproject.toml`)
- The cache directory name (`~/.cache/mvmctl/`)
- The network device prefixes (`mvm-br0`, `fc-<name>-0`)

To rename the project, update `pyproject.toml` and rebuild with the build script (`scripts/build_services.py`). No grep-and-replace needed.

### Working with libguestfs (direct cloud-init mode)

If you're developing or testing the **direct cloud-init injection** feature (\`--cloud-init-mode inject\`),
you need the `guestfs` Python bindings available in your uv virtual environment.
Since `guestfs` is not on PyPI and must come from your system package manager, use the
Taskfile helper to symlink the system bindings into the uv venv:

**1. Install system libguestfs packages:**

```bash
# Debian/Ubuntu
sudo apt-get install libguestfs0 libguestfs-tools python3-libguestfs supermin

# Arch Linux
sudo pacman -S libguestfs python-libguestfs supermin
```

**2. Link guestfs into the uv venv:**

```bash
task link-guestfs
```

This creates symlinks from the system Python site-packages into the uv virtual
environment, making `import guestfs` work under `uv run`.

**3. Verify the link:**

```bash
task test-guestfs
# ✅ libguestfs is active in .venv
```

**4. Unlink when done (optional):**

```bash
task unlink-guestfs
```

> **Note:** This linking approach is only needed for local development. When building
> a standalone binary with Nuitka, use `--include-package=guestfs` to bundle the
> system bindings directly (see "Build with Guestfs Support" below).

### Building the Standalone Binary

The project ships a self-contained single-file binary built primarily with Nuitka for high performance. The binary bundles all runtime dependencies and requires no Python installation on the target machine.

```bash
git clone https://github.com/your-org/mvmctl
cd mvmctl
uv sync --group dev --group build
uv run --group build python -m nuitka --onefile --output-dir=dist --output-filename=mvm --include-package=mvmctl --include-data-dir=src/mvmctl/assets=mvmctl/assets --lto=yes --enable-plugin=anti-bloat src/mvmctl/main.py
./dist/mvm --version
./dist/mvm --help
```

PyInstaller can also be used for faster compilation during development. The GitHub Actions `release.yml` workflow runs Nuitka automatically on every tagged release and uploads the binary as a release asset.

## Privileged Operations

Networking operations (bridge/TAP setup, iptables, sysctl) require elevated privileges.
Rather than requiring `sudo` for every command, mvm uses a privilege delegation model:

All privileged commands must be executed within the `mvm` group context. Use the `sg mvm -c` pattern:
```bash
sg mvm -c 'mvm host init'
sg mvm -c 'mvm network create --name mynet'
```

1. **`sudo mvm host init`** creates a system group (`mvm`) and a sudoers drop-in file
   (`/etc/sudoers.d/mvm`) that grants members of the `mvm` group passwordless access
   to a specific set of binaries defined in `mvmctl.constants.PRIVILEGED_BINARIES`.

2. **`PRIVILEGED_BINARIES`** is the single source of truth for which system binaries
   the sudoers file grants access to:
   - `/usr/sbin/ip` (iproute2)
   - `/usr/sbin/iptables`, `/usr/sbin/iptables-save`
   - `/usr/sbin/sysctl` (procps)

3. **`HostPrivilegeHelper.check_privileges(binary, description)`** (in `src/mvmctl/core/host/_helper.py`)
   verifies that the current user can invoke a given binary with elevated privileges. It checks:
   - The binary exists on the host.
   - The user is either root or a member of the `mvm` group.
   Raises `PrivilegeError` (a subclass of `HostError`) on failure. Called from `api/host_operations.py`.

4. **Sudoers generation** is handled by `HostService.validate_sudoers_binaries()`,
   `HostService._generate_sudoers_content()`, and `HostService.write_sudoers()` in
   `src/mvmctl/core/host/_service.py`. The generated file is validated with
   `visudo -c` before being written to the final location.

5. **Cleanup**: `mvm host reset` removes the sudoers drop-in and the `mvm` group,
   fully reverting the privilege setup. `mvm host clean` only tears down networking
   without touching the privilege model.

When adding a new binary that needs elevated privileges, add it to
`PRIVILEGED_BINARIES` in `constants.py` and update `HostService.validate_sudoers_binaries()`
in `src/mvmctl/core/host/_service.py` if the binary belongs to a specific package.

## Bumping the Version

The project version is defined in exactly one place: the `version` field under `[project]` in `pyproject.toml`. There is no separate version constant to update — `importlib.metadata` reads it at runtime, and `__version__` in `src/mvmctl/__init__.py` exists only as a fallback for editable installs.

To cut a release:

1. Edit `pyproject.toml` and update `version` (e.g., `"0.1.0"` to `"0.2.0"`).
2. Update the matching `__version__` in `src/mvmctl/__init__.py` to the same value.
3. Commit the change: `git commit -m "chore: bump version to 0.2.0"`.
4. Tag the commit: `git tag -a v0.2.0 -m "Release v0.2.0"`.
5. Push the tag: `git push origin v0.2.0`.

Pushing the tag triggers the `release.yml` GitHub Actions workflow, which builds binaries, publishes to PyPI, and creates a GitHub release automatically.

See `docs/RELEASE.md` for the full release process, including hotfix and yank procedures.

## Questions

Open an issue if something in this guide is unclear or out of date.
