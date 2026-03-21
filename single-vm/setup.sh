#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Firecracker Ubuntu Setup ==="

# Check for shared assets
echo "[1/5] Checking shared assets..."
if [ ! -f "../assets/bin/firecracker" ]; then
  echo "Shared Firecracker not found. Run ../assets/download-assets.sh first"
  exit 1
fi
if [ ! -f "../assets/kernels/vmlinux" ]; then
  echo "Shared kernel not found. Run ../assets/download-assets.sh first"
  exit 1
fi

# Link shared assets locally
ln -sf "../assets/bin/firecracker" firecracker
ln -sf "../assets/bin/jailer" jailer 2>/dev/null || true
ln -sf "../assets/kernels/vmlinux" vmlinux
echo "Shared assets linked"

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

echo "[3/5] Downloading Ubuntu ${UBUNTU_VERSION} cloud image..."
if [ ! -f "ubuntu-${UBUNTU_VERSION}-server-cloudimg-amd64.img" ]; then
  curl -sL "https://cloud-images.ubuntu.com/${UBUNTU_VERSION}/current/${UBUNTU_VERSION}-server-cloudimg-amd64.img" -o "ubuntu-${UBUNTU_VERSION}-server-cloudimg-amd64.img"
fi

echo "[4/5] Preparing rootfs and Cloud-Init..."
if [ ! -f "rootfs.ext4" ]; then
  qemu-img convert -f qcow2 -O raw "ubuntu-${UBUNTU_VERSION}-server-cloudimg-amd64.img" "rootfs.ext4"
  truncate -s "$DISK_SIZE" rootfs.ext4
  resize2fs rootfs.ext4
fi

# Create cloud-init seed
echo "Generating cloud-init seed..."
mkdir -p cloud-init
cat >cloud-init/meta-data <<EOF
instance-id: i-$(
  head /dev/urandom | tr -dc A-Za-z0-9 | head -c 12
  echo ''
)
local-hostname: ubuntu-fc
EOF
genisoimage -output cloudinit.iso -volid cidata -joliet -rock cloud-init/user-data cloud-init/meta-data

# Update firecracker.json with config.env values using Python for robustness
if [ -f "firecracker.json" ]; then
  echo "Updating firecracker.json..."
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

echo "[4/5] Preparing rootfs and Cloud-Init..."
if [ ! -f "rootfs.ext4" ]; then
  qemu-img convert -f qcow2 -O raw "ubuntu-${UBUNTU_VERSION}-server-cloudimg-amd64.img" "rootfs.ext4"
  truncate -s "$DISK_SIZE" rootfs.ext4
  resize2fs rootfs.ext4
fi

# Create cloud-init seed
echo "Generating cloud-init seed..."
mkdir -p cloud-init
cat >cloud-init/meta-data <<EOF
instance-id: i-$(
  head /dev/urandom | tr -dc A-Za-z0-9 | head -c 12
  echo ''
)
local-hostname: ubuntu-fc
EOF
genisoimage -output cloudinit.iso -volid cidata -joliet -rock cloud-init/user-data cloud-init/meta-data

echo ""
echo "=== Setup Complete ==="
echo "Run ./start-vm.sh to start the VM"
echo "Run ./cleanup.sh when done to clean up resources"
