# Release Process

This document covers how to release a new version of mvmctl.

## Prerequisites

Before releasing, ensure the following are available on your workstation:

- **Python 3.13+** — required for local development and building
- **Linux (KVM-capable host)** — required to run integration tests and to produce a valid binary
- **git** — for tagging and pushing
- **uv** — for dependency management and running tools (`pip install uv` or see [uv docs](https://docs.astral.sh/uv/))

## Bumping the Version

The version is defined in one place: the `version` field under `[project]` in `pyproject.toml`. Update it there, and also update the `__version__` fallback in `src/mvmctl/__init__.py` to match.

### Manual Version Bump

Manually update these files:

1. `pyproject.toml` — update `version`
2. `src/mvmctl/__init__.py` — update `__version__`
3. `docs/mvm.1` — update version in `.TH` line
4. `CHANGELOG.md` — add new version section

This project uses **semantic versioning** (MAJOR.MINOR.PATCH):

- **MAJOR** — increment when you make incompatible API or CLI changes (e.g., removing a command, renaming a flag without a deprecation alias, changing config file format in a breaking way).
- **MINOR** — increment when you add new functionality in a backward-compatible manner (e.g., new commands, new flags, new config keys with defaults).
- **PATCH** — increment when you make backward-compatible bug fixes (e.g., fixing a crash, correcting wrong behavior, documentation fixes that ship with the binary).

Example: going from `0.3.1` to `0.4.0` means new features were added; going to `0.3.2` means only bugs were fixed.

## Building Locally

While the release workflow automates the binary build on GitHub Actions, you can build both the Python package and the standalone binary locally for testing.

### 1. Building the Python Wheel with uv

This produces a standard `.whl` and `.tar.gz` in the `dist/` folder:

```bash
uv build
```

### 2. Building the Standalone Binary with uv

You can build a standalone executable using either **PyInstaller** or **Nuitka**.

#### Option A: PyInstaller (Bundled Byte-code)

PyInstaller produces a single-file executable by bundling the Python interpreter and byte-code. This is fast to build but has a slight decompression overhead on startup.

```bash
uv run --group build pyinstaller --onefile --name mvm --collect-all mvmctl src/mvmctl/main.py
# The output will be located at dist/mvm
```

#### Option B: Nuitka (Compiled C++)

Nuitka translates the Python code into C++ and compiles it into a machine-code binary. This results in much faster startup and overall execution, though the build time is significantly longer.

```bash
uv run --group build python -m nuitka --onefile --output-dir=dist --output-filename=mvm-nuitka --include-package=mvmctl --include-data-dir=src/mvmctl/assets=mvmctl/assets src/mvmctl/main.py
# The output will be located at dist/mvm-nuitka
```

### 3. Comparing Performance

To compare the startup and execution speed of both binaries, use the `time` command:

```bash
# Compare help output speed
time ./dist/mvm --help
time ./dist/mvm-nuitka --help

# Compare version output speed
time ./dist/mvm --version
time ./dist/mvm-nuitka --version
```

Look at the `real` time to see the total elapsed time for each. Nuitka should typically be faster due to the lack of a decompression step.

### 4. Tagging and Pushing

After committing the version bump:

```bash
# Create an annotated tag
git tag -a v1.2.3 -m "Release v1.2.3"

# Push the tag to the remote (this triggers the release workflow)
git push origin v1.2.3
```

Pushing a tag that matches `v*` triggers the `release.yml` GitHub Actions workflow. Do not push the tag until the version bump commit is on `main` and CI is green.

> **Test gate**: The `ci.yml` workflow runs `pytest` with a 79% coverage minimum on every push and pull request to `main`. If tests fail, the CI run is red and the tag must not be pushed — the release workflow does not re-run tests, so a red `main` means a broken binary may be released. Always verify CI is green on the version-bump commit before tagging.

## What the Release Workflow Does Automatically

Once the tag is pushed, `release.yml` runs without any manual intervention:

1. **Binary builds** — Nuitka builds a standalone `mvm` binary on the runner:
   - `ubuntu-24.04` (glibc 2.39) — uploaded as artifact `mvm-linux-ubuntu-24.04`, attached to release as `mvm`

2. **GitHub release creation** — a GitHub release is created for the tag with auto-generated release notes. The binary is attached as a release asset.

3. **Artifact upload** — all build artifacts are uploaded as GitHub Actions artifacts for debugging if needed.

> **Note:** PyPI publishing is **not** automated by the release workflow. To publish to PyPI, follow the manual steps in the [Yanking a Bad Release](#yanking-a-bad-release) section or run `uv build && twine upload dist/*` after verifying the release.

You do not need to run Nuitka or `gh release` manually. Binary builds and GitHub release creation are automated.

## Verifying a Release

After the workflow completes (typically 5-10 minutes), verify the release is correct:

### Binary verification

```bash
# Download the binary for your platform
curl -L -o mvm https://github.com/<org>/mvmctl/releases/download/v1.2.3/mvm-linux-ubuntu-24.04
chmod +x mvm

# Check the version
./mvm --version
# Expected: mvm 1.2.3
```

### PyPI verification

```bash
# Install from PyPI
pip install mvmctl==1.2.3

# Check the version
mvm --version
# Expected: mvm 1.2.3
```

### pipx / uvx verification

```bash
# Install with pipx
pipx install mvmctl==1.2.3
mvm --version

# Or run directly with uvx (no install)
uvx mvmctl==1.2.3 mvm --version
```

### GitHub release page

Visit the releases page and confirm:
- The release notes are present and accurate.
- The `mvm` binary asset and `mvm.sha256` checksum file are attached.

## Issuing a Hotfix

If a bug is found in a released version and `main` has moved on with new features, issue a hotfix from the release tag:

```bash
# Branch from the release tag
git checkout -b hotfix/v1.2.4 v1.2.3

# Make the fix
# ...

# Bump the patch version in pyproject.toml and src/mvmctl/__init__.py
# e.g., 1.2.3 -> 1.2.4

# Commit, tag, and push
git commit -am "fix: description of the hotfix"
git tag -a v1.2.4 -m "Release v1.2.4 (hotfix)"
git push origin hotfix/v1.2.4
git push origin v1.2.4
```

Then open a PR to merge the hotfix branch back into `main` so the fix is not lost.

## Yanking a Bad Release

If a release is broken and should not be installed by anyone:

### Yank from PyPI

```bash
# Yank the specific version (requires PyPI credentials or token)
pip install twine
twine yank mvmctl 1.2.3
```

Yanking does not delete the release — it marks it so that `pip install mvmctl` will not pick it up, but `pip install mvmctl==1.2.3` still works for anyone who explicitly pins to it.

### Mark or delete the GitHub release

- To mark as pre-release (warns users but keeps the assets available):
  ```bash
  gh release edit v1.2.3 --prerelease
  ```

- To delete the release entirely (removes assets and release notes):
  ```bash
  gh release delete v1.2.3 --yes
  ```

- Optionally delete the tag as well:
  ```bash
  git tag -d v1.2.3
  git push origin :refs/tags/v1.2.3
  ```

After yanking, immediately publish a new patch release with the fix (see "Issuing a Hotfix" above).

## Man Page Installation

A man page is included at `docs/mvm.1`. Install it system-wide:

### Manual Installation

```bash
# Copy to system man page directory
sudo cp docs/mvm.1 /usr/local/share/man/man1/
sudo mandb  # Update man database

# View the man page
man mvm
```

### Package Installation (when packaging for distributions)

For distro packages (deb, rpm, etc.), install to:
- **Debian/Ubuntu**: `/usr/share/man/man1/mvm.1`
- **RHEL/CentOS/Fedora**: `/usr/share/man/man1/mvm.1`
- **Arch**: `/usr/share/man/man1/mvm.1`

## Distribution Package Builds

The CI builds distribution packages using GitHub Actions workflows. Refer to `.github/workflows/release.yml` for the exact build matrix.

### Local Package Building

To build packages locally (for testing):

#### Debian/Ubuntu
```bash
# Install build dependencies
sudo apt-get install debhelper build-essential

# Build the binary first
uv run --group build python -m nuitka --onefile --output-dir=dist --output-filename=mvm \
  --include-package=mvmctl --include-data-dir=src/mvmctl/assets=mvmctl/assets \
  --lto=yes src/mvmctl/main.py

# Build the .deb (using dh-compatible rules)
mkdir -p debian
cp -r .github/workflows/release/debian/* debian/ 2>/dev/null || true
dpkg-buildpackage -us -uc -b
# Package will be at ../mvmctl_*.deb
```

#### RPM (on Fedora/RHEL)
```bash
# Install build tools
sudo dnf install rpm-build rpmdevtools

# Set up build tree
rpmdev-setuptree

# Copy files
cp .github/workflows/release/mvmctl.spec ~/rpmbuild/SPECS/
cp dist/mvm ~/rpmbuild/SOURCES/
cp docs/mvm.1 ~/rpmbuild/SOURCES/

# Build
rpmbuild -bb ~/rpmbuild/SPECS/mvmctl.spec
# Package will be at ~/rpmbuild/RPMS/x86_64/*.rpm
```

## Appendix: Dynamic Import Handling in Compiled Binaries

mvmctl uses dynamic imports for optional dependencies to keep the core runtime lightweight:

| Module | Import Pattern | Nuitka Flag | PyInstaller Hook |
|--------|---------------|-------------|------------------|
| `guestfs` | `importlib.import_module("guestfs")` | `--include-package=guestfs` | `--hidden-import=guestfs` |

### Why Explicit Inclusion Is Required

Nuitka and PyInstaller perform static analysis to detect dependencies. Because mvmctl imports `guestfs` dynamically only when `--cloud-init-mode inject` is used, static analysis cannot detect this dependency. Without explicit flags:

- Nuitka: Module not included → `GuestfsNotAvailableError` at runtime
- PyInstaller: Module not included → `ModuleNotFoundError` at runtime

### Build Matrix

| Build Type | Command | Guestfs Support |
|------------|---------|-----------------|
| Minimal | `uv sync --group dev --group build` | No (nocloud-net only) |
| Full (with guestfs) | Install `python3-libguestfs` via distro package manager, then `uv sync --group dev --group build` | Yes — `guestfs` is a system/distro package only; there is no `--group guestfs` uv group in this repo |

> **Note:** Even with guestfs included in the binary, the host system must still have libguestfs0 and supermin installed. The Python bindings are bundled; the C library and appliance are system dependencies.
