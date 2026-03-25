# Release Process

This document covers how to release a new version of firecracker-manager.

## Prerequisites

Before releasing, ensure the following are available on your workstation:

- **Python 3.13+** — required for local development and building
- **Linux (KVM-capable host)** — required to run integration tests and to produce a valid binary
- **git** — for tagging and pushing
- **uv** — for dependency management and running tools (`pip install uv` or see [uv docs](https://docs.astral.sh/uv/))

## Bumping the Version

The version is defined in one place: the `version` field under `[project]` in `pyproject.toml`. Update it there, and also update the `__version__` fallback in `src/fcm/__init__.py` to match.

This project uses **semantic versioning** (MAJOR.MINOR.PATCH):

- **MAJOR** — increment when you make incompatible API or CLI changes (e.g., removing a command, renaming a flag without a deprecation alias, changing config file format in a breaking way).
- **MINOR** — increment when you add new functionality in a backward-compatible manner (e.g., new commands, new flags, new config keys with defaults).
- **PATCH** — increment when you make backward-compatible bug fixes (e.g., fixing a crash, correcting wrong behavior, documentation fixes that ship with the binary).

Example: going from `0.3.1` to `0.4.0` means new features were added; going to `0.3.2` means only bugs were fixed.

## Tagging and Pushing

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

1. **Binary builds** — PyInstaller builds a standalone `fcm` binary on two runners:
   - `ubuntu-22.04` (glibc 2.35) — produces artifact named `fcm-linux-ubuntu-22.04`
   - `ubuntu-24.04` (glibc 2.39) — produces artifact named `fcm-linux-ubuntu-24.04`

   Two binaries are needed because a binary linked against glibc 2.39 will not run on a host with glibc 2.35.

2. **GitHub release creation** — a GitHub release is created for the tag with auto-generated release notes. Both binaries are attached as release assets.

3. **Artifact upload** — all build artifacts are uploaded as GitHub Actions artifacts for debugging if needed.

> **Note:** PyPI publishing is **not** automated by the release workflow. To publish to PyPI, follow the manual steps in the [Yanking a Bad Release](#yanking-a-bad-release) section or run `uv build && twine upload dist/*` after verifying the release.

You do not need to run PyInstaller or `gh release` manually. Binary builds and GitHub release creation are automated.

## Verifying a Release

After the workflow completes (typically 5-10 minutes), verify the release is correct:

### Binary verification

```bash
# Download the binary for your platform
curl -L -o fcm https://github.com/<org>/firecracker-manager/releases/download/v1.2.3/fcm-linux-ubuntu-24.04
chmod +x fcm

# Check the version
./fcm --version
# Expected: fcm 1.2.3
```

### PyPI verification

```bash
# Install from PyPI
pip install firecracker-manager==1.2.3

# Check the version
fcm --version
# Expected: fcm 1.2.3
```

### pipx / uvx verification

```bash
# Install with pipx
pipx install firecracker-manager==1.2.3
fcm --version

# Or run directly with uvx (no install)
uvx firecracker-manager==1.2.3 fcm --version
```

### GitHub release page

Visit the releases page and confirm:
- The release notes are present and accurate.
- Both binary assets (`fcm-linux-ubuntu-22.04`, `fcm-linux-ubuntu-24.04`) are attached.

## Issuing a Hotfix

If a bug is found in a released version and `main` has moved on with new features, issue a hotfix from the release tag:

```bash
# Branch from the release tag
git checkout -b hotfix/v1.2.4 v1.2.3

# Make the fix
# ...

# Bump the patch version in pyproject.toml and src/fcm/__init__.py
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
twine yank firecracker-manager 1.2.3
```

Yanking does not delete the release — it marks it so that `pip install firecracker-manager` will not pick it up, but `pip install firecracker-manager==1.2.3` still works for anyone who explicitly pins to it.

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
