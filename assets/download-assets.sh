#!/bin/bash
set -e

# Shared asset download script for Firecracker setups
# Downloads to ../assets/ directory to be shared across single-vm and multi-vm

# Get the script directory and assets directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS_DIR="$(cd "$SCRIPT_DIR" && pwd)"
cd "$SCRIPT_DIR"

# Check for config.env from calling directory
if [ -f "../single-vm/config.env" ]; then
  source "../single-vm/config.env"
elif [ -f "../multi-vm/config.env" ]; then
  source "../multi-vm/config.env"
else
  echo "WARNING: Could not find config.env in parent directories"
fi

echo "=== Firecracker Assets Setup ==="

# Download Firecracker binary
download_firecracker() {
  echo "[1/2] Checking Firecracker binary..."
  if [ ! -f "bin/firecracker" ]; then
    echo "Downloading Firecracker..."
    if [ "${FIRECRACKER_VERSION:-}" = "" ]; then
      FIRECRACKER_VERSION=$(curl -s https://api.github.com/repos/firecracker-microvm/firecracker/releases/latest | grep -oP '"tag_name":\s*"\K[^"]+' || echo "v1.10.1")
      export FIRECRACKER_VERSION
    fi
    curl -sL "https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/firecracker-${FIRECRACKER_VERSION}-x86_64.tar.gz" | tar xz -C /tmp
    mv /tmp/firecracker-"$FIRECRACKER_VERSION"-x86_64/firecracker bin/
    mv /tmp/firecracker-"$FIRECRACKER_VERSION"-x86_64/jailer bin/
    rm -rf /tmp/firecracker-"$FIRECRACKER_VERSION"-x86_64
    chmod +x bin/firecracker bin/jailer
    echo "Firecracker downloaded to bin/"
  else
    echo "Firecracker already exists"
  fi
}

# Download kernel
download_kernel() {
  echo "[2/2] Checking kernel..."
  if [ ! -f "kernels/vmlinux" ]; then
    echo "Downloading Firecracker kernel..."
    if curl -sL "https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin" -o "kernels/vmlinux"; then
      chmod +x kernels/vmlinux
      echo "Kernel downloaded to kernels/"
    else
      echo "ERROR: Failed to download kernel"
      exit 1
    fi
  else
    echo "Kernel already exists"
  fi
}

download_firecracker
download_kernel

echo ""
echo "=== Assets Setup Complete ==="
echo "Location: ${ASSETS_DIR}"
echo " - bin/firecracker (binary)"
echo " - kernels/vmlinux (kernel)"
