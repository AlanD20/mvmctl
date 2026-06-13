# Release Process

This document covers how to release a new version of mvmctl from start to finish.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Step 1: Verify Locally](#step-1-verify-locally)
- [Step 2: Bump the Version](#step-2-bump-the-version)
- [Step 3: Build and Verify After Bump](#step-3-build-and-verify-after-bump)
- [Step 4: Commit and Push](#step-4-commit-and-push)
- [Step 5: Tag and Push the Tag](#step-5-tag-and-push-the-tag)
- [Step 6: CI Pipeline](#step-6-ci-pipeline)
- [Step 7: Verify the Release](#step-7-verify-the-release)
- [Step 8: Update AUR Package](#step-8-update-aur-package)
- [Issuing a Hotfix](#issuing-a-hotfix)
- [Yanking a Bad Release](#yanking-a-bad-release)

---

## Prerequisites

- **Go 1.26+** — local development and building
- **Linux (KVM-capable host)** — tests and valid binary
- **git** — tagging and pushing
- **uv** — Python dependency management (for system tests only)

---

## Step 1: Verify Locally

Run the full CI gate:

```bash
# Compile
go build ./...

# Vet
go vet ./...

# Unit tests
go test ./...

# Build release binary for system tests
./scripts/build.sh release
cp dist/mvm ~/.local/bin/mvm

# System tests (requires KVM, groups, assets)
# Run per-domain — batch runs cause cross-file state pollution.
export MVM_BINARY=dist/mvm
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
for domain in \
  bin cache cli config console cp host \
  images init invariants kernel keys logs \
  network ssh vm volume zzz_destructive; do
  python3 scripts/run_tests.py --domain "$domain"
done
```

All five must pass. If system tests fail, investigate before proceeding.

To set up a test environment from scratch:

```bash
sudo python3 scripts/setup-test-environment.py
```

This installs system packages, configures KVM, sets up mvmctl, and pre-downloads
test assets. See `python3 scripts/setup-test-environment.py --help` for options.

---

## Step 2: Bump the Version

```bash
# Bump version across all project files (requires version argument)
python scripts/bump-version.py X.Y.Z

# Preview changes first
python scripts/bump-version.py X.Y.Z --dry-run

# Bump and auto-commit
python scripts/bump-version.py X.Y.Z --commit
```

The script updates version strings in:
- `internal/lib/version/info.go` — Go source version defaults
- `packaging/PKGBUILD` — Arch Linux pkgver
- `packaging/mvmctl.spec` — RPM spec Version + `%changelog` entry
- `packaging/debian/changelog` — Debian changelog entry
- `docs/mvm.1` — Man page `.TH` header version
- `CHANGELOG.md` — Moves `[Unreleased]` content to new version section

---

## Step 3: Build and Verify After Bump

```bash
# Build release binary (stripped, PIE, static, netgo/osusergo)
./scripts/build.sh release --version X.Y.Z

# Verify version output
./dist/mvm version
```

The build script (`scripts/build.sh`) is the canonical way to build. It supports:

| Mode | Command | Output | Use case |
|------|---------|--------|----------|
| Dev | `./scripts/build.sh` | `./mvm` | Local development (debuggable) |
| Release | `./scripts/build.sh release` | `./dist/mvm` | Production release (stripped, PIE) |
| Version | `./scripts/build.sh version` | stdout | Print resolved version |

Options:
- `--version X.Y.Z` — Explicit version (overrides auto-detection)
- `--output ./path` — Custom output path

Version detection priority:
1. `--version X.Y.Z` flag
2. `GITHUB_REF_NAME` environment variable (CI — strips leading "v")
3. `git describe --tags --dirty --always`
4. Fallback: `"0.0.0-dev"`

---

## Step 4: Commit and Push

```bash
git add -A
git commit -m "release: vX.Y.Z"
git push origin main
```

---

## Step 5: Tag and Push the Tag

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

---

## Step 6: CI Pipeline

The `release.yml` workflow runs on tag push (`v*.*.*`):

1. `go mod tidy` + diff check — ensures no stale dependencies
2. `go vet ./...` — static analysis
3. `go build ./...` — compile check
4. `go test ./...` — unit tests with coverage
5. Build release binary via `./scripts/build.sh release --version X.Y.Z`
6. Smoke test the binary (`./dist/mvm --version`, `./dist/mvm --help`)
7. Generate SHA256 checksum
8. Build distribution packages (`.deb`, `.rpm`, PKGBUILD)
9. Create GitHub release with all assets

The `ci.yml` workflow (push to main, PRs) additionally runs:
- `go mod tidy` + diff check
- `gofmt` formatting check
- `golines --max-len=120` line-length enforcement
- `go vet ./...` — static analysis
- `go build ./...` — compile check
- Coverage report generation and upload

System tests (`system-tests.yml`) are triggered manually via `workflow_dispatch`
and run on a KVM-capable runner.

---

## Step 7: Verify the Release

```bash
# Check the tag exists
git tag -l 'v*'

# Verify the binary
./scripts/build.sh release --version X.Y.Z
./dist/mvm --version

# Check the GitHub release
gh release view vX.Y.Z
```

---

## Step 8: Update AUR Package

After the GitHub release is published, update the AUR PKGBUILD checksums:

```bash
# Preview changes
python scripts/post-release.py --aur --dry-run

# Update checksums and regenerate .SRCINFO
python scripts/post-release.py --aur
```

Then push to AUR:

```bash
cd packaging
git push aur master
```

---

## Issuing a Hotfix

1. Create a branch from the release tag: `git checkout -b hotfix/vX.Y.Z+1 vX.Y.Z`
2. Apply the fix
3. Run the full CI gate (Step 1)
4. Bump the patch version (Step 2)
5. Commit, tag, push (Steps 4-5)

---

## Yanking a Bad Release

```bash
# Delete the remote tag
git push --delete origin vX.Y.Z

# Delete the local tag
git tag -d vX.Y.Z
```

Document the reason in the changelog or a GitHub release note.
