#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Source configuration
if [ -f "config.env" ]; then
  source config.env
fi

# Configuration
KERNEL_VERSION="${KERNEL_VERSION:-6.12}"
KERNEL_SOURCE_URL="${KERNEL_SOURCE_URL:-https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz}"
BUILD_DIR="${BUILD_DIR:-/tmp/firecracker-kernel-build}"
OUTPUT_KERNEL="${OUTPUT_KERNEL:-kernels/vmlinux-upstream}"
PARALLEL_JOBS="${PARALLEL_JOBS:-$(nproc)}"

# FIX: Always use the 6.1 config as base — it's the latest Firecracker maintains.
# The original script used ${KERNEL_VERSION%.*} which produced "6" not "6.1",
# causing the download to fail silently and fall back to defconfig.
FIRECRACKER_CONFIG_URL="https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config"

echo "=== Firecracker Upstream Kernel Builder ==="
echo ""
echo "Configuration:"
echo "  Kernel Version: $KERNEL_VERSION"
echo "  Build Directory: $BUILD_DIR"
echo "  Output Kernel: $OUTPUT_KERNEL"
echo "  Parallel Jobs: $PARALLEL_JOBS"
echo ""

# Check dependencies
check_dependencies() {
  echo "[1/4] Checking dependencies..."

  local missing=""
  for cmd in git curl make gcc flex bison bc pahole; do
    if ! command -v "$cmd" &>/dev/null; then
      missing="$missing $cmd"
    fi
  done

  # Check for libelf
  if ! pkg-config --exists libelf 2>/dev/null; then
    missing="$missing libelf-dev"
  fi

  # Check for openssl headers (required for kernel >= 5.x)
  if ! pkg-config --exists openssl 2>/dev/null; then
    missing="$missing libssl-dev"
  fi

  if ! command -v ld &>/dev/null; then
    missing="$missing build-essential"
  fi

  if [ -n "$missing" ]; then
    echo "ERROR: Missing dependencies:$missing"
    echo ""
    echo "Install on Ubuntu/Debian:"
    echo "  sudo apt update"
    echo "  sudo apt install -y build-essential libncurses-dev bison flex"
    echo "  sudo apt install -y libssl-dev libelf-dev bc curl git dwarves"
    echo ""
    echo "Install on Arch Linux:"
    echo "  sudo pacman -S base-devel ncurses bison flex"
    echo "  sudo pacman -S openssl bc curl git pahole"
    exit 1
  fi

  echo "✓ All dependencies present"
}

# Download kernel source
download_kernel() {
  echo "[2/4] Downloading kernel source..."

  mkdir -p "$BUILD_DIR"
  cd "$BUILD_DIR"

  if [ -d "linux-${KERNEL_VERSION}" ]; then
    echo " - Kernel source already exists at linux-${KERNEL_VERSION}, skipping download"
    cd "linux-${KERNEL_VERSION}"
    return
  fi

  if [[ "$KERNEL_SOURCE_URL" == *.git ]]; then
    echo " - Cloning from git: $KERNEL_SOURCE_URL"
    git clone --depth=1 "$KERNEL_SOURCE_URL" "linux-${KERNEL_VERSION}"
    cd "linux-${KERNEL_VERSION}"
  else
    echo " - Downloading Linux ${KERNEL_VERSION}..."
    echo "   URL: $KERNEL_SOURCE_URL"
    curl -L --progress-bar -o "linux-${KERNEL_VERSION}.tar.xz" "$KERNEL_SOURCE_URL"
    echo " - Extracting..."
    tar xf "linux-${KERNEL_VERSION}.tar.xz"
    rm -f "linux-${KERNEL_VERSION}.tar.xz"
    cd "linux-${KERNEL_VERSION}"
  fi

  echo "✓ Kernel source ready"
}

# Configure kernel
configure_kernel() {
  echo "[3/4] Configuring kernel..."

  cd "$BUILD_DIR/linux-${KERNEL_VERSION}"

  # Download Firecracker microvm base config
  echo " - Downloading Firecracker microvm base config..."
  echo "   URL: $FIRECRACKER_CONFIG_URL"

  if curl -fsSL -o .config "$FIRECRACKER_CONFIG_URL"; then
    echo "   ✓ Firecracker base config downloaded"
  else
    echo "   WARNING: Could not download Firecracker config, falling back to defconfig"
    make defconfig
  fi

  # Sync config to the current kernel version (fills in new symbols with defaults)
  echo " - Synchronizing config to kernel ${KERNEL_VERSION}..."
  make olddefconfig

  # --- Filesystems ---
  echo " - Enabling filesystems..."
  # FIX: btrfs must be =y (built-in), not =m (module). Firecracker has no initrd
  # to load modules before mounting root, so module support is useless here.
  ./scripts/config --enable CONFIG_BTRFS_FS
  ./scripts/config --enable CONFIG_BTRFS_FS_POSIX_ACL
  ./scripts/config --enable CONFIG_EXT4_FS
  ./scripts/config --enable CONFIG_EXT4_FS_POSIX_ACL
  ./scripts/config --enable CONFIG_XFS_FS
  ./scripts/config --enable CONFIG_SQUASHFS

  # --- VirtIO (all must be built-in for Firecracker) ---
  echo " - Enabling VirtIO drivers (built-in)..."
  ./scripts/config --enable CONFIG_VIRTIO
  ./scripts/config --enable CONFIG_VIRTIO_MENU
  ./scripts/config --enable CONFIG_VIRTIO_PCI
  ./scripts/config --enable CONFIG_VIRTIO_BLK
  ./scripts/config --enable CONFIG_VIRTIO_NET
  ./scripts/config --enable CONFIG_VIRTIO_CONSOLE

  # --- Serial console ---
  echo " - Enabling serial console..."
  ./scripts/config --enable CONFIG_SERIAL_8250
  ./scripts/config --enable CONFIG_SERIAL_8250_CONSOLE

  # FIX: CONFIG_SERIAL_8250_NR_UARTS is an integer value, not a boolean.
  # Using --enable on it sets it to "y" which is wrong and can cause build errors.
  # Set it properly as an integer instead.
  ./scripts/config --set-val CONFIG_SERIAL_8250_NR_UARTS 4

  # --- Network ---
  echo " - Enabling network support..."
  ./scripts/config --enable CONFIG_NET
  ./scripts/config --enable CONFIG_INET
  ./scripts/config --enable CONFIG_IPV6

  # --- KVM / paravirt guest optimizations ---
  echo " - Enabling KVM guest optimizations..."
  ./scripts/config --enable CONFIG_KVM_GUEST
  ./scripts/config --enable CONFIG_PARAVIRT

  # --- Some applications require these functionality. e.g, pacman ---
  echo " - Enabling LandLock..."
  ./scripts/config --enable CONFIG_SECURITY_LANDLOCK
  ./scripts/config --enable CONFIG_BPF_SYSCALL
  ./scripts/config --enable CONFIG_CGROUPS
  ./scripts/config --enable CONFIG_MEMCG

  echo " - The pain with upstream kernel with Firecracker..."
  ./scripts/config --enable CONFIG_PCI
  ./scripts/config --disable CONFIG_BLK_DEV_ZONED
  ./scripts/config --disable CONFIG_VIRTIO_BLK_F_SECURE_ERASE
  ./scripts/config --disable CONFIG_VIRTIO_BLK_SCSI


  # FIX: Re-run olddefconfig AFTER all --enable calls so dependency resolution
  # picks up everything we just enabled. The original script only ran it once
  # before the --enable calls, so dependencies of newly enabled options
  # (e.g. CONFIG_BTRFS_FS pulling in CONFIG_LIBCRC32C) could be left unset.
  echo " - Resolving all dependencies..."
  make olddefconfig

  # Verify critical settings are actually =y (not =m or unset)
  echo " - Verifying configuration..."
  local fail=0
  for setting in \
    CONFIG_BTRFS_FS=y \
    CONFIG_VIRTIO_BLK=y \
    CONFIG_VIRTIO_NET=y \
    CONFIG_SERIAL_8250_CONSOLE=y \
    CONFIG_KVM_GUEST=y; do
    if ! grep -q "^${setting}$" .config; then
      echo "  MISSING or wrong value: $setting"
      fail=1
    else
      echo "  ✓ $setting"
    fi
  done

  if [ "$fail" -eq 1 ]; then
    echo ""
    echo "ERROR: One or more required config options are not set correctly."
    echo "       Check .config manually and re-run."
    exit 1
  fi

  echo "✓ Kernel configured"
}

# Build the kernel
build_kernel() {
  echo "[4/4] Building kernel..."

  cd "$BUILD_DIR/linux-${KERNEL_VERSION}"

  echo " - Building vmlinux with ${PARALLEL_JOBS} parallel jobs..."
  echo "   This may take 10-30 minutes depending on your system..."
  echo ""

  # FIX: Capture build output to a log file so errors aren't buried in parallel
  # output. On failure, print the last 60 lines which will contain the real error.
  local build_log="/tmp/kernel-build-$(date +%s).log"
  echo "   Build log: $build_log"
  echo ""

  if ! make vmlinux -j"$PARALLEL_JOBS" 2>&1 | tee "$build_log"; then
    echo ""
    echo "ERROR: Kernel build failed. Last output:"
    echo "---"
    # FIX: grep for actual compiler/linker errors rather than showing raw tail,
    # which is usually just successful CC lines from parallel jobs.
    grep -E "error:|Error|undefined reference|cannot find" "$build_log" | tail -30
    echo "---"
    echo "Full log: $build_log"
    exit 1
  fi

  if [ ! -f "vmlinux" ]; then
    echo "ERROR: Build succeeded but vmlinux not found — something is wrong"
    exit 1
  fi

  # Copy to output
  mkdir -p "$(dirname "$SCRIPT_DIR/$OUTPUT_KERNEL")"
  cp vmlinux "$SCRIPT_DIR/$OUTPUT_KERNEL"
  chmod +x "$SCRIPT_DIR/$OUTPUT_KERNEL"

  local size
  size=$(du -sh "$SCRIPT_DIR/$OUTPUT_KERNEL" | cut -f1)
  echo "✓ Kernel built successfully ($size)"
  echo ""
  echo "Output: $SCRIPT_DIR/$OUTPUT_KERNEL"
  echo ""
  echo "Recommended boot_args for Firecracker:"
  echo "  console=ttyS0 reboot=k panic=1 pci=off root=/dev/vda rw rootfstype=btrfs"
}

# Show config summary
show_config_summary() {
  cd "$BUILD_DIR/linux-${KERNEL_VERSION}"
  echo ""
  echo "=== Build Summary ==="
  echo ""
  echo "Filesystems:"
  grep -E "^CONFIG_(BTRFS|EXT4|XFS)_FS=" .config

  echo ""
  echo "VirtIO:"
  grep -E "^CONFIG_VIRTIO_(BLK|NET|PCI|CONSOLE)=" .config

  echo ""
  echo "Serial Console:"
  grep -E "^CONFIG_SERIAL_8250" .config
}

# Cleanup
cleanup() {
  if [ -d "$BUILD_DIR" ] && [ "${KEEP_BUILD:-false}" != "true" ]; then
    echo ""
    echo "Cleaning up build directory... (set KEEP_BUILD=true to skip)"
    rm -rf "$BUILD_DIR"
  fi
}

main() {
  check_dependencies
  download_kernel
  configure_kernel
  build_kernel
  show_config_summary
  cleanup

  echo ""
  echo "=== Build Complete ==="
}

main "$@"
