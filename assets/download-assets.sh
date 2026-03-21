#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS_DIR="$(cd "$SCRIPT_DIR" && pwd)"
cd "$SCRIPT_DIR"

echo "=== Firecracker CI Assets Setup ==="

ARCH="x86_64"
OUTPUT_DIR="."
KERNEL_OUTPUT="kernels/vmlinux"
ROOTFS_OUTPUT_BASE="images/ubuntu"
KEYS_DIR="keys"
SSH_KEY_NAME="id_rsa"

detect_latest_ci_version() {
  echo "[1/4] Detecting latest Firecracker CI version..."
  local release_url="https://github.com/firecracker-microvm/firecracker/releases"
  local latest_version=$(basename $(curl -fsSLI -o /dev/null -w %{url_effective} ${release_url}/latest))
  CI_VERSION=${latest_version%.*}
  echo "✓ Latest CI version: $CI_VERSION"
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

  if [ -f "$rootfs_output" ] && [ -f "$ASSETS_DIR/${KEYS_DIR}/${SSH_KEY_NAME}" ]; then
    echo "✓ Rootfs and SSH key already exist for Ubuntu $ubuntu_version"
    return
  fi

  mkdir -p "$(dirname "$rootfs_output")"

  echo " - Downloading Ubuntu $ubuntu_version rootfs..."
  wget -q -O "$squashfs_file" "https://s3.amazonaws.com/spec.ccfc.min/$latest_ubuntu_key"

  echo " - Extracting squashfs..."
  local temp_dir=$(mktemp -d)
  cd "$temp_dir"
  unsquashfs "$squashfs_file" >/dev/null

  echo " - Generating SSH key..."
  mkdir -p "$ASSETS_DIR/${KEYS_DIR}"
  ssh-keygen -f "$ASSETS_DIR/${KEYS_DIR}/${SSH_KEY_NAME}" -N "" -q
  mkdir -p squashfs-root/root/.ssh
  # Add all public keys from assets/keys to authorized_keys
  for pub_key in "$ASSETS_DIR/${KEYS_DIR}"/*.pub; do
    if [ -f "$pub_key" ]; then
      cat "$pub_key" >>squashfs-root/root/.ssh/authorized_keys
      echo "   Added $(basename "$pub_key") to authorized_keys"
    fi
  done
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

  local download_url="https://github.com/firecracker-microvm/firecracker/releases/download/${CI_VERSION}/firecracker-${CI_VERSION}-x86_64.tgz"
  local temp_dir=$(mktemp -d)

  echo " - Downloading Firecracker $CI_VERSION..."
  if curl -sL "$download_url" | tar xz -C "$temp_dir"; then
    mv "$temp_dir"/release-*/firecracker-*/firecracker-* bin/firecracker
    mv "$temp_dir"/release-*/firecracker-*/jailer-* bin/jailer
    chmod +x bin/firecracker bin/jailer
    rm -rf "$temp_dir"
    echo "✓ Firecracker downloaded"
  else
    rm -rf "$temp_dir"
    echo "⚠️  Could not download Firecracker $CI_VERSION, continuing without it"
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
  echo " - SSH Key: ${ROOTFS_OUTPUT_BASE}-*.id_rsa"
  echo " - Firecracker: bin/firecracker (if downloaded)"
}

main "$@"
