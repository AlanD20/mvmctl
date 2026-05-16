# Release Process

This document covers how to release a new version of mvmctl.

## Prerequisites

- **Python 3.13+** — local development and building
- **Linux (KVM-capable host)** — integration tests and valid binary
- **git** — tagging and pushing
- **uv** — dependency management (see [uv docs](https://docs.astral.sh/uv/))

## Bumping the Version

Use the automated bump script:

```bash
./bump-version.py 0.1.0              # Bump to 0.1.0
./bump-version.py 0.1.0 --dry-run    # Preview changes only
./bump-version.py 0.1.0 --commit     # Bump and auto-commit
./bump-version.py 0.1.0 --aur        # Also update AUR PKGBUILD checksums
```

Files updated: `pyproject.toml`, `src/mvmctl/__init__.py`, `packaging/PKGBUILD`, `packaging/mvmctl.spec`, `packaging/debian/changelog`, `docs/mvm.1`, `CHANGELOG.md`

This project uses **semantic versioning** (MAJOR.MINOR.PATCH):
- **MAJOR** — incompatible API or CLI changes
- **MINOR** — new functionality, backward-compatible
- **PATCH** — backward-compatible bug fixes

## Build Verification

Before tagging, verify the build locally:

```bash
uv sync --group dev --group build
python scripts/build_services.py            # Build everything (release mode, default)
python scripts/build_services.py --mvm     # Main binary only
```

Output: `dist/mvm` (main binary) and `dist/services/mvm-services` (multidist services binary).

Verify the binary:

```bash
./dist/mvm --version
./dist/mvm --help
```

## Tag and Push

```bash
# Create an annotated tag
git tag -a v0.1.0 -m "Release v0.1.0"

# Push the tag (triggers release.yml workflow)
git push origin v0.1.0
```

> **Test gate**: The `ci.yml` workflow runs `pytest` with an 80% coverage minimum on every push and PR to `main`. Always verify CI is green on the version-bump commit before tagging. Pushing a tag that matches `v*` triggers `release.yml`.

## What the CI Pipeline Does (`.github/workflows/release.yml`)

Triggered on tag push `v*.*.*`:

| Job | Description |
|-----|-------------|
| **test** | Runs all tests with 80% coverage gate |
| **build** | Builds Nuitka binary on `ubuntu-24.04`, generates SHA256, uploads as artifact |
| **build-deb** | Builds `.deb` package via `dpkg-buildpackage` (needs build) |
| **build-rpm** | Builds `.rpm` in Fedora container (needs build) |
| **build-arch** | Creates Arch PKGBUILD files (needs build) |
| **publish-pypi** | Publishes wheel + sdist to PyPI via trusted publishing (needs all prior) |
| **upload-packages-to-release** | Uploads all packages to the GitHub release |

## Packaging Formats

| Format | Location | Dependencies |
|--------|----------|-------------|
| `.deb` | `packaging/debian/` | `iproute2, iptables, qemu-utils, libguestfs-tools | guestfs-tools, xorriso | genisoimage` |
| `.rpm` | `packaging/mvmctl.spec` | `iproute, iptables, qemu-img, libguestfs, xorriso, openssh-clients` |
| Arch Linux | `packaging/PKGBUILD` | `iproute2, iptables, qemu, libguestfs, xorriso, openssh` |
| PyPI | via trusted publishing | Python package (wheel + sdist) |

## Verifying a Release

After the workflow completes (typically 5-10 minutes):

```bash
# Download the binary
curl -L -o mvm https://github.com/AlanD20/mvmctl/releases/download/v0.1.0/mvm
chmod +x mvm

# Verify checksum
sha256sum -c mvm.sha256

# Check version
./mvm --version
# Expected: mvm 0.1.0
```

### PyPI verification

```bash
uv tool install mvmctl==0.1.0
mvm --version
# Expected: mvm 0.1.0
```

### GitHub release page

Visit the releases page and confirm:
- Release notes are present and accurate
- The `mvm` binary and `mvm.sha256` checksum are attached

## Issuing a Hotfix

```bash
# Branch from the release tag
git checkout -b hotfix/v0.1.1 v0.1.0

# Make the fix, then bump version
./bump-version.py 0.1.1 --commit

# Tag and push
git push origin hotfix/v0.1.1
git tag -a v0.1.1 -m "Release v0.1.1 (hotfix)"
git push origin v0.1.1
```

Then open a PR to merge the hotfix branch back into `main`.

## Yanking a Bad Release

### Yank from PyPI

```bash
uv tool install twine
twine yank mvmctl 0.1.0
```

### Mark or delete the GitHub release

```bash
# Mark as pre-release
gh release edit v0.1.0 --prerelease

# Or delete entirely
gh release delete v0.1.0 --yes
git tag -d v0.1.0
git push origin :refs/tags/v0.1.0
```

After yanking, immediately publish a new patch release with the fix.

## Man Page Installation

```bash
sudo cp docs/mvm.1 /usr/local/share/man/man1/
sudo mandb
man mvm
```

## Appendix: Dynamic Import Handling

Nuitka performs static analysis to detect dependencies. Modules using dynamic registries (e.g., `passlib`) or runtime lookups (e.g., `rich._unicode_data`, `jinja2.tests`) must be force-included:

| Module | Nuitka Flag |
|--------|-------------|
| `passlib.handlers.bcrypt` | `--include-module=passlib.handlers.bcrypt` |
| `passlib.handlers.sha2_crypt` | `--include-module=passlib.handlers.sha2_crypt` |
| `rich._unicode_data` | `--include-package=rich._unicode_data` |
| `jinja2.tests` | `--include-module=jinja2.tests` |

These are already configured in `scripts/build_services.py`.
