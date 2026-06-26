#!/usr/bin/env bash
# ─── Build all distribution packages: .deb, .rpm, .pkg.tar.zst ──────────────
#
# Builds both amd64 and arm64 packages for all supported formats.
# Requires: Go 1.26+, debhelper, rpm-build (on Fedora), and the mvm binary
# already cross-compiled (or use --build-binaries to do it here).
#
# Usage:
#   ./scripts/build-packages.sh                    # uses existing dist/mvm*
#   ./scripts/build-packages.sh --build-binaries   # also builds from source
#   ./scripts/build-packages.sh --version X.Y.Z    # explicit version
#
# Output directory: ./dist/packages/
#
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_DIR="${PROJECT_DIR}/dist/packages"
VERSION=""

# ─── Parse flags ────────────────────────────────────────────────────────────

BUILD_BINARIES=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --build-binaries) BUILD_BINARIES=true; shift ;;
    --version)
      if [[ -z "${2:-}" ]]; then echo "ERROR: --version requires a value" >&2; exit 1; fi
      VERSION="$2"; shift 2 ;;
    --help|-h)
      sed -n '2,12p' "$0" | sed 's/^# \?//'; exit 0 ;;
    *) echo "ERROR: unknown argument: $1" >&2; exit 1 ;;
  esac
done

# ─── Resolve version ────────────────────────────────────────────────────────

if [[ -z "${VERSION}" ]]; then
  VERSION="$(cd "$PROJECT_DIR" && git describe --tags --dirty --always 2>/dev/null || echo "0.0.0-dev")"
  VERSION="${VERSION#v}"
fi

echo "==> Building packages for mvmctl v${VERSION}"
echo "    output: ${OUTPUT_DIR}"
mkdir -p "${OUTPUT_DIR}"

# ─── Build binaries ─────────────────────────────────────────────────────────

if [[ "$BUILD_BINARIES" == "true" ]]; then
  echo ""
  echo "==> Building binaries..."
  "${SCRIPT_DIR}/build.sh" release --arch all --version "${VERSION}"
fi

# Verify binaries exist
if [[ ! -f "${PROJECT_DIR}/dist/mvm" ]]; then
  echo "ERROR: dist/mvm not found. Build it first with: ./scripts/build.sh release --arch all" >&2
  exit 1
fi
if [[ ! -f "${PROJECT_DIR}/dist/mvm-arm64" ]]; then
  echo "ERROR: dist/mvm-arm64 not found. Build it first with: ./scripts/build.sh release --arch all" >&2
  exit 1
fi

# ─── Debian .deb packages ───────────────────────────────────────────────────

build_deb() {
  local arch="$1"     # amd64 or arm64
  local binary="$2"   # path to mvm binary
  local pkgarch="$3"  # debian arch name

  echo ""
  echo "==> Building .deb (${arch})..."

  local builddir="${OUTPUT_DIR}/deb-build-${arch}"
  rm -rf "${builddir}"
  mkdir -p "${builddir}/debian" "${builddir}/dist" "${builddir}/docs"
  cp -r "${PROJECT_DIR}/packaging/debian/"* "${builddir}/debian/"
  sed -i "s/Architecture:.*/Architecture: ${pkgarch}/" "${builddir}/debian/control"
  cp "${binary}" "${builddir}/dist/mvm"
  cp "${PROJECT_DIR}/docs/mvm.1" "${builddir}/docs/"

  # Stage files for dpkg-deb. Using dpkg-deb instead of dpkg-buildpackage
  # avoids cross-architecture issues when packaging an arm64 binary on an
  # amd64 host (the usual case for the arm64 .deb build).
  local staging="${builddir}/staging"
  mkdir -p "${staging}/DEBIAN" "${staging}/usr/bin" "${staging}/usr/share/man/man1"
  cp "${binary}" "${staging}/usr/bin/mvm"
  chmod 755 "${staging}/usr/bin/mvm"
  cp "${PROJECT_DIR}/docs/mvm.1" "${staging}/usr/share/man/man1/"
  gzip -9 "${staging}/usr/share/man/man1/mvm.1"

  # Generate a minimal DEBIAN/control. We use dpkg-deb instead of
  # dpkg-buildpackage so the same amd64 host can package an arm64 binary
  # without needing a full cross-build toolchain.
  cat > "${staging}/DEBIAN/control" <<EOF
Package: mvmctl
Version: ${VERSION}-1
Section: utils
Priority: optional
Architecture: ${pkgarch}
Maintainer: AlanD20 <aland20@pm.me>
Depends: iproute2, iptables, nftables, qemu-utils, openssh-client, e2fsprogs, util-linux, passwd, sudo, procps, kmod, tar
Recommends: cloud-image-utils, libguestfs-tools | guestfs-tools
Description: MicroVM Manager - Container speed, VM isolation
 mvmctl is a production-grade CLI for managing microVMs on Linux.
 It handles VM lifecycle: downloading kernels/images, networking, VM creation,
 SSH access, log streaming, snapshots, and cleanup.
 .
 Features:
  - Firecracker microVM orchestration
  - Bridge/TAP networking with NAT
  - SSH key management
  - VM snapshots and state management
  - Cloud-init integration
  - Multiple kernel and image support
EOF

  dpkg-deb --root-owner-group --build "${staging}" \
    "${OUTPUT_DIR}/mvmctl_${VERSION}-1_${pkgarch}.deb"

  rm -rf "${builddir}"
  echo "    done: $(ls -lh "${OUTPUT_DIR}"/mvmctl_*"${pkgarch}.deb" | awk '{print $5}')"
}

build_deb "amd64" "${PROJECT_DIR}/dist/mvm" "amd64"
build_deb "arm64" "${PROJECT_DIR}/dist/mvm-arm64" "arm64"

# ─── RPM packages ───────────────────────────────────────────────────────────

build_rpm() {
  local arch="$1"      # x86_64 or aarch64
  local binary="$2"    # path to mvm binary
  local target="$3"    # rpmbuild --target value

  echo ""
  echo "==> Building .rpm (${arch})..."

  # Debian/Ubuntu hosts: rpmbuild is in the 'rpm' package
  if ! command -v rpmbuild &>/dev/null; then
    echo "    WARNING: rpmbuild not found, skipping .rpm build"
    echo "    Install with: sudo apt-get install rpm  (or dnf install rpm-build)"
    return
  fi

  local builddir="${OUTPUT_DIR}/rpmbuild-${arch}"
  rm -rf "${builddir}"
  mkdir -p "${builddir}"/{SPECS,SOURCES,BUILD,RPMS,SRPMS}
  cp "${PROJECT_DIR}/packaging/mvmctl.spec" "${builddir}/SPECS/"
  cp "${binary}" "${builddir}/SOURCES/mvm"
  cp "${PROJECT_DIR}/docs/mvm.1" "${builddir}/SOURCES/"
  cp "${PROJECT_DIR}/packaging/LICENSE" "${builddir}/BUILD/"

  rpmbuild -bb --target "${target}" \
    --define "_topdir ${builddir}" \
    "${builddir}/SPECS/mvmctl.spec" 2>&1 | tail -3
  cp "${builddir}/RPMS/${arch}/"*.rpm "${OUTPUT_DIR}/"
  rm -rf "${builddir}"
  echo "    done: $(ls -lh "${OUTPUT_DIR}"/*"${arch}.rpm" | awk '{print $5}')"
}

build_rpm "x86_64" "${PROJECT_DIR}/dist/mvm" "x86_64"
build_rpm "aarch64" "${PROJECT_DIR}/dist/mvm-arm64" "aarch64"

# ─── Arch Linux .pkg.tar.zst ─────────────────────────────────────────────────

echo ""
echo "==> Building Arch .pkg.tar.zst..."
if command -v docker &>/dev/null; then
  # Use the local PKGBUILD with file:// sources pointing to pre-built binaries
  # so makepkg doesn't try to download from GitHub (which may not have the release yet).
  pkgbuild="/tmp/arch-PKGBUILD-local"
  if [[ ! -f "$pkgbuild" ]]; then
    pkgbuild="/mnt/mvmctl/packaging/PKGBUILD"
  fi
  if [[ ! -f "$pkgbuild" ]]; then
    echo "    WARNING: no local PKGBUILD found — shipping PKGBUILD + binaries"
    cp "${PROJECT_DIR}/packaging/PKGBUILD" "${OUTPUT_DIR}/"
    cp "${PROJECT_DIR}/dist/mvm" "${OUTPUT_DIR}/"
    cp "${PROJECT_DIR}/dist/mvm-arm64" "${OUTPUT_DIR}/"
  else
    docker run -i --rm -v "${PROJECT_DIR}:/work:Z" -v "${pkgbuild}:/tmp/PKGBUILD:Z" \
      -e "VERSION=${VERSION}" \
      archlinux:latest bash -s <<'DOCKEREOF'
      set -euo pipefail
      pacman -Syu --noconfirm base-devel sudo >/dev/null 2>&1
      useradd -m build
      echo 'build ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/build

      # Generate a local PKGBUILD that uses the pre-built binaries in /work
      # instead of downloading release assets from GitHub.
      cat > /work/PKGBUILD <<'PKEOF'
pkgname=mvmctl-bin
pkgver=${VERSION}
pkgrel=1
pkgdesc="MicroVM Manager - Container speed, VM isolation"
arch=('x86_64' 'aarch64')
url="https://github.com/AlanD20/mvmctl"
license=('MIT')
depends=(
  'iproute2' 'nftables' 'iptables'
  'qemu-base'
  'openssh'
  'e2fsprogs' 'util-linux'
  'kmod'
  'sudo' 'shadow'
  'tar'
)
provides=('mvmctl')
conflicts=('mvmctl')
source=("mvm" "mvm.1")
sha256sums=('SKIP' 'SKIP')
package() {
    install -Dm755 "${srcdir}/mvm" "${pkgdir}/usr/bin/mvm"
    install -Dm644 "${srcdir}/mvm.1" "${pkgdir}/usr/share/man/man1/mvm.1"
    gzip -9 "${pkgdir}/usr/share/man/man1/mvm.1"
}
PKEOF

      cp /work/dist/mvm /work/mvm
      cp /work/docs/mvm.1 /work/mvm.1
      chown -R build:build /work
      cd /work
      su build -c 'makepkg -f --noconfirm --nodeps' 2>&1 | tail -5
      mv mvmctl-bin-*.pkg.tar.zst /work/dist/packages/ 2>/dev/null || true
DOCKEREOF
    echo "    done: $(ls -lh "${OUTPUT_DIR}"/*.pkg.tar.zst 2>/dev/null | awk '{print $5}')"
  fi
elif command -v makepkg &>/dev/null; then
  # Native Arch host
  cp "${PROJECT_DIR}/packaging/PKGBUILD" "${OUTPUT_DIR}/"
  cd "${OUTPUT_DIR}"
  makepkg -f --noconfirm 2>&1 | tail -5
else
  echo "    WARNING: no docker or makepkg found — shipping PKGBUILD + binaries instead"
  cp "${PROJECT_DIR}/packaging/PKGBUILD" "${OUTPUT_DIR}/"
  cp "${PROJECT_DIR}/dist/mvm" "${OUTPUT_DIR}/"
  cp "${PROJECT_DIR}/dist/mvm-arm64" "${OUTPUT_DIR}/"
fi

# ─── Checksums ──────────────────────────────────────────────────────────────

echo ""
echo "==> Generating checksums..."
cd "${OUTPUT_DIR}"
sha256sum ./*.deb ./*.rpm ./*.pkg.tar.zst ./PKGBUILD 2>/dev/null > checksums.sha256 || true
cat checksums.sha256

# ─── Summary ────────────────────────────────────────────────────────────────

echo ""
echo "=========================================="
echo " All packages built in ${OUTPUT_DIR}"
echo "=========================================="
ls -lh "${OUTPUT_DIR}/"
