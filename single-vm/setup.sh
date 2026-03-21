#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

source config.env

echo "=== Firecracker VM Setup (Direct Rootfs + SSH) ==="
echo "Using kernel: kernels/vmlinux (from assets)"
echo "Using rootfs: $(basename "$(ls ../assets/images/ubuntu-*.ext4 | head -1)") (from assets)"
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
if [ ! -f "../assets/kernels/vmlinux" ]; then
  echo "ERROR: Kernel not found at ../assets/kernels/vmlinux"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi
ROOTFS_SOURCE=$(ls ../assets/images/ubuntu-*.ext4 2>/dev/null | head -1)
if [ "$ROOTFS_SOURCE" = "" ]; then
  echo "ERROR: Rootfs not found at ../assets/images/ubuntu-*.ext4"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi
SSH_KEY_SOURCE=$(ls ../assets/keys/id_rsa 2>/dev/null | head -1)
if [ -z "$SSH_KEY_SOURCE" ]; then
  echo "ERROR: SSH key not found at ../assets/keys/id_rsa"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
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
mkdir -p "${OUTPUT_DIR}/cloud-init"
cat >"${OUTPUT_DIR}/cloud-init/meta-data" <<EOF
instance-id: i-$(
  head /dev/urandom | tr -dc A-Za-z0-9 | head -c 12
  echo ''
)
local-hostname: ${VM_NAME}
EOF

# Create user-data with DNS nameservers
cat >"${OUTPUT_DIR}/cloud-init/user-data" <<EOF
#cloud-config
manage_resolv_conf: true
resolv_conf:
  nameservers:
    - '1.1.1.1'
    - '8.8.8.8'
  searchdomains:
    - local
EOF

mkisofs -output "${OUTPUT_DIR}/cloudinit.iso" -volid cidata -joliet -rock "${OUTPUT_DIR}/cloud-init/user-data" "${OUTPUT_DIR}/cloud-init/meta-data"

echo "✓ Cloud-init created"

echo "[5/5] Generating VM configuration..."
cat >"${OUTPUT_DIR}/firecracker.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "../assets/kernels/vmlinux",
    "boot_args": "console=ttyS0 noapic reboot=k panic=1 pci=off ip=${GUEST_IP}::${HOST_IP}:${MASK}::eth0:off root=/dev/vda rw",
    "initrd_path": null
  },
  "drives": [
    {
      "drive_id": "rootfs",
      "path_on_host": "${OUTPUT_DIR}/rootfs.ext4",
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
      "path_on_host": "${OUTPUT_DIR}/cloudinit.iso",
      "is_root_device": false,
      "is_read_only": true
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
    "log_path": "${OUTPUT_DIR}/firecracker.log",
    "level": "Info",
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
echo " - Kernel: ../assets/kernels/vmlinux"
echo " - Rootfs: ${DISK_SIZE} ext4 with SSH"
echo " - SSH Key: ${OUTPUT_DIR}/vm.id_rsa"
echo " - vCPUs: ${VM_VCPU}"
echo " - Memory: ${VM_MEM_MIB} MiB"
echo " - Network: ${GUEST_IP}/30 via ${TAP_DEV}"
echo ""
echo "Run: ./start-vm.sh"
echo "View: cat ${OUTPUT_DIR}/firecracker.log"
