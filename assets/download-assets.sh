#!/bin/bash
set -e

# Shared asset download script for Firecracker setups
# Downloads kernel, firecracker binary, and OS image to assets/ directory

# Get the script directory and assets directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ASSETS_DIR="$(cd "$SCRIPT_DIR" && pwd)"
cd "$SCRIPT_DIR"

# Source the centralized assets config
echo "=== Firecracker Assets Setup ==="
echo "Loading configuration from assets/config.env..."
source config.env

echo "Configuration:"
echo "  Kernel: ${KERNEL_NAME} (version: ${KERNEL_VERSION})"
echo "  OS Image: ${IMAGE_OS} ${IMAGE_VERSION} (${IMAGE_ARCH})"
echo "  Firecracker: ${FIRECRACKER_VERSION}"
echo ""

# Download Firecracker binary
download_firecracker() {
  echo "[1/3] Checking Firecracker binary..."
  if [ ! -f "bin/firecracker" ]; then
    echo "Downloading Firecracker ${FIRECRACKER_VERSION}..."
    curl -sL "https://github.com/firecracker-microvm/firecracker/releases/download/${FIRECRACKER_VERSION}/firecracker-${FIRECRACKER_VERSION}-x86_64.tgz" | tar xz -C /tmp
    mv /tmp/release-"${FIRECRACKER_VERSION}"-x86_64/firecracker-${FIRECRACKER_VERSION}-x86_64 bin/firecracker
    mv /tmp/release-"${FIRECRACKER_VERSION}"-x86_64/jailer-${FIRECRACKER_VERSION}-x86_64 bin/jailer
    rm -rf /tmp/release-"${FIRECRACKER_VERSION}"-x86_64
    chmod +x bin/firecracker bin/jailer
    echo "Firecracker downloaded to bin/"
  else
    echo "Firecracker already exists"
  fi
}

# Download kernel using config from assets/config.env
download_kernel() {
  echo "[2/3] Checking kernel..."
  if [ ! -f "kernels/${KERNEL_NAME}" ]; then
    echo "Downloading kernel ${KERNEL_NAME}..."
    if curl -sL "${KERNEL_URL}" -o "kernels/${KERNEL_NAME}"; then
      chmod +x "kernels/${KERNEL_NAME}"
      echo "Kernel downloaded to kernels/"
    else
      echo "ERROR: Failed to download kernel from ${KERNEL_URL}"
      exit 1
    fi
  else
    echo "Kernel ${KERNEL_NAME} already exists"
  fi
}

# Download OS image using config from assets/config.env
download_image() {
  echo "[3/3] Checking OS image..."
  local IMAGE_FILENAME="${IMAGE_OS}-${IMAGE_VERSION}-server-cloudimg-${IMAGE_ARCH}.img"
  if [ ! -f "images/${IMAGE_FILENAME}" ]; then
    echo "Downloading ${IMAGE_OS} ${IMAGE_VERSION} ${IMAGE_ARCH} image..."
    if curl -sL "${IMAGE_URL}" -o "images/${IMAGE_FILENAME}"; then
      echo "OS image downloaded to images/"
    else
      echo "ERROR: Failed to download image from ${IMAGE_URL}"
      exit 1
    fi
  else
    echo "OS image already exists"
  fi
}

download_firecracker
download_kernel
download_image

echo ""
echo "=== Assets Setup Complete ==="
echo "Location: ${ASSETS_DIR}"
echo " - bin/firecracker (binary)"
echo " - bin/jailer (jailer)"
echo " - kernels/${KERNEL_NAME} (kernel)"
echo " - images/${IMAGE_FILENAME} (OS image)"
