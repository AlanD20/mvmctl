#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Firecracker Ubuntu Setup ==="

echo "[1/5] Checking KVM availability..."
if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM is not available. Please ensure KVM is enabled."
  exit 1
fi
echo "KVM is available"

echo "[2/5] Downloading Firecracker binary..."
if [ ! -f "firecracker" ]; then
  curl -sL "https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/firecracker-${FIRECRACKER_VERSION}-x86_64.tar.gz" | tar xz -C .
  mv firecracker-${FIRECRACKER_VERSION}-x86_64/firecracker .
  mv firecracker-${FIRECRACKER_VERSION}-x86_64/jailer .
  rm -rf firecracker-${FIRECRACKER_VERSION}-x86_64
fi
chmod +x firecracker jailer
echo "Firecracker installed"

echo "[3/5] Downloading Ubuntu ${UBUNTU_VERSION} cloud image..."
if [ ! -f "ubuntu-${UBUNTU_VERSION}-server-cloudimg-amd64.img" ]; then
  curl -sL "https://cloud-images.ubuntu.com/${UBUNTU_VERSION}/current/${UBUNTU_VERSION}-server-cloudimg-amd64.img" -o "ubuntu-${UBUNTU_VERSION}-server-cloudimg-amd64.img"
fi

echo "[4/5] Converting and resizing rootfs..."
if [ ! -f "rootfs.ext4" ]; then
  qemu-img convert -f qcow2 -O raw "ubuntu-${UBUNTU_VERSION}-server-cloudimg-amd64.img" "rootfs.ext4"
  truncate -s "$DISK_SIZE" rootfs.ext4
  e2fsck -f rootfs.ext4 || true
  resize2fs rootfs.ext4
fi

echo "[5/5] Downloading vmlinux kernel..."
if [ ! -f "vmlinux" ]; then
  KERNEL_VERSION="6.1.128"
  curl -sL "https://s3.amazonaws.com/spec.ccfc.min/img/unsupported/vmlinux" -o "vmlinux" 2>/dev/null ||
    curl -sL "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz" -o "linux-${KERNEL_VERSION}.tar.xz"
fi

echo ""
echo "=== Setup Complete ==="
echo "Run ./start-vm.sh to start the VM"
echo "Run ./cleanup.sh when done to clean up resources"
