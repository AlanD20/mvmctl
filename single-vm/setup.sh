#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Firecracker VM Setup (Direct Rootfs + SSH) ==="
echo "Image Source: ${IMAGE_SOURCE:-firecracker-ci}"
echo "Using kernel: ${KERNEL_PATH}"
echo "Using rootfs: ${ROOTFS_PATH}"
echo ""

echo "[1/4] Checking dependencies..."
for cmd in mkisofs curl bc screen; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: $cmd is not installed"
    exit 1
  fi
done
if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM not available"
  exit 1
fi
echo "✓ Dependencies and KVM OK"

echo "[2/4] Checking assets..."
if [ ! -f "$KERNEL_PATH" ]; then
  echo "ERROR: Kernel not found at $KERNEL_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi
if [ ! -f "$ROOTFS_PATH" ]; then
  echo "ERROR: Rootfs not found at $ROOTFS_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi
ROOTFS_SOURCE="$ROOTFS_PATH"
SSH_KEY_SOURCE=$(ls ../assets/keys/id_rsa 2>/dev/null | head -1)
if [ -z "$SSH_KEY_SOURCE" ]; then
  echo "ERROR: SSH key not found at ../assets/keys/id_rsa"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi
# Also check for public key
SSH_PUB_KEY_SOURCE=$(ls ../assets/keys/id_rsa.pub 2>/dev/null | head -1)
if [ -z "$SSH_PUB_KEY_SOURCE" ]; then
  echo "WARNING: SSH public key not found at ../assets/keys/id_rsa.pub"
fi
echo "✓ All assets present"

echo "[3/4] Setting up VM environment..."
mkdir -p "$OUTPUT_DIR"

if [ ! -f "${OUTPUT_DIR}/rootfs.ext4" ]; then
  echo " - Copying rootfs..."
  cp "$ROOTFS_SOURCE" "${OUTPUT_DIR}/rootfs.ext4"
  echo " - Copying SSH key..."
  cp "$SSH_KEY_SOURCE" "${OUTPUT_DIR}/vm.id_rsa"
  chmod 600 "${OUTPUT_DIR}/vm.id_rsa"
fi

echo "✓ Rootfs and SSH key copied"

echo "[4/4] Creating cloud-init..."
cp -r cloud-init "${OUTPUT_DIR}/cloud-init"

# Create meta-data
cat >"${OUTPUT_DIR}/cloud-init/meta-data" <<EOF
instance-id: i-$(
  head /dev/urandom | tr -dc A-Za-z0-9 | head -c 12
  echo ''
)
local-hostname: ${VM_NAME}
EOF

# Create network-config with static IP (matching boot args)
cat >"${OUTPUT_DIR}/cloud-init/network-config" <<EOF
version: 2
ethernets:
  eth0:
    dhcp4: false
    dhcp6: false
    addresses:
      - ${GUEST_IP}/30
    routes:
      - to: default
        via: ${HOST_IP}
    nameservers:
      addresses:
        - 1.1.1.1
        - 8.8.8.8
EOF

# Read the public key content
PUB_KEY_CONTENT=""
if [ -f "$SSH_PUB_KEY_SOURCE" ]; then
  PUB_KEY_CONTENT=$(cat "$SSH_PUB_KEY_SOURCE")
fi

# Create user-data from template or generate default
if [ -f "cloud-init/user-data" ]; then
  echo " - Using custom cloud-init/user-data..."
  # Read the template and inject SSH keys
  sed -e "s|# SSH keys will be injected here by setup.sh from assets/keys/|${PUB_KEY_CONTENT}|g" \
    -e "s|# Root SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
    -e "s|# Ubuntu user SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
    "cloud-init/user-data" >"${OUTPUT_DIR}/cloud-init/user-data"
fi

echo " - Embedding cloud-init into rootfs..."

# Mount rootfs and embed cloud-init files
MOUNT_DIR="${OUTPUT_DIR}/mnt-rootfs"
mkdir -p "$MOUNT_DIR"

# Try to mount (may need sudo)
if mount "${OUTPUT_DIR}/rootfs.ext4" "$MOUNT_DIR" 2>/dev/null || sudo mount "${OUTPUT_DIR}/rootfs.ext4" "$MOUNT_DIR"; then
  # Create cloud-init seed directory structure
  sudo mkdir -p "$MOUNT_DIR/var/lib/cloud/seed/nocloud"
  sudo mkdir -p "$MOUNT_DIR/etc/cloud/cloud.cfg.d"

  # Copy cloud-init files
  sudo cp -t "$MOUNT_DIR/var/lib/cloud/seed/nocloud" "${OUTPUT_DIR}"/cloud-init/*

  # Set proper permissions
  sudo chmod 644 "$MOUNT_DIR"/var/lib/cloud/seed/nocloud/*

  # Unmount
  umount "$MOUNT_DIR" 2>/dev/null || sudo umount "$MOUNT_DIR"
  sudo rmdir "$MOUNT_DIR" 2>/dev/null || true

  echo "✓ Cloud-init embedded into rootfs at /var/lib/cloud/seed/nocloud/"
else
  echo "⚠️ Could not mount rootfs, creating ISO instead..."
  mkisofs -output "${OUTPUT_DIR}/cloudinit.iso" -volid cidata -joliet -rock "${OUTPUT_DIR}/cloud-init/user-data" "${OUTPUT_DIR}/cloud-init/meta-data"
  echo "✓ Cloud-init ISO created"
fi

echo "[5/5] Generating VM configuration..."
# Use absolute paths for Firecracker
ROOTFS_ABS_PATH="${SCRIPT_DIR}/${OUTPUT_DIR}/rootfs.ext4"
KERNEL_ABS_PATH="${SCRIPT_DIR}/${KERNEL_PATH}"

cat >"${OUTPUT_DIR}/firecracker.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "${KERNEL_ABS_PATH}",
    "boot_args": "console=ttyS0 reboot=k panic=1 pci=off ip=${GUEST_IP}::${HOST_IP}:${MASK}::eth0:off rw rootwait ds=nocloud;s=file:///var/lib/cloud/seed/nocloud/",
    "initrd_path": null
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "${ROOTFS_ABS_PATH}",
      "is_root_device": true,
      "is_read_only": false,
      "partuuid": null,
      "cache_type": "Unsafe",
      "io_engine": "Sync",
      "rate_limiter": null,
      "socket": null
    }
  ],
  "network-interfaces": [
    {
      "iface_id": "eth0",
      "guest_mac": "${MAC}",
      "host_dev_name": "${TAP_DEV}"
    }
  ],
  "machine-config": {
    "vcpu_count": ${VM_VCPU},
    "mem_size_mib": ${VM_MEM_MIB},
    "smt": false,
    "cpu_template": null
  },
  "cpu-config": null,
  "balloon": null,
  "vsock": null,
  "logger": {
    "log_path": "${SCRIPT_DIR}/${OUTPUT_DIR}/firecracker.log",
    "level": "debug",
    "show_level": true,
    "show_log_origin": true
  },
  "metrics": {
    "metrics_path": "${OUTPUT_DIR}/firecracker.metrics"
  }
}
EOF

echo "✓ Configuration generated: ${OUTPUT_DIR}/firecracker.json"
echo ""
echo "=========================================="
echo "✓✓✓ Setup Complete! ✓✓✓"
echo "=========================================="
echo ""
echo "VM configuration:"
echo " - Kernel: ${KERNEL_PATH}"
echo " - Rootfs: ${DISK_SIZE} ext4"
echo " - SSH Key: ${OUTPUT_DIR}/vm.id_rsa"
echo " - vCPUs: ${VM_VCPU}"
echo " - Memory: ${VM_MEM_MIB} MiB"
echo " - Network: ${GUEST_IP}/30 via ${TAP_DEV}"
echo ""
echo "Run: ./start-vm.sh"
echo "View: cat ${OUTPUT_DIR}/firecracker.log"
