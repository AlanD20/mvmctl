#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "=== Downloading vmlinux kernel ==="

if [ -f "vmlinux" ]; then
  echo "Kernel already exists"
  exit 0
fi

echo "Trying official Firecracker vmlinux kernel..."
if curl -sL "https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin" -o "vmlinux"; then
  echo "Downloaded successfully"
  exit 0
fi

echo "Trying alternative AWS S3 location..."
if curl -sL "https://s3.amazonaws.com/spec.ccfc.min/img/unsupported/vmlinux" -o "vmlinux"; then
  echo "Downloaded from alternative AWS S3"
  exit 0
fi

echo "Trying kernel.org..."
KERNEL_VERSION="6.1.128"
if curl -sL "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KERNEL_VERSION}.tar.xz" -o "linux-${KERNEL_VERSION}.tar.xz"; then
  echo "Downloaded kernel source, building..."
  tar -xf "linux-${KERNEL_VERSION}.tar.xz"
  cd "linux-${KERNEL_VERSION}"
  make defconfig
  make -j$(nproc) vmlinux
  mv vmlinux ..
  cd ..
  rm -rf "linux-${KERNEL_VERSION}" "linux-${KERNEL_VERSION}.tar.xz"
  echo "Kernel built successfully"
  exit 0
fi

echo "ERROR: Could not download kernel"
exit 1
