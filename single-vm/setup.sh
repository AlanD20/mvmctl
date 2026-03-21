#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Firecracker Ubuntu Setup ==="
echo "Using kernel: ${KERNEL_NAME} (from assets)"
echo "Using image: ${IMAGE_OS}-${IMAGE_VERSION}-server-cloudimg-${IMAGE_ARCH}.img (from assets)"
echo ""

# Check for shared assets
echo "[1/5] Checking shared assets..."
if [ ! -f "../assets/bin/firecracker" ]; then
  echo "Shared Firecracker not found. Run ../assets/download-assets.sh first"
  exit 1
fi
if [ ! -f "../assets/kernels/${KERNEL_NAME}" ]; then
  echo "Shared kernel '${KERNEL_NAME}' not found. Run ../assets/download-assets.sh first"
  exit 1
fi
if [ ! -f "../assets/images/${IMAGE_OS}-${IMAGE_VERSION}-server-cloudimg-${IMAGE_ARCH}.img" ]; then
  echo "OS image not found. Run ../assets/download-assets.sh first"
  exit 1
fi
echo "All assets present"

echo "[2/5] Checking dependencies..."
for cmd in qemu-img genisoimage curl bc screen; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: $cmd is not installed."
    exit 1
  fi
done

if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM is not available. Please ensure KVM is enabled and you have permissions."
  exit 1
fi
echo "Dependencies and KVM check passed"

echo "[3/5] Preparing rootfs from assets..."
IMAGE_PATH="../assets/images/${IMAGE_OS}-${IMAGE_VERSION}-server-cloudimg-${IMAGE_ARCH}.img"
if [ ! -f "rootfs.ext4" ]; then
  if [ ! -f "$IMAGE_PATH" ]; then
    echo "ERROR: Image file not found at $IMAGE_PATH"
    exit 1
  fi
  echo "Converting image to rootfs..."
  qemu-img convert -f qcow2 -O raw "$IMAGE_PATH" "rootfs.ext4"
  truncate -s "$DISK_SIZE" rootfs.ext4
  resize2fs rootfs.ext4
fi

# Create cloud-init seed
echo "[4/5] Generating cloud-init seed..."
mkdir -p cloud-init
cat >cloud-init/meta-data <<EOF
instance-id: i-$(
  head /dev/urandom | tr -dc A-Za-z0-9 | head -c 12
  echo ''
)
local-hostname: ubuntu-fc
EOF

if [ -f "cloud-init/user-data" ]; then
  genisoimage -output cloudinit.iso -volid cidata -joliet -rock cloud-init/user-data cloud-init/meta-data
else
  genisoimage -output cloudinit.iso -volid cidata -joliet -rock cloud-init/meta-data
fi

# Update firecracker.json with config.env values
echo "[5/5] Updating VM configuration..."
if [ -f "firecracker.json" ]; then
  python3 - <<EOF
import json
with open("firecracker.json", "r") as f:
  config = json.load(f)

# Update values from env variables
config["network-interfaces"][0]["host_dev_name"] = "$TAP_DEV"
config["network-interfaces"][0]["guest_mac"] = "$MAC"
config["network-interfaces"][0]["guest_ip"] = "$GUEST_IP"
config["network-interfaces"][0]["netmask"] = "$MASK"
config["machine-config"]["vcpu_count"] = $VM_VCPU
config["machine-config"]["mem_size_mib"] = $VM_MEM_MIB

with open("firecracker.json", "w") as f:
  json.dump(config, f, indent=2)
EOF
fi

echo ""
echo "=== Setup Complete ==="
echo "Run ./start-vm.sh to start the VM"
echo "Run ./cleanup.sh when done to clean up resources"
