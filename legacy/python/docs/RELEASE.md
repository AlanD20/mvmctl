# Release Process

This document covers how to release a new version of mvmctl from start to finish.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Step 1: Build and Verify Locally with Tests](#step-1-build-and-verify-locally-with-tests)
- [Step 2: Bump the Version](#step-2-bump-the-version)
- [Step 3: Build and Verify After Bump](#step-3-build-and-verify-after-bump)
- [Step 4: Commit and Push](#step-4-commit-and-push)
- [Step 5: Tag and Push the Tag](#step-5-tag-and-push-the-tag)
- [Step 6: CI Pipeline](#step-6-ci-pipeline)
- [Step 7: Verify the Release](#step-7-verify-the-release)
- [Step 8: Update Downstream Packages](#step-8-update-downstream-packages)
- [Step 9: Install the Man Page](#step-9-install-the-man-page)
- [Issuing a Hotfix](#issuing-a-hotfix)
- [Yanking a Bad Release](#yanking-a-bad-release)
- [Appendix: Dynamic Import Handling](#appendix-dynamic-import-handling)

---

## Prerequisites

- **Python 3.13+** — local development and building
- **Linux (KVM-capable host)** — tests and valid binary
- **git** — tagging and pushing
- **uv** — dependency management (see [uv docs](https://docs.astral.sh/uv/))

---

## Step 1: Build and Verify Locally with Tests

Before bumping any versions, verify the codebase is green:

```bash
uv sync --group dev
uv run ruff check src/
uv run mypy src/
uv run scripts/run_tests.py --ci        # All tests + 80% coverage gate
```

Then verify the binary compiles:

```bash
uv sync --group dev --group build
python scripts/build_services.py        # Build everything (default)
```

Output: `dist/mvm` (main binary) and `dist/services/mvm-services` (multidist services binary).

Verify the binary runs:
```bash
./dist/mvm --version
./dist/mvm --help
```

Do not proceed until linting, type checks, tests, and the build all pass.

---

## Step 2: Bump the Version

Run the automated bump script:

```bash
./bump-version.py 0.2.0              # Bump to 0.2.0
./bump-version.py 0.2.0 --dry-run    # Preview changes only
./bump-version.py 0.2.0 --commit     # Bump and auto-commit
```

Files updated: `pyproject.toml`, `src/mvmctl/__init__.py`, `packaging/PKGBUILD`,
`packaging/mvmctl.spec`, `packaging/debian/changelog`, `packaging/debian/control`,
`docs/mvm.1`, `CHANGELOG.md`

This project uses **semantic versioning** (MAJOR.MINOR.PATCH):
- **MAJOR** — incompatible API or CLI changes
- **MINOR** — new functionality, backward-compatible
- **PATCH** — backward-compatible bug fixes

---

## Step 3: Build and Verify After Bump

After bumping the version, rebuild the binary to confirm the bumped version compiles
and the new version string is embedded correctly:

```bash
uv sync --group dev --group build
python scripts/build_services.py            # Build everything (default)
```

Output: `dist/mvm` (main binary) and `dist/services/mvm-services` (multidist services binary).

Verify the binary reports the new version:
```bash
./dist/mvm --version
./dist/mvm --help
```

## Step 4: Commit and Push

If you used `--commit` in step 2, skip to step 5. Otherwise:

```bash
git add pyproject.toml src/mvmctl/__init__.py CHANGELOG.md docs/mvm.1 \
       packaging/PKGBUILD packaging/mvmctl.spec packaging/debian/changelog packaging/debian/control
git commit -m "chore: bump version to 0.2.0"
git push origin main
```

Wait for CI (`ci.yml`) to pass on this commit — it runs all tests with an 80% coverage
gate. Do not tag until CI is green.

---

## Step 5: Tag and Push the Tag

```bash
# Create an annotated tag
git tag -a v0.2.0 -m "Release v0.2.0"

# Push the tag (triggers release.yml workflow)
git push origin v0.2.0
```

Pushing a tag matching `v*.*.*` triggers the release workflow.

---

## Step 6: CI Pipeline

The `.github/workflows/release.yml` workflow runs automatically when the tag is pushed:

| Job | Description |
|-----|-------------|
| **test** | All tests with 80% coverage gate |
| **build** | Nuitka binary on `ubuntu-24.04`, SHA256 checksum, uploaded as artifact |
| **build-deb** | `.deb` package via `dpkg-buildpackage` |
| **build-rpm** | `.rpm` in Fedora container |
| **build-arch** | Arch PKGBUILD files |
| **publish-pypi** | Wheel + sdist to PyPI via trusted publishing (waits for all prior) |
| **upload-packages-to-release** | Uploads all packages to the GitHub release |

The full run typically takes 5-10 minutes.

---

## Step 7: Verify the Release

### Download and check the binary

```bash
curl -L -o mvm https://github.com/AlanD20/mvmctl/releases/download/v0.2.0/mvm
chmod +x mvm
sha256sum -c mvm.sha256
./mvm --version
# Expected: mvm 0.2.0
```

### Check PyPI

```bash
uv tool install mvmctl==0.2.0
mvm --version
# Expected: mvm 0.2.0
```

### Check GitHub release page

Visit the releases page and confirm:
- Release notes are present and accurate
- The `mvm` binary and `mvm.sha256` checksum are attached
- `.deb`, `.rpm`, and Arch PKGBUILD are attached

---

## Step 8: Update Downstream Packages

After the release is published, update distribution packaging that requires hardcoded checksums.

### AUR PKGBUILD

```bash
python scripts/post-release.py --aur           # Auto-detects version from latest git tag
python scripts/post-release.py --aur --dry-run # Preview only
```

This downloads the released `mvm` binary and man page from GitHub, computes SHA256 checksums,
updates `packaging/PKGBUILD`, and regenerates `packaging/.SRCINFO`.

Then push the updated PKGBUILD to AUR:

```bash
cd packaging
git commit -am "aur: update to v0.2.0"
git push aur master
```

---

## Step 9: Install the Man Page

```bash
sudo cp docs/mvm.1 /usr/local/share/man/man1/
sudo mandb
man mvm
```

---

## Issuing a Hotfix

```bash
# Branch from the release tag
git checkout -b hotfix/v0.2.1 v0.2.0

# Make the fix, then bump version
./bump-version.py 0.2.1 --commit

# Tag and push
git push origin hotfix/v0.2.1
git tag -a v0.2.1 -m "Release v0.2.1 (hotfix)"
git push origin v0.2.1
```

Then open a PR to merge the hotfix branch back into `main`.

---

## Yanking a Bad Release

### Yank from PyPI

```bash
uv tool install twine
twine yank mvmctl 0.2.0
```

### Mark or delete the GitHub release

```bash
# Mark as pre-release
gh release edit v0.2.0 --prerelease

# Or delete entirely
gh release delete v0.2.0 --yes
git tag -d v0.2.0
git push origin :refs/tags/v0.2.0
```

After yanking, immediately publish a new patch release with the fix.

---

## Appendix: Dynamic Import Handling

Nuitka performs static analysis to detect dependencies. Modules using dynamic registries
(e.g., `passlib`) or runtime lookups (e.g., `rich._unicode_data`, `jinja2.tests`) must be
force-included:

| Module | Nuitka Flag |
|--------|-------------|
| `passlib.handlers.bcrypt` | `--include-module=passlib.handlers.bcrypt` |
| `passlib.handlers.sha2_crypt` | `--include-module=passlib.handlers.sha2_crypt` |
| `rich._unicode_data` | `--include-package=rich._unicode_data` |
| `jinja2.tests` | `--include-module=jinja2.tests` |

These are already configured in `scripts/build_services.py`.
