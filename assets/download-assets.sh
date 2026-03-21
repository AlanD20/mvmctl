#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS_DIR="$(cd "$SCRIPT_DIR" && pwd)"
cd "$SCRIPT_DIR"

echo "=== Firecracker CI Assets Setup ==="

ARCH="x86_64"
KERNEL_OUTPUT="kernels/vmlinux"
ROOTFS_OUTPUT_BASE="images/ubuntu"
KEYS_DIR="keys"

detect_latest_ci_version() {
  echo "[1/4] Detecting latest Firecracker CI version..."
  local release_url="https://github.com/firecracker-microvm/firecracker/releases"
  local latest_version=$(basename $(curl -fsSLI -o /dev/null -w %{url_effective} ${release_url}/latest))
  CI_VERSION=${latest_version%.*}
  FULL_VERSION="$latest_version"
  echo "✓ Latest CI version: $CI_VERSION (full: $FULL_VERSION)"
}

download_kernel() {
  echo "[2/4] Downloading kernel for $CI_VERSION..."

  if [ -f "$KERNEL_OUTPUT" ]; then
    echo "✓ Kernel already exists: $KERNEL_OUTPUT"
    return
  fi

  mkdir -p "$(dirname "$KERNEL_OUTPUT")"

  local kernel_list_url="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/$CI_VERSION/$ARCH/vmlinux-&list-type=2"
  local latest_kernel_key=$(curl -s "$kernel_list_url" |
    grep -oP "(?<=<Key>)(firecracker-ci/$CI_VERSION/$ARCH/vmlinux-[0-9]+\.[0-9]+\.[0-9]{1,3})(?=</Key>)" |
    sort -V | tail -1)

  if [ -z "$latest_kernel_key" ]; then
    echo "ERROR: Could not find kernel for CI version $CI_VERSION"
    exit 1
  fi

  echo " - Downloading kernel: $latest_kernel_key"
  wget -q -O "$KERNEL_OUTPUT" "https://s3.amazonaws.com/spec.ccfc.min/$latest_kernel_key"
  chmod +x "$KERNEL_OUTPUT"
  echo "✓ Kernel downloaded: $KERNEL_OUTPUT"
}

download_and_convert_rootfs() {
  echo "[3/4] Downloading and converting rootfs..."

  local rootfs_list_url="http://spec.ccfc.min.s3.amazonaws.com/?prefix=firecracker-ci/$CI_VERSION/$ARCH/ubuntu-&list-type=2"
  local latest_ubuntu_key=$(curl -s "$rootfs_list_url" |
    grep -oP "(?<=<Key>)(firecracker-ci/$CI_VERSION/$ARCH/ubuntu-[0-9]+\.[0-9]+\.squashfs)(?=</Key>)" |
    sort -V | tail -1)

  if [ -z "$latest_ubuntu_key" ]; then
    echo "ERROR: Could not find Ubuntu rootfs for CI version $CI_VERSION"
    exit 1
  fi

  local ubuntu_version=$(basename "$latest_ubuntu_key" .squashfs | grep -oE '[0-9]+\.[0-9]+')
  local squashfs_file="/tmp/ubuntu-${ubuntu_version}.squashfs"
  local rootfs_output="${ROOTFS_OUTPUT_BASE}-${ubuntu_version}.ext4"

  if [ -f "$rootfs_output" ]; then
    echo "✓ Rootfs already exists for Ubuntu $ubuntu_version"
    return
  fi

  mkdir -p "$(dirname "$rootfs_output")"

  echo " - Downloading Ubuntu $ubuntu_version rootfs..."
  wget -q -O "$squashfs_file" "https://s3.amazonaws.com/spec.ccfc.min/$latest_ubuntu_key"

  echo " - Extracting squashfs..."
  local temp_dir=$(mktemp -d)
  cd "$temp_dir"
  unsquashfs "$squashfs_file" >/dev/null

  mkdir -p squashfs-root/root/.ssh
  # Add all public keys from assets/keys to authorized_keys
  if [ -d "$ASSETS_DIR/${KEYS_DIR}" ]; then
    for pub_key in "$ASSETS_DIR/${KEYS_DIR}"/*.pub; do
      if [ -f "$pub_key" ]; then
        cat "$pub_key" >>squashfs-root/root/.ssh/authorized_keys
        echo "   Added $(basename "$pub_key") to authorized_keys"
      fi
    done
  fi
  chmod 600 squashfs-root/root/.ssh/authorized_keys

  echo " - Creating ext4 filesystem..."
  #sudo chown -R root:root squashfs-root
  truncate -s 1G "ubuntu-${ubuntu_version}.ext4"
  mkfs.ext4 -d squashfs-root -F "ubuntu-${ubuntu_version}.ext4" >/dev/null

  cp "ubuntu-${ubuntu_version}.ext4" "$ASSETS_DIR/${rootfs_output}"
  # Keys are already in assets/keys/ directory

  cd "$ASSETS_DIR"
  rm -rf "$temp_dir" "$squashfs_file"

  echo "✓ Rootfs created: $rootfs_output"
  echo "✓ SSH keys in: ${KEYS_DIR}/"
}
download_firecracker() {
  echo "[4/4] Checking Firecracker binary..."
  if [ -f "bin/firecracker" ]; then
    echo "✓ Firecracker already exists"
    return
  fi

  mkdir -p bin

  # Use full version for download URL (e.g., v1.11.0 not v1.11)
  local download_url="https://github.com/firecracker-microvm/firecracker/releases/download/${FULL_VERSION}/firecracker-${FULL_VERSION}-${ARCH}.tgz"
  local temp_dir=$(mktemp -d)

  echo " - Downloading Firecracker ${FULL_VERSION}..."
  echo "   URL: $download_url"
  if curl -sL "$download_url" | tar xz -C "$temp_dir" 2>/dev/null; then
    # Find the actual firecracker and jailer binaries (they have version in name)
    local fc_bin=$(find "$temp_dir" -name "firecracker*" -type f | head -1)
    local jailer_bin=$(find "$temp_dir" -name "jailer*" -type f | head -1)
    if [ -n "$fc_bin" ]; then
      cp "$fc_bin" bin/firecracker
      chmod +x bin/firecracker
      echo "✓ Firecracker downloaded"
    else
      echo "⚠️ Could not find firecracker binary in archive"
    fi
    if [ -n "$jailer_bin" ]; then
      cp "$jailer_bin" bin/jailer
      chmod +x bin/jailer
      echo "✓ Jailer downloaded"
    fi
    rm -rf "$temp_dir"
  else
    rm -rf "$temp_dir"
    echo "⚠️ Could not download Firecracker ${FULL_VERSION}, continuing without it"
    echo "   You may need to download manually from:"
    echo "   https://github.com/firecracker-microvm/firecracker/releases"
  fi
}

main() {
  detect_latest_ci_version
  download_firecracker
  download_kernel
  download_and_convert_rootfs

  echo ""
  echo "=== Assets Setup Complete ==="
  echo "Location: $ASSETS_DIR"
  echo " - Kernel: $KERNEL_OUTPUT"
  echo " - Rootfs: ${ROOTFS_OUTPUT_BASE}-*.ext4"
  echo " - Firecracker: bin/firecracker"
  echo " - Jailer: bin/jailer"
}

main "$@"
