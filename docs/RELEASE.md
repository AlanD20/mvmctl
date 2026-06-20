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
- [Local Package Builds](#local-package-builds)
- [Cross-Compilation](#cross-compilation)
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

# E2E tests (requires KVM, groups, assets inside runner VM)
export MVM_BINARY=dist/mvm
export MVM_ASSET_MIRROR=~/.cache/mvm-asset-mirror
# Run inside the disposable runner VM (Firecracker VM with nested KVM)
pytest tests/e2e/
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
- `--arch ARCH` — Target architecture: `amd64`, `arm64`, or `all` (default: host arch)

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

The `release.yml` workflow runs on tag push (`v*.*.*`). It's a single pipeline:

1. **Test** — `go mod tidy`, `go vet ./...`, `go test ./...` with coverage
2. **Build all packages** — runs `scripts/build-packages.sh --build-binaries --version X.Y.Z`

The `build-packages.sh` script produces everything in `dist/packages/`:

```
dist/packages/
├── mvmctl_X.Y.Z-1_amd64.deb        # Debian/Ubuntu amd64
├── mvmctl_X.Y.Z-1_arm64.deb        # Debian/Ubuntu arm64
├── mvmctl-X.Y.Z-1.x86_64.rpm       # Fedora/RHEL x86_64
├── mvmctl-X.Y.Z-1.aarch64.rpm      # Fedora/RHEL aarch64
├── mvmctl-bin-X.Y.Z-1-x86_64.pkg.tar.zst  # Arch Linux x86_64
├── PKGBUILD                         # Arch Linux build recipe
├── mvm                              # Standalone amd64 binary
├── mvm-arm64                        # Standalone arm64 binary
└── checksums.sha256                 # SHA256 for all artifacts
```

All of these are uploaded to the GitHub Release automatically.

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

## Local Package Builds

### Option A: Direct (any Linux)

```bash
# Full build from source (cross-compiles both arches)
./scripts/build-packages.sh --build-binaries --version X.Y.Z

# Or build packages from existing binaries
./scripts/build-packages.sh --version X.Y.Z

# Output goes to dist/packages/
```

Requirements:
- **Debian packages**: `sudo apt-get install debhelper build-essential`
- **RPM packages**: `sudo apt-get install rpm` (Ubuntu) or `sudo dnf install rpm-build` (Fedora)
- **Arch packages**: build on Arch Linux with `base-devel`, or use the PKGBUILD

### Option B: Self-hosted (mvmctl + Firecracker VMs)

Uses mvmctl's own Firecracker VMs to build everything in an isolated Ubuntu 24.04
environment — identical to what CI runs:

```bash
# Build both arch binaries first
./scripts/build.sh release --arch all

# Provision VM, build all packages, retrieve artifacts
mvm env apply packaging/mvmctl.yaml

# Output: mvmctl_*.deb, mvmctl-*.rpm, mvmctl-bin-*.pkg.tar.zst
```

The `packaging/mvmctl.yaml` spec creates a single Ubuntu 24.04 Firecracker VM,
installs Go 1.26 + `debhelper` + `rpm` + Docker, and runs
`build-packages.sh --build-binaries` inside it. The Arch `.pkg.tar.zst` is built
in a Docker `archlinux:latest` container within the VM (same approach as CI).

This is useful for:
- Testing the exact CI environment locally
- Building packages without installing build tools on the host
- Reproducing release artifacts in an isolated VM

---

## Cross-Compilation

Go cross-compilation is supported out of the box (`CGO_ENABLED=0`):

```bash
# Build for both architectures (one command)
./scripts/build.sh release --arch all

# Build for arm64 only
./scripts/build.sh release --arch arm64

# Build for amd64 only (default)
./scripts/build.sh release --arch amd64
```

The guest agent (vsock) is automatically cross-compiled and embedded for the target architecture.

**Limitations:**
- `makepkg` (Arch Linux) only builds for the host architecture — arm64 packages require an arm64 host
- `dpkg-buildpackage` uses `dh_strip` which can't strip cross-arch binaries — the `build-packages.sh` script handles this with `DEB_BUILD_OPTIONS=nostrip` for arm64

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
