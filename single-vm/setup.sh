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
for cmd in qemu-img mkisofs curl bc screen; do
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
  mkisofs -output cloudinit.iso -volid cidata -joliet -rock cloud-init/user-data cloud-init/meta-data
else
  mkisofs -output cloudinit.iso -volid cidata -joliet -rock cloud-init/meta-data
fi

# Generate firecracker.json with dynamic paths
echo "[5/5] Generating VM configuration..."
cat >firecracker.json <<EOF
{
  "boot-source": {
    "kernel_image_path": "../assets/kernels/${KERNEL_NAME}",
    "boot_args": "ro console=ttyS0 noapic reboot=k panic=1 pci=off ip=${GUEST_IP}::${HOST_IP}:${MASK}::eth0:off",
    "initrd_path": null
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "rootfs.ext4",
      "is_root_device": true,
      "is_read_only": false,
      "partuuid": null,
      "cache_type": "Unsafe",
      "io_engine": "Sync",
      "rate_limiter": null,
      "socket": null
    },
    {
      "drive_id": "cloudinit",
      "path_on_host": "cloudinit.iso",
      "is_root_device": false,
      "is_read_only": true
    }
  ],
  "network-interfaces": [
    {
      "iface_id": "eth0",
      "guest_mac": "${MAC}"
    }
  ],
  "machine-config": {
    "vcpu_count": ${VM_VCPU},
    "mem_size_mib": ${VM_MEM_MIB},
    "ht_enabled": false,
    "cpu_template": null
  },
  "cpu-config": null,
  "balloon": null,
  "vsock": null,
  "logger": {
    "log_path": "./firecracker.log",
    "level": "Info",
    "show_level": true,
    "show_log_origin": true
  },
  "metrics": {
    "metrics_path": "./firecracker.metrics"
  }
}
EOF
echo "Configuration generated (firecracker.json)"

echo ""
echo "=== Setup Complete ==="
echo "Run ./start-vm.sh to start the VM"
echo "Run ./cleanup.sh when done to clean up resources"
