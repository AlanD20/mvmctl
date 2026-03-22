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

# =============================================================================
# STEP 1: Check Dependencies
# =============================================================================
echo "[1/4] Checking dependencies..."

# Required commands for setup
declare -a REQUIRED_CMDS=("mkisofs" "mount" "umount" "sudo" "ip" "iptables")
for cmd in "${REQUIRED_CMDS[@]}"; do
  if ! command -v "$cmd" &>/dev/null; then
    echo "ERROR: Required command '$cmd' is not installed"
    exit 1
  fi
done

# Check KVM is available
if [ ! -c /dev/kvm ]; then
  echo "ERROR: KVM not available (/dev/kvm not found)"
  exit 1
fi

echo "✓ All dependencies and KVM OK"

# =============================================================================
# STEP 2: Check Assets
# =============================================================================
echo "[2/4] Checking assets..."

# Check kernel exists
if [ ! -f "$KERNEL_PATH" ]; then
  echo "ERROR: Kernel not found at $KERNEL_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

# Check rootfs exists
if [ ! -f "$ROOTFS_PATH" ]; then
  echo "ERROR: Rootfs not found at $ROOTFS_PATH"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi
ROOTFS_SOURCE="$ROOTFS_PATH"

# Check SSH key exists
SSH_KEY_SOURCE=$(ls ../assets/keys/id_rsa 2>/dev/null | head -1)
if [ -z "$SSH_KEY_SOURCE" ]; then
  echo "ERROR: SSH key not found at ../assets/keys/id_rsa"
  echo "Run '../assets/download-assets.sh' first"
  exit 1
fi

# Check public key (optional but recommended)
SSH_PUB_KEY_SOURCE=$(ls ../assets/keys/id_rsa.pub 2>/dev/null | head -1)
if [ -z "$SSH_PUB_KEY_SOURCE" ]; then
  echo "WARNING: SSH public key not found at ../assets/keys/id_rsa.pub"
fi

echo "✓ All assets present"

# =============================================================================
# STEP 3: Setup VM Environment
# =============================================================================
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

# Check if rootfs is a partitioned disk (not a raw filesystem)
if file "${OUTPUT_DIR}/rootfs.ext4" 2>/dev/null | grep -qE "DOS/MBR|partition|boot sector"; then
  echo ""
  echo "⚠️  WARNING: Rootfs appears to be a partitioned disk image, not a raw filesystem."
  echo "    This will cause a kernel panic during boot (VFS: Unable to mount root fs)."
  echo ""
  echo "    SOLUTION - Choose one:"
  echo ""
  echo "    Option 1: Extract the root partition (recommended):"
  echo "      rm -rf ${OUTPUT_DIR}"
  echo "      cd ../assets/images"
  echo "      sudo kpartx -av arch.raw"
  echo "      sudo dd if=/dev/mapper/loop0p1 of=arch.ext4 bs=4M"
  echo "      sudo kpartx -dv arch.raw"
  echo "      cd ../../single-vm"
  echo "      sudo ./setup.sh"
  echo ""
  echo "    Option 2: Use partitioned image with boot args:"
  echo "      Edit ${OUTPUT_DIR}/firecracker.json after setup completes"
  echo "      Change: \"root=/dev/vda\" to \"root=/dev/vda1\""
  echo ""
  echo "    See custom-images.md for detailed instructions."
  echo ""
fi

# =============================================================================
# STEP 4: Create Cloud-Init
# =============================================================================
echo "[4/4] Creating cloud-init..."
cp -r cloud-init "${OUTPUT_DIR}"

# Create meta-data with random instance ID
INSTANCE_ID="i-$(head /dev/urandom | tr -dc A-Za-z0-9 | head -c 12)"
cat >"${OUTPUT_DIR}/cloud-init/meta-data" <<EOF
instance-id: ${INSTANCE_ID}
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
  # Read the template and inject SSH keys and hostname
  sed -e "s|# SSH keys will be injected here by setup.sh from assets/keys/|${PUB_KEY_CONTENT}|g" \
    -e "s|# Root SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
    -e "s|# User SSH keys will be injected here by setup.sh|${PUB_KEY_CONTENT}|g" \
    -e "s|HOSTNAME_PLACEHOLDER|${VM_NAME}|g" \
    "cloud-init/user-data" >"${OUTPUT_DIR}/cloud-init/user-data"
fi

echo " - Embedding cloud-init into rootfs..."

# Mount rootfs and embed cloud-init files
MOUNT_DIR="${OUTPUT_DIR}/mnt-rootfs"
mkdir -p "$MOUNT_DIR"

# Try to mount (may need sudo)
if sudo mount "${OUTPUT_DIR}/rootfs.ext4" "$MOUNT_DIR" 2>/dev/null; then
  # Create cloud-init seed directory structure
  sudo mkdir -p "$MOUNT_DIR/var/lib/cloud/seed/nocloud"
  sudo mkdir -p "$MOUNT_DIR/etc/cloud/cloud.cfg.d"

  # Copy cloud-init files
  sudo cp -r "${OUTPUT_DIR}/cloud-init/"* "$MOUNT_DIR/var/lib/cloud/seed/nocloud/"

  # Set proper permissions
  sudo chmod 644 "$MOUNT_DIR"/var/lib/cloud/seed/nocloud/*

  # Unmount
  sudo umount "$MOUNT_DIR"
  sudo rmdir "$MOUNT_DIR" 2>/dev/null || true

  echo "✓ Cloud-init embedded into rootfs at /var/lib/cloud/seed/nocloud/"
else
  echo "⚠️ Could not mount rootfs, creating ISO instead..."
  mkisofs -output "${OUTPUT_DIR}/cloudinit.iso" -volid cidata -joliet -rock "${OUTPUT_DIR}/cloud-init/user-data" "${OUTPUT_DIR}/cloud-init/meta-data"
  echo "✓ Cloud-init ISO created"
fi

# =============================================================================
# STEP 5: Setup Network
# =============================================================================
echo "[5/6] Setting up network..."

# Check global IP forwarding
if [ "$(sysctl -n net.ipv4.ip_forward)" != "1" ]; then
  echo "ERROR: Global IP forwarding is not enabled."
  echo "Run ../environment_setup.sh first"
  exit 1
fi

# Remove existing tap if present
if ip link show "$TAP_DEV" &>/dev/null; then
  echo " - Tap device $TAP_DEV already exists, removing..."
  sudo ip link del "$TAP_DEV" 2>/dev/null || true
fi

echo " - Creating tap device $TAP_DEV..."
sudo ip tuntap add dev "$TAP_DEV" mode tap
sudo ip link set dev "$TAP_DEV" up

echo " - Configuring IP addresses..."
sudo ip addr add "${HOST_IP}/30" dev "$TAP_DEV"

echo " - Enabling proxy ARP..."
sudo sysctl -w net.ipv4.conf."$TAP_DEV".proxy_arp=1 >/dev/null

echo " - Setting up NAT..."
DEFAULT_IFACE=$(ip route | grep default | awk '{print $5}' | head -n1)
if [ -z "$DEFAULT_IFACE" ]; then
  echo "ERROR: Could not detect default network interface."
  exit 1
fi
sudo iptables -t nat -A POSTROUTING -o "$DEFAULT_IFACE" -j MASQUERADE
sudo iptables -A FORWARD -i "$TAP_DEV" -o "$DEFAULT_IFACE" -j ACCEPT
sudo iptables -A FORWARD -i "$DEFAULT_IFACE" -o "$TAP_DEV" -j ACCEPT

echo "✓ Network configured"

# =============================================================================
# STEP 6: Generate VM Configuration
# =============================================================================
echo "[6/6] Generating VM configuration..."
# Use absolute paths for Firecracker
ROOTFS_ABS_PATH="${SCRIPT_DIR}/${OUTPUT_DIR}/rootfs.ext4"
KERNEL_ABS_PATH="${SCRIPT_DIR}/${KERNEL_PATH}"

# Detect filesystem type for proper boot args
ROOTFS_TYPE="ext4"
if command -v file &>/dev/null; then
  FS_INFO=$(file -b "$ROOTFS_ABS_PATH" 2>/dev/null || echo "")
  if echo "$FS_INFO" | grep -qi "btrfs"; then
    ROOTFS_TYPE="btrfs"
  elif echo "$FS_INFO" | grep -qi "xfs"; then
    ROOTFS_TYPE="xfs"
  elif echo "$FS_INFO" | grep -qi "ext2"; then
    ROOTFS_TYPE="ext2"
  elif echo "$FS_INFO" | grep -qi "ext3"; then
    ROOTFS_TYPE="ext3"
  fi
fi

echo " - Detected filesystem type: $ROOTFS_TYPE"

LSM_ENABLED=true
LSM_FLAGS="landlock,lockdown,yama,integrity,selinux,bpf"

# Build boot args with appropriate root filesystem type
BOOT_ARGS="console=ttyS0 reboot=k panic=1 ip=${GUEST_IP}::${HOST_IP}:${MASK}::eth0:off root=/dev/vda rw rootwait rootfstype=${ROOTFS_TYPE} ds=nocloud;s=file:///var/lib/cloud/seed/nocloud/ lsm=$LSM_FLAGS"

cat >"${OUTPUT_DIR}/firecracker.json" <<EOF
{
  "boot-source": {
    "kernel_image_path": "${KERNEL_ABS_PATH}",
    "boot_args": "${BOOT_ARGS}",
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
echo "Run: ./create-vm.sh"
echo "View: cat ${OUTPUT_DIR}/firecracker.log"
