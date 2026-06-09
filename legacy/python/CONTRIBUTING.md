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
│   ├── unit/                # Unit tests — 119 files
│   ├── integration/         # Workflow tests — 18 files
│   ├── system/              # Full-stack tests — 24 files (KVM/root not required)
│   │   ├── bin/, cache/, cli/, config/, console/
│   │   ├── cp/, full_journeys/, host/, images/, init/
│   │   ├── invariants/, kernel/, keys/, logs/, network/
│   │   ├── ssh/, vm/ (lifecycle, nested_virt, snapshot_load),
│   │   ├── volume/ (volume, volume_hotplug), zzz_destructive/
│   └── layer_compliance/    # Architecture constraint verification — 8 files
├── pyproject.toml
└── README.md
```

Three-tier architecture: **CLI → API → Core**. `cli/` stays thin (arg parsing + output). `api/` is the stable public interface and sole orchestrator of core modules. `core/` holds isolated domain logic in subdirectories. CLI modules call into `api/`, never `core/` directly.

## Running Tests

```bash
# Run all tests (all three levels)
uv run scripts/run_tests.py

# Run a specific file
uv run scripts/run_tests.py --test tests/integration/test_vm_lifecycle.py

# Run tests matching a name pattern
uv run scripts/run_tests.py --pytest-extra "-v -k test_create"

# Run with coverage via the script
uv run scripts/run_tests.py --pytest-extra "--cov=src/mvmctl --cov-report=term-missing"
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
        name: str = typer.Argument(..., help="VM name"),
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

Image types are defined in `src/mvmctl/assets/images.yaml` and describe how to discover,
download, and verify cloud images for use with Firecracker. Each entry is a YAML mapping
under the `image_types:` list key. The internal `ImageSpec` model (`src/mvmctl/models/image.py`)
is constructed **from** these config entries and represents a resolved, version-pinned image
ready for download — it is **not** a 1:1 serialization of the YAML format.

### images.yaml Format

```yaml
image_types:
  - type: <string>              # Unique type key (e.g. "ubuntu", "debian", "alpine")
    name: <string>              # Human-readable display name
    resolver: <string | null>   # Version resolver strategy (see below)
    versions_url: <string | null>  # URL template for listing available versions
    download_url: <string>      # URL template for downloading a specific version
    sha256_url: <string | null> # URL template for the SHA256 checksum file
    format: <string>            # Source image format: tar-rootfs, qcow2, vhd, squashfs
    options:                    # Resolver-specific configuration (see below)
      ...
```

### Resolver Types

| Resolver | Description | Example |
|----------|-------------|---------|
| `http-dir` | Scrapes an Apache/nginx directory listing to discover versions. Requires `versions_url` pointing to a directory index. | ubuntu, debian, alpine |
| `firecracker-s3` | Uses S3 XML `ListBucket` requests to discover versions. Requires `list_url_template`. | firecracker CI images |
| `null` | No version discovery — a single download URL with a pinned version. Used for rolling-release distros. | archlinux |

### Options Sub-fields

The `options:` block contains resolver-specific configuration:

| Option | Type | Purpose | Used by |
|--------|------|---------|---------|
| `codename_mapping` | `map[str, str]` | Maps release codenames to version numbers (e.g. `noble → "24.04"`) | ubuntu, debian |
| `arch_mapping` | `map[str, str]` | Maps local architecture names to upstream conventions (e.g. `x86_64 → amd64`) | all http-dir resolvers |
| `skip_patterns` | `list[str]` | Directory listing entries to skip during version discovery | ubuntu, debian, alpine |
| `version_prefix` | `str` | Prefix to add to discovered version strings (e.g. `"v"` for Alpine) | alpine |
| `file_discovery` | `map` | For providers where versions are at the file level rather than subdirectories. Sub-fields: `enabled`, `pattern`, `suffix`, `sha256_suffix` | alpine |
| `s3_version_pattern` | `str` | Regex to extract version from S3 object keys | firecracker-s3 |
| `convert_to` | `str` | Target filesystem format to convert the downloaded image to (e.g. `ext4`) | archlinux (qcow2 → ext4) |
| `version_name_template` | `str` | Template for the display name (uses `{codename}`, `{version}`, `{arch}` placeholders) | all resolvers |

### Examples

**http-dir resolver (Ubuntu):**

```yaml
image_types:
  - type: ubuntu
    name: "Ubuntu LTS"
    resolver: http-dir
    version_name_template: "Ubuntu {codename} ({version}) LTS"
    versions_url: "https://cloud-images.ubuntu.com/releases/"
    download_url: "https://cloud-images.ubuntu.com/{codename}/current/{codename}-server-cloudimg-{arch}-root.tar.xz"
    sha256_url: "https://cloud-images.ubuntu.com/{codename}/current/SHA256SUMS"
    format: tar-rootfs
    options:
      skip_patterns:
        - streams
        - releases
        - server
        - Parent Directory
      codename_mapping:
        noble: "24.04"
        jammy: "22.04"
        focal: "20.04"
      arch_mapping:
        x86_64: "amd64"
        aarch64: "arm64"
```

**null resolver (single-source / rolling release):**

```yaml
image_types:
  - type: archlinux
    name: "Arch Linux"
    resolver: null
    version_name_template: "Arch Linux"
    versions_url: null
    download_url: "https://geo.mirror.pkgbuild.com/images/latest/Arch-Linux-{arch}-cloudimg.qcow2"
    sha256_url: "https://geo.mirror.pkgbuild.com/images/latest/Arch-Linux-{arch}-cloudimg.qcow2.SHA256"
    format: qcow2
    options:
      convert_to: ext4
```

### Adding Kernels (kernels.yaml)

Kernels are defined in `src/mvmctl/assets/kernels.yaml` using a **different** top-level structure:

```yaml
kernel-official:     # Kernel set identifier
  type: official     # "official" (build from kernel.org source) or "firecracker" (prebuilt binary)
  version: "6.19.9"
  resolver: http-dir
  versions_url: "https://cdn.kernel.org/pub/linux/kernel/"
  source: "https://cdn.kernel.org/pub/linux/kernel/v{series}.x/linux-{version}.tar.xz"
  sha256_url: "..."
  config_url_template: "https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/resources/guest_configs/microvm-kernel-ci-{arch}-6.1.config"
  output_name: vmlinux-official
  options:
    version_discoveries: ["v6.x", "v7.x"]
    file_pattern: "linux-"
    file_suffix: ".tar.xz"
```

Key differences from `images.yaml`:
- Top-level keys are **named kernel set identifiers** (`kernel-official:`, `kernel-firecracker:`) rather than a list.
- Each entry includes build configuration (`config_fragments`, `enabled_configs`, `disabled_configs`, `set_val_configs`).
- The `source:` field points to kernel source tarballs, not pre-built images.
- The `resolver:` can be `http-dir` (for kernel.org mirrors) or `firecracker-s3` (for Firecracker CI prebuilt kernels).

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
    uv run ruff check src/ && uv run scripts/run_tests.py
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

The project ships a self-contained single-file binary built with Nuitka for high performance. Use the build script:

```bash
python scripts/build_services.py                    # Build everything (default)
python scripts/build_services.py --services         # Build all service binaries only
python scripts/build_services.py --service <name>   # Build a specific service binary
./dist/mvm --version
./dist/mvm --help
```

The build script always uses the same release-quality settings (LTO, anti-bloat, deployment mode) regardless of which flags are passed — the flags only control **what** to build, not **how**.

The GitHub Actions `release.yml` workflow runs Nuitka automatically on every tagged release and uploads the binary as a release asset.

## Privileged Operations

Networking operations (bridge/TAP setup, iptables, sysctl) require elevated privileges.
Rather than requiring `sudo` for every command, mvm uses a privilege delegation model:

All privileged commands must be executed within the `mvm` group context. Use the `sg mvm -c` pattern:
```bash
sg mvm -c 'mvm host init'
sg mvm -c 'mvm network create mynet'
```

1. **`sudo mvm host init`** creates a system group (`mvm`) and a sudoers drop-in file
   (`/etc/sudoers.d/mvm`) that grants members of the `mvm` group passwordless access
   to a specific set of binaries defined in `mvmctl.constants.PRIVILEGED_BINARIES`.

2. **`PRIVILEGED_BINARIES`** is the single source of truth for which system binaries
   the sudoers file grants access to:
   - `/usr/sbin/ip` (iproute2)
   - `/usr/sbin/iptables`, `/usr/sbin/iptables-save`
   - `/usr/sbin/nft` (nftables)
   - `/usr/sbin/sysctl` (procps)
   - `/usr/sbin/modprobe` (kmod)

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
